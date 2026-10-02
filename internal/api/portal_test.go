package api_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/portal"
)

// The guest portal, driven over HTTP.
//
// The rules being checked here are the ones that are properties of the *wire*
// rather than of a decision, and every one of them is invisible in a browser:
//
//   - **`303`, never `200`.** The iOS Captive Network Assistant re-checks
//     captivity only after a full document navigation. Without one its button
//     stays "Cancel" and Cancel disassociates the device, so a portal that
//     answered `200` would authenticate the guest and then throw them off the
//     network — and a passing screenshot would prove nothing.
//   - **Nothing the guest must see comes after authorization.** Android destroys
//     the login WebView the instant the network validates.
//   - **No grant for a MAC the driver did not confirm**, through the routes as
//     well as through the service.
//
// `internal/portal` has the same rules asserted against a store it can see the
// inside of. Both exist: one proves the rule, this one proves the rule is wired
// to a route.

const (
	portalSite  = "site-portal"
	apMAC       = "11:22:33:44:55:66"
	guestMAC    = "aa:bb:cc:dd:ee:ff"
	strangerMAC = "99:88:77:66:55:44"
	goodCode    = "ABCDEFGHJKMN"
	guestUser   = "kim.guest"
	guestPass   = "a-long-enough-passphrase"
)

// portalWorld is a venue that offers all three ways in.
func portalWorld(t *testing.T) *memory.World {
	t.Helper()
	mac := apMAC
	sixty := 60
	thirty := 30
	name := "Café 1 hour"

	// The venue's terms are assigned rather than written as a literal: the field
	// is an anonymous struct generated from the contract, and a literal here
	// would have to restate its JSON tags — a second copy of a shape that is
	// regenerated on every `make generate`.
	settings := gen.PortalSettings{
		SiteId: portalSite, VenueName: "The Venue",
		Languages:           []gen.Language{gen.En, gen.Vi},
		Methods:             []gen.PortalMethod{gen.PortalMethodVoucher, gen.PortalMethodAccount, gen.PortalMethodClickThrough},
		ClickThroughMinutes: &thirty,
	}
	settings.Terms.Summary = map[string]string{
		"en": "Internet access is provided as is.",
		"vi": "Truy cập Internet được cung cấp nguyên trạng.",
	}

	w := &memory.World{
		Sites: []gen.Site{{Id: portalSite, Name: "The Venue", Environment: gen.SiteEnvironmentLive}},
		Nas: []gen.Nas{{
			Id: "nas-portal", Name: "Front of house", SiteId: portalSite, Mac: &mac,
			Driver: gen.NasDriverKindUnifi, Enabled: true, SecretSet: true,
			Reachability:         gen.NasReachabilityOk,
			MessageAuthenticator: gen.NasMessageAuthenticatorEnforced,
			Source:               gen.Discovered,
			Capabilities:         gen.Capabilities{WalledGarden: gen.GardenKindFqdnSni, HardQuota: true},
		}},
		Batches: []gen.VoucherBatch{{
			Id: "batch-1", SiteId: portalSite, Label: "Front counter", Count: 1,
			CreatedAt: at(-1), ExpiryMode: gen.FromFirstUse,
			Plan: gen.Plan{
				Name: &name, DurationMinutes: &sixty,
				EnforcedBy: gen.PlanEnforcedByUnenforced,
			},
		}},
		Vouchers: []gen.Voucher{{
			Id: "voucher-1", BatchId: "batch-1", Code: goodCode,
			State: gen.VoucherStateUnused, CreatedAt: ptrTime(at(-1)),
		}},
		Accounts: []gen.Account{{
			Id: "account-1", SiteId: portalSite, Username: guestUser, Enabled: true,
			CredentialKind: gen.Argon2id, CreatedAt: at(-30),
		}},
		Credentials: map[string]string{"account-1": guestPass},
	}
	w.Portal = map[string]gen.PortalSettings{portalSite: settings}
	return w
}

func ptrTime(t time.Time) *time.Time { return &t }

// errControllerDown is what an unreachable UniFi looks like from here: a
// transport error beside a `PresenceUnavailable` outcome. Both halves matter —
// a caller checking only one of them classifies an outage as a refusal half the
// time, which is how the collapse goes unnoticed.
var errControllerDown = errors.New("dial tcp 192.0.2.1:443: connect: connection refused")

