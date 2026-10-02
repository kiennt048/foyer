package store_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// Least-privileged logins. FreeRADIUS and the application once both connected to
// PostgreSQL as the superuser. These are the criteria that close that gap, as tests,
// each driven through a REAL login rather than `SET ROLE` from the schema
// owner — because `SET ROLE` from the schema owner is precisely the arrangement that was
// wrong, and a harness built on it could not see the defect it exists to catch.
//
// (`storeAsRole`, used by most of this package, is that arrangement. It remains
// right for tests about grants, and wrong for tests about who logs in.)

// radiusConn logs in as FreeRADIUS does: its own role, and no tenant GUC at all,
// because FreeRADIUS has no concept of one.
func radiusConn(t *testing.T) *pgx.Conn {
	t.Helper()
	requireLogins(t)
	conn, err := pgx.Connect(context.Background(), radiusURL(t))
	fatalf(t, err, "logging in as %s", store.RadiusRole)
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// productStore opens the store exactly as cmd/foyer does: NewStore, one URL.
func productStore(t *testing.T) *store.Store {
	t.Helper()
	requireLogins(t)
	st, err := store.NewStore(context.Background(), appURL(t))
	fatalf(t, err, "opening the store the way cmd/foyer does")
	t.Cleanup(st.Close)
	return st
}

// Criterion 1: from each connection, `rolsuper` of current_user is false.
//
// session_user is asserted as well as current_user. After `SET ROLE` the two
// differ, and current_user alone is exactly the check a SET ROLE arrangement
// passes: the application's current_user would be foyer_app, and its
// session_user the superuser that RESET ROLE returns to.
func TestNoConnectionIsASuperuser(t *testing.T) {
	ctx := context.Background()

	check := func(t *testing.T, q interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}, wantUser string) {
		t.Helper()
		var cur, sess string
		var curSuper, sessSuper bool
		err := q.QueryRow(ctx, `
			SELECT current_user, (SELECT rolsuper FROM pg_roles WHERE rolname = current_user),
			       session_user, (SELECT rolsuper FROM pg_roles WHERE rolname = session_user)`).
			Scan(&cur, &curSuper, &sess, &sessSuper)
		fatalf(t, err, "reading who this connection is")
		if curSuper || sessSuper {
			t.Fatalf("current_user %s (super=%v), session_user %s (super=%v): a connection that is a superuser in either sense is a shell on the database host one injection away",
				cur, curSuper, sess, sessSuper)
		}
		if sess != wantUser {
			t.Fatalf("session_user is %s, want %s: the login itself must be the least-privileged role", sess, wantUser)
		}
	}

	t.Run("the application", func(t *testing.T) {
		check(t, productStore(t).Pool(), store.DefaultAppRole)
	})
	t.Run("FreeRADIUS", func(t *testing.T) {
		check(t, radiusConn(t), store.RadiusRole)
	})
}

// The product refuses an over-privileged login rather than tolerating it.
//
// This is the test that is red against a `NewStore` that accepts the schema
// owner's URL, SETs ROLE foyer_app, and runs.
func TestTheControlPlaneRefusesAnOverPrivilegedLogin(t *testing.T) {
	requireDocker(t)
	_, err := store.NewStore(context.Background(), ownerURL(t))
	if err == nil {
		t.Fatal("NewStore accepted a superuser login; RESET ROLE would make every statement the superuser again")
	}
	if !errors.Is(err, store.ErrOverPrivilegedLogin) {
		t.Fatalf("refused, but not as ErrOverPrivilegedLogin: %v", err)
	}
	if !strings.Contains(err.Error(), "SUPERUSER") {
		t.Errorf("the refusal does not say what is wrong with the login: %v", err)
	}
	if u, _ := url.Parse(ownerURL(t)); u != nil {
		if pw, ok := u.User.Password(); ok && strings.Contains(err.Error(), pw) {
			t.Fatal("the refusal printed the database password")
		}
	}
}

