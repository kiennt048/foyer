package apistore_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/portal"
	"github.com/kiennt048/foyer/internal/store"
)

// The first-access setup against PostgreSQL: the property is that **a fresh
// install cannot be signed in to with any fixed password**, that the setup works once, and that a second attempt is refused —
// and the "once" is a row lock in `console_setup`, which only a real database can
// show.

// uninstalled is a fresh install as `first-boot.sh` leaves it: a tenant of its
// own, no operator, and a setup opened with the code the installer printed.
func uninstalled(t *testing.T) (*world, string) {
	t.Helper()
	w := newFreshInstall(t)
	// newFreshInstall creates an administrator. `first-boot.sh` makes none.
	if _, err := w.owner.Exec(context.Background(), `DELETE FROM operators WHERE id = $1`, w.adminID); err != nil {
		t.Fatal(err)
	}
	code, err := identity.NewSetupCode()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.st.ConsoleSetup().Open(context.Background(), identity.SetupCodeDigest(code)); err != nil {
		t.Fatalf("opening the setup: %v", err)
	}
	return w, code
}

// uninstalledAndUnfinished is a tenant with **no operator** and **no
// `console_setup` row at all** — the bootstrap never ran, or a restore left the
// row behind. A flat `{"required":false}` from `GET /auth/setup` for it would be
// the same answer a finished install gives, and the console would render its
// sign-in form.
func uninstalledAndUnfinished(t *testing.T) *world {
	t.Helper()
	w := newFreshInstall(t)
	if _, err := w.owner.Exec(context.Background(), `DELETE FROM operators WHERE id = $1`, w.adminID); err != nil {
		t.Fatal(err)
	}
	return w
}

// TestAFreshInstallWhoseBootstrapNeverRanSaysSo: the bit has to survive the real
// database, not only the memory world — `HasAdministrator` is a count over
// `operators`, and the console must be told the truth about that count.
func TestAFreshInstallWhoseBootstrapNeverRanSaysSo(t *testing.T) {
	w := uninstalledAndUnfinished(t)
	var st gen.AdminSetupState
	w.getJSON("/auth/setup", &st)
	if st.Required {
		t.Error("no installer opened a setup, yet getAdminSetup says one is required")
	}
	if !st.NoAdministrator {
		t.Errorf("no administrator and no code reports noAdministrator %v; the console "+
			"shows a sign-in form that can never succeed", st.NoAdministrator)
	}
	// The guard is untouched: knowing the installation is unreachable is not a way in.
	if status, body := w.post("/auth/setup", map[string]string{
		"setupCode": "0000-0000-0000-0000", "username": "owner", "password": w.adminPassword,
	}); status != http.StatusConflict || !strings.Contains(string(body), string(gen.ProblemssetupComplete)) {
		t.Errorf("completing a setup nobody opened answered %d, want 409 setup-complete: %s", status, body)
	}

	// The finished install is the other `false`, and must stay a plain closed setup.
	opened := newFreshInstall(t)
	var done gen.AdminSetupState
	opened.getJSON("/auth/setup", &done)
	if done.Required || done.NoAdministrator {
		t.Errorf("an install with an administrator reports noAdministrator %v", done.NoAdministrator)
	}
}

func (w *world) post(path string, body any) (int, []byte) {
	w.t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(w.url(path), "application/json", strings.NewReader(string(raw)))
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, []byte(b.String())
}

func TestAFreshInstallHasNoPasswordThatSignsIn(t *testing.T) {
	w, _ := uninstalled(t)
	// The two this product has shipped, and the obvious third.
	for _, cred := range [][2]string{{"admin", "Foyer@123"}, {"admin", "admin"}, {"admin", "password"}} {
		if status, body := w.post("/auth/session", map[string]string{"username": cred[0], "password": cred[1]}); status != http.StatusUnauthorized {
			t.Errorf("%s / %s answered %d, want 401: %s", cred[0], cred[1], status, body)
		}
	}
	var st gen.AdminSetupState
	w.getJSON("/auth/setup", &st)
	if !st.Required {
		t.Fatal("the setup is open and getAdminSetup says it is not, so the console would show a sign-in form nobody can use")
	}
}

func TestTheSetupWorksOnceAndOnlyWithItsCode(t *testing.T) {
	w, code := uninstalled(t)
	const user, pass = "owner", "a-password-of-my-own"

	if status, body := w.post("/auth/setup", map[string]string{
		"setupCode": "0000-0000-0000-0000", "username": user, "password": pass,
	}); status != http.StatusUnprocessableEntity || !strings.Contains(string(body), `"setupCode":"incorrect"`) {
		t.Fatalf("a wrong code answered %d, want 422 incorrect on setupCode: %s", status, body)
	}

	// Two browsers at once with the right code: one administrator, one 409.
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	for i := range statuses {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Retyped the way a person does: lower case, spaces for dashes.
			statuses[i], _ = w.post("/auth/setup", map[string]string{
				"setupCode": strings.ToLower(strings.ReplaceAll(code, "-", " ")),
				"username":  user + string(rune('a'+i)), "password": pass,
			})
		}(i)
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, s := range statuses {
		switch s {
		case http.StatusOK:
			ok++
		case http.StatusConflict:
			conflict++
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("two racing setups answered %v, want one 200 and one 409", statuses)
	}
	var admins int
	if err := w.st.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM operators WHERE role = 'administrator' AND enabled`).Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Fatalf("%d administrators after a race for the setup, want exactly 1", admins)
	}

	// Closed for good, whatever the body says — the right code included.
	if status, body := w.post("/auth/setup", map[string]string{"setupCode": code, "username": "late", "password": pass}); status != http.StatusConflict {
		t.Fatalf("a second setup answered %d, want 409: %s", status, body)
	}
	var st gen.AdminSetupState
	w.getJSON("/auth/setup", &st)
	if st.Required {
		t.Fatal("getAdminSetup still says required after the setup completed")
	}

	// And the installer cannot reopen it now that somebody can administer.
	fresh, _ := identity.NewSetupCode()
	if err := w.st.ConsoleSetup().Open(context.Background(), identity.SetupCodeDigest(fresh)); !errors.Is(err, store.ErrSetupNotNeeded) {
		t.Fatalf("reopening with an administrator present: %v, want ErrSetupNotNeeded", err)
	}
}

// TestAnUnknownGuestUsernamePaysTheSameVerification: if the portal's
// `AuthenticateGuest` returned at once for a username it did not know, an argon2id
// earlier than for one it did, a stopwatch would be a username oracle.
func TestAnUnknownGuestUsernamePaysTheSameVerification(t *testing.T) {
	v := newVenue(t)
	count, restore := apistore.CountDecoyVerifications()
	defer restore()

	creds := v.adapter.PortalPorts().Credentials
	_, err := creds.AuthenticateGuest(context.Background(), v.site, uniq("nobody"), "whatever-was-typed")
	if !errors.Is(err, portal.ErrBadCredentials) {
		t.Fatalf("an unknown username answered %v, want ErrBadCredentials", err)
	}
	if count() != 1 {
		t.Fatalf("the unknown username ran %d decoy verifications, want 1: it answers faster than a wrong password", count())
	}
	// A known username with a wrong password runs the real one, not the decoy.
	if _, err := creds.AuthenticateGuest(context.Background(), v.site, v.accountUser, "not-the-password"); !errors.Is(err, portal.ErrBadCredentials) {
		t.Fatalf("a wrong password answered %v", err)
	}
	if count() != 1 {
		t.Fatalf("a known username ran the decoy too (%d)", count())
	}
}