// newGuestDriver is the shipped mock, configured the way this file needs it.
//
// **Not a hand-rolled fake.** The shipped `MockDriver` reaches all six
// `PresenceOutcome` values, and `WithControllerUnreachable` returns the outcome
// *and* a non-nil error rather than a bare false, which is the shape this file
// exists to keep apart from a refusal. A duplicate would be a second copy of a
// driver's behaviour sitting one import away from the original.
//
// **Strict**, and nothing else set. No permissive default, because a fake that
// answered "present" for a MAC nobody told it about would make the named
// security test pass against a portal that never called a driver at all — that
// swap is the sabotage recorded on
// `TestVerifyPresence_NoGrantForAMACTheDriverDidNotConfirm_OverHTTP`.
//
// `HardQuota` is *not* raised here, and the retired fake raising it was the
// mistake in miniature: the shipped UniFi driver already reports it DOCUMENTED,
// so the literal was a second copy of a value one file away that happened to
// agree. Derived, this venue enforces its plan on the NAS for the reason a real
// one does, and the day that changes the test says so instead of agreeing with
// itself.
func newGuestDriver() *nas.MockDriver {
	return nas.NewStrictMockDriver(nas.VendorUniFi)
}

// guestRig is the portal listener with one venue behind it.
type guestRig struct {
	t      *testing.T
	server *httptest.Server
	store  *memory.Store
	driver *nas.MockDriver
	jar    []*http.Cookie
}