// Criterion 3: RESET ROLE from the application gains nothing.
//
// Driven on one held connection from the product's own pool, which is where an
// injected statement would run. Every escalation an attacker would try next is
// tried, and each must be refused by PostgreSQL naming what it refused.
func TestResetRoleFromTheApplicationGainsNothing(t *testing.T) {
	ctx := context.Background()
	st := productStore(t)
	conn, err := st.Pool().Acquire(ctx)
	fatalf(t, err, "acquiring a connection")
	defer conn.Release()

	if _, err := conn.Exec(ctx, `RESET ROLE`); err != nil {
		t.Fatalf("RESET ROLE: %v", err)
	}
	var cur string
	var super, bypass bool
	fatalf(t, conn.QueryRow(ctx, `
		SELECT current_user, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).
		Scan(&cur, &super, &bypass), "reading the role after RESET ROLE")
	if cur != store.DefaultAppRole || super || bypass {
		t.Fatalf("after RESET ROLE the session is %s (super=%v, bypassrls=%v); want %s with neither",
			cur, super, bypass, store.DefaultAppRole)
	}

	t.Run("radacct is still read-only", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			INSERT INTO radacct (acctsessionid, acctuniqueid, nasipaddress)
			VALUES ('after-reset', 'after-reset', '10.250.0.1')`)
		requireRefused(t, err, "INSERT INTO radacct after RESET ROLE", "radacct")
	})

	owner := "foyer"
	if u, err := url.Parse(ownerURL(t)); err == nil {
		owner = u.User.Username()
	}
	for _, role := range []string{store.RadiusRole, "foyer_reaper", "foyer_credential_view", owner} {
		t.Run("cannot SET ROLE "+role, func(t *testing.T) {
			_, err := conn.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize())
			requireSQLState(t, err, "SET ROLE "+role, insufficientPrivilege, "permission denied to set role")
		})
	}

	t.Run("cannot SET SESSION AUTHORIZATION", func(t *testing.T) {
		_, err := conn.Exec(ctx, "SET SESSION AUTHORIZATION "+pgx.Identifier{owner}.Sanitize())
		requireSQLState(t, err, "SET SESSION AUTHORIZATION", insufficientPrivilege, "permission denied to set session authorization")
	})

	t.Run("cannot create a table", func(t *testing.T) {
		_, err := conn.Exec(ctx, `CREATE TABLE escalation_probe (x int)`)
		requireSQLState(t, err, "CREATE TABLE", insufficientPrivilege, "permission denied for schema public")
	})

	t.Run("cannot give itself a privilege", func(t *testing.T) {
		_, err := conn.Exec(ctx, `ALTER ROLE foyer_app BYPASSRLS`)
		requireSQLState(t, err, "ALTER ROLE foyer_app BYPASSRLS", insufficientPrivilege, "permission denied to alter role")
	})

	t.Run("owns nothing and can reach no other role", func(t *testing.T) {
		p, err := store.LoginPrivileges(ctx, conn)
		fatalf(t, err, "reading the login's privileges")
		if problems := p.Problems(store.DefaultAppRole); len(problems) > 0 {
			t.Fatalf("the application login holds more than foyer_app: %s", strings.Join(problems, "; "))
		}
	})
}

