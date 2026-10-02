package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
)

// Making the harness fail on purpose.
//
// ── why this is a mode and not a paragraph in a document ─────────────────────
//
// A `make diff-api` that never touches `internal/apistore` is a mechanism that
// looks like a check and is not: its report is byte-identical with or without the
// real adapter, and nothing about a green run says so.
//
// A mode that agrees on its first run looks exactly the same from outside. So
// the claim this harness makes — *it can detect a broken adapter* — has to be
// re-derived rather than recorded. `-sabotage` breaks one binding and requires the
// run to go **red**; a green run under sabotage is itself a failure, and says which
// class of defect went unnoticed.
//
// This is the pattern `docs/rules/testing.md` already names: most of the focus
// locks re-inject their defect from the page "so the evidence that they can fail
// is re-derived on every run".
//
// ── what these break, and why at the port seam ───────────────────────────────
//
// Each kind wraps one of the fourteen interfaces `internal/api` declares and
// corrupts an answer in the shape of a real adapter defect. The seam is where a
// harness may reach; editing `internal/apistore` from here would make this command
// own a package it does not own, and a sabotage that lives in the code under test
// is one somebody eventually commits.
//
// The three kinds are drawn from defects real adapters have:
//
//   - `site-scope` — the scope silently dropped. `internal/apistore/authattempts.go`
//     opens by explaining that `radpostauth` carries no site, and that a naive
//     adapter therefore answers `listAuthAttempts?siteId=…` with **every** site's
//     usernames and MACs.
//   - `column-swap` — a binding that reads the neighbouring column. Type-checks,
//     compiles, ships.
//   - `device-id` — `Session.nasId` answered as the `clients.conf` row rather than
//     the device. `ListNas` in `internal/store/radiusclients.go` resolves it.

type sabotageKind struct {
	name   string
	reason string
	apply  func(api.Ports) api.Ports

	// precondition refuses the run when the seeded world cannot express this
	// defect at all.
	//
	// **Without it, "the sabotage went unnoticed" says the wrong thing.** A
	// dropped site scope over a world whose rows all live in one site changes no
	// answer, so the harness reports that it cannot see the defect — true, and
	// misleading, because the cause is the fixtures rather than the comparison.
	// A check that cannot distinguish *I did not notice* from *there was nothing
	// to notice* is a check that looks like one and is not.
	precondition func(*memory.World) error
}

var sabotages = map[string]sabotageKind{
	"site-scope": {
		name: "site-scope",
		reason: "`listSessions` answers with every site's rows: the site scope is dropped before the " +
			"query runs. On the one surface an operator opens when a guest " +
			"is standing in front of them, leaking another venue's identifiers.",
		apply: func(p api.Ports) api.Ports {
			p.Sessions = unscopedSessions{p.Sessions}
			return p
		},
		precondition: needsTwoOccupiedSites,
	},
	"column-swap": {
		name: "column-swap",
		reason: "the device read model returns `firmware` where `model` belongs and the reverse — a " +
			"binding that reads the neighbouring column. It type-checks, it compiles, and no " +
			"fake-backed comparison can see it.",
		apply: func(p api.Ports) api.Ports {
			p.AccessPoints = swappedColumns{p.AccessPoints}
			return p
		},
	},
	"device-id": {
		name: "device-id",
		reason: "`Session.nasId` is answered as the `clients.conf` row's id rather than the device's. " +
			"A device id read off `listNas` and sent back as " +
			"`listSessions?nasId=` then matches nothing, and a failed session end loses the name " +
			"of the device it failed on.",
		apply: func(p api.Ports) api.Ports {
			p.Sessions = clientIDForDevice{p.Sessions}
			return p
		},
	},
}