func newGuestRig(t *testing.T, driver *nas.MockDriver) *guestRig {
	t.Helper()
	store := memory.NewStore(portalWorld(t))
	store.SetClock(func() time.Time { return at(0) })

	srv, err := api.New(api.Config{
		Ports:  store.Ports(),
		Portal: store.PortalPorts(driver),
		Now:    func() time.Time { return at(0) },
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv.PortalHandler())
	t.Cleanup(ts.Close)
	return &guestRig{t: t, server: ts, store: store, driver: driver}
}

// presentRig tells the driver this one MAC really is associated, and nothing else.
func presentRig(t *testing.T) *guestRig {
	t.Helper()
	d := newGuestDriver()
	d.Presences[guestMAC] = true
	return newGuestRig(t, d)
}

// arrive is the NAS redirect: the request that carries the parameters and gets a
// session cookie back.
func (g *guestRig) arrive(mac string) *http.Response {
	g.t.Helper()
	q := url.Values{"ap": {apMAC}, "id": {mac}, "ssid": {"Venue Guest"}, "url": {"http://example.test/news"}}
	return g.send(http.MethodGet, "/portal/context?"+q.Encode(), nil)
}

// send makes one request, carrying whatever cookies the portal has set.
//
// The client refuses to follow redirects, because the redirect is the assertion:
// a helper that followed one would turn "the portal answered 303 to the connected
// page" into "the portal answered 404 for a document this server does not serve".
func (g *guestRig) send(method, path string, form url.Values) *http.Response {
	g.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, g.server.URL+api.BasePath+path, body)
	if err != nil {
		g.t.Fatalf("new request: %v", err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range g.jar {
		req.AddCookie(c)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		g.t.Fatalf("%s %s: %v", method, path, err)
	}
	for _, c := range resp.Cookies() {
		if c.Name == portal.SessionCookieName && c.Value != "" {
			g.jar = []*http.Cookie{c}
		}
	}
	return resp
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

// ── the two rules that are invisible in a browser ────────────────────────────

// TestTheAuthenticatingOperationsAnswer303AndNever200.
//
// The status code is the thing that decides whether an iPhone stays on the
// network, and it is the thing a screenshot cannot show. `expect(successBadge)
// .toBeVisible()` passes identically for the build that disassociates every
// device, which is why `web/e2e` carries `navigation-not-xhr` and why this exists
// on the server side of the same seam.
//
// Every outcome, not only success: the CNA re-checks captivity after a
// navigation, so a failure that answered `200` with a rendered form would leave a
// guest who mistyped a code stuck behind a Cancel button.
func TestTheAuthenticatingOperationsAnswer303AndNever200(t *testing.T) {
	cases := []struct {
		name string
		path string
		form url.Values
		page string
	}{
		{"a good voucher", "/portal/voucher", url.Values{"code": {goodCode}, "lang": {"en"}}, "/en/connected/"},
		{"a bad voucher", "/portal/voucher", url.Values{"code": {"ZZZZZZZZZZZZ"}, "lang": {"en"}}, "/en/voucher/?"},
		{"a malformed voucher", "/portal/voucher", url.Values{"code": {"nope"}, "lang": {"vi"}}, "/vi/voucher/?"},
		{"click-through", "/portal/agree", url.Values{"lang": {"en"}}, "/en/connected/"},
		{"a good account", "/portal/account", url.Values{"username": {guestUser}, "password": {guestPass}, "lang": {"en"}}, "/en/connected/"},
		{"a bad account", "/portal/account", url.Values{"username": {guestUser}, "password": {"wrong"}, "lang": {"en"}}, "/en/account/?"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := presentRig(t)
			g.arrive(guestMAC).Body.Close()

			resp := g.send(http.MethodPost, tc.path, tc.form)
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusSeeOther {
				t.Fatalf("status is %d, want 303 — the CNA re-checks captivity only after a document "+
					"navigation, and Cancel disassociates the device", resp.StatusCode)
			}
			loc := resp.Header.Get("Location")
			if !strings.HasPrefix(loc, tc.page) {
				t.Errorf("Location is %q, want a document under %q", loc, tc.page)
			}
			if got := resp.Header.Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control is %q — the contract requires no-store on this response by name", got)
			}
			// A `303` carries no body. Anything here is something the guest was
			// meant to read, arriving at a moment they may already be gone.
			if b := body(t, resp); b != "" {
				t.Errorf("the redirect carried %d bytes of body: %q", len(b), b)
			}
		})
	}
}

// TestPortalRedirectLocationMatchesTheContractsPattern.
//
// The test above asserts the `Location` starts with a path this file spells out,
// which pins the server against an accidental edit and says nothing about
// whether the document says the same thing. A `PortalRedirect.Location` of
// `type: string` with no constraint would let `web/mock`'s absolute URL at
// `PORTAL_ORIGIN` and this server's root-relative path **both conform**, and the
// differential harness could only file the disagreement as a contract gap.
//
// The rule is a `pattern` in the contract and this reads it out of the
// embedded document rather than restating it. So the shape has one authority and
// this test moves with it — including if somebody widens it, which is what the
// non-vacuity check at the end is for: a pattern of `.*` would make every
// assertion below pass, and nothing else in this package would notice.
//
// Why the shape is the one it is: a captive portal is reached over plain HTTP on
// an SSID the guest has no route off, so an absolute `Location` does not carry a
// scheme and a host — it picks them, on the screen where the guest has just typed
// a credential, and a wrong one is a redirect out of the walled garden.
func TestPortalRedirectLocationMatchesTheContractsPattern(t *testing.T) {
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}
	resp := spec.Components.Responses["PortalRedirect"]
	if resp == nil || resp.Value == nil {
		t.Fatal("the contract declares no `PortalRedirect` response")
	}
	header := resp.Value.Headers["Location"]
	if header == nil || header.Value == nil || header.Value.Schema == nil || header.Value.Schema.Value == nil {
		t.Fatal("`PortalRedirect` declares no schema for `Location` — nothing here compares anything")
	}
	pattern := header.Value.Schema.Value.Pattern
	if pattern == "" {
		t.Fatal("`PortalRedirect.headers.Location` has no `pattern`, so an absolute URL " +
			"conforms again")
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("the contract's pattern %q does not compile: %v", pattern, err)
	}

	// Every outcome that produces a `303`, success and failure, because the failure
	// path is the one that builds the header out of a client-supplied language and
	// a reason code.
	cases := []struct {
		name string
		path string
		form url.Values
	}{
		{"a good voucher", "/portal/voucher", url.Values{"code": {goodCode}, "lang": {"en"}}},
		{"a bad voucher", "/portal/voucher", url.Values{"code": {"ZZZZZZZZZZZZ"}, "lang": {"en"}}},
		{"click-through", "/portal/agree", url.Values{"lang": {"vi"}}},
		{"a good account", "/portal/account", url.Values{"username": {guestUser}, "password": {guestPass}, "lang": {"vi"}}},
		{"a bad account", "/portal/account", url.Values{"username": {guestUser}, "password": {"wrong"}, "lang": {"en"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := presentRig(t)
			g.arrive(guestMAC).Body.Close()

			answer := g.send(http.MethodPost, tc.path, tc.form)
			defer answer.Body.Close()

			loc := answer.Header.Get("Location")
			if loc == "" {
				t.Fatalf("no Location at all on a %d", answer.StatusCode)
			}
			if !re.MatchString(loc) {
				t.Errorf("Location is %q, which the contract's pattern %q refuses — an "+
					"absolute or protocol-relative value picks a scheme and a host for a "+
					"guest who has no route off this SSID", loc, pattern)
			}
		})
	}

	// **And the pattern can refuse.** Without this the loop above is satisfied by a
	// contract that constrains nothing: a mechanism that looks like a check. The
	// first value is a dev-server origin, the likeliest wrong value to leak.
	for _, bad := range []string{
		"http://localhost:5171/en/connected/",
		"//evil.example/en/connected/",
		`/\evil.example/en/connected/`,
		"/en/connected/\r\nSet-Cookie: a=b",
		"",
	} {
		if re.MatchString(bad) {
			t.Errorf("the contract's pattern %q accepts %q", pattern, bad)
		}
	}
}

// TestTermsAreDeliveredBeforeAuthorization.
//
// The other named frontend lock, asserted on the server that has to make it
// possible. Android destroys the login WebView the instant the network validates,
// so anything the guest must see has to be on the wire before the grant call.
//
// Three things together are the claim, and none of them is enough alone:
//
//  1. the terms are carried by `getPortalContext`, which is a **read** — it
//     creates no grant and asks no driver to authorize anybody;
//  2. the authorizing responses carry **no body at all**, so there is nothing
//     they could be carrying that a guest needs;
//  3. the driver is asked to authorize **after** the response is written, which
//     is asserted by observing the response before the driver has been called.
func TestTermsAreDeliveredBeforeAuthorization(t *testing.T) {
	g := presentRig(t)

	// 1 — the terms arrive on a read, and nothing is granted by reading them.
	resp := g.arrive(guestMAC)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("getPortalContext: %d %s", resp.StatusCode, body(t, resp))
	}
	var ctx gen.PortalContext
	if err := json.Unmarshal([]byte(body(t, resp)), &ctx); err != nil {
		t.Fatalf("PortalContext: %v", err)
	}
	if ctx.Terms.Summary == nil || (*ctx.Terms.Summary)["en"] == "" {
		t.Fatal("the terms are not on the document the guest reads before authenticating")
	}
	if len(*ctx.Terms.Summary) != 2 {
		t.Errorf("the venue supplied two languages and %d arrived — terms are legal text and the "+
			"portal may never silently substitute one language for another", len(*ctx.Terms.Summary))
	}
	if len(g.store.PortalStore.Grants()) != 0 {
		t.Error("reading the terms created a grant")
	}
	if g.driver.AuthorizeCount() != 0 {
		t.Error("reading the terms authorized somebody on the network")
	}

	// 2 and 3 — the grant call answers with nothing, and the driver is asked
	// afterwards. `http.Client.Do` returns once the status line and headers are
	// on the wire, and the handler flushes before calling the driver, so an
	// authorization observed before this point would mean the ordering is wrong.
	post := g.send(http.MethodPost, "/portal/agree", url.Values{"lang": {"en"}})
	if post.StatusCode != http.StatusSeeOther {
		t.Fatalf("acceptTerms: %d", post.StatusCode)
	}
	if b := body(t, post); b != "" {
		t.Errorf("the authorizing response carried a body: %q", b)
	}
}

