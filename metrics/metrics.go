package metrics

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// latencyBufferSize bounds memory use for latency tracking: a fixed ring
// buffer of the most recent samples rather than an unbounded slice. 20,000
// samples is plenty to get stable p50/p99/p999 estimates under the
// assignment's 30k-100k orders/sec load profiles without needing a proper
// streaming histogram library.
const latencyBufferSize = 20000

// Metrics tracks request counts and latency using only sync/atomic and a
// small mutex-guarded ring buffer -- deliberately no external dependency.
// Percentiles are computed on read (GET /metrics is called rarely compared
// to order submission), so a copy+sort there is cheap and keeps the hot
// order-submission path lock-free.
type Metrics struct {
	startTime time.Time

	ordersReceived  atomic.Int64
	ordersMatched   atomic.Int64
	ordersCancelled atomic.Int64
	ordersInBook    atomic.Int64
	tradesExecuted  atomic.Int64

	latMu  sync.Mutex
	latBuf []float64
	latPos int
}

func New() *Metrics {
	return &Metrics{
		startTime: time.Now(),
		latBuf:    make([]float64, 0, latencyBufferSize),
	}
}

func (m *Metrics) RecordOrderReceived() { m.ordersReceived.Add(1) }
func (m *Metrics) RecordCancelled()     { m.ordersCancelled.Add(1) }
func (m *Metrics) IncInBook()           { m.ordersInBook.Add(1) }
func (m *Metrics) DecInBook()           { m.ordersInBook.Add(-1) }

// RecordMatched should be called with the number of trades produced by one
// order submission (0 if it rested on the book without matching).
func (m *Metrics) RecordMatched(trades int) {
	if trades > 0 {
		m.ordersMatched.Add(1)
		m.tradesExecuted.Add(int64(trades))
	}
}

// RecordLatency records one request's complete round-trip time.
func (m *Metrics) RecordLatency(d time.Duration) {
	ms := float64(d.Microseconds()) / 1000.0
	m.latMu.Lock()
	if len(m.latBuf) < latencyBufferSize {
		m.latBuf = append(m.latBuf, ms)
	} else {
		m.latBuf[m.latPos] = ms
		m.latPos = (m.latPos + 1) % latencyBufferSize
	}
	m.latMu.Unlock()
}

// Snapshot is the JSON shape returned by GET /metrics.
type Snapshot struct {
	OrdersReceived   int64   `json:"orders_received"`
	OrdersMatched    int64   `json:"orders_matched"`
	OrdersCancelled  int64   `json:"orders_cancelled"`
	OrdersInBook     int64   `json:"orders_in_book"`
	TradesExecuted   int64   `json:"trades_executed"`
	LatencyP50Ms     float64 `json:"latency_p50_ms"`
	LatencyP99Ms     float64 `json:"latency_p99_ms"`
	LatencyP999Ms    float64 `json:"latency_p999_ms"`
	ThroughputPerSec float64 `json:"throughput_orders_per_sec"`
	UptimeSeconds    int64   `json:"uptime_seconds"`
}

func (m *Metrics) Snapshot() Snapshot {
	m.latMu.Lock()
	latencies := make([]float64, len(m.latBuf))
	copy(latencies, m.latBuf)
	m.latMu.Unlock()
	sort.Float64s(latencies)

	uptime := time.Since(m.startTime).Seconds()
	received := m.ordersReceived.Load()

	s := Snapshot{
		OrdersReceived:  received,
		OrdersMatched:   m.ordersMatched.Load(),
		OrdersCancelled: m.ordersCancelled.Load(),
		OrdersInBook:    m.ordersInBook.Load(),
		TradesExecuted:  m.tradesExecuted.Load(),
		LatencyP50Ms:    percentile(latencies, 0.50),
		LatencyP99Ms:    percentile(latencies, 0.99),
		LatencyP999Ms:   percentile(latencies, 0.999),
		UptimeSeconds:   int64(uptime),
	}
	if uptime > 0 {
		s.ThroughputPerSec = float64(received) / uptime
	}
	return s
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
