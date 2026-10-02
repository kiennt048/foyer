package api

import (
	"context"
	"sync"
)

// The in-flight body budget.
//
// `bodyLimitFor` bounds **one** request: an anonymous caller gets 16 KiB, a
// signed-in operator the console's megabyte. Nothing bounded how many of those
// megabytes are decoded at once, and the decoding is what costs: the request
// validator turns a JSON body into a tree before the handler runs, and a 1 MiB body
// of `[0,0,…]` peaked at 34–40 MiB of heap (measured in this package). Ten
// signed-in operators — or one script
// with a stolen session — sending ten such bodies at once put the process that
// answers FreeRADIUS past its 256 MiB limit.
//
// So bodies larger than the anonymous ceiling share one budget of bytes in flight,
// and a request waits for room rather than being refused. Waiting, not refusing,
// because every caller that reaches this is signed in and every body it covers is
// an operator's deliberate action — a certificate upload, the hero photograph, a
// page design — and a `503` for an honest upload that arrived beside another one
// would be a refusal the contract does not declare and the operator cannot act on.
// Uploads still work; two at once take turns. nginx's `limit_conn` and Go's own
// `http.Server` offer the per-connection half of this and nothing for decoded
// bytes, which is why it is here and not a listener setting.
//
// **Not a console setting.** No operator can tell what figure to choose, and the
// right one is a function of the container's memory, not of the venue.
//
// **Its units are decoded bytes, and its size is the memory plan's.** A fixed
// 4 MiB of *body* bytes would be about 160 MiB decoded at the measured worst
// shape, and a constant is never set against the container. `planBodies`
// (memplan.go) sizes both budgets from the soft limit, and a body is weighed at
// what its shape costs to decode, not at its length.

type bodyBudget struct {
	mu      sync.Mutex
	free    int64
	total   int64
	waiters []*bodyWaiter
}

type bodyWaiter struct {
	n     int64
	ready chan struct{}
}

func newBodyBudget(total int64) *bodyBudget { return &bodyBudget{free: total, total: total} }

// acquire takes n bytes of the budget, waiting in arrival order until there is
// room or ctx ends. n is capped at the whole budget, and that cap is **not** a
// weight: `limitBody` reserves at most the whole budget while a body is read, and
// `bodyReservation.settle` refuses one whose real weight is larger. Were this cap
// the only weighing, a 16 MiB body would run "alone" as 4 MiB.
func (b *bodyBudget) acquire(ctx context.Context, n int64) (release func(), ok bool) {
	if n <= 0 {
		return func() {}, true
	}
	if n > b.total {
		n = b.total
	}
	b.mu.Lock()
	if len(b.waiters) == 0 && b.free >= n {
		b.free -= n
		b.mu.Unlock()
		return func() { b.put(n) }, true
	}
	w := &bodyWaiter{n: n, ready: make(chan struct{})}
	b.waiters = append(b.waiters, w)
	b.mu.Unlock()

	select {
	case <-w.ready:
		return func() { b.put(n) }, true
	case <-ctx.Done():
		b.mu.Lock()
		defer b.mu.Unlock()
		select {
		case <-w.ready:
			// Granted as the context ended: hand it straight back.
			b.free += n
			b.wake()
		default:
			for i, x := range b.waiters {
				if x == w {
					b.waiters = append(b.waiters[:i], b.waiters[i+1:]...)
					break
				}
			}
			b.wake()
		}
		return nil, false
	}
}

func (b *bodyBudget) put(n int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.free += n
	b.wake()
}

// wake grants waiters in order while the one at the head fits. Called with mu held.
func (b *bodyBudget) wake() {
	for len(b.waiters) > 0 && b.waiters[0].n <= b.free {
		w := b.waiters[0]
		b.waiters = b.waiters[1:]
		b.free -= w.n
		close(w.ready)
	}
}

// bodyReservation is one request's hold on a budget, from `limitBody` until the
// request ends. See memplan.go for why it is taken at the worst case and settled
// once the body has been read.
type bodyReservation struct {
	budget *bodyBudget

	mu   sync.Mutex
	held int64
}

type bodyReservationKey struct{}

func reservationOf(ctx context.Context) *bodyReservation {
	res, _ := ctx.Value(bodyReservationKey{}).(*bodyReservation)
	return res
}

// settle weighs the read body: false when its real weight exceeds the whole
// budget, which no amount of waiting could make room for. Otherwise the reserve
// beyond the weight goes back to the budget at once.
func (r *bodyReservation) settle(weight int64) bool {
	if weight > r.budget.total {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if weight < r.held {
		r.budget.put(r.held - weight)
		r.held = weight
	}
	return true
}

func (r *bodyReservation) done() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.held > 0 {
		r.budget.put(r.held)
		r.held = 0
	}
}
