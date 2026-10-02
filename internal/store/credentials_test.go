package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// checkViolation is PostgreSQL's SQLSTATE for a failed CHECK constraint.
const checkViolation = "23514"

// requireCheckRefused asserts a write was refused by the CHECK constraint named,
// and not by something adjacent.
//
// The constraint name is not decoration, for the reason `docs/rules/database.md`
// records at length: a negative test whose subject has more than one way to fail is
// a test of whichever guard fires first, and you do not find out which until you
// remove one. `accounts.username` is `NOT NULL` and `site_id` is a foreign key; a
// row that trips either of those "fails" a SQLSTATE-only assertion while the
// credential column stays wide open.
func requireCheckRefused(t *testing.T, err error, what, constraint string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s SUCCEEDED. docs/rules/security.md §1 is then a comment: nothing stops a caller writing a "+
			"credential in a form MSCHAPv2 can use, or one rlm_pap rewrites into the password itself.", what)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s failed, but not with a PostgreSQL error: %v", what, err)
	}
	if pgErr.Code != checkViolation {
		t.Fatalf("%s failed with SQLSTATE %s (%s), want %s (check_violation). A different failure is not the "+
			"guarantee — a NOT NULL or a foreign key would also 'fail'.", what, pgErr.Code, pgErr.Message, checkViolation)
	}
	if pgErr.ConstraintName != constraint {
		t.Fatalf("%s was refused by %q, not by %q. Another constraint firing first leaves the credential column "+
			"unguarded and the test green.", what, pgErr.ConstraintName, constraint)
	}
}

// TestCredentialColumnsHoldArgon2idOnly is the schema half of the named security
// test in `docs/rules/testing.md`: *no argon2-hashed identity is ever reachable
// by MSCHAPv2*.
//
// The data-plane half is measured rather than asserted — `docs/protocol-reality.md`,
// FreeRADIUS 3.2.10 with a real supplicant: an identity whose only
// verifier is a one-way hash is refused with *"FAILED: No NT-Password. Cannot
// perform authentication"*, for argon2, bcrypt and yescrypt alike.
//
// What that measurement also shows is where the guarantee breaks, and it is not
// where the sentence points. It breaks on a **cleartext** value in the ordinary
// credential column: rlm_mschap derives the NT hash from `Cleartext-Password` on
// the fly, so one such row makes that identity MSCHAPv2-authenticable with no
// opt-in and no row in `account_nt_hashes` for the console to render from. And a
// verifier in a scheme FreeRADIUS does not recognise is worse still: rlm_pap
// rewrites an unknown `Password-With-Header` to `Cleartext-Password`, so the hash
// becomes the password — proven with an eapol_test run that authenticated by
// sending the stored argon2id string as the password.
//
// So the guarantee is enforced at the column, by the database, and this test
// attempts the write rather than re-implementing the rule in Go.
func TestCredentialColumnsHoldArgon2idOnly(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	// The real thing, from the real hasher, so the constraint is proven against
	// the shape the product actually writes rather than a literal that happens
	// to match. `internal/identity` is the authority for that shape; restating
	// it here would be the second-source-of-truth defect one file over.
	good, err := identity.HashPassword("a password nobody uses twice")
	fatalf(t, err, "hashing")

	t.Run("an argon2id verifier is accepted", func(t *testing.T) {
		acct := &store.Account{
			ID: uniq("acct"), SiteID: site.ID, Username: uniq("guest"),
			PasswordHash: good, Enabled: true,
		}
		fatalf(t, st.Accounts().Create(ctx, acct), "creating an account with an argon2id verifier")
	})

	// Each of these is a real way the column has been filled in some other
	// product, and each has a different consequence on the data plane.
	forbidden := []struct {
		name  string
		value string
		why   string
	}{
		{
			"cleartext", "correct-horse",
			"rlm_mschap derives the NT hash from Cleartext-Password, so this identity becomes " +
				"MSCHAPv2-authenticable without any opt-in",
		},
		{
			"an NT hash", "91C81BD7D0872AD66D3F917FEA9FB5FB",
			"password-equivalent material outside account_nt_hashes, unencrypted and unrecorded",
		},
		{
			"an SSHA digest", "{SSHA}bm90YXJlYWxoYXNodGhpc2lzZmFrZQ==",
			"a fast hash rlm_pap verifies happily; §1 says argon2id and means it",
		},
		{
			"a bcrypt verifier", "$2b$10$abcdefghijklmnopqrstuuf4yQswfQRuOxajohDXdzREFVDuc.Vdy",
			"measured to complete EAP-TTLS/PAP — which is a decision under §1, not something " +
				"a caller may take by choosing a different hasher",
		},
		{
			"an unrecognised header", "{argon2id}" + "$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA",
			"rlm_pap rewrites an unknown Password-With-Header to Cleartext-Password: the stored " +
				"string becomes the password",
		},
		{"empty", "", "no verifier at all"},
	}

	for _, f := range forbidden {
		t.Run("accounts refuse "+f.name, func(t *testing.T) {
			err := st.Accounts().Create(ctx, &store.Account{
				ID: uniq("acct"), SiteID: site.ID, Username: uniq("guest"),
				PasswordHash: f.value, Enabled: true,
			})
			requireCheckRefused(t, err, "writing "+f.name+" into accounts.password_hash ("+f.why+")",
				"accounts_password_hash_is_argon2id")
		})
	}

	// Operators separately, because §1 names both tables and because the failure
	// is worse there: an admin password is the console.
	//
	// `administrator` and not `admin`, and that is this file's own instance of
	// the hazard the helper above exists for. `admin` is refused by
	// `operators_role_check`, so **every operator subtest below would pass on the
	// wrong constraint** and go on passing with the credential CHECK removed.
	// Running the sabotage shows it: the operators half reports *"refused by
	// operators_role_check"*, which is a test telling you it is not measuring
	// what its name says.
	for _, f := range forbidden {
		t.Run("operators refuse "+f.name, func(t *testing.T) {
			err := st.Operators().Create(ctx, &store.Operator{
				ID: uniq("op"), Username: uniq("admin"), PasswordHash: f.value,
				Role: "administrator", Enabled: true,
			})
			requireCheckRefused(t, err, "writing "+f.name+" into operators.password_hash",
				"operators_password_hash_is_argon2id")
		})
	}
}

