package aaa

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A voucher over RADIUS is claimed at the site the request arrived at.
//
// A claim that carried no site would let a code printed for venue A be accepted
// by venue B's access points. These are the Go half; internal/store's
// TestRADIUSVoucherIsClaimedOnlyAtItsOwnSite is the SQL half, and lab/policy's
// credentials_test.go drives both through a real FreeRADIUS.

const (
	siteA   = "site-a"
	siteB   = "site-b"
	clientA = "10.0.1.2"
	clientB = "10.0.2.2"
)

func siteBoundStore() *mockStore {
	return &mockStore{
		vouchers: map[string]*VoucherInfo{
			"CODE-A": {Code: "CODE-A", State: "unused", DurationMinutes: 60, SiteID: siteA},
		},
		sites:     map[string]string{clientA: siteA, clientB: siteB},
		singleUse: true,
	}
}

func TestAVoucherFromOneSiteIsRefusedAtAnother(t *testing.T) {
	st := siteBoundStore()
	e := NewEngine(st, 300)

	dec, err := e.Authorize(context.Background(), Request{UserName: "CODE-A", ClientIP: clientB})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if dec.Allow {
		t.Fatal("a voucher sold at site A was accepted by a RADIUS client at site B")
	}
	if dec.Reason != "identity not found or not authorized" {
		t.Errorf("reason = %q: a foreign code must read exactly like a code that does not exist, "+
			"or site B's NAS learns which of site A's codes are live", dec.Reason)
	}
	if st.claims != 0 {
		t.Errorf("the foreign voucher was claimed %d time(s); site B spent site A's code", st.claims)
	}

	// And the same code at its own site is still good — the refusal above did not
	// consume it.
	dec, err = e.Authorize(context.Background(), Request{UserName: "CODE-A", ClientIP: clientA})
	if err != nil || !dec.Allow {
		t.Fatalf("the voucher was refused at its own site: %+v, %v", dec, err)
	}
}

func TestAVoucherFromAClientBoundToNoSiteIsRefused(t *testing.T) {
	for name, ip := range map[string]string{
		"an address no client covers": "192.0.2.99",
		"no source address at all":    "",
	} {
		t.Run(name, func(t *testing.T) {
			st := siteBoundStore()
			dec, err := NewEngine(st, 300).Authorize(context.Background(), Request{UserName: "CODE-A", ClientIP: ip})
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if dec.Allow || st.claims != 0 {
				t.Fatalf("a request with no venue was allowed (%+v) or claimed (%d): it can only have been "+
					"claimed at every venue", dec, st.claims)
			}
		})
	}
}

// The source address travels as `client_ip`, which is what the generated rest
// module sends (internal/raddb's TestTheRestModuleSendsThePacketSource).
func TestTheHandlerReadsTheClientAddressFromTheWire(t *testing.T) {
	st := siteBoundStore()
	h := NewEngine(st, 300).Handler(HandlerOptions{})
	// The positive direction, because a lost field and a foreign site both refuse:
	// only an accept at the voucher's own site proves the address arrived.
	body, _ := json.Marshal(map[string]string{"user_name_hex": "434f44452d41", "client_ip": clientA})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/aaa/authorize", strings.NewReader(string(body))))
	if st.claims != 1 {
		t.Fatalf("status %d, %d claim(s): the handler did not carry client_ip to the engine, so a "+
			"voucher at its own site was refused", rec.Code, st.claims)
	}
}
