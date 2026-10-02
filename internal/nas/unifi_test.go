package nas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// The fixtures under testdata/ are the response *shapes* Ubiquiti's own worked
// example documents (Help Center article 31228198640023). They are NOT captured
// from a controller — `foyerctl probe-unifi` does that, with a human at the
// keyboard. Where a fixture is not a captured answer, every assertion here proves
// the driver against a document, which is weaker than a
// measurement and is exactly why `foyerctl probe-unifi` exists.
//
// docs/rules/testing.md: a hand-written NAS mock encodes the author's
// assumptions. So this one is built to *refuse*: it 404s any path the driver
// gets wrong, 403s cmd/stamgr without the CSRF token, and answers the client
// query with a MAC the caller did not ask for in one case, because a fixture
// that only ever says yes proves nothing about a verifier.

const (
	testMAC       = "1c:71:25:63:e4:24"
	testClientID  = "d8b3f2a0-1c71-4e2b-9f10-2563e4241c71"
	testAPIKey    = "unit-test-integration-key-4f2b"
	testCSRFToken = "csrf-9d41c0ba"
	// testVendorSiteID is the id in `unifi-sites.json`, which is the id the
	// shape a real controller reports for its one site. It is not the
	// string an operator types.
	testVendorSiteID = "00000000-0000-4000-8000-000000000001"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(path.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Body   string
	CSRF   string
	APIKey string
}

// fixtureConfig is how one fixture controller differs from another.
type fixtureConfig struct {
	flavour     UniFiFlavour
	clientsFile string
	detailFile  string
	infoFile    string
	// issueCSRF is the token the login response hands out. Empty means the
	// login answered with no token, which is the shape that makes every
	// cmd/stamgr POST fail 403 on UniFi OS.
	issueCSRF string
	// actionStatus is what the action endpoint answers with.
	actionStatus int
	// legacyDevicesFile is the fixture served by the legacy device list.
	// Empty means the path 404s, which is a controller that has no such route.
	legacyDevicesFile string
	// deviceTrailingSlashOnly answers only /stat/device/ and 404s /stat/device,
	// which is how the candidate loop earns its existence: PacketFence walks the
	// trailing-slash form and most other clients do not.
	deviceTrailingSlashOnly bool

	// devicesFile is the fixture served by the *Integration API* device list —
	// the one `uplinkDeviceId` resolves against. Empty serves the two-device
	// default; the sentinel devicesAbsent makes the route 404, which is a
	// controller this driver cannot build an access-point map from.
	devicesFile string
	// devicesStatus, when non-zero, is the status the device list answers with
	// instead of 200. A controller that is up and refusing this one route is not
	// the same as one nobody can reach, and the driver has to tell them apart.
	devicesStatus int
	// devicePageCap forces the device list to serve at most this many rows per
	// page whatever `limit` asked for, which is the **measured** behaviour: on
	// Network 10.5.67 `?limit=201` answers 200 with `"limit":200` in the body.
	// The clamp is silent, so a driver that asked for more and trusted the
	// answer would read a truncated list as a complete one.
	devicePageCap int
}

// devicesAbsent makes the fixture 404 the Integration API device list.
const devicesAbsent = "-"

// fixtureController is a UniFi controller of a stated flavour.
type fixtureController struct {
	fixtureConfig

	mu       sync.Mutex
	requests []recordedRequest

	server *httptest.Server
}

func newFixtureController(t *testing.T, cfg fixtureConfig) *fixtureController {
	t.Helper()
	fc := &fixtureController{fixtureConfig: cfg}
	if fc.clientsFile == "" {
		fc.clientsFile = "unifi-clients-unauthorized.json"
	}
	if fc.detailFile == "" {
		fc.detailFile = "unifi-client-detail.json"
	}
	if fc.infoFile == "" {
		fc.infoFile = "unifi-info.json"
	}
	if fc.actionStatus == 0 {
		fc.actionStatus = http.StatusOK
	}
	fc.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fc.serve(t, w, r)
	}))
	t.Cleanup(fc.server.Close)
	return fc
}

func (fc *fixtureController) integrationPrefix() string {
	if fc.flavour == FlavourUniFiOS {
		return unifiOSIntegrationPrefix
	}
	return standaloneIntegrationPrefix
}

func (fc *fixtureController) networkPrefix() string {
	if fc.flavour == FlavourUniFiOS {
		return unifiOSNetworkPrefix
	}
	return ""
}

