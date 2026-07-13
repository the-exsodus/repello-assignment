package engine

import "testing"

func newLimit(side Side, price, qty int64) *Order {
	return &Order{ID: newID(), Symbol: "TEST", Side: side, Type: Limit, Price: price, Quantity: qty}
}

func newMarket(side Side, qty int64) *Order {
	return &Order{ID: newID(), Symbol: "TEST", Side: side, Type: Market, Quantity: qty}
}

// Example 1 from the spec: a buy at the resting ask's exact price fully
// consumes it.
func TestSimpleFullMatch(t *testing.T) {
	ob := NewOrderBook("TEST")
	mustSubmit(t, ob, newLimit(Sell, 15050, 1000))

	trades, err := ob.Submit(newLimit(Buy, 15050, 500))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(trades) != 1 || trades[0].Quantity != 500 || trades[0].Price != 15050 {
		t.Fatalf("unexpected trades: %+v", trades)
	}

	_, asks := ob.Snapshot(10)
	if len(asks) != 1 || asks[0].Quantity != 500 {
		t.Fatalf("expected 500 remaining on resting ask, got %+v", asks)
	}
}

// Example 2: an incoming order walks multiple price levels, partially
// filling and then resting the remainder.
func TestWalkingTheBook(t *testing.T) {
	ob := NewOrderBook("TEST")
	mustSubmit(t, ob, newLimit(Sell, 15050, 300))
	mustSubmit(t, ob, newLimit(Sell, 15052, 400))
	mustSubmit(t, ob, newLimit(Sell, 15055, 600))

	trades, err := ob.Submit(newLimit(Buy, 15053, 800))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(trades) != 2 {
		t.Fatalf("expected 2 trades, got %d: %+v", len(trades), trades)
	}
	if trades[0].Price != 15050 || trades[0].Quantity != 300 {
		t.Errorf("trade 1 mismatch: %+v", trades[0])
	}
	if trades[1].Price != 15052 || trades[1].Quantity != 400 {
		t.Errorf("trade 2 mismatch: %+v", trades[1])
	}

	bids, asks := ob.Snapshot(10)
	if len(asks) != 1 || asks[0].Price != 15055 || asks[0].Quantity != 600 {
		t.Errorf("expected only the 15055 ask left untouched, got %+v", asks)
	}
	if len(bids) != 1 || bids[0].Price != 15053 || bids[0].Quantity != 100 {
		t.Errorf("expected 100 remaining resting at 15053, got %+v", bids)
	}
}

// Example 3: equal-priced resting orders match in strict arrival (FIFO) order.
func TestTimePriorityFIFO(t *testing.T) {
	ob := NewOrderBook("TEST")
	first := newLimit(Sell, 15050, 200)
	second := newLimit(Sell, 15050, 300)
	third := newLimit(Sell, 15050, 400)
	mustSubmit(t, ob, first)
	mustSubmit(t, ob, second)
	mustSubmit(t, ob, third)

	trades, err := ob.Submit(newLimit(Buy, 15050, 500))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(trades))
	}
	if trades[0].SellOrderID != first.ID || trades[0].Quantity != 200 {
		t.Errorf("expected first trade against the oldest order (200), got %+v", trades[0])
	}
	if trades[1].SellOrderID != second.ID || trades[1].Quantity != 300 {
		t.Errorf("expected second trade against the second order (300), got %+v", trades[1])
	}

	_, asks := ob.Snapshot(10)
	if len(asks) != 1 || asks[0].Quantity != 400 {
		t.Errorf("third order should be untouched, got %+v", asks)
	}
}

// Example 4: a market order walks the book across multiple price levels.
func TestMarketOrderExecution(t *testing.T) {
	ob := NewOrderBook("TEST")
	mustSubmit(t, ob, newLimit(Sell, 15050, 200))
	mustSubmit(t, ob, newLimit(Sell, 15052, 300))
	mustSubmit(t, ob, newLimit(Sell, 15055, 400))

	trades, err := ob.Submit(newMarket(Buy, 600))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(trades) != 3 {
		t.Fatalf("expected 3 trades, got %d: %+v", len(trades), trades)
	}
	wantPrices := []int64{15050, 15052, 15055}
	wantQtys := []int64{200, 300, 100}
	for i, tr := range trades {
		if tr.Price != wantPrices[i] || tr.Quantity != wantQtys[i] {
			t.Errorf("trade %d = {price:%d qty:%d}, want {price:%d qty:%d}", i, tr.Price, tr.Quantity, wantPrices[i], wantQtys[i])
		}
	}
}