// TestTheAccountsTableHasNoSecondCredentialColumn.
//
// The constraint above guards the column everyone knows about. This guards
// against the next one: `docs/rules/security.md` §1 says NT hashes live in a
// separate table, and the reason is that no ordinary account query can then reach
// one by accident. A second credential column on `accounts` would defeat that
// silently, and it is exactly the shape a well-meaning "add MSCHAPv2 support"
// change takes.
//
// Derived from `information_schema`, not from a list of columns typed here: a
// hardcoded expected set is a stale copy of the schema the moment the schema
// moves, and `docs/rules/testing.md` has four instances of that costing time.
func TestTheAccountsTableHasNoSecondCredentialColumn(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()

	for _, table := range []string{"accounts", "operators"} {
		rows, err := st.Pool().Query(ctx, `
			SELECT column_name, data_type FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = $1`, table)
		fatalf(t, err, "reading the columns of %s", table)

		var suspicious []string
		for rows.Next() {
			var name, dataType string
			fatalf(t, rows.Scan(&name, &dataType), "scanning a column name")
			lower := strings.ToLower(name)
			if lower == "password_hash" {
				continue // the one credential column, and it is constrained above
			}
			// **A boolean cannot hold a credential, in any encoding.**
			//
			// The name markers below are the cheap half and they stay. What they
			// cannot see is whether the thing under the name could *be* a
			// credential, and that matters:
			// `operators.must_change_password` matches `password` and holds one bit.
			//
			// Not an allow-list of column names, which was the tempting repair and
			// is the one that rots -- an exemption for `must_change_password` would
			// go on excusing that name after somebody changed its type. Derived from
			// `information_schema` like everything else here, so the day this column
			// becomes `text` it is suspicious again with nobody editing this file.
			if dataType == "boolean" {
				continue
			}
			for _, marker := range []string{"nt_hash", "ntlm", "lm_hash", "cleartext", "password", "secret", "credential"} {
				if strings.Contains(lower, marker) {
					suspicious = append(suspicious, name)
					break
				}
			}
		}
		rows.Close()

		if len(suspicious) > 0 {
			t.Errorf("%s has credential-shaped columns beside password_hash: %v.\n"+
				"docs/rules/security.md §1 puts password-equivalent material in account_nt_hashes precisely so that no "+
				"ordinary account query can return one. A column here is reachable by every SELECT this package "+
				"already has, and by the constraint added in migration 00013 not at all.", table, suspicious)
		}
	}
}