func (fc *fixtureController) serve(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	fc.mu.Lock()
	fc.requests = append(fc.requests, recordedRequest{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
		Body: string(body), CSRF: r.Header.Get("X-CSRF-Token"),
		APIKey: r.Header.Get("X-API-KEY"),
	})
	fc.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	p := r.URL.Path
	ip := fc.integrationPrefix()

	switch {
	case p == "/api/system":
		_, _ = w.Write([]byte(`{"name":"UniFi OS Server","version":"5.1.37"}`))
		return

	case p == unifiOSStatusPath:
		if fc.flavour == FlavourUniFiOS {
			// 401 is the positive signal: the reverse proxy answered.
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"meta":{"rc":"error","msg":"api.err.LoginRequired"}}`))
			return
		}
		http.NotFound(w, r)
		return

	case p == ip+"/v1/sites":
		_, _ = w.Write(readFixture(t, "unifi-sites.json"))
		return

	case p == ip+"/v1/info":
		_, _ = w.Write(readFixture(t, fc.infoFile))
		return

	case p == ip+"/v1/sites/default/clients":
		_, _ = w.Write(readFixture(t, fc.clientsFile))
		return

	// The device list answers at the legacy site segment *and* at the site id
	// `unifi-sites.json` names. The real controller accepts only the id —
	// `/v1/sites/default/…` answers `400 'default' is not a valid 'siteId'
	// value`, which is the two-identifier-space trap: an access-point field scan
	// over that error body draws a confident negative from it. Both segments are
	// served here because every other route in this fixture uses the legacy
	// one.
	case p == ip+"/v1/sites/default/devices",
		p == ip+"/v1/sites/"+testVendorSiteID+"/devices":
		fc.serveDevices(t, w, r)
		return

	case p == ip+"/v1/sites/default/clients/"+testClientID:
		_, _ = w.Write(readFixture(t, fc.detailFile))
		return

	case p == ip+"/v1/sites/default/clients/"+testClientID+"/actions":
		w.WriteHeader(fc.actionStatus)
		_, _ = w.Write([]byte(`{"id":"` + testClientID + `"}`))
		return

	case p == unifiOSLoginPath && fc.flavour == FlavourUniFiOS,
		p == standaloneLoginPath && fc.flavour == FlavourStandalone:
		http.SetCookie(w, &http.Cookie{Name: "TOKEN", Value: "session-cookie"})
		if fc.issueCSRF != "" {
			w.Header().Set("X-CSRF-Token", fc.issueCSRF)
		}
		_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[]}`))
		return

	case p == fc.networkPrefix()+"/api/s/default/stat/device",
		p == fc.networkPrefix()+"/api/s/default/stat/device/":
		if fc.legacyDevicesFile == "" {
			http.NotFound(w, r)
			return
		}
		if fc.deviceTrailingSlashOnly && !strings.HasSuffix(p, "/") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(readFixture(t, fc.legacyDevicesFile))
		return

	case p == fc.networkPrefix()+"/api/s/default/cmd/stamgr":
		// The documented UniFi OS failure: 403, silently, on a missing or wrong
		// token. An absent token is a 403 even when the login response carried
		// none to echo — that is the whole shape of the bug, and a fixture that
		// accepted "" would be green against it.
		if got := r.Header.Get("X-CSRF-Token"); fc.flavour == FlavourUniFiOS && (got == "" || got != fc.issueCSRF) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"meta":{"rc":"error","msg":"api.err.NoPermission"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[]}`))
		return
	}

	http.NotFound(w, r)
}

// serveDevices answers the Integration API device list the way a real
// controller (Network 10.5.67) answers it: an envelope carrying `offset`,
// `limit`, `count` and `totalCount`, with `limit` silently clamped rather than
// refused.
//
// It pages for real rather than returning the whole fixture, because a fixture
// that ignores `offset` is a world in which a truncated read and a complete one
// are the same bytes — and the driver's pagination would then be a branch no
// test can reach. A fixture world that cannot express a property makes every
// check of it vacuous (`docs/rules/testing.md`, Principles).
func (fc *fixtureController) serveDevices(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	if fc.devicesFile == devicesAbsent {
		http.NotFound(w, r)
		return
	}
	if fc.devicesStatus != 0 {
		w.WriteHeader(fc.devicesStatus)
		_, _ = w.Write([]byte(`{"error":{"code":` + strconv.Itoa(fc.devicesStatus) + `}}`))
		return
	}
	name := fc.devicesFile
	if name == "" {
		name = "unifi-devices.json"
	}
	var page struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(readFixture(t, name), &page); err != nil {
		t.Fatalf("device fixture %s: %v", name, err)
	}

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 25
	}
	if cap := fc.devicePageCap; cap > 0 && limit > cap {
		limit = cap // silent, exactly as measured
	}
	if offset < 0 || offset > len(page.Data) {
		offset = len(page.Data)
	}
	end := offset + limit
	if end > len(page.Data) {
		end = len(page.Data)
	}
	rows := page.Data[offset:end]
	out, err := json.Marshal(map[string]any{
		"offset": offset, "limit": limit,
		"count": len(rows), "totalCount": len(page.Data),
		"data": rows,
	})
	if err != nil {
		t.Fatalf("encoding device page: %v", err)
	}
	_, _ = w.Write(out)
}

