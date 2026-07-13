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

// throughputWindowSeconds bounds the sliding window used for the
// "instantaneous" throughput figure. Earlier versions of this file computed
// throughput as ordersReceived / (time since server start), which silently
// becomes a lifetime average once the server has been up longer than the
// load test itself -- e.g. after a 60s test on a server with 1000s of
// uptime, that formula reports something close to received/1000, not
// received-during-the-test/60. A short rolling window of per-second buckets
// avoids that trap and matches what "throughput_orders_per_sec" actually
// implies: the current sustained rate, not an all-time average.
const throughputWindowSeconds = 5

// secondBucket counts orders received during one wall-clock second. second
// is compared-and-swapped on writes so a stale bucket from a previous lap
// around the ring gets reset rather than accumulating forever.
type secondBucket struct {
	second atomic.Int64
	count  atomic.Int64
}

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

	throughputBuckets [throughputWindowSeconds]secondBucket

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

func (m *Metrics) RecordOrderReceived() {
	m.ordersReceived.Add(1)
	m.tickThroughput()
}
func (m *Metrics) RecordCancelled() { m.ordersCancelled.Add(1) }
func (m *Metrics) IncInBook()       { m.ordersInBook.Add(1) }
func (m *Metrics) DecInBook()       { m.ordersInBook.Add(-1) }

// tickThroughput increments the bucket for the current wall-clock second.
// A benign race is possible if two goroutines land on the same bucket in
// the same instant it rolls over (both see the stale second and both
// reset+increment), which would undercount by at most 1 in that second --
// acceptable slop for an approximate, lock-free counter.
func (m *Metrics) tickThroughput() {
	sec := time.Now().Unix()
	idx := int(sec % throughputWindowSeconds)
	b := &m.throughputBuckets[idx]
	if b.second.Load() != sec {
		b.second.Store(sec)
		b.count.Store(1)
		return
	}
	b.count.Add(1)
}

// windowedThroughput averages orders/sec over however much of the trailing
// throughputWindowSeconds is actually populated with fresh buckets, so it
// reads correctly both mid-burst and right after the server starts.
func (m *Metrics) windowedThroughput() float64 {
	now := time.Now().Unix()
	var total int64
	var freshBuckets int64
	for i := range m.throughputBuckets {
		b := &m.throughputBuckets[i]
		age := now - b.second.Load()
		if age >= 0 && age < throughputWindowSeconds {
			total += b.count.Load()
			freshBuckets++
		}
	}
	if freshBuckets == 0 {
		return 0
	}
	return float64(total) / float64(freshBuckets)
}

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

	return Snapshot{
		OrdersReceived:   received,
		OrdersMatched:    m.ordersMatched.Load(),
		OrdersCancelled:  m.ordersCancelled.Load(),
		OrdersInBook:     m.ordersInBook.Load(),
		TradesExecuted:   m.tradesExecuted.Load(),
		LatencyP50Ms:     percentile(latencies, 0.50),
		LatencyP99Ms:     percentile(latencies, 0.99),
		LatencyP999Ms:    percentile(latencies, 0.999),
		ThroughputPerSec: m.windowedThroughput(),
		UptimeSeconds:    int64(uptime),
	}
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
