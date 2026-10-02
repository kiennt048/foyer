package api_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
)

// The behavioural suite, driven over HTTP.
//
// It is `package api_test` because it uses `internal/api/memory`, which imports
// `internal/api` — the internal pins live next door in `security_test.go`.

const (
	adminUser  = "kim.administrator"
	staffUser  = "sam.staff"
	testSecret = "correct-horse-battery"
	testSite   = "site-one"
)

// world builds a small, deliberately awkward world.
//
// It is small because these tests are about the API layer; the *ugly* world —
// the 40-day orphan, the wrapping counter, the expired API key — arrives from
// `web/mock` in the differential harness, which is where a fixture set that
// nobody in Go maintains belongs.
func world(t *testing.T) *memory.Store {
	t.Helper()
	w := &memory.World{
		Sites: []gen.Site{
			{Id: testSite, Name: "The Venue", Environment: gen.SiteEnvironmentLive},
		},
		Operators: []gen.Operator{
			{Id: "op-admin", Username: adminUser, Role: gen.Administrator, Enabled: true, CreatedAt: at(-100)},
			{Id: "op-staff", Username: staffUser, Role: gen.Staff, Enabled: true, CreatedAt: at(-90)},
			{Id: "op-off", Username: "olive.disabled", Role: gen.Administrator, Enabled: false, CreatedAt: at(-80)},
		},
		Credentials: map[string]string{
			"op-admin": testSecret,
			"op-staff": testSecret,
			"op-off":   testSecret,
		},
		Nas: []gen.Nas{
			{
				Id: "nas-1", Name: "Front of house", SiteId: testSite,
				Driver: gen.NasDriverKindUnifi, Enabled: true, SecretSet: true,
				Reachability:         gen.NasReachabilityOk,
				MessageAuthenticator: gen.NasMessageAuthenticatorEnforced,
				Source:               gen.Discovered,
				Capabilities:         gen.Capabilities{WalledGarden: gen.GardenKindFqdnSni, RotateSecret: true},
			},
			{
				// The device with no route back: the driver cannot push a secret
				// and nothing can reach it. Rotation is refused here and nowhere
				// else.
				Id: "nas-2", Name: "Back yard", SiteId: testSite,
				Driver: gen.NasDriverKindOmada, Enabled: true, SecretSet: true,
				Reachability:         gen.NasReachabilityUnreachable,
				MessageAuthenticator: gen.NasMessageAuthenticatorRefused,
				Source:               gen.Discovered,
				Capabilities:         gen.Capabilities{WalledGarden: gen.GardenKindNone},
			},
		},
		Garden: map[string]gen.WalledGarden{
			testSite: {
				SiteId:        testSite,
				EffectiveKind: gen.GardenKindNone,
				Drivers:       []gen.GardenDriverCoverage{{NasId: "nas-2", Name: "Back yard", WalledGarden: gen.GardenKindNone}},
				Entries: []gen.GardenEntry{
					{
						Id: "garden-portal", SiteId: testSite, Kind: gen.GardenEntryKindFqdn,
						Value: "portal.example.test", Origin: gen.GardenEntryOriginRequired, CreatedAt: at(-400),
					},
					{
						Id: "garden-maps", SiteId: testSite, Kind: gen.GardenEntryKindFqdn,
						Value: "maps.example.test", Origin: gen.GardenEntryOriginOperator, CreatedAt: at(-12),
					},
				},
			},
		},
		Portal: map[string]gen.PortalSettings{
			testSite: {
				SiteId: testSite, VenueName: "The Venue",
				Languages: []gen.Language{gen.En},
				Methods:   []gen.PortalMethod{gen.PortalMethodVoucher},
			},
		},
	}
	s := memory.NewStore(w)
	s.SetClock(func() time.Time { return at(0) })
	return s
}

func at(days int) time.Time {
	return time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC).AddDate(0, 0, days)
}

type rig struct {
	t      *testing.T
	server *httptest.Server
	store  *memory.Store
	csrf   string
	cookie *http.Cookie
}

