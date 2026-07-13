package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ordermatch/internal/engine"
	"ordermatch/internal/metrics"
)

func newTestServer() *Server {
	return NewServer(engine.NewEngine(), metrics.New())
}

func postJSON(t *testing.T, srv *Server, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestIntegration_FullOrderLifecycle(t *testing.T) {
	srv := newTestServer()

	// Rest a sell order.
	rec := postJSON(t, srv, "/api/v1/orders", SubmitOrderRequest{
		Symbol: "AAPL", Side: "SELL", Type: "LIMIT", Price: ptr(int64(15050)), Quantity: 1000,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var resting SubmitOrderResponse
	mustUnmarshal(t, rec, &resting)
	if resting.Status != "ACCEPTED" {
		t.Fatalf("expected ACCEPTED, got %+v", resting)
	}

	// Cross it with a partial-fill buy.
	rec = postJSON(t, srv, "/api/v1/orders", SubmitOrderRequest{
		Symbol: "AAPL", Side: "BUY", Type: "LIMIT", Price: ptr(int64(15050)), Quantity: 400,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (fully filled), got %d: %s", rec.Code, rec.Body.String())
	}
	var filled SubmitOrderResponse
	mustUnmarshal(t, rec, &filled)
	if filled.Status != "FILLED" || filled.FilledQuantity != 400 || len(filled.Trades) != 1 {
		t.Fatalf("unexpected fill response: %+v", filled)
	}

	// Check the order book reflects the remaining resting quantity.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/orderbook/AAPL?depth=5", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var book OrderBookResponse
	mustUnmarshal(t, rec, &book)
	if len(book.Asks) != 1 || book.Asks[0].Quantity != 600 {
		t.Fatalf("unexpected order book: %+v", book)
	}

	// Check order status for the resting (now partially filled) order.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/orders/"+resting.OrderID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var status OrderStatusResponse
	mustUnmarshal(t, rec, &status)
	if status.Status != "PARTIAL_FILL" || status.FilledQuantity != 400 {
		t.Fatalf("unexpected order status: %+v", status)
	}

	// Cancel the remainder.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/orders/"+resting.OrderID, nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on cancel, got %d: %s", rec.Code, rec.Body.String())
	}
	var cancelled CancelResponse
	mustUnmarshal(t, rec, &cancelled)
	if cancelled.Status != "CANCELLED" {
		t.Fatalf("expected CANCELLED, got %+v", cancelled)
	}

	// Cancelling again should now 400.
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 re-cancelling, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIntegration_MarketOrderRejectedOn400(t *testing.T) {
	srv := newTestServer()
	rec := postJSON(t, srv, "/api/v1/orders", SubmitOrderRequest{
		Symbol: "AAPL", Side: "BUY", Type: "MARKET", Quantity: 100,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for market order against an empty book, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIntegration_InvalidOrderReturns400(t *testing.T) {
	srv := newTestServer()
	rec := postJSON(t, srv, "/api/v1/orders", SubmitOrderRequest{
		Symbol: "AAPL", Side: "BUY", Type: "LIMIT", Price: ptr(int64(100)), Quantity: -5,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for negative quantity, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIntegration_MalformedJSON(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orders", bytes.NewReader([]byte(`{not json`)))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for malformed JSON, got %d", rec.Code)
	}
}

func TestIntegration_CancelUnknownOrder404(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/orders/does-not-exist", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestIntegration_HealthAndMetrics(t *testing.T) {
	srv := newTestServer()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /health, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /metrics, got %d", rec.Code)
	}
}

func ptr[T any](v T) *T { return &v }

func mustUnmarshal(t *testing.T, rec *httptest.ResponseRecorder, v any) {
	t.Helper()
	if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
		t.Fatalf("unmarshal response %q: %v", rec.Body.String(), err)
	}
}
