package apistore_test

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// The `reports` operations over PostgreSQL.
//
// `internal/api`'s suite proves the handler against the memory fake; this proves
// the SQL — the range, the site scope through the longest-prefix client join, the
// order, the grant join and the trend aggregates — on the rows the rest of this
// package already seeds, which carry the orphan, the wrapped counter and the
// second site on purpose.

func reportQuery(siteID string, from, to time.Time) string {
	q := url.Values{}
	q.Set("siteId", siteID)
	q.Set("from", from.UTC().Format(time.RFC3339))
	q.Set("to", to.UTC().Format(time.RFC3339))
	return q.Encode()
}

func csvRows(t *testing.T, body []byte) []map[string]string {
	t.Helper()
	text := string(body)
	if !strings.HasPrefix(text, "\uFEFF") {
		t.Fatalf("no byte-order mark: %q", truncate(body))
	}
	recs, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(text, "\uFEFF"))).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v\n%s", err, truncate(body))
	}
	var out []map[string]string
	for _, rec := range recs[1:] {
		row := map[string]string{}
		for i, h := range recs[0] {
			row[h] = rec[i]
		}
		out = append(out, row)
	}
	return out
}

// TestTheSessionExportIsScopedRangedAndNeutralisedInSQL.
func TestTheSessionExportIsScopedRangedAndNeutralisedInSQL(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	now := time.Now()

	// A guest-typed username that is a formula, on an ended session in range.
	hostile := `=HYPERLINK("https://attacker.example/","x")`
	if _, err := w.owner.Exec(ctx, `
		INSERT INTO radacct (acctuniqueid, acctsessionid, username, nasipaddress, callingstationid,
		                     acctstarttime, acctupdatetime, acctstoptime, acctinputoctets, acctoutputoctets)
		VALUES ($1, 'h-1', $2, $3::inet, '@SUM(A1)', $4, $5, $5, 10, 20)`,
		w.block+"hostile", hostile, w.block+"11", now.Add(-2*time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	w.signIn()

	body := w.do(http.MethodGet, "/exports/sessions?"+reportQuery(w.siteA, now.Add(-24*time.Hour), now.Add(time.Minute)), nil, http.StatusOK)
	rows := csvRows(t, body)
	ids := map[string]map[string]string{}
	var order []string
	for _, r := range rows {
		ids[r["sessionId"]] = r
		order = append(order, r["sessionId"])
	}
	for _, want := range []string{w.sessionOnline, w.sessionWrapped, w.block + "hostile"} {
		if ids[want] == nil {
			t.Errorf("session %s is missing from the export: %v", want, order)
		}
	}
	// Out of range (forty days old) and out of scope (the other site).
	for _, gone := range []string{w.sessionOrphaned, w.block + "other-site"} {
		if ids[gone] != nil {
			t.Errorf("session %s is in the export — the range or the site scope leaked", gone)
		}
	}
	// Oldest first: the wrapped session started three hours ago, the hostile one two.
	if len(order) >= 3 && (order[0] != w.sessionWrapped || order[1] != w.block+"hostile") {
		t.Errorf("order = %v, want oldest first", order)
	}
	if got := ids[w.block+"hostile"]["username"]; got != "'"+hostile {
		t.Errorf("username = %q, want the formula defused", got)
	}
	if got := ids[w.block+"hostile"]["callingStationId"]; got != "'@SUM(A1)" {
		t.Errorf("callingStationId = %q, want it defused", got)
	}
	if strings.Contains(string(body), w.voucherCode) {
		t.Error("the voucher code the NAS put in User-Name left in full")
	}
	if got := ids[w.sessionWrapped]; got["countersWrapped"] != "true" || got["inputOctetsTotal"] != "4294979641" {
		t.Errorf("wrapped session exported as %q / %q", got["countersWrapped"], got["inputOctetsTotal"])
	}

	// The entry was written, as the venue's.
	var page struct {
		Items []gen.AuditEntry `json:"items"`
	}
	w.getJSON("/audit?limit=200", &page)
	found := false
	for _, e := range page.Items {
		if e.Kind == gen.AuditKindDataExported && e.SiteId != nil && *e.SiteId == w.siteA {
			found = true
		}
	}
	if !found {
		t.Error("the export left no data-exported entry in the venue's audit log")
	}
}

func TestTheVoucherAndGuestExportsOverPostgres(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	now := time.Now()

	// Two grants in site A — one for the redeemed voucher, one click-through — and
	// one in site B that must not appear.
	for _, g := range []*store.PortalGrant{
		{ID: w.block + "g-voucher", SiteID: w.siteA, ClientMAC: "aa:bb:cc:dd:ee:01", Method: "voucher",
			VoucherID: ptr(w.voucherInUse), GrantedAt: now.Add(-20 * time.Minute)},
		{ID: w.block + "g-click", SiteID: w.siteA, ClientMAC: "02:00:00:00:00:77", Method: "click-through",
			GrantedAt: now.Add(-10 * time.Minute)},
		{ID: w.block + "g-other", SiteID: w.siteB, ClientMAC: "02:00:00:00:00:78", Method: "click-through",
			GrantedAt: now.Add(-10 * time.Minute)},
	} {
		if err := w.st.Grants().CreateGrant(ctx, g); err != nil {
			t.Fatal(err)
		}
	}
	w.signIn()

	body := w.do(http.MethodGet, "/exports/vouchers?"+reportQuery(w.siteA, now.Add(-time.Hour), now.Add(time.Minute)), nil, http.StatusOK)
	vouchers := csvRows(t, body)
	if len(vouchers) != 4 {
		t.Errorf("got %d voucher rows, want the batch of four created in range (and none of site B's)", len(vouchers))
	}
	if strings.Contains(string(body), w.voucherCode) {
		t.Error("a voucher code left in full")
	}

	body = w.do(http.MethodGet, "/exports/guests?"+reportQuery(w.siteA, now.Add(-time.Hour), now.Add(time.Minute)), nil, http.StatusOK)
	guests := csvRows(t, body)
	if len(guests) != 2 || guests[0]["grantId"] != w.block+"g-voucher" || guests[1]["grantId"] != w.block+"g-click" {
		t.Fatalf("guest rows = %v, want the two site-A grants oldest first", guests)
	}
	if guests[1]["macIsRandomised"] != "true" || guests[0]["voucherId"] != w.voucherInUse {
		t.Errorf("guest rows = %v", guests)
	}
}

// TestTrendsOverPostgres sums over the buckets rather than asserting one day, so a
// run that straddles local midnight still asserts the same totals.
func TestTrendsOverPostgres(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	now := time.Now()
	// A grant for the voucher session's own MAC, granted as that session started:
	// one device, not two.
	if err := w.st.Grants().CreateGrant(ctx, &store.PortalGrant{
		ID: w.block + "g-same", SiteID: w.siteA, ClientMAC: "AA-BB-CC-DD-EE-01", Method: "click-through",
		GrantedAt: now.Add(-30 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	w.signIn()

	var tr gen.Trends
	w.getJSON("/trends?"+reportQuery(w.siteA, now.Add(-48*time.Hour), now.Add(time.Minute)), &tr)
	if len(tr.Buckets) < 3 {
		t.Fatalf("got %d buckets for two days and a minute, want at least 3", len(tr.Buckets))
	}
	var devices, redeemed int
	var bytes int64
	lower := false
	sampled := 0
	for _, b := range tr.Buckets {
		devices += b.GuestDevices
		redeemed += b.VouchersRedeemed
		bytes += b.Bytes
		lower = lower || b.BytesLowerBound
		switch b.BytesFreshness.State {
		case gen.FreshnessStateSampled:
			sampled++
			if b.BytesFreshness.IntervalSeconds == nil || *b.BytesFreshness.IntervalSeconds != 300 {
				t.Errorf("a sampled bucket without the 300-second floor: %+v", b.BytesFreshness)
			}
		case gen.FreshnessStateStale:
			t.Errorf("a stale bucket — the forty-day orphan is outside this range and nothing else is silent: %+v", b)
		}
	}
	// The two open site-A sessions in range (online and wrapped) have two MACs, and
	// the grant shares the online one's; the orphan is out of range and site B's is
	// out of scope.
	if devices != 2 {
		t.Errorf("guestDevices summed to %d, want 2 — a session and a grant for one MAC are one device", devices)
	}
	if redeemed != 1 {
		t.Errorf("vouchersRedeemed summed to %d, want the one redeemed voucher", redeemed)
	}
	want := int64((1<<20)+(2<<20)) + int64((1<<32)+12345+(7<<20))
	if bytes != want {
		t.Errorf("bytes summed to %d, want %d", bytes, want)
	}
	if !lower {
		t.Error("no bucket says its bytes are a lower bound, although one counted session wrapped")
	}
	if sampled == 0 {
		t.Error("no bucket is sampled, although two counted sessions are open")
	}
}
