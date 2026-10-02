package store_test

import (
	"context"
	"testing"

	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// TestTheAccountAuthorizedIsTheAccountWhosePasswordIsChecked is the identity
// binding between the account the Policy API authorizes and the one rlm_pap checks.
//
// The generated virtual server verifies an account's User-Password against the
// verifier `radius_pap_verifiers` holds for the User-Name, and that namespace is
// global. A LookupGuestAccount that picked the OLDEST account with the name
// would split them: two venues each with a `reception`, the newer one opted in,
// the Policy API authorizing the older one and rlm_pap checking the newer one's
// password -- a guest let on as one identity on the strength of another's
// credential.
func TestTheAccountAuthorizedIsTheAccountWhosePasswordIsChecked(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	username := uniq("reception")

	hash, err := identity.HashPassword("correct-horse-battery-staple")
	fatalf(t, err, "hashing the argon2id verifier")
	create := func(minutes int) *store.Account {
		t.Helper()
		m := minutes
		a := &store.Account{
			ID: uniq("acc"), SiteID: seedSite(t, st).ID, Username: username,
			PasswordHash: hash, Enabled: true, Plan: store.Plan{DurationMinutes: &m},
		}
		fatalf(t, st.Accounts().Create(ctx, a), "creating account")
		return a
	}
	older := create(60)
	newer := create(30)

	// The control: with no verifier anywhere, the oldest is still the answer.
	got, err := st.LookupGuestAccount(ctx, username)
	fatalf(t, err, "looking the account up")
	if got.DurationMinutes != 60 {
		t.Fatalf("with no verifier the lookup answered the %d-minute account, want the older 60-minute one", got.DurationMinutes)
	}

	verifier, err := identity.HashPAPVerifier("the newer venue's password")
	fatalf(t, err, "hashing the bcrypt verifier")
	fatalf(t, st.Accounts().OptInTo8021X(ctx, newer.ID, verifier, nil), "opting the newer account in")

	got, err = st.LookupGuestAccount(ctx, username)
	fatalf(t, err, "looking the account up")
	if got.DurationMinutes != 30 {
		t.Fatalf("the Policy API authorized the %d-minute account (id %s) while rlm_pap will check the password "+
			"of the 30-minute one (id %s), which owns the only verifier for %q: the allow and the proof are about "+
			"two different identities", got.DurationMinutes, older.ID, newer.ID, username)
	}

	// And a disabled owner is a refusal, not a fall-back to the other account.
	pool := ownerPool(t)
	if _, err := pool.Exec(ctx, `UPDATE accounts SET enabled = false WHERE id = $1`, newer.ID); err != nil {
		t.Fatalf("disabling: %v", err)
	}
	got, err = st.LookupGuestAccount(ctx, username)
	fatalf(t, err, "looking the account up")
	if got.Enabled || got.DurationMinutes != 30 {
		t.Fatalf("with the verifier's owner disabled the lookup answered enabled=%v, %d minutes; want the "+
			"disabled owner, so that the engine refuses", got.Enabled, got.DurationMinutes)
	}
}
