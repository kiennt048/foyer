package aaa

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Two properties of the Policy API boundary -- an allow names what still has to
// be proven, and a question that can be read two ways is refused -- at the Go
// half of the boundary. The FreeRADIUS half -- that an account allow really does send the
// request through rlm_pap, and that a hostile User-Name really is refused on a
// fail-open site -- is proven against a real 3.2.10 in lab/policy, because no
// test in this package can say what unlang does with an answer.
//
// The tests below name the wire strings as literals rather than through the
// package's constants, deliberately: a red proof reverts the implementation and
// runs these same tests, and a test that no longer compiles against the reverted
// code proves less than one that fails its assertion. internal/raddb's tests compare the
// generated unlang against the same literals, so the two halves still agree
// through them. A renamed constant fails the behavioural assertions below, which read the reply.

// kindAttribute is the attribute name the generated dictionary defines.
const kindAttribute = "Foyer-Identity-Kind"

// TestAnAllowSaysWhatStillHasToBeProven is the Go half of the identity-kind reply.
//
// If an allow for an account and an allow for a voucher were the same reply, the
// virtual server would turn both into `Auth-Type := Accept`: a username would be
// enough. The allow now names its kind, and the generated server
// accepts on its own only for a voucher.
func TestAnAllowSaysWhatStillHasToBeProven(t *testing.T) {
	e := NewEngine(&mockStore{
		vouchers: map[string]*VoucherInfo{"CODE-1": {Code: "CODE-1", State: "unused", DurationMinutes: 60}},
		accounts: map[string]*GuestAccountInfo{"linh": {Username: "linh", Enabled: true, DurationMinutes: 60}},
	}, 300)

	for _, tc := range []struct{ subject, want string }{
		{"CODE-1", "voucher"},
		{"linh", "account"},
	} {
		dec, err := e.Authorize(context.Background(), Request{UserName: tc.subject})
		if err != nil || !dec.Allow {
			t.Fatalf("%s: allow expected, got %+v, %v", tc.subject, dec, err)
		}
		if got := dec.ReplyAttributes[kindAttribute]; got != tc.want {
			t.Errorf("%s: %s = %q, want %q. Without it the virtual server cannot tell an account, "+
				"which still needs its password checked, from a voucher, which does not", tc.subject,
				kindAttribute, got, tc.want)
		}
	}

	// And it survives the reply gate: an attribute the gate refused would turn
	// every allow in the product into a 503.
	if ForbiddenReplyAttribute(kindAttribute) {
		t.Fatalf("the reply gate refuses %s", kindAttribute)
	}
	w := post(t, e.Handler(HandlerOptions{Logger: discardLogger()}), Request{UserName: "linh"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"`+kindAttribute+`":"account"`) {
		t.Errorf("served %d %s; want 200 carrying the kind", w.Code, w.Body.String())
	}
}

// TestAVoucherOverRADIUSIsSingleUse: the portal claims a code once and so must
// this path. The engine cannot enforce single use itself -- the store's one
// statement does -- so what is asserted is that the engine claims, and refuses
// when the claim finds nothing.
func TestAVoucherOverRADIUSIsSingleUse(t *testing.T) {
	store := &mockStore{
		singleUse: true,
		vouchers:  map[string]*VoucherInfo{"ONCE": {Code: "ONCE", State: "unused", DurationMinutes: 60}},
	}
	e := NewEngine(store, 300)

	first, err := e.Authorize(context.Background(), Request{UserName: "ONCE"})
	if err != nil || !first.Allow {
		t.Fatalf("the first use was refused: %+v %v", first, err)
	}
	second, err := e.Authorize(context.Background(), Request{UserName: "ONCE"})
	if err != nil {
		t.Fatalf("a second use produced an error rather than a refusal: %v", err)
	}
	if second.Allow {
		t.Error("a single-use voucher was accepted twice over RADIUS")
	}
	if store.claims != 1 {
		t.Errorf("claims = %d, want 1", store.claims)
	}
}

// TestOnlyAnUnusedVoucherIsClaimable: over RADIUS, a voucher that is in use,
// exhausted, expired, revoked, past its expiry or in an unknown state is refused.
func TestOnlyAnUnusedVoucherIsClaimable(t *testing.T) {
	past := time.Now().Add(-time.Minute)
	vouchers := map[string]*VoucherInfo{
		"IN-USE":    {Code: "IN-USE", State: "in-use", DurationMinutes: 60},
		"EXHAUSTED": {Code: "EXHAUSTED", State: "exhausted", DurationMinutes: 60},
		"EXPIRED":   {Code: "EXPIRED", State: "expired"},
		"REVOKED":   {Code: "REVOKED", State: "revoked"},
		"STALE":     {Code: "STALE", State: "unused", ExpiresAt: &past}, // the sweep has not run yet
		"ODD":       {Code: "ODD", State: "active", DurationMinutes: 60},
	}
	store := &mockStore{vouchers: vouchers}
	e := NewEngine(store, 300)
	for code := range vouchers {
		dec, err := e.Authorize(context.Background(), Request{UserName: code})
		if err != nil {
			t.Fatalf("%s: an error rather than a refusal: %v", code, err)
		}
		if dec.Allow {
			t.Errorf("%s was accepted", code)
		}
	}
	if store.claims != 0 {
		t.Errorf("%d claims were attempted on vouchers that are not claimable", store.claims)
	}
}

// TestAFromCreationExpiryBoundsTheSession: a code claimed ten minutes before it
// expires does not get the plan's hour.
func TestAFromCreationExpiryBoundsTheSession(t *testing.T) {
	exp := time.Now().Add(10 * time.Minute)
	e := NewEngine(&mockStore{vouchers: map[string]*VoucherInfo{
		"SOON": {Code: "SOON", State: "unused", DurationMinutes: 60, ExpiresAt: &exp},
	}}, 300)

	dec, err := e.Authorize(context.Background(), Request{UserName: "SOON"})
	if err != nil || !dec.Allow {
		t.Fatalf("%+v %v", dec, err)
	}
	// 599 or 600: the clock moved between building the voucher and claiming it.
	if got := dec.ReplyAttributes["Session-Timeout"]; got != "600" && got != "599" {
		t.Errorf("Session-Timeout = %s, want 600 -- the code expires in ten minutes", got)
	}
}

type failingClaimStore struct{ *mockStore }

func (failingClaimStore) ClaimVoucher(context.Context, string, string, time.Time) (*VoucherInfo, error) {
	return nil, errors.New("connection reset by peer")
}

// TestAClaimThatCannotBeMadeIsUndecided: the database dying between the read and
// the claim is an outage, not a wrong code.
func TestAClaimThatCannotBeMadeIsUndecided(t *testing.T) {
	e := NewEngine(failingClaimStore{&mockStore{vouchers: map[string]*VoucherInfo{
		"CODE": {Code: "CODE", State: "unused", DurationMinutes: 60},
	}}}, 300)
	if dec, err := e.Authorize(context.Background(), Request{UserName: "CODE"}); err == nil {
		t.Fatalf("a claim that errored produced a decision: %+v", dec)
	}
}

// TestAQuestionThatMeansTwoThingsIsRefused is the Go half of strict request
// decoding. Each body is a way
// for `encoding/json`'s lenient Decoder to read something other than what was
// sent, and each one decodes under the default Decoder.
func TestAQuestionThatMeansTwoThingsIsRefused(t *testing.T) {
	e := NewEngine(&mockStore{vouchers: map[string]*VoucherInfo{
		"GOOD": {Code: "GOOD", State: "unused", DurationMinutes: 60},
	}}, 300)
	h := e.Handler(HandlerOptions{Logger: discardLogger()})

	for name, body := range map[string]string{
		"duplicate key, last wins": `{"user_name":"nobody","user_name":"GOOD"}`,
		// encoding/json matches keys to fields case-insensitively, so these two
		// are user_name to Unmarshal while being different strings to a
		// byte-for-byte duplicate check. Both decode to GOOD without the
		// exact-name check.
		"duplicate key in another case": `{"user_name":"nobody","USER_NAME":"GOOD"}`,
		"a known key in another case":   `{"User_Name":"GOOD"}`,
		"an escaped duplicate key":      `{"user_name":"nobody","user_nam\u0065":"GOOD"}`,
		"trailing object":               `{"user_name":"nobody"} {"user_name":"GOOD"}`,
		"trailing garbage":              `{"user_name":"GOOD"}garbage`,
		"unknown field":                 `{"user_name":"GOOD","site_id":"lobby"}`,
		"invalid UTF-8":                 "{\"user_name\":\"GOOD\xff\"}",
		"a value that is no string":     `{"user_name":"GOOD","nas_ip":{"x":1}}`,
		"not an object":                 `["GOOD"]`,
	} {
		t.Run(name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/aaa/authorize", strings.NewReader(body))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400: the body was read as a question", w.Code)
			}
		})
	}

	// The control: the same handler still answers a well-formed question.
	r := httptest.NewRequest(http.MethodPost, "/api/v1/aaa/authorize", strings.NewReader(`{"user_name":"GOOD"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("a well-formed question got %d: every refusal above proves nothing", w.Code)
	}
}

// TestAnIdentityCarryingControlCharactersIsADenial: a refusal, never an error --
// an error reaches the failure branch, and a fail-open site would let it on. A
// trailing newline is the case that matters most, because the subject is
// TrimSpace'd and "GOOD\n" would otherwise resolve to the real voucher GOOD.
func TestAnIdentityCarryingControlCharactersIsADenial(t *testing.T) {
	e := NewEngine(&mockStore{vouchers: map[string]*VoucherInfo{
		"GOOD": {Code: "GOOD", State: "unused", DurationMinutes: 60},
	}}, 300)
	for _, name := range []string{"GOOD\n", "GO\x00OD", "GOOD\x1b", "GOOD\x7f", "GOOD�"} {
		dec, err := e.Authorize(context.Background(), Request{UserName: name})
		if err != nil {
			t.Fatalf("%q: an error, which reaches the failure branch: %v", name, err)
		}
		if dec.Allow {
			t.Errorf("%q was authorized", name)
		}
	}
}

// TestAnIdentityArrivesAsItsOwnBytes: the generated tree sends user_name and
// outer_user_name as hex, because rlm_rest's jsonquote turns every byte >= 0x80
// into an escaped code point below U+0100 on the amd64 NetworkRADIUS build, and a
// non-ASCII name then arrived as a different name (measured). The hex is the attribute's own bytes; it must decode, be UTF-8,
// and never arrive beside the plain spelling of the same field.
func TestAnIdentityArrivesAsItsOwnBytes(t *testing.T) {
	const name = "nguyễn" // nguyễn; U+1EC5 is E1 BB 85 in UTF-8
	e := NewEngine(&mockStore{
		accounts: map[string]*GuestAccountInfo{name: {Username: name, Enabled: true, DurationMinutes: 60}},
	}, 300)
	h := e.Handler(HandlerOptions{Logger: discardLogger()})
	send := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/aaa/authorize", strings.NewReader(body))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	hexOf := func(s string) string { return hex.EncodeToString([]byte(s)) }

	// What the amd64 build's jsonquote made of the same name -- each byte of
	// U+1EC5 as its own code point -- is not this account. The control for why
	// the tree stopped sending it that way.
	mangled, _ := json.Marshal(map[string]string{"user_name": "nguy" + string([]rune{0xe1, 0xbb, 0x85}) + "n"})
	if w := send(string(mangled)); w.Code == http.StatusOK {
		t.Fatalf("the jsonquote-mangled spelling was authorized: %d %s", w.Code, w.Body.String())
	}
	if w := send(`{"user_name_hex":"` + hexOf(name) + `","outer_user_name_hex":"` + hexOf(name) + `"}`); w.Code != http.StatusOK {
		t.Fatalf("the hex spelling of a real account's name got %d %s; want 200", w.Code, w.Body.String())
	}

	for what, body := range map[string]string{
		"plain and hex together": `{"user_name":"` + name + `","user_name_hex":"` + hexOf(name) + `"}`,
		"outer, both ways":       `{"user_name_hex":"` + hexOf(name) + `","outer_user_name":"x","outer_user_name_hex":"78"}`,
		"not hex":                `{"user_name_hex":"zz"}`,
		"odd length":             `{"user_name_hex":"abc"}`,
		"hex of non-UTF-8":       `{"user_name_hex":"` + hexOf("nguy\xffn") + `"}`,
	} {
		t.Run(what, func(t *testing.T) {
			if w := send(body); w.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400", w.Code)
			}
		})
	}

	// A control byte inside the hex is still the identity refusal, not an error
	// -- an error would reach the failure branch.
	if w := send(`{"user_name_hex":"` + hexOf(name+"\n") + `"}`); w.Code == http.StatusOK || w.Code >= 500 {
		t.Errorf("a hex identity carrying a newline got %d; want a denial", w.Code)
	}
}
