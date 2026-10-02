package apistore_test

import (
	"context"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity"
)

// `credentialKind` is a security statement, and it is asserted here because the
// place it can go wrong is not the place it is computed.
//
// `docs/rules/security.md` §1 is explicit that an identity which authenticates
// to the network over 802.1X is verified against **bcrypt**, not argon2id, and that
// a console calling it argon2 describes something the data plane cannot do. The
// value is derived in `internal/store` from a `LEFT JOIN` per opt-in table — a flag
// can be wrong, a join cannot — and `internal/store/papverifier_test.go` pins that
// derivation.
//
// **What was never pinned is the seam.** `accountOut` casts the store's string
// straight into `gen.AccountCredentialKind`, so a widening of the enum on either
// side, or a `Get` that stopped selecting the computed column, would reach the
// console as `argon2id` for an identity that is reachable by EAP-TTLS/PAP — the one
// sentence §1 forbids — with every store test still green. This drives the whole
// path the operator's screen uses: opt in through the repository, read back through
// the adapter, over the same HTTP surface the console calls.
//
// ── how this fails ───────────────────────────────────────────────────────────
//
// Deleting the `+bcrypt` arm of `accountColumns` in `internal/store/accounts.go` —
// the `CASE` over `account_pap_verifiers`, which is the whole of the product
// behaviour under test — turns both halves red:
//
//	credentialKind is "argon2id" for an identity with an 802.1X verifier
//
// and it stays red for the both-opt-ins case, which is what says the assertion
// reaches the value rather than the shape.
func TestCredentialKindNamesTheEightZeroTwoOneXVerifier(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	acc, err := w.adapter.CreateAccount(ctx, gen.AccountCreate{
		SiteId: w.siteA, Username: uniq("dot1x"), Password: randomString() + "Aa1",
	})
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	if acc.CredentialKind != gen.Argon2id {
		t.Fatalf("a new account is %q, want argon2id — every identity has the argon2id "+
			"verifier and only that", acc.CredentialKind)
	}

	verifier, err := identity.HashPAPVerifier(randomString() + "Aa1")
	if err != nil {
		t.Fatalf("hashing the 802.1X verifier: %v", err)
	}
	if err := w.st.Accounts().OptInTo8021X(ctx, acc.Id, verifier, &w.adminUser); err != nil {
		t.Fatalf("opting in to 802.1X: %v", err)
	}

	if got := w.credentialKind(acc.Id); got != gen.Argon2idBcrypt {
		t.Fatalf("credentialKind is %q for an identity with an 802.1X verifier, want "+
			"argon2id+bcrypt. docs/rules/security.md §1: an identity that authenticates over 802.1X "+
			"is verified against bcrypt, and a console that renders that as argon2 is "+
			"describing something rlm_pap cannot do.", got)
	}

	// Both opt-ins, because the two are independent and the enum can hold the
	// combination. It is the case a `+bcrypt`-only implementation gets wrong in the
	// other direction: an NT hash is password-equivalent and must not disappear
	// from the answer because a second verifier arrived beside it.
	if err := w.st.Accounts().OptInToMSCHAPv2(ctx, acc.Id, make([]byte, 16), &w.adminUser); err != nil {
		t.Fatalf("opting in to MSCHAPv2: %v", err)
	}
	if got := w.credentialKind(acc.Id); got != gen.Argon2idNtBcrypt {
		t.Fatalf("credentialKind is %q for an identity with both opt-ins, want argon2id+nt+bcrypt", got)
	}

	// And it goes back. The kind is computed from rows, so removing the row is the
	// whole of taking the identity back out of EAP-TTLS/PAP's reach.
	if err := w.st.Accounts().OptOutOf8021X(ctx, acc.Id); err != nil {
		t.Fatalf("opting out of 802.1X: %v", err)
	}
	if got := w.credentialKind(acc.Id); got != gen.Argon2idNt {
		t.Fatalf("credentialKind is %q after opting out of 802.1X, want argon2id+nt", got)
	}
}

// credentialKind reads one account back through the adapter's own list port.
//
// Through `Accounts` rather than a repository read, because the list is what the
// console renders and the cast from the store's string lives on that path.
func (w *world) credentialKind(id string) gen.AccountCredentialKind {
	w.t.Helper()
	page, err := w.adapter.Accounts(context.Background(), w.siteA, "", api.Window{Limit: 200})
	if err != nil {
		w.t.Fatalf("listing accounts: %v", err)
	}
	for _, a := range page.Items {
		if a.Id == id {
			return a.CredentialKind
		}
	}
	w.t.Fatalf("account %s is not in its own site's listing", id)
	return ""
}
