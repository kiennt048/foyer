package apistore_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// Controller visibility is derived from the binding, not from the probe.
//
// **A world of controllers with one vendor site each cannot tell those two rules
// apart**, and neither can a world with one site in it. Either predicate answers
// such a world identically, so a test built on one is green whichever rule is
// implemented — `docs/rules/testing.md`'s *a fixture world that cannot express a
// property makes every check of that property vacuous*, of which single-site
// fixture blindness is the familiar case.
//
// So this file seeds the two shapes that separate them, in a world that already has
// two populated sites:
//
//   - **probed and bound to nothing** — one vendor site, no binding. Because
//     `UniFiDriver.Probe` reports a controller's own sites, a probe-shaped predicate
//     stops matching it anywhere, including the site whose screen carries
//     `discoverFromController` — the only call that would bind it.
//   - **bound to site A with a second vendor site nobody has adopted.** The same
//     mistake pointing the other way: a predicate that asks whether *some* vendor
//     site is unbound lists the café's controller on the hotel's screen, next to
//     Delete and Discover.
//
// One controller is right under both wrong rules and one is wrong under each, which is
// what makes the pair a measurement rather than a preference.

// controllerScope is the four-controller world these tests share.
type controllerScope struct {
	*world
	// neverProbed has no vendor sites at all — registered, nothing has reached it.
	neverProbed string
	// probedUnbound has a vendor site and no binding.
	probedUnbound string
	// boundToA is adopted into site A and nothing else.
	boundToA string
	// partlyBound is adopted into site A and has a second vendor site that nobody
	// has adopted.
	partlyBound string
}

func newControllerScope(t *testing.T) *controllerScope {
	t.Helper()
	w := newWorld(t)
	ctx := context.Background()
	at := time.Now().UTC()

	// The harness already seeds one controller and never probes it.
	s := &controllerScope{world: w, neverProbed: w.controllerID}

	// `controllers` is `UNIQUE (tenant_id, name)` and every world in this package
	// shares one schema and one tenant, so the name carries the id. Restating that
	// constraint here rather than colliding with a sibling test is the same reason
	// `world` hands out its own address block.
	register := func(what string, sites []store.VendorSite) string {
		id := uniq("ctl")
		c := &store.Controller{
			ID: id, Name: what + " " + id, Vendor: "unifi",
			BaseURL: "https://192.0.2.20:8443", AuthMode: "api-key", TLSMode: "trust-on-first-use",
		}
		if err := w.st.Controllers().Create(ctx, c, []byte(randomString())); err != nil {
			t.Fatalf("creating %s: %v", what, err)
		}
		// A probe is what records the controller's own sites, which is the whole
		// mechanism this file is about: a controller nothing has probed has none.
		if err := w.st.Controllers().RecordProbe(ctx, id, store.ControllerProbe{
			Reachability: string(gen.NasReachabilityOk),
			Version:      ptr("9.4.19"),
			// `false`, so this controller also raises a
			// `controller-version-unsupported` alarm. The dashboard scopes those
			// with the same predicate, so a second copy of the rule there would hide
			// the alarm along with the row.
			VersionSupported: ptr(false),
			VendorSites:      sites,
		}, at); err != nil {
			t.Fatalf("probing %s: %v", what, err)
		}
		return id
	}

	s.probedUnbound = register("Probed, adopted by nobody", []store.VendorSite{{ID: "default", Name: "Default"}})
	s.boundToA = register("Adopted into site A", []store.VendorSite{{ID: "default", Name: "Default"}})
	s.partlyBound = register("Adopted into site A, second site free", []store.VendorSite{
		{ID: "default", Name: "Default"},
		{ID: "annexe", Name: "Annexe"},
	})

	for _, id := range []string{s.boundToA, s.partlyBound} {
		if err := w.st.Controllers().BindVendorSite(ctx, id, "default", &w.siteA); err != nil {
			t.Fatalf("binding %s to site A: %v", id, err)
		}
	}
	return s
}

// listed is the ids the port answers for one site.
func (s *controllerScope) listed(t *testing.T, siteID string) map[string]bool {
	t.Helper()
	rows, err := s.adapter.ControllersInSite(context.Background(), siteID)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, c := range rows {
		ids[c.Id] = true
	}
	return ids
}

func TestAControllerIsScopedByItsBindingAndNotByItsProbe(t *testing.T) {
	s := newControllerScope(t)

	a := s.listed(t, s.siteA)
	b := s.listed(t, s.siteB)

	// The state with a next step in it, and the step is on this screen.
	if !b[s.probedUnbound] {
		t.Error("a probed controller bound to no site is invisible from site B, so the screen carrying the action that would bind it cannot be reached")
	}
	if !a[s.probedUnbound] {
		t.Error("a probed controller bound to no site is invisible from site A either, so it is invisible everywhere")
	}
	if !b[s.neverProbed] || !a[s.neverProbed] {
		t.Error("a registered, never-probed controller is not listed everywhere, so registration leads nowhere")
	}

	// The sharper half: this is a management object with Delete
	// and Discover beside it, on the screen of a venue that does not own it.
	if b[s.partlyBound] {
		t.Error("a controller adopted into site A is listed under site B because one of its vendor sites is unadopted")
	}
	if b[s.boundToA] {
		t.Error("a controller adopted into site A is listed under site B")
	}

	// The positives, so none of the above is satisfied by a list that is empty.
	for _, id := range []string{s.boundToA, s.partlyBound} {
		if !a[id] {
			t.Errorf("controller %s is adopted into site A and is missing from it", id)
		}
	}
	if len(b) == 0 {
		t.Fatal("site B lists no controllers at all, which passes every negative above for the wrong reason")
	}
}

