package apistore_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/identity/sms"
	"github.com/kiennt048/foyer/internal/portal"
)

// A code whose send failed is retired in PostgreSQL, not only in the in-memory
// store: a no-op Retire in the adapter would leave every other test green, because
// TestAFailedSendRetiresTheCode runs against `otp.Memory`. Without the retire, a guest (or anybody holding the browser's
// session) could spend five guesses on a code that never reached a phone.
func TestAFailedSendRetiresTheCodeInPostgres(t *testing.T) {
	fake := &sms.Fake{Fail: sms.ErrRefused}
	w := newWorldWith(t, func(c *apistore.Config) {
		c.SMSProvider = func(sms.Config) (sms.Provider, error) { return fake, nil }
	})
	w.signIn()
	ctx := context.Background()
	w.do(http.MethodPut, "/sites/"+w.siteA+"/portal/phone", map[string]any{
		"provider": "esms-sms", "brandname": "FOYER", "dailyLimit": 10, "sessionMinutes": 45,
		"messageTemplate": "{code} la ma Wi-Fi", "apiKey": "K", "secretKey": "S",
	}, http.StatusOK)

	codes := w.adapter.PortalPorts().PhoneCodes
	req := portal.PhoneCodeRequest{SiteID: w.siteA, SessionTokenHash: uniq("tok"), ClientMAC: "aa:bb:cc:00:11:30",
		ClientIP: "10.9.9.30", Phone: "0912 345 678"}
	if err := codes.Send(ctx, req); !errors.Is(err, portal.ErrPhoneCannotSend) {
		t.Fatalf("a refused send: %v", err)
	}
	var live int
	if err := w.owner.QueryRow(ctx,
		`SELECT count(*) FROM phone_codes WHERE session_token_hash = $1 AND consumed_at IS NULL`,
		req.SessionTokenHash).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 0 {
		t.Fatalf("%d live codes after a send the provider refused", live)
	}
	if _, err := codes.Check(ctx, req.SessionTokenHash, "000000"); !errors.Is(err, portal.ErrCodeExpired) {
		t.Fatalf("a guess at a code that never arrived was compared: %v", err)
	}
}