func (fc *fixtureController) seen() []recordedRequest {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	out := make([]recordedRequest, len(fc.requests))
	copy(out, fc.requests)
	return out
}

func (fc *fixtureController) countMatching(method, substr string) int {
	n := 0
	for _, r := range fc.seen() {
		if r.Method == method && strings.Contains(r.Path, substr) {
			n++
		}
	}
	return n
}

func (fc *fixtureController) driver(t *testing.T, mutate ...func(*UniFiConfig)) *UniFiDriver {
	t.Helper()
	cfg := UniFiConfig{
		BaseURL: fc.server.URL,
		APIKey:  testAPIKey,
		Site:    "default",
		Timeout: 5 * time.Second,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	d, err := NewUniFiDriverWithError(cfg)
	if err != nil {
		t.Fatalf("NewUniFiDriverWithError: %v", err)
	}
	return d
}

// ---------------------------------------------------------------------------

// TestUniFiURLShapePerFlavour is the regression for a driver that built
// {baseURL}/v1/sites/… and therefore could not reach a
// UniFi OS controller at all. The fixture 404s anything else, so a wrong prefix
// is a failure and not a silent difference.
func TestUniFiURLShapePerFlavour(t *testing.T) {
	cases := []struct {
		name          string
		flavour       UniFiFlavour
		wantLookup    string
		wantAction    string
		wantStatusHit bool
	}{
		{
			name:          "unifi os",
			flavour:       FlavourUniFiOS,
			wantLookup:    "/proxy/network/integration/v1/sites/default/clients",
			wantAction:    "/proxy/network/integration/v1/sites/default/clients/" + testClientID + "/actions",
			wantStatusHit: true,
		},
		{
			name:          "standalone",
			flavour:       FlavourStandalone,
			wantLookup:    "/integration/v1/sites/default/clients",
			wantAction:    "/integration/v1/sites/default/clients/" + testClientID + "/actions",
			wantStatusHit: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFixtureController(t, fixtureConfig{flavour: tc.flavour})
			d := fc.driver(t)

			ok, err := d.VerifyPresence(context.Background(), testMAC, "", "default")
			if err != nil || !ok {
				t.Fatalf("VerifyPresence = %v, %v; want true, nil", ok, err)
			}
			if err := d.AuthorizeGuest(context.Background(), AuthorizeRequest{
				ClientMAC: testMAC, TimeLimitMinutes: 60, DataUsageLimitMB: 500,
				RxRateLimitKbps: 5000, TxRateLimitKbps: 2000,
			}); err != nil {
				t.Fatalf("AuthorizeGuest: %v", err)
			}

			var sawStatus, sawLookup, sawAction bool
			var actionBody string
			for _, r := range fc.seen() {
				switch {
				case r.Path == unifiOSStatusPath:
					sawStatus = true
				case r.Path == tc.wantLookup && r.Method == http.MethodGet:
					sawLookup = true
					if !strings.Contains(r.Query, "macAddress.eq") {
						t.Errorf("lookup query = %q, want a macAddress.eq filter", r.Query)
					}
				case r.Path == tc.wantAction && r.Method == http.MethodPost:
					sawAction = true
					actionBody = r.Body
				}
			}
			if tc.wantStatusHit && !sawStatus {
				t.Errorf("the flavour probe never ran; the driver assumed a shape instead of measuring it")
			}
			if !sawLookup {
				t.Errorf("no lookup at %s; saw %v", tc.wantLookup, pathsOf(fc.seen()))
			}
			if !sawAction {
				t.Errorf("no action at %s; saw %v", tc.wantAction, pathsOf(fc.seen()))
			}

			// The action path carries the controller's client id, never the MAC.
			for _, r := range fc.seen() {
				if strings.Contains(r.Path, "/actions") && strings.Contains(r.Path, testMAC) {
					t.Errorf("action path %s carries the MAC; the documented path takes the client id", r.Path)
				}
			}

			var payload map[string]any
			if err := json.Unmarshal([]byte(actionBody), &payload); err != nil {
				t.Fatalf("action body %q is not JSON: %v", actionBody, err)
			}
			for _, key := range []string{"action", "timeLimitMinutes", "dataUsageLimitMBytes", "rxRateLimitKbps", "txRateLimitKbps"} {
				if _, ok := payload[key]; !ok {
					t.Errorf("action body is missing %s: %v", key, payload)
				}
			}
			if payload["action"] != actionAuthorize {
				t.Errorf("action = %v, want %s", payload["action"], actionAuthorize)
			}
		})
	}
}

func pathsOf(rs []recordedRequest) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

