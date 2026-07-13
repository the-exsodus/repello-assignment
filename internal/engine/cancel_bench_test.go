package engine

import (
	"fmt"
	"testing"
)

// BenchmarkCancelAtDepth measures Cancel() latency with the book pre-loaded
// to different depths. If cancellation were secretly O(n) (a linear scan
// somewhere), the ns/op figure would grow proportionally with depth. It
// should instead stay flat.
func BenchmarkCancelAtDepth(b *testing.B) {
	for _, depth := range []int{1_000, 100_000, 1_000_000, 4_000_000} {
		depth := depth
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			ob := NewOrderBook("BENCH")
			// Spread across many price levels so no single level dominates,
			// then grab a set of order IDs scattered throughout to cancel.
			ids := make([]string, 0, b.N)
			for i := 0; i < depth; i++ {
				o := &Order{ID: newID(), Symbol: "BENCH", Side: Buy, Type: Limit, Price: int64(1 + i%5000), Quantity: 10}
				_, _ = ob.Submit(o)
				if len(ids) < b.N {
					ids = append(ids, o.ID)
				}
			}
			for len(ids) < b.N {
				// If depth < b.N, top up with more resting orders to cancel.
				o := &Order{ID: newID(), Symbol: "BENCH", Side: Buy, Type: Limit, Price: int64(1 + len(ids)%5000), Quantity: 10}
				_, _ = ob.Submit(o)
				ids = append(ids, o.ID)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = ob.Cancel(ids[i])
			}
		})
	}
}
