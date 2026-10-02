package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
)

// Seeding the Go server from the running mock.
//
// **Why seed rather than transcribe.** `web/fixtures` builds every timestamp as
// `Date.now() ± a fixed offset` at module load, so a Go copy of the ugly world
// would disagree with the mock about every date the moment either process
// restarted — and about all of them, permanently, if the two ran on different
// days. Reading the world out of the mock removes that whole class of false
// difference, and it removes the maintenance burden of a second fixture set that
// nobody would keep ugly.
//
// **What that costs, stated plainly.** The harness then compares *behaviour*
// rather than data: routing, the gates, pagination, filtering, site scoping,
// ordering, envelopes and refusals. It does not check that the Go server derives
// the same figures from raw accounting, because in this configuration the Go
// server does not derive them — the in-memory fake returns what it was handed.
// `getOverview` and `listAlarms` are therefore transport comparisons and are
// reported as such rather than counted as agreement about a computation.
//
// **The independent check is the contract.** The mock validates every response
// against the schema at runtime and answers `500` when one does not match, and
// the Go side is generated from the same document. So neither server is trusted
// on shape merely because the other agrees with it.

// snapshot reads the mock's world through its own API.
func snapshot(c *client) (*memory.World, error) {
	w := &memory.World{
		Garden:      map[string]gen.WalledGarden{},
		Portal:      map[string]gen.PortalSettings{},
		Alarms:      map[string][]gen.Alarm{},
		Overviews:   map[string]gen.Overview{},
		Coverage:    map[string]gen.MacCoverage{},
		BlindSpots:  map[string][]gen.AuthBlindSpot{},
		Capability:  map[string]gen.ControllerProbe{},
		NasProbes:   map[string]gen.NasProbe{},
		Discovery:   map[string]gen.DiscoveryResult{},
		Credentials: map[string]string{},
	}

	if err := getInto(c, "/sites", &w.Sites); err != nil {
		return nil, err
	}
	if len(w.Sites) == 0 {
		return nil, fmt.Errorf("the mock reports no sites — is it seeded?")
	}

	// Operators are deployment-wide, so they are read once and not per site.
	if err := pageInto(c, "/operators", nil, &w.Operators); err != nil {
		return nil, err
	}
	// Every operator gets the same password in the Go world, and the mock is
	// started with the same one through `FOYER_MOCK_ADMIN_PASSWORD` — so both
	// servers verify the *same* credential and the outcome of signing in is
	// comparable rather than coincidental — against a mock that accepted any
	// non-empty password, this line would agree with it by accident.
	for _, op := range w.Operators {
		w.Credentials[op.Id] = harnessPassword
	}

	if err := pageInto(c, "/audit", nil, &w.Audit); err != nil {
		return nil, err
	}

	for _, site := range w.Sites {
		q := url.Values{"siteId": {site.Id}}

		var controllers []gen.Controller
		if err := getInto(c, "/controllers?"+q.Encode(), &controllers); err != nil {
			return nil, err
		}
		w.Controllers = appendNew(w.Controllers, controllers, func(v gen.Controller) string { return v.Id })

		var nas []gen.Nas
		if err := pageInto(c, "/nas", q, &nas); err != nil {
			return nil, err
		}
		w.Nas = appendNew(w.Nas, nas, func(v gen.Nas) string { return v.Id })

		var batches []gen.VoucherBatch
		if err := pageInto(c, "/voucher-batches", q, &batches); err != nil {
			return nil, err
		}
		w.Batches = appendNew(w.Batches, batches, func(v gen.VoucherBatch) string { return v.Id })

		var vouchers []gen.Voucher
		if err := pageInto(c, "/vouchers", q, &vouchers); err != nil {
			return nil, err
		}
		w.Vouchers = appendNew(w.Vouchers, vouchers, func(v gen.Voucher) string { return v.Id })

		var sessions []gen.Session
		if err := pageInto(c, "/sessions", q, &sessions); err != nil {
			return nil, err
		}
		w.Sessions = appendNew(w.Sessions, sessions, func(v gen.Session) string { return v.Id })

		var accounts []gen.Account
		if err := pageInto(c, "/accounts", q, &accounts); err != nil {
			return nil, err
		}
		w.Accounts = appendNew(w.Accounts, accounts, func(v gen.Account) string { return v.Id })

		// `listAuthAttempts` carries two required members beside `items`, and
		// they are the reason the operation exists: an empty result means *we
		// have no record*, never *nothing went wrong*.
		var attempts struct {
			Items       []gen.AuthAttempt   `json:"items"`
			NextCursor  *string             `json:"nextCursor"`
			MacCoverage gen.MacCoverage     `json:"macCoverage"`
			BlindSpots  []gen.AuthBlindSpot `json:"blindSpots"`
		}
		if err := getInto(c, "/auth-attempts?"+q.Encode()+"&limit=200", &attempts); err != nil {
			return nil, err
		}
		w.Attempts = appendNew(w.Attempts, attempts.Items, func(v gen.AuthAttempt) string { return v.Id })
		w.Coverage[site.Id] = attempts.MacCoverage
		w.BlindSpots[site.Id] = attempts.BlindSpots

		var settings gen.PortalSettings
		if err := getInto(c, "/sites/"+site.Id+"/portal", &settings); err == nil {
			w.Portal[site.Id] = settings
		}
		var garden gen.WalledGarden
		if err := getInto(c, "/sites/"+site.Id+"/walled-garden", &garden); err == nil {
			w.Garden[site.Id] = garden
		}
		var overview gen.Overview
		if err := getInto(c, "/overview?"+q.Encode(), &overview); err == nil {
			w.Overviews[site.Id] = overview
		}
		var alarms []gen.Alarm
		if err := getInto(c, "/alarms?"+q.Encode(), &alarms); err == nil {
			w.Alarms[site.Id] = alarms
		}
	}

	return w, nil
}

