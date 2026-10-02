package portal

import (
	"context"
	"testing"
)

// The driver seam is handed the vendor's site, not Foyer's.
//
// `Drivers.Authorize` took only the grant, and a grant records what the venue
// decided in Foyer's own terms: its `SiteID` is the Foyer site. The driver takes
// the controller's — UniFi's `SiteID` field — so the adapter went and resolved the
// access point for itself on every authorize call, which is the conflation
// `ports.go` exists to warn about and the lookup `Verify` had already done.
//
// Before the placement was passed:
//
//	placement_test.go:25: the driver was handed site "site-one"
//
// where the controller knows that venue as `default`.
func TestTheSeamHandsOverTheVendorSiteAndTheController(t *testing.T) {
	r := newRig(t, present(), nil)
	place := Placement{
		SiteID: "site-one", NASID: "nas-1",
		VendorSiteID: "default", ControllerID: "ctl-lobby",
	}
	r.svc.ports.Sites = fakeSites{place: place}

	// A session minted at the front door carries the placement it was resolved
	// with, which is what a session read back out of the store has to carry too.
	sess := r.session
	sess.VendorSiteID = place.VendorSiteID
	sess.ControllerID = place.ControllerID

	out := r.svc.Redeem(context.Background(), sess, "ABCDEFGHJKMN")
	if out.Authorize == nil {
		t.Fatal("no authorize call was produced")
	}
	if err := out.Authorize(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(r.driver.placements) != 1 {
		t.Fatalf("the driver was called %d times", len(r.driver.placements))
	}
	got := r.driver.placements[0]
	if got.VendorSiteID != "default" {
		t.Errorf("the driver was handed vendor site %q; the controller knows this venue as %q",
			got.VendorSiteID, "default")
	}
	if got.ControllerID != "ctl-lobby" {
		t.Errorf("the driver was handed controller %q, so it cannot tell which controller to ask", got.ControllerID)
	}
	// And Foyer's site is still on the grant, where the database keys on it.
	if r.driver.authorized[0].SiteID != "site-one" {
		t.Errorf("the grant names site %q, and the database keys on Foyer's", r.driver.authorized[0].SiteID)
	}
}

// One placement, resolved once, used by both calls.
//
// `VerifyPresence` is the precondition for a grant (`security.md` §4) and
// `AuthorizeGuest` is what acts on it. Resolving the controller separately for each
// leaves a window in which the two are different controllers — a precondition
// checked against one thing and acted on against another is not a precondition.
func TestVerifyAndAuthorizeAgreeOnWhereTheGuestIs(t *testing.T) {
	r := newRig(t, present(), nil)
	place := Placement{
		SiteID: "site-one", NASID: "nas-1",
		VendorSiteID: "default", ControllerID: "ctl-lobby",
	}
	r.svc.ports.Sites = fakeSites{place: place}

	sess := r.session
	sess.VendorSiteID = place.VendorSiteID
	sess.ControllerID = place.ControllerID

	if v := r.svc.Presence(context.Background(), sess); v != VerdictProceed {
		t.Fatalf("presence: %v", v)
	}
	verified := r.driver.verified

	out := r.svc.Redeem(context.Background(), sess, "ABCDEFGHJKMN")
	if err := out.Authorize(context.Background()); err != nil {
		t.Fatal(err)
	}

	if verified.Placement() != r.driver.placements[0] {
		t.Errorf("presence was confirmed at %+v and the guest was authorized at %+v",
			verified.Placement(), r.driver.placements[0])
	}
}

// A limit nobody is sampling is `unenforced`, and it could not be said before.
//
// With only two inputs to `enforcement` — the plan and the driver's `HardQuota` — a
// limited plan behind a driver that cannot enforce it would always be
// `foyer-sampled`. That is a claim about accounting *arriving*: `radacct` byte counters advance only
// on Interim-Update or Stop, and a NAS sends none unless the server asks in an
// Access-Accept. On the controller-API path there is no Access-Accept at all, so
// `radacct` rows exist only where an operator separately attached an accounting
// profile — the fact `docs/rules/radius.md` says to assert at NAS registration.
//
// The portal would tell a guest a 500 MB allowance was being sampled every five
// minutes on a device from which no byte counter will ever arrive. `frontend.md`:
// a figure with no freshness is the portal promising a cap that nothing on the
// network applies.
//
// The measurement exists — `radius_clients.accounting_configured`, written by the
// NAS probe — and the placement carries it.
func TestALimitNobodyIsSamplingIsUnenforced(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name       string
		accounting AccountingExpectation
		want       Enforcement
	}{
		{"a probe found accounting configured", AccountingArrives, EnforcedBySampling},
		{"a probe found none", AccountingAbsent, EnforcedByNothing},
		// Never measured. `foyer-sampled` is the guess, and it is the guess the
		// rule asks to be closed at NAS registration rather than here: reporting
		// `unenforced` for a device nobody has probed would tell an operator that
		// a working quota is not being applied.
		{"nobody has measured it", AccountingUnknown, EnforcedBySampling},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, present(), nil)
			r.driver.hardQuota = false
			place := Placement{SiteID: "site-one", NASID: "nas-1", Accounting: tc.accounting}
			r.svc.ports.Sites = fakeSites{place: place}

			sess := r.session
			sess.Accounting = tc.accounting

			out := r.svc.Agree(ctx, sess, true, 30)
			if out.Grant == nil {
				t.Fatalf("no grant: %+v", out)
			}
			if out.Grant.Plan.EnforcedBy != tc.want {
				t.Errorf("enforcedBy is %q, want %q", out.Grant.Plan.EnforcedBy, tc.want)
			}
		})
	}

	// And a NAS that enforces the plan itself is unaffected: `radacct` is
	// reporting rather than enforcement there, so accounting not arriving does
	// not mean the limit is not applied.
	t.Run("hard quota does not depend on accounting", func(t *testing.T) {
		r := newRig(t, present(), nil)
		r.driver.hardQuota = true
		sess := r.session
		sess.Accounting = AccountingAbsent
		out := r.svc.Agree(ctx, sess, true, 30)
		if out.Grant.Plan.EnforcedBy != EnforcedByNAS {
			t.Errorf("enforcedBy is %q behind a NAS that enforces it, want %q", out.Grant.Plan.EnforcedBy, EnforcedByNAS)
		}
	})
}
