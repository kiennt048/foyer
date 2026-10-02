package ratelimit

import (
	"sync"
	"time"
)

// Window counts events per key in fixed windows: `limit` in any one `per`, and
// then nothing until the window that filled is over.
//
// **The second shape this package offers, and the reason there are two.** A
// `Limiter` bounds *attempts*: it refills continuously, so an operator who mistypes
// waits seconds, never an hour. A code send wants the opposite — a handful an hour,
// then nothing until the hour is up — because each one costs the venue money, and a
// bucket of five that refills one every twelve minutes lets ten through in the
// first hour. Twilio Verify's and Authgear's per-phone limits are windows for the
// same reason. `internal/identity/otp` carries its per-device and per-address
// limits here; its per-number limit and daily ceiling stay in PostgreSQL, counted in
// the transaction that records the code, because those bound what the venue pays
// and must survive a restart and be shared by every replica.
//
// A private copy with no bound on its map would be the unbounded
// pre-authentication state a limiter exists to bound. It is bounded here by the same rule as `Limiter` (CONTRACT.md invariant
// 3): full of live keys, a **new** key is refused and no existing key is evicted.
//
// The limit and the period are per call, not per Window, so a venue that raised a
// limit in the console binds on its next event with nothing rebuilt.
type Window struct {
	mu        sync.Mutex
	counts    map[string]*window
	lastSweep time.Time
	maxKeys   int
}

type window struct {
	start time.Time
	n     int
}

// NewWindow returns an empty counter bounded at DefaultMaxKeys.
func NewWindow() *Window { return &Window{counts: map[string]*window{}, maxKeys: DefaultMaxKeys} }

// Allow records one event against key and reports whether it was within limit per
// period. A refused event is not counted, so a device that keeps asking is not
// locked out for longer than the window it filled.
func (w *Window) Allow(key string, limit int, per time.Duration, at time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.counts == nil {
		w.counts = map[string]*window{}
	}
	if w.maxKeys == 0 {
		w.maxKeys = DefaultMaxKeys
	}
	w.sweep(per, at)
	c, ok := w.counts[key]
	if !ok || at.Sub(c.start) >= per {
		if !ok && len(w.counts) >= w.maxKeys {
			// Evicting would let a flood of fresh keys reset any window.
			return false
		}
		c = &window{start: at}
		w.counts[key] = c
	}
	if c.n >= limit {
		return false
	}
	c.n++
	return true
}

// Len is how many keys are held.
func (w *Window) Len() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.counts)
}

// sweep drops windows that are over, at most once per period.
func (w *Window) sweep(per time.Duration, at time.Time) {
	if at.Sub(w.lastSweep) < per {
		return
	}
	w.lastSweep = at
	for k, c := range w.counts {
		if at.Sub(c.start) >= per {
			delete(w.counts, k)
		}
	}
}
