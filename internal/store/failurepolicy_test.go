package store_test

import (
	"context"
	"testing"

	"github.com/kiennt048/foyer/internal/aaa"
	"github.com/kiennt048/foyer/internal/store"
)

// A site nobody has configured is fail-closed, and it is fail-closed at the schema
// level rather than only in Go.
//
// `aaa.DefaultPolicy` already answers fail-closed for a site it cannot resolve. The
// column default matters anyway: a row inserted by something that never heard of
// that function -- a restore, a fixture, a future admin API -- is still fail-closed.
func TestANewSiteIsFailClosed(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	policies, err := st.Sites().SiteFailurePolicies(ctx)
	fatalf(t, err, "reading failure policies")

	found := false
	for _, p := range policies {
		if p.SiteID != site.ID {
			continue
		}
		found = true
		if p.GuestFailureMode != aaa.FailClosed {
			t.Fatalf("a site nobody configured is %q", p.GuestFailureMode)
		}
		if p.GuestGraceSeconds != aaa.DefaultGraceSeconds {
			t.Fatalf("grace is %d, want %d", p.GuestGraceSeconds, aaa.DefaultGraceSeconds)
		}
	}
	if !found {
		t.Fatalf("site %s is missing from the policy list", site.ID)
	}
}

// The store satisfies aaa.FailurePolicyStore, and `configured` flips because of it.
//
// That boolean is the whole reason this method returns every site at once: "every
// site is fail-closed" and "nothing is configured, so every site is fail-closed"
// render identically to an operator reading a generated config, and they are not
// the same fact.
func TestTheStoreSatisfiesTheFailurePolicyInterface(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	seedSite(t, st)

	if _, ok := any(st.Sites()).(aaa.FailurePolicyStore); !ok {
		t.Fatal("SiteRepo does not satisfy aaa.FailurePolicyStore")
	}

	advice, configured, err := aaa.FailurePolicies(ctx, st.Sites(), 0)
	fatalf(t, err, "resolving advice")
	if !configured {
		t.Fatal("configured is false with a real store attached; the generated config cannot tell " +
			"'every site is fail-closed' from 'nothing is configured'")
	}
	if len(advice) == 0 {
		t.Fatal("no advice for any site")
	}
}

func TestFailurePolicyRoundTripsAndClamps(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	read := func(t *testing.T) aaa.SiteFailurePolicy {
		t.Helper()
		policies, err := st.Sites().SiteFailurePolicies(ctx)
		fatalf(t, err, "reading failure policies")
		for _, p := range policies {
			if p.SiteID == site.ID {
				return p
			}
		}
		t.Fatalf("site %s vanished", site.ID)
		return aaa.SiteFailurePolicy{}
	}

	t.Run("fail-open is stored", func(t *testing.T) {
		fatalf(t, st.Sites().SetFailurePolicy(ctx, site.ID, aaa.FailOpen, 600), "setting fail-open")
		p := read(t)
		if p.GuestFailureMode != aaa.FailOpen || p.GuestGraceSeconds != 600 {
			t.Fatalf("stored %q/%d", p.GuestFailureMode, p.GuestGraceSeconds)
		}
	})

	t.Run("a grace below the floor is clamped, not refused", func(t *testing.T) {
		fatalf(t, st.Sites().SetFailurePolicy(ctx, site.ID, aaa.FailOpen, 5), "setting a tiny grace")
		if p := read(t); p.GuestGraceSeconds != store.MinGraceSeconds {
			t.Fatalf("grace is %d, want the floor %d", p.GuestGraceSeconds, store.MinGraceSeconds)
		}
	})

	t.Run("a grace above the ceiling is clamped", func(t *testing.T) {
		fatalf(t, st.Sites().SetFailurePolicy(ctx, site.ID, aaa.FailOpen, 99999), "setting a huge grace")
		if p := read(t); p.GuestGraceSeconds != aaa.MaxGraceSeconds {
			t.Fatalf("grace is %d, want the ceiling %d", p.GuestGraceSeconds, aaa.MaxGraceSeconds)
		}
	})

	t.Run("an unknown mode is a named refusal", func(t *testing.T) {
		err := st.Sites().SetFailurePolicy(ctx, site.ID, aaa.FailureMode("fail-sometimes"), 300)
		if err == nil {
			t.Fatal("an unknown failure mode was accepted")
		}
		if p := read(t); p.GuestFailureMode != aaa.FailOpen {
			t.Fatalf("the refused write changed the stored mode to %q", p.GuestFailureMode)
		}
	})

	// The rule the columns exist to serve, and the one they must never be read as
	// overriding. A venue may choose to wave guests through while the control plane
	// is blind; it may not choose that for a corporate supplicant.
	t.Run("802.1X ignores fail-open entirely", func(t *testing.T) {
		fatalf(t, st.Sites().SetFailurePolicy(ctx, site.ID, aaa.FailOpen, 600), "setting fail-open")
		p := read(t)
		if p.For(aaa.MethodGuest) != aaa.FailOpen {
			t.Fatalf("guest resolves to %q on a fail-open site", p.For(aaa.MethodGuest))
		}
		if p.For(aaa.MethodDot1X) != aaa.FailClosed {
			t.Fatalf("802.1X resolves to %q on a fail-open site; it is never configurable",
				p.For(aaa.MethodDot1X))
		}
	})
}

