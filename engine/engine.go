package engine

import (
	"context"
	"sync"
	"time"
)

const (
	defaultInboxSize = 4096
	requestTimeout   = 2 * time.Second
)

// Engine is the entry point the API layer talks to. It creates one
// SymbolActor per symbol lazily (on first order for that symbol) and owns
// the registry used to route symbol-less requests to the right actor.
type Engine struct {
	mu     sync.RWMutex
	actors map[string]*SymbolActor

	registry *orderRegistry
}

func NewEngine() *Engine {
	return &Engine{
		actors:   make(map[string]*SymbolActor),
		registry: newOrderRegistry(),
	}
}

// actorFor returns the actor for symbol, creating it if this is the first
// time the symbol has been seen. Double-checked locking keeps the common
// case (actor already exists) to a cheap read lock.
func (e *Engine) actorFor(symbol string) *SymbolActor {
	e.mu.RLock()
	a, ok := e.actors[symbol]
	e.mu.RUnlock()
	if ok {
		return a
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if a, ok := e.actors[symbol]; ok {
		return a
	}
	a = newSymbolActor(symbol, defaultInboxSize)
	e.actors[symbol] = a
	return a
}

// send dispatches a command to the symbol's actor and waits for its reply,
// bounded by requestTimeout so a stuck actor can't hang the caller (or, via
// HTTP, the client) forever.
func (e *Engine) send(ctx context.Context, symbol string, cmd command) (result, error) {
	a := e.actorFor(symbol)
	select {
	case a.inbox <- cmd:
	case <-ctx.Done():
		return result{}, ctx.Err()
	}
	select {
	case r := <-cmd.reply:
		return r, nil
	case <-ctx.Done():
		return result{}, ctx.Err()
	}
}

// SubmitOrder generates a server-side order ID and submits it to the owning
// symbol's actor, registering the ID for future cancel/status lookups on
// success.
func (e *Engine) SubmitOrder(symbol string, side Side, typ OrderType, price, qty int64) (Order, []Trade, error) {
	o := &Order{
		ID:       newID(),
		Symbol:   symbol,
		Side:     side,
		Type:     typ,
		Price:    price,
		Quantity: qty,
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	reply := make(chan result, 1)
	r, err := e.send(ctx, symbol, command{kind: cmdSubmit, order: o, reply: reply})
	if err != nil {
		return Order{}, nil, err
	}
	if r.err != nil {
		return r.order, r.trades, r.err
	}
	e.registry.set(o.ID, symbol)
	return r.order, r.trades, nil
}

// CancelOrder looks up which symbol owns orderID and cancels it there.
func (e *Engine) CancelOrder(orderID string) (Order, error) {
	symbol, ok := e.registry.get(orderID)
	if !ok {
		return Order{}, ErrOrderNotFound
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	reply := make(chan result, 1)
	r, err := e.send(ctx, symbol, command{kind: cmdCancel, orderID: orderID, reply: reply})
	if err != nil {
		return Order{}, err
	}
	if r.err != nil {
		return Order{}, r.err
	}
	return r.order, nil
}

// GetOrder returns the current state of any order ever submitted.
func (e *Engine) GetOrder(orderID string) (Order, error) {
	symbol, ok := e.registry.get(orderID)
	if !ok {
		return Order{}, ErrOrderNotFound
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	reply := make(chan result, 1)
	r, err := e.send(ctx, symbol, command{kind: cmdGetOrder, orderID: orderID, reply: reply})
	if err != nil {
		return Order{}, err
	}
	if r.err != nil {
		return Order{}, r.err
	}
	return r.order, nil
}

// OrderBookSnapshot returns aggregated depth for a symbol. An unknown
// symbol simply returns an empty book rather than an error, since "no
// orders yet for AAPL" is a normal state, not a failure.
func (e *Engine) OrderBookSnapshot(symbol string, depth int) (bids, asks []PriceLevelView, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()

	reply := make(chan result, 1)
	r, sendErr := e.send(ctx, symbol, command{kind: cmdSnapshot, depth: depth, reply: reply})
	if sendErr != nil {
		return nil, nil, sendErr
	}
	return r.bids, r.asks, nil
}