// TestTheDriverIsAskedAfterTheDocumentIsWritten.
//
// The ordering rule, on its own, with the observation the other test cannot make:
// a `ResponseWriter` that records **when** the header was written, beside a driver
// that records when it was called.
//
// It is worth having separately because it is the half that a refactor breaks
// silently. A handler that called the driver first and then returned the redirect
// would pass every other assertion in this file.
func TestTheDriverIsAskedAfterTheDocumentIsWritten(t *testing.T) {
	g := presentRig(t)
	g.arrive(guestMAC).Body.Close()

	// The driver blocks until it is released, so if the handler called it before
	// writing the response, the response would not arrive.
	release := make(chan struct{})
	called := make(chan struct{})
	g.driver.WithHooks(func(h *nas.MockHooks) {
		h.OnAuthorize = func() {
			close(called)
			<-release
		}
	})

	done := make(chan int, 1)
	go func() {
		resp := g.send(http.MethodPost, "/portal/agree", url.Values{"lang": {"en"}})
		done <- resp.StatusCode
		resp.Body.Close()
	}()

	// The failure has to be *reported*, not fataled: the driver call is blocked
	// on `release`, so a `t.Fatal` here would leave the request in flight and the
	// server's cleanup would wait for it until the whole suite's timeout. A lock
	// that deadlocks instead of failing is a lock nobody can read the output of.
	early := false
	select {
	case status := <-done:
		// The redirect arrived while the driver call is still blocked, which is
		// the ordering the contract requires.
		if status != http.StatusSeeOther {
			t.Errorf("status is %d, want 303", status)
		}
	case <-time.After(2 * time.Second):
		early = true
	}

	close(release)
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Error("the driver was never asked to authorize — the guest has a grant and no network")
	}
	if early {
		<-done
		t.Error("the response did not arrive while the driver call was in flight — " +
			"the NAS is being asked to authorize before the document is written, which is the " +
			"case Android's WebView teardown makes invisible")
	}
}

