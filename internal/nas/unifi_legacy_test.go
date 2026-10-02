package nas

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// A controller reached with a local admin account and no API key.
//
// A VerifyPresence that called only the Integration API's client lookup could never
// let a guest on in this mode: a controller answers that 401 with no key, so the
// portal would refuse every guest before AuthorizeGuest's legacy branch was ever
// reached. These tests drive the whole path a
// portal takes on such a controller — presence, then authorize — against a fake
// legacy API that refuses anything carrying no session cookie.

type legacyController struct {
	mu       sync.Mutex
	paths    []string
	stations string // the stat/sta answer body
	status   int    // the stat/sta status, 200 when zero
	server   *httptest.Server
}

func newLegacyController(t *testing.T, stations string) *legacyController {
	t.Helper()
	lc := &legacyController{stations: stations}
	lc.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lc.mu.Lock()
		lc.paths = append(lc.paths, r.Method+" "+r.URL.Path)
		lc.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == standaloneLoginPath:
			http.SetCookie(w, &http.Cookie{Name: "unifises", Value: "legacy-session"})
			_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[]}`))
			return
		case strings.HasPrefix(r.URL.Path, "/api/"):
			if c, err := r.Cookie("unifises"); err != nil || c.Value != "legacy-session" {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"meta":{"rc":"error","msg":"api.err.LoginRequired"}}`))
				return
			}
		case strings.Contains(r.URL.Path, "/v1/"):
			// The Integration API with no key: exactly what a real controller does.
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/api/s/default/stat/sta/"+testMAC:
			if lc.status != 0 {
				w.WriteHeader(lc.status)
			}
			_, _ = w.Write([]byte(lc.stations))
		case r.URL.Path == "/api/self/sites":
			_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[{"name":"default","desc":"Default"}]}`))
		case r.URL.Path == "/api/s/default/cmd/stamgr":
			_, _ = w.Write([]byte(`{"meta":{"rc":"ok"},"data":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(lc.server.Close)
	return lc
}

func (lc *legacyController) driver(t *testing.T) *UniFiDriver {
	t.Helper()
	d, err := NewUniFiDriverWithError(UniFiConfig{
		BaseURL: lc.server.URL, Site: "default", Timeout: 5 * time.Second,
		Username: "local-admin", Password: "not-a-real-password",
	})
	if err != nil {
		t.Fatalf("NewUniFiDriverWithError: %v", err)
	}
	// The fake is a standalone controller; skip the flavour probe's guesswork.
	d.setFlavour(FlavourStandalone)
	return d
}

func (lc *legacyController) called(substr string) bool {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	for _, p := range lc.paths {
		if strings.Contains(p, substr) {
			return true
		}
	}
	return false
}

const testAPLegacyMAC = "00:00:5e:00:53:01"

func stationJSON(authorized bool, apMAC string) string {
	a := "false"
	if authorized {
		a = "true"
	}
	return `{"meta":{"rc":"ok"},"data":[{"_id":"5f1e0c","mac":"` + testMAC + `","ap_mac":"` + apMAC + `","authorized":` + a + `,"is_guest":true}]}`
}

func TestUniFiLocalAdminModeCanVerifyAndAuthorizeAGuest(t *testing.T) {
	lc := newLegacyController(t, stationJSON(false, testAPLegacyMAC))
	d := lc.driver(t)
	ctx := context.Background()

	p, err := d.VerifyPresenceDetail(ctx, testMAC, testAPLegacyMAC, "default")
	if err != nil {
		t.Fatalf("VerifyPresenceDetail in local-admin mode: %v — the portal refuses every guest here", err)
	}
	if !p.OK || p.Outcome != PresenceVerified {
		t.Fatalf("presence = %+v, want a verified guest", p)
	}
	if !p.APConfirmed {
		t.Error("stat/sta names the access point as ap_mac and it matched the claim, yet APConfirmed is false")
	}
	if lc.called("/v1/") {
		t.Error("local-admin mode called the Integration API, which has no credential to answer to")
	}
	if err := d.AuthorizeGuest(ctx, AuthorizeRequest{ClientMAC: testMAC, SiteID: "default", TimeLimitMinutes: 60}); err != nil {
		t.Fatalf("AuthorizeGuest after a verified presence: %v", err)
	}
	if !lc.called("/cmd/stamgr") {
		t.Error("the authorize never reached cmd/stamgr")
	}
}