// TestUniFiVerifyPresence covers both halves security.md §4 asks for: really
// associated, and still unauthorized. The already-authorized case is the one
// that makes this a verifier rather than a lookup.
func TestUniFiVerifyPresence(t *testing.T) {
	cases := []struct {
		name        string
		clientsFile string
		mac         string
		apMAC       string
		requireAP   bool
		wantOK      bool
		wantOutcome PresenceOutcome
		wantErr     bool
	}{
		{
			name:        "associated and unauthorized",
			clientsFile: "unifi-clients-unauthorized.json",
			mac:         testMAC,
			wantOK:      true,
			wantOutcome: PresenceVerified,
		},
		{
			name:        "the controller knows of no such device",
			clientsFile: "unifi-clients-empty.json",
			mac:         testMAC,
			wantOutcome: PresenceNotFound,
		},
		{
			name:        "already authorized is a refusal, not a pass",
			clientsFile: "unifi-clients-authorized.json",
			mac:         testMAC,
			wantOutcome: PresenceAlreadyAuthorized,
		},
		{
			name:        "the controller ignored the filter and answered about somebody else",
			clientsFile: "unifi-clients-wrong-mac.json",
			mac:         testMAC,
			wantOutcome: PresenceNotFound,
		},
		{
			name:        "a MAC in another notation is the same MAC",
			clientsFile: "unifi-clients-unauthorized.json",
			mac:         "1C-71-25-63-E4-24",
			wantOK:      true,
			wantOutcome: PresenceVerified,
		},
		{
			name:        "an unconfirmable access point is refused when the operator requires one",
			clientsFile: "unifi-clients-unauthorized.json",
			mac:         testMAC,
			apMAC:       "94:2a:6f:d0:30:57",
			requireAP:   true,
			wantOutcome: PresenceAPMismatch,
		},
		{
			name:        "not a MAC at all",
			clientsFile: "unifi-clients-unauthorized.json",
			mac:         "'; DROP TABLE radacct;--",
			wantOutcome: PresenceNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFixtureController(t, fixtureConfig{
				flavour:     FlavourUniFiOS,
				clientsFile: tc.clientsFile,
			})
			d := fc.driver(t, func(c *UniFiConfig) { c.RequireAPMatch = tc.requireAP })

			got, err := d.VerifyPresenceDetail(context.Background(), tc.mac, tc.apMAC, "default")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got.OK != tc.wantOK {
				t.Errorf("OK = %v, want %v (outcome %q)", got.OK, tc.wantOK, got.Outcome)
			}
			if got.Outcome != tc.wantOutcome {
				t.Errorf("outcome = %q, want %q", got.Outcome, tc.wantOutcome)
			}
			if got.OK && got.APConfirmed {
				t.Errorf("APConfirmed is true, but nothing in the Integration API client object " +
					"identifies the access point; claiming otherwise is the wide reading")
			}
		})
	}
}

// TestUniFiVerifyPresence_ControllerUnreachableIsNeverYes: an outage must not
// read as a refusal *or* as a pass — the caller has to be able to tell them
// apart, and neither one may authorize.
func TestUniFiVerifyPresence_ControllerUnreachableIsNeverYes(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{flavour: FlavourUniFiOS})
	d := fc.driver(t)
	fc.server.Close() // the controller goes away

	got, err := d.VerifyPresenceDetail(context.Background(), testMAC, "", "default")
	if err == nil {
		t.Fatalf("want an error when the controller is unreachable, got outcome %q", got.Outcome)
	}
	if got.OK {
		t.Fatalf("an unreachable controller verified a device")
	}
	if got.Outcome != PresenceUnavailable {
		t.Errorf("outcome = %q, want %q — an outage is not a refusal", got.Outcome, PresenceUnavailable)
	}
}

// TestVerifyPresence_NoGrantForAMACTheDriverDidNotConfirm is the named security
// test from docs/rules/testing.md, in its driver half.
//
// Two assertions, because one of them can be satisfied by a well-behaved caller
// and the other cannot: a caller that honours the contract issues no action, and
// a caller that *forgets* still cannot authorize, because the action endpoint
// needs a client id and only the controller can supply one.
func TestVerifyPresence_NoGrantForAMACTheDriverDidNotConfirm(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{
		flavour:     FlavourUniFiOS,
		clientsFile: "unifi-clients-empty.json",
	})
	d := fc.driver(t)
	ctx := context.Background()
	const neverAssociated = "de:ad:be:ef:00:01"

	ok, err := d.VerifyPresence(ctx, neverAssociated, "", "default")
	if err != nil {
		t.Fatalf("VerifyPresence: %v", err)
	}
	if ok {
		t.Fatalf("a device the controller has never seen was confirmed present")
	}
	if n := fc.countMatching(http.MethodPost, "/actions"); n != 0 {
		t.Fatalf("%d authorize calls were made for an unverified MAC", n)
	}

	// And the same again with the check skipped, which is the mistake this
	// guard exists for.
	if err := d.AuthorizeGuest(ctx, AuthorizeRequest{ClientMAC: neverAssociated}); err == nil {
		t.Fatalf("AuthorizeGuest succeeded for a MAC the controller does not know")
	}
	if n := fc.countMatching(http.MethodPost, "/actions"); n != 0 {
		t.Fatalf("%d authorize calls reached the controller for a MAC it does not know", n)
	}
}

