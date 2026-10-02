package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// The two surfaces a caller can reach without signing in must not let that
// caller decide how much this process allocates.

// endless is a body of any length that allocates nothing: it hands out the same
// byte forever and counts how much was taken. The count is the assertion — a
// server that read it to the end would have allocated all of it.
type endless struct {
	n     atomic.Int64
	limit int64
}

func (e *endless) Read(p []byte) (int, error) {
	if e.n.Load() >= e.limit {
		return 0, io.EOF
	}
	for i := range p {
		p[i] = 'a'
	}
	e.n.Add(int64(len(p)))
	return len(p), nil
}

type surfaceCase struct {
	name        string
	handler     func(*api.Server) http.Handler
	path        string
	contentType string
	limit       int64
}

func surfaces() []surfaceCase {
	return []surfaceCase{
		{
			name:        "guest portal, redeemVoucher",
			handler:     (*api.Server).PortalHandler,
			path:        api.BasePath + "/portal/voucher",
			contentType: "application/x-www-form-urlencoded",
			limit:       api.PortalBodyLimit,
		},
		{
			name:        "console, signIn",
			handler:     (*api.Server).Handler,
			path:        api.BasePath + "/auth/session",
			contentType: "application/json",
			// Public, so the anonymous ceiling whoever asks: see
			// `TestAnAnonymousCallerGetsTheAnonymousCeiling`.
			limit: api.AnonymousBodyLimit,
		},
	}
}

func bareServer(t *testing.T) *api.Server {
	t.Helper()
	srv, err := api.New(api.Config{Ports: world(t).Ports(), Now: func() time.Time { return at(0) }})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	return srv
}

