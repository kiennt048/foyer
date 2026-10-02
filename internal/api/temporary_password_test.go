package api_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The forced password change, and the reason every case here drives the SERVER.
//
// No install ships a password (`handlers_setup.go`); the flag's only writer is
// `foyerctl create-operator --must-change-password`, the host-side way to hand
// somebody a temporary password, and the gate still has to be real.
//
// A console that merely *routes* to the change-password screen has forced nothing:
// a second browser, a stale tab, a script, or `curl` with the session cookie all
// walk straight past it. Every case below asserts on a wire response with no client
// in the picture.

// signInOnATemporaryPassword signs in as the administrator and marks that operator
// as holding a password somebody else set.
//
// The order matters: the flag is read per request rather than remembered at sign-in,
// so setting it after signing in is the harder case.
func signInOnATemporaryPassword(t *testing.T, r *rig) {
	t.Helper()
	r.signIn(adminUser)
	r.store.SetMustChangePassword("op-admin", true)
}

func TestEveryOperationIsRefusedOnATemporaryPassword(t *testing.T) {
	r := newRig(t)
	signInOnATemporaryPassword(t, r)

	// A read, a write, and an operation on a different tag each. A gate that
	// only covered writes would leave an unauthorised person on the venue LAN
	// reading the guest list, the session history and the access-point
	// inventory, which is not a smaller problem than writing to them.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/nas?siteId=" + testSite, nil},
		{http.MethodGet, "/sessions?siteId=" + testSite, nil},
		{http.MethodGet, "/operators", nil},
		{http.MethodGet, "/overview?siteId=" + testSite, nil},
		{http.MethodPost, "/sites", map[string]string{"name": "Another"}},
	} {
		t.Run(c.method+" "+c.path, func(t *testing.T) {
			resp := r.do(c.method, c.path, c.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("got %d, want 403: %s", resp.StatusCode, readAll(t, resp))
			}
			var p gen.Problem
			if err := json.NewDecoder(resp.Body).Decode(&p); err != nil {
				t.Fatalf("decode: %v", err)
			}
			// Naming the type, not just the status: the client's answer to
			// `forbidden` is to render a refusal and its answer to this one is to
			// route to the single screen that clears it. A test satisfied by any
			// 403 could not tell a working gate from one that reported the wrong
			// reason and left the console showing "you may not do this" with no
			// way forward.
			if p.Type != gen.ProblemspasswordChangeRequired {
				t.Errorf("type %q, want /problems/password-change-required", p.Type)
			}
		})
	}
}

// TestTheWayOutIsOpenOnATemporaryPassword is the other half, and without it the case above is
// satisfied by refusing everything — which is a console nobody can rescue.
func TestTheWayOutIsOpenOnATemporaryPassword(t *testing.T) {
	r := newRig(t)
	signInOnATemporaryPassword(t, r)

	resp := r.do(http.MethodGet, "/auth/session", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("getAuthSession got %d, want 200: %s", resp.StatusCode, readAll(t, resp))
	}
	var s gen.AuthSession
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !s.MustChangePassword {
		t.Error("getAuthSession answers mustChangePassword=false while the server is refusing everything else for that reason — a console reading this would route to the dashboard and see every call fail")
	}

	change := r.do(http.MethodPost, "/auth/password", map[string]string{
		"currentPassword": testSecret,
		"newPassword":     "a-password-nobody-shipped",
	})
	defer change.Body.Close()
	if change.StatusCode != http.StatusNoContent {
		t.Fatalf("changeOwnPassword got %d, want 204: %s", change.StatusCode, readAll(t, change))
	}
}

// TestChangingThePasswordOpensTheConsole is the transition, asserted end to end
// because that is the sentence the product makes: change it, and everything works.
func TestChangingThePasswordOpensTheConsole(t *testing.T) {
	r := newRig(t)
	signInOnATemporaryPassword(t, r)

	before := r.do(http.MethodGet, "/nas?siteId="+testSite, nil)
	before.Body.Close()
	if before.StatusCode != http.StatusForbidden {
		t.Fatalf("the gate was not closed to begin with (%d), so this test observes nothing", before.StatusCode)
	}

	change := r.do(http.MethodPost, "/auth/password", map[string]string{
		"currentPassword": testSecret,
		"newPassword":     "a-password-nobody-shipped",
	})
	change.Body.Close()
	if change.StatusCode != http.StatusNoContent {
		t.Fatalf("changeOwnPassword: %d", change.StatusCode)
	}

	after := r.do(http.MethodGet, "/nas?siteId="+testSite, nil)
	defer after.Body.Close()
	if after.StatusCode != http.StatusOK {
		t.Fatalf("still refused after the password was changed (%d): the flag did not clear, and the operator has done what the console asked with nothing to show for it: %s",
			after.StatusCode, readAll(t, after))
	}

	session := r.do(http.MethodGet, "/auth/session", nil)
	defer session.Body.Close()
	var s gen.AuthSession
	if err := json.NewDecoder(session.Body).Decode(&s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.MustChangePassword {
		t.Error("mustChangePassword is still true after a successful change")
	}
}

// TestTheTemporaryPasswordCannotBeChosenAgain is what keeps the forced change from
// being a formality satisfied by retyping what you were handed.
func TestTheTemporaryPasswordCannotBeChosenAgain(t *testing.T) {
	r := newRig(t)
	signInOnATemporaryPassword(t, r)

	resp := r.do(http.MethodPost, "/auth/password", map[string]string{
		"currentPassword": testSecret,
		"newPassword":     testSecret,
	})
	body := readAll(t, resp)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("the temporary password was accepted as the new one (%d): %s", resp.StatusCode, body)
	}
	var p gen.Problem
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if p.Errors == nil || (*p.Errors)["newPassword"] != "Same as the current one." {
		t.Errorf("refused by some other rule: %s", body)
	}
}

// TestAnOperatorWhoChoseTheirOwnPasswordIsNotAsked keeps the gate off everybody it
// was not built for. Without this, the feature is indistinguishable from a console
// that refuses every request.
func TestAnOperatorWhoChoseTheirOwnPasswordIsNotAsked(t *testing.T) {
	r := newRig(t)
	r.signIn(adminUser)

	resp := r.do(http.MethodGet, "/nas?siteId="+testSite, nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d, want 200 for an operator never given a temporary password: %s", resp.StatusCode, readAll(t, resp))
	}

	session := r.do(http.MethodGet, "/auth/session", nil)
	defer session.Body.Close()
	var s gen.AuthSession
	if err := json.NewDecoder(session.Body).Decode(&s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.MustChangePassword {
		t.Error("mustChangePassword is true for an operator that was never marked")
	}
}
