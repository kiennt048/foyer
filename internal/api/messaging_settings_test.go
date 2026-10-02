package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
)

// Messaging settings are configured in the console, not in a file: each
// setting below is written through the console's operation and then proved to
// change what the next guest request does — on the same server, with no restart.
// A setting that saved and changed nothing would pass every read-back test and
// fail this one.

// consoleAndPortal is one server with both surfaces over one store, the shape
// `cmd/foyer` runs: an administrator writes on one listener and a guest meets the
// result on the other.
func consoleAndPortal(t *testing.T) (*rig, *guestRig) {
	t.Helper()
	w := portalWorld(t)
	w.Operators = []gen.Operator{{Id: "op-admin", Username: adminUser, Role: gen.Administrator, Enabled: true, CreatedAt: at(-100)}}
	w.Credentials = map[string]string{"op-admin": testSecret}
	store := memory.NewStore(w)
	store.SetClock(func() time.Time { return at(0) })
	d := newGuestDriver()
	d.Presences[guestMAC] = true
	srv, err := api.New(api.Config{Ports: store.Ports(), Portal: store.PortalPorts(d), Now: func() time.Time { return at(0) }})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	admin := httptest.NewServer(srv.Handler())
	guest := httptest.NewServer(srv.PortalHandler())
	t.Cleanup(admin.Close)
	t.Cleanup(guest.Close)
	r := &rig{t: t, server: admin, store: store}
	r.signIn(adminUser)
	return r, &guestRig{t: t, server: guest, store: store, driver: d}
}

func mustStatus(t *testing.T, resp *http.Response, want int) string {
	t.Helper()
	b := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("%d, want %d: %s", resp.StatusCode, want, b)
	}
	return b
}

func requestCode(g *guestRig, phone string) string {
	resp := g.send(http.MethodPost, "/portal/phone", url.Values{"phone": {phone}, "lang": {"en"}})
	resp.Body.Close()
	return resp.Header.Get("Location")
}

// The site's calendar moves every daily boundary: a trend bucket starts at the
// site's midnight, and a portal block that ended yesterday-in-UTC is still live at
// a venue that is still in yesterday. `at(0)` is 09:00 UTC, which is 22:00 the day
// before in Pago Pago (UTC−11).
func TestTheSitesTimeZoneMovesTheDayBoundaryWithoutARestart(t *testing.T) {
	r, g := consoleAndPortal(t)
	path := "/sites/" + portalSite + "/time-zone"

	for _, bad := range []string{"Local", "Mars/Olympus_Mons", " "} {
		b := mustStatus(t, r.do(http.MethodPut, path, map[string]any{"timeZone": bad}), http.StatusUnprocessableEntity)
		if !strings.Contains(b, `"timeZone":"not-a-time-zone"`) {
			t.Errorf("%q: want not-a-time-zone, got %s", bad, b)
		}
	}

	yesterday := at(-1).Format("2006-01-02")
	set := g.store.World().Portal[portalSite]
	var d gen.PortalDesign
	raw, _ := json.Marshal(design(block("yesterday", "text", "welcome", map[string]any{"endsOn": yesterday,
		"content": map[string]any{"en": map[string]any{"title": "Ends yesterday"}}})))
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	set.Design = d
	g.store.World().Portal[portalSite] = set
	if b := body(t, g.arrive(guestMAC)); strings.Contains(b, "Ends yesterday") {
		t.Fatal("before the zone is set, a block that ended yesterday is live on the server's day")
	}

	b := mustStatus(t, r.do(http.MethodPut, path, map[string]any{"timeZone": "Pacific/Pago_Pago"}), http.StatusOK)
	var tz gen.SiteTimeZone
	_ = json.Unmarshal([]byte(b), &tz)
	if tz.TimeZone != "Pacific/Pago_Pago" {
		t.Fatalf("read back %s", b)
	}
	if b := body(t, g.arrive(guestMAC)); !strings.Contains(b, "Ends yesterday") {
		t.Error("the venue is still in yesterday, and its block for yesterday is not live")
	}

	q := url.Values{"siteId": {portalSite}, "from": {at(-2).Format(time.RFC3339)}, "to": {at(0).Format(time.RFC3339)}}
	resp := r.do(http.MethodGet, "/trends?"+q.Encode(), nil)
	var tr gen.Trends
	_ = json.Unmarshal([]byte(mustStatus(t, resp, http.StatusOK)), &tr)
	loc, _ := time.LoadLocation("Pacific/Pago_Pago")
	if len(tr.Buckets) == 0 {
		t.Fatal("no buckets")
	}
	for _, bucket := range tr.Buckets {
		if lt := bucket.Start.In(loc); lt.Hour() != 0 || lt.Minute() != 0 {
			t.Errorf("bucket starts %s, which is not midnight in the site's zone", bucket.Start)
		}
	}
	if !auditHas(t, r, gen.AuditKindSiteTimeZoneChanged) {
		t.Error("the change is not in the audit log")
	}
}

