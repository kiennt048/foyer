package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity/otp"
	"github.com/kiennt048/foyer/internal/portal"
)

// The per-address limit is keyed on the peer address and on nothing the guest
// writes. Proven only inside `internal/identity/otp`, with the address handed in,
// keying it on `X-Forwarded-For`, or not wiring the address into the context at
// all, would leave every test green — and either one turns
// "sixty an hour per address" into "unlimited for anybody who sets a header".
func TestThePerAddressLimitIsTheSocketAddressNotAHeader(t *testing.T) {
	g := presentRig(t)
	configurePhone(t, g)
	devices := otp.DefaultLimits.PerAddressHour + 1
	// The site's daily ceiling above the devices, so the refusal this test waits
	// for can only be the per-address one.
	brand, key, secret := "FOYER", "api-key", "secret-key"
	if _, err := g.store.SetPhoneSignIn(context.Background(), portalSite, gen.PhoneSignInUpdate{
		Provider: gen.EsmsSms, Brandname: &brand, ApiKey: &key, SecretKey: &secret,
		DailyLimit: 500, SessionMinutes: 120, MessageTemplate: "{code} la ma Wi-Fi",
	}); err != nil {
		t.Fatal(err)
	}
	macs := make([]string, devices)
	for i := range macs {
		macs[i] = fmt.Sprintf("02:00:00:00:01:%02x", i)
		// Set before any request is served: the driver's map is read by the server.
		g.driver.Presences[macs[i]] = true
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i, mac := range macs {
		g.jar = nil
		g.arrive(mac).Body.Close()
		form := url.Values{"phone": {fmt.Sprintf("09123450%02d", i)}, "lang": {"en"}}
		req, err := http.NewRequest(http.MethodPost, g.server.URL+api.BasePath+"/portal/phone", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		// A different forwarded address every time. The limit must not see it.
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i+1))
		for _, c := range g.jar {
			req.AddCookie(c)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc := resp.Header.Get("Location")
		if i < devices-1 && !strings.Contains(loc, "sent=1") {
			t.Fatalf("device %d of %d from one address was refused: %q", i+1, devices, loc)
		}
		if i == devices-1 && !strings.Contains(loc, "e="+string(portal.ReasonCodeLimit)) {
			t.Fatalf("device %d from one address was not refused by the per-address limit: %q", devices, loc)
		}
	}
	if n := len(g.store.Phone().SMS.Sent()); n != devices-1 {
		t.Fatalf("%d messages from one address, want %d", n, devices-1)
	}
}

// The audit entry says whether the keys changed. It is the security-relevant half
// of the entry (the contract's words), and without this test hard-coding it to
// false would leave the suite green.
func TestPhoneSignInAuditSaysWhetherTheKeysChanged(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	path := "/sites/" + testSite + "/portal/phone"
	base := map[string]any{"provider": "esms-sms", "brandname": "FOYER", "dailyLimit": 20, "sessionMinutes": 60,
		"messageTemplate": "{code} la ma Wi-Fi"}
	withKeys := map[string]any{"apiKey": "AAA-KEY-VALUE", "secretKey": "BBB-SECRET-VALUE"}
	for k, v := range base {
		withKeys[k] = v
	}
	for _, body := range []map[string]any{withKeys, base} {
		resp := r.do(http.MethodPut, path, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("configure: %d", resp.StatusCode)
		}
	}
	var got []any
	for _, e := range r.store.World().Audit {
		if e.Params != nil && (*e.Params)["section"] == "phone-sign-in" {
			got = append(got, (*e.Params)["credentialsChanged"])
		}
	}
	// The world's audit log is newest first: the keyless write, then the first.
	if len(got) != 2 || got[0] != false || got[1] != true {
		t.Fatalf("credentialsChanged across a write with keys and one without: %v", got)
	}
}