// ── the named security test, through the routes ──────────────────────────────

// TestVerifyPresence_NoGrantForAMACTheDriverDidNotConfirm_OverHTTP.
//
// The service-level lock in `internal/portal` proves the rule. This proves the
// rule is wired to the route, which is a different statement: a handler that
// resolved a session and never called the service would pass the first and fail
// this.
//
// `nas.NewMockDriver` in place of `nas.NewStrictMockDriver` — a driver whose
// `DefaultPresent` is true, which is exactly a fake answering "present"
// unconditionally — turns every case here red on the grant count and on the
// redirect.
func TestVerifyPresence_NoGrantForAMACTheDriverDidNotConfirm_OverHTTP(t *testing.T) {
	for _, tc := range []struct {
		name string
		path string
		form url.Values
	}{
		{"redeemVoucher", "/portal/voucher", url.Values{"code": {goodCode}, "lang": {"en"}}},
		{"acceptTerms", "/portal/agree", url.Values{"lang": {"en"}}},
		{"loginWithAccount", "/portal/account", url.Values{"username": {guestUser}, "password": {guestPass}, "lang": {"en"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A driver that was told about one MAC, approached by another.
			d := newGuestDriver()
			d.Presences[guestMAC] = true
			g := newGuestRig(t, d)
			g.arrive(strangerMAC).Body.Close()

			resp := g.send(http.MethodPost, tc.path, tc.form)
			defer resp.Body.Close()

			if loc := resp.Header.Get("Location"); strings.Contains(loc, "connected") {
				t.Errorf("a MAC the driver never confirmed was sent to %q", loc)
			}
			if got := g.store.PortalStore.Grants(); len(got) != 0 {
				t.Errorf("%d grant(s) exist for a MAC the driver did not confirm: %+v", len(got), got)
			}
			if n := g.driver.AuthorizeCount(); n != 0 {
				t.Errorf("the driver was asked to authorize %d device(s)", n)
			}
			// And the voucher is still there to be sold to somebody who is.
			for _, v := range g.store.World().Vouchers {
				if v.State != gen.VoucherStateUnused {
					t.Errorf("voucher %s is %q after a refused redemption", v.Id, v.State)
				}
			}
		})
	}
}

// TestAForgedRedirectGetsNoContext.
//
// UniFi passes `ap`, `id`, `t`, `url` and `ssid` as unsigned query parameters, so
// this request is the whole attack: a stranger calling the portal with somebody
// else's MAC, or with one that never associated. The refusal carries no `detail`
// and does not say which check failed — an answer that distinguished them would
// be an oracle for what the portal believes about a MAC anybody can name.
func TestAForgedRedirectGetsNoContext(t *testing.T) {
	g := newGuestRig(t, newGuestDriver())

	resp := g.arrive(strangerMAC)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status is %d, want 403", resp.StatusCode)
	}
	var p gen.Problem
	if err := json.Unmarshal([]byte(body(t, resp)), &p); err != nil {
		t.Fatalf("not a problem document: %v", err)
	}
	if p.Type != gen.ProblemsdeviceNotVerified {
		t.Errorf("type is %q", p.Type)
	}
	if p.Detail != nil {
		t.Errorf("the refusal carries a sentence: %q", *p.Detail)
	}
	if strings.Contains(body(t, resp), strangerMAC) {
		t.Error("the refusal echoed the MAC it was given")
	}
}

