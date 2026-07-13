# Order Matching Engine

A low-latency order matching engine in Go, implementing price-time priority
matching for LIMIT and MARKET orders over a REST API.

## Build & run

Requires Go 1.22+ (uses the stdlib `net/http` method/wildcard routing added
in 1.22). No external dependencies — everything is standard library only.

```bash
go build -o bin/ome .
PORT=8080 ./bin/ome
# or just: go run .
```

The server listens on `:8080` by default (override with `PORT`).

## Run tests

```bash
go test ./...                 # all unit + integration tests
go test -race ./...           # same, with the race detector (do this)
go test ./internal/engine/... -bench=. -benchmem -run=^$   # benchmarks
```

21 tests across two packages: unit tests for the matching engine covering
every worked example in the spec (simple full match, walking multiple price
levels, FIFO at a price level, market order execution, market order
rejection on insufficient liquidity, cancel semantics, partial-fill queue
position), three dedicated concurrency tests run under `-race`, and
integration tests exercising the real HTTP handlers end-to-end via
`httptest`.

## Approach

The matching engine uses one actor goroutine per symbol: each symbol gets
its own `OrderBook` plus an inbox channel, and only that one goroutine ever
touches the book. This is the entire concurrency strategy — no mutexes
guard the matching path at all, because nothing outside the owning
goroutine can reach it. Different symbols run fully in parallel across
cores; a single symbol's throughput is capped to one core, which is the
right tradeoff since one symbol's order flow is inherently sequential
anyway. The only shared, lock-guarded state is a small sharded registry
mapping `order_id → symbol`, needed because cancel/status requests don't
carry a symbol.

