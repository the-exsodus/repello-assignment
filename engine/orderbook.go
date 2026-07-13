package engine

import (
	"container/list"
	"errors"
	"fmt"
	"sort"
	"time"
)

var (
	ErrInvalidOrder          = errors.New("invalid order")
	ErrInsufficientLiquidity = errors.New("insufficient liquidity")
	ErrOrderNotFound         = errors.New("order not found")
	ErrAlreadyFilled         = errors.New("order already filled or cancelled")
)

// priceLevel holds every resting order at one price, in strict arrival
// (FIFO) order. qty is the sum of Remaining() across the level, cached so
// that order book depth snapshots don't need to walk the list.
type priceLevel struct {
	price  int64
	orders *list.List // element.Value is *Order
	qty    int64
}

func newPriceLevel(price int64) *priceLevel {
	return &priceLevel{price: price, orders: list.New()}
}

// orderRef lets Cancel go straight from an order ID to its list node and
// price level in O(1), without scanning the book.
type orderRef struct {
	level *priceLevel
	elem  *list.Element
	side  Side
}

// OrderBook is the matching engine state for a single symbol.
//
// It is deliberately NOT safe for concurrent use on its own: correctness
// comes from every method being called only from the single goroutine that
// owns this book (see SymbolActor in actor.go), so no locks are needed here
// at all.
//
// Price levels are kept in slices sorted ascending by price. Both bids and
// asks use the same ascending order; the "best" side just differs (asks
// read from the front, bids read from the back), which lets one pair of
// insert/remove helpers serve both sides. Finding a level is O(log n) via
// binary search; inserting/removing a level shifts the slice, which is
// O(n) worst case but touches contiguous memory and is fast in practice
// for the price-level counts a matching engine actually sees. A skip list
// or B-tree would give O(log n) inserts too, at the cost of pointer-chasing
// and more code -- a reasonable upgrade if book depth grows very large.
type OrderBook struct {
	symbol string

	bidPrices []int64 // ascending; best bid = bidPrices[len-1]
	askPrices []int64 // ascending; best ask = askPrices[0]
	bidLevels map[int64]*priceLevel
	askLevels map[int64]*priceLevel

	active  map[string]*orderRef // orders still live in the book (for O(1) cancel)
	history map[string]*Order    // every order ever submitted (for status lookups)

	nextSeq uint64
}

func NewOrderBook(symbol string) *OrderBook {
	return &OrderBook{
		symbol:    symbol,
		bidLevels: make(map[int64]*priceLevel),
		askLevels: make(map[int64]*priceLevel),
		active:    make(map[string]*orderRef),
		history:   make(map[string]*Order),
	}
}

// Submit validates, matches, and (if anything remains) books the incoming
// order. It returns every trade generated. Errors are returned instead of
// panicking so the caller (the HTTP layer, via the actor) can map them to
// the right HTTP status code.
func (ob *OrderBook) Submit(o *Order) ([]Trade, error) {
	if err := validate(o); err != nil {
		return nil, err
	}

	ob.nextSeq++
	o.seq = ob.nextSeq
	if o.Timestamp == 0 {
		o.Timestamp = nowMillis()
	}

	// Market orders must execute fully or not at all -- never partially
	// execute and then reject. That means we must check aggregate
	// liquidity on the opposite side BEFORE generating any trades.
	if o.Type == Market {
		if !ob.hasSufficientLiquidity(o.Side, o.Quantity) {
			o.Status = StatusRejected
			ob.history[o.ID] = o
			return nil, ErrInsufficientLiquidity
		}
	}

	trades := ob.match(o)

	switch {
	case o.Remaining() == 0:
		o.Status = StatusFilled
	case o.Type == Market:
		// Unreachable given the liquidity pre-check above; guarded
		// defensively in case match() ever changes.
		o.Status = StatusRejected
		ob.history[o.ID] = o
		return trades, ErrInsufficientLiquidity
	case o.Filled > 0:
		o.Status = StatusPartialFill
		ob.addToBook(o)
	default:
		o.Status = StatusAccepted
		ob.addToBook(o)
	}

	ob.history[o.ID] = o
	return trades, nil
}

