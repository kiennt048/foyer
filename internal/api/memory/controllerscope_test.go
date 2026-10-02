package memory

import (
	"context"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The oracle's copy of the visibility rule, pinned.
//
// `make diff-api` compares this store against `internal/apistore`, so a rule that
// differs here is reported as a divergence in the product when it is a divergence in
// the harness — `docs/rules/testing.md`'s false positive, which costs somebody a
// day fixing a defect that is not there. The rule itself is stated once, on
// `apistore.controllerServesSite`.
//
// The world below is the smallest one that can tell the two shipped predicates
// apart. A controller with one vendor site satisfies all of them identically, so a
// world built only from those is green whichever rule is in place.

type vendorSite = struct {
	BoundSiteId *string `json:"boundSiteId,omitempty"`
	Id          string  `json:"id"`
	Name        string  `json:"name"`
}

func site(id string) *string { return &id }

func TestControllersAreScopedByTheirBinding(t *testing.T) {
	s := NewStore(&World{Controllers: []gen.Controller{
		// Registered, nothing has reached it. Belongs everywhere or registration
		// leads nowhere.
		{Id: "ctl-registered", VendorSites: []vendorSite{}},
		// Probed and adopted by nobody. An earlier predicate asked
		// `len(VendorSites) == 0`, so recording a controller's own sites removed it
		// from every site view — including the one carrying the call that binds it.
		{Id: "ctl-probed-unbound", VendorSites: []vendorSite{{Id: "default", Name: "Default"}}},
		// Adopted into site A.
		{Id: "ctl-a", VendorSites: []vendorSite{{Id: "default", Name: "Default", BoundSiteId: site("site-a")}}},
		// Adopted into site A, with a second vendor site nobody has adopted.
		// A rule that asks whether *some* vendor site is free would put this one
		// on site B's screen beside Delete.
		{Id: "ctl-a-partly", VendorSites: []vendorSite{
			{Id: "default", Name: "Default", BoundSiteId: site("site-a")},
			{Id: "annexe", Name: "Annexe"},
		}},
	}})

	listed := func(siteID string) map[string]bool {
		rows, err := s.ControllersInSite(context.Background(), siteID)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]bool{}
		for _, c := range rows {
			ids[c.Id] = true
		}
		return ids
	}

	want := map[string]map[string]bool{
		"site-a": {"ctl-registered": true, "ctl-probed-unbound": true, "ctl-a": true, "ctl-a-partly": true},
		"site-b": {"ctl-registered": true, "ctl-probed-unbound": true, "ctl-a": false, "ctl-a-partly": false},
		// No scope at all is every controller: the console always asks with one,
		// and the differential harness asks without.
		"": {"ctl-registered": true, "ctl-probed-unbound": true, "ctl-a": true, "ctl-a-partly": true},
	}
	for siteID, expected := range want {
		got := listed(siteID)
		for id, in := range expected {
			if got[id] != in {
				t.Errorf("site %q: listed(%s) = %v, want %v", siteID, id, got[id], in)
			}
		}
	}
}
