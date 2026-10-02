package apistore_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/identity/otp"
	"github.com/kiennt048/foyer/internal/identity/sms"
	"github.com/kiennt048/foyer/internal/portal"
)

// Phone sign-in through the adapter and PostgreSQL: the wiring an operator gets.
// `internal/identity/otp` proves the rules against its in-memory store and
// `internal/store` proves the SQL; this is the half that proves they are joined —
// the keys sealed by the admin API are the keys the send path unseals, the code
// lands as a keyed MAC, and the portal port answers through the SQL limits.
func TestPhoneSignInThroughTheAdapter(t *testing.T) {
	fake := &sms.Fake{}
	var seen sms.Config
	w := newWorldWith(t, func(c *apistore.Config) {
		c.SMSProvider = func(cfg sms.Config) (sms.Provider, error) { seen = cfg; return fake, nil }
	})
	w.signIn()
	ctx := context.Background()

	body := map[string]any{
		"provider": "esms-sms", "brandname": "FOYER", "dailyLimit": 2, "sessionMinutes": 45,
		"messageTemplate": "{code} la ma Wi-Fi", "apiKey": "APIKEY-AT-REST", "secretKey": "SECRET-AT-REST",
	}
	raw := w.do(http.MethodPut, "/sites/"+w.siteA+"/portal/phone", body, http.StatusOK)
	if bytes.Contains(raw, []byte("APIKEY-AT-REST")) || bytes.Contains(raw, []byte("SECRET-AT-REST")) {
		t.Fatalf("the response carries a key: %s", raw)
	}
	var sealed []byte
	if err := w.owner.QueryRow(ctx, `SELECT api_key FROM phone_sign_in WHERE site_id = $1`, w.siteA).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("APIKEY-AT-REST")) {
		t.Fatal("the API key is at rest in the clear")
	}

	codes := w.adapter.PortalPorts().PhoneCodes
	if codes == nil {
		t.Fatal("the adapter wires no phone sign-in port")
	}
	if m, ok := codes.Offered(ctx, w.siteA); !ok || m != 45 {
		t.Fatalf("offered %d %v", m, ok)
	}
	req := portal.PhoneCodeRequest{SiteID: w.siteA, SessionTokenHash: uniq("tok"), ClientMAC: "aa:bb:cc:00:11:22",
		ClientIP: "10.9.9.9", Phone: "0912 345 678"}
	if err := codes.Send(ctx, req); err != nil {
		t.Fatalf("send: %v", err)
	}
	if seen.APIKey != "APIKEY-AT-REST" || seen.SecretKey != "SECRET-AT-REST" || seen.Brandname != "FOYER" {
		t.Fatalf("the send path did not unseal what the admin API sealed: %v", seen)
	}
	msg := fake.Last()

	var mac []byte
	var keyID, phone string
	if err := w.owner.QueryRow(ctx,
		`SELECT code_mac, key_id, phone FROM phone_codes WHERE id = $1`, msg.RequestID).Scan(&mac, &keyID, &phone); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(mac, []byte(msg.Code)) || len(mac) != 32 || phone != "+84912345678" {
		t.Fatalf("stored %x %q", mac, phone)
	}
	id, key, err := w.st.Keys().PhoneCodeKey()
	if err != nil {
		t.Fatal(err)
	}
	if keyID != id || !otp.SameMAC(mac, otp.MAC(key, msg.RequestID, req.SessionTokenHash, msg.Code)) {
		t.Fatal("the stored MAC is not the KEK-derived MAC of this code")
	}

	got, err := codes.Check(ctx, req.SessionTokenHash, msg.Code)
	if err != nil || got != "+84912345678" {
		t.Fatalf("check: %q %v", got, err)
	}
	if _, err := codes.Check(ctx, req.SessionTokenHash, msg.Code); !errors.Is(err, portal.ErrCodeExpired) {
		t.Fatalf("second use: %v", err)
	}

	// The ceiling of two, counted in SQL, refuses the third message of the day.
	req2 := req
	req2.SessionTokenHash, req2.ClientMAC, req2.ClientIP, req2.Phone = uniq("tok"), "aa:bb:cc:00:11:23", "10.9.9.8", "0987654321"
	if err := codes.Send(ctx, req2); err != nil {
		t.Fatalf("second send: %v", err)
	}
	req3 := req2
	req3.SessionTokenHash, req3.ClientMAC, req3.ClientIP, req3.Phone = uniq("tok"), "aa:bb:cc:00:11:24", "10.9.9.7", "0977654321"
	if err := codes.Send(ctx, req3); !errors.Is(err, portal.ErrPhoneCannotSend) {
		t.Fatalf("third send past a ceiling of two: %v", err)
	}
	if n := len(fake.Sent()); n != 2 {
		t.Fatalf("%d messages against a ceiling of two", n)
	}

	var out struct {
		SentToday      int  `json:"sentToday"`
		CredentialsSet bool `json:"credentialsSet"`
	}
	raw = w.do(http.MethodGet, "/sites/"+w.siteA+"/portal/phone", nil, http.StatusOK)
	_ = json.Unmarshal(raw, &out)
	if out.SentToday != 2 || !out.CredentialsSet || strings.Contains(string(raw), "AT-REST") {
		t.Fatalf("read back %s", raw)
	}
}
