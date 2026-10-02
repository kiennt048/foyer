package apistore_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/portal"
)

// TestTheCaptivePortalAPIAnswersFalseAfterARedemptionInPostgreSQL — RFC 8908 per
// device, against the real store rather than the portal's
// in-memory fake.
//
// What only PostgreSQL can say: that the session row's `grant_id` is readable back
// through the adapter after the redemption, and that `client_mac`, stored as
// `macaddr`, compares equal to the MAC the front door bound — the endpoint refuses
// to answer `captive: false` for a grant whose MAC differs from the address's.
//
// The query carries **no cookie**, because the operating system's API client is not
// the portal's WebView. Every request here comes from loopback, which is the "same
// address" RFC 8908 §4 asks the server and the enforcement device to share.
func TestTheCaptivePortalAPIAnswersFalseAfterARedemptionInPostgreSQL(t *testing.T) {
	v := newVenue(t)

	ask := func() portal.CapportData {
		t.Helper()
		res, err := http.Get(v.server.URL + "/api/v1/captive-portal")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var data portal.CapportData
		if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
			t.Fatalf("decoding the RFC 8908 answer: %v", err)
		}
		return data
	}

	v.arrive(v.guestMAC)
	if got := ask(); !got.Captive {
		t.Fatalf("captive = false before any grant exists")
	}

	resp := v.api(http.MethodPost, "/portal/voucher", url.Values{"code": {v.code}, "lang": {"en"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeeming: %d", resp.StatusCode)
	}

	if got := ask(); got.Captive {
		t.Errorf("captive = true for the device that just redeemed, asking from the address it arrived at")
	}
}

// TestTheCaptiveAnswerSurvivesARestartAndASecondProcess — the address book is in
// PostgreSQL (migration 00045).
//
// Held in the process's memory, a restart or a second control-plane process would
// answer the device that had just redeemed `captive: true` until it next reached
// the front door. Here a second adapter and service over the
// same database — a restarted process, or a replica — answer it `captive: false`
// without having seen it, and a device the SECOND process sees from the same
// address makes the address ambiguous for both: the fail-safe is shared too.
func TestTheCaptiveAnswerSurvivesARestartAndASecondProcess(t *testing.T) {
	v := newVenue(t)

	other, err := apistore.New(apistore.Config{Store: openStore(t), Drivers: v.drivers})
	if err != nil {
		t.Fatal(err)
	}
	ports := other.PortalPorts()
	svc, err := portal.NewService(portal.ServiceConfig{Ports: ports})
	if err != nil {
		t.Fatal(err)
	}
	restarted := httptest.NewServer(svc.CaptiveHandler("http://portal.example/en/", ""))
	t.Cleanup(restarted.Close)

	ask := func(base string) portal.CapportData {
		t.Helper()
		res, err := http.Get(base + "/api/v1/captive-portal")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var data portal.CapportData
		if err := json.NewDecoder(res.Body).Decode(&data); err != nil {
			t.Fatal(err)
		}
		return data
	}

	v.arrive(v.guestMAC)
	resp := v.api(http.MethodPost, "/portal/voucher", url.Values{"code": {v.code}, "lang": {"en"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeeming: %d", resp.StatusCode)
	}
	if ask(v.server.URL).Captive {
		t.Fatal("the process that saw the redemption answers captive")
	}
	if ask(restarted.URL).Captive {
		t.Error("a process that did not see the redemption answers captive: the book is still in one process's memory")
	}

	// A second device behind the same address, seen only by the second process.
	ports.Addresses.Observe(netip.MustParseAddr("127.0.0.1"), "another-browser", "02:99:99:99:99:99", "", time.Now())
	if !ask(v.server.URL).Captive || !ask(restarted.URL).Captive {
		t.Error("an address two devices were seen from answers for one of them")
	}
}
