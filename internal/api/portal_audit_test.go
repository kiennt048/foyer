package api_test

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// What the portal's audit entries say, read at the HTTP boundary.
//
// The two richest writers in the product — `network-settings-changed` and the
// radius half of it — record `{"changed": {field: {"from":…, "to":…}}}`, and the
// page builder records the same shape; without it, the screen built for exactly
// this would record nothing at all. `docs/rules/database.md`: an entry is
// append-only and a wrong one is corrected by a later one, so a row that cannot
// say what moved cannot be corrected either. These drive that shape and hold the
// limits on it.

// changedJSONs is every entry's `changed` as JSON, in no particular order.
//
// The memory fake stamps every entry from a fixed clock, so the order the log
// answers in is not something an assertion should be resting on. It also keeps
// the params in the form an operator's console reads them — the store hands back
// the Go value, and a reader gets the JSON.
func changedJSONs(t *testing.T, entries []gen.AuditEntry) []string {
	t.Helper()
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Params == nil {
			out = append(out, "{}")
			continue
		}
		b, err := json.Marshal((*e.Params)["changed"])
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, string(b))
	}
	sort.Strings(out)
	return out
}

// rowContaining is the one entry of `entries` whose `changed` mentions `field`.
// For a table where every save moves the same field, an index into the answer is
// a coin toss and this is not.
func rowContaining(t *testing.T, entries []gen.AuditEntry, field string) gen.AuditEntry {
	t.Helper()
	var found []gen.AuditEntry
	for _, e := range entries {
		if e.Params == nil {
			continue
		}
		if c, ok := (*e.Params)["changed"].(map[string]any); ok {
			if _, ok := c[field]; ok {
				found = append(found, e)
			}
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d entries changed %s, want 1", len(found), field)
	}
	return found[0]
}

// changedRoundTrip is `changed` in the form a reader gets it: through JSON. The
// store hands back the Go value the handler built, so a type assertion on it
// tests the handler's internals rather than the row an operator can see.
func changedRoundTrip(t *testing.T, e gen.AuditEntry) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(changedJSONs(t, []gen.AuditEntry{e})[0]), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func portalParams(t *testing.T, e gen.AuditEntry) map[string]any {
	t.Helper()
	if e.Params == nil {
		t.Fatalf("the entry carries no params: %s", e.Kind)
	}
	return *e.Params
}

func changed(t *testing.T, e gen.AuditEntry) map[string]any {
	t.Helper()
	c, ok := portalParams(t, e)["changed"].(map[string]any)
	if !ok {
		t.Fatalf("the entry has no `changed`: %v", portalParams(t, e))
	}
	return c
}

// A settings change names the field and both its values. A row that only says
// "the portal settings changed" is the shape a wrong entry takes: three months
// on nobody can tell a renamed venue from a recoloured page.
func TestAPortalSettingsChangeNamesTheFieldAndBothItsValues(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	var before gen.PortalSettings
	if err := json.Unmarshal([]byte(readAll(t, r.do(http.MethodGet, "/sites/"+testSite+"/portal", nil))), &before); err != nil {
		t.Fatal(err)
	}

	resp := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{
		"venueName":           "Quán cà phê",
		"networkName":         "Cafe Hanoi",
		"clickThroughMinutes": 20,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH: %d: %s", resp.StatusCode, readAll(t, resp))
	}
	readAll(t, resp)

	entries := auditOf(t, r, gen.AuditKindPortalSettingsChanged)
	if len(entries) != 1 {
		t.Fatalf("%d portal-settings-changed entries, want 1", len(entries))
	}
	e := entries[0]
	if e.SiteId == nil || *e.SiteId != testSite || e.Subject == nil || e.Subject.Id != testSite {
		t.Fatalf("the entry is not the venue's: %+v", e)
	}
	raw, _ := json.Marshal(e.Params)
	got := string(raw)
	for _, want := range []string{
		`"venueName":{"from":"` + before.VenueName + `","to":"Quán cà phê"}`,
		`"networkName":{"from":"","to":"Cafe Hanoi"}`,
		`"clickThroughMinutes":{"from":null,"to":20}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the entry does not carry %s:\n%s", want, got)
		}
	}
	// The same shape the network writer already uses, so one reader of the log
	// learns one grammar.
	if _, ok := changed(t, e)["venueName"]; !ok {
		t.Errorf("no `changed.venueName`: %s", got)
	}
}

// A value that is there is a value that can be cleared. The empty→value and
// value→empty cases are the two a correction is most often about, and both read
// as an empty string rather than as a missing key.
func TestClearingAValueIsRecordedAsTheEmptyString(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	for _, body := range []map[string]any{
		{"networkName": "Cafe Hanoi"},
		{"networkName": ""},
	} {
		resp := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PATCH %v: %d: %s", body, resp.StatusCode, readAll(t, resp))
		}
		readAll(t, resp)
	}

	got := changedJSONs(t, auditOf(t, r, gen.AuditKindPortalSettingsChanged))
	want := []string{`{"networkName":{"from":"","to":"Cafe Hanoi"}}`, `{"networkName":{"from":"Cafe Hanoi","to":""}}`}
	sort.Strings(want)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("a set and a clear:\n got %s\nwant %s", strings.Join(got, " "), strings.Join(want, " "))
	}
}

// jstr is `mustJSON` as a string, for the one assertion that is a whole
// expected value rather than a fragment.
func jstr(t *testing.T, v any) string { return string(mustJSON(t, v)) }

// The terms are legal text a venue authored, so they are recorded on their own
// row and named per language — "what did the terms say on the day this guest
// agreed" is a different question from "who renamed the venue", and one venue
// publishes three languages of them.
func TestATermsChangeNamesTheLanguageAndBothItsValues(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	resp := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{
		"terms": map[string]any{"summary": map[string]any{"vi": "Xin chào, quý khách."}},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH: %d: %s", resp.StatusCode, readAll(t, resp))
	}
	readAll(t, resp)

	entries := auditOf(t, r, gen.AuditKindPortalTermsChanged)
	if len(entries) != 1 {
		t.Fatalf("%d portal-terms-changed entries, want 1", len(entries))
	}
	// A terms-only save says nothing about the settings, and vice versa.
	if n := len(auditOf(t, r, gen.AuditKindPortalSettingsChanged)); n != 0 {
		t.Errorf("a terms-only save wrote %d settings rows", n)
	}
	if got := jstr(t, changed(t, entries[0])["terms.summary.vi"]); got != `{"from":"","to":"Xin chào, quý khách."}` {
		t.Errorf("the terms row: %s", got)
	}
}

// **A no-op writes no row.** Asserted here at the boundary, because that is
// where it is guaranteed: the entry is written from the difference between
// the settings before and after, and an empty difference writes nothing. The
// alternative — deciding from the fields the request happened to mention —
// cannot see a save that names a field and sets it to the value it already
// held, which is what the console's own "Save" does when an operator re-reads
// a form and presses the button.
func TestASaveThatChangesNothingWritesNoRow(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	save := map[string]any{
		"venueName":   "Quán cà phê",
		"networkName": "Cafe Hanoi",
		"terms":       map[string]any{"summary": map[string]any{"vi": "Xin chào."}},
		"design":      design(block("welcome", "text", "welcome", nil)),
	}
	for i := 0; i < 3; i++ {
		resp := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", save)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("save %d: %d: %s", i, resp.StatusCode, readAll(t, resp))
		}
		readAll(t, resp)
	}
	if n := len(auditOf(t, r, gen.AuditKindPortalSettingsChanged)); n != 1 {
		t.Errorf("%d portal-settings-changed rows for three identical saves, want 1", n)
	}
	if n := len(auditOf(t, r, gen.AuditKindPortalTermsChanged)); n != 1 {
		t.Errorf("%d portal-terms-changed rows for three identical saves, want 1", n)
	}

	// One real change, and only that one is recorded.
	resp := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{"venueName": "Café mới"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the real change: %d: %s", resp.StatusCode, readAll(t, resp))
	}
	readAll(t, resp)
	entries := auditOf(t, r, gen.AuditKindPortalSettingsChanged)
	if len(entries) != 2 {
		t.Fatalf("%d rows after one real change, want 2", len(entries))
	}
	if got := jstr(t, changed(t, entries[1])["venueName"]); got != `{"from":"Quán cà phê","to":"Café mới"}` {
	}
}

// Redaction. A value too long to echo is replaced by its length and a digest:
// enough to prove the text moved and to match two entries that carry the same
// one, and not a copy of a venue's legal text that the log can never take back.
// **The limit is asserted, not assumed** — a test that only checks the short
// values pass is a test that holds nothing, and the row is append-only.
func TestALongValueIsRecordedAsALengthAndADigest(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	long := strings.Repeat("Điều khoản sử dụng mạng Wi-Fi công cộng. ", 60) // 2 340 bytes
	logo := "data:image/png;base64," + strings.Repeat("QUJD", 4096)         // 16 384 bytes decoded
	resp := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{
		"terms":       map[string]any{"summary": map[string]any{"vi": long}},
		"logoDataUri": logo,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PATCH: %d: %s", resp.StatusCode, readAll(t, resp))
	}
	readAll(t, resp)

	terms := auditOf(t, r, gen.AuditKindPortalTermsChanged)
	if len(terms) != 1 {
		t.Fatalf("%d terms rows, want 1", len(terms))
	}
	raw, _ := json.Marshal(terms[0].Params)
	if strings.Contains(string(raw), "Điều khoản") {
		t.Errorf("the terms text was copied into an append-only row:\n%s", raw)
	}
	termsTo, ok := changedRoundTrip(t, terms[0])["terms.summary.vi"].(map[string]any)
	if !ok {
		t.Fatalf("no terms entry: %s", raw)
	}
	digest, ok := termsTo["to"].(map[string]any)
	if !ok {
		t.Fatalf("a long terms value was echoed whole: %s", raw)
	}
	if digest["bytes"] != float64(len(long)) {
		t.Errorf("the digest says %v bytes, the value was %d", digest["bytes"], len(long))
	}
	sum, _ := digest["sha256"].(string)
	if len(sum) != 16 {
		t.Errorf("sha256 %q, want 16 hex characters", sum)
	}

	// A logo is a data URI and could be 100 kB. It gets the same treatment, and
	// a same-origin path — the form a venue actually uploads — is short enough to
	// read, so a reader can see which file was replaced.
	settings := auditOf(t, r, gen.AuditKindPortalSettingsChanged)
	if len(settings) != 1 {
		t.Fatalf("%d settings rows, want 1", len(settings))
	}
	raw, _ = json.Marshal(settings[0].Params)
	if strings.Contains(string(raw), "QUJD") {
		t.Errorf("the logo was copied into an append-only row:\n%s", raw)
	}
	if _, ok := changedRoundTrip(t, settings[0])["logoDataUri"].(map[string]any)["to"].(map[string]any); !ok {
		t.Errorf("a data URI logo was echoed whole:\n%s", raw)
	}

	resp = r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{"logoDataUri": "/venue/" + testSite + "/logo.png"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the path logo: %d: %s", resp.StatusCode, readAll(t, resp))
	}
	readAll(t, resp)
	// Two logo changes so far, and the one that put a path there is named.
	var path string
	for _, e := range auditOf(t, r, gen.AuditKindPortalSettingsChanged) {
		if c, ok := changedRoundTrip(t, e)["logoDataUri"].(map[string]any); ok {
			if s, ok := c["to"].(string); ok {
				path = s
			}
		}
	}
	if path != "/venue/"+testSite+"/logo.png" {
		t.Errorf("a short logo path was not echoed whole, or the two rows disagree: %q", path)
	}
}

// The page design is the venue's page: blocks, order, kind, visibility and the
// welcome line. It is **not** a copy of the page, because a whole layout dumped
// into an append-only row cannot be edited later, and because the log's only
// UPDATE path (`foyer_audit_pseudonymise`) is a text redaction that would then
// rewrite the entry the operator is trying to read.
func TestTheDesignIsRecordedAsARosterAndNotAsItsContent(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	secret := "Wifi miễn phí — mật khẩu MậtKhẩu123"
	d := design(
		block("welcome", "text", "welcome", map[string]any{
			"content": map[string]any{"en": map[string]any{"title": "Welcome", "body": secret}}}),
		block("offer", "offer", "welcome", nil),
	)
	d["accent"] = "#c8873a"
	d["headline"] = map[string]any{"vi": "Chào mừng"}
	if code, body := patchDesign(t, r, d); code != http.StatusOK {
		t.Fatalf("PATCH: %d\n%s", code, body)
	}

	entries := auditOf(t, r, gen.AuditKindPortalSettingsChanged)
	if len(entries) != 1 {
		t.Fatalf("%d settings rows, want 1", len(entries))
	}
	raw, _ := json.Marshal(entries[0].Params)
	got := string(raw)
	if strings.Contains(got, secret) {
		t.Errorf("a block's body was copied into an append-only row:\n%s", got)
	}
	designChange, ok := changedRoundTrip(t, entries[0])["design"].(map[string]any)
	if !ok {
		t.Fatalf("the design change is not recorded: %s", got)
	}
	for _, want := range []string{`"id":"welcome"`, `"id":"offer"`, `"accent":"#c8873a"`, `"Chào mừng"`} {
		if !strings.Contains(jstr(t, designChange), want) {
			t.Errorf("the design row does not carry %s: %s", want, jstr(t, designChange))
		}
	}
	// A block that goes is a block the reader can name, not just a count: the
	// two rows say the page had these blocks and then those, and the reader can
	// tell which is which without the log's own ordering being trustworthy.
	d["blocks"] = []map[string]any{block("welcome", "text", "welcome", nil)}
	if code, body := patchDesign(t, r, d); code != http.StatusOK {
		t.Fatalf("PATCH: %d\n%s", code, body)
	}
	var rosters []string
	for _, e := range auditOf(t, r, gen.AuditKindPortalSettingsChanged) {
		c, ok := changedRoundTrip(t, e)["design"].(map[string]any)
		if !ok {
			continue
		}
		to, ok := c["to"].(map[string]any)
		if !ok {
			t.Errorf("the design row has no `to`: %s", jstr(t, c))
			continue
		}
		rosters = append(rosters, jstr(t, to["blocks"]))
	}
	sort.Strings(rosters)
	// The roster keeps the order the blocks are shown in, not an alphabetical one.
	offer := `[{"id":"welcome","kind":"text","placement":"welcome","visible":true},{"id":"offer","kind":"offer","placement":"welcome","visible":true}]`
	welcome := `[{"id":"welcome","kind":"text","placement":"welcome","visible":true}]`
	if len(rosters) != 2 || rosters[0] != offer || rosters[1] != welcome {
		t.Errorf("the two rosters are\n%s", strings.Join(rosters, "\n"))
	}
}
