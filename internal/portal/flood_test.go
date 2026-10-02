package portal

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/ratelimit"
)

// Somebody within radio range could fill a MAC-keyed rate-limit table with MACs
// they typed into the query string, and every real guest who arrived afterwards
// would be refused. These drive the service the
// way that attack does — thousands of sessions claiming MACs nobody vouched for —
// and then ask whether a real guest still gets on.

// rosterDriver vouches for the MACs it is told about and nobody else, and counts
// how often it was asked: the controller is what the limiter protects first.
type rosterDriver struct {
	mu      sync.Mutex
	present map[string]bool
	asked   int
}

func (d *rosterDriver) Verify(_ context.Context, s Session) (nas.Presence, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asked++
	if d.present[s.ClientMAC] {
		return nas.Presence{OK: true, Outcome: nas.PresenceVerified}, nil
	}
	return nas.Presence{Outcome: nas.PresenceNotFound}, nil
}

func (d *rosterDriver) Authorize(context.Context, Placement, Grant) error { return nil }
func (d *rosterDriver) HardQuota(context.Context, Session) bool           { return false }

func (d *rosterDriver) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.asked
}

const (
	realGuestMAC = "aa:bb:cc:dd:ee:01"
	realGuestIP  = "192.0.2.10:50100"
	attackerIP   = "192.0.2.66:40000"
)

type floodRig struct {
	svc    *Service
	lim    *TokenBucket
	driver *rosterDriver
	now    time.Time
}

func newFloodRig(t *testing.T, present ...string) *floodRig {
	t.Helper()
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	store.Now = func() time.Time { return now }
	sixty := 60
	d := &rosterDriver{present: map[string]bool{}}
	for _, m := range present {
		d.present[m] = true
	}
	lim := NewTokenBucket(DefaultBurst, DefaultRefill)
	svc, err := NewService(ServiceConfig{
		Ports: Ports{
			Sessions: store, Grants: store, Authorizations: store,
			Credentials: &fakeCredentials{code: "ABCDEFGHJKMN", plan: Plan{Name: "1h", DurationMinutes: &sixty}},
			Sites:       fakeSites{place: Placement{SiteID: "site-one", NASID: "nas-1"}},
			Drivers:     d,
			Limiter:     lim,
		},
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	return &floodRig{svc: svc, lim: lim, driver: d, now: now}
}

func sessionFor(mac, source string, i int) Session {
	return Session{
		TokenHash: fmt.Sprintf("token-%d", i), SiteID: "site-one", NASID: "nas-1",
		ClientMAC: mac, APMAC: "11:22:33:44:55:66", Source: source,
	}
}

func randomMAC(i int) string {
	return fmt.Sprintf("02:%02x:%02x:%02x:%02x:%02x", byte(i>>32), byte(i>>24), byte(i>>16), byte(i>>8), byte(i))
}

// TestAFloodOfClaimedMACsDoesNotLockARealGuestOut is the acceptance test: fill
// the table with random MACs, and a real guest still signs in.
//
// More claims than the limiter has keys, from one laptop and then from a spread
// of addresses, so both halves of the defence are exercised: one sender's claims
// are one bucket, and a table that does fill forgets its oldest entry rather than
// refusing the newcomer.
func TestAFloodOfClaimedMACsDoesNotLockARealGuestOut(t *testing.T) {
	r := newFloodRig(t, realGuestMAC)
	ctx := context.Background()
	flood := ratelimit.DefaultMaxKeys + 5_000

	// One laptop, claiming a fresh MAC every time.
	for i := 0; i < flood; i++ {
		r.svc.Redeem(ctx, sessionFor(randomMAC(i), attackerIP, i), "ABCDEFGHJKMN")
	}
	// Then as many sources as the table has room for, and more.
	for i := 0; i < flood; i++ {
		src := fmt.Sprintf("10.%d.%d.%d:1234", byte(i>>16), byte(i>>8), byte(i))
		r.svc.Look(ctx, sessionFor(randomMAC(flood+i), src, flood+i))
	}

	out := r.svc.Redeem(ctx, sessionFor(realGuestMAC, realGuestIP, -1), "ABCDEFGHJKMN")
	if !out.Connected || out.Grant == nil {
		t.Fatalf("after a flood of claimed MACs a real guest was refused (%q): the rate limit is a lockout", out.Reason)
	}
}

// TestOneSenderSprayingMACsIsOneBucket: the controller calls a presence check
// costs are bounded per connection source, not per claimed MAC. Keyed on the MAC,
// every fresh MAC was a fresh burst of calls to the venue's UniFi.
func TestOneSenderSprayingMACsIsOneBucket(t *testing.T) {
	r := newFloodRig(t)
	ctx := context.Background()
	for i := 0; i < 5_000; i++ {
		r.svc.Look(ctx, sessionFor(randomMAC(i), attackerIP, i))
	}
	if got, bound := r.driver.calls(), DefaultBurst*SourceShare; got > bound {
		t.Fatalf("one sender claiming 5000 MACs made %d controller calls; the source's burst is %d", got, bound)
	}
}

// TestNoDeviceBucketIsSpentBeforePresence: a MAC the controller did not vouch for
// never occupies a per-device bucket, so the device table is bounded by the
// devices really on the network rather than by what somebody typed.
func TestNoDeviceBucketIsSpentBeforePresence(t *testing.T) {
	r := newFloodRig(t)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		// Fifty sources, one absent MAC each, all inside their source's burst.
		src := fmt.Sprintf("198.51.100.%d:1234", i)
		if out := r.svc.Redeem(ctx, sessionFor(randomMAC(i), src, i), "ABCDEFGHJKMN"); out.Reason != ReasonNotPresent {
			t.Fatalf("claim %d: %q, want not-present", i, out.Reason)
		}
	}
	if _, device := r.lim.Len(); device != 0 {
		t.Fatalf("%d device buckets exist for MACs the controller never confirmed", device)
	}
}

// TestAStrangerClaimingAGuestsMACSpendsTheirOwnBudget: MACs are visible over the
// air, so claiming a present guest's is easy. What that must not buy is the
// guest's own attempts — the device bucket is the MAC *on its source*.
func TestAStrangerClaimingAGuestsMACSpendsTheirOwnBudget(t *testing.T) {
	r := newFloodRig(t, realGuestMAC)
	ctx := context.Background()
	for i := 0; i < DefaultBurst*3; i++ {
		// Wrong codes from the attacker's laptop, claiming the guest's MAC.
		r.svc.Redeem(ctx, sessionFor(realGuestMAC, attackerIP, i), "ZZZZZZZZZZZZ")
	}
	out := r.svc.Redeem(ctx, sessionFor(realGuestMAC, realGuestIP, -1), "ABCDEFGHJKMN")
	if !out.Connected {
		t.Fatalf("a stranger claiming the guest's MAC spent the guest's budget: %q", out.Reason)
	}
}