func TestUniFiLocalAdminPresenceRefusesWhatSecurityMd4Refuses(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stations string
		status   int
		ap       string
		want     PresenceOutcome
		wantErr  bool
	}{
		{"not associated (empty list)", `{"meta":{"rc":"ok"},"data":[]}`, 0, testAPLegacyMAC, PresenceNotFound, false},
		{"not associated (UnknownStation)", `{"meta":{"rc":"error","msg":"api.err.UnknownStation"},"data":[]}`, http.StatusBadRequest, testAPLegacyMAC, PresenceNotFound, false},
		{"already authorized", stationJSON(true, testAPLegacyMAC), 0, testAPLegacyMAC, PresenceAlreadyAuthorized, false},
		{"under another access point", stationJSON(false, "aa:bb:cc:dd:ee:ff"), 0, testAPLegacyMAC, PresenceAPMismatch, false},
		{"controller refusing", `{"meta":{"rc":"error","msg":"api.err.NoPermission"}}`, http.StatusForbidden, testAPLegacyMAC, PresenceUnavailable, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lc := newLegacyController(t, tc.stations)
			lc.status = tc.status
			p, err := lc.driver(t).VerifyPresenceDetail(context.Background(), testMAC, tc.ap, "default")
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if p.OK || p.Outcome != tc.want {
				t.Fatalf("presence = %+v, want outcome %s and not OK", p, tc.want)
			}
		})
	}
}

func TestUniFiLocalAdminProbeReachesTheLegacyAPIAndListsItsSites(t *testing.T) {
	lc := newLegacyController(t, stationJSON(false, testAPLegacyMAC))
	res, err := lc.driver(t).Probe(context.Background())
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.Reachable || len(res.Errors) != 0 {
		t.Fatalf("probe = reachable %v, errors %v; a controller every guest call reaches was reported down", res.Reachable, res.Errors)
	}
	if res.AuthMode != "legacy-login" {
		t.Errorf("AuthMode = %q, want legacy-login", res.AuthMode)
	}
	if len(res.VendorSites) != 1 || res.VendorSites[0].ID != "default" {
		t.Errorf("VendorSites = %+v, want the legacy site named default", res.VendorSites)
	}
}

// Live re-authorization was measured on the Integration API and nothing else, so
// the capability follows the path the driver actually takes.
func TestUniFiLiveReauthorizeIsWhatWasMeasuredAndNoMore(t *testing.T) {
	integration, err := NewUniFiDriverWithError(UniFiConfig{BaseURL: "https://controller.invalid", APIKey: testAPIKey})
	if err != nil {
		t.Fatal(err)
	}
	if !integration.Capabilities().LiveReauthorize {
		t.Error("liveReauthorize is false on the Integration API, which was measured true")
	}
	if ev := integration.CapabilityEvidence()[CapLiveReauthorize]; ev.Evidence != EvidenceMeasured || !strings.Contains(ev.Note, "Integration API") {
		t.Errorf("evidence = %+v, want MEASURED citing the Integration API", ev)
	}

	legacy := newLegacyController(t, "{}").driver(t)
	if legacy.Capabilities().LiveReauthorize {
		t.Error("liveReauthorize is true for cmd/stamgr, which was never measured")
	}
	if ev := legacy.CapabilityEvidence()[CapLiveReauthorize]; ev.Evidence != EvidenceUnverified {
		t.Errorf("legacy evidence = %+v, want UNVERIFIED", ev)
	}
	var unsupported *UnsupportedActionError
	if err := legacy.Reauthorize(context.Background(), AuthorizeRequest{ClientMAC: testMAC}); err == nil || !asUnsupported(err, &unsupported) {
		t.Errorf("Reauthorize in legacy mode = %v, want an UnsupportedActionError", err)
	}
	if !ShippedCapabilities(VendorUniFi).LiveReauthorize {
		t.Error("the shipped UniFi driver (the Integration API) does not report liveReauthorize")
	}
}

func asUnsupported(err error, target **UnsupportedActionError) bool {
	e, ok := err.(*UnsupportedActionError)
	if ok {
		*target = e
	}
	return ok
}