// hasSufficientLiquidity sums resting quantity on the opposite side of a
// market order and checks it covers the requested quantity.
func (ob *OrderBook) hasSufficientLiquidity(side Side, qty int64) bool {
	levels := ob.askLevels
	if side == Sell {
		levels = ob.bidLevels
	}
	var total int64
	for _, lvl := range levels {
		total += lvl.qty
		if total >= qty {
			return true
		}
	}
	return false
}

// match walks the opposite side of the book from best price outward,
// consuming FIFO at each price level, until the incoming order is filled or
// no more compatible resting orders remain.
func (ob *OrderBook) match(incoming *Order) []Trade {
	var trades []Trade

	if incoming.Side == Buy {
		for incoming.Remaining() > 0 && len(ob.askPrices) > 0 {
			bestAsk := ob.askPrices[0]
			if incoming.Type == Limit && incoming.Price < bestAsk {
				break // best ask too expensive for this limit buy
			}
			level := ob.askLevels[bestAsk]
			trades = append(trades, ob.matchLevel(incoming, level)...)
			if level.orders.Len() == 0 {
				delete(ob.askLevels, bestAsk)
				ob.askPrices = removeSortedPrice(ob.askPrices, bestAsk)
			}
		}
		return trades
	}

	for incoming.Remaining() > 0 && len(ob.bidPrices) > 0 {
		bestBid := ob.bidPrices[len(ob.bidPrices)-1]
		if incoming.Type == Limit && incoming.Price > bestBid {
			break // best bid too low for this limit sell
		}
		level := ob.bidLevels[bestBid]
		trades = append(trades, ob.matchLevel(incoming, level)...)
		if level.orders.Len() == 0 {
			delete(ob.bidLevels, bestBid)
			ob.bidPrices = removeSortedPrice(ob.bidPrices, bestBid)
		}
	}
	return trades
}

// matchLevel consumes resting orders at a single price level in FIFO order
// against the incoming order, executing every trade at the RESTING order's
// price (the resting order is the "maker"; the incoming order is the
// "taker" and accepts the maker's price).
func (ob *OrderBook) matchLevel(incoming *Order, level *priceLevel) []Trade {
	var trades []Trade

	for incoming.Remaining() > 0 {
		elem := level.orders.Front()
		if elem == nil {
			break
		}
		resting := elem.Value.(*Order)

		qty := min64(incoming.Remaining(), resting.Remaining())
		incoming.Filled += qty
		resting.Filled += qty
		level.qty -= qty

		trade := Trade{
			ID:        "trade-" + newID(),
			Symbol:    ob.symbol,
			Price:     level.price,
			Quantity:  qty,
			Timestamp: nowMillis(),
			TakerSide: incoming.Side,
		}
		if incoming.Side == Buy {
			trade.BuyOrderID, trade.SellOrderID = incoming.ID, resting.ID
		} else {
			trade.BuyOrderID, trade.SellOrderID = resting.ID, incoming.ID
		}
		trades = append(trades, trade)

		if resting.Remaining() == 0 {
			resting.Status = StatusFilled
			level.orders.Remove(elem)
			delete(ob.active, resting.ID)
		} else {
			resting.Status = StatusPartialFill
		}
	}
	return trades
}

// addToBook appends the order's remaining quantity as a new resting order,
// creating the price level if this is the first order at that price.
func (ob *OrderBook) addToBook(o *Order) {
	if o.Side == Buy {
		level, ok := ob.bidLevels[o.Price]
		if !ok {
			level = newPriceLevel(o.Price)
			ob.bidLevels[o.Price] = level
			ob.bidPrices = insertSortedPrice(ob.bidPrices, o.Price)
		}
		elem := level.orders.PushBack(o)
		level.qty += o.Remaining()
		ob.active[o.ID] = &orderRef{level: level, elem: elem, side: Buy}
		return
	}
	level, ok := ob.askLevels[o.Price]
	if !ok {
		level = newPriceLevel(o.Price)
		ob.askLevels[o.Price] = level
		ob.askPrices = insertSortedPrice(ob.askPrices, o.Price)
	}
	elem := level.orders.PushBack(o)
	level.qty += o.Remaining()
	ob.active[o.ID] = &orderRef{level: level, elem: elem, side: Sell}
}

