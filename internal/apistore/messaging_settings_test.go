package apistore_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/identity/sms"
	"github.com/kiennt048/foyer/internal/portal"
)

// The admin-configurability rule over PostgreSQL: each setting written through the
// console's operation reaches the send path on the very next call, through the
// adapter's per-request reads (and its zone cache, which a write must clear).
// `internal/api/messaging_settings_test.go` proves the same against the memory
// world; this proves the SQL, the CHECKs and the wiring agree with it.
func TestMessagingSettingsTakeEffectThroughPostgres(t *testing.T) {
	fake := &sms.Fake{}
	w := newWorldWith(t, func(c *apistore.Config) {
		c.SMSProvider = func(sms.Config) (sms.Provider, error) { return fake, nil }
	})
	w.signIn()
	ctx := context.Background()
	w.do(http.MethodPut, "/sites/"+w.siteA+"/portal/phone", map[string]any{
		"provider": "esms-sms", "brandname": "FOYER", "dailyLimit": 10, "sessionMinutes": 45,
		"messageTemplate": "{code} la ma Wi-Fi", "apiKey": "K", "secretKey": "S",
	}, http.StatusOK)
	// The calendar first: Kiritimati (UTC+14) and Pago Pago (UTC−11) are never on
	// the same date, so moving the site between them at the end moves "today" off
	// every message sent here.
	w.do(http.MethodPut, "/sites/"+w.siteA+"/time-zone", map[string]any{"timeZone": "Pacific/Kiritimati"}, http.StatusOK)
	codes := w.adapter.PortalPorts().PhoneCodes
	send := func(ip, phone string) error {
		return codes.Send(ctx, portal.PhoneCodeRequest{SiteID: w.siteA, SessionTokenHash: uniq("tok"),
			ClientMAC: "aa:bb:cc:00:11:22", ClientIP: ip, Phone: phone})
	}

	// Abuse controls: one code an hour per address, 09x only.
	w.do(http.MethodPut, "/sites/"+w.siteA+"/portal/phone/abuse-controls", map[string]any{
		"limits": map[string]any{"perNumberPerHour": 3, "perNumberPerDay": 5, "perDevicePerHour": 5,
			"perAddressPerHour": 1, "codeMinutes": 5, "maxAttempts": 5},
		"mobilePrefixes": []string{"09"},
	}, http.StatusOK)
	if err := send("10.1.1.1", "0332345678"); !errors.Is(err, portal.ErrPhoneInvalid) {
		t.Fatalf("a 03x number with only 09 open: %v", err)
	}
	if err := send("10.1.1.1", "0912345678"); err != nil {
		t.Fatalf("first code: %v", err)
	}
	if err := send("10.1.1.1", "0987654321"); !errors.Is(err, portal.ErrPhoneLimited) {
		t.Fatalf("second code from one address at a limit of one: %v", err)
	}
	var maxAttempts int
	if err := w.owner.QueryRow(ctx, `SELECT max_attempts FROM phone_codes WHERE id = $1`, fake.Last().RequestID).Scan(&maxAttempts); err != nil || maxAttempts != 5 {
		t.Fatalf("the stored code's attempts: %d %v", maxAttempts, err)
	}

	// The deployment cap: one more message today in total, counted in SQL.
	w.do(http.MethodPut, "/phone-sign-in/spend-cap", map[string]any{"dailyLimit": 2}, http.StatusOK)
	if err := send("10.1.1.2", "0987654321"); err != nil {
		t.Fatalf("second message under a cap of two: %v", err)
	}
	var res struct {
		Outcome   string `json:"outcome"`
		SentToday int    `json:"sentToday"`
	}
	raw := w.do(http.MethodPost, "/sites/"+w.siteA+"/portal/phone/test-message", map[string]any{"phone": "0912345678"}, http.StatusOK)
	_ = json.Unmarshal(raw, &res)
	if res.Outcome != "ceiling-reached" || res.SentToday != 2 {
		t.Fatalf("a test message past the deployment cap: %s", raw)
	}
	w.do(http.MethodPut, "/phone-sign-in/spend-cap", map[string]any{"dailyLimit": nil}, http.StatusOK)
	raw = w.do(http.MethodPost, "/sites/"+w.siteA+"/portal/phone/test-message", map[string]any{"phone": "0912345678"}, http.StatusOK)
	_ = json.Unmarshal(raw, &res)
	if res.Outcome != "sent" || res.SentToday != 3 || len(fake.Sent()) != 3 {
		t.Fatalf("a test message with the cap cleared: %s (%d sent)", raw, len(fake.Sent()))
	}

	var p struct {
		SentToday int `json:"sentToday"`
	}
	_ = json.Unmarshal(w.do(http.MethodGet, "/sites/"+w.siteA+"/portal/phone", nil, http.StatusOK), &p)
	if p.SentToday != 3 {
		t.Fatalf("sentToday on the site's calendar: %d, want 3", p.SentToday)
	}
	w.do(http.MethodPut, "/sites/"+w.siteA+"/time-zone", map[string]any{"timeZone": "Pacific/Pago_Pago"}, http.StatusOK)
	_ = json.Unmarshal(w.do(http.MethodGet, "/sites/"+w.siteA+"/portal/phone", nil, http.StatusOK), &p)
	if p.SentToday != 0 {
		t.Fatalf("sentToday after the zone moved a day: %d — the cached zone outlived the write", p.SentToday)
	}
	var zone string
	if err := w.owner.QueryRow(ctx, `SELECT time_zone FROM sites WHERE id = $1`, w.siteA).Scan(&zone); err != nil || zone != "Pacific/Pago_Pago" {
		t.Fatalf("stored zone %q %v", zone, err)
	}
}
