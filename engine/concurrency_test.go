package engine

import (
	"sync"
	"testing"
)

// TestConcurrentSubmitSameSymbol hammers a single symbol from many
// goroutines at once. Run with -race: because every mutation happens
// inside SymbolActor.run() on one goroutine, there should be zero data
// races no matter how many callers submit concurrently.
func TestConcurrentSubmitSameSymbol(t *testing.T) {
	e := NewEngine()
	const workers = 50
	const perWorker = 100

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				side := Buy
				if (w+i)%2 == 0 {
					side = Sell
				}
				price := int64(10000 + (i % 10))
				if _, _, err := e.SubmitOrder("AAPL", side, Limit, price, 10); err != nil {
					t.Errorf("unexpected error: %v", err)
				}
			}
		}(w)
	}
	wg.Wait()

	// Total quantity submitted must be conserved: whatever isn't resting
	// in the book was matched into trades. We can't easily count trades
	// here without extra plumbing, so the strongest check available at
	// this layer is that the book is internally consistent (no panic, no
	// race under -race, and depth queries succeed).
	bids, asks, err := e.OrderBookSnapshot("AAPL", 100)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var remaining int64
	for _, b := range bids {
		remaining += b.Quantity
	}
	for _, a := range asks {
		remaining += a.Quantity
	}
	if remaining < 0 || remaining > workers*perWorker*10 {
		t.Errorf("implausible remaining book quantity: %d", remaining)
	}
}

// TestConcurrentDifferentSymbolsRunInParallel exercises many distinct
// symbols concurrently, which should each get their own actor.
func TestConcurrentDifferentSymbolsRunInParallel(t *testing.T) {
	e := NewEngine()
	symbols := []string{"AAPL", "GOOGL", "MSFT", "TSLA", "AMZN", "BTC", "ETH"}

	var wg sync.WaitGroup
	for _, sym := range symbols {
		wg.Add(1)
		go func(symbol string) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				side := Sell
				if i%2 == 0 {
					side = Buy
				}
				if _, _, err := e.SubmitOrder(symbol, side, Limit, int64(1000+i%5), 5); err != nil {
					t.Errorf("%s: unexpected error: %v", symbol, err)
				}
			}
		}(sym)
	}
	wg.Wait()

	for _, sym := range symbols {
		if _, _, err := e.OrderBookSnapshot(sym, 10); err != nil {
			t.Errorf("%s: unexpected error reading book: %v", sym, err)
		}
	}
}

// TestConcurrentCancelRace submits a batch of orders, then hammers cancel
// on the same IDs from many goroutines concurrently -- at most one caller
// per ID should succeed, everyone else should get a clean "already
// filled/cancelled" or "not found" error, never a panic or race.
func TestConcurrentCancelRace(t *testing.T) {
	e := NewEngine()
	const n = 200
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		o, _, err := e.SubmitOrder("AAPL", Buy, Limit, 10000, 10)
		if err != nil {
			t.Fatalf("setup: unexpected error: %v", err)
		}
		ids[i] = o.ID
	}

	var wg sync.WaitGroup
	var successCount int32
	var mu sync.Mutex
	for _, id := range ids {
		for attempt := 0; attempt < 3; attempt++ {
			wg.Add(1)
			go func(orderID string) {
				defer wg.Done()
				if _, err := e.CancelOrder(orderID); err == nil {
					mu.Lock()
					successCount++
					mu.Unlock()
				}
			}(id)
		}
	}
	wg.Wait()

	if int(successCount) != n {
		t.Errorf("expected exactly %d successful cancellations (one per order), got %d", n, successCount)
	}
}
