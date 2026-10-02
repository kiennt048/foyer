package apistore_test

import (
	"context"
	"net/netip"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// `no-radius-configured` is one kind carrying two situations, and Go has to be able
// to tell them apart.
//
// **No device is registered in this site at all** is the first-run state, and the
// answer is to register one. **Devices are registered and none of them has a shared
// secret** is either a deliberate controller-API deployment or the single most
// common real fault in the product wearing a different coat — a wrong or missing
// secret, which FreeRADIUS answers by discarding the packet, so there is no
// `radpostauth` row for anything downstream to report.
//
// The kind stays the same for both, because `AuthBlindSpotKind` is a closed enum
// with no member for *no devices* and both situations are honestly "no RADIUS
// configured". What separates them is `params.deviceCount`, exactly as
// `before-retention` already carries `retentionDays`; `params` is
// `additionalProperties: true`, so this needs no contract change.
//
// **A count that is absent is not zero.** `web/admin`'s panel branches on
// `typeof deviceCount !== "number"` and falls back to the neutral sentence, so a Go
// server that emits the kind without the count leaves the console unable to tell
// the two situations apart — which is why the count is asserted rather than the sentence.
//
// ── how this fails ───────────────────────────────────────────────────────────
//
// Run against the naive predicate — `if len(clients) == 0` over `ListClients`,
// with no `params` — all three cases go red, and they go red differently, which
// is the point:
//
//   - the empty site gets the kind with **no params at all**, so the console renders
//     the neutral fallback: *"deviceCount is absent"*;
//   - the secretless site gets **no blind spot at all**, because it has an enabled
//     client — a site where nobody can get online and the empty stream says nothing;
//   - the healthy site is the one case the naive predicate gets right, and it is here
//     as the control that stops this from being satisfied by emitting the kind
//     unconditionally.
func TestTheNoRadiusBlindSpotTellsAnEmptySiteFromASecretlessOne(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	// A site nobody has put anything in. This is what a stranger sees on the
	// afternoon they install Foyer.
	empty := w.seedBareSite()

	// A site with a device registered and no shared secret on it. The fixtures
	// cannot express this — `seedNas` always creates a secret, because every other
	// assertion in this package needs one — so it is written directly, and it is a
	// row the schema permits: `radius_clients.shared_secret_id` is nullable.
	secretless := w.seedBareSite()
	if err := w.st.RadiusClients().Create(ctx, &store.RadiusClient{
		ID: uniq("rc"), SiteID: secretless, Name: "Adopted, never configured",
		Address: netip.MustParsePrefix(w.nextAddr() + "/32"),
		Source:  "discovered", Driver: "unifi", SharedSecretID: nil,
		RequireMsgAuth: true, LimitProxyState: true,
		MessageAuthState: "unknown", Reachability: "unknown", Enabled: true,
	}); err != nil {
		t.Fatalf("registering a device with no shared secret: %v", err)
	}

	cases := []struct {
		name      string
		siteID    string
		wantSpot  bool
		wantCount int
	}{
		{
			name: "a site whose devices all have secrets raises nothing",
			// The control. Without it, emitting the kind for every site would
			// satisfy both cases below.
			siteID: w.siteA, wantSpot: false,
		},
		{
			name:   "a site with no devices at all reports zero",
			siteID: empty, wantSpot: true, wantCount: 0,
		},
		{
			name:   "a site whose one device has no secret reports one",
			siteID: secretless, wantSpot: true, wantCount: 1,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			spots, err := w.adapter.BlindSpots(ctx, c.siteID)
			if err != nil {
				t.Fatalf("reading the blind spots for %s: %v", c.siteID, err)
			}
			var found *gen.AuthBlindSpot
			for i := range spots {
				if spots[i].Kind == gen.NoRadiusConfigured {
					found = &spots[i]
				}
			}
			if !c.wantSpot {
				if found != nil {
					t.Fatalf("site %s has devices with shared secrets and was still told "+
						"no RADIUS is configured; the kind means the stream is empty by "+
						"design, and here it is not", c.siteID)
				}
				return
			}
			if found == nil {
				t.Fatalf("site %s has no device that can speak RADIUS and raised no "+
					"`no-radius-configured` blind spot. Nobody there can get online and "+
					"the empty attempt list says nothing about why", c.siteID)
			}
			if found.Params == nil {
				t.Fatalf("site %s raised the kind with no params. `web/admin` branches on "+
					"`typeof deviceCount !== \"number\"` and falls back to the neutral "+
					"sentence, so an absent count cannot tell the two situations apart", c.siteID)
			}
			got, ok := (*found.Params)["deviceCount"]
			if !ok {
				t.Fatalf("site %s sent params %v with no deviceCount", c.siteID, *found.Params)
			}
			n, ok := got.(int)
			if !ok {
				t.Fatalf("deviceCount for %s is %T (%v); the console requires a JSON "+
					"number and reads anything else as absent", c.siteID, got, got)
			}
			if n != c.wantCount {
				t.Fatalf("deviceCount for %s is %d, want %d — the two situations this "+
					"count exists to separate would be given one sentence", c.siteID, n, c.wantCount)
			}
		})
	}
}

// seedBareSite creates a site with nothing in it and returns its id.
//
// Its own site rather than a reuse of `siteB`, because both cases below are claims
// about a *population*, and a site any other test in this package seeds into would
// make the count a race with whatever ran first.
func (w *world) seedBareSite() string {
	w.t.Helper()
	id := uniq("site")
	if err := w.st.Sites().Create(context.Background(), &store.Site{
		ID: id, Name: "Venue " + id, Environment: "lab",
	}); err != nil {
		w.t.Fatalf("creating a bare site: %v", err)
	}
	return id
}