// freshRig is a deployment that has been migrated and first-booted and nothing
// more: operators who can sign in, and **no sites**.
//
// It exists because every other world here starts with `testSite`, so every drive
// begins after the step that creates the first site, and *nothing in this
// repository could see* that step being unreachable. And with one venue per
// install, `createSite` refuses when a site already exists — which turns that
// blind spot into a world where the `201` cannot be reached at all.
//
// So the one-venue rule does not need the site-creation test weakened. It needs
// the world a real install starts in.
//
// The operators are kept, because `deploy/first-boot.sh` creates the administrator
// before anything else and a deployment nobody can sign in to could not reach
// `createSite` to test either branch.
func freshRig(t *testing.T) *rig {
	t.Helper()
	w := &memory.World{
		Operators: []gen.Operator{
			{Id: "op-admin", Username: adminUser, Role: gen.Administrator, Enabled: true, CreatedAt: at(-100)},
		},
		Credentials: map[string]string{"op-admin": testSecret},
	}
	store := memory.NewStore(w)
	srv, err := api.New(api.Config{Ports: store.Ports(), Now: func() time.Time { return at(0) }})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &rig{t: t, server: ts, store: store}
}

func newRig(t *testing.T) *rig {
	t.Helper()
	store := world(t)
	srv, err := api.New(api.Config{
		Ports: store.Ports(),
		Now:   func() time.Time { return at(0) },
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &rig{t: t, server: ts, store: store}
}

// do sends one request. It attaches the session cookie and the CSRF token when
// the rig has signed in, and never otherwise.
func (r *rig) do(method, path string, body any) *http.Response {
	r.t.Helper()
	var rdr io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			r.t.Fatalf("marshal: %v", err)
		}
		rdr = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, r.server.URL+api.BasePath+path, rdr)
	if err != nil {
		r.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.cookie != nil {
		req.AddCookie(r.cookie)
	}
	if r.csrf != "" {
		req.Header.Set("X-Foyer-CSRF", r.csrf)
	}
	resp, err := r.server.Client().Do(req)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func (r *rig) signIn(username string) {
	r.t.Helper()
	resp := r.do(http.MethodPost, "/auth/session", map[string]string{
		"username": username, "password": testSecret,
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.t.Fatalf("sign in as %s: %d %s", username, resp.StatusCode, readAll(r.t, resp))
	}
	var out gen.AuthSession
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		r.t.Fatalf("decode AuthSession: %v", err)
	}
	for _, c := range resp.Cookies() {
		if strings.HasSuffix(c.Name, "foyer_admin_session") && c.Value != "" {
			r.cookie = c
		}
	}
	if r.cookie == nil {
		r.t.Fatal("sign in set no session cookie")
	}
	r.csrf = out.CsrfToken
	if r.csrf == "" {
		r.t.Fatal("sign in issued no CSRF token — every state-changing request would then be refused")
	}
}

func readAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

// ── the headline guarantee ───────────────────────────────────────────────────

// TestUnauthenticatedRequestToAProtectedOperationIs401 is the test the whole
// rewrite is for.
//
// The code this replaced ended its auth middleware with `next.ServeHTTP(w, r)`
// when no session resolved, so **every endpoint was open**. This drives every
// operation the contract does not mark `security: []`, with no cookie, and
// requires 401.
//
// **The operation list is computed from the embedded contract, never typed
// here.** A hardcoded list is a list somebody forgets to extend, and the
// forgotten entry is precisely the endpoint that stays open. `signOut` is the
// single exception and the contract states it: sign-me-out has one correct
// outcome, and a client that has just been told 401 by something else must still
// be able to reach it.
//
// Restoring the fall-through — `return f(ctx, w, r, request)` in place of the
// `notSignedIn` branch in `Server.gate` — turns every driven operation red but
// `changeOwnPassword`, which refuses inside its own handler; every other
// protected operation answers 200, 404 or 422 to a request with no session.
func TestUnauthenticatedRequestToAProtectedOperationIs401(t *testing.T) {
	r := newRig(t)

	protected := protectedOperations(t)
	if len(protected) == 0 {
		t.Fatal("computed an empty set of protected operations — the derivation is vacuous")
	}

	checked := 0
	for _, op := range protected {
		if op.id == "SignOut" {
			continue
		}
		resp := r.do(op.method, op.path, op.body)
		body := readAll(t, resp)
		resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s (%s): got %d, want 401\n  %s", op.method, op.path, op.id, resp.StatusCode, body)
			continue
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/problem+json") {
			t.Errorf("%s %s: content-type %q, want application/problem+json", op.method, op.path, ct)
		}
		var p gen.Problem
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Errorf("%s %s: body is not a problem document: %v", op.method, op.path, err)
			continue
		}
		if p.Type != gen.ProblemsnotSignedIn {
			t.Errorf("%s %s: type %q, want /problems/not-signed-in", op.method, op.path, p.Type)
		}
		checked++
	}
	t.Logf("drove %d protected operations with no session", checked)
}