// TestAnUnreachableControllerIsNotTheGuestsFault.
//
// The venue's outage must not be rendered as a statement about the guest's phone.
// `getPortalContext` answers the venue's page — the contract's `403` enumerates
// not-associated, wrong AP or site, and already-authorized, and an outage is in
// none of them — and the grant path still fails closed, telling the guest
// `generic` rather than `not-present`.
func TestAnUnreachableControllerIsNotTheGuestsFault(t *testing.T) {
	// The MAC is seeded and then the controller goes away, which is the order it
	// happens in: an outage is not a statement about whether the device is there.
	// `WithControllerUnreachable` answers `PresenceUnavailable` **and** a non-nil
	// error, and a caller reading only one of them classifies a venue outage as a
	// refusal — which is how the three cases collapsed into one sentence.
	d := newGuestDriver()
	d.Presences[guestMAC] = true
	d.WithControllerUnreachable(errControllerDown)
	g := newGuestRig(t, d)

	resp := g.arrive(guestMAC)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("getPortalContext answered %d during an outage — a guest cannot act on that, "+
			"and it is a statement about their device that is not true", resp.StatusCode)
	}
	resp.Body.Close()

	post := g.send(http.MethodPost, "/portal/agree", url.Values{"lang": {"en"}})
	defer post.Body.Close()
	if post.StatusCode != http.StatusSeeOther {
		t.Fatalf("acceptTerms: %d", post.StatusCode)
	}
	loc := post.Header.Get("Location")
	if strings.Contains(loc, "connected") {
		t.Fatal("an unreachable controller let somebody onto the network")
	}
	if !strings.Contains(loc, "e=generic") {
		t.Errorf("Location is %q — an outage reported as not-present sends a guest to toggle "+
			"Wi-Fi for twenty minutes instead of to the counter", loc)
	}
	if len(g.store.PortalStore.Grants()) != 0 {
		t.Error("a grant exists off an outage")
	}
}

// ── the whole flow ───────────────────────────────────────────────────────────

func TestAGuestRedeemsAVoucherAndTheConnectedPageCanBeRendered(t *testing.T) {
	g := presentRig(t)
	g.arrive(guestMAC).Body.Close()

	// Nothing to report before the guest has done anything.
	before := g.send(http.MethodGet, "/portal/status", nil)
	if before.StatusCode != http.StatusNotFound {
		t.Errorf("getPortalStatus is %d before any grant, want 404", before.StatusCode)
	}
	var p gen.Problem
	_ = json.Unmarshal([]byte(body(t, before)), &p)
	if p.Type != gen.ProblemsnoGrant {
		t.Errorf("type is %q, want /problems/no-grant", p.Type)
	}

	redeem := g.send(http.MethodPost, "/portal/voucher", url.Values{
		// With the dashes a guest would actually type off a receipt.
		"code": {"ABCD-EFGH-JKMN"}, "lang": {"en"},
	})
	if redeem.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeemVoucher: %d", redeem.StatusCode)
	}
	redeem.Body.Close()

	status := g.send(http.MethodGet, "/portal/status", nil)
	defer status.Body.Close()
	if status.StatusCode != http.StatusOK {
		t.Fatalf("getPortalStatus: %d %s", status.StatusCode, body(t, status))
	}
	var s gen.PortalStatus
	if err := json.Unmarshal([]byte(body(t, status)), &s); err != nil {
		t.Fatalf("PortalStatus: %v", err)
	}
	if s.State != gen.PortalStatusStateConnected {
		t.Errorf("state is %q", s.State)
	}
	if s.Plan.DurationMinutes == nil || *s.Plan.DurationMinutes != 60 {
		t.Errorf("the plan did not come from the batch: %+v", s.Plan)
	}
	// The driver reports `HardQuota`, so the access point is what enforces the
	// limit and the figure is authoritative. A console or a portal may present it
	// as a cap only because of this value.
	if s.Plan.EnforcedBy != gen.PlanEnforcedByNas {
		t.Errorf("enforcedBy is %q behind a driver with hard quota, want %q",
			s.Plan.EnforcedBy, gen.PlanEnforcedByNas)
	}
	if s.VenueName == nil || *s.VenueName != "The Venue" {
		t.Error("the connected page would welcome the guest to a different business than the one " +
			"whose terms they accepted")
	}
	if s.OriginalUrl == nil || *s.OriginalUrl != "http://example.test/news" {
		t.Errorf("originalUrl is %v", s.OriginalUrl)
	}

	// The voucher is spent, and a second attempt says so rather than granting.
	again := g.send(http.MethodPost, "/portal/voucher", url.Values{"code": {goodCode}, "lang": {"en"}})
	defer again.Body.Close()
	if loc := again.Header.Get("Location"); !strings.Contains(loc, "e=already-used") {
		t.Errorf("a second redemption went to %q", loc)
	}
	if n := len(g.store.PortalStore.Grants()); n != 1 {
		t.Errorf("%d grants exist after one redemption and one refusal", n)
	}
}

