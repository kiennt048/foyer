package portal

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// tlsState stands in for a real handshake. Only its presence is read.
var tlsState = tls.ConnectionState{}

// The front door: where a MAC is learned, and the only place it is.

func entryRig(t *testing.T) *rig {
	t.Helper()
	return newRig(t, present(), nil)
}

func get(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, target, nil)
}

// TestTheSessionIsBoundToTheMACTheNASNamed.
func TestTheSessionIsBoundToTheMACTheNASNamed(t *testing.T) {
	r := entryRig(t)
	w := httptest.NewRecorder()

	sess, err := r.svc.Resolve(context.Background(), w,
		get("/?ap=11:22:33:44:55:66&id=AA-BB-CC-DD-EE-FF&t=1&ssid=Cafe&url=http%3A%2F%2Fexample.test%2F"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if sess.ClientMAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("client MAC is %q — vendors spell an address three ways and they are one address", sess.ClientMAC)
	}
	if sess.APMAC != "11:22:33:44:55:66" {
		t.Errorf("AP MAC is %q", sess.APMAC)
	}
	if sess.OriginalURL != "http://example.test/" {
		t.Errorf("original URL is %q", sess.OriginalURL)
	}
	if sess.ExpiresAt.IsZero() || !sess.ExpiresAt.After(sess.CreatedAt) {
		t.Error("the session has no bound — a pre-authentication row with no identity behind it must have one")
	}

	// The cookie is set, and it carries nothing that is stored.
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != SessionCookieName {
		t.Fatalf("cookies: %+v", cookies)
	}
	c := cookies[0]
	if !c.HttpOnly {
		t.Error("the portal session cookie is readable from script")
	}
	if c.MaxAge != 0 || !c.Expires.IsZero() {
		t.Error("the cookie asks to persist — the CNA destroys its jar on close regardless, " +
			"so this would be a promise to a client that cannot keep it")
	}
	if HashToken(c.Value) != sess.TokenHash {
		t.Error("the stored value is not the hash of the cookie — a database dump would contain live sessions")
	}
	if strings.Contains(c.Value, sess.ClientMAC) {
		t.Error("the cookie value carries the MAC it names")
	}
}

// TestAMidFlowMACChangeNeverRePointsTheBinding.
//
// §4 says to reject one. **What is rejected is precisely this**: an existing
// binding is never re-pointed at a different MAC. The old session is forgotten and
// a new one is minted, which then has to pass `VerifyPresence` on its own like any
// other.
//
// The narrow reading is the true one and the wide one would be a claim this cannot
// support: nothing stops somebody deleting their own cookie and starting again. It
// is not a barrier to *trying* a second MAC. It prevents a grant issued after a
// presence check on one address from being carried over to another, which is the
// only thing here that would actually be a vulnerability.
func TestAMidFlowMACChangeNeverRePointsTheBinding(t *testing.T) {
	r := entryRig(t)
	ctx := context.Background()

	first := httptest.NewRecorder()
	a, err := r.svc.Resolve(ctx, first, get("/?ap=11:22:33:44:55:66&id=aa:bb:cc:dd:ee:ff"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Give that browser a grant, so that re-pointing would carry something.
	if err := r.store.CreateGrant(ctx, Grant{ID: "grant-1", ClientMAC: a.ClientMAC}); err != nil {
		t.Fatalf("CreateGrant: %v", err)
	}
	if err := r.store.AttachGrant(ctx, a.TokenHash, "grant-1"); err != nil {
		t.Fatalf("AttachGrant: %v", err)
	}
	token := first.Result().Cookies()[0]

	// The same browser, now claiming a different MAC.
	second := httptest.NewRecorder()
	req := get("/?ap=11:22:33:44:55:66&id=99:88:77:66:55:44")
	req.AddCookie(token)
	b, err := r.svc.Resolve(ctx, second, req)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if b.TokenHash == a.TokenHash {
		t.Fatal("the same session answered for two different MACs — the binding was re-pointed")
	}
	if b.ClientMAC != "99:88:77:66:55:44" {
		t.Errorf("the new session's MAC is %q", b.ClientMAC)
	}
	if b.GrantID != "" {
		t.Error("the new MAC inherited the old MAC's grant — this is the whole failure §4 names")
	}
	// The old row is gone rather than left behind for the old cookie to find.
	if _, err := r.store.Session(ctx, a.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Error("the disputed session survived")
	}
}

// TestASessionSurvivesARequestCarryingTheSameMAC.
//
// The CNA re-probes the portal repeatedly while a guest reads a page, and every
// one of those probes carries the NAS parameters again. A binding that reset on
// each of them would mint a row per probe and lose the grant on every navigation.
func TestASessionSurvivesARequestCarryingTheSameMAC(t *testing.T) {
	r := entryRig(t)
	ctx := context.Background()

	w := httptest.NewRecorder()
	a, _ := r.svc.Resolve(ctx, w, get("/?ap=11:22:33:44:55:66&id=aa:bb:cc:dd:ee:ff"))
	token := w.Result().Cookies()[0]

	for _, target := range []string{
		"/?ap=11:22:33:44:55:66&id=aa:bb:cc:dd:ee:ff",
		"/?ap=11:22:33:44:55:66&id=AA:BB:CC:DD:EE:FF",
		"/api/v0/portal/status",
	} {
		req := get(target)
		req.AddCookie(token)
		b, err := r.svc.Resolve(ctx, httptest.NewRecorder(), req)
		if err != nil {
			t.Fatalf("%s: %v", target, err)
		}
		if b.TokenHash != a.TokenHash {
			t.Errorf("%s: minted a new session for the same browser and the same MAC", target)
		}
	}
}

// TestNoNASParametersAndNoCookieIsNotASession.
//
// Somebody typed the venue's address into a browser. There is no MAC, so there is
// nothing a driver could confirm, and the honest answer is that this request names
// no device — not a default one.
func TestNoNASParametersAndNoCookieIsNotASession(t *testing.T) {
	r := entryRig(t)
	_, err := r.svc.Resolve(context.Background(), httptest.NewRecorder(), get("/"))
	if !errors.Is(err, ErrNoClaim) {
		t.Errorf("err is %v, want ErrNoClaim", err)
	}
}

// TestAMalformedMACIsNoMACAtAll.
//
// A truncated address that still looks like an address is a lookup against the
// wrong device. `NormaliseMAC` answers "" rather than a partial one, and the
// caller then has nothing to bind.
func TestAMalformedMACIsNoMACAtAll(t *testing.T) {
	for _, raw := range []string{
		"", "aa:bb:cc:dd:ee", "aa:bb:cc:dd:ee:ff:00", "zz:zz:zz:zz:zz:zz",
		"not-a-mac", "aabbccddee", "../../etc/passwd",
	} {
		if got := NormaliseMAC(raw); got != "" {
			t.Errorf("NormaliseMAC(%q) = %q", raw, got)
		}
	}
	for _, raw := range []string{
		"aa:bb:cc:dd:ee:ff", "AA-BB-CC-DD-EE-FF", "aabb.ccdd.eeff", "AABBCCDDEEFF",
	} {
		if got := NormaliseMAC(raw); got != "aa:bb:cc:dd:ee:ff" {
			t.Errorf("NormaliseMAC(%q) = %q", raw, got)
		}
	}
}

// TestAHostileOriginalURLNeverEntersTheStore.
//
// Allowlisted on the way *in*, not only on the way out. A value that never enters
// the store in a dangerous shape cannot be rendered in one by a future reader who
// forgets to check — which is the failure mode a validator applied only at the
// render site has.
func TestAHostileOriginalURLNeverEntersTheStore(t *testing.T) {
	r := entryRig(t)
	for _, raw := range []string{
		"javascript:alert(1)",
		"//evil.example",
		"http://portal.example.test@evil.example",
		"data:text/html,x",
	} {
		w := httptest.NewRecorder()
		sess, err := r.svc.Resolve(context.Background(), w,
			get("/?ap=11:22:33:44:55:66&id=aa:bb:cc:dd:ee:ff&url="+raw))
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if sess.OriginalURL != "" {
			t.Errorf("stored %q for a submitted %q", sess.OriginalURL, raw)
		}
	}
}

// TestAnExpiredSessionIsNotASession.
//
// The pre-authentication time limit, enforced on read rather than by a timer —
// the same reason `Grant.StateAt` derives rather than stores.
func TestAnExpiredSessionIsNotASession(t *testing.T) {
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	at := now
	store.Now = func() time.Time { return at }

	ctx := context.Background()
	if err := store.CreateSession(ctx, Session{TokenHash: "h", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := store.Session(ctx, "h"); err != nil {
		t.Fatalf("a live session did not resolve: %v", err)
	}
	at = now.Add(2 * time.Minute)
	if _, err := store.Session(ctx, "h"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an expired session resolved: %v", err)
	}
}

// TestTheEntryHandlerRelaysTheQueryAndAnswersADocument.
//
// It negotiates a language and redirects. **It does not call the driver**: the CNA
// probes this address repeatedly while a guest reads, and a controller round trip
// per probe is a denial of service the venue pays for.
func TestTheEntryHandlerRelaysTheQueryAndAnswersADocument(t *testing.T) {
	r := entryRig(t)
	h := r.svc.EntryHandler([]string{"en", "vi"}, "en")

	w := httptest.NewRecorder()
	req := get("/?ap=11:22:33:44:55:66&id=aa:bb:cc:dd:ee:ff&url=http%3A%2F%2Fexample.test%2F")
	req.Header.Set("Accept-Language", "vi-VN,vi;q=0.9,en;q=0.8")
	h(w, req)

	res := w.Result()
	if res.StatusCode != http.StatusFound {
		t.Errorf("status is %d, want 302", res.StatusCode)
	}
	loc := res.Header.Get("Location")
	if !strings.HasPrefix(loc, "/vi/?") {
		t.Errorf("Location is %q — the language was not negotiated or the query was dropped", loc)
	}
	if !strings.Contains(loc, "ap=11") {
		t.Errorf("Location is %q — the NAS parameters were not relayed", loc)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Error("the front door is cacheable — a CNA that cached it would never re-probe")
	}
	if r.driver.calls() != 0 {
		t.Error("the front door called the driver")
	}

	// A browser with no NAS parameters still gets a document rather than a bare
	// refusal. Somebody typed the venue's address; the portal tells them on the
	// page, not with a 403.
	w = httptest.NewRecorder()
	h(w, get("/"))
	if w.Result().StatusCode != http.StatusFound {
		t.Errorf("a bare visit answered %d", w.Result().StatusCode)
	}
}

// TestSecureFollowsTheConnection.
//
// A captive portal is regularly reached over plain HTTP: the guest has no route
// to a certificate authority, and whether the iOS CNA accepts a self-signed
// certificate at all is an open question. `Secure` set unconditionally
// would produce a portal that works in a browser and silently drops every cookie
// on a phone — the failure mode this project is least able to see.
func TestSecureFollowsTheConnection(t *testing.T) {
	r := entryRig(t)

	plain := httptest.NewRecorder()
	if _, err := r.svc.Resolve(context.Background(), plain, get("/?ap=11:22:33:44:55:66&id=aa:bb:cc:dd:ee:ff")); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if plain.Result().Cookies()[0].Secure {
		t.Error("Secure over plain HTTP — the user agent drops the cookie and the flow cannot complete")
	}

	secure := httptest.NewRecorder()
	req := get("https://portal.example.test/?ap=11:22:33:44:55:66&id=99:88:77:66:55:44")
	req.TLS = &tlsState
	if _, err := r.svc.Resolve(context.Background(), secure, req); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !secure.Result().Cookies()[0].Secure {
		t.Error("no Secure over TLS")
	}
}

// TestLookupReadsWithoutMinting — the document path's contract.
//
// Lookup answers the session the cookie names and mints nothing: no cookie, no
// claim, an expired row and a MAC-swapped claim are all refusals, and none of
// them writes a row. A GET that minted for every prefetch, preview, forged
// link and crawler would fill the session table with browsers nobody sent —
// which is exactly what `GuestDocumentsHandler` would do through `Resolve`,
// and why it calls this instead.
func TestLookupReadsWithoutMinting(t *testing.T) {
	r := entryRig(t)
	ctx := context.Background()

	if _, err := r.svc.Lookup(ctx, get("/en/")); !errors.Is(err, ErrNoClaim) {
		t.Errorf("no cookie and no claim: %v, want ErrNoClaim", err)
	}
	if _, err := r.svc.Lookup(ctx, get("/?id=aa:bb:cc:dd:ee:ff")); !errors.Is(err, ErrNoClaim) {
		t.Errorf("claim without cookie: %v, want ErrNoClaim — a stranger the front door has not met", err)
	}

	w := httptest.NewRecorder()
	minted, err := r.svc.Resolve(ctx, w, get("/?ap=11:22:33:44:55:66&id=aa:bb:cc:dd:ee:ff"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	token := w.Result().Cookies()[0]

	same, err := r.svc.Lookup(ctx, func() *http.Request {
		req := get("/?ap=11:22:33:44:55:66&id=aa:bb:cc:dd:ee:ff")
		req.AddCookie(token)
		return req
	}())
	if err != nil {
		t.Fatalf("Lookup with cookie: %v", err)
	}
	if same.TokenHash != minted.TokenHash {
		t.Error("Lookup answered a different session than the cookie names")
	}

	// A claim for another MAC resolves to nothing rather than re-pointing —
	// the same refusal Resolve applies, minus the mint that follows it.
	swapped := get("/?ap=11:22:33:44:55:66&id=99:88:77:66:55:44")
	swapped.AddCookie(token)
	if _, err := r.svc.Lookup(ctx, swapped); !errors.Is(err, ErrNoClaim) {
		t.Errorf("MAC-swapped claim: %v, want a refusal rather than a re-point", err)
	}
	if _, err := r.store.Session(ctx, minted.TokenHash); err != nil {
		t.Errorf("the bound session survived Lookup: %v — reading must not disturb", err)
	}
	before := len(r.store.Sessions())
	if _, err := r.svc.Lookup(ctx, get("/?id=99:88:77:66:55:44")); !errors.Is(err, ErrNoClaim) {
		t.Fatalf("forged claim: %v", err)
	}
	if got := len(r.store.Sessions()); got != before {
		t.Errorf("%d sessions after a forged document fetch, was %d — a GET minted a row", got, before)
	}
}
