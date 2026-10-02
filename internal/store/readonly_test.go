package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kiennt048/foyer/internal/store"
)

// `radacct` and `radpostauth` belong to FreeRADIUS, and this file is where that
// stops being a sentence in a rules document.
//
// It was a convention enforced by three separate mechanisms, all of which are
// things a person can be careful about and therefore things a person can forget: a
// line in docs/rules/radius.md, the repository's write-time guards that grep Go
// source for `INSERT INTO radacct`, and code review. The guards do not run in CI on
// somebody else's machine, and none of the three can see a query assembled from a variable.
//
// What runs regardless is `GRANT SELECT ON radacct TO foyer_app` and nothing more.
// The tests below drive the application role at the two tables and require
// PostgreSQL to refuse -- with 42501, `insufficient_privilege`, and not merely with
// some error, because "some error" is satisfied by a typo in the SQL.

// insufficientPrivilege is PostgreSQL's SQLSTATE for a refused permission.
const insufficientPrivilege = "42501"

// requireRefused asserts that a statement was refused for lack of privilege *on the
// named table*.
//
// The `table` argument is not decoration: without it, a deliberate break of this
// test does not turn it red.
//
// Granting `INSERT ON radacct TO foyer_app` -- the exact hole this file exists to
// catch -- would leave an SQLSTATE-only check green. `radacct.radacctid` is a
// bigserial, so the INSERT's default calls `nextval()`, which needs USAGE on the
// sequence, which `foyer_app` also does not have. PostgreSQL refuses with 42501
// either way, and an assertion on the SQLSTATE alone cannot tell "the table is
// protected" from "the sequence is protected and the table is wide open".
//
// Such a test looks like it is checking something and is checking something
// adjacent. Naming the object closes it.
func requireRefused(t *testing.T, err error, what, table string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s SUCCEEDED. This role can write %s, which it must never be able to do.", what, table)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s failed, but not with a PostgreSQL error: %v", what, err)
	}
	if pgErr.Code != insufficientPrivilege {
		t.Fatalf("%s failed with SQLSTATE %s (%s), want %s (insufficient_privilege).\n"+
			"A different failure is not the guarantee: a syntax error would also 'fail'.",
			what, pgErr.Code, pgErr.Message, insufficientPrivilege)
	}
	if want := "for table " + table; !strings.Contains(pgErr.Message, want) {
		t.Fatalf("%s was refused, but for the wrong object: %q does not mention %q.\n"+
			"A refusal about a sequence, a column default or another table would satisfy a\n"+
			"SQLSTATE-only assertion while leaving %s writable.",
			what, pgErr.Message, want, table)
	}
}

func TestApplicationRoleCannotWriteAccounting(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()

	// A row for the UPDATE and DELETE attempts to aim at, written by the role that
	// legitimately owns these tables.
	owner := ownerPool(t)
	uid := uniq("acct")
	_, err := owner.Exec(ctx, `
		INSERT INTO radacct (acctsessionid, acctuniqueid, nasipaddress, acctstarttime, callingstationid)
		VALUES ($1, $1, '10.99.0.1', now(), '02:00:00:00:00:01')`, uid)
	fatalf(t, err, "seeding radacct as the owner")

	_, err = owner.Exec(ctx, `
		INSERT INTO radpostauth (username, reply, callingstationid, authdate)
		VALUES ($1, 'Access-Accept', '02:00:00:00:00:01', now())`, uniq("user"))
	fatalf(t, err, "seeding radpostauth as the owner")

	pool := st.Pool()

	t.Run("radacct refuses INSERT", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO radacct (acctsessionid, acctuniqueid, nasipaddress)
			VALUES ('forged', 'forged', '10.99.0.2')`)
		requireRefused(t, err, "INSERT INTO radacct", "radacct")
	})

	t.Run("radacct refuses UPDATE", func(t *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE radacct SET acctinputoctets = 0 WHERE acctuniqueid = $1`, uid)
		requireRefused(t, err, "UPDATE radacct", "radacct")
	})

	t.Run("radacct refuses DELETE", func(t *testing.T) {
		_, err := pool.Exec(ctx, `DELETE FROM radacct WHERE acctuniqueid = $1`, uid)
		requireRefused(t, err, "DELETE FROM radacct", "radacct")
	})

	t.Run("radpostauth refuses INSERT", func(t *testing.T) {
		_, err := pool.Exec(ctx, `
			INSERT INTO radpostauth (username, reply) VALUES ('forged', 'Access-Accept')`)
		requireRefused(t, err, "INSERT INTO radpostauth", "radpostauth")
	})

	t.Run("radpostauth refuses UPDATE", func(t *testing.T) {
		_, err := pool.Exec(ctx, `UPDATE radpostauth SET reply = 'Access-Accept'`)
		requireRefused(t, err, "UPDATE radpostauth", "radpostauth")
	})

	// The other half, and it is not decoration: a role with no privilege at all
	// would pass every assertion above while breaking the entire console. The
	// guarantee is "reads, and only reads", so both halves are asserted.
	t.Run("both tables still read", func(t *testing.T) {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM radacct`).Scan(&n); err != nil {
			t.Fatalf("the application role cannot SELECT radacct: %v", err)
		}
		if n == 0 {
			t.Fatal("SELECT on radacct returned no rows; the seed above should be visible")
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM radpostauth`).Scan(&n); err != nil {
			t.Fatalf("the application role cannot SELECT radpostauth: %v", err)
		}
		if n == 0 {
			t.Fatal("SELECT on radpostauth returned no rows; the seed above should be visible")
		}
	})
}