// Cancel removes a resting order from the book. Synchronous by construction:
// since the actor serializes all commands through one goroutine, by the
// time Cancel returns the order is guaranteed unable to match any future
// incoming order.
func (ob *OrderBook) Cancel(orderID string) error {
	ref, ok := ob.active[orderID]
	if !ok {
		if o, seen := ob.history[orderID]; seen {
			return fmt.Errorf("%w: order %s already %s", ErrAlreadyFilled, orderID, o.Status)
		}
		return ErrOrderNotFound
	}

	order := ref.elem.Value.(*Order)
	ref.level.qty -= order.Remaining()
	ref.level.orders.Remove(ref.elem)
	delete(ob.active, orderID)
	order.Status = StatusCancelled

	if ref.level.orders.Len() == 0 {
		if ref.side == Buy {
			delete(ob.bidLevels, ref.level.price)
			ob.bidPrices = removeSortedPrice(ob.bidPrices, ref.level.price)
		} else {
			delete(ob.askLevels, ref.level.price)
			ob.askPrices = removeSortedPrice(ob.askPrices, ref.level.price)
		}
	}
	return nil
}

// GetOrder returns the current state of any order ever submitted to this
// book, active or not.
func (ob *OrderBook) GetOrder(orderID string) (*Order, bool) {
	o, ok := ob.history[orderID]
	return o, ok
}

// Snapshot returns aggregated depth (price + total quantity) for both
// sides, best price first, limited to depth levels per side.
func (ob *OrderBook) Snapshot(depth int) (bids, asks []PriceLevelView) {
	bids = make([]PriceLevelView, 0, depth)
	for i := len(ob.bidPrices) - 1; i >= 0 && len(bids) < depth; i-- {
		p := ob.bidPrices[i]
		bids = append(bids, PriceLevelView{Price: p, Quantity: ob.bidLevels[p].qty})
	}
	asks = make([]PriceLevelView, 0, depth)
	for i := 0; i < len(ob.askPrices) && len(asks) < depth; i++ {
		p := ob.askPrices[i]
		asks = append(asks, PriceLevelView{Price: p, Quantity: ob.askLevels[p].qty})
	}
	return bids, asks
}

// BestBidAsk returns the current top of book. ok is false for a side with
// no resting orders.
func (ob *OrderBook) BestBidAsk() (bid int64, bidOK bool, ask int64, askOK bool) {
	if len(ob.bidPrices) > 0 {
		bid, bidOK = ob.bidPrices[len(ob.bidPrices)-1], true
	}
	if len(ob.askPrices) > 0 {
		ask, askOK = ob.askPrices[0], true
	}
	return
}

func validate(o *Order) error {
	if o.Quantity <= 0 {
		return fmt.Errorf("%w: quantity must be positive", ErrInvalidOrder)
	}
	if o.Side != Buy && o.Side != Sell {
		return fmt.Errorf("%w: side must be BUY or SELL", ErrInvalidOrder)
	}
	if o.Type != Limit && o.Type != Market {
		return fmt.Errorf("%w: type must be LIMIT or MARKET", ErrInvalidOrder)
	}
	if o.Type == Limit && o.Price <= 0 {
		return fmt.Errorf("%w: limit order price must be positive", ErrInvalidOrder)
	}
	if o.Type == Market && o.Price != 0 {
		return fmt.Errorf("%w: market order must not specify a price", ErrInvalidOrder)
	}
	return nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func nowMillis() int64 { return time.Now().UnixMilli() }

// insertSortedPrice inserts price into an ascending-sorted slice, keeping it
// sorted, and returns the (possibly reallocated) slice.
func insertSortedPrice(prices []int64, price int64) []int64 {
	i := sort.Search(len(prices), func(i int) bool { return prices[i] >= price })
	prices = append(prices, 0)
	copy(prices[i+1:], prices[i:])
	prices[i] = price
	return prices
}

// removeSortedPrice removes price from an ascending-sorted slice, if present.
func removeSortedPrice(prices []int64, price int64) []int64 {
	i := sort.Search(len(prices), func(i int) bool { return prices[i] >= price })
	if i < len(prices) && prices[i] == price {
		prices = append(prices[:i], prices[i+1:]...)
	}
	return prices
}