// TestUniFiLegacyStamgrCSRF pins the failure that looks like a permissions
// problem and is not: UniFi OS answers 403 to every cmd/stamgr POST when the
// X-CSRF-Token from the login response is not echoed.
func TestUniFiLegacyStamgrCSRF(t *testing.T) {
	cases := []struct {
		name      string
		flavour   UniFiFlavour
		issue     string
		wantErr   bool
		wantEcho  string
		wantInErr string
	}{
		{
			name: "unifi os echoes the token it was given", flavour: FlavourUniFiOS,
			issue: testCSRFToken, wantEcho: testCSRFToken,
		},
		{
			name:    "unifi os with no token in the login response is the documented 403",
			flavour: FlavourUniFiOS, issue: "", wantErr: true, wantInErr: "X-CSRF-Token",
		},
		{
			name: "standalone needs no token", flavour: FlavourStandalone, issue: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFixtureController(t, fixtureConfig{flavour: tc.flavour, issueCSRF: tc.issue})
			// No API key: that is what selects the legacy path.
			d := fc.driver(t, func(c *UniFiConfig) {
				c.APIKey = ""
				c.Username = "local-admin"
				c.Password = "correct horse battery staple"
			})

			err := d.AuthorizeGuest(context.Background(), AuthorizeRequest{
				ClientMAC: testMAC, TimeLimitMinutes: 30,
			})
			if (err != nil) != tc.wantErr {
				t.Fatalf("AuthorizeGuest err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantInErr != "" && !strings.Contains(err.Error(), tc.wantInErr) {
				t.Errorf("error %q does not mention %q, so the operator cannot tell this from a permissions problem",
					err, tc.wantInErr)
			}

			var stamgr *recordedRequest
			for _, r := range fc.seen() {
				if strings.HasSuffix(r.Path, "/cmd/stamgr") {
					rr := r
					stamgr = &rr
				}
			}
			if stamgr == nil {
				t.Fatalf("no cmd/stamgr call; saw %v", pathsOf(fc.seen()))
			}
			if stamgr.CSRF != tc.wantEcho {
				t.Errorf("X-CSRF-Token = %q, want %q", stamgr.CSRF, tc.wantEcho)
			}
			if !strings.Contains(stamgr.Body, "authorize-guest") {
				t.Errorf("cmd/stamgr body = %q, want the authorize-guest command", stamgr.Body)
			}
		})
	}
}

