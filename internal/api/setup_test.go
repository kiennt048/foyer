package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity"
)

// The first-access setup over the wire (handlers_setup.go). The PostgreSQL half —
// the row lock that makes "once" true under a race, and the migration that removes
// operators still holding the shipped password — is `internal/apistore`'s.

func openSetup(t *testing.T, r *rig) string {
	t.Helper()
	code, err := identity.NewSetupCode()
	if err != nil {
		t.Fatal(err)
	}
	r.store.OpenSetup(code)
	return code
}

func setupState(t *testing.T, r *rig) bool {
	t.Helper()
	return setupStateBody(t, r).Required
}

// setupStateBody is `getAdminSetup` decoded whole: `required` alone cannot tell a
// finished install from one whose installer never ran, which is the whole of the
// defect the test below pins.
func setupStateBody(t *testing.T, r *rig) gen.AdminSetupState {
	t.Helper()
	resp := r.do(http.MethodGet, "/auth/setup", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("getAdminSetup answered %d", resp.StatusCode)
	}
	var st gen.AdminSetupState
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// newBareRig is a fresh install as it is when the bootstrap never ran: a site and
// **no operator at all**, which is the state in which a sign-in form can never
// succeed. `newRig` over a world with its operators removed.
func newBareRig(t *testing.T) *rig {
	t.Helper()
	store := world(t)
	store.World().Operators = nil
	store.World().Credentials = map[string]string{}
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

// TestAnInstallWithNoAdministratorIsNeverOfferedTheSignInForm: reproduced on a
// fresh install — without `noAdministrator`, `GET /auth/setup` answers 200
// `{"required":false}` for an install with zero administrators, and the console
// renders its sign-in form. The request is not failing open; the server is
// reporting a setup it considers closed. Both `false` cases are distinguishable,
// and the sign-in one is only reachable with an administrator.
func TestAnInstallWithNoAdministratorIsNeverOfferedTheSignInForm(t *testing.T) {
	broken := newBareRig(t)
	st := setupStateBody(t, broken)
	if st.Required {
		t.Error("no installer opened a setup, yet getAdminSetup says one is required")
	}
	if !st.NoAdministrator {
		t.Error("an install with no administrator and no code says nothing about it, " +
			"which is what had the console show a sign-in form nobody can use")
	}
	// And the guard is unchanged: the screen can tell the truth about the dead end
	// and still complete nothing.
	resp := broken.do(http.MethodPost, "/auth/setup", map[string]string{
		"setupCode": "0000-0000-0000-0000", "username": "someone", "password": "long-enough-password",
	})
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, string(gen.ProblemssetupComplete)) {
		t.Errorf("completing a setup nobody opened answered %d, want 409 setup-complete: %s", resp.StatusCode, body)
	}

	// A finished install is the other `false`, and must not be mistaken for it.
	done := newRig(t)
	finished := setupStateBody(t, done)
	if finished.Required || finished.NoAdministrator {
		t.Error("an install with an administrator reports the dead end, so a working " +
			"console would tell its own operator to go and re-run the installer")
	}
}

// TestAnOpenSetupSuppressesTheNoAdministratorBit: the two bits cannot both be set,
// or a browser racing the installer would show a dead end where a working form is.
func TestAnOpenSetupSuppressesTheNoAdministratorBit(t *testing.T) {
	r := newBareRig(t)
	openSetup(t, r)
	st := setupStateBody(t, r)
	if !st.Required {
		t.Fatal("the setup is open and getAdminSetup says it is not")
	}
	if st.NoAdministrator {
		t.Error("an open setup also reports the dead end; the console would show a " +
			"warning where a working form is")
	}
}

func TestNoSetupIsOfferedUnlessTheInstallerOpenedOne(t *testing.T) {
	r := newRig(t)
	if setupState(t, r) {
		t.Fatal("an install nobody opened a setup on says one is required")
	}
	resp := r.do(http.MethodPost, "/auth/setup", map[string]string{
		"setupCode": "0000-0000-0000-0000", "username": "someone", "password": "long-enough-password",
	})
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict || !strings.Contains(body, string(gen.ProblemssetupComplete)) {
		t.Fatalf("completing a setup nobody opened answered %d, want 409 setup-complete: %s", resp.StatusCode, body)
	}
}

func TestTheSetupCreatesTheAdministratorOnceAndSignsItIn(t *testing.T) {
	r := newRig(t)
	code := openSetup(t, r)
	if !setupState(t, r) {
		t.Fatal("the setup is open and getAdminSetup says it is not")
	}

	for _, c := range []struct {
		name, code, user, pass, field, fault string
	}{
		{"wrong code", "0000-0000-0000-0000", "owner", "long-enough-password", "setupCode", "incorrect"},
		{"no code", "", "owner", "long-enough-password", "setupCode", "required"},
		{"short password", code, "owner", "short", "password", "below-minimum"},
		{"taken username", code, adminUser, "long-enough-password", "username", "duplicate"},
	} {
		t.Run(c.name, func(t *testing.T) {
			resp := r.do(http.MethodPost, "/auth/setup", map[string]string{"setupCode": c.code, "username": c.user, "password": c.pass})
			body := readAll(t, resp)
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body, `"`+c.field+`":"`+c.fault+`"`) {
				t.Fatalf("got %d, want 422 %s on %s: %s", resp.StatusCode, c.fault, c.field, body)
			}
		})
	}
	if !setupState(t, r) {
		t.Fatal("a refused attempt closed the setup")
	}

	var wg sync.WaitGroup
	statuses := make([]int, 3)
	sessions := make([]gen.AuthSession, 3)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp := r.do(http.MethodPost, "/auth/setup", map[string]string{
				"setupCode": strings.ToLower(code), "username": "Owner" + string(rune('a'+i)), "password": "a-password-of-my-own",
			})
			defer resp.Body.Close()
			statuses[i] = resp.StatusCode
			if resp.StatusCode == http.StatusOK {
				_ = json.NewDecoder(resp.Body).Decode(&sessions[i])
				if len(resp.Cookies()) == 0 {
					t.Error("the setup signed nobody in: no cookie")
				}
			}
		}(i)
	}
	wg.Wait()
	won := -1
	for i, s := range statuses {
		switch {
		case s == http.StatusOK && won < 0:
			won = i
		case s == http.StatusOK:
			t.Fatalf("two setups succeeded: %v", statuses)
		case s != http.StatusConflict:
			t.Fatalf("a losing setup answered %d, want 409: %v", s, statuses)
		}
	}
	if won < 0 {
		t.Fatalf("no setup succeeded: %v", statuses)
	}
	got := sessions[won]
	if got.Operator.Role != gen.Administrator || !got.Operator.Enabled || got.MustChangePassword {
		t.Fatalf("the setup made %+v, want an enabled administrator with a password of their own", got)
	}
	if got.Operator.Username != "owner"+string(rune('a'+won)) {
		t.Errorf("username %q was not normalised to lower case", got.Operator.Username)
	}
	if setupState(t, r) {
		t.Fatal("getAdminSetup still says required after the setup completed")
	}

	// The administrator signs in with what they chose; the code is spent.
	resp := r.do(http.MethodPost, "/auth/session", map[string]string{"username": got.Operator.Username, "password": "a-password-of-my-own"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the new administrator could not sign in: %d", resp.StatusCode)
	}
	again := r.do(http.MethodPost, "/auth/setup", map[string]string{"setupCode": code, "username": "later", "password": "a-password-of-my-own"})
	again.Body.Close()
	if again.StatusCode != http.StatusConflict {
		t.Fatalf("the spent code answered %d, want 409", again.StatusCode)
	}
}

// TestSetupAttemptsAreThrottledPerAddress: 80 bits is out of reach of guessing,
// and the throttle is what bounds the argon2id a right code spends — and it is
// per address only, so nothing one device does can close the setup to the owner.
func TestSetupAttemptsAreThrottledPerAddress(t *testing.T) {
	r := newThrottleRig(t)
	r.store.OpenSetup("0000-0000-0000-0001")
	for i := 0; i < 10; i++ {
		resp := r.do(http.MethodPost, "/auth/setup", map[string]string{
			"setupCode": "0000-0000-0000-0000", "username": "owner", "password": "long-enough-password",
		})
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Fatalf("attempt %d answered %d, want 422", i+1, resp.StatusCode)
		}
	}
	resp := r.do(http.MethodPost, "/auth/setup", map[string]string{
		"setupCode": "0000-0000-0000-0001", "username": "owner", "password": "long-enough-password",
	})
	retry := resp.Header.Get("Retry-After")
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests || retry == "" {
		t.Fatalf("the eleventh attempt from one address answered %d (Retry-After %q), want 429", resp.StatusCode, retry)
	}
}