// Criterion 2: from FreeRADIUS's login, operator and secret tables are refused.
//
// A fixed list of tenant tables, each refusal asserted by name. A table added
// later is NOT in this list, which is why
// TestTheRADIUSLoginHoldsExactlyItsGrants reads the catalog instead. The positive
// half below is what stops "refuse everything" from passing as least privilege,
// because a FreeRADIUS that can read nothing starts no server.
func TestTheRADIUSLoginIsRefusedEverythingButItsOwn(t *testing.T) {
	ctx := context.Background()
	conn := radiusConn(t)

	for _, table := range []string{
		"operators", "operator_sessions", "audit_log", "sites", "tenants",
		"controllers", "controller_vendor_sites", "shared_secrets", "radius_clients", "access_points",
		"voucher_batches", "vouchers", "accounts", "account_nt_hashes", "account_pap_verifiers",
		"portal_settings", "portal_hero_images", "walled_garden_entries",
		"portal_grants", "portal_sessions", "authorization_events", "goose_db_version",
	} {
		t.Run("refused "+table, func(t *testing.T) {
			var n int
			err := conn.QueryRow(ctx, "SELECT count(*) FROM "+pgx.Identifier{table}.Sanitize()).Scan(&n)
			requireRefused(t, err, "FreeRADIUS SELECT on "+table, table)
		})
	}

	t.Run("refused DELETE on radacct", func(t *testing.T) {
		_, err := conn.Exec(ctx, `DELETE FROM radacct WHERE acctuniqueid = 'nothing'`)
		requireRefused(t, err, "FreeRADIUS DELETE FROM radacct", "radacct")
	})

	t.Run("refused writing nas", func(t *testing.T) {
		_, err := conn.Exec(ctx, `UPDATE nas SET secret = 'x' WHERE false`)
		requireRefused(t, err, "FreeRADIUS UPDATE nas", "nas")
	})

	// The other half. These are the statements the generated tree issues
	// (internal/raddb/templates/queries.conf.tmpl), run as the role that issues them.
	owner := ownerPool(t)
	uid := uniq("radius-acct")
	t.Cleanup(func() {
		_, _ = owner.Exec(ctx, `DELETE FROM radacct WHERE acctuniqueid = $1`, uid)
		_, _ = owner.Exec(ctx, `DELETE FROM radpostauth WHERE username = $1`, uid)
	})

	t.Run("reads the client list", func(t *testing.T) {
		rows, err := conn.Query(ctx, `SELECT id, nasname, shortname, type, secret, server FROM nas`)
		fatalf(t, err, "the client_query FreeRADIUS runs at startup")
		rows.Close()
		fatalf(t, rows.Err(), "reading the client list")
	})
	t.Run("writes accounting", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			INSERT INTO radacct (acctsessionid, acctuniqueid, nasipaddress, acctstarttime)
			VALUES ($1, $1, '10.250.0.2', now())`, uid)
		fatalf(t, err, "Accounting-Start as FreeRADIUS")
		_, err = conn.Exec(ctx, `UPDATE radacct SET acctstoptime = now() WHERE acctuniqueid = $1`, uid)
		fatalf(t, err, "Accounting-Stop as FreeRADIUS")
	})
	t.Run("writes the post-auth ledger", func(t *testing.T) {
		_, err := conn.Exec(ctx, `
			INSERT INTO radpostauth (username, reply, authdate, calledstationid, callingstationid, class)
			VALUES ($1, 'Access-Reject', now(), '', '', '')`, uid)
		fatalf(t, err, "post-auth as FreeRADIUS")
	})
}

// What FreeRADIUS's login holds, read from the catalog rather than probed table by
// table — so a grant a LATER migration adds to foyer_radius goes red here without
// anybody remembering to extend the list above.
//
// The test above names 21 tables and says a
// table outside its list "is a table somebody granted it after this test was
// written". Made to fail once: a migration that created a new table and granted
// foyer_radius SELECT on it left that test — and every other test in this package
// — green. The allowlist below is 00002's and 00016's grants, and nothing else;
// widening it is a decision, and this is where it has to be written down.
//
// It also asks the login itself what RESET ROLE could reach, which the SELECT
// probes above can miss: a membership granted WITH INHERIT FALSE gives a role no
// privilege until it runs `SET ROLE`, so every probe passes while `SET ROLE
// foyer_app` is one statement away.
func TestTheRADIUSLoginHoldsExactlyItsGrants(t *testing.T) {
	ctx := context.Background()
	conn := radiusConn(t)

	t.Run("it can reach no other role and owns nothing", func(t *testing.T) {
		p, err := store.LoginPrivileges(ctx, conn)
		fatalf(t, err, "reading the FreeRADIUS login's privileges")
		if problems := p.Problems(store.RadiusRole); len(problems) > 0 {
			t.Fatalf("the FreeRADIUS login holds more than %s: %s", store.RadiusRole, strings.Join(problems, "; "))
		}
	})

	want := map[string]string{
		"nas":                   "SELECT",
		"radacct":               "INSERT,SELECT,UPDATE",
		"radpostauth":           "INSERT,SELECT",
		"radius_pap_verifiers":  "SELECT",
		"radacct_radacctid_seq": "USAGE",
		"radpostauth_id_seq":    "USAGE",
	}

	// Table-level OR any-column privilege, because a column grant is a grant:
	// `GRANT SELECT (label) ON shared_secrets` makes `SELECT label` work without
	// making has_table_privilege say so. Asked of the role, as the schema owner, so the
	// answer does not depend on what the login can see of the catalog.
	rows, err := ownerPool(t).Query(ctx, `
		SELECT c.relname,
		       concat_ws(',',
		         CASE WHEN c.relkind <> 'S' AND (has_table_privilege($1, c.oid, 'DELETE'))     THEN 'DELETE' END,
		         CASE WHEN c.relkind <> 'S' AND (has_table_privilege($1, c.oid, 'INSERT')
		                                      OR has_any_column_privilege($1, c.oid, 'INSERT')) THEN 'INSERT' END,
		         CASE WHEN c.relkind <> 'S' AND (has_table_privilege($1, c.oid, 'REFERENCES')
		                                      OR has_any_column_privilege($1, c.oid, 'REFERENCES')) THEN 'REFERENCES' END,
		         CASE WHEN c.relkind =  'S' AND has_sequence_privilege($1, c.oid, 'SELECT')    THEN 'SELECT'
		              WHEN c.relkind <> 'S' AND (has_table_privilege($1, c.oid, 'SELECT')
		                                      OR has_any_column_privilege($1, c.oid, 'SELECT')) THEN 'SELECT' END,
		         CASE WHEN c.relkind <> 'S' AND has_table_privilege($1, c.oid, 'TRIGGER')      THEN 'TRIGGER' END,
		         CASE WHEN c.relkind <> 'S' AND has_table_privilege($1, c.oid, 'TRUNCATE')     THEN 'TRUNCATE' END,
		         CASE WHEN c.relkind =  'S' AND has_sequence_privilege($1, c.oid, 'UPDATE')    THEN 'UPDATE'
		              WHEN c.relkind <> 'S' AND (has_table_privilege($1, c.oid, 'UPDATE')
		                                      OR has_any_column_privilege($1, c.oid, 'UPDATE')) THEN 'UPDATE' END,
		         CASE WHEN c.relkind =  'S' AND has_sequence_privilege($1, c.oid, 'USAGE')     THEN 'USAGE' END)
		FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
		ORDER BY c.relname`, store.RadiusRole)
	fatalf(t, err, "reading foyer_radius's privileges from the catalog")
	defer rows.Close()

	seen := map[string]bool{}
	var extra []string
	for rows.Next() {
		var rel, held string
		fatalf(t, rows.Scan(&rel, &held), "scanning")
		seen[rel] = true
		if held != want[rel] {
			extra = append(extra, fmt.Sprintf("%s: holds {%s}, want {%s}", rel, held, want[rel]))
		}
	}
	fatalf(t, rows.Err(), "listing")
	for rel := range want {
		if !seen[rel] {
			extra = append(extra, rel+": expected in the schema and not there")
		}
	}
	if len(extra) > 0 {
		t.Fatalf("foyer_radius's privileges are not exactly 00002's and 00016's:\n  %s\n"+
			"A privilege the data plane does not use is one a packet parser could be talked into using.",
			strings.Join(extra, "\n  "))
	}
}

// No function in the schema runs as a superuser on a login role's behalf.
//
// A SECURITY DEFINER function executes with its OWNER's privileges, so one owned
// by the migrator and executable by foyer_app or foyer_radius is the RESET ROLE
// hole again with a different spelling: the login is least-privileged and one
// `SELECT f()` is not. There is none today; this is what keeps it that way.
func TestNoSuperuserOwnedFunctionRunsForALoginRole(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	rows, err := ownerPool(t).Query(ctx, `
		SELECT p.oid::regprocedure::text, r.rolname
		FROM pg_proc p JOIN pg_roles r ON r.oid = p.proowner
		WHERE p.pronamespace = 'public'::regnamespace
		  AND p.prosecdef
		  AND (r.rolsuper OR r.rolbypassrls)
		  AND (has_function_privilege($1, p.oid, 'EXECUTE') OR has_function_privilege($2, p.oid, 'EXECUTE'))
		ORDER BY 1`, store.DefaultAppRole, store.RadiusRole)
	fatalf(t, err, "listing SECURITY DEFINER functions")
	defer rows.Close()
	var found []string
	for rows.Next() {
		var fn, owner string
		fatalf(t, rows.Scan(&fn, &owner), "scanning")
		found = append(found, fn+" (owned by "+owner+")")
	}
	fatalf(t, rows.Err(), "listing")
	if len(found) > 0 {
		t.Fatalf("SECURITY DEFINER functions owned by a superuser and executable by a login role: %s",
			strings.Join(found, ", "))
	}
}

// The credential view answers FreeRADIUS's real login with no tenant GUC, under
// FORCE ROW LEVEL SECURITY, and it does so as an owner that is not a superuser.
//
// `TestTheRADIUSRoleReadsTheViewAndNothingElse` asserts the first half through
// `storeAsRole("foyer_radius", "")` — which sets the default tenant anyway, because
// `withDefaults` fills an empty TenantID, and runs as the superuser underneath. So
// it could not have seen either of the failures below, and they are the two that
// FORCE makes possible:
//
//   - the view's owner, once subject to row security, sees no row without the GUC,
//     and every 802.1X password is "wrong";
//   - the view's owner is a superuser, and the "minimally-privileged view"
//     internal/aaa/CONTRACT.md promises is executing with every privilege there is.
func TestTheCredentialViewIsMinimallyPrivilegedAndStillAnswers(t *testing.T) {
	ctx := context.Background()
	st := appStore(t)
	site := seedSite(t, st)

	enabledUser, disabledUser := uniq("u"), uniq("u")
	for _, u := range []string{enabledUser, disabledUser} {
		acc := seedAccount(t, st, site.ID, u)
		v, err := identity.HashPAPVerifier("correct-horse-battery-staple")
		fatalf(t, err, "hashing the bcrypt verifier")
		fatalf(t, st.Accounts().OptInTo8021X(ctx, acc.ID, v, nil), "opting in to 802.1X")
		if u == disabledUser {
			_, err := ownerPool(t).Exec(ctx, `UPDATE accounts SET enabled = false WHERE id = $1`, acc.ID)
			fatalf(t, err, "disabling the second account")
		}
	}

	t.Run("its owner is neither a superuser nor BYPASSRLS", func(t *testing.T) {
		var owner string
		var super, bypass bool
		fatalf(t, ownerPool(t).QueryRow(ctx, `
			SELECT r.rolname, r.rolsuper, r.rolbypassrls
			FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner
			WHERE c.oid = 'radius_pap_verifiers'::regclass`).Scan(&owner, &super, &bypass), "reading the view's owner")
		if super || bypass {
			t.Fatalf("radius_pap_verifiers is owned by %s (super=%v, bypassrls=%v): the view FreeRADIUS reads executes with that role's privileges",
				owner, super, bypass)
		}
	})

	conn := radiusConn(t)
	var guc *string
	fatalf(t, conn.QueryRow(ctx, `SELECT nullif(current_setting('foyer.tenant_id', true), '')`).Scan(&guc), "reading the GUC")
	if guc != nil {
		t.Fatalf("the FreeRADIUS login has foyer.tenant_id = %q; this test must run with none, as FreeRADIUS does", *guc)
	}

	t.Run("an enabled identity's verifier is readable", func(t *testing.T) {
		var got string
		err := conn.QueryRow(ctx, `SELECT verifier FROM radius_pap_verifiers WHERE username = $1`, enabledUser).Scan(&got)
		if err != nil {
			t.Fatalf("FreeRADIUS's own login could not read the credential view: %v\n"+
				"'no rows' here is every 802.1X authentication failing as a password problem", err)
		}
		if !strings.HasPrefix(got, "$2") {
			t.Fatalf("the view answered %q, which is not a bcrypt verifier", got)
		}
	})
	t.Run("a disabled identity is absent", func(t *testing.T) {
		var n int
		fatalf(t, conn.QueryRow(ctx, `SELECT count(*) FROM radius_pap_verifiers WHERE username = $1`, disabledUser).Scan(&n), "counting")
		if n != 0 {
			t.Fatal("a disabled account's verifier is readable through the view")
		}
	})
}

// FORCE ROW LEVEL SECURITY on every table that has a policy — including tables a
// later migration adds. The list in 00019 is a record of what it did; this is the
// rule, and it goes red the day a migration enables a policy and forgets FORCE.
func TestEveryRowLevelPolicyIsForced(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	rows, err := ownerPool(t).Query(ctx, `
		SELECT c.relname, c.relforcerowsecurity
		FROM pg_class c
		WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'r' AND c.relrowsecurity
		ORDER BY c.relname`)
	fatalf(t, err, "listing tables with row security")
	defer rows.Close()

	var enabled int
	var unforced []string
	for rows.Next() {
		var name string
		var forced bool
		fatalf(t, rows.Scan(&name, &forced), "scanning")
		enabled++
		if !forced {
			unforced = append(unforced, name)
		}
	}
	fatalf(t, rows.Err(), "listing")
	if enabled < 21 {
		t.Fatalf("only %d tables have row security enabled; migration 00019 forced 21, so a policy has been dropped", enabled)
	}
	if len(unforced) > 0 {
		t.Fatalf("row security is enabled and not FORCED on: %s. Without FORCE the table's owner bypasses every policy; add `ALTER TABLE … FORCE ROW LEVEL SECURITY` in the migration that enabled it",
			strings.Join(unforced, ", "))
	}
}

// `foyerctl doctor` reads migration status as the application, not as the
// schema owner. Migration 00019 grants what that needs, and nothing that writes.
func TestTheApplicationLoginCanReadMigrationStatus(t *testing.T) {
	requireLogins(t)
	states, err := store.MigrationStatus(context.Background(), appURL(t))
	fatalf(t, err, "reading migration status as foyer_app")
	for _, s := range states {
		if !s.Applied {
			t.Fatalf("%s reads as pending to the application", s.Name)
		}
	}
	// That it cannot APPLY one is `cannot create a table` above: Migrate on a
	// current database is a no-op for any login, so calling it here would prove
	// nothing either way.
}

// SetLoginPassword is a schema-owner operation and refuses what it must.
func TestSetLoginPasswordRefusesWhatItMust(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	strong := randomString() + randomString()

	for _, c := range []struct{ name, role, pw, want string }{
		{"the owner's own role", "foyer", strong, "not a login role"},
		{"the credential view's owner, which nothing logs in as", "foyer_credential_view", strong, "not a login role"},
		{"a short password", store.DefaultAppRole, "short", "floor"},
		{"a non-ASCII password", store.DefaultAppRole, strong + "é", "printable ASCII"},
		{"a password with a space", store.DefaultAppRole, strong + " x", "printable ASCII"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := store.SetLoginPassword(ctx, ownerURL(t), c.role, c.pw)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want an error containing %q", err, c.want)
			}
			if err != nil && len(c.pw) > 8 && strings.Contains(err.Error(), c.pw) {
				t.Fatal("the refusal printed the password")
			}
		})
	}

	// And what it sets is a SCRAM verifier, never the password: the server stores
	// what it was sent, so this is also what a statement log would have held.
	requireLogins(t)
	var stored string
	fatalf(t, ownerPool(t).QueryRow(ctx, `SELECT rolpassword FROM pg_authid WHERE rolname = $1`, store.RadiusRole).Scan(&stored), "reading pg_authid")
	if !strings.HasPrefix(stored, "SCRAM-SHA-256$4096:") {
		t.Fatalf("foyer_radius's stored password is not a SCRAM-SHA-256 verifier: %.20q…", stored)
	}
	if u, _ := url.Parse(radiusURL(t)); u != nil {
		if pw, _ := u.User.Password(); pw != "" && strings.Contains(stored, pw) {
			t.Fatal("pg_authid holds the cleartext password")
		}
	}
}

// requireSQLState asserts a refusal by SQLSTATE and by the object the message
// names, for the reason requireRefused gives.
func requireSQLState(t *testing.T, err error, what, code, message string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s SUCCEEDED from the application login", what)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s failed, but not with a PostgreSQL error: %v", what, err)
	}
	if pgErr.Code != code || !strings.Contains(pgErr.Message, message) {
		t.Fatalf("%s failed with %s %q; want %s mentioning %q", what, pgErr.Code, pgErr.Message, code, message)
	}
}
