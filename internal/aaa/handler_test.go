package aaa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The three status classes the generated FreeRADIUS config depends on, and the
// bounds that keep a slow Policy API from taking the health check down with it.
//
// `docs/rules/security.md` §2: `rlm_rest` blocks a FreeRADIUS worker, the pool
// is fixed, and when it is exhausted Status-Server is blocked too. Everything here
// exists so that the Policy API gives up before FreeRADIUS does, and answers 5xx
// when it gives up, so `fail` reaches the site's declared failure branch.

type slowStore struct {
	mockStore
	delay time.Duration
}

func (s *slowStore) LookupVoucher(ctx context.Context, code string) (*VoucherInfo, error) {
	select {
	case <-time.After(s.delay):
		return s.mockStore.LookupVoucher(ctx, code)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type brokenStore struct{ *mockStore }

func (brokenStore) LookupVoucher(context.Context, string) (*VoucherInfo, error) {
	return nil, errors.New("dial tcp 10.0.0.2:5432: connect: connection refused")
}

func post(t *testing.T, h http.Handler, req Request) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/aaa/authorize", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// TestAStoreThatCannotBeReachedIsUndecidedNotADenial is the rule this handler is
// built around, and it is worth stating plainly because the alternative looks
// correct.
//
// An engine that tests `err == nil && voucher != nil` and falls through
// otherwise turns a PostgreSQL outage into "identity not found" -- an
// authoritative Access-Reject, minted while the control plane knows nothing. That
// answer bypasses the per-site failure model completely: a venue configured
// fail-open is silently fail-closed for as long as the database is down, and
// nothing anywhere says so.
//
// 503, so `rlm_rest` returns `fail`, so the declared branch runs.
func TestAStoreThatCannotBeReachedIsUndecidedNotADenial(t *testing.T) {
	e := NewEngine(brokenStore{&mockStore{}}, 300)

	if _, err := e.Authorize(context.Background(), Request{UserName: "VALID-123"}); err == nil {
		t.Fatal("a database that refused the connection produced a decision rather than an error")
	}

	w := post(t, e.Handler(HandlerOptions{Logger: discardLogger()}), Request{UserName: "VALID-123"})
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d, want 503: a Policy API that could not look is not a Policy API that refused", w.Code)
	}
	// The reason a connection string must not leak: FreeRADIUS logs the body, and
	// `radiusd -X` output ends up in a support bundle.
	if strings.Contains(w.Body.String(), "5432") {
		t.Errorf("the store's error reached the wire: %s", w.Body.String())
	}
}

// TestAMissingIdentityIsADenialNotUndecided is the other half, and without it the
// test above is satisfied by a handler that answers 503 to everything.
//
// It matters most on a fail-open site: a wrong voucher code answered 503 would
// reach the failure branch and be let on to the network.
func TestAMissingIdentityIsADenialNotUndecided(t *testing.T) {
	e := NewEngine(&mockStore{vouchers: map[string]*VoucherInfo{}, accounts: map[string]*GuestAccountInfo{}}, 300)

	w := post(t, e.Handler(HandlerOptions{Logger: discardLogger()}), Request{UserName: "WRONG-CODE"})
	if w.Code == http.StatusServiceUnavailable {
		t.Fatal("a wrong voucher code answered 503, which reaches the failure branch: on a fail-open site that authorizes it")
	}
	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("status %d for a wrong voucher code; a denial must be 4xx and must not be 5xx", w.Code)
	}
}

// TestNotFoundSentinelIsHonoured: a store that reports absence with an error, as
// `internal/store` does, must not have that read as unreachable.
func TestNotFoundSentinelIsHonoured(t *testing.T) {
	e := NewEngine(sentinelStore{&mockStore{}}, 300)
	dec, err := e.Authorize(context.Background(), Request{UserName: "WRONG-CODE"})
	if err != nil {
		t.Fatalf("ErrIdentityNotFound was read as unreachable: %v", err)
	}
	if dec.Allow {
		t.Error("a missing identity was authorized")
	}
}

type sentinelStore struct{ *mockStore }

func (sentinelStore) LookupVoucher(context.Context, string) (*VoucherInfo, error) {
	return nil, ErrIdentityNotFound
}

func (sentinelStore) LookupGuestAccount(context.Context, string) (*GuestAccountInfo, error) {
	return nil, ErrIdentityNotFound
}

// TestTheDeadlineIsEnforcedRatherThanDocumented.
//
// A timeout that is only written down is a FreeRADIUS worker held for as long as
// the store feels like taking. The handler's own deadline must fire, and it must
// fire as 503 -- the same answer as being down, because to FreeRADIUS it is the
// same thing.
func TestTheDeadlineIsEnforcedRatherThanDocumented(t *testing.T) {
	e := NewEngine(&slowStore{mockStore: mockStore{}, delay: 5 * time.Second}, 300)
	h := e.Handler(HandlerOptions{Timeout: 50 * time.Millisecond, Logger: discardLogger()})

	start := time.Now()
	w := post(t, h, Request{UserName: "VALID-123"})
	elapsed := time.Since(start)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status %d after the deadline, want 503", w.Code)
	}
	// Generous, because a loaded CI machine is not the subject. The claim under
	// test is "well under the ~3 s NAS retransmit interval", not a stopwatch.
	if elapsed > time.Second {
		t.Errorf("the handler took %s to give up on a 50 ms deadline; rlm_rest holds a FreeRADIUS worker for every millisecond of that", elapsed)
	}
}