// TestARevokedGrantIsAnsweredRatherThanHidden.
//
// `404` means no grant answers to this browser, and nothing else. Folding "the
// grant ended" into it is how `PortalStatus.state` ends up with two members
// nothing can produce — and how a guest whose voucher was revoked while they were
// reading gets shown a green tick, because the page reads the plan and never the
// state.
func TestARevokedGrantIsAnsweredRatherThanHidden(t *testing.T) {
	g := presentRig(t)
	g.arrive(guestMAC).Body.Close()
	g.send(http.MethodPost, "/portal/agree", url.Values{"lang": {"en"}}).Body.Close()

	grants := g.store.PortalStore.Grants()
	if len(grants) != 1 {
		t.Fatalf("%d grants", len(grants))
	}
	g.store.PortalStore.Revoke(grants[0].ID, at(0))

	resp := g.send(http.MethodGet, "/portal/status", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a revoked grant answered %d — the guest is owed the fact", resp.StatusCode)
	}
	var s gen.PortalStatus
	_ = json.Unmarshal([]byte(body(t, resp)), &s)
	if s.State != gen.PortalStatusStateRevoked {
		t.Errorf("state is %q, want %q", s.State, gen.PortalStatusStateRevoked)
	}
}

// TestClickThroughIsRefusedWhereItIsNotOffered.
//
// `404`, not a grant. An endpoint that works while its button is hidden is an
// endpoint somebody finds, and this is the one method with no credential in front
// of it.
func TestClickThroughIsRefusedWhereItIsNotOffered(t *testing.T) {
	g := presentRig(t)
	set := g.store.World().Portal[portalSite]
	set.Methods = []gen.PortalMethod{gen.PortalMethodVoucher}
	g.store.World().Portal[portalSite] = set

	g.arrive(guestMAC).Body.Close()
	resp := g.send(http.MethodPost, "/portal/agree", url.Values{"lang": {"en"}})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status is %d, want 404", resp.StatusCode)
	}
	var p gen.Problem
	_ = json.Unmarshal([]byte(body(t, resp)), &p)
	if p.Type != gen.ProblemsmethodNotOffered {
		t.Errorf("type is %q", p.Type)
	}
	if len(g.store.PortalStore.Grants()) != 0 {
		t.Error("a grant exists for a method the venue does not offer")
	}
}

// ── the open redirect ────────────────────────────────────────────────────────

// TestTheOriginalURLIsAllowlistedOnTheWayOut.
//
// The NAS supplies it, unsigned. The design is explicit about why this one
// matters more than it looks: "the victim is on an untrusted network, *expects*
// to be redirected, and has just typed credentials."
//
// The payloads go in through the same channel a real one would — the redirect
// parameters — and the assertion is that `PortalStatus.originalUrl` is **absent**
// rather than sanitised. A link to nowhere is a mild inconvenience; a link
// somewhere else is a phish the venue vouched for.
func TestTheOriginalURLIsAllowlistedOnTheWayOut(t *testing.T) {
	for _, payload := range []string{
		"javascript:alert(1)",
		"data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==",
		"//evil.example/",
		"http://portal.example.test@evil.example/",
		"https://portal.example.test:pw@evil.example/login",
		"file:///etc/passwd",
		"intent://evil.example#Intent;scheme=http;end",
		"http://evil.example/\r\nSet-Cookie: a=b",
	} {
		g := presentRig(t)
		q := url.Values{"ap": {apMAC}, "id": {guestMAC}, "url": {payload}}
		g.send(http.MethodGet, "/portal/context?"+q.Encode(), nil).Body.Close()
		g.send(http.MethodPost, "/portal/agree", url.Values{"lang": {"en"}}).Body.Close()

		resp := g.send(http.MethodGet, "/portal/status", nil)
		raw := body(t, resp)
		resp.Body.Close()

		var s gen.PortalStatus
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			t.Fatalf("PortalStatus: %v", err)
		}
		if s.OriginalUrl != nil {
			t.Errorf("payload %q came back as a link: %q", payload, *s.OriginalUrl)
		}
		if strings.Contains(raw, "evil.example") || strings.Contains(raw, "javascript:") {
			t.Errorf("payload %q reached the response body: %s", payload, raw)
		}
	}
}