// TestSignOutIsReachableWithoutASession is the exception, asserted rather than
// assumed — otherwise the test above would be satisfied by a server that also
// refuses the one operation the contract says must always answer.
func TestSignOutIsReachableWithoutASession(t *testing.T) {
	r := newRig(t)
	resp := r.do(http.MethodDelete, "/auth/session", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("got %d, want 204: %s", resp.StatusCode, readAll(t, resp))
	}
}

// TestGetAuthSessionAnswers401 — the contract is explicit that a
// client's handling of this is to route, not to render an error.
func TestGetAuthSessionAnswers401(t *testing.T) {
	r := newRig(t)
	resp := r.do(http.MethodGet, "/auth/session", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("got %d, want 401", resp.StatusCode)
	}
	var p gen.Problem
	if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Type != gen.ProblemsnotSignedIn {
		t.Errorf("type %q, want /problems/not-signed-in", p.Type)
	}
}

// ── role and CSRF ────────────────────────────────────────────────────────────

// TestStaffIsRefusedNasManage drives the second half of the gate: a session that
// exists but may not do this. The client's answer to 403 is different from its
// answer to 401 — signing in again does not help — which is why they are
// distinct problem types rather than one refusal.
func TestStaffIsRefusedNasManage(t *testing.T) {
	r := newRig(t)
	r.signIn(staffUser)

	resp := r.do(http.MethodPost, "/nas/nas-1/secret", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("got %d, want 403: %s", resp.StatusCode, readAll(t, resp))
	}
	var p gen.Problem
	_ = json.NewDecoder(resp.Body).Decode(&p)
	if p.Type != gen.Problemsforbidden {
		t.Errorf("type %q, want /problems/forbidden", p.Type)
	}

	// And the same operator may read, which is the narrow reading the contract
	// argues for. A test that only asserted the refusal would be satisfied by a
	// server that refused staff everything.
	ok := r.do(http.MethodGet, "/nas?siteId="+testSite, nil)
	defer ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Errorf("staff reading /nas: got %d, want 200", ok.StatusCode)
	}
}

// TestStateChangingRequestWithoutCSRFIsRefused.
//
// `SameSite=Lax` is the first defence and this is the second. Neither is
// sufficient alone: Lax still permits a top-level POST navigation in some
// browsers, and a token that is never checked is a header. `web/mock` issues the
// token and does not check it — this is the first place the rule is enforced.
func TestStateChangingRequestWithoutCSRFIsRefused(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	token := r.csrf
	r.csrf = ""
	resp := r.do(http.MethodPost, "/nas/nas-1/secret", nil)
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without the token: got %d, want 403\n%s", resp.StatusCode, body)
	}

	r.csrf = "not-the-token"
	wrong := r.do(http.MethodPost, "/nas/nas-1/secret", nil)
	wrong.Body.Close()
	if wrong.StatusCode != http.StatusForbidden {
		t.Fatalf("with a wrong token: got %d, want 403", wrong.StatusCode)
	}

	// With it, the same request succeeds — so the refusal above is about the
	// token and not about the request being unacceptable for some other reason.
	r.csrf = token
	good := r.do(http.MethodPost, "/nas/nas-1/secret", nil)
	defer good.Body.Close()
	if good.StatusCode != http.StatusOK {
		t.Fatalf("with the token: got %d, want 200: %s", good.StatusCode, readAll(t, good))
	}
}

// TestReadsDoNotNeedCSRF — a GET that demanded a header would break every
// client that navigates to a page, and the rule follows from the method.
func TestReadsDoNotNeedCSRF(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	r.csrf = ""
	resp := r.do(http.MethodGet, "/sites", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got %d, want 200", resp.StatusCode)
	}
}

// ── sign-in ──────────────────────────────────────────────────────────────────