func TestUniFiProbe(t *testing.T) {
	cases := []struct {
		name        string
		flavour     UniFiFlavour
		infoFile    string
		tlsMode     TLSMode
		wantBase    string
		wantVersion string
		// wantWarn is a *kind*, not a phrase. Asserting on the sentence is the
		// thing the wire format refuses to be built on, and a test that does it
		// is the same hand-maintained copy one layer down.
		wantWarn WarningKind
		// wantParams are the params that kind must carry. A figure that lives
		// only inside a Detail string is one nothing downstream can interpolate,
		// and the console's copy is assembled out of exactly these.
		wantParams map[string]any
	}{
		{
			name: "unifi os", flavour: FlavourUniFiOS, infoFile: "unifi-info.json",
			wantBase: "/proxy/network/integration/v1", wantVersion: "10.5.67",
		},
		{
			name: "standalone", flavour: FlavourStandalone, infoFile: "unifi-info.json",
			wantBase: "/integration/v1", wantVersion: "10.5.67",
		},
		{
			name: "a controller below the documented floor says so", flavour: FlavourUniFiOS,
			infoFile: "unifi-info-old.json", wantBase: "/proxy/network/integration/v1",
			wantVersion: "8.6.9", wantWarn: WarnVersionBelowFloor,
			wantParams: map[string]any{"version": "8.6.9", "minimumVersion": UniFiIntegrationAPIMinVersion},
		},
		{
			name: "a disabled certificate check is surfaced, never silent", flavour: FlavourUniFiOS,
			infoFile: "unifi-info.json", tlsMode: TLSInsecureSkipVerify,
			wantBase: "/proxy/network/integration/v1", wantVersion: "10.5.67",
			wantWarn:   WarnCertificateVerificationDisabled,
			wantParams: map[string]any{"tlsMode": string(TLSInsecureSkipVerify)},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fc := newFixtureController(t, fixtureConfig{flavour: tc.flavour, infoFile: tc.infoFile})
			d := fc.driver(t, func(c *UniFiConfig) {
				if tc.tlsMode != "" {
					c.TLSMode = tc.tlsMode
				}
			})

			res, err := d.Probe(context.Background())
			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if !res.Reachable {
				t.Errorf("Reachable = false, errors %v", res.Errors)
			}
			if res.Flavour != string(tc.flavour) {
				t.Errorf("Flavour = %q, want %q", res.Flavour, tc.flavour)
			}
			if !strings.HasSuffix(res.APIBase, tc.wantBase) {
				t.Errorf("APIBase = %q, want it to end in %q", res.APIBase, tc.wantBase)
			}
			if res.Version != tc.wantVersion {
				t.Errorf("Version = %q, want %q", res.Version, tc.wantVersion)
			}
			if res.AuthMode != "api-key" {
				t.Errorf("AuthMode = %q, want api-key", res.AuthMode)
			}
			if tc.wantWarn != "" {
				w, ok := findWarning(res.Warnings, tc.wantWarn)
				if !ok {
					t.Errorf("warnings %v carry no %q", warningKinds(res.Warnings), tc.wantWarn)
				}
				for k, want := range tc.wantParams {
					if got := w.Params[k]; got != want {
						t.Errorf("%s params[%q] = %v, want %v — the figure has to travel "+
							"structurally, because a client that parses it back out of a "+
							"sentence is a hand-maintained copy", tc.wantWarn, k, got, want)
					}
				}
			}
			if containsSubstring(res.Unverified, CapLiveReauthorize) {
				t.Errorf("Unverified = %v; liveReauthorize was MEASURED on the Integration API and must not be listed", res.Unverified)
			}
			// **Absent on every row of this table.** security.md §4 asks for
			// MAC + AP + site + still-unauthorized, and this driver checks all
			// four: the client object carries `uplinkDeviceId`, the driver
			// spends the extra call, and every controller in this table answers
			// the device list — so the notice appearing here would be the driver
			// claiming a gap it does not have.
			//
			// Where it *can* still appear is
			// TestUniFiProbe_AccessPointWarningTracksWhatTheDriverCanActuallyDo,
			// which drives both directions. This row is the half that says the
			// warning is capable of being absent, which is the half a warning
			// appended to every probe never had.
			if _, ok := findWarning(res.Warnings, WarnAccessPointUnconfirmed); ok {
				t.Errorf("warnings %v carry %q on a controller whose device list answers; the access "+
					"point a redirect claims is checked here, and a notice saying otherwise is the "+
					"one an operator learns to skip",
					warningKinds(res.Warnings), WarnAccessPointUnconfirmed)
			}
		})
	}
}

// findWarning returns the first warning of a kind. Kind, never prose: the
// contract's ProbeWarning is a kind plus params precisely so nothing has to
// match on a sentence, and a test that matched on one would reinstate the copy
// the kind removed, one layer down.
func findWarning(in []ProbeWarning, kind WarningKind) (ProbeWarning, bool) {
	for _, w := range in {
		if w.Kind == kind {
			return w, true
		}
	}
	return ProbeWarning{}, false
}

func warningKinds(in []ProbeWarning) []WarningKind {
	kinds := make([]WarningKind, 0, len(in))
	for _, w := range in {
		kinds = append(kinds, w.Kind)
	}
	return kinds
}