// ── the two surfaces ─────────────────────────────────────────────────────────

// TestTheConsoleAndTheGuestPortalDoNotShareRoutes.
//
// The design puts them on separate listeners, and this is what that means once
// one contract covers both. The direction that matters is the second: an operation
// requiring `nas:manage` reachable on the listener bound to `0.0.0.0` would be
// the console open to everyone within radio range.
func TestTheConsoleAndTheGuestPortalDoNotShareRoutes(t *testing.T) {
	store := memory.NewStore(portalWorld(t))
	store.SetClock(func() time.Time { return at(0) })
	srv, err := api.New(api.Config{
		Ports: store.Ports(), Portal: store.PortalPorts(newGuestDriver()),
		Now: func() time.Time { return at(0) },
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}

	admin := httptest.NewServer(srv.Handler())
	defer admin.Close()
	guest := httptest.NewServer(srv.PortalHandler())
	defer guest.Close()

	// A guest operation is not on the console's listener.
	resp, err := admin.Client().Get(admin.URL + api.BasePath + "/portal/context")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("the console answered %d for a guest operation — the guest portal is a separate "+
			"listener, and on this one there is no such route", resp.StatusCode)
	}
	resp.Body.Close()

	// And a console operation is not on the listener anybody within radio range
	// can reach. `404`, not `401`: `401` would say the route exists.
	resp, err = guest.Client().Get(guest.URL + api.BasePath + "/nas?siteId=" + portalSite)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("the guest listener answered %d for listNas", resp.StatusCode)
	}
}

// TestAHalfWiredPortalIsRefusedAtConstruction.
//
// Not `501` per operation. The portal's ports are not independent: a grant store
// with no driver seam is a portal that records grants for devices nothing
// confirmed, and there is no useful subset of this seam.
func TestAHalfWiredPortalIsRefusedAtConstruction(t *testing.T) {
	store := memory.NewStore(portalWorld(t))
	full := store.PortalPorts(newGuestDriver())
	full.Drivers = nil

	if _, err := api.New(api.Config{Ports: store.Ports(), Portal: full}); err == nil {
		t.Fatal("a server was built with a grant store and no driver")
	}
}

// TestAnAlreadyAuthorizedGuestIsRefusedByContextAndConnectedByTheRest.
//
// The asymmetry is deliberate and it is worth pinning, because it reads as a bug
// until you know why.
//
// The contract enumerates `getPortalContext`'s `403` as "not associated, is on a
// different AP or site, **or is already authorized**" — one answer for all three,
// because an answer that distinguished them would be an oracle for what the portal
// believes about a MAC anybody can name. So a guest who is already online and opens
// the portal gets the same problem document as a forged redirect. That is the
// contract's decision, and this server follows it rather than diverging.
//
// The authenticating operations are not bound by it: they answer `303`, so they can
// send that guest to the connected page — and, the part that costs money if it is
// wrong, **without claiming a voucher.**
func TestAnAlreadyAuthorizedGuestIsRefusedByContextAndConnectedByTheRest(t *testing.T) {
	// A client the controller holds and has already let in. `WithClient` is the
	// richer answer the bool truncates: `Authorized: true` is what produces
	// `PresenceAlreadyAuthorized` on the shipped driver too, rather than a value
	// this test asserted into place.
	d := newGuestDriver().WithClient(guestMAC, nas.MockClient{ID: "c-1", Authorized: true})
	g := newGuestRig(t, d)

	resp := g.arrive(guestMAC)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("getPortalContext is %d for an already-authorized guest, want 403 — "+
			"the contract enumerates that case under its 403", resp.StatusCode)
	}
	resp.Body.Close()

	redeem := g.send(http.MethodPost, "/portal/voucher", url.Values{"code": {goodCode}, "lang": {"en"}})
	defer redeem.Body.Close()
	if loc := redeem.Header.Get("Location"); loc != "/en/connected/" {
		t.Errorf("Location is %q — a guest who is already online should reach the connected page", loc)
	}
	for _, v := range g.store.World().Vouchers {
		if v.State != gen.VoucherStateUnused {
			t.Errorf("voucher %s is %q — a single-use code was burned for a guest who was already "+
				"online, which ends with staff issuing a second one", v.Id, v.State)
		}
	}
	if n := len(g.store.PortalStore.Grants()); n != 0 {
		t.Errorf("%d grant(s) created for a device the driver reports as already authorized", n)
	}
}