// TestSignInDoesNotSayWhichWasWrong.
//
// Unknown username, wrong password and disabled operator produce byte-identical
// refusals. An answer that distinguished them would make "is there an account
// for this person" a fact anyone can confirm from the sign-in form, and would
// make disabling an account publish its existence.
func TestSignInDoesNotSayWhichWasWrong(t *testing.T) {
	r := newRig(t)

	bodies := map[string]string{}
	for name, creds := range map[string]map[string]string{
		"unknown username": {"username": "nobody.at.all", "password": testSecret},
		"wrong password":   {"username": adminUser, "password": "not-the-password"},
		"disabled":         {"username": "olive.disabled", "password": testSecret},
	} {
		resp := r.do(http.MethodPost, "/auth/session", creds)
		body := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s: got %d, want 401", name, resp.StatusCode)
		}
		bodies[name] = body
	}
	var seen string
	for name, body := range bodies {
		if seen == "" {
			seen = body
			continue
		}
		if body != seen {
			t.Errorf("%s answered differently from another refusal:\n  %s\n  %s", name, body, seen)
		}
	}
}

// TestNothingEchoesASubmittedCredential is the Go half of the rule `web/mock`
// already carries on `AuthAttempt`.
//
// It drives every response this rig can reach — including the refusals, which is
// where a careless implementation puts the submitted value to be helpful — and
// fails on any key or value that looks like a credential.
func TestNothingEchoesASubmittedCredential(t *testing.T) {
	r := newRig(t)
	const secret = "hunter2-hunter2-hunter2"

	probes := []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/auth/session", map[string]string{"username": adminUser, "password": secret}},
		{http.MethodPost, "/auth/session", map[string]string{"username": "", "password": secret}},
	}
	for _, p := range probes {
		resp := r.do(p.method, p.path, p.body)
		body := readAll(t, resp)
		resp.Body.Close()
		if strings.Contains(body, secret) {
			t.Errorf("%s %s (%d) echoed the submitted password:\n%s", p.method, p.path, resp.StatusCode, body)
		}
		if keyLooksLikeACredential(body) {
			t.Errorf("%s %s (%d) carries a credential-shaped key:\n%s", p.method, p.path, resp.StatusCode, body)
		}
	}

	// **A SUCCESSFUL sign-in, key-checked.**
	//
	// Both probes above submit the wrong password by construction, so they only ever
	// examine a refusal. The one response that carries a whole `AuthSession` — the
	// thing sign-in exists to return — would otherwise be checked by nothing here,
	// and `web/mock`'s twin could go red on `mustChangePassword` while this file
	// stayed green. Two harnesses disagreeing about the same rule is a finding about
	// the quieter one.
	//
	// Without this block, removing the boolean exemption below leaves the test
	// passing — the check would be aimed somewhere the subject does not appear.
	{
		resp := r.do(http.MethodPost, "/auth/session",
			map[string]string{"username": adminUser, "password": testSecret})
		body := readAll(t, resp)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("the successful sign-in this case depends on answered %d, so nothing below examined an AuthSession", resp.StatusCode)
		}
		if !strings.Contains(body, "mustChangePassword") {
			t.Fatal("the AuthSession carries no mustChangePassword, so the boolean exemption this case exercises has no subject")
		}
		if keyLooksLikeACredential(body) {
			t.Errorf("a successful sign-in carries a credential-shaped key:\n%s", body)
		}
	}

	r.signIn(adminUser)
	for _, p := range []struct {
		method, path string
		body         any
	}{
		{http.MethodPost, "/auth/password", map[string]string{"currentPassword": secret, "newPassword": "x"}},
		{http.MethodPost, "/operators", map[string]any{"username": adminUser, "password": secret, "role": "staff"}},
		{http.MethodPost, "/accounts", map[string]any{"username": "guest", "password": secret, "siteId": testSite}},
	} {
		resp := r.do(p.method, p.path, p.body)
		body := readAll(t, resp)
		resp.Body.Close()
		if strings.Contains(body, secret) {
			t.Errorf("%s %s (%d) echoed the submitted password:\n%s", p.method, p.path, resp.StatusCode, body)
		}
		if keyLooksLikeACredential(body) {
			t.Errorf("%s %s (%d) carries a credential-shaped key:\n%s", p.method, p.path, resp.StatusCode, body)
		}
	}
}

var credentialKey = regexp.MustCompile(`"[^"]*(?i:pass|secret|credential)[^"]*"\s*:`)

// credentialShapedName is the same pattern, matched against a bare key.
var credentialShapedName = regexp.MustCompile(`(?i)pass|secret|credential`)