// Example 5: a market order that can't be fully filled is rejected outright,
// with zero trades -- not partially executed.
func TestMarketOrderInsufficientLiquidity(t *testing.T) {
	ob := NewOrderBook("TEST")
	mustSubmit(t, ob, newLimit(Sell, 15050, 100))

	trades, err := ob.Submit(newMarket(Buy, 500))
	if err == nil {
		t.Fatal("expected an error for insufficient liquidity")
	}
	if len(trades) != 0 {
		t.Fatalf("expected zero trades on rejection, got %d", len(trades))
	}

	_, asks := ob.Snapshot(10)
	if len(asks) != 1 || asks[0].Quantity != 100 {
		t.Errorf("resting ask should be untouched after rejection, got %+v", asks)
	}
}

func TestCancelRemovesOrderAndFreesPriceLevel(t *testing.T) {
	ob := NewOrderBook("TEST")
	o := newLimit(Buy, 15045, 500)
	mustSubmit(t, ob, o)

	if err := ob.Cancel(o.ID); err != nil {
		t.Fatalf("unexpected error cancelling: %v", err)
	}

	bids, _ := ob.Snapshot(10)
	if len(bids) != 0 {
		t.Errorf("expected no bids after cancel, got %+v", bids)
	}

	got, ok := ob.GetOrder(o.ID)
	if !ok || got.Status != StatusCancelled {
		t.Errorf("expected order to be recorded as CANCELLED, got %+v ok=%v", got, ok)
	}

	if err := ob.Cancel(o.ID); err == nil {
		t.Error("expected an error cancelling an already-cancelled order")
	}
}

func TestCancelUnknownOrderReturnsNotFound(t *testing.T) {
	ob := NewOrderBook("TEST")
	if err := ob.Cancel("does-not-exist"); err != ErrOrderNotFound {
		t.Errorf("expected ErrOrderNotFound, got %v", err)
	}
}

func TestCannotCancelAlreadyFilledOrder(t *testing.T) {
	ob := NewOrderBook("TEST")
	resting := newLimit(Sell, 15050, 100)
	mustSubmit(t, ob, resting)
	mustSubmit(t, ob, newLimit(Buy, 15050, 100)) // fully fills `resting`

	if err := ob.Cancel(resting.ID); err == nil {
		t.Error("expected an error cancelling a fully filled order")
	}
}

func TestPartialFillOrderKeepsTimePriorityInBook(t *testing.T) {
	ob := NewOrderBook("TEST")
	resting := newLimit(Sell, 15050, 1000)
	mustSubmit(t, ob, resting)

	mustSubmit(t, ob, newLimit(Buy, 15050, 300))

	got, ok := ob.GetOrder(resting.ID)
	if !ok {
		t.Fatal("expected resting order to still exist in history")
	}
	if got.Status != StatusPartialFill || got.Filled != 300 || got.Remaining() != 700 {
		t.Errorf("unexpected resting order state: %+v", got)
	}

	// A second incoming buy at the same price should match the SAME
	// resting order next (it kept its original queue position), not skip
	// to a new one.
	trades, err := ob.Submit(newLimit(Buy, 15050, 100))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(trades) != 1 || trades[0].SellOrderID != resting.ID {
		t.Errorf("expected the partially-filled resting order to match again first, got %+v", trades)
	}
}

func TestInvalidOrdersRejected(t *testing.T) {
	ob := NewOrderBook("TEST")

	cases := []*Order{
		{ID: newID(), Side: Buy, Type: Limit, Price: 100, Quantity: 0},   // zero quantity
		{ID: newID(), Side: Buy, Type: Limit, Price: 0, Quantity: 100},   // zero price on a limit order
		{ID: newID(), Side: Buy, Type: Market, Price: 100, Quantity: 10}, // market order with a price set
		{ID: newID(), Side: "SIDEWAYS", Type: Limit, Price: 100, Quantity: 10},
	}
	for i, o := range cases {
		if _, err := ob.Submit(o); err == nil {
			t.Errorf("case %d: expected validation error, got none", i)
		}
	}
}

func TestNoSelfCrossWithoutOverlap(t *testing.T) {
	ob := NewOrderBook("TEST")
	mustSubmit(t, ob, newLimit(Sell, 15050, 500))

	trades, err := ob.Submit(newLimit(Buy, 15045, 500))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(trades) != 0 {
		t.Fatalf("prices don't cross, expected no trades, got %+v", trades)
	}

	bids, asks := ob.Snapshot(10)
	if len(bids) != 1 || len(asks) != 1 {
		t.Errorf("expected both orders resting untouched, got bids=%+v asks=%+v", bids, asks)
	}
}

func mustSubmit(t *testing.T, ob *OrderBook, o *Order) []Trade {
	t.Helper()
	trades, err := ob.Submit(o)
	if err != nil {
		t.Fatalf("unexpected error submitting order: %v", err)
	}
	return trades
}