// The distinction the whole three-outcome model rests on, driven end to end through
// the policy engine rather than asserted against the sentinel.
//
// A wrong voucher code and a dead database both come back from `LookupVoucher` as a
// non-nil error. Only `IsIdentityNotFound` separates them, and the two halves below
// are the two ways of getting it wrong:
//
//   - Absence read as an outage answers 503, which reaches the failure branch. On a
//     fail-open site that is a typo being let on to the network.
//   - An outage read as absence mints an authoritative Access-Reject while the
//     control plane knows nothing, which bypasses the failure model entirely.
//
// Neither half can catch both. Both halves are asserted.
func TestAbsenceAndOutageAreDifferentAnswers(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	engine := aaa.NewEngine(st, 0)

	t.Run("a code that does not exist is an authoritative denial", func(t *testing.T) {
		decision, err := engine.Authorize(ctx, aaa.Request{UserName: "NOSUCHCODE99"})
		if err != nil {
			t.Fatalf("a nonexistent voucher answered undecided (%v).\n"+
				"On a fail-open site that is a guest typing a wrong code and being let on.", err)
		}
		if decision == nil || decision.Allow {
			t.Fatalf("a nonexistent voucher was allowed: %+v", decision)
		}
	})

	t.Run("a database that cannot answer is undecided", func(t *testing.T) {
		// The pool is closed under the engine: the same call, the same code path,
		// a store that cannot look rather than one that looked and found nothing.
		st.Close()

		decision, err := engine.Authorize(ctx, aaa.Request{UserName: "NOSUCHCODE99"})
		if err == nil {
			t.Fatalf("a dead database produced an authoritative answer (%+v).\n"+
				"That is an Access-Reject minted while the control plane knows nothing, "+
				"and it bypasses the per-site failure model completely.", decision)
		}
		if decision != nil {
			t.Fatalf("undecided came with a decision attached: %+v", decision)
		}
	})
}

// The reporter itself, at the level the interface is written at.
func TestIsIdentityNotFoundSeparatesAbsenceFromFailure(t *testing.T) {
	st := appStore(t)

	if _, ok := any(st).(aaa.IdentityNotFoundReporter); !ok {
		t.Fatal("*store.Store does not satisfy aaa.IdentityNotFoundReporter")
	}
	if !st.IsIdentityNotFound(store.ErrNotFound) {
		t.Fatal("ErrNotFound is not reported as an absent identity")
	}
	if st.IsIdentityNotFound(store.ErrConflict) {
		t.Fatal("a conflict is reported as an absent identity")
	}
	if st.IsIdentityNotFound(context.DeadlineExceeded) {
		t.Fatal("a timeout is reported as an absent identity; a slow database would mint denials")
	}
	if st.IsIdentityNotFound(nil) {
		t.Fatal("a nil error is reported as an absent identity")
	}
}
