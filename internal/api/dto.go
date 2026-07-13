package api

// SubmitOrderRequest is the POST /api/v1/orders request body. Price is a
// pointer so it can be omitted entirely for MARKET orders, distinguishing
// "no price sent" from "price sent as zero".
type SubmitOrderRequest struct {
	Symbol   string `json:"symbol"`
	Side     string `json:"side"`
	Type     string `json:"type"`
	Price    *int64 `json:"price,omitempty"`
	Quantity int64  `json:"quantity"`
}

type TradeDTO struct {
	TradeID   string `json:"trade_id"`
	Price     int64  `json:"price"`
	Quantity  int64  `json:"quantity"`
	Timestamp int64  `json:"timestamp"`
}

// SubmitOrderResponse covers all three success shapes from the spec
// (ACCEPTED / PARTIAL_FILL / FILLED) with one struct; fields that don't
// apply to a given status are omitted via omitempty.
type SubmitOrderResponse struct {
	OrderID           string     `json:"order_id"`
	Status            string     `json:"status"`
	Message           string     `json:"message,omitempty"`
	FilledQuantity    int64      `json:"filled_quantity,omitempty"`
	RemainingQuantity int64      `json:"remaining_quantity,omitempty"`
	Trades            []TradeDTO `json:"trades,omitempty"`
}

type CancelResponse struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
}

type OrderStatusResponse struct {
	OrderID        string `json:"order_id"`
	Symbol         string `json:"symbol"`
	Side           string `json:"side"`
	Type           string `json:"type"`
	Price          int64  `json:"price"`
	Quantity       int64  `json:"quantity"`
	FilledQuantity int64  `json:"filled_quantity"`
	Status         string `json:"status"`
	Timestamp      int64  `json:"timestamp"`
}

type PriceLevelDTO struct {
	Price    int64 `json:"price"`
	Quantity int64 `json:"quantity"`
}

type OrderBookResponse struct {
	Symbol    string          `json:"symbol"`
	Timestamp int64           `json:"timestamp"`
	Bids      []PriceLevelDTO `json:"bids"`
	Asks      []PriceLevelDTO `json:"asks"`
}

type HealthResponse struct {
	Status          string `json:"status"`
	UptimeSeconds   int64  `json:"uptime_seconds"`
	OrdersProcessed int64  `json:"orders_processed"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}
