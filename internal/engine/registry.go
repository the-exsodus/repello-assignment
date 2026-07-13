package engine

import (
	"hash/fnv"
	"sync"
)

const registryShards = 32

type registryShard struct {
	mu sync.RWMutex
	m  map[string]string
}

// orderRegistry maps an order ID to the symbol whose actor owns it. It
// exists because DELETE /orders/{id} and GET /orders/{id} don't carry a
// symbol, so something has to know where to route them.
//
// This is the one piece of state genuinely shared across goroutines (every
// HTTP request submitting or cancelling an order touches it), so it's
// sharded by a hash of the order ID into independent locks rather than
// guarded by one global mutex -- that would otherwise become a contention
// point completely separate from, and unnecessary given, the lock-free
// per-symbol actors.
type orderRegistry struct {
	shards [registryShards]*registryShard
}

func newOrderRegistry() *orderRegistry {
	r := &orderRegistry{}
	for i := range r.shards {
		r.shards[i] = &registryShard{m: make(map[string]string)}
	}
	return r
}

func (r *orderRegistry) shardFor(orderID string) *registryShard {
	h := fnv.New32a()
	_, _ = h.Write([]byte(orderID))
	return r.shards[h.Sum32()%registryShards]
}

func (r *orderRegistry) set(orderID, symbol string) {
	s := r.shardFor(orderID)
	s.mu.Lock()
	s.m[orderID] = symbol
	s.mu.Unlock()
}

func (r *orderRegistry) get(orderID string) (string, bool) {
	s := r.shardFor(orderID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	symbol, ok := s.m[orderID]
	return symbol, ok
}