// TestAnOversizedBodyIsRefusedWith413 — the declared-length half. A body whose
// Content-Length is over the listener's ceiling is refused with the contract's
// `/problems/too-large`, naming the figures, and without being read.
func TestAnOversizedBodyIsRefusedWith413(t *testing.T) {
	for _, sc := range surfaces() {
		t.Run(sc.name, func(t *testing.T) {
			ts := httptest.NewServer(sc.handler(bareServer(t)))
			defer ts.Close()

			size := sc.limit * 4
			body := &endless{limit: size}
			req, err := http.NewRequest(http.MethodPost, ts.URL+sc.path, body)
			if err != nil {
				t.Fatal(err)
			}
			req.ContentLength = size
			req.Header.Set("Content-Type", sc.contentType)
			resp, err := ts.Client().Do(req)
			if err != nil {
				// A server that answers before reading may close the connection while
				// the client is still writing. That is a refusal too, but the test
				// wants the answer, so it is a failure of the test's own shape.
				t.Fatalf("request: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("a %d-byte body answered %d, want 413", size, resp.StatusCode)
			}
			var p struct {
				Type   string `json:"type"`
				Params struct {
					FieldParams map[string]map[string]float64 `json:"fieldParams"`
				} `json:"params"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
				t.Fatalf("not a problem document: %v", err)
			}
			if p.Type != string(gen.ProblemstooLarge) {
				t.Fatalf("type = %q, want %q", p.Type, gen.ProblemstooLarge)
			}
			if got := int64(p.Params.FieldParams["body"]["limitBytes"]); got != sc.limit {
				t.Fatalf("limitBytes = %d, want %d", got, sc.limit)
			}
		})
	}
}

// TestAStreamedBodyStopsAtTheCeiling — the half a Content-Length check cannot
// see. A chunked body declares no length; the server must stop reading at the
// ceiling and answer 413, and what it allocated must be bounded by the ceiling
// rather than by what the caller sent.
//
// Driven in-process rather than over a socket so the body's consumption can be
// counted exactly: `endless` records how much the server took.
func TestAStreamedBodyStopsAtTheCeiling(t *testing.T) {
	for _, sc := range surfaces() {
		t.Run(sc.name, func(t *testing.T) {
			h := sc.handler(bareServer(t))
			const sent = 64 << 20
			body := &endless{limit: sent}
			req := httptest.NewRequest(http.MethodPost, sc.path, body)
			req.ContentLength = -1
			req.Header.Set("Content-Type", sc.contentType)
			rec := httptest.NewRecorder()

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)
			h.ServeHTTP(rec, req)
			runtime.ReadMemStats(&after)

			if rec.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("a %d MiB streamed body answered %d, want 413", sent>>20, rec.Code)
			}
			// MaxBytesReader reads one buffer past the limit to learn it was
			// exceeded; anything beyond a few buffers is a server still reading.
			if taken := body.n.Load(); taken > sc.limit+64<<10 {
				t.Fatalf("the server read %d bytes of a body it should have stopped at %d", taken, sc.limit)
			}
			allocated := after.TotalAlloc - before.TotalAlloc
			t.Logf("sent %d MiB, server read %d B, allocated %d KiB", sent>>20, body.n.Load(), allocated>>10)
			if bound := uint64(sc.limit)*4 + 4<<20; allocated > bound {
				t.Fatalf("serving the refusal allocated %d KiB, over %d KiB", allocated>>10, bound>>10)
			}
		})
	}
}

// ── sign-in throttling ───────────────────────────────────────────────────────

// countingOperators counts how often a password is actually checked, which is
// the thing the throttle exists to bound: every call is an argon2id computation
// in a deployment.
type countingOperators struct {
	api.OperatorDirectory
	calls atomic.Int32
}

func (c *countingOperators) Authenticate(ctx context.Context, username, password string) (gen.Operator, error) {
	c.calls.Add(1)
	return c.OperatorDirectory.Authenticate(ctx, username, password)
}

type throttleRig struct {
	*rig
	ops   *countingOperators
	clock *atomic.Int64 // seconds past at(0)
}

func newThrottleRig(t *testing.T) *throttleRig {
	t.Helper()
	store := world(t)
	ports := store.Ports()
	ops := &countingOperators{OperatorDirectory: ports.Operators}
	ports.Operators = ops
	var clock atomic.Int64
	srv, err := api.New(api.Config{
		Ports: ports,
		Now:   func() time.Time { return at(0).Add(time.Duration(clock.Load()) * time.Second) },
	})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &throttleRig{rig: &rig{t: t, server: ts, store: store}, ops: ops, clock: &clock}
}

func (r *throttleRig) attempt(username, password string) *http.Response {
	r.t.Helper()
	return r.do(http.MethodPost, "/auth/session", map[string]string{"username": username, "password": password})
}

func assertRateLimited(t *testing.T, resp *http.Response) int {
	t.Helper()
	b := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("got %d, want 429\n%s", resp.StatusCode, b)
	}
	retry, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil || retry < 1 {
		t.Fatalf("Retry-After = %q, want a whole number of seconds >= 1", resp.Header.Get("Retry-After"))
	}
	var p struct {
		Type   string         `json:"type"`
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(b), &p); err != nil {
		t.Fatalf("not a problem document: %v\n%s", err, b)
	}
	if p.Type != string(gen.ProblemsrateLimited) {
		t.Fatalf("type = %q, want %q", p.Type, gen.ProblemsrateLimited)
	}
	if got, _ := p.Params["retryAfterSeconds"].(float64); int(got) != retry {
		t.Fatalf("params.retryAfterSeconds = %v, Retry-After = %d; they must be the same figure", p.Params["retryAfterSeconds"], retry)
	}
	return retry
}

// TestConsoleSignInIsRateLimitedPerAccount: five wrong passwords against one
// account, then 429 — **and the password is not checked**, which is the
// property that bounds the argon2id spend. The correct password is refused too
// while the bucket is empty: a limit that let the right guess through would be a
// limit an attacker simply keeps guessing past.
func TestConsoleSignInIsRateLimitedPerAccount(t *testing.T) {
	r := newThrottleRig(t)
	for i := 0; i < 5; i++ {
		resp := r.attempt(adminUser, "wrong-password")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, resp.StatusCode)
		}
	}
	checked := r.ops.calls.Load()

	assertRateLimited(t, r.attempt(adminUser, "wrong-password"))
	assertRateLimited(t, r.attempt(adminUser, testSecret))
	// Spelled differently, still the same account.
	assertRateLimited(t, r.attempt("  "+strings.ToUpper(adminUser)+" ", "wrong-password"))
	if got := r.ops.calls.Load(); got != checked {
		t.Fatalf("a rate-limited attempt still checked the password (%d checks, want %d)", got, checked)
	}

	// And it recovers: thirty seconds later there is one token back.
	r.clock.Add(30)
	resp := r.attempt(adminUser, testSecret)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("after the refill interval the right password answered %d, want 200", resp.StatusCode)
	}
}

// TestConsoleSignInIsRateLimitedPerAddress: one address walking a username list —
// Keycloak's documented gap — is stopped by the address bucket, and the refusal
// is identical for names that exist and names that do not.
func TestConsoleSignInIsRateLimitedPerAddress(t *testing.T) {
	r := newThrottleRig(t)
	for i := 0; i < 10; i++ {
		resp := r.attempt("nobody-"+strconv.Itoa(i), "whatever-it-is")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: got %d, want 401", i+1, resp.StatusCode)
		}
	}
	unknown := r.attempt("nobody-else", "whatever-it-is")
	unknownBody := readAll(t, unknown)
	known := r.attempt(staffUser, "whatever-it-is")
	knownBody := readAll(t, known)
	if unknown.StatusCode != http.StatusTooManyRequests || known.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after ten attempts from one address: %d and %d, want 429 twice", unknown.StatusCode, known.StatusCode)
	}
	if unknownBody != knownBody {
		t.Fatalf("the refusal differs between a real and an unknown username:\n  %s\n  %s", unknownBody, knownBody)
	}
}

// TestASuccessfulSignInClearsTheAccountBucket: the owner of the account does not
// carry an attacker's failures into their next sign-in.
func TestASuccessfulSignInClearsTheAccountBucket(t *testing.T) {
	r := newThrottleRig(t)
	for i := 0; i < 4; i++ {
		r.attempt(adminUser, "wrong-password").Body.Close()
	}
	if resp := r.attempt(adminUser, testSecret); resp.StatusCode != http.StatusOK {
		t.Fatalf("the fifth attempt, correct, answered %d", resp.StatusCode)
	}
	// Four more failures would be over the burst without the reset. The address
	// bucket (ten) has room for exactly these.
	for i := 0; i < 4; i++ {
		resp := r.attempt(adminUser, "wrong-password")
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("after a success, failure %d answered %d, want 401", i+1, resp.StatusCode)
		}
	}
}

// TestPortalAccountLoginIsRateLimitedPerAccount — the guest surface. Its answer
// stays `303` (the CNA rule), so a throttled login is the form again with the
// generic reason, and the right password does not get through while the bucket
// is empty.
//
// Five attempts, under the portal's own per-MAC burst of ten, so what refuses the
// sixth is this limit and not that one.
func TestPortalAccountLoginIsRateLimitedPerAccount(t *testing.T) {
	g := presentRig(t)
	g.arrive(guestMAC).Body.Close()
	for i := 0; i < 5; i++ {
		resp := g.send(http.MethodPost, "/portal/account", url.Values{"username": {guestUser}, "password": {"not-it"}, "lang": {"en"}})
		resp.Body.Close()
		if loc := resp.Header.Get("Location"); !strings.Contains(loc, "e=bad-credentials") {
			t.Fatalf("attempt %d: %d Location %q, want the bad-credentials form", i+1, resp.StatusCode, loc)
		}
	}
	resp := g.send(http.MethodPost, "/portal/account", url.Values{"username": {guestUser}, "password": {guestPass}, "lang": {"en"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status %d, want 303 — the portal never answers a form with anything else", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "e=generic") {
		t.Fatalf("the sixth attempt, with the right password, went to %q; want the generic refusal", loc)
	}
	if n := len(g.store.PortalStore.Grants()); n != 0 {
		t.Fatalf("a throttled login created %d grant(s)", n)
	}
}

// TestAStrangerCannotLockAGuestOutOfTheirPortalAccount is the portal half of the
// lockout defence: keyed by username alone, a stranger in radio range spending five
// wrong passwords would hold a guest's account shut. The bucket is per account and
// per source address (hardening.go says why a device cookie would not do on the
// portal), so the stranger empties only their own.
func TestAStrangerCannotLockAGuestOutOfTheirPortalAccount(t *testing.T) {
	g := presentRig(t)
	g.arrive(guestMAC).Body.Close()

	stranger := func(password string) string {
		form := url.Values{"username": {guestUser}, "password": {password}, "lang": {"en"}}
		req := httptest.NewRequest(http.MethodPost, api.BasePath+"/portal/account", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.RemoteAddr = "10.66.0.7:40000"
		rec := httptest.NewRecorder()
		g.server.Config.Handler.ServeHTTP(rec, req)
		return rec.Header().Get("Location")
	}
	for i := 0; i < 5; i++ {
		stranger("a-guess")
	}
	if loc := stranger(guestPass); !strings.Contains(loc, "e=generic") {
		t.Fatalf("the stranger's own sixth attempt went to %q; want the generic refusal — the test is not measuring an emptied bucket", loc)
	}

	resp := g.send(http.MethodPost, "/portal/account", url.Values{"username": {guestUser}, "password": {guestPass}, "lang": {"en"}})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); strings.Contains(loc, "e=") {
		t.Fatalf("the guest, from their own address, went to %q while a stranger held the account's bucket empty; want them connected", loc)
	}
	if n := len(g.store.PortalStore.Grants()); n != 1 {
		t.Fatalf("the guest's login made %d grant(s), want 1", n)
	}
}