// TestTheProbeShapedAndPerVendorSitePredicatesGetOneOfThesePairWrong re-injects
// the two plausible wrong rules from the data rather than trusting that somebody
// watched them fail.
//
// Both rules are written out below and asked the same two questions the port was
// just asked. Each gets exactly one of them wrong — and the test says **which**,
// because a variant that failed for some third reason would otherwise look like
// evidence.
//
// It is the pair that matters. Either question alone is answered correctly by one of
// the two wrong rules, so a test carrying only one of them would be satisfied by
// swapping one defect for the other, which is picking a favourite rather than
// removing a copy.
func TestTheProbeShapedAndPerVendorSitePredicatesGetOneOfThesePairWrong(t *testing.T) {
	s := newControllerScope(t)
	rows, err := s.st.Controllers().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]store.Controller{}
	for _, c := range rows {
		byID[c.ID] = c
	}

	boundToSite := func(c store.Controller, siteID string) bool {
		for _, vs := range c.VendorSites {
			if vs.BoundSiteID != nil && *vs.BoundSiteID == siteID {
				return true
			}
		}
		return false
	}

	variants := []candidateRule{
		{
			// Probe-shaped: `len(VendorSites) == 0` written where "bound to
			// nothing" is meant, and a probe that reports vendor sites pulls the
			// two apart.
			name: "probe-shaped",
			serves: func(c store.Controller, siteID string) bool {
				return siteID == "" || len(c.VendorSites) == 0 || boundToSite(c, siteID)
			},
			wrongAbout: s.probedUnbound,
			want:       true,
		},
		{
			// Per vendor site, so one unadopted site makes the whole controller
			// everybody's.
			name: "per-vendor-site",
			serves: func(c store.Controller, siteID string) bool {
				if len(c.VendorSites) == 0 {
					return true
				}
				for _, vs := range c.VendorSites {
					if vs.BoundSiteID == nil || *vs.BoundSiteID == siteID {
						return true
					}
				}
				return false
			},
			wrongAbout: s.partlyBound,
			want:       false,
		},
	}

	for _, v := range variants {
		c, ok := byID[v.wrongAbout]
		if !ok {
			t.Fatalf("%s: controller %s is not in the world", v.name, v.wrongAbout)
		}
		if got := v.serves(c, s.siteB); got == v.want {
			t.Errorf("%s answers %v for %s under site B — the same answer the product gives, so this world does not separate that rule from the product's",
				v.name, got, v.wrongAbout)
		}
		// And the other member of the pair, which that rule gets right. Without
		// this the variant could be failing for any reason at all.
		right := v.rightAbout(s)
		if got := v.serves(byID[right], s.siteB); got != !v.want {
			t.Errorf("%s answers %v for %s under site B, which is not the half it was supposed to get right — the transcription is wrong",
				v.name, got, right)
		}
	}
}

// candidateRule is one of the candidate visibility predicates, transcribed so the
// world can be asked whether it separates it from the others.
type candidateRule struct {
	name string
	// serves is the rule, transcribed.
	serves func(store.Controller, string) bool
	// wrongAbout is the controller it answers incorrectly for site B, and want is
	// the answer the product gives.
	wrongAbout string
	want       bool
}

// rightAbout is the member of the pair this rule answers correctly: the
// per-vendor-site rule lists the probed-unbound controller, the probe-shaped rule
// hides the partly-bound one.
func (v candidateRule) rightAbout(s *controllerScope) string {
	if v.wrongAbout == s.probedUnbound {
		return s.partlyBound
	}
	return s.probedUnbound
}

// TestControllerAlarmsAreScopedLikeTheList is the second call site, and it is the
// reason the predicate is a function.
//
// A `dashboard.go` with its own transcription of the rule — `docs/rules/database.md`'s
// second-source-of-truth defect in Go — would hide the controller from the list and
// suppress the alarm that would say something is wrong with it. An alarm
// the operator cannot find a row for is an alarm they cannot act on; the inverse,
// which is what this asserts, is that every controller alarm has a row.
func TestControllerAlarmsAreScopedLikeTheList(t *testing.T) {
	s := newControllerScope(t)
	ctx := context.Background()

	for _, siteID := range []string{s.siteA, s.siteB} {
		alarms, err := s.adapter.Alarms(ctx, siteID)
		if err != nil {
			t.Fatal(err)
		}
		listed := s.listed(t, siteID)
		seen := map[string]bool{}
		for _, al := range alarms {
			if al.Kind != gen.AlarmKindControllerVersionUnsupported || al.Subject == nil || al.Subject.Id == nil {
				continue
			}
			seen[*al.Subject.Id] = true
			if !listed[*al.Subject.Id] {
				t.Errorf("site %s alarms about controller %s and does not list it, so the operator has nothing to act on", siteID, *al.Subject.Id)
			}
		}
		// Every one of the three probed controllers reports
		// `versionSupported: false`, so the ones this site lists must all alarm.
		for _, id := range []string{s.probedUnbound, s.boundToA, s.partlyBound} {
			if listed[id] && !seen[id] {
				t.Errorf("site %s lists controller %s on an unsupported version and raises no alarm about it", siteID, id)
			}
		}
		if len(seen) == 0 {
			t.Errorf("site %s raised no controller alarm at all, so the assertion above is vacuous", siteID)
		}
	}
}
