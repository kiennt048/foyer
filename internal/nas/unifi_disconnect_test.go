package nas

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// UniFi's half of `disconnectSession`, against the fixture controller.
//
// **A fixture, not a controller.** `httptest` answers the routes this driver is
// documented to call, so this proves which route and which body a disconnect sends
// — and that a controller's refusal is reported rather than flattened — and nothing
// about what Network 10.x then does to the guest. That is UNVERIFIED until
// somebody runs it on a real controller.

// TestUniFiDisconnectUnauthorizesTheGuest: the Integration API path posts
// UNAUTHORIZE_GUEST_ACCESS for the controller's own client id, with the API key,
// and carries no authorize-only field.
func TestUniFiDisconnectUnauthorizesTheGuest(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{flavour: FlavourUniFiOS, clientsFile: "unifi-clients-authorized.json"})
	d := fc.driver(t)

	if err := d.DeauthorizeGuest(context.Background(), DeauthorizeRequest{ClientMAC: strings.ToUpper(testMAC)}); err != nil {
		t.Fatalf("DeauthorizeGuest: %v", err)
	}

	var action *recordedRequest
	for _, r := range fc.seen() {
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/clients/"+testClientID+"/actions") {
			rr := r
			action = &rr
		}
	}
	if action == nil {
		t.Fatalf("no client action was posted; saw %v", pathsOf(fc.seen()))
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(action.Body), &body); err != nil {
		t.Fatalf("action body %q: %v", action.Body, err)
	}
	if body["action"] != actionUnauthorize {
		t.Errorf("action = %v, want %s — the authorize action here would let the guest *on*", body["action"], actionUnauthorize)
	}
	if len(body) != 1 {
		t.Errorf("unauthorize carries %v; limits belong to the authorize action only", body)
	}
	if action.APIKey != testAPIKey {
		t.Errorf("X-API-KEY = %q, want the controller's key", action.APIKey)
	}
}

// TestUniFiDisconnectOnTheLegacyAPISendsUnauthorizeGuest: below 9.1.105 the same act
// is `cmd/stamgr` `unauthorize-guest`, echoing the CSRF token UniFi OS demands.
func TestUniFiDisconnectOnTheLegacyAPISendsUnauthorizeGuest(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{flavour: FlavourUniFiOS, issueCSRF: testCSRFToken})
	d := fc.driver(t, func(c *UniFiConfig) {
		c.APIKey = ""
		c.Username = "local-admin"
		c.Password = "correct horse battery staple"
	})

	if err := d.DeauthorizeGuest(context.Background(), DeauthorizeRequest{ClientMAC: testMAC}); err != nil {
		t.Fatalf("DeauthorizeGuest: %v", err)
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
	var cmd map[string]any
	if err := json.Unmarshal([]byte(stamgr.Body), &cmd); err != nil {
		t.Fatalf("stamgr body %q: %v", stamgr.Body, err)
	}
	if cmd["cmd"] != "unauthorize-guest" || cmd["mac"] != testMAC {
		t.Errorf("cmd/stamgr body = %v, want unauthorize-guest for %s", cmd, testMAC)
	}
	if stamgr.CSRF != testCSRFToken {
		t.Errorf("X-CSRF-Token = %q, want %q", stamgr.CSRF, testCSRFToken)
	}
}

// TestUniFiDisconnectRefusalIsTyped: a controller that answers the unauthorize with
// a 4xx is an *APIError — which `internal/apistore` reports as `refused` — and never
// a success. A body the controller sent is not what reaches an operator.
func TestUniFiDisconnectRefusalIsTyped(t *testing.T) {
	fc := newFixtureController(t, fixtureConfig{
		flavour: FlavourUniFiOS, clientsFile: "unifi-clients-authorized.json", actionStatus: http.StatusForbidden,
	})
	err := fc.driver(t).DeauthorizeGuest(context.Background(), DeauthorizeRequest{ClientMAC: testMAC})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("a 403 answered %v, want a typed *APIError with status 403", err)
	}
}

// TestUniFiCanDeauthGuestIsWhatDisconnectReads: the capability the console gates the
// control on is true for UniFi and DOCUMENTED, not MEASURED — nobody has watched a
// guest drop off on a real controller yet.
func TestUniFiCanDeauthGuestIsWhatDisconnectReads(t *testing.T) {
	d := newFixtureController(t, fixtureConfig{flavour: FlavourUniFiOS}).driver(t)
	if !d.Capabilities().CanDeauthGuest {
		t.Fatal("UniFi reports it cannot end a session; the console would hide a control the controller documents")
	}
	if ev := d.CapabilityEvidence()[CapCanDeauthGuest]; ev.Evidence == EvidenceMeasured {
		t.Errorf("canDeauthGuest is marked MEASURED (%q); no disconnect has been observed on real UniFi — "+
			"it must stay UNVERIFIED until one is", ev.Note)
	}
}
