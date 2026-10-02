package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
)

// Guest data and marketing consent at the HTTP boundary, against the memory fake:
// what the portal records for a guest who ticked the marketing box and — the half
// that matters — what it records for one who did not. The PostgreSQL side (the
// tables, the erasure, the retention sweep) is
// internal/store/privacy_guestdata_test.go.

const consentEN = "Send me offers from The Venue by e-mail."

func versionOf(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

// askForDetails configures the venue: a name (optional), an e-mail address
// (required), and marketing consent in English.
func askForDetails(t *testing.T, s *memory.Store, site string) {
	t.Helper()
	text := consentEN
	_, err := s.SetGuestDataSettings(context.Background(), site, gen.GuestDataSettingsUpdate{
		Fields: []gen.GuestDataFieldSetting{
			{Field: gen.GuestDataFieldName, Required: false},
			{Field: gen.GuestDataFieldEmail, Required: true},
		},
		MarketingConsent: gen.MarketingConsentSettingUpdate{Enabled: true, Text: gen.ConsentText{En: &text}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func consents(t *testing.T, s *memory.Store, site string) []gen.MarketingConsent {
	t.Helper()
	p, err := s.MarketingConsents(context.Background(), site, nil, api.Window{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	return p.Items
}

// TestAGuestWhoDoesNotTickTheBoxLeavesNoMarketingRecord: a guest who does not
// consent leaves no marketing record, driven through every way a form can arrive
// without a tick: the box left alone, a value that is not `yes`,
// a tick with no version beside it, and accepting the terms — which is the one a
// careless implementation would read as consent.
func TestAGuestWhoDoesNotTickTheBoxLeavesNoMarketingRecord(t *testing.T) {
	for name, extra := range map[string]url.Values{
		"box left unticked":        {},
		"a value that is not yes":  {"marketingConsent": {"on"}, "consentVersion": {versionOf(consentEN)}},
		"a tick with no version":   {"marketingConsent": {"yes"}},
		"a version that was never": {"marketingConsent": {"yes"}, "consentVersion": {versionOf("something else")}},
	} {
		t.Run(name, func(t *testing.T) {
			g := presentRig(t)
			askForDetails(t, g.store, portalSite)
			g.arrive(guestMAC).Body.Close()

			form := url.Values{"lang": {"en"}, "name": {"Lan"}, "email": {"lan@example.test"}}
			for k, v := range extra {
				form[k] = v
			}
			resp := g.send(http.MethodPost, "/portal/agree", form)
			resp.Body.Close()
			if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(resp.Header.Get("Location"), "/en/connected/") {
				t.Fatalf("the guest was not let on: %d %s", resp.StatusCode, resp.Header.Get("Location"))
			}
			if got := consents(t, g.store, portalSite); len(got) != 0 {
				t.Fatalf("a guest who did not consent has a marketing record: %+v", got)
			}
			// The details they typed are kept with the grant — the venue's record of
			// who it let on — and are not a mailing list.
			if d := g.store.GuestDetails(); len(d) != 1 || text(d[0].Email) != "lan@example.test" {
				t.Fatalf("the guest's details were not kept with the grant: %+v", d)
			}
		})
	}
}

// TestATickedBoxIsRecordedWithTheTextShownAndTheGuestCanWithdrawIt: the consent
// carries the exact sentence and its SHA-256, and the guest's own button on the
// portal withdraws it, clearing the contact and keeping the evidence.
func TestATickedBoxIsRecordedWithTheTextShownAndTheGuestCanWithdrawIt(t *testing.T) {
	g := presentRig(t)
	askForDetails(t, g.store, portalSite)
	g.arrive(guestMAC).Body.Close()

	resp := g.send(http.MethodPost, "/portal/agree", url.Values{
		"lang": {"en"}, "name": {"Lan"}, "email": {"lan@example.test"},
		"marketingConsent": {"yes"}, "consentVersion": {versionOf(consentEN)},
	})
	resp.Body.Close()
	got := consents(t, g.store, portalSite)
	if len(got) != 1 {
		t.Fatalf("want one consent, got %d", len(got))
	}
	c := got[0]
	if c.ConsentText != consentEN || c.ConsentVersion != versionOf(consentEN) || c.Lang != gen.En {
		t.Errorf("the consent does not say what the guest read: %+v", c)
	}
	if c.ClientMac != guestMAC || text(c.Email) != "lan@example.test" || c.State != gen.Active {
		t.Errorf("the consent is not this guest's: %+v", c)
	}

	resp = g.send(http.MethodPost, "/portal/marketing-consent/withdraw", url.Values{"lang": {"vi"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/vi/connected/?n=consent-withdrawn" {
		t.Fatalf("withdrawal answered %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	c = consents(t, g.store, portalSite)[0]
	if c.State != gen.Withdrawn || c.WithdrawnAt == nil || c.WithdrawnBy == nil || *c.WithdrawnBy != gen.ConsentWithdrawnByGuest {
		t.Fatalf("not withdrawn by the guest: %+v", c)
	}
	if c.Email != nil || c.Name != nil || c.Phone != nil {
		t.Errorf("a withdrawn consent still carries the contact: %+v", c)
	}
	if c.ConsentVersion != versionOf(consentEN) {
		t.Error("withdrawal lost the evidence of what was agreed to")
	}
}

// TestAStrangerCannotWithdrawAnOnlineGuestsConsent: the guest consents and is
// online; somebody else — their own browser, their own cookie — arrives claiming
// the guest's MAC, which the controller confirms is on the network, and presses
// the withdrawal button. The consent must still be the guest's: presence proves
// the MAC is here, not that the caller is that device.
func TestAStrangerCannotWithdrawAnOnlineGuestsConsent(t *testing.T) {
	g := presentRig(t)
	askForDetails(t, g.store, portalSite)
	g.arrive(guestMAC).Body.Close()
	resp := g.send(http.MethodPost, "/portal/agree", url.Values{
		"lang": {"en"}, "email": {"lan@example.test"},
		"marketingConsent": {"yes"}, "consentVersion": {versionOf(consentEN)},
	})
	resp.Body.Close()
	if got := consents(t, g.store, portalSite); len(got) != 1 || got[0].State != gen.Active {
		t.Fatalf("setup: want one active consent, got %+v", got)
	}
	guestJar := g.jar

	// The stranger: a fresh cookie jar, the guest's MAC in the redirect parameters.
	g.jar = nil
	g.arrive(guestMAC).Body.Close()
	if len(g.jar) == 0 || g.jar[0].Value == guestJar[0].Value {
		t.Fatal("setup: the stranger did not get a session of their own")
	}
	resp = g.send(http.MethodPost, "/portal/marketing-consent/withdraw", url.Values{"lang": {"en"}})
	resp.Body.Close()
	// Answered exactly as a real withdrawal is — whether the device ever consented
	// is not the stranger's to learn.
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/en/connected/?n=consent-withdrawn" {
		t.Fatalf("withdrawal answered %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if c := consents(t, g.store, portalSite)[0]; c.State != gen.Active || text(c.Email) != "lan@example.test" {
		t.Fatalf("a stranger claiming the guest's MAC withdrew the guest's consent: %+v", c)
	}

	// And the guest, from the browser that signed in, still can.
	g.jar = guestJar
	resp = g.send(http.MethodPost, "/portal/marketing-consent/withdraw", url.Values{"lang": {"en"}})
	resp.Body.Close()
	if c := consents(t, g.store, portalSite)[0]; c.State != gen.Withdrawn {
		t.Fatalf("the guest's own session could not withdraw: %+v", c)
	}
}

// TestARequiredDetailIsAskedForBeforeAnythingIsClaimed: a voucher is not spent on a
// form the server is about to refuse, and the refusal never carries the value.
func TestARequiredDetailIsAskedForBeforeAnythingIsClaimed(t *testing.T) {
	for name, c := range map[string]struct {
		email  string
		reason string
	}{
		"blank":    {"", "details-required"},
		"no at":    {"lan.example.test", "details-invalid"},
		"two ats":  {"a@b@example.test", "details-invalid"},
		"no dot":   {"lan@localhost", "details-invalid"},
		"a tag in": {"<b>@example.test", "details-invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			g := presentRig(t)
			askForDetails(t, g.store, portalSite)
			g.arrive(guestMAC).Body.Close()
			resp := g.send(http.MethodPost, "/portal/voucher", url.Values{
				"lang": {"en"}, "code": {goodCode}, "email": {c.email},
			})
			resp.Body.Close()
			loc := resp.Header.Get("Location")
			if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/en/voucher/?") || !strings.Contains(loc, "e="+c.reason) {
				t.Fatalf("answered %d %q, want the voucher form with %s", resp.StatusCode, loc, c.reason)
			}
			if c.email != "" && strings.Contains(loc, url.QueryEscape(c.email)) {
				t.Errorf("the refused value travelled in the address: %s", loc)
			}
			if n := len(g.store.PortalStore.Grants()); n != 0 {
				t.Fatalf("%d grant(s) made on a refused form", n)
			}
		})
	}
}

// TestAnOptionalDetailIsOptional: a venue that asks but does not require lets a
// guest with nothing to give straight on, and records nothing about them.
func TestAnOptionalDetailIsOptional(t *testing.T) {
	g := presentRig(t)
	text := consentEN
	if _, err := g.store.SetGuestDataSettings(context.Background(), portalSite, gen.GuestDataSettingsUpdate{
		Fields:           []gen.GuestDataFieldSetting{{Field: gen.GuestDataFieldPhone}},
		MarketingConsent: gen.MarketingConsentSettingUpdate{Enabled: true, Text: gen.ConsentText{En: &text}},
	}); err != nil {
		t.Fatal(err)
	}
	g.arrive(guestMAC).Body.Close()
	resp := g.send(http.MethodPost, "/portal/voucher", url.Values{"lang": {"en"}, "code": {goodCode}})
	resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Location"), "/en/connected/") {
		t.Fatalf("an optional field kept the guest off: %q", resp.Header.Get("Location"))
	}
	if d := g.store.GuestDetails(); len(d) != 0 {
		t.Errorf("a guest who gave nothing has a details record: %+v", d)
	}
}

// TestGuestDataSettingsRefuseConsentWithNothingToContact covers the three rules
// beyond the schema and the audit entry a change writes.
func TestGuestDataSettingsRefuseConsentWithNothingToContact(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	path := "/sites/" + testSite + "/guest-data"
	text := "Send me offers."
	for name, c := range map[string]struct {
		body  map[string]any
		field string
		fault gen.FieldFault
	}{
		"a field twice": {map[string]any{
			"fields":           []any{map[string]any{"field": "email", "required": false}, map[string]any{"field": "email", "required": true}},
			"marketingConsent": map[string]any{"enabled": false, "text": map[string]any{}},
		}, "fields", gen.FieldFaultDuplicate},
		"consent with no words": {map[string]any{
			"fields":           []any{map[string]any{"field": "email", "required": false}},
			"marketingConsent": map[string]any{"enabled": true, "text": map[string]any{"en": "  "}},
		}, "marketingConsent.text", gen.FieldFaultRequired},
		"consent with nobody to contact": {map[string]any{
			"fields":           []any{map[string]any{"field": "name", "required": false}},
			"marketingConsent": map[string]any{"enabled": true, "text": map[string]any{"en": text}},
		}, "fields", gen.FieldFaultNeedsContactField},
	} {
		t.Run(name, func(t *testing.T) {
			resp := r.do(http.MethodPut, path, c.body)
			b := readAll(t, resp)
			if resp.StatusCode != http.StatusUnprocessableEntity {
				t.Fatalf("%d, want 422: %s", resp.StatusCode, b)
			}
			if !strings.Contains(b, `"`+c.field+`":"`+string(c.fault)+`"`) {
				t.Errorf("want %s on %s: %s", c.fault, c.field, b)
			}
		})
	}

	resp := r.do(http.MethodPut, path, map[string]any{
		"fields":           []any{map[string]any{"field": "email", "required": true}},
		"marketingConsent": map[string]any{"enabled": true, "text": map[string]any{"vi": " Gửi ưu đãi cho tôi. "}},
	})
	b := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a valid setting: %d %s", resp.StatusCode, b)
	}
	var set gen.GuestDataSettings
	_ = json.Unmarshal([]byte(b), &set)
	if set.MarketingConsent.Versions.Vi == nil || *set.MarketingConsent.Versions.Vi != versionOf("Gửi ưu đãi cho tôi.") {
		t.Errorf("the version is not the SHA-256 of the trimmed text: %+v", set.MarketingConsent)
	}
	if !auditHas(t, r, gen.AuditKindGuestDataSettingsChanged) {
		t.Error("the change is not in the audit log")
	}
}

// TestAnAdministratorWithdrawsOnAGuestsBehalfAndTheExportSaysSo: the admin action,
// its audit entry naming the consent by id only, and the export — which carries
// the consent, neutralises a hostile name, and after withdrawal carries the row
// with no contact so a mailing tool learns to stop.
func TestAnAdministratorWithdrawsOnAGuestsBehalfAndTheExportSaysSo(t *testing.T) {
	r := newRig(t)
	r.store.SetClock(func() time.Time { return at(0) })
	askForDetails(t, r.store, testSite)
	hostile := "=HYPERLINK(\"http://evil.example\")"
	email := "lan@example.test"
	if err := r.store.RecordMarketingConsent(context.Background(), api.ConsentGiven{
		SiteID: testSite, ClientMAC: "02:00:00:00:00:21", Name: &hostile, Email: &email, Version: versionOf(consentEN),
	}); err != nil {
		t.Fatal(err)
	}
	r.signIn(adminUser)

	export := func() map[string]string {
		t.Helper()
		resp := r.do(http.MethodGet, "/exports/marketing-consents?"+rangeQuery(at(-1), at(1)), nil)
		b := readAll(t, resp)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("export: %d %s", resp.StatusCode, b)
		}
		recs := readCSV(t, b)
		want := declaredColumns(t, "/exports/marketing-consents")
		if strings.Join(recs[0], ",") != strings.Join(want, ",") {
			t.Fatalf("header %v, want %v", recs[0], want)
		}
		if len(recs) != 2 {
			t.Fatalf("want one row, got %d", len(recs)-1)
		}
		return byColumn(recs[0], recs[1])
	}
	row := export()
	if row["name"] != "'"+hostile || row["email"] != email || row["state"] != "active" {
		t.Errorf("the export row is wrong or the formula was not neutralised: %v", row)
	}

	id := consents(t, r.store, testSite)[0].Id
	resp := r.do(http.MethodPost, "/sites/"+testSite+"/marketing-consents/"+id+"/withdraw", nil)
	b := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("withdraw: %d %s", resp.StatusCode, b)
	}
	var c gen.MarketingConsent
	_ = json.Unmarshal([]byte(b), &c)
	if c.State != gen.Withdrawn || c.WithdrawnBy == nil || *c.WithdrawnBy != gen.ConsentWithdrawnByOperator || c.Email != nil {
		t.Errorf("not withdrawn by an operator, or the contact survived: %s", b)
	}
	if !auditHas(t, r, gen.AuditKindMarketingConsentWithdrawn) {
		t.Error("the withdrawal is not in the audit log")
	}
	if strings.Contains(auditDump(t, r), email) {
		t.Error("the audit log carries the guest's e-mail address")
	}
	row = export()
	if row["state"] != "withdrawn" || row["email"] != "" || row["name"] != "" || row["withdrawnBy"] != "operator" {
		t.Errorf("the export does not tell a mailing tool to stop: %v", row)
	}

	// Staff read the network; they do not read, withdraw or take home a list of
	// people's contact details.
	s := newRig(t)
	s.signIn(staffUser)
	for _, p := range []struct{ method, path string }{
		{http.MethodGet, "/sites/" + testSite + "/marketing-consents"},
		{http.MethodPost, "/sites/" + testSite + "/marketing-consents/" + id + "/withdraw"},
		{http.MethodGet, "/exports/marketing-consents?" + rangeQuery(at(-1), at(1))},
	} {
		resp := s.do(p.method, p.path, nil)
		readAll(t, resp)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("staff %s %s: %d, want 403", p.method, p.path, resp.StatusCode)
		}
	}
}

func auditDump(t *testing.T, r *rig) string {
	t.Helper()
	resp := r.do(http.MethodGet, "/audit?limit=200", nil)
	return readAll(t, resp)
}

func auditHas(t *testing.T, r *rig, kind gen.AuditKind) bool {
	t.Helper()
	var p struct {
		Items []gen.AuditEntry `json:"items"`
	}
	_ = json.Unmarshal([]byte(auditDump(t, r)), &p)
	for _, e := range p.Items {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

func text(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
