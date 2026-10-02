package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// The second verifier — the one that completes EAP-TTLS/PAP — and the four things
// about it that must be true in the database rather than in a comment.
//
// What makes it acceptable where PEAP-MSCHAPv2 is not is a single property: a
// bcrypt verifier is one-way, so `rlm_sql` may read it, where an NT hash is
// password-equivalent and may not.
// Everything below is that property being kept honest.

func seedAccount(t *testing.T, st *store.Store, siteID, username string) *store.Account {
	t.Helper()
	hash, err := identity.HashPassword("correct-horse-battery-staple")
	fatalf(t, err, "hashing the argon2id verifier")
	a := &store.Account{
		ID:           uniq("acc"),
		SiteID:       siteID,
		Username:     username,
		PasswordHash: hash,
		Enabled:      true,
	}
	fatalf(t, st.Accounts().Create(context.Background(), a), "creating account")
	return a
}

// TestOptInTo8021XIsVisibleInCredentialKind — the console's only signal that an
// identity authenticates to the network against something that is not argon2id.
//
// It is a `LEFT JOIN`, not a flag, for the reason the NT half already is: a flag
// can be wrong.
func TestOptInTo8021XIsVisibleInCredentialKind(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	acc := seedAccount(t, st, site.ID, uniq("u"))

	got, err := st.Accounts().Get(ctx, acc.ID)
	fatalf(t, err, "reading the account back")
	if got.CredentialKind != "argon2id" {
		t.Fatalf("credentialKind is %q before any opt-in, want argon2id", got.CredentialKind)
	}

	verifier, err := identity.HashPAPVerifier("correct-horse-battery-staple")
	fatalf(t, err, "hashing the bcrypt verifier")
	by := "operator@example.test"
	fatalf(t, st.Accounts().OptInTo8021X(ctx, acc.ID, verifier, &by), "opting in to 802.1X")

	got, err = st.Accounts().Get(ctx, acc.ID)
	fatalf(t, err, "reading the account back after opt-in")
	if got.CredentialKind != "argon2id+bcrypt" {
		t.Fatalf("credentialKind is %q after opting in to 802.1X, want argon2id+bcrypt.\n"+
			"The console reads this string to tell an operator that the network does NOT verify\n"+
			"this identity against argon2id. A kind that does not change is a console that says\n"+
			"argon2id for an identity whose 802.1X verifier is bcrypt.", got.CredentialKind)
	}

	fatalf(t, st.Accounts().OptOutOf8021X(ctx, acc.ID), "opting out of 802.1X")
	got, err = st.Accounts().Get(ctx, acc.ID)
	fatalf(t, err, "reading the account back after opt-out")
	if got.CredentialKind != "argon2id" {
		t.Fatalf("credentialKind is %q after opting out, want argon2id", got.CredentialKind)
	}
}