// perAddressPerHour and the destination ranges, tuned in the console, hold the
// next code requested — tighter, and then looser, with nothing restarted.
func TestTunedAbuseControlsHoldTheNextCode(t *testing.T) {
	r, g := consoleAndPortal(t)
	configurePhone(t, g)
	path := "/sites/" + portalSite + "/portal/phone/abuse-controls"
	limits := func(perAddress int) map[string]any {
		return map[string]any{"perNumberPerHour": 3, "perNumberPerDay": 5, "perDevicePerHour": 5,
			"perAddressPerHour": perAddress, "codeMinutes": 5, "maxAttempts": 5}
	}

	b := mustStatus(t, r.do(http.MethodPut, path, map[string]any{
		"limits": map[string]any{"perNumberPerHour": 4, "perNumberPerDay": 3, "perDevicePerHour": 5,
			"perAddressPerHour": 20, "codeMinutes": 5, "maxAttempts": 5},
		"mobilePrefixes": []string{"09"}}), http.StatusUnprocessableEntity)
	if !strings.Contains(b, `"limits.perNumberPerDay":"below-minimum"`) || !strings.Contains(b, `"minimumCount":4`) {
		t.Errorf("a day limit under the hour limit: %s", b)
	}
	mustStatus(t, r.do(http.MethodPut, path, map[string]any{"limits": limits(0), "mobilePrefixes": []string{"09"}}),
		http.StatusUnprocessableEntity) // the schema's floor: there is no "off"

	b = mustStatus(t, r.do(http.MethodPut, path, map[string]any{"limits": limits(1), "mobilePrefixes": []string{"09"}}), http.StatusOK)
	var c gen.PhoneAbuseControls
	_ = json.Unmarshal([]byte(b), &c)
	if !c.Customised || c.Limits.PerAddressPerHour != 1 {
		t.Fatalf("read back %s", b)
	}
	var p gen.PhoneSignIn
	_ = json.Unmarshal([]byte(mustStatus(t, r.do(http.MethodGet, "/sites/"+portalSite+"/portal/phone", nil), http.StatusOK)), &p)
	if p.Limits.PerAddressPerHour != 1 {
		t.Errorf("the sign-in panel states %d an hour per address, the venue set 1", p.Limits.PerAddressPerHour)
	}

	g.arrive(guestMAC).Body.Close()
	if loc := requestCode(g, "0332345678"); !strings.Contains(loc, "e=phone-invalid") {
		t.Errorf("a 03x number with only 09 open: %q", loc)
	}
	if loc := requestCode(g, "0912345678"); loc != "/en/phone/?sent=1" {
		t.Fatalf("first code: %q", loc)
	}
	if loc := requestCode(g, "0912345678"); !strings.Contains(loc, "e=code-limit") {
		t.Fatalf("second code from one address at a limit of one: %q", loc)
	}
	mustStatus(t, r.do(http.MethodPut, path, map[string]any{"limits": limits(3), "mobilePrefixes": []string{"03", "09"}}), http.StatusOK)
	if loc := requestCode(g, "0912345678"); loc != "/en/phone/?sent=1" {
		t.Fatalf("after raising the limit, the next code: %q", loc)
	}
	if loc := requestCode(g, "0332345678"); loc != "/en/phone/?sent=1" {
		t.Fatalf("after opening 03, a 03x number: %q", loc)
	}
	if n := len(g.store.Phone().SMS.Sent()); n != 3 {
		t.Errorf("%d messages, want 3", n)
	}
}

// The deployment-wide cap refuses past its limit, fails the way the per-site
// ceiling does, and clearing it lets the next code through.
func TestTheDeploymentSpendCapHoldsEverySend(t *testing.T) {
	r, g := consoleAndPortal(t)
	configurePhone(t, g)
	var cap gen.PhoneSpendCap
	_ = json.Unmarshal([]byte(mustStatus(t, r.do(http.MethodGet, "/phone-sign-in/spend-cap", nil), http.StatusOK)), &cap)
	if cap.DailyLimit != nil {
		t.Fatalf("a cap nobody set: %+v", cap)
	}
	mustStatus(t, r.do(http.MethodPut, "/phone-sign-in/spend-cap", map[string]any{"dailyLimit": 0}), http.StatusUnprocessableEntity)
	mustStatus(t, r.do(http.MethodPut, "/phone-sign-in/spend-cap", map[string]any{"dailyLimit": 1}), http.StatusOK)

	g.arrive(guestMAC).Body.Close()
	if loc := requestCode(g, "0912345678"); loc != "/en/phone/?sent=1" {
		t.Fatalf("first code: %q", loc)
	}
	if loc := requestCode(g, "0987654321"); !strings.Contains(loc, "e=code-unavailable") {
		t.Fatalf("second code under a deployment cap of one: %q", loc)
	}
	mustStatus(t, r.do(http.MethodPut, "/phone-sign-in/spend-cap", map[string]any{"dailyLimit": nil}), http.StatusOK)
	if loc := requestCode(g, "0987654321"); loc != "/en/phone/?sent=1" {
		t.Fatalf("with the cap cleared: %q", loc)
	}

	var p struct {
		Items []gen.AuditEntry `json:"items"`
	}
	_ = json.Unmarshal([]byte(auditDump(t, r)), &p)
	found := false
	for _, e := range p.Items {
		if e.Kind == gen.AuditKindPhoneSpendCapChanged {
			found = true
			if e.SiteId != nil {
				t.Errorf("the deployment's cap was logged against a venue: %+v", e)
			}
		}
	}
	if !found {
		t.Error("the cap change is not in the audit log")
	}
}

