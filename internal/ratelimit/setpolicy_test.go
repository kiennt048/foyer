package ratelimit

import (
	"testing"
	"time"
)

// TestSetPolicyReshapesWithoutRefunding — an operator's change in the console binds
// at once (a lowered burst caps what a caller holds) and hands nobody a fresh
// budget (a raised one is refilled towards, not jumped to). Replacing the limiter
// would have forgotten every bucket: a reset anyone could have an operator trigger.
func TestSetPolicyReshapesWithoutRefunding(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := New(Policy{Burst: 10, Every: time.Minute})
	for range 2 {
		l.Allow("k", now)
	}

	l.SetPolicy(Policy{Burst: 1, Every: time.Minute}, now)
	if ok, _ := l.Allow("k", now); !ok {
		t.Fatal("a caller holding eight tokens under a new burst of one was refused its one")
	}
	if ok, _ := l.Allow("k", now); ok {
		t.Fatal("the lowered burst did not bind on the next attempt")
	}

	l.SetPolicy(Policy{Burst: 100, Every: time.Minute}, now)
	if ok, _ := l.Allow("k", now); ok {
		t.Error("raising the burst refunded an empty bucket")
	}
	if ok, _ := l.Allow("k", now.Add(time.Minute)); !ok {
		t.Error("the raised policy does not refill")
	}
	if p := l.Policy(); p.Burst != 100 {
		t.Errorf("policy in force %+v", p)
	}
}
