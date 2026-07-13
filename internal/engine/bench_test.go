package engine

import "testing"

// BenchmarkSubmitNonCrossing measures pure order-book insertion cost (no
// matching): every order rests since buys and sells never cross.
func BenchmarkSubmitNonCrossing(b *testing.B) {
	ob := NewOrderBook("BENCH")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		side := Buy
		price := int64(10000 - (i % 500))
		if i%2 == 0 {
			side = Sell
			price = int64(10500 + (i % 500))
		}
		_, _ = ob.Submit(&Order{ID: newID(), Symbol: "BENCH", Side: side, Type: Limit, Price: price, Quantity: 10})
	}
}

// BenchmarkSubmitCrossing measures the matching path itself: every incoming
// order crosses and fills against a deep resting book on the other side.
func BenchmarkSubmitCrossing(b *testing.B) {
	ob := NewOrderBook("BENCH")
	for i := 0; i < 100000; i++ {
		_, _ = ob.Submit(&Order{ID: newID(), Symbol: "BENCH", Side: Sell, Type: Limit, Price: int64(10000 + i%1000), Quantity: 1000000})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ob.Submit(&Order{ID: newID(), Symbol: "BENCH", Side: Buy, Type: Limit, Price: 20000, Quantity: 10})
	}
}

// BenchmarkEngineEndToEnd measures the full path including the actor
// channel round-trip, across multiple symbols concurrently -- the closest
// in-process proxy for the HTTP load test's throughput number.
func BenchmarkEngineEndToEnd(b *testing.B) {
	e := NewEngine()
	symbols := []string{"AAPL", "GOOGL", "MSFT", "TSLA"}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			sym := symbols[i%len(symbols)]
			side := Buy
			if i%2 == 0 {
				side = Sell
			}
			_, _, _ = e.SubmitOrder(sym, side, Limit, int64(10000+i%200), 10)
			i++
		}
	})
}