// keyLooksLikeACredential is the same shape as the mock's assertion on
// `AuthAttempt`.
//
// **A boolean is exempt by its TYPE, before any name is considered**, because a
// boolean cannot be a credential in any encoding. That is not a convenience: the
// alternative is a list of permitted names, and a permitted name goes on being
// permitted after somebody changes what it holds — a stale second copy of a rule,
// which `docs/rules/database.md` has a section about. `mustChangePassword` is
// exempt without being named; the day it becomes a string it is suspicious again
// with nobody editing this file.
//
// The named exemptions below are the ones a type cannot answer: `credentialKind` is
// an enum, `sharedSecretId` an identifier, and the refusal field names are strings by
// construction. Each is a *string* whose name matches and whose content is not a
// credential, so there is nothing to derive from and a human has to say so.
func keyLooksLikeACredential(body string) bool {
	for _, m := range credentialKey.FindAllString(body, -1) {
		switch {
		case strings.Contains(m, "secretSet"),
			strings.Contains(m, "credentialSet"),
			strings.Contains(m, "credentialKind"),
			strings.Contains(m, "sharedSecretId"),
			// The one-time secret from `createNas` and `rotateNasSecret`, which
			// the contract returns exactly once and by name.
			strings.Contains(m, "sharedSecret\""),
			// Refusals name the field that was refused; naming it is not
			// echoing what was submitted into it.
			strings.Contains(m, "currentPassword"),
			strings.Contains(m, "newPassword"),
			strings.Contains(m, "\"password\""):
			continue
		default:
			// A boolean under a credential-shaped name is not one. Asked of the
			// parsed value rather than of the matched text, so no name has to be
			// listed for it.
			if valueIsBool(body, m) {
				continue
			}
			return true
		}
	}
	return false
}

// valueIsBool answers whether the key in `match` (a `"name":` fragment) carries a
// boolean anywhere in the document.
func valueIsBool(body, match string) bool {
	name := strings.Trim(strings.TrimSuffix(strings.TrimSpace(match), ":"), " \"")
	var doc any
	if json.Unmarshal([]byte(body), &doc) != nil {
		return false
	}
	return keyIsBool(doc, name)
}

func keyIsBool(v any, name string) bool {
	switch t := v.(type) {
	case map[string]any:
		if val, ok := t[name]; ok {
			if _, isBool := val.(bool); isBool {
				return true
			}
			return false
		}
		for _, val := range t {
			if keyIsBool(val, name) {
				return true
			}
		}
	case []any:
		for _, val := range t {
			if keyIsBool(val, name) {
				return true
			}
		}
	}
	return false
}

// ── shape ────────────────────────────────────────────────────────────────────

// TestBasePathIsV0 pins the base path: `/api/v0` answers and `/api/v1` does not.
func TestBasePathIsV0(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	ok := r.do(http.MethodGet, "/sites", nil)
	ok.Body.Close()
	if ok.StatusCode != http.StatusOK {
		t.Errorf("/api/v0/sites: got %d, want 200", ok.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, r.server.URL+"/api/v1/sites", nil)
	req.AddCookie(r.cookie)
	old, err := r.server.Client().Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer old.Body.Close()
	if old.StatusCode != http.StatusNotFound {
		t.Errorf("/api/v1/sites: got %d, want 404 — only /api/v0 answers", old.StatusCode)
	}
}

// TestNoCORSHeaders pins that no CORS header is sent.
//
// The code this replaced sent `Access-Control-Allow-Origin: *` beside
// `Access-Control-Allow-Credentials: true`. Browsers reject that pair, which is
// the only reason it was not already a vulnerability — a browser that honoured
// it would let any origin read an authenticated response.
func TestNoCORSHeaders(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)
	resp := r.do(http.MethodGet, "/sites", nil)
	defer resp.Body.Close()
	for _, h := range []string{
		"Access-Control-Allow-Origin",
		"Access-Control-Allow-Credentials",
		"Access-Control-Allow-Methods",
		"Access-Control-Allow-Headers",
	} {
		if v := resp.Header.Get(h); v != "" {
			t.Errorf("%s is %q — the console is same-origin and this API sends no CORS headers", h, v)
		}
	}
}

// ── behaviour the console cannot start without ───────────────────────────────