// The reaper is the one audited exception, and an exception that is not bounded is
// not an exception.
//
// It may close a session a rebooted NAS never sent an Acct-Stop for. It may not
// create one, may not delete one, may not touch a byte counter, and has no
// privilege at all on radpostauth. Those are column-level grants, so PostgreSQL
// enforces the boundary inside a single table.
func TestReaperRoleCanOnlyCloseSessions(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	owner := ownerPool(t)
	uid := uniq("reap")
	_, err := owner.Exec(ctx, `
		INSERT INTO radacct (acctsessionid, acctuniqueid, nasipaddress, acctstarttime, acctinputoctets)
		VALUES ($1, $1, '10.99.0.3', now() - interval '40 days', 1024)`, uid)
	fatalf(t, err, "seeding radacct as the owner")

	reaper := storeAsRole(t, "foyer_reaper", store.DefaultTenantID).Pool()

	t.Run("may close an abandoned session", func(t *testing.T) {
		tag, err := reaper.Exec(ctx, `
			UPDATE radacct
			SET acctstoptime = now(), acctterminatecause = 'Lost-Carrier',
			    acctsessiontime = 3456000, acctupdatetime = now()
			WHERE acctuniqueid = $1`, uid)
		if err != nil {
			t.Fatalf("the reaper cannot close a session, which is its only job: %v", err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("closed %d rows, want 1", tag.RowsAffected())
		}
	})

	t.Run("may not rewrite a byte counter", func(t *testing.T) {
		_, err := reaper.Exec(ctx,
			`UPDATE radacct SET acctinputoctets = 0 WHERE acctuniqueid = $1`, uid)
		requireRefused(t, err, "reaper UPDATE of radacct.acctinputoctets", "radacct")
	})

	t.Run("may not create a session", func(t *testing.T) {
		_, err := reaper.Exec(ctx, `
			INSERT INTO radacct (acctsessionid, acctuniqueid, nasipaddress)
			VALUES ('reaper', 'reaper', '10.99.0.4')`)
		requireRefused(t, err, "reaper INSERT INTO radacct", "radacct")
	})

	t.Run("may not delete a session", func(t *testing.T) {
		_, err := reaper.Exec(ctx, `DELETE FROM radacct WHERE acctuniqueid = $1`, uid)
		requireRefused(t, err, "reaper DELETE FROM radacct", "radacct")
	})

	t.Run("cannot see radpostauth at all", func(t *testing.T) {
		var n int
		err := reaper.QueryRow(ctx, `SELECT count(*) FROM radpostauth`).Scan(&n)
		requireRefused(t, err, "reaper SELECT on radpostauth", "radpostauth")
	})
}

// The audit log is append-only, and that is a grant rather than a promise too:
// `GRANT SELECT, INSERT ON audit_log`, with no UPDATE and no DELETE.
func TestAuditLogIsAppendOnly(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	entry := &store.AuditEntry{
		ID: uniq("aud"), ActorID: "op-1", ActorUsername: "linh",
		Kind: "nas-registered", SiteID: &site.ID,
		SubjectKind: "nas", SubjectID: "nas-1",
	}
	fatalf(t, st.Audit().Append(ctx, entry), "appending an audit entry")

	t.Run("refuses UPDATE", func(t *testing.T) {
		_, err := st.Pool().Exec(ctx, `UPDATE audit_log SET actor_username = 'someone else' WHERE id = $1`, entry.ID)
		requireRefused(t, err, "UPDATE audit_log", "audit_log")
	})

	t.Run("refuses DELETE", func(t *testing.T) {
		_, err := st.Pool().Exec(ctx, `DELETE FROM audit_log WHERE id = $1`, entry.ID)
		requireRefused(t, err, "DELETE FROM audit_log", "audit_log")
	})
}