func containsSubstring(hay []string, needle string) bool {
	for _, s := range hay {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

// TestUniFiCapabilityEvidence locks the evidence behind each capability: a value
// must never be returned as a fact that nobody has measured. It goes red if somebody promotes a value without taking the measurement,
// and red again if an UNVERIFIED fact stops naming the measurement that settles
// it — a label with no experiment behind it is a guess in a costume.
func TestUniFiCapabilityEvidence(t *testing.T) {
	d, err := NewUniFiDriverWithError(UniFiConfig{BaseURL: "https://controller.invalid"})
	if err != nil {
		t.Fatalf("NewUniFiDriverWithError: %v", err)
	}
	ev := d.CapabilityEvidence()

	all := []string{
		CapCanAuthorizeGuest, CapCanDeauthGuest, CapHardQuota,
		CapBandwidthLimits, CapWalledGarden, CapLiveReauthorize, CapRotateSecret,
	}
	for _, name := range all {
		fact, ok := ev[name]
		if !ok {
			t.Errorf("capability %s has no evidence at all", name)
			continue
		}
		switch fact.Evidence {
		case EvidenceMeasured, EvidenceDocumented, EvidenceUnverified:
		default:
			t.Errorf("capability %s has evidence %q, which is not one of the three words", name, fact.Evidence)
		}
		if fact.Note == "" {
			t.Errorf("capability %s has no note; a bare label carries no citation and names no experiment", name)
		}
	}

	// Live re-authorization was measured against a real controller and is
	// MEASURED: on the Integration API a second
	// AUTHORIZE_GUEST_ACCESS changes a live session's limits. The value and its
	// evidence move together, and the note must cite the measurement.
	// TestUniFiLiveReauthorizeIsWhatWasMeasuredAndNoMore holds the legacy side.
	live := ev[CapLiveReauthorize]
	if live.Evidence != EvidenceMeasured || !strings.Contains(live.Note, "Integration API") {
		t.Errorf("liveReauthorize is %q (%q); it was measured on the Integration API and the note must say so",
			live.Evidence, live.Note)
	}
	if !d.Capabilities().LiveReauthorize {
		t.Errorf("liveReauthorize is returned false while its evidence is MEASURED true")
	}

	if got := UnverifiedCapabilities(d); len(got) == 0 {
		t.Errorf("UnverifiedCapabilities is empty; this driver still has two unmeasured values")
	}
}

// TestRunProbe drives the measurement instrument against both flavours, and
// checks the one thing output meant for sharing must never contain.
func TestRunProbe(t *testing.T) {
	for _, flavour := range []UniFiFlavour{FlavourUniFiOS, FlavourStandalone} {
		t.Run(string(flavour), func(t *testing.T) {
			fc := newFixtureController(t, fixtureConfig{flavour: flavour})
			d := fc.driver(t)

			steps := d.RunProbe(context.Background(), UniFiProbePlan{
				SiteID: "default", ClientMAC: testMAC,
				Authorize: true, RunReauthorize: true,
				First:  AuthorizeRequest{TimeLimitMinutes: 60, DataUsageLimitMB: 500},
				Second: AuthorizeRequest{TimeLimitMinutes: 15, DataUsageLimitMB: 100},
			})

			var integ, reauth, detail int
			for _, s := range steps {
				switch s.Name {
				case "integration":
					integ++
				case "reauthorize":
					reauth++
				case "client-detail":
					detail++
				}
				if strings.Contains(s.Body, testAPIKey) || strings.Contains(s.URL, testAPIKey) ||
					strings.Contains(s.RequestBody, testAPIKey) || strings.Contains(s.Note, testAPIKey) {
					t.Fatalf("the API key is in the probe output, which is meant to be shared: %+v", s)
				}
			}
			if integ == 0 || reauth == 0 || detail == 0 {
				t.Fatalf("steps do not cover integration/reauthorize/client-detail: %d/%d/%d", integ, reauth, detail)
			}
			// The client-detail step must read the whole client object at least twice — once before
			// any action and once after — or there is nothing to compare.
			if n := fc.countMatching(http.MethodGet, "/clients/"+testClientID); n < 2 {
				t.Errorf("%d full client reads; client-detail needs a before and an after", n)
			}
			// The re-authorization check is a *second* authorize on an already-authorized client.
			if n := fc.countMatching(http.MethodPost, "/actions"); n != 2 {
				t.Errorf("%d authorize calls, want 2 (authorize, then re-authorize)", n)
			}
			// Every recorded body is raw, so a field nobody predicted survives.
			var sawRawSites bool
			for _, s := range steps {
				if strings.HasSuffix(s.URL, "/v1/sites") && strings.Contains(s.Body, "internalReference") {
					sawRawSites = true
				}
			}
			if !sawRawSites {
				t.Errorf("the sites response was summarised rather than recorded raw")
			}
		})
	}
}

func TestRunProbe_ReadOnlyByDefault(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{flavour: FlavourUniFiOS})
	d := fc.driver(t)

	_ = d.RunProbe(context.Background(), UniFiProbePlan{SiteID: "default", ClientMAC: testMAC})

	if n := fc.countMatching(http.MethodPost, "/actions"); n != 0 {
		t.Fatalf("a probe with neither -authorize nor -reauthorize changed a live network %d times", n)
	}
}

func TestNormalizeMAC(t *testing.T) {
	cases := []struct{ in, want string }{
		{"1C:71:25:63:E4:24", "1c:71:25:63:e4:24"},
		{"1c-71-25-63-e4-24", "1c:71:25:63:e4:24"},
		{"1c7125 63e424", "1c:71:25:63:e4:24"},
		{" 1C7125:63E424 ", "1c:71:25:63:e4:24"},
		{"not-a-mac", "not-a-mac"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := normalizeMAC(tc.in); got != tc.want {
			t.Errorf("normalizeMAC(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestUniFiTLSModes: the safe default holds, and the unsafe choice has to be
// spelled out. Pinning is checked against a real handshake, because a pin that
// is never exercised is a comment.
func TestUniFiTLSModes(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer ts.Close()

	sum := sha256.Sum256(ts.Certificate().Raw)
	good := hex.EncodeToString(sum[:])
	bad := strings.Repeat("ab", sha256.Size)

	cases := []struct {
		name      string
		mode      TLSMode
		pin       string
		wantCfg   bool // construction succeeds
		wantReach bool // a request completes
	}{
		{name: "verify refuses a self-signed controller", mode: TLSVerify, wantCfg: true},
		{name: "insecure reaches it, explicitly", mode: TLSInsecureSkipVerify, wantCfg: true, wantReach: true},
		{name: "pinned to the right fingerprint", mode: TLSPinnedSHA256, pin: good, wantCfg: true, wantReach: true},
		{name: "pinned to the wrong fingerprint", mode: TLSPinnedSHA256, pin: bad, wantCfg: true},
		{name: "pinned with no fingerprint is a configuration error", mode: TLSPinnedSHA256},
		{name: "an unknown mode is refused rather than downgraded", mode: TLSMode("whatever")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, err := NewUniFiDriverWithError(UniFiConfig{
				BaseURL: ts.URL, APIKey: testAPIKey, TLSMode: tc.mode, PinnedSHA256: tc.pin,
				Flavour: FlavourStandalone, Timeout: 3 * time.Second,
			})
			if (err == nil) != tc.wantCfg {
				t.Fatalf("construction err = %v, want ok = %v", err, tc.wantCfg)
			}
			if err != nil {
				return
			}
			_, lookupErr := d.lookupClient(context.Background(), "default", testMAC)
			if (lookupErr == nil) != tc.wantReach {
				t.Fatalf("request err = %v, want reachable = %v", lookupErr, tc.wantReach)
			}
		})
	}
}

// TestUniFiRevokeCoAFallback: UniFi's captive-portal *authorization* has no
// RADIUS equivalent, but revocation does, so a Disconnect is a legitimate
// fallback when the controller API is unreachable — and only then.
//
// The second case is the one that matters: a controller that answered and said
// no is not an outage, and disconnecting anyway would be acting on our own
// opinion of its answer.
//
// UNVERIFIED against hardware. Nothing here has met a real NAS.
func TestUniFiRevokeCoAFallback(t *testing.T) {
	t.Run("controller unreachable falls back", func(t *testing.T) {
		fc := newFixtureController(t, fixtureConfig{flavour: FlavourUniFiOS})
		r := newCoAResponder(t, coaTestSecret, func(req []byte) []byte {
			return buildReply(req, coaTestSecret, codeDisconnectACK, 0, false)
		})
		d := fc.driver(t, func(c *UniFiConfig) {
			c.Flavour = FlavourUniFiOS // no flavour probe once the server is gone
			c.CoAFallback = &CoAClient{Addr: r.addr(), Secret: coaTestSecret, Timeout: time.Second}
		})
		fc.server.Close()

		if err := d.DeauthorizeGuest(context.Background(), DeauthorizeRequest{ClientMAC: testMAC}); err != nil {
			t.Fatalf("DeauthorizeGuest: %v", err)
		}
		if got := r.packets.Load(); got != 1 {
			t.Errorf("the NAS saw %d Disconnect packets, want 1", got)
		}
	})

	t.Run("a controller that refuses is not an outage", func(t *testing.T) {
		fc := newFixtureController(t, fixtureConfig{
			flavour:      FlavourUniFiOS,
			clientsFile:  "unifi-clients-authorized.json",
			actionStatus: http.StatusForbidden,
		})
		r := newCoAResponder(t, coaTestSecret, func(req []byte) []byte {
			return buildReply(req, coaTestSecret, codeDisconnectACK, 0, false)
		})
		d := fc.driver(t, func(c *UniFiConfig) {
			c.CoAFallback = &CoAClient{Addr: r.addr(), Secret: coaTestSecret, Timeout: time.Second}
		})

		if err := d.DeauthorizeGuest(context.Background(), DeauthorizeRequest{ClientMAC: testMAC}); err == nil {
			t.Fatalf("a 403 from the controller was reported as a successful revocation")
		}
		if got := r.packets.Load(); got != 0 {
			t.Errorf("a Disconnect was sent though the controller had answered: %d packets", got)
		}
	})
}

// TestNewUniFiDriver_MisconfiguredIsInertNotInsecure: the compatibility
// constructor cannot return an error, so it must not answer a bad TLS
// configuration by talking to the controller anyway.
func TestNewUniFiDriver_MisconfiguredIsInertNotInsecure(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{flavour: FlavourUniFiOS})
	d := NewUniFiDriver(UniFiConfig{
		BaseURL: fc.server.URL, APIKey: testAPIKey, TLSMode: TLSPinnedSHA256, // no fingerprint
	})
	if _, err := d.VerifyPresence(context.Background(), testMAC, "", "default"); err == nil {
		t.Fatalf("a driver with an unusable TLS configuration answered a presence check")
	}
	if len(fc.seen()) != 0 {
		t.Fatalf("a misconfigured driver still reached the controller: %v", pathsOf(fc.seen()))
	}
}