// The console's test message is a real, counted message through the saved
// provider; its answer and its audit entry name the outcome and never the number
// or a key.
func TestATestMessageIsSentAndCountedAndLeaksNothing(t *testing.T) {
	r, g := consoleAndPortal(t)
	path := "/sites/" + portalSite + "/portal/phone/test-message"
	var res gen.PhoneTestMessageResult
	_ = json.Unmarshal([]byte(mustStatus(t, r.do(http.MethodPost, path, map[string]any{"phone": "0912345678"}), http.StatusOK)), &res)
	if res.Outcome != gen.PhoneTestMessageOutcomeNotConfigured {
		t.Fatalf("before a provider is saved: %+v", res)
	}
	configurePhone(t, g)

	b := mustStatus(t, r.do(http.MethodPost, path, map[string]any{"phone": "0912 345 678"}), http.StatusOK)
	_ = json.Unmarshal([]byte(b), &res)
	if res.Outcome != gen.PhoneTestMessageOutcomeSent || res.SentToday != 1 {
		t.Fatalf("test message: %s", b)
	}
	msg := g.store.Phone().SMS.Last()
	if msg.To != "+84912345678" || !strings.Contains(msg.Text, msg.Code) || len(msg.Code) != 6 {
		t.Fatalf("the provider got %+v", msg)
	}
	_ = json.Unmarshal([]byte(mustStatus(t, r.do(http.MethodPost, path, map[string]any{"phone": "0241234567"}), http.StatusOK)), &res)
	if res.Outcome != gen.PhoneTestMessageOutcomePhoneInvalid {
		t.Errorf("a landline: %+v", res)
	}
	log := auditDump(t, r)
	for _, leak := range []string{"912345678", "241234567", "api-key", "secret-key"} {
		if strings.Contains(log, leak) || strings.Contains(b, leak) {
			t.Errorf("%q is in the answer or the audit log", leak)
		}
	}
	if !strings.Contains(log, `"section":"phone-test-message"`) {
		t.Error("the test message is not in the audit log")
	}
}

// The venue's guest-data setting applies to phone sign-in. The code form
// is read against it before the code is spent, and a consent given there names
// the number the guest just proved.
func TestAPhoneGuestGivesTheVenuesDetailsAndConsentsWithTheVerifiedNumber(t *testing.T) {
	r, g := consoleAndPortal(t)
	configurePhone(t, g)
	consent := "Send me offers from The Venue by SMS."
	mustStatus(t, r.do(http.MethodPut, "/sites/"+portalSite+"/guest-data", map[string]any{
		"fields": []any{
			map[string]any{"field": "name", "required": true},
			map[string]any{"field": "phone", "required": true},
		},
		"marketingConsent": map[string]any{"enabled": true, "text": map[string]any{"en": consent}},
	}), http.StatusOK)

	g.arrive(guestMAC).Body.Close()
	if loc := requestCode(g, "0912345678"); loc != "/en/phone/?sent=1" {
		t.Fatalf("code: %q", loc)
	}
	code := g.store.Phone().SMS.Last().Code

	// The required name left blank: back to the code form, with the code unspent.
	resp := g.send(http.MethodPost, "/portal/phone/code", url.Values{"code": {code}, "lang": {"en"}})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/en/phone/?e=details-required&sent=1" {
		t.Fatalf("a blank required name: %q", loc)
	}
	resp = g.send(http.MethodPost, "/portal/phone/code", url.Values{
		"code": {code}, "lang": {"en"}, "name": {"Lan"},
		"marketingConsent": {"yes"}, "consentVersion": {versionOf(consent)},
	})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/en/connected/" {
		t.Fatalf("the same code after the details were given: %q", loc)
	}
	got := consents(t, g.store, portalSite)
	if len(got) != 1 || text(got[0].Phone) != "+84912345678" || text(got[0].Name) != "Lan" {
		t.Fatalf("the consent does not name the verified number: %+v", got)
	}
	if d := g.store.GuestDetails(); len(d) != 1 || text(d[0].Phone) != "+84912345678" || text(d[0].Name) != "Lan" {
		t.Fatalf("the guest's details: %+v", d)
	}
}
