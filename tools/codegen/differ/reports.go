package main

import (
	"net/http"
	"net/url"
	"time"

	"github.com/kiennt048/foyer/internal/api/memory"
)

// The four `reports` operations, in two groups.
//
// The range is taken once, when the scenario is built, and handed to both servers
// verbatim: the exports and the trends are functions of `[from, to)`, and two
// requests a second apart would ask two questions. `to` is an hour ahead so rows
// made a moment ago are inside it.
//
// The exports are compared **cell by cell** (`decodeCSV`), so a declaration can
// name the column it is about. The bytes are held equal elsewhere — the golden
// document both writers' tests read.

func reportWindow(site string, now time.Time, days int) string {
	q := url.Values{}
	q.Set("siteId", site)
	q.Set("from", now.AddDate(0, 0, -days).UTC().Format(time.RFC3339))
	q.Set("to", now.Add(time.Hour).UTC().Format(time.RFC3339))
	return q.Encode()
}

// reportReadSteps runs with the reads, **before any mutation**, and that position
// is the whole of its correctness.
//
// Driven after the scenario's mutations, the session export and the trends
// disagreed for a reason neither server is wrong about: the oracle simulates the
// network. Revoking an in-use voucher ends that guest's session in `web/mock` — the
// 40-day orphan carries one, so its week's bytes went from `stale` to `live` — and
// every guest the portal steps let on gains an accounting row there. In Go
// `radacct` is FreeRADIUS's and nothing the API does writes it, so the same drive
// leaves it as it was — measured as 36 session rows against 34, and one weekly
// bucket `live` against `stale`. Comparing before the world moves compares the
// rule rather than the simulation.
func reportReadSteps(w *memory.World, site string) []step {
	now := time.Now().Truncate(time.Second)
	inverted := url.Values{}
	inverted.Set("siteId", site)
	inverted.Set("from", now.UTC().Format(time.RFC3339))
	inverted.Set("to", now.Add(-time.Hour).UTC().Format(time.RFC3339))

	return []step{
		{Op: "GetTrends", Method: http.MethodGet, Path: "/trends?" + reportWindow(site, now, 13)},
		{Op: "GetTrends", Method: http.MethodGet, Path: "/trends?" + reportWindow(site, now, 55) + "&granularity=week"},
		// Past the daily bound: `range-too-long` with its figure.
		{Op: "GetTrends", Method: http.MethodGet, Path: "/trends?" + reportWindow(site, now, 400)},
		{Op: "ExportSessions", Method: http.MethodGet, Path: "/exports/sessions?" + reportWindow(site, now, 60),
			Adapter: sessionExportDivergences(w)},
		{Op: "ExportVouchers", Method: http.MethodGet, Path: "/exports/vouchers?" + reportWindow(site, now, 90)},
		{Op: "ExportSessions", Method: http.MethodGet, Path: "/exports/sessions?" + inverted.Encode()},
		{Op: "ExportVouchers", Method: http.MethodGet, Path: "/exports/vouchers?" + reportWindow(site, now, 200)},
	}
}

// sessionExportDivergences are `listSessions`'s adapter-mode declarations, carried
// onto the export's columns — **the same rows through the same converter**, so the
// same three rules differ and nothing else may.
//
// Restated per column rather than reused, because two of the list's keys have no
// column here (`plan` and `voucherId` are not exported) and one is spelled
// differently (`freshness.intervalSeconds` is `freshnessIntervalSeconds`). Reusing
// the list's declarations would leave a key that can never happen, and a
// declaration that does not happen fails the run — which is the harness being
// right about a sentence that would have been false.
func sessionExportDivergences(w *memory.World) []divergence {
	out := []divergence{{
		Keys: []string{"identityKind"},
		Reason: "the same finding as `listSessions`: the oracle's voucher sessions carry a null " +
			"username, and `internal/store` guesses `identityKind` from the shape of " +
			"`radacct.username`, so each reads `unknown` where the oracle says `voucher`.",
	}}
	if len(apLessDevices(w)) > 0 {
		out = append(out, divergence{
			Keys: []string{"apName"},
			Reason: "the same finding as `listSessions`: a device with no MAC has no `access_points` " +
				"row, so nothing carries a radio's name for it and the oracle answers the device's own.",
		})
	}
	return append(out, divergence{
		Keys:   []string{"state", "freshnessIntervalSeconds"},
		Reason: orphanRuleReason,
	})
}

// reportGuestSteps runs **after** the guest steps, which are what make grants exist
// on both sides: a guest export compared over two empty lists agrees for the wrong
// reason.
func reportGuestSteps(site string) []step {
	now := time.Now().Truncate(time.Second)
	return []step{
		// `grantId` is minted by each server's own portal flow, so it cannot match;
		// every other column of a grant is the guest steps' doing and is compared.
		{Op: "ExportGuests", Method: http.MethodGet, Path: "/exports/guests?" + reportWindow(site, now, 30),
			Volatile: []string{"grantId"}},
	}
}
