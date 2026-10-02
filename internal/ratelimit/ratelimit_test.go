package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

func TestTheBurstIsSpentThenRefused(t *testing.T) {
	l := New(Policy{Burst: 3, Every: 10 * time.Second})
	now := time.Unix(1_000_000, 0)
	for i := 0; i < 3; i++ {
		if ok, _ := l.Allow("k", now); !ok {
			t.Fatalf("attempt %d inside the burst was refused", i+1)
		}
	}
	ok, wait := l.Allow("k", now)
	if ok {
		t.Fatal("the attempt after the burst was allowed")
	}
	if wait <= 0 || wait > 10*time.Second {
		t.Fatalf("retry-after = %v, want within one refill interval", wait)
	}
}

// TestItRefills: a limiter whose recovery was never exercised might be a
// permanent lockout, which for an operator's own account is worse than the
// guessing it prevents.
func TestItRefills(t *testing.T) {
	l := New(Policy{Burst: 1, Every: 10 * time.Second})
	now := time.Unix(1_000_000, 0)
	if ok, _ := l.Allow("k", now); !ok {
		t.Fatal("first attempt refused")
	}
	if ok, wait := l.Allow("k", now.Add(4*time.Second)); ok || wait != 6*time.Second {
		t.Fatalf("four seconds in: ok=%v wait=%v, want refused with 6s left", ok, wait)
	}
	if ok, _ := l.Allow("k", now.Add(10*time.Second)); !ok {
		t.Fatal("a full interval later the attempt was still refused")
	}
}

func TestKeysAreIndependentAndForgetRestores(t *testing.T) {
	l := New(Policy{Burst: 1, Every: time.Minute})
	now := time.Unix(1_000_000, 0)
	l.Allow("a", now)
	if ok, _ := l.Allow("b", now); !ok {
		t.Fatal("spending a's bucket spent b's")
	}
	if ok, _ := l.Allow("a", now); ok {
		t.Fatal("a was allowed twice")
	}
	l.Forget("a")
	if ok, _ := l.Allow("a", now); !ok {
		t.Fatal("Forget did not restore the burst")
	}
}

// TestTheMapIsBounded: one entry per key an attacker names is the unbounded state
// this package exists to bound. Past the cap a new key is refused, and an old
// one is not evicted — eviction would let a flood of fresh keys reset any bucket.
func TestTheMapIsBounded(t *testing.T) {
	l := New(Policy{Burst: 1, Every: time.Minute})
	l.maxKeys = 50
	now := time.Unix(1_000_000, 0)
	l.Allow("victim", now)
	for i := 0; i < 200; i++ {
		l.Allow(fmt.Sprintf("k%d", i), now)
	}
	if l.Len() > 50 {
		t.Fatalf("holding %d keys, cap is 50", l.Len())
	}
	if ok, _ := l.Allow("victim", now); ok {
		t.Fatal("a flood of fresh keys reset an existing bucket")
	}
	// And the sweep gives the room back once buckets have refilled.
	if ok, _ := l.Allow("late", now.Add(time.Hour)); !ok {
		t.Fatal("after every bucket refilled, a new key was still refused")
	}
}

func TestClientKey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"192.0.2.7:51000", "ip:192.0.2.7"},
		{"[::ffff:192.0.2.7]:51000", "ip:192.0.2.7"},
		{"[2001:db8:1:2:aaaa:bbbb:cccc:dddd]:443", "ip6:2001:db8:1:2::/64"},
		{"[2001:db8:1:2::1]:443", "ip6:2001:db8:1:2::/64"},
		{"not-an-address", "ip:not-an-address"},
	} {
		if got := ClientKey(tc.in); got != tc.want {
			t.Errorf("ClientKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAnEvictingLimiterNeverRefusesANewcomer checks, at the bottom of the stack,
// that a full table does not become a lockout of whoever arrives next. The least
// recently seen key is the one forgotten, so a key in use survives a flood that
// only ever touches fresh keys.
func TestAnEvictingLimiterNeverRefusesANewcomer(t *testing.T) {
	l := NewEvicting(Policy{Burst: 2, Every: time.Minute})
	l.maxKeys = 50
	now := time.Unix(1_000_000, 0)
	l.Allow("regular", now)
	for i := 0; i < 200; i++ {
		// The regular keeps using its bucket through the flood, so it is never
		// the least recently seen.
		if i%10 == 0 {
			l.Allow("regular", now)
		}
		if ok, _ := l.Allow(fmt.Sprintf("flood%d", i), now); !ok {
			t.Fatalf("flood key %d was refused: a full evicting limiter refused a newcomer", i)
		}
	}
	if l.Len() > 50 {
		t.Fatalf("holding %d keys, cap is 50", l.Len())
	}
	if ok, _ := l.Allow("real-guest", now); !ok {
		t.Fatal("after a flood of fresh keys a real newcomer was refused — the bound is a lockout")
	}
	// The regular's bucket was not reset by the flood: it spent its tokens and is
	// still refused.
	if ok, _ := l.Allow("regular", now); ok {
		t.Fatal("the flood evicted a key that was in use, resetting its bucket")
	}
}
