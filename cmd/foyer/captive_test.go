package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/portal"
)

// RFC 8908 through the listener the process actually builds.
//
// **Written against the listener's routing and the store, and nothing newer**, so
// that it goes red against a `portalListener` that mounts
// `portal.RFC8908Handler(true, …)`, which answers `captive: true` to every device
// including one that has just been let on.

// knownSite places every access point in one site, so the front door can mint.
type knownSite struct{}

func (knownSite) Placement(context.Context, string) (portal.Placement, error) {
	return portal.Placement{SiteID: "site-one", VendorSiteID: "default"}, nil
}

func captivePorts() (portal.Ports, *portal.MemoryStore) {
	ports, mem := testPortalPorts()
	ports.Sites = knownSite{}
	return ports, mem
}

func serve(t *testing.T, h http.Handler, method, target, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func captiveAnswer(t *testing.T, h http.Handler, remote string) portal.CapportData {
	t.Helper()
	rec := serve(t, h, http.MethodGet, "/api/v1/captive-portal", remote)
	if ct := rec.Header().Get("Content-Type"); ct != portal.MediaTypeCaptiveJSON {
		t.Fatalf("the RFC 8908 endpoint answered %q, want %q", ct, portal.MediaTypeCaptiveJSON)
	}
	var data portal.CapportData
	body, _ := io.ReadAll(rec.Body)
	if err := json.Unmarshal(body, &data); err != nil {
		t.Fatalf("the RFC 8908 answer is not JSON: %v (%q)", err, body)
	}
	return data
}

// TestTheCaptivePortalAPIAnswersForTheDeviceThatAsked — `captive: false` for the
// device holding a live grant, asking the way an operating system does (no
// cookie, from its own address), and `captive: true` for every other device.
func TestTheCaptivePortalAPIAnswersForTheDeviceThatAsked(t *testing.T) {
	ports, mem := captivePorts()
	srv, err := api.New(api.Config{Portal: ports})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	svc, err := portal.NewService(portal.ServiceConfig{Ports: ports})
	if err != nil {
		t.Fatalf("portal.NewService: %v", err)
	}
	h := portalListener(testConfig(), srv, svc, testObservability(t)).srv.Handler

	const guest = "10.20.0.5"
	const mac = "aa:bb:cc:00:11:22"

	// The NAS redirect reaches the front door from the guest's address.
	serve(t, h, http.MethodGet, "/guest/s/default/?id="+mac+"&ap=11:22:33:44:55:66", guest+":40000")
	sessions := mem.Sessions()
	if len(sessions) != 1 {
		t.Fatalf("the front door minted %d sessions, want 1", len(sessions))
	}

	if got := captiveAnswer(t, h, guest+":40001"); !got.Captive {
		t.Fatalf("captive = false before the device holds any grant")
	}

	// The venue lets it on: a live one-hour grant, bound to that browser.
	end := time.Now().Add(time.Hour)
	g := portal.Grant{
		ID: "grant-q4a", SiteID: "site-one", ClientMAC: mac,
		Method: portal.MethodClickThrough, GrantedAt: time.Now(), ExpiresAt: &end,
	}
	if err := mem.CreateGrant(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if err := mem.AttachGrant(context.Background(), sessions[0].TokenHash, g.ID); err != nil {
		t.Fatal(err)
	}

	granted := captiveAnswer(t, h, guest+":40002")
	if granted.Captive {
		t.Errorf("captive = true for the device that holds a live grant — the endpoint is not answering per device")
	}
	if granted.SecondsRemaining == nil || *granted.SecondsRemaining <= 0 || *granted.SecondsRemaining > 3600 {
		t.Errorf("seconds-remaining = %v, want the grant's remaining time", granted.SecondsRemaining)
	}

	if other := captiveAnswer(t, h, "10.20.0.99:40003"); !other.Captive {
		t.Errorf("captive = false for a device the portal never saw")
	}
}

// TestTheRFC8908EndpointReadsWhatTheAPIObserved holds `run()`'s wiring in place:
// the API server's `portal.Service` and the front door's must share one address
// book, or the endpoint cannot see a device the API middleware observed.
//
// The device reaches the front door from one address and calls
// `/api/v0/portal/status` from another — a dual-stack phone does exactly this,
// the WebView on IPv4 and the page's `fetch` on IPv6 — and the operating system
// then asks from the second. Only the API server's service saw that address. With
// two books (`NewService`'s default each), the answer is `captive: true`.
func TestTheRFC8908EndpointReadsWhatTheAPIObserved(t *testing.T) {
	ports, mem := captivePorts()
	srv, svc, err := portalServices(testConfig(), api.Ports{}, ports)
	if err != nil {
		t.Fatalf("portalServices: %v", err)
	}
	h := portalListener(testConfig(), srv, svc, testObservability(t)).srv.Handler

	const mac = "aa:bb:cc:00:11:33"
	front := serve(t, h, http.MethodGet, "/guest/s/default/?id="+mac+"&ap=11:22:33:44:55:66", "10.20.0.7:40000")
	var cookie *http.Cookie
	for _, c := range front.Result().Cookies() {
		if c.Name == portal.SessionCookieName {
			cookie = c
		}
	}
	sessions := mem.Sessions()
	if cookie == nil || len(sessions) != 1 {
		t.Fatalf("the front door minted no session (cookie %v, %d sessions)", cookie, len(sessions))
	}
	end := time.Now().Add(time.Hour)
	g := portal.Grant{
		ID: "grant-shared", SiteID: "site-one", ClientMAC: mac,
		Method: portal.MethodClickThrough, GrantedAt: time.Now(), ExpiresAt: &end,
	}
	if err := mem.CreateGrant(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	if err := mem.AttachGrant(context.Background(), sessions[0].TokenHash, g.ID); err != nil {
		t.Fatal(err)
	}

	const second = "[2001:db8::7]"
	req := httptest.NewRequest(http.MethodGet, api.BasePath+"/portal/status", nil)
	req.RemoteAddr = second + ":40001"
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /portal/status with the session cookie answered %d", rec.Code)
	}

	if got := captiveAnswer(t, h, second+":40002"); got.Captive {
		t.Errorf("captive = true from the address only the API server's service saw — " +
			"the two portal services do not share one address book")
	}
}