// seedProbes runs the mock's probes and records what they answered.
//
// It runs **before** the world snapshot on purpose: `probeNas` writes back what
// it measured, so a snapshot taken first would capture the pre-probe rows and
// then disagree with the mock the moment the scenario probed anything. Running
// it first means both servers start from post-probe state.
//
// The probe results are then what the Go server's `DeviceOperations` fake
// returns, which is the only defensible thing it can do: `docs/rules/
// nas-drivers.md` is explicit that a hand-written NAS mock encodes the author's
// assumptions, so inventing capability answers in Go would be inventing
// evidence. Replaying the oracle's is not evidence about hardware either — and
// the report says so.
func seedProbes(c *client, w *memory.World) error {
	for _, ctl := range w.Controllers {
		var probe gen.ControllerProbe
		if err := postInto(c, "/controllers/"+ctl.Id+"/probe", nil, &probe); err != nil {
			continue
		}
		w.Capability[ctl.Id] = probe
	}
	for _, n := range w.Nas {
		var probe gen.NasProbe
		if err := postInto(c, "/nas/"+n.Id+"/probe", nil, &probe); err != nil {
			continue
		}
		w.NasProbes[n.Id] = probe
	}
	return nil
}

// ── transport ────────────────────────────────────────────────────────────────

func getInto(c *client, path string, out any) error {
	a, err := c.do(http.MethodGet, path, nil, false, false)
	if err != nil {
		return err
	}
	if a.Status != http.StatusOK {
		return fmt.Errorf("GET %s: %d %s", path, a.Status, truncate(a.Body))
	}
	return json.Unmarshal(a.Body, out)
}

func postInto(c *client, path string, body any, out any) error {
	a, err := c.do(http.MethodPost, path, body, false, false)
	if err != nil {
		return err
	}
	if a.Status != http.StatusOK && a.Status != http.StatusCreated {
		return fmt.Errorf("POST %s: %d %s", path, a.Status, truncate(a.Body))
	}
	return json.Unmarshal(a.Body, out)
}

// pageInto follows `nextCursor` to the end.
//
// The whole collection is needed, not the first page: the mock has 625 vouchers
// and the pathological rows are not all on page one. A seeder that stopped at 50
// would produce a Go world missing exactly the cases the fixtures exist to
// carry.
func pageInto[T any](c *client, path string, q url.Values, out *[]T) error {
	if q == nil {
		q = url.Values{}
	}
	cursor := ""
	for {
		v := url.Values{}
		for k, vals := range q {
			v[k] = vals
		}
		v.Set("limit", "200")
		if cursor != "" {
			v.Set("cursor", cursor)
		}
		var page struct {
			Items      []T     `json:"items"`
			NextCursor *string `json:"nextCursor"`
		}
		if err := getInto(c, path+"?"+v.Encode(), &page); err != nil {
			return err
		}
		*out = append(*out, page.Items...)
		if page.NextCursor == nil || *page.NextCursor == "" {
			return nil
		}
		cursor = *page.NextCursor
	}
}

// appendNew merges per-site reads without duplicating a row that two sites both
// returned. The mock's `listVouchers` ignores `siteId` entirely — a finding this
// harness files — so without this the voucher list would be multiplied by the
// number of sites.
func appendNew[T any](dst, src []T, key func(T) string) []T {
	seen := map[string]bool{}
	for _, v := range dst {
		seen[key(v)] = true
	}
	for _, v := range src {
		if seen[key(v)] {
			continue
		}
		seen[key(v)] = true
		dst = append(dst, v)
	}
	return dst
}