// TestTheConcurrencyCeilingRefusesRatherThanQueues.
//
// Queueing behind a full pool is how a slow Policy API becomes a wedged one: the
// requests pile up in Go instead of in FreeRADIUS, the latency grows without bound,
// and every one of them is still holding a worker thread at the other end. On a
// host with under 2 GB shared between PostgreSQL, FreeRADIUS and this process, the
// pile is also the thing that gets something OOM-killed.
//
// Refused means answered, immediately, with the status that reaches the failure
// branch.
func TestTheConcurrencyCeilingRefusesRatherThanQueues(t *testing.T) {
	release := make(chan struct{})
	e := NewEngine(&blockingStore{release: release}, 300)
	h := e.Handler(HandlerOptions{MaxConcurrent: 2, Timeout: 10 * time.Second, Logger: discardLogger()})

	var wg sync.WaitGroup
	inFlight := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inFlight <- post(t, h, Request{UserName: "VALID-123"})
		}()
	}

	// Wait until both slots are genuinely occupied, rather than sampling and
	// hoping: an assertion taken at the moment a goroutine was started is a race
	// by construction (`docs/rules/testing.md`).
	deadline := time.Now().Add(2 * time.Second)
	for {
		if entered := loadEntered(e); entered >= 2 {
			break
		}
		if time.Now().After(deadline) {
			close(release)
			wg.Wait()
			t.Fatal("the two blocking requests never reached the store")
		}
		time.Sleep(time.Millisecond)
	}

	third := post(t, h, Request{UserName: "VALID-123"})
	if third.Code != http.StatusServiceUnavailable {
		t.Errorf("the third concurrent request over a ceiling of two got %d, want 503", third.Code)
	}

	close(release)
	wg.Wait()
	close(inFlight)
	for w := range inFlight {
		if w.Code != http.StatusOK {
			t.Errorf("a request inside the ceiling got %d, want 200: the ceiling refused work it had room for", w.Code)
		}
	}
}

type blockingStore struct {
	mockStore
	release chan struct{}
	mu      sync.Mutex
	entered int
}

func (b *blockingStore) LookupVoucher(ctx context.Context, code string) (*VoucherInfo, error) {
	b.mu.Lock()
	b.entered++
	b.mu.Unlock()
	select {
	case <-b.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &VoucherInfo{Code: code, State: "unused", DurationMinutes: 60}, nil
}

func (b *blockingStore) ClaimVoucher(_ context.Context, _, code string, _ time.Time) (*VoucherInfo, error) {
	return &VoucherInfo{Code: code, State: "in-use", DurationMinutes: 60}, nil
}

func loadEntered(e *Engine) int {
	b, ok := e.store.(*blockingStore)
	if !ok {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.entered
}

// TestAnUnreadableBodyIsNotAnAuthorizationOutcome.
//
// A body Go cannot parse is answered 400, which rlm_rest maps to `invalid`
// (measured on 3.2.10). It is not a 403 because it is not a claim about an
// identity -- and it is not a 5xx either, because the generated virtual server sends `invalid` to the REJECT branch: the body is built
// from client-chosen values, so an unreadable one is the client's doing and must
// never reach the fail-open branch. See TestAQuestionThatMeansTwoThingsIsRefused.
func TestAnUnreadableBodyIsNotAnAuthorizationOutcome(t *testing.T) {
	e := NewEngine(&mockStore{}, 300)
	h := e.Handler(HandlerOptions{Logger: discardLogger()})

	r := httptest.NewRequest(http.MethodPost, "/api/v1/aaa/authorize", strings.NewReader("{not json"))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d for an unparseable body, want 400", w.Code)
	}
}

// TestTheBodyIsBounded: a request that is never finished must not hold a goroutine,
// a buffer, or the FreeRADIUS worker at the other end of it.
func TestTheBodyIsBounded(t *testing.T) {
	e := NewEngine(&mockStore{}, 300)
	h := e.Handler(HandlerOptions{MaxBodyBytes: 128, Logger: discardLogger()})

	huge := `{"user_name":"` + strings.Repeat("A", 4096) + `"}`
	r := httptest.NewRequest(http.MethodPost, "/api/v1/aaa/authorize", strings.NewReader(huge))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)

	if w.Code == http.StatusOK {
		t.Error("a 4 KiB body was accepted against a 128 byte cap")
	}
}

// TestHTTPHandlerStillServesTheHappyPath.
//
// The signature `cmd/foyer` wires is unchanged, and this path was measured working
// against a real FreeRADIUS. Extending it must not have moved it.
func TestHTTPHandlerStillServesTheHappyPath(t *testing.T) {
	e := NewEngine(&mockStore{
		vouchers: map[string]*VoucherInfo{
			"VALID-123": {Code: "VALID-123", State: "unused", DurationMinutes: 60},
		},
	}, 300)

	w := post(t, http.HandlerFunc(e.HTTPHandler()), Request{UserName: "VALID-123", CallingStationID: "00:11:22:33:44:55"})
	if w.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", w.Code, w.Body.String())
	}
	var attrs map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &attrs); err != nil {
		t.Fatal(err)
	}
	if attrs["Session-Timeout"] != "3600" || attrs["Acct-Interim-Interval"] != "300" {
		t.Errorf("reply attributes %v", attrs)
	}
}

// TestOnlyPOST: a GET that reached this handler would be an authorization decision
// made from a URL, which is a thing that can be linked to.
func TestOnlyPOST(t *testing.T) {
	e := NewEngine(&mockStore{}, 300)
	h := e.Handler(HandlerOptions{Logger: discardLogger()})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/aaa/authorize", nil))
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET got %d, want 405", w.Code)
	}
}