func TestListsPaginateWithACursorAndNoTotal(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	resp := r.do(http.MethodGet, "/nas?siteId="+testSite+"&limit=1", nil)
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d: %s", resp.StatusCode, body)
	}
	var page struct {
		Items      []gen.Nas      `json:"items"`
		NextCursor *string        `json:"nextCursor"`
		Extra      map[string]any `json:"-"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("limit=1 returned %d items", len(page.Items))
	}
	if page.NextCursor == nil {
		t.Fatal("no nextCursor with two rows and limit=1")
	}
	if strings.Contains(body, `"total"`) {
		t.Error("carries a total — the contract says there deliberately is none")
	}

	second := r.do(http.MethodGet, "/nas?siteId="+testSite+"&limit=1&cursor="+*page.NextCursor, nil)
	secondBody := readAll(t, second)
	second.Body.Close()
	var page2 struct {
		Items      []gen.Nas `json:"items"`
		NextCursor *string   `json:"nextCursor"`
	}
	_ = json.Unmarshal([]byte(secondBody), &page2)
	if len(page2.Items) != 1 || page2.Items[0].Id == page.Items[0].Id {
		t.Errorf("the second page repeated the first: %s", secondBody)
	}
	if page2.NextCursor != nil {
		t.Errorf("nextCursor is %q on the last page, want null", *page2.NextCursor)
	}
}

// TestRotationIsRefusedOnlyWhereThereIsNoRouteBack.
//
// Both halves, because either alone is satisfied by the wrong rule. A device
// that cannot be pushed to but *is* reachable must still rotate — hiding that
// would leave it unable to rotate a secret at all, which is the over-broad
// reading `docs/rules/nas-drivers.md` records as having been wrong twice.
func TestRotationIsRefusedOnlyWhereThereIsNoRouteBack(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	// nas-2: cannot push, unreachable. Refused.
	refused := r.do(http.MethodPost, "/nas/nas-2/secret", nil)
	refusedBody := readAll(t, refused)
	refused.Body.Close()
	if refused.StatusCode != http.StatusConflict {
		t.Fatalf("unreachable + cannot push: got %d, want 409\n%s", refused.StatusCode, refusedBody)
	}
	var p gen.Problem
	_ = json.Unmarshal([]byte(refusedBody), &p)
	if p.Type != gen.ProblemsrotationRefused {
		t.Errorf("type %q, want /problems/rotation-refused", p.Type)
	}

	// The same device, reachable: permitted, and the operator enters the new
	// value by hand.
	r.store.World().Nas[1].Reachability = gen.NasReachabilityOk
	allowed := r.do(http.MethodPost, "/nas/nas-2/secret", nil)
	allowedBody := readAll(t, allowed)
	allowed.Body.Close()
	if allowed.StatusCode != http.StatusOK {
		t.Fatalf("reachable + cannot push: got %d, want 200\n%s", allowed.StatusCode, allowedBody)
	}
	var out struct {
		SharedSecret string          `json:"sharedSecret"`
		ChangeClass  gen.ChangeClass `json:"changeClass"`
	}
	_ = json.Unmarshal([]byte(allowedBody), &out)
	if out.SharedSecret == "" {
		t.Error("no secret returned — this is the only response that ever carries one")
	}
	if out.ChangeClass == "" {
		t.Error("no changeClass — the console warns the operator with this before they confirm")
	}
}

// TestRequiredGardenEntryCannotBeDeleted.
//
// Those entries are the portal's own FQDN, its OCSP/CRL endpoints and the
// RFC 8908 endpoint. Removing one presents to every guest as a network that is
// simply broken, and the console does not render the control — this is the
// second gate, for a client that tries anyway.
func TestRequiredGardenEntryCannotBeDeleted(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	refused := r.do(http.MethodDelete, "/sites/"+testSite+"/walled-garden/garden-portal", nil)
	refusedBody := readAll(t, refused)
	refused.Body.Close()
	if refused.StatusCode != http.StatusConflict {
		t.Fatalf("got %d, want 409\n%s", refused.StatusCode, refusedBody)
	}
	var p gen.Problem
	_ = json.Unmarshal([]byte(refusedBody), &p)
	if p.Type != gen.ProblemsgardenEntryRequired {
		t.Errorf("type %q, want /problems/garden-entry-required", p.Type)
	}

	// An operator entry deletes, so the refusal is about `origin` and not about
	// the route being broken.
	ok := r.do(http.MethodDelete, "/sites/"+testSite+"/walled-garden/garden-maps", nil)
	ok.Body.Close()
	if ok.StatusCode != http.StatusNoContent {
		t.Errorf("deleting an operator entry: got %d, want 204", ok.StatusCode)
	}
}

// TestLastAdministratorIsRefused. There is no password-reset channel, so a
// deployment with nobody who can administer it has no recovery short of the
// binary's first-boot path.
func TestLastAdministratorIsRefused(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	// `op-admin` is the only *enabled* administrator; `op-off` is disabled and
	// therefore does not count, which is the whole subtlety.
	resp := r.do(http.MethodPatch, "/operators/op-admin", map[string]any{"role": "staff"})
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("got %d, want 409\n%s", resp.StatusCode, body)
	}
	var p gen.Problem
	_ = json.Unmarshal([]byte(body), &p)
	if p.Type != gen.ProblemslastAdministrator {
		t.Errorf("type %q, want /problems/last-administrator", p.Type)
	}
}

// TestUnwiredPortAnswers501 — the honest gap.
//
// Driven through `PortalHandler` rather than `Handler`, because `/portal/context`
// is a guest operation and the console's surface answers `404` for one — which is
// what it is: on that listener there is no such route. The operation is public,
// so the gate is not what refuses it either way, which is why it is still the one
// this drives.
func TestUnwiredPortAnswers501(t *testing.T) {
	srv, err := api.New(api.Config{})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv.PortalHandler())
	defer ts.Close()

	resp, err := ts.Client().Get(ts.URL + api.BasePath + "/portal/context")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("got %d, want 501 — a gap must be louder than a success", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	var p gen.Problem
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("not a problem document: %s", body)
	}
	if p.Detail != nil {
		t.Error("carries a detail")
	}
}

// ── driving the contract ─────────────────────────────────────────────────────

type operation struct {
	id, method, path string
	body             any
}

// protectedOperations builds one drivable request per operation the contract
// does not mark `security: []`.
//
// Path parameters get a placeholder and required query parameters get a value,
// because the point is to reach the gate — an operation refused with 422 for a
// missing `siteId` would pass a 401 check by accident and prove nothing.
func protectedOperations(t *testing.T) []operation {
	t.Helper()
	spec, err := gen.GetSwagger()
	if err != nil {
		t.Fatalf("GetSwagger: %v", err)
	}

	var out []operation
	for pattern, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			if op.Security != nil && len(*op.Security) == 0 {
				continue
			}
			path := pattern
			for _, seg := range placeholders(pattern) {
				path = strings.Replace(path, "{"+seg+"}", "placeholder", 1)
			}
			if q := requiredQuery(item, op); q != "" {
				path += "?" + q
			}
			out = append(out, operation{
				id:     strings.ToUpper(op.OperationID[:1]) + op.OperationID[1:],
				method: method, path: path,
				body: bodyFor(method),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

func placeholders(pattern string) []string {
	var out []string
	for _, part := range strings.Split(pattern, "/") {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			out = append(out, strings.Trim(part, "{}"))
		}
	}
	return out
}

// requiredQuery supplies every required query parameter, so the request reaches
// the gate. `siteId` gets the test site; a `date-time` gets a timestamp — the
// `reports` operations require a bounded range, and without one the binder answers
// 422 before the gate is asked, which is the accident this helper exists to avoid.
func requiredQuery(item *openapi3.PathItem, op *openapi3.Operation) string {
	q := url.Values{}
	for _, p := range append(slices.Clone(item.Parameters), op.Parameters...) {
		if p.Value == nil || p.Value.In != "query" || !p.Value.Required {
			continue
		}
		switch {
		case p.Value.Name == "siteId":
			q.Set("siteId", testSite)
		case p.Value.Schema != nil && p.Value.Schema.Value != nil && p.Value.Schema.Value.Format == "date-time":
			q.Set(p.Value.Name, at(0).Format(time.RFC3339))
		}
	}
	return q.Encode()
}

// bodyFor keeps a PATCH or POST from being refused for an unreadable body
// before the gate has had a chance to refuse it for having no session.
func bodyFor(method string) any {
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodPut:
		return map[string]any{}
	default:
		return nil
	}
}

var _ = fmt.Sprintf
