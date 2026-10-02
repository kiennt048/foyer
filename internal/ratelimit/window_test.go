package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestAWindowAllowsItsLimitThenNothingUntilItIsOver(t *testing.T) {
	w := NewWindow()
	now := time.Unix(1_000_000, 0)
	for i := 0; i < 3; i++ {
		if !w.Allow("k", 3, time.Hour, now.Add(time.Duration(i)*time.Minute)) {
			t.Fatalf("event %d inside the limit was refused", i+1)
		}
	}
	// A token bucket would have refilled by now; a window does not.
	if w.Allow("k", 3, time.Hour, now.Add(59*time.Minute)) {
		t.Fatal("the fourth event inside the hour was allowed")
	}
	if !w.Allow("k", 3, time.Hour, now.Add(time.Hour)) {
		t.Fatal("the window that filled is over and the next event was still refused")
	}
}

// TestAFullWindowRefusesNewcomersRatherThanEvicting is the bound the private copy
// in `otp` did not have: one entry per MAC anybody names, unbounded until the
// hourly sweep.
func TestAFullWindowRefusesNewcomersRatherThanEvicting(t *testing.T) {
	w := NewWindow()
	w.maxKeys = 3
	now := time.Unix(1_000_000, 0)
	for i := 0; i < 3; i++ {
		if !w.Allow(fmt.Sprint(i), 1, time.Hour, now) {
			t.Fatalf("key %d refused below the bound", i)
		}
	}
	if w.Allow("fresh", 1, time.Hour, now) {
		t.Fatal("a fourth key was admitted past the bound")
	}
	if w.Len() != 3 {
		t.Fatalf("held %d keys, want 3: an existing key was evicted", w.Len())
	}
	// And a held key's own window still answers for it.
	if w.Allow("0", 1, time.Hour, now) {
		t.Fatal("key 0 had spent its window and was allowed again")
	}
	// Once the windows are over they are swept and newcomers get in.
	if !w.Allow("fresh", 1, time.Hour, now.Add(time.Hour)) {
		t.Fatal("after every window ended a new key was still refused")
	}
}
