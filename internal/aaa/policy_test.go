package aaa

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type mockStore struct {
	vouchers map[string]*VoucherInfo
	accounts map[string]*GuestAccountInfo

	// singleUse makes ClaimVoucher behave like the real statement: an `unused`
	// voucher becomes `in-use` and a second claim finds nothing. Off by default,
	// so the fixtures that authorize one voucher many times over (the identity
	// and vendor-profile tests) keep testing what they are about.
	singleUse bool
	mu        sync.Mutex
	claims    int

	// sites maps a RADIUS client's address to its venue. Nil places every
	// client at mockSite, so the tests that are not about site binding need not
	// say where they are; a non-nil map answers only the addresses it holds.
	sites map[string]string
}

const mockSite = "site-mock"

func (m *mockStore) RADIUSClientSite(_ context.Context, clientIP string) (string, error) {
	if m.sites == nil {
		return mockSite, nil
	}
	if s, ok := m.sites[clientIP]; ok {
		return s, nil
	}
	return "", ErrIdentityNotFound
}

func (m *mockStore) LookupVoucher(ctx context.Context, code string) (*VoucherInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.vouchers[code]
	if v == nil {
		return nil, nil
	}
	c := *v
	return &c, nil
}

func (m *mockStore) ClaimVoucher(ctx context.Context, site, code string, at time.Time) (*VoucherInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v := m.vouchers[code]
	if v == nil || v.State != "unused" || (v.SiteID != "" && v.SiteID != site) {
		return nil, ErrIdentityNotFound
	}
	m.claims++
	c := *v
	c.State = "in-use"
	if m.singleUse {
		v.State = "in-use"
	}
	return &c, nil
}

func (m *mockStore) LookupGuestAccount(ctx context.Context, username string) (*GuestAccountInfo, error) {
	return m.accounts[username], nil
}

func (m *mockStore) IsMACAllowed(ctx context.Context, mac string) (bool, error) {
	return true, nil
}

func TestEngine_AuthorizeVoucher(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	past := time.Now().Add(-24 * time.Hour)

	store := &mockStore{
		vouchers: map[string]*VoucherInfo{
			"VALID-123": {
				Code:            "VALID-123",
				State:           "unused",
				DurationMinutes: 60,
				RxRateKbps:      5000,
				TxRateKbps:      2000,
				ExpiresAt:       &future,
			},
			"REVOKED-999": {
				Code:  "REVOKED-999",
				State: "revoked",
			},
			"EXPIRED-000": {
				Code:      "EXPIRED-000",
				State:     "expired",
				ExpiresAt: &past,
			},
		},
		accounts: map[string]*GuestAccountInfo{},
	}

	engine := NewEngine(store, 300)

	// Case 1: Valid voucher
	dec, err := engine.Authorize(context.Background(), Request{UserName: "VALID-123"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !dec.Allow {
		t.Errorf("expected VALID-123 to be allowed, got: %s", dec.Reason)
	}
	if dec.ReplyAttributes["Session-Timeout"] != "3600" {
		t.Errorf("expected Session-Timeout 3600, got: %s", dec.ReplyAttributes["Session-Timeout"])
	}
	if dec.ReplyAttributes["Acct-Interim-Interval"] != "300" {
		t.Errorf("expected Acct-Interim-Interval 300, got: %s", dec.ReplyAttributes["Acct-Interim-Interval"])
	}

	// Case 2: Revoked voucher
	dec, err = engine.Authorize(context.Background(), Request{UserName: "REVOKED-999"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Allow {
		t.Errorf("expected REVOKED-999 to be rejected")
	}

	// Case 3: Expired voucher
	dec, err = engine.Authorize(context.Background(), Request{UserName: "EXPIRED-000"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.Allow {
		t.Errorf("expected EXPIRED-000 to be rejected")
	}
}

func TestEngine_HTTPHandler_SecurityInvariants(t *testing.T) {
	store := &mockStore{
		vouchers: map[string]*VoucherInfo{
			"GUEST-VOUCHER": {
				Code:            "GUEST-VOUCHER",
				State:           "unused",
				DurationMinutes: 120,
			},
		},
		accounts: map[string]*GuestAccountInfo{},
	}

	engine := NewEngine(store, 300)
	handler := engine.HTTPHandler()

	reqBody, _ := json.Marshal(Request{
		UserName:         "GUEST-VOUCHER",
		CallingStationID: "00:11:22:33:44:55",
	})
	httpReq := httptest.NewRequest(http.MethodPost, "/api/v1/aaa/authorize", bytes.NewReader(reqBody))
	httpReq.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler(w, httpReq)

	if w.Code != http.StatusOK {
		t.Fatalf("expected HTTP 200, got: %d", w.Code)
	}

	// Invariant: never return credential material.
	//
	// This loop was `bytes.Contains(bytes.ToLower(body), []byte(s))` over a list
	// whose first and most important entry was "Password" -- capital P, compared
	// against a lower-cased body, so the one substring the rule is actually about
	// could never match. Three of the four entries could fire and the fourth was
	// dead. It is the seventh mechanism in this repo that looked like a check.
	//
	// The list is lower-cased here so every entry can fire; the gate that does the
	// real work is in reply.go, and reply_test.go is where it is proven by making
	// it fail.
	bodyStr := strings.ToLower(w.Body.String())
	forbiddenStrings := []string{"password", "secret", "hash", "argon2", "credential"}
	for _, s := range forbiddenStrings {
		if strings.Contains(bodyStr, s) {
			t.Errorf("SECURITY INVARIANT VIOLATION: HTTP response contains sensitive substring %q: %s", s, w.Body.String())
		}
	}
}

// TestTheForbiddenSubstringListCanActuallyFire.
//
// The loop above is a negative assertion over a hardcoded list, which is the shape
// that hid the capital-P bug for as long as it existed: a list entry that can never
// match is indistinguishable from a list entry that never matched. So the list is
// run once against a body that does contain each term, and every entry has to be
// found.
func TestTheForbiddenSubstringListCanActuallyFire(t *testing.T) {
	body := strings.ToLower(`{"Password":"x","Client-Secret":"y","NT-Hash":"z","argon2id":"w","credential":"v"}`)
	for _, s := range []string{"password", "secret", "hash", "argon2", "credential"} {
		if !strings.Contains(body, s) {
			t.Errorf("the forbidden substring %q cannot match anything, so its place in the list proves nothing", s)
		}
	}
}
