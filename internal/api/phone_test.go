package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity/otp"
	"github.com/kiennt048/foyer/internal/portal"
)

// Phone sign-in over HTTP: the wire half of what `internal/portal/phone_test.go`
// and `internal/identity/otp` prove against their stores.

func configurePhone(t *testing.T, g *guestRig) {
	t.Helper()
	brand := "FOYER"
	key, secret := "api-key", "secret-key"
	_, err := g.store.SetPhoneSignIn(context.Background(), portalSite, gen.PhoneSignInUpdate{
		Provider: gen.EsmsSms, Brandname: &brand, ApiKey: &key, SecretKey: &secret,
		DailyLimit: 50, SessionMinutes: 120, MessageTemplate: "{code} la ma Wi-Fi",
	})
	if err != nil {
		t.Fatalf("configure: %v", err)
	}
	offerPhone(g)
}

// offerPhone shows the button: `phone` in the venue's methods.
func offerPhone(g *guestRig) {
	w := g.store.World()
	s := w.Portal[portalSite]
	s.Methods = append(s.Methods, gen.PortalMethodPhone)
	w.Portal[portalSite] = s
}

// A configured provider is not enough: with the button hidden, both endpoints
// answer 404 rather than spending the venue's money for whoever found them.
func TestPhoneEndpointsAreRefusedWhileTheButtonIsHidden(t *testing.T) {
	g := presentRig(t)
	configurePhone(t, g)
	w := g.store.World()
	s := w.Portal[portalSite]
	s.Methods = []gen.PortalMethod{gen.PortalMethodVoucher}
	w.Portal[portalSite] = s
	g.arrive(guestMAC).Body.Close()
	resp := g.send(http.MethodPost, "/portal/phone", url.Values{"phone": {"0912345678"}, "lang": {"en"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || len(g.store.Phone().SMS.Sent()) != 0 {
		t.Fatalf("%d, %d sent", resp.StatusCode, len(g.store.Phone().SMS.Sent()))
	}
}

func TestAGuestSignsInWithTheirPhone(t *testing.T) {
	g := presentRig(t)
	configurePhone(t, g)
	g.arrive(guestMAC).Body.Close()

	send := g.send(http.MethodPost, "/portal/phone", url.Values{"phone": {"0912 345 678"}, "lang": {"vi"}})
	send.Body.Close()
	if send.StatusCode != http.StatusSeeOther {
		t.Fatalf("requestPhoneCode: %d", send.StatusCode)
	}
	if loc := send.Header.Get("Location"); loc != "/vi/phone/?sent=1" {
		t.Fatalf("location %q", loc)
	}
	msg := g.store.Phone().SMS.Last()
	if msg.To != "+84912345678" || len(msg.Code) != 6 {
		t.Fatalf("sent %+v", msg)
	}

	// A wrong code keeps the code form and grants nothing.
	wrong := "000000"
	if wrong == msg.Code {
		wrong = "111111"
	}
	bad := g.send(http.MethodPost, "/portal/phone/code", url.Values{"code": {wrong}, "lang": {"vi"}})
	bad.Body.Close()
	if bad.StatusCode != http.StatusSeeOther || !strings.Contains(bad.Header.Get("Location"), "e=code-wrong") ||
		!strings.Contains(bad.Header.Get("Location"), "sent=1") {
		t.Fatalf("wrong code: %d %q", bad.StatusCode, bad.Header.Get("Location"))
	}
	if n := len(g.store.PortalStore.Grants()); n != 0 {
		t.Fatalf("%d grants after a wrong code", n)
	}

	ok := g.send(http.MethodPost, "/portal/phone/code", url.Values{"code": {msg.Code}, "lang": {"vi"}})
	ok.Body.Close()
	if ok.StatusCode != http.StatusSeeOther || ok.Header.Get("Location") != "/vi/connected/" {
		t.Fatalf("verifyPhoneCode: %d %q", ok.StatusCode, ok.Header.Get("Location"))
	}
	grants := g.store.PortalStore.Grants()
	if len(grants) != 1 || grants[0].Method != portal.MethodPhone || grants[0].Phone != "+84912345678" {
		t.Fatalf("grants %+v", grants)
	}
	// The NAS is asked *after* the 303 is flushed (CONTRACT inv. 4), so the client
	// can hold the redirect before the call has happened; wait for it, boundedly.
	deadline := time.Now().Add(10 * time.Second)
	for g.driver.AuthorizeCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := g.driver.AuthorizeCount(); n != 1 {
		t.Fatalf("the driver was asked to authorize %d times", n)
	}

	status := g.send(http.MethodGet, "/portal/status", nil)
	var s gen.PortalStatus
	_ = json.Unmarshal([]byte(body(t, status)), &s)
	if s.State != gen.PortalStatusStateConnected || s.Plan.DurationMinutes == nil || *s.Plan.DurationMinutes != 120 {
		t.Fatalf("status %+v", s)
	}
}

// TestAPhoneGuestWhoDoesNotTickTheBoxLeavesNoMarketingRecord: the phone branch
// completes the consent from the verified number (`withVerifiedPhone`) and carries
// its own copy of the "the box says exactly yes" check — and removing that copy
// leaves every other test green. Here the venue collects a phone number and asks
// for consent; a guest who leaves the box alone, or sends something that is not
// `yes`, beside a valid version, must leave no marketing record. The ticked case
// is the control that proves the branch is reached at all.
func TestAPhoneGuestWhoDoesNotTickTheBoxLeavesNoMarketingRecord(t *testing.T) {
	const consentText = "Send me offers from The Venue by text message."
	for name, c := range map[string]struct {
		extra url.Values
		want  int
	}{
		"box left unticked":       {url.Values{"consentVersion": {versionOf(consentText)}}, 0},
		"a value that is not yes": {url.Values{"marketingConsent": {"on"}, "consentVersion": {versionOf(consentText)}}, 0},
		"ticked":                  {url.Values{"marketingConsent": {"yes"}, "consentVersion": {versionOf(consentText)}}, 1},
	} {
		t.Run(name, func(t *testing.T) {
			g := presentRig(t)
			configurePhone(t, g)
			text := consentText
			if _, err := g.store.SetGuestDataSettings(context.Background(), portalSite, gen.GuestDataSettingsUpdate{
				Fields:           []gen.GuestDataFieldSetting{{Field: gen.GuestDataFieldPhone, Required: false}},
				MarketingConsent: gen.MarketingConsentSettingUpdate{Enabled: true, Text: gen.ConsentText{En: &text}},
			}); err != nil {
				t.Fatal(err)
			}
			g.arrive(guestMAC).Body.Close()
			g.send(http.MethodPost, "/portal/phone", url.Values{"phone": {"0912345678"}, "lang": {"en"}}).Body.Close()
			form := url.Values{"code": {g.store.Phone().SMS.Last().Code}, "lang": {"en"}}
			for k, v := range c.extra {
				form[k] = v
			}
			resp := g.send(http.MethodPost, "/portal/phone/code", form)
			resp.Body.Close()
			if resp.Header.Get("Location") != "/en/connected/" {
				t.Fatalf("the guest was not let on: %d %q", resp.StatusCode, resp.Header.Get("Location"))
			}
			got := consents(t, g.store, portalSite)
			if len(got) != c.want {
				t.Fatalf("%d marketing record(s), want %d: %+v", len(got), c.want, got)
			}
			if c.want == 1 && (got[0].Phone == nil || *got[0].Phone != "+84912345678") {
				t.Fatalf("the ticked consent does not carry the verified number: %+v", got[0])
			}
		})
	}
}

// The number never reaches a Location header, on success or on any refusal.
func TestThePhoneNumberIsNeverInARedirect(t *testing.T) {
	g := presentRig(t)
	configurePhone(t, g)
	g.arrive(guestMAC).Body.Close()
	for _, phone := range []string{"0912345678", "+12025550123", "0241234567"} {
		resp := g.send(http.MethodPost, "/portal/phone", url.Values{"phone": {phone}, "lang": {"en"}})
		resp.Body.Close()
		for _, fragment := range []string{"912345678", "2025550123", "241234567"} {
			if strings.Contains(resp.Header.Get("Location"), fragment) {
				t.Fatalf("%q put the number in %q", phone, resp.Header.Get("Location"))
			}
		}
	}
}

func TestNoCodeIsSentToAMACTheDriverDidNotConfirm_OverHTTP(t *testing.T) {
	g := presentRig(t)
	configurePhone(t, g)
	g.arrive(strangerMAC).Body.Close()
	resp := g.send(http.MethodPost, "/portal/phone", url.Values{"phone": {"0912345678"}, "lang": {"en"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || strings.Contains(resp.Header.Get("Location"), "sent=1") {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if n := len(g.store.Phone().SMS.Sent()); n != 0 {
		t.Fatalf("%d messages were paid for on behalf of a device nobody confirmed", n)
	}
}

func TestPhoneSignInIsNotOfferedUntilConfigured(t *testing.T) {
	g := presentRig(t)
	g.arrive(guestMAC).Body.Close()
	for path, form := range map[string]url.Values{
		"/portal/phone":      {"phone": {"0912345678"}, "lang": {"en"}},
		"/portal/phone/code": {"code": {"123456"}, "lang": {"en"}},
	} {
		resp := g.send(http.MethodPost, path, form)
		b := body(t, resp)
		if resp.StatusCode != http.StatusNotFound || !strings.Contains(b, string(gen.ProblemsmethodNotOffered)) {
			t.Fatalf("%s: %d %s", path, resp.StatusCode, b)
		}
	}
}

func TestPhoneSignInSettingsNeverReturnTheKeysAndAreAudited(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	path := "/sites/" + testSite + "/portal/phone"

	get := r.do(http.MethodGet, path, nil)
	raw := readAll(t, get)
	get.Body.Close()
	if get.StatusCode != http.StatusOK || !strings.Contains(raw, `"provider":null`) || !strings.Contains(raw, `"credentialsSet":false`) {
		t.Fatalf("unconfigured: %d %s", get.StatusCode, raw)
	}

	// The first configuration without keys is refused, naming both.
	noKeys := map[string]any{"provider": "esms-sms", "brandname": "FOYER", "dailyLimit": 20, "sessionMinutes": 60,
		"messageTemplate": "{code} la ma Wi-Fi"}
	resp := r.do(http.MethodPut, path, noKeys)
	raw = readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(raw, "apiKey") {
		t.Fatalf("first write without keys: %d %s", resp.StatusCode, raw)
	}

	// A template without the code is refused with its own fault.
	bad := map[string]any{"provider": "esms-sms", "brandname": "FOYER", "dailyLimit": 20, "sessionMinutes": 60,
		"messageTemplate": "Welcome to the venue", "apiKey": "AAA-KEY-VALUE", "secretKey": "BBB-SECRET-VALUE"}
	resp = r.do(http.MethodPut, path, bad)
	raw = readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(raw, string(gen.FieldFaultMissingCodePlaceholder)) {
		t.Fatalf("template: %d %s", resp.StatusCode, raw)
	}

	good := map[string]any{"provider": "esms-sms", "brandname": "FOYER", "dailyLimit": 20, "sessionMinutes": 60,
		"messageTemplate": "{code} la ma Wi-Fi", "apiKey": "AAA-KEY-VALUE", "secretKey": "BBB-SECRET-VALUE"}
	resp = r.do(http.MethodPut, path, good)
	raw = readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || strings.Contains(raw, "AAA-KEY-VALUE") || strings.Contains(raw, "BBB-SECRET-VALUE") ||
		!strings.Contains(raw, `"credentialsSet":true`) || !strings.Contains(raw, `"perNumberPerHour":3`) {
		t.Fatalf("configure: %d %s", resp.StatusCode, raw)
	}
	var audited int
	for _, e := range r.store.World().Audit {
		if e.Kind == gen.AuditKindPortalSettingsChanged && e.Params != nil && (*e.Params)["section"] == "phone-sign-in" {
			audited++
			if b, _ := json.Marshal(e); strings.Contains(string(b), "AAA-KEY") {
				t.Fatalf("the audit entry carries a key: %s", b)
			}
		}
	}
	if audited != 1 {
		t.Fatalf("%d audit entries for one configuration", audited)
	}
}

func TestPhoneCannotBeOfferedBeforeItIsConfigured(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	resp := r.do(http.MethodPatch, "/sites/"+testSite+"/portal", map[string]any{"methods": []string{"voucher", "phone"}})
	raw := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(raw, string(gen.FieldFaultNeedsPhoneProvider)) {
		t.Fatalf("%d %s", resp.StatusCode, raw)
	}
	_ = api.BasePath
}

// TestTheCodeFormStatesTheVenuesCodeLifetime: a code form that says *5 minutes*
// in both languages is wrong once an administrator can set the lifetime anywhere
// from one to ten. Both the served
// document and the JSON context must carry the venue's own number, from the same
// abuse controls the code's expiry is set from.
func TestTheCodeFormStatesTheVenuesCodeLifetime(t *testing.T) {
	if portal.DefaultPhoneCodeMinutes != int(otp.DefaultLimits.CodeLifetime.Minutes()) {
		t.Fatalf("the page's default lifetime (%d) is not the code's (%v)", portal.DefaultPhoneCodeMinutes, otp.DefaultLimits.CodeLifetime)
	}
	g := presentRig(t)
	configurePhone(t, g)
	if _, err := g.store.SetPhoneAbuseControls(context.Background(), portalSite, gen.PhoneAbuseControlsUpdate{
		Limits: gen.PhoneAbuseLimitsUpdate{
			CodeMinutes: 2, MaxAttempts: 5, PerAddressPerHour: 60, PerDevicePerHour: 5, PerNumberPerDay: 5, PerNumberPerHour: 3,
		},
		MobilePrefixes: []gen.PhoneMobilePrefix{"03", "05", "07", "08", "09"},
	}); err != nil {
		t.Fatal(err)
	}

	var pc gen.PortalContext
	if err := json.Unmarshal([]byte(body(t, g.arrive(guestMAC))), &pc); err != nil {
		t.Fatal(err)
	}
	if pc.PhoneCodeMinutes == nil || *pc.PhoneCodeMinutes != 2 {
		t.Fatalf("getPortalContext.phoneCodeMinutes = %v, want the venue's 2", pc.PhoneCodeMinutes)
	}

	srv, err := api.New(api.Config{Ports: g.store.Ports(), Portal: g.store.PortalPorts(g.driver)})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.GuestDocumentsHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/vi/phone/?sent=1", nil))
	if doc := rec.Body.String(); !strings.Contains(doc, "hiệu lực trong 2 phút") || strings.Contains(doc, "5 phút") {
		t.Fatalf("the served code form does not state the venue's two minutes:\n%s", doc)
	}
}

// The phone page is a document the Go twin serves, both steps.
func TestThePhoneDocumentRendersFromTheSentFlag(t *testing.T) {
	g := presentRig(t)
	store := g.store
	srv, err := api.New(api.Config{Ports: store.Ports(), Portal: store.PortalPorts(g.driver)})
	if err != nil {
		t.Fatal(err)
	}
	h := srv.GuestDocumentsHandler()
	for query, want := range map[string]string{
		"":                     `action="/api/v0/portal/phone"`,
		"?sent=1":              `action="/api/v0/portal/phone/code"`,
		"?sent=1&e=code-wrong": "That code is not right",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/en/phone/"+query, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%q: %d, want %q in\n%s", query, rec.Code, want, rec.Body.String())
		}
	}
}