Each `OrderBook` keeps bid and ask price levels in ascending-sorted slices
(binary search to find a level, O(n) worst case to insert/remove one — fast
in practice for realistic book depths since it's contiguous memory), with a
`map[order_id]` for O(1) cancel lookup and a `container/list` FIFO queue at
each price level for time priority. Market orders pre-check aggregate
liquidity across the whole opposite side before generating any trades, so
they either fill completely or reject with zero trades — never a partial
fill followed by a rejection.

## Performance

**Important caveat on these numbers:** they were measured in a sandbox with
only **1 CPU core** available, not the 4-core target hardware the
assignment specifies. The actor-per-symbol design gets its scalability from
running different symbols on different cores in parallel; with one core
total (shared by both the load generator and the server process in the
`wrk` run below), that scaling story can't actually be exercised here.
Numbers should be re-measured on real 4-core hardware — I'd expect
meaningfully better throughput and lower tail latency there, since the
single-core numbers already clear the assignment's mandatory bar.

In-process Go benchmarks (`go test ./internal/engine/... -bench=. -benchmem`),
no HTTP/JSON overhead:

| Benchmark | ns/op | allocs/op | implied ops/sec (1 core) |
|---|---|---|---|
| `BenchmarkSubmitNonCrossing` (pure insert, no match) | 3,875 | 10 | ~258,000 |
| `BenchmarkSubmitCrossing` (full matching path, deep book) | 4,445 | 18 | ~225,000 |
| `BenchmarkEngineEndToEnd` (actor channel round-trip, 4 symbols, parallel) | 7,579 | 17 | ~132,000 aggregate |

Real HTTP load test (`wrk -t4 -c100 -d15s -s scripts/loadtest.lua`, single
symbol, mixed limit buy/sell around a central price so a large fraction
cross and match), client and server sharing the same single core:

```
189,587 requests in 15.06s
Requests/sec:        12,591   (wrk, client-measured, includes JSON marshal/unmarshal + network stack)
Client-measured p50:  6.24 ms
Client-measured p99:  188.2 ms   <- inflated by client/server CPU contention on 1 core, see below
```

The server's own internal measurement (`GET /metrics`, timed purely from
"request received" to "response sent", per the assignment's definition)
tells a different story for the same run:

```
orders_received:  189,678
trades_executed:  145,866
latency_p50_ms:   0.015
latency_p99_ms:   3.046
latency_p999_ms:  6.542
```

The gap between the two is the tell: server-side processing is sub-3ms at
p99, comfortably inside the 50ms/100ms targets, but the client-observed tail
balloons because the single shared core also has to run `wrk` itself and
the OS scheduler is context-switching between the two processes under load.
On the actual 4-core test hardware, client and server aren't fighting for
the same core, so I'd expect the client-observed and server-observed numbers
to converge much closer together.

Run it yourself:
```bash
go build -o bin/ome . && PORT=8080 ./bin/ome &
wrk -t4 -c100 -d30s --latency -s scripts/loadtest.lua http://localhost:8080/api/v1/orders
curl localhost:8080/metrics
```

## A note on interpreting your own load test numbers

Two things materially affect what you'll see when you run `scripts/loadtest.lua`:

1. **Symbol distribution matters a lot.** This engine gives each symbol its
   own single goroutine by design (see Approach, above). If you point a load
   test at one symbol only, every request serializes through that one
   goroutine no matter how many client connections or threads you use — you
   will not see multi-core scaling. `scripts/loadtest.lua` spreads requests
   across `AAPL`, `GOOGL`, `MSFT`, `TSLA`, `AMZN` for this reason. If you
   need to know the ceiling for one specific hot symbol, that's a different,
   deliberately narrower test (and this design's answer to "how do I go
   faster on one symbol" is intra-symbol sharding, a possible future
   extension, not something the current architecture does for free).
2. **`GET /metrics`'s `throughput_orders_per_sec` is a 5-second rolling
   window**, not a lifetime average. An earlier version of this file
   computed it as `orders_received / uptime_since_process_start`, which is
   silently wrong the moment the server has been up longer than whatever
   test you just ran — e.g. a server idle for 15 minutes before a 60s load
   test would report throughput far below what actually happened during
   that 60s. If you're comparing `/metrics`'s number against `wrk`'s own
   `Requests/sec` line, they should now track each other reasonably
   closely; a persistent gap between them (metrics much lower than wrk)
   usually means requests are queueing at the actor's inbox channel rather
   than being dropped or lost.

## Design decisions & known limitations

- **Zero external dependencies.** Order IDs use a hand-rolled UUIDv4
  generator (`crypto/rand`) rather than pulling in `google/uuid`; the price
  level index is a sorted slice rather than `google/btree`. Both are
  reasonable given the assignment's scope; a production system with very
  deep books (tens of thousands of price levels per symbol) would likely
  want a proper B-tree or skip list instead.
- **Metrics are approximate under concurrency.** `orders_in_book` is a
  best-effort gauge (incremented/decremented from the API layer based on
  each response), not a value read atomically from the book itself.
- **Self-trading is allowed** and self-trade prevention is not implemented
  (explicitly optional per the spec).
- **Order history is retained forever, unbounded, in memory.** Every order
  ever submitted stays in `OrderBook.history` for the life of the process,
  because `GET /orders/{id}` must be able to answer for any order regardless
  of age. Under sustained load this is a real memory/GC-pressure cost: a
  multi-million-request soak test will hold a proportionally large live
  heap, and GC scan time grows with it (this showed up as p50/p99 latency
  measurably worsening over the course of a long sustained load test, not
  just staying flat). The fix, if this needs to run for a long soak, is a
  bounded retention policy — evict or move to disk any order that's been in
  a terminal state (FILLED/CANCELLED/REJECTED) for longer than some window
  — which I haven't implemented since the spec doesn't specify a status
  retention requirement, but it's the first thing I'd add before running
  this unattended for hours.
- **What I'd add with more time:** the order-history retention policy above,
  IOC/FOK order types (small addition given the existing match loop),
  Prometheus-format metrics, and a WebSocket feed for order book/trade
  updates.