// TestPAPVerifierColumnHoldsBcryptOnly is the sibling of
// TestCredentialColumnsHoldArgon2idOnly, and the values it refuses are the ones
// that would each fail differently and badly.
func TestPAPVerifierColumnHoldsBcryptOnly(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	acc := seedAccount(t, st, site.ID, uniq("u"))
	pool := st.Pool()

	argon2id, err := identity.HashPassword("correct-horse-battery-staple")
	fatalf(t, err, "hashing argon2id")

	for _, tc := range []struct {
		what  string
		value string
		why   string
	}{
		{
			what:  "a cleartext password",
			value: "correct-horse-battery-staple",
			why: "rlm_mschap derives the NT hash from Cleartext-Password on the fly, so this row " +
				"would make the identity MSCHAPv2-authenticable with no opt-in anywhere",
		},
		{
			what:  "an argon2id verifier",
			value: argon2id,
			why: "libxcrypt answers its failure token *0 for $argon2id$ — measured — so this identity " +
				"would simply never authenticate, and the failure would be blamed on the supplicant",
		},
		{
			what:  "an NT hash",
			value: "8846f7eaee8fb117ad06bdd830b7586c",
			why:   "password-equivalent material belongs in the enveloped table or nowhere",
		},
		{
			what:  "a Password-With-Header form",
			value: "{crypt}$2a$12$kj9KyF2WkWElv68bfOHVkuJXKcivZ4PVyAc8IuM2Y8iej.URU3jLW",
			why: "rlm_pap rewrites an unrecognised Password-With-Header to Cleartext-Password, and " +
				"nothing in Foyer may ever emit that attribute",
		},
		{
			what:  "an empty string",
			value: "",
			why:   "NOT NULL does not stop the empty string, and rlm_pap would compare against it",
		},
	} {
		_, err := pool.Exec(ctx, `
			INSERT INTO account_pap_verifiers (account_id, username, verifier)
			VALUES ($1, $2, $3)`, acc.ID, acc.Username, tc.value)
		requireCheckRefused(t, err, "storing "+tc.what+" as an 802.1X verifier ("+tc.why+")",
			"account_pap_verifiers_is_bcrypt")
	}

	// And the one form that must be accepted, so the CHECK is not simply refusing
	// everything — a constraint nothing can satisfy is a feature that does not work
	// and a test that cannot tell.
	verifier, err := identity.HashPAPVerifier("correct-horse-battery-staple")
	fatalf(t, err, "hashing the bcrypt verifier")
	fatalf(t, st.Accounts().OptInTo8021X(ctx, acc.ID, verifier, nil), "storing a real bcrypt verifier")
}

// TestThe8021XUsernameIsGlobal — the finding that shaped the table.
//
// `accounts` is unique per (tenant, site, username). FreeRADIUS's identity
// namespace is flat: a supplicant sends a username and there is no site in the
// packet. Two opted-in identities sharing a username make the credential query
// ambiguous, and the failure is a SUCCESS — one site's password authenticating
// another site's identity, with nothing in the log to say which row answered.
func TestThe8021XUsernameIsGlobal(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()

	cafe := seedSite(t, st)
	hotel := seedSite(t, st)
	shared := uniq("shared")

	inCafe := seedAccount(t, st, cafe.ID, shared)
	inHotel := seedAccount(t, st, hotel.ID, shared)

	verifier, err := identity.HashPAPVerifier("correct-horse-battery-staple")
	fatalf(t, err, "hashing the bcrypt verifier")

	fatalf(t, st.Accounts().OptInTo8021X(ctx, inCafe.ID, verifier, nil), "opting the first identity in")

	err = st.Accounts().OptInTo8021X(ctx, inHotel.ID, verifier, nil)
	if err == nil {
		t.Fatal("a SECOND identity with the same username was opted in to 802.1X.\n" +
			"An 802.1X supplicant sends a username with no site in it, so the credential query now\n" +
			"returns two verifiers for one User-Name. That is a cross-site authentication bypass\n" +
			"that SUCCEEDS rather than failing, and no packet or log says which row answered.")
	}
	if !errors.Is(err, store.ErrPAPUsernameTaken) {
		t.Fatalf("the refusal was %v, not ErrPAPUsernameTaken. A generic conflict cannot be rendered\n"+
			"as the thing an operator has to do about it.", err)
	}
	// The one thing a flat namespace buys: the refusal can say what it collided
	// with. Same argument as `nas.nasname` naming the other tenant.
	if !strings.Contains(err.Error(), cafe.ID) {
		t.Fatalf("the refusal does not name the site already holding the username: %v\n"+
			"An operator facing 'that name is taken' with no way to find out where cannot act on it.", err)
	}

	// And the first identity is untouched — a refused second write must not have
	// half-applied.
	got, err := st.Accounts().Get(ctx, inCafe.ID)
	fatalf(t, err, "reading the first identity back")
	if got.CredentialKind != "argon2id+bcrypt" {
		t.Fatalf("the first identity is %q after the second was refused", got.CredentialKind)
	}
}

