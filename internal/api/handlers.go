package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ordermatch/internal/engine"
	"ordermatch/internal/metrics"
)

// Server wires the engine and metrics into a standard http.Handler.
//
// Routing intentionally uses only plain http.ServeMux prefix matching plus
// manual path-suffix parsing (no method-prefixed patterns, no {id}
// wildcards, no r.PathValue) so the project compiles on any Go 1.18+
// toolchain, not just Go 1.22+. See handleOrderByID and handleOrderBook for
// where the {id}/{symbol} extraction happens by hand.
type Server struct {
	engine  *engine.Engine
	metrics *metrics.Metrics
	mux     *http.ServeMux
}

func NewServer(e *engine.Engine, m *metrics.Metrics) *Server {
	s := &Server{engine: e, metrics: m, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	// Exact match wins over the trailing-slash prefix pattern below, so
	// "POST /api/v1/orders" (no id) always lands here regardless of the
	// "/api/v1/orders/" registration.
	s.mux.HandleFunc("/api/v1/orders", s.withLatency(s.handleOrdersRoot))
	// Prefix match: anything under /api/v1/orders/<something> -- the
	// handler itself dispatches on method and extracts the id.
	s.mux.HandleFunc("/api/v1/orders/", s.withLatency(s.handleOrderByID))
	s.mux.HandleFunc("/api/v1/orderbook/", s.withLatency(s.handleOrderBook))
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/metrics", s.handleMetrics)
}

// handleOrdersRoot handles POST /api/v1/orders (order submission). Any
// other method on this exact path is a 405.
func (s *Server) handleOrdersRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.handleSubmitOrder(w, r)
}

// handleOrderByID handles GET/DELETE /api/v1/orders/{id} by pulling the id
// off the end of the path manually.
func (s *Server) handleOrderByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/orders/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleGetOrder(w, r, id)
	case http.MethodDelete:
		s.handleCancelOrder(w, r, id)
	default:
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// withLatency records round-trip time as defined by the assignment: from
// the server receiving the request to sending the complete response.
func (s *Server) withLatency(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next(w, r)
		s.metrics.RecordLatency(time.Since(start))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

func (s *Server) handleSubmitOrder(w http.ResponseWriter, r *http.Request) {
	var req SubmitOrderRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "malformed JSON: "+err.Error())
		return
	}
	if req.Symbol == "" {
		writeError(w, http.StatusBadRequest, "symbol is required")
		return
	}

	var price int64
	if req.Price != nil {
		price = *req.Price
	}

	s.metrics.RecordOrderReceived()

	order, trades, err := s.engine.SubmitOrder(req.Symbol, engine.Side(req.Side), engine.OrderType(req.Type), price, req.Quantity)
	if err != nil {
		switch {
		case errors.Is(err, engine.ErrInvalidOrder), errors.Is(err, engine.ErrInsufficientLiquidity):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	s.metrics.RecordMatched(len(trades))
	if order.Status == engine.StatusAccepted || order.Status == engine.StatusPartialFill {
		s.metrics.IncInBook()
	}

	tradeDTOs := make([]TradeDTO, len(trades))
	for i, t := range trades {
		tradeDTOs[i] = TradeDTO{TradeID: t.ID, Price: t.Price, Quantity: t.Quantity, Timestamp: t.Timestamp}
	}

	switch order.Status {
	case engine.StatusFilled:
		writeJSON(w, http.StatusOK, SubmitOrderResponse{
			OrderID: order.ID, Status: string(order.Status),
			FilledQuantity: order.Filled, Trades: tradeDTOs,
		})
	case engine.StatusPartialFill:
		writeJSON(w, http.StatusAccepted, SubmitOrderResponse{
			OrderID: order.ID, Status: string(order.Status),
			FilledQuantity: order.Filled, RemainingQuantity: order.Remaining(), Trades: tradeDTOs,
		})
	default: // ACCEPTED
		writeJSON(w, http.StatusCreated, SubmitOrderResponse{
			OrderID: order.ID, Status: string(order.Status), Message: "Order added to book",
		})
	}
}

func (s *Server) handleCancelOrder(w http.ResponseWriter, r *http.Request, id string) {
	order, err := s.engine.CancelOrder(id)
	if err != nil {
		switch {
		case errors.Is(err, engine.ErrOrderNotFound):
			writeError(w, http.StatusNotFound, "Order not found")
		case errors.Is(err, engine.ErrAlreadyFilled):
			writeError(w, http.StatusBadRequest, "Cannot cancel: order already filled")
		default:
			writeError(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	s.metrics.RecordCancelled()
	s.metrics.DecInBook()
	writeJSON(w, http.StatusOK, CancelResponse{OrderID: order.ID, Status: string(order.Status)})
}

func (s *Server) handleGetOrder(w http.ResponseWriter, r *http.Request, id string) {
	order, err := s.engine.GetOrder(id)
	if err != nil {
		writeError(w, http.StatusNotFound, "Order not found")
		return
	}

	writeJSON(w, http.StatusOK, OrderStatusResponse{
		OrderID: order.ID, Symbol: order.Symbol, Side: string(order.Side), Type: string(order.Type),
		Price: order.Price, Quantity: order.Quantity, FilledQuantity: order.Filled,
		Status: string(order.Status), Timestamp: order.Timestamp,
	})
}

func (s *Server) handleOrderBook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	symbol := strings.TrimPrefix(r.URL.Path, "/api/v1/orderbook/")
	if symbol == "" || strings.Contains(symbol, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}

	depth := 10
	if d := r.URL.Query().Get("depth"); d != "" {
		if parsed, convErr := strconv.Atoi(d); convErr == nil && parsed > 0 {
			depth = parsed
		}
	}

	bids, asks, err := s.engine.OrderBookSnapshot(symbol, depth)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}

	resp := OrderBookResponse{
		Symbol:    symbol,
		Timestamp: time.Now().UnixMilli(),
		Bids:      make([]PriceLevelDTO, len(bids)),
		Asks:      make([]PriceLevelDTO, len(asks)),
	}
	for i, b := range bids {
		resp.Bids[i] = PriceLevelDTO{Price: b.Price, Quantity: b.Quantity}
	}
	for i, a := range asks {
		resp.Asks[i] = PriceLevelDTO{Price: a.Price, Quantity: a.Quantity}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	snap := s.metrics.Snapshot()
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:          "healthy",
		UptimeSeconds:   snap.UptimeSeconds,
		OrdersProcessed: snap.OrdersReceived,
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.metrics.Snapshot())
}