func sabotageNames() []string {
	out := make([]string, 0, len(sabotages))
	for k := range sabotages {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// needsTwoOccupiedSites refuses when the oracle's world has rows in one site.
//
// **This is a finding about `web/fixtures` rather than a limit of the harness.** A
// world that advertises three sites and puts every session, account, device and
// voucher batch in one of them cannot show a site scope being honoured or dropped —
// the property where a naive `listAuthAttempts` answers with every site's usernames
// and MACs. Every suite run against such a world is blind to that class of defect.
func needsTwoOccupiedSites(w *memory.World) error {
	occupied := occupiedSites(w)
	if len(occupied) >= 2 {
		return nil
	}
	return fmt.Errorf(
		"the oracle's world has rows in %d of its %d sites, so a dropped site scope changes\n"+
			"  no answer and this sabotage cannot bite. That is a fact about `web/fixtures`, not\n"+
			"  about the harness: **nothing in this world can show a site scope being honoured or\n"+
			"  dropped**. Refusing rather than reporting that\n"+
			"  the sabotage went unnoticed, because those two sentences mean opposite things",
		len(occupied), len(w.Sites))
}

// occupiedSites names the sites that actually carry rows.
func occupiedSites(w *memory.World) []string {
	seen := map[string]bool{}
	for _, s := range w.Sessions {
		seen[s.SiteId] = true
	}
	for _, a := range w.Accounts {
		seen[a.SiteId] = true
	}
	for _, n := range w.Nas {
		seen[n.SiteId] = true
	}
	for _, b := range w.Batches {
		seen[b.SiteId] = true
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func sabotageReason(name string) string {
	if k, ok := sabotages[name]; ok {
		return k.reason
	}
	return ""
}

func applySabotage(p api.Ports, name string, w *memory.World) (api.Ports, error) {
	k, ok := sabotages[name]
	if !ok {
		return p, fmt.Errorf("unknown -sabotage %q: one of %v", name, sabotageNames())
	}
	if k.precondition != nil {
		if err := k.precondition(w); err != nil {
			return p, fmt.Errorf("-sabotage %s: %w", name, err)
		}
	}
	return k.apply(p), nil
}

// sabotageVerdict inverts the exit code.
//
// Under sabotage a **green** run is the failure, and the message says which class of
// defect the harness did not notice — because that is the sentence somebody needs,
// not "0 differences".
func sabotageVerdict(err error) error {
	if err != nil {
		fmt.Printf("  the sabotage was detected: the harness reported the differences above and\n" +
			"  would have exited non-zero. That is the result this mode exists to produce.\n\n")
		return nil
	}
	return fmt.Errorf("THE SABOTAGE WENT UNNOTICED.\n" +
		"  The adapter was answering wrongly on purpose and every compared step still\n" +
		"  agreed, which means this harness cannot see that class of defect. Do not\n" +
		"  trust a green run of this mode until it can")
}

// ── the three wrappers ───────────────────────────────────────────────────────

type unscopedSessions struct{ api.SessionLog }

func (u unscopedSessions) Sessions(ctx context.Context, q api.SessionQuery) (api.Page[gen.Session], error) {
	q.SiteID = ""
	return u.SessionLog.Sessions(ctx, q)
}

type clientIDForDevice struct{ api.SessionLog }

func (c clientIDForDevice) Sessions(ctx context.Context, q api.SessionQuery) (api.Page[gen.Session], error) {
	page, err := c.SessionLog.Sessions(ctx, q)
	for i := range page.Items {
		page.Items[i].NasId = "rc-" + page.Items[i].NasId
	}
	return page, err
}

func (c clientIDForDevice) SessionByID(ctx context.Context, id string) (gen.Session, error) {
	s, err := c.SessionLog.SessionByID(ctx, id)
	s.NasId = "rc-" + s.NasId
	return s, err
}

type swappedColumns struct{ api.AccessPointRegistry }

func (s swappedColumns) AccessPoints(ctx context.Context, siteID, search string, w api.Window) (api.Page[gen.Nas], error) {
	page, err := s.AccessPointRegistry.AccessPoints(ctx, siteID, search, w)
	for i := range page.Items {
		page.Items[i].Model, page.Items[i].Firmware = page.Items[i].Firmware, page.Items[i].Model
	}
	return page, err
}

func (s swappedColumns) AccessPoint(ctx context.Context, id string) (gen.Nas, error) {
	n, err := s.AccessPointRegistry.AccessPoint(ctx, id)
	n.Model, n.Firmware = n.Firmware, n.Model
	return n, err
}
