package engine

// cmdKind identifies which operation a command asks the actor to perform.
type cmdKind int

const (
	cmdSubmit cmdKind = iota
	cmdCancel
	cmdGetOrder
	cmdSnapshot
)

// command is sent down a SymbolActor's inbox channel. reply is always
// buffered (size 1) so the actor never blocks trying to send a response.
type command struct {
	kind    cmdKind
	order   *Order // cmdSubmit: the incoming order, mutated in place by Submit
	orderID string // cmdCancel / cmdGetOrder
	depth   int    // cmdSnapshot
	reply   chan result
}

// result carries a VALUE copy of any Order, never a pointer. This is
// deliberate: the actor goroutine may go on to mutate the same underlying
// *Order later (e.g. if it gets further partially filled), so handing the
// caller a pointer would be a data race the moment the HTTP goroutine reads
// a field. A value copy taken at the instant of reply is a safe snapshot.
type result struct {
	order  Order
	trades []Trade
	bids   []PriceLevelView
	asks   []PriceLevelView
	err    error
}

// SymbolActor owns exactly one OrderBook and processes every command for it
// sequentially on a single goroutine. This is the entire concurrency
// strategy for correctness: because nothing outside this goroutine ever
// touches the OrderBook, no mutex is needed on the hot matching path at
// all. Different symbols run on different goroutines, so the engine still
// scales across every CPU core -- it's per-symbol throughput that's capped
// to one core, which is the right tradeoff for a matching engine (a single
// symbol's order flow is inherently sequential; unrelated symbols are not).
type SymbolActor struct {
	symbol string
	book   *OrderBook
	inbox  chan command
}

func newSymbolActor(symbol string, inboxSize int) *SymbolActor {
	a := &SymbolActor{
		symbol: symbol,
		book:   NewOrderBook(symbol),
		inbox:  make(chan command, inboxSize),
	}
	go a.run()
	return a
}

func (a *SymbolActor) run() {
	for cmd := range a.inbox {
		switch cmd.kind {
		case cmdSubmit:
			trades, err := a.book.Submit(cmd.order)
			cmd.reply <- result{order: *cmd.order, trades: trades, err: err}

		case cmdCancel:
			if err := a.book.Cancel(cmd.orderID); err != nil {
				cmd.reply <- result{err: err}
				continue
			}
			o, _ := a.book.GetOrder(cmd.orderID)
			cmd.reply <- result{order: *o}

		case cmdGetOrder:
			o, ok := a.book.GetOrder(cmd.orderID)
			if !ok {
				cmd.reply <- result{err: ErrOrderNotFound}
				continue
			}
			cmd.reply <- result{order: *o}

		case cmdSnapshot:
			bids, asks := a.book.Snapshot(cmd.depth)
			cmd.reply <- result{bids: bids, asks: asks}
		}
	}
}
