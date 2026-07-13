package engine

// Side indicates whether an order buys or sells.
type Side string

const (
	Buy  Side = "BUY"
	Sell Side = "SELL"
)

// OrderType distinguishes limit orders (price specified) from market orders
// (execute immediately at the best available price, or reject).
type OrderType string

const (
	Limit  OrderType = "LIMIT"
	Market OrderType = "MARKET"
)

// Status reflects the lifecycle of an order.
type Status string

const (
	StatusAccepted    Status = "ACCEPTED"
	StatusPartialFill Status = "PARTIAL_FILL"
	StatusFilled      Status = "FILLED"
	StatusCancelled   Status = "CANCELLED"
	StatusRejected    Status = "REJECTED"
)

// Order is a single resting or incoming order. Price and Quantity are always
// integers: Price is in the smallest currency unit (e.g. cents) to avoid any
// floating point precision issues in matching or trade valuation.
type Order struct {
	ID        string
	Symbol    string
	Side      Side
	Type      OrderType
	Price     int64 // ignored/zero for MARKET orders
	Quantity  int64 // original requested quantity
	Filled    int64 // cumulative filled quantity
	Status    Status
	Timestamp int64 // unix milliseconds, set server-side on acceptance

	// seq is a monotonically increasing insertion sequence assigned by the
	// symbol actor. It breaks ties within a price level deterministically
	// (FIFO) even if two orders share a millisecond timestamp.
	seq uint64
}

// Remaining returns the quantity still unfilled.
func (o *Order) Remaining() int64 {
	return o.Quantity - o.Filled
}

// Trade is a single execution resulting from matching two orders.
type Trade struct {
	ID          string
	Symbol      string
	Price       int64
	Quantity    int64
	Timestamp   int64
	BuyOrderID  string
	SellOrderID string
	// TakerSide identifies which side was the incoming ("aggressor") order,
	// useful for market-data/OHLCV features later.
	TakerSide Side
}

// PriceLevelView is an aggregated, read-only view of one price level, used
// for order book snapshots returned by the API.
type PriceLevelView struct {
	Price    int64 `json:"price"`
	Quantity int64 `json:"quantity"`
}