// TestTheRADIUSRoleReadsTheViewAndNothingElse.
//
// Two halves, and they fail in opposite directions:
//
//   - The view must RETURN ROWS to `foyer_radius`. This is the `nas` trap one table
//     over: every table here carries an RLS policy, FreeRADIUS has no concept of a
//     tenant and never sets the GUC, and an unset GUC matches no row. A view that
//     came back empty would look like every corporate laptop's password being
//     suddenly wrong, and nothing would be red.
//   - The base table must be REFUSED to `foyer_radius`, naming the table, so the
//     data plane cannot read `opted_in_by`, or a disabled account's verifier, or
//     join to anything.
func TestTheRADIUSRoleReadsTheViewAndNothingElse(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	username := uniq("u")
	acc := seedAccount(t, st, site.ID, username)

	verifier, err := identity.HashPAPVerifier("correct-horse-battery-staple")
	fatalf(t, err, "hashing the bcrypt verifier")
	fatalf(t, st.Accounts().OptInTo8021X(ctx, acc.ID, verifier, nil), "opting in to 802.1X")

	// This reads "no tenant id", and it is not: `withDefaults` turns "" into the
	// default tenant, and storeAsRole is SET ROLE from the superuser. The version
	// of this assertion that really has no GUC and really logs in as foyer_radius is
	// TestTheCredentialViewIsMinimallyPrivilegedAndStillAnswers; this one stays
	// for the column-set and base-table halves, which do not depend on either.
	radius := storeAsRole(t, "foyer_radius", "")
	pool := radius.Pool()

	t.Run("the view answers", func(t *testing.T) {
		var got string
		err := pool.QueryRow(ctx,
			`SELECT verifier FROM radius_pap_verifiers WHERE username = $1`, username).Scan(&got)
		if err != nil {
			t.Fatalf("the RADIUS role could not read the credential view: %v\n"+
				"If this is 'no rows', the view is inheriting a row-level policy from a role that never\n"+
				"sets foyer.tenant_id — the failure `docs/rules/database.md` records for `nas`, where\n"+
				"the server loads nothing and discards every packet. Here it would be every 802.1X\n"+
				"authentication failing, and it would read as a password problem.", err)
		}
		if got != verifier {
			t.Fatalf("the view returned a different verifier than was stored")
		}
	})

	t.Run("the base table is refused", func(t *testing.T) {
		_, err := pool.Exec(ctx, `SELECT verifier FROM account_pap_verifiers`)
		requireRefused(t, err, "the RADIUS role reading account_pap_verifiers", "account_pap_verifiers")
	})

	t.Run("the view exposes two columns and no more", func(t *testing.T) {
		rows, err := pool.Query(ctx, `
			SELECT column_name FROM information_schema.columns
			WHERE table_name = 'radius_pap_verifiers' ORDER BY column_name`)
		fatalf(t, err, "reading the view's columns")
		defer rows.Close()
		var cols []string
		for rows.Next() {
			var c string
			fatalf(t, rows.Scan(&c), "scanning a column name")
			cols = append(cols, c)
		}
		want := "username,verifier"
		if strings.Join(cols, ",") != want {
			t.Fatalf("the credential view exposes %v, want [%s].\n"+
				"docs/rules/security.md §2 calls this a minimally-privileged view. A column added here is a column\n"+
				"the data plane can read, and `radiusd -X` prints what it read.", cols, want)
		}
	})

	t.Run("a disabled account is absent, not merely filtered later", func(t *testing.T) {
		enabled := false
		_, err := st.Accounts().Update(ctx, acc.ID, store.AccountPatch{Enabled: &enabled})
		fatalf(t, err, "disabling the account")

		var n int
		err = pool.QueryRow(ctx,
			`SELECT count(*) FROM radius_pap_verifiers WHERE username = $1`, username).Scan(&n)
		fatalf(t, err, "counting the view's rows for a disabled account")
		if n != 0 {
			t.Fatalf("a disabled account still has %d row(s) in the credential view.\n"+
				"`enabled` is applied in the view rather than in the generated query on purpose: a query\n"+
				"lives in emitted configuration and can be regenerated without it, and a disabled staff\n"+
				"account that still authenticates over 802.1X is the exact incident disabling exists for.", n)
		}
	})
}
