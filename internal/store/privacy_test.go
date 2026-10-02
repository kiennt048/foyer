package store_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// The retention and erasure feature, driven against real PostgreSQL.
//
// Each test gets a database of its own, migrated from nothing, because a sweep is a
// statement about *every* row older than a cutoff and the shared container holds
// other tests' accounting.

type privacyWorld struct {
	url   string
	app   *store.Store
	ret   *store.Retention
	owner *pgxpool.Pool
	site  *store.Site
}

func newPrivacyWorld(t *testing.T) *privacyWorld {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	url := freshDatabase(t)
	fatalf(t, store.Migrate(ctx, url), "migrating")

	app, err := store.Open(ctx, store.Config{URL: url, Keys: testKeyring(t), MaxConns: 3})
	fatalf(t, err, "opening the application store")
	t.Cleanup(app.Close)

	ret, err := store.OpenRetention(ctx, store.Config{URL: url})
	fatalf(t, err, "opening the retention connection")
	t.Cleanup(ret.Close)

	cfg, err := pgxpool.ParseConfig(url)
	fatalf(t, err, "parsing")
	cfg.MaxConns = 2
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SELECT set_config('foyer.tenant_id', $1, false)`, store.DefaultTenantID)
		return err
	}
	owner, err := pgxpool.NewWithConfig(ctx, cfg)
	fatalf(t, err, "owner pool")
	t.Cleanup(owner.Close)

	return &privacyWorld{url: url, app: app, ret: ret, owner: owner, site: seedSite(t, app)}
}

func (w *privacyWorld) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.owner.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seeding (%s): %v", strings.Fields(sql)[0]+" "+strings.Fields(sql)[2], err)
	}
}

func (w *privacyWorld) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("counting (%s): %v", sql, err)
	}
	return n
}

func (w *privacyWorld) acct(t *testing.T, username, calling string, start time.Time, stop *time.Time) string {
	t.Helper()
	uid := uniq("acct")
	w.exec(t, `INSERT INTO radacct (acctsessionid, acctuniqueid, username, nasipaddress,
		acctstarttime, acctstoptime, calledstationid, callingstationid)
		VALUES ($1, $1, $2, '10.20.0.1', $3, $4, '94-2A-6F-D0-30-57:Guest', $5)`,
		uid, username, start, stop, calling)
	return uid
}

func (w *privacyWorld) postauth(t *testing.T, username, calling string, at time.Time) {
	t.Helper()
	w.exec(t, `INSERT INTO radpostauth (username, reply, calledstationid, callingstationid, authdate)
		VALUES ($1, 'Access-Accept', '94-2A-6F-D0-30-57:Guest', $2, $3)`, username, calling, at)
}

func (w *privacyWorld) grant(t *testing.T, mac string, accountID *string, granted time.Time, expires *time.Time) string {
	t.Helper()
	id := uniq("grant")
	method := "click-through"
	if accountID != nil {
		method = "account"
	}
	w.exec(t, `INSERT INTO portal_grants (id, site_id, client_mac, ap_mac, method, account_id,
		enforced_by, granted_at, expires_at)
		VALUES ($1, $2, $3::macaddr, '94:2a:6f:d0:30:57', $4, $5, 'unenforced', $6, $7)`,
		id, w.site.ID, mac, method, accountID, granted, expires)
	return id
}

func (w *privacyWorld) portalSession(t *testing.T, mac string, grantID *string, expires time.Time) {
	t.Helper()
	w.exec(t, `INSERT INTO portal_sessions (token_hash, site_id, client_mac, ap_mac, ssid,
		original_url, grant_id, expires_at)
		VALUES ($1, $2, $3::macaddr, '94:2a:6f:d0:30:57', 'Guest', 'http://example.test/', $4, $5)`,
		uniq("tok"), w.site.ID, mac, grantID, expires)
}

func (w *privacyWorld) authEvent(t *testing.T, grantID, mac string, username *string, at time.Time) {
	t.Helper()
	w.exec(t, `INSERT INTO authorization_events (site_id, grant_id, client_mac, ap_mac, method,
		username, at, delivery, attempts)
		VALUES ($1, $2, $3::macaddr, '94:2a:6f:d0:30:57', 'click-through', $4, $5, 'open', 1)`,
		w.site.ID, grantID, mac, username, at)
}

func (w *privacyWorld) audit(t *testing.T, at time.Time) string {
	t.Helper()
	id := uniq("aud")
	w.exec(t, `INSERT INTO audit_log (id, at, actor_id, actor_username, kind, subject_kind, subject_id)
		VALUES ($1, $2, 'op-1', 'linh', 'nas-registered', 'nas', 'nas-1')`, id, at)
	return id
}

// ── the role ─────────────────────────────────────────────────────────────────

// TestRetentionRoleCanOnlyDelete is the database half of the extended exception:
// foyer_retention deletes from the FreeRADIUS tables and can do nothing else to
// them. Every refusal must name the table (docs/rules/database.md: the permission
// test that passed while the table was open).
func TestRetentionRoleCanOnlyDelete(t *testing.T) {
	ctx := context.Background()
	owner := ownerPool(t)
	ret := storeAsRole(t, store.RetentionRole, store.DefaultTenantID).Pool()

	uid := uniq("ret")
	_, err := owner.Exec(ctx, `INSERT INTO radacct (acctsessionid, acctuniqueid, nasipaddress, acctstarttime, callingstationid)
		VALUES ($1, $1, '10.99.0.9', now(), '02:00:00:00:00:09')`, uid)
	fatalf(t, err, "seeding radacct as the owner")

	t.Run("radacct refuses INSERT", func(t *testing.T) {
		_, err := ret.Exec(ctx, `INSERT INTO radacct (acctsessionid, acctuniqueid, nasipaddress) VALUES ('f', 'f', '10.99.0.2')`)
		requireRefused(t, err, "retention INSERT INTO radacct", "radacct")
	})
	t.Run("radacct refuses UPDATE", func(t *testing.T) {
		_, err := ret.Exec(ctx, `UPDATE radacct SET username = NULL WHERE acctuniqueid = $1`, uid)
		requireRefused(t, err, "retention UPDATE radacct", "radacct")
	})
	t.Run("radacct refuses TRUNCATE", func(t *testing.T) {
		_, err := ret.Exec(ctx, `TRUNCATE radacct`)
		requireRefused(t, err, "retention TRUNCATE radacct", "radacct")
	})
	t.Run("radpostauth refuses INSERT", func(t *testing.T) {
		_, err := ret.Exec(ctx, `INSERT INTO radpostauth (username, reply) VALUES ('x', 'Access-Accept')`)
		requireRefused(t, err, "retention INSERT INTO radpostauth", "radpostauth")
	})
	t.Run("audit_log refuses INSERT", func(t *testing.T) {
		_, err := ret.Exec(ctx, `INSERT INTO audit_log (id, actor_id, actor_username, kind, subject_kind, subject_id)
			VALUES ('x', 'x', 'x', 'nas-registered', 'nas', 'x')`)
		requireRefused(t, err, "retention INSERT INTO audit_log", "audit_log")
	})
	t.Run("radacct accepts DELETE", func(t *testing.T) {
		tag, err := ret.Exec(ctx, `DELETE FROM radacct WHERE acctuniqueid = $1`, uid)
		fatalf(t, err, "retention DELETE FROM radacct")
		if tag.RowsAffected() != 1 {
			t.Fatalf("deleted %d rows, want 1", tag.RowsAffected())
		}
	})
	t.Run("the application role still cannot DELETE audit_log", func(t *testing.T) {
		_, err := appStore(t).Pool().Exec(ctx, `DELETE FROM audit_log WHERE id = 'nothing'`)
		requireRefused(t, err, "foyer_app DELETE FROM audit_log", "audit_log")
	})
}

// TestOpenRetentionRefusesAWiderConnection: the code asserts the role rather than
// trusting the caller, so the schema owner -- who can write everything -- and the
// application are both refused.
func TestOpenRetentionRefusesAWiderConnection(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	for name, role := range map[string]string{"schema owner": store.NoRole, "application": store.DefaultAppRole} {
		t.Run(name, func(t *testing.T) {
			r, err := store.OpenRetention(ctx, store.Config{URL: ownerURL(t), Role: role})
			if err == nil {
				r.Close()
				t.Fatalf("OpenRetention accepted a connection as %s", role)
			}
			if !errors.Is(err, store.ErrRetentionPrivilege) {
				t.Fatalf("refused, but not for its privileges: %v", err)
			}
		})
	}
}

// ── retention ────────────────────────────────────────────────────────────────

// TestRetentionSweepDeletesExpiredDataAfterOneCycle: data past its period is gone after one sweep, data inside it is not,
// and an open session is never touched however old it is.
func TestRetentionSweepDeletesExpiredDataAfterOneCycle(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old, fresh := now.Add(-400*24*time.Hour), now.Add(-2*24*time.Hour)
	oldStop, freshStop := old.Add(time.Hour), fresh.Add(time.Hour)

	oldAcct := w.acct(t, "guest", "02:00:00:00:00:01", old, &oldStop)
	freshAcct := w.acct(t, "guest", "02:00:00:00:00:01", fresh, &freshStop)
	openAcct := w.acct(t, "guest", "02:00:00:00:00:01", old, nil)
	w.postauth(t, "guest", "02:00:00:00:00:01", old)
	w.postauth(t, "guest", "02:00:00:00:00:01", fresh)
	oldExp, freshExp := old.Add(time.Hour), fresh.Add(time.Hour)
	oldGrant := w.grant(t, "02:00:00:00:00:01", nil, old, &oldExp)
	freshGrant := w.grant(t, "02:00:00:00:00:01", nil, fresh, &freshExp)
	unlimitedOld := w.grant(t, "02:00:00:00:00:02", nil, old, nil)
	w.authEvent(t, freshGrant, "02:00:00:00:00:01", nil, old) // old event on a live grant
	w.authEvent(t, freshGrant, "02:00:00:00:00:01", nil, fresh)
	oldAudit := w.audit(t, old.Add(-400*24*time.Hour)) // past the 730-day default
	freshAudit := w.audit(t, fresh)

	removed, err := store.SweepRetention(ctx, store.SweepTargets{App: w.app, Retention: w.ret}, now, discard())
	fatalf(t, err, "sweeping")

	gone := func(what, sql string, args ...any) {
		t.Helper()
		if n := w.count(t, sql, args...); n != 0 {
			t.Errorf("%s survived the sweep (%d row(s))", what, n)
		}
	}
	kept := func(what, sql string, args ...any) {
		t.Helper()
		if n := w.count(t, sql, args...); n != 1 {
			t.Errorf("%s: want 1 row after the sweep, got %d", what, n)
		}
	}
	gone("a closed session that ended 400 days ago", `SELECT count(*) FROM radacct WHERE acctuniqueid = $1`, oldAcct)
	kept("a closed session from two days ago", `SELECT count(*) FROM radacct WHERE acctuniqueid = $1`, freshAcct)
	kept("an OPEN session, however old", `SELECT count(*) FROM radacct WHERE acctuniqueid = $1`, openAcct)
	gone("a 400-day-old post-auth row", `SELECT count(*) FROM radpostauth WHERE authdate < $1`, now.Add(-30*24*time.Hour))
	kept("a recent post-auth row", `SELECT count(*) FROM radpostauth`)
	gone("a grant that ended 400 days ago", `SELECT count(*) FROM portal_grants WHERE id = $1`, oldGrant)
	gone("an unlimited grant granted 400 days ago", `SELECT count(*) FROM portal_grants WHERE id = $1`, unlimitedOld)
	kept("a live grant", `SELECT count(*) FROM portal_grants WHERE id = $1`, freshGrant)
	kept("a recent authorization event", `SELECT count(*) FROM authorization_events`)
	gone("an audit entry older than two years", `SELECT count(*) FROM audit_log WHERE id = $1`, oldAudit)
	kept("a recent audit entry", `SELECT count(*) FROM audit_log WHERE id = $1`, freshAudit)
	if removed != 6 {
		t.Errorf("the sweep reported %d rows removed, want 6", removed)
	}

	// The sweep's own record is what the console shows.
	policies, err := w.app.Privacy().RetentionPolicies(ctx)
	fatalf(t, err, "reading policies")
	for _, p := range policies {
		if p.LastSweep == nil {
			t.Errorf("category %s has no sweep record after a sweep", p.Category.ID)
		}
	}
}

// TestRetentionPeriodsAreConfigurable: an operator's period replaces the default,
// out-of-range values are refused, and clearing one restores the default.
func TestRetentionPeriodsAreConfigurable(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	tenDays := now.Add(-10 * 24 * time.Hour)
	stop := tenDays.Add(time.Hour)
	uid := w.acct(t, "guest", "02:00:00:00:00:03", tenDays, &stop)

	seven := 7
	fatalf(t, w.app.Privacy().SetRetention(ctx, []store.RetentionChange{{Category: "accounting", Days: &seven}}, "linh"), "setting 7 days")
	_, err := store.SweepRetention(ctx, store.SweepTargets{App: w.app, Retention: w.ret}, now, discard())
	fatalf(t, err, "sweeping")
	if n := w.count(t, `SELECT count(*) FROM radacct WHERE acctuniqueid = $1`, uid); n != 0 {
		t.Fatal("a ten-day-old session survived a seven-day accounting period")
	}

	zero := 0
	if err := w.app.Privacy().SetRetention(ctx, []store.RetentionChange{{Category: "accounting", Days: &zero}}, "linh"); !errors.Is(err, store.ErrRetentionOutOfRange) {
		t.Fatalf("accounting accepted 0 days (keep forever): %v", err)
	}
	if err := w.app.Privacy().SetRetention(ctx, []store.RetentionChange{{Category: "nonsense", Days: &seven}}, "linh"); !errors.Is(err, store.ErrUnknownRetentionCategory) {
		t.Fatalf("an unknown category was accepted: %v", err)
	}
	fatalf(t, w.app.Privacy().SetRetention(ctx, []store.RetentionChange{{Category: "accounting"}}, "linh"), "restoring the default")
	policies, err := w.app.Privacy().RetentionPolicies(ctx)
	fatalf(t, err, "reading")
	for _, p := range policies {
		if p.Category.ID == "accounting" && (!p.IsDefault || p.Days != p.Category.DefaultDays) {
			t.Fatalf("clearing the override left %d days (default %v)", p.Days, p.IsDefault)
		}
	}
}

// ── erasure ──────────────────────────────────────────────────────────────────

// TestEraseByMACRemovesThatMACFromEveryRegisteredTable: the subject is seeded in every table the registry says holds a MAC, in every
// spelling a NAS sends, and a bystander beside it; afterwards no registered
// identifier column still matches the subject and the bystander is untouched.
func TestEraseByMACRemovesThatMACFromEveryRegisteredTable(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	const subject, bystander = "AA-BB-CC-00-11-22", "02:00:00:00:00:99"

	stop := now
	for _, spelling := range []string{"AA-BB-CC-00-11-22", "aa:bb:cc:00:11:22", "aabb.cc00.1122", "AABBCC001122"} {
		w.acct(t, "someone", spelling, now.Add(-time.Hour), &stop)
		w.postauth(t, "someone", spelling, now)
	}
	// MAC authentication: the MAC is the User-Name and Calling-Station-Id is blank.
	w.acct(t, "aabbcc001122", "", now.Add(-time.Hour), &stop)
	w.postauth(t, "aa-bb-cc-00-11-22", "", now)
	// A username that is hex and punctuation but not MAC-shaped is not the subject.
	w.postauth(t, "aabb-cc00-1122-x", bystander, now)
	g := w.grant(t, subject, nil, now, nil)
	w.portalSession(t, subject, &g, now.Add(time.Hour))
	w.authEvent(t, g, subject, nil, now)
	bg := w.grant(t, bystander, nil, now, nil)
	w.portalSession(t, bystander, &bg, now.Add(time.Hour))
	w.authEvent(t, bg, bystander, nil, now)
	// The RFC 8908 address book (00045) names the device too.
	w.exec(t, `INSERT INTO portal_addresses (address, token_hash, client_mac, grant_id, seen_at)
		VALUES ('10.20.0.50', 'tok-subject', $1::macaddr, $2, $4), ('10.20.0.51', 'tok-bystander', $3::macaddr, $5, $4)`,
		subject, g, bystander, now, bg)
	w.acct(t, "other", bystander, now.Add(-time.Hour), &stop)
	w.postauth(t, "other", bystander, now)

	s, err := store.MACSubject(subject)
	fatalf(t, err, "parsing the subject")
	rep, err := store.Erase(ctx, w.app, w.ret, s)
	fatalf(t, err, "erasing")

	checked := 0
	for _, c := range store.PersonalColumns() {
		if !slices.Contains(c.Identifies, store.SubjectMAC) || c.Pseudonymised {
			continue
		}
		checked++
		if n := w.count(t, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s`,
			pgx.Identifier{c.Table}.Sanitize(), macMatch(t, w, c)), s.MACHex); n != 0 {
			t.Errorf("%s.%s still holds the erased MAC in %d row(s)", c.Table, c.Column, n)
		}
	}
	if checked < 5 {
		t.Fatalf("only %d registered MAC columns were checked; the registry has lost its MAC tables", checked)
	}

	// The bystander is untouched in every table.
	for table, want := range map[string]int{"radacct": 1, "radpostauth": 2, "portal_grants": 1,
		"portal_sessions": 1, "authorization_events": 1, "portal_addresses": 1} {
		if n := w.count(t, `SELECT count(*) FROM `+table); n != want {
			t.Errorf("%s holds %d row(s) after erasing somebody else, want %d (the bystander's)", table, n, want)
		}
	}
	if rep.Total != 4+4+1+1+1+1+1+1 {
		t.Errorf("the report counts %d rows, want 14: %+v", rep.Total, rep.Removed)
	}
	for _, table := range []string{"radacct", "radpostauth", "authorization_events", "portal_grants", "portal_sessions", "portal_addresses"} {
		if !slices.ContainsFunc(rep.Removed, func(e store.ErasedTable) bool { return e.Table == table }) {
			t.Errorf("the report omits %s", table)
		}
	}
	if strings.Contains(s.Masked(), "aa") || strings.Contains(s.Masked(), "bb:cc") {
		t.Errorf("the masked subject %q still carries the identifying octets", s.Masked())
	}
}

// TestEraseByAccountRemovesTheAccountAndItsHistory: the account, its verifiers, its
// grants and the sessions bound to them, and every FreeRADIUS row carrying its
// username.
func TestEraseByAccountRemovesTheAccountAndItsHistory(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	username := uniq("minh")
	acc := seedAccount(t, w.app, w.site.ID, username)
	verifier, err := identity.HashPAPVerifier("correct-horse-battery-staple")
	fatalf(t, err, "bcrypt")
	fatalf(t, w.app.Accounts().OptInTo8021X(ctx, acc.ID, verifier, nil), "opting in to 802.1X")
	other := seedAccount(t, w.app, w.site.ID, uniq("lan"))

	stop := now
	w.acct(t, username, "02:00:00:00:00:10", now.Add(-time.Hour), &stop)
	w.postauth(t, username, "02:00:00:00:00:10", now)
	g := w.grant(t, "02:00:00:00:00:10", &acc.ID, now, nil)
	w.portalSession(t, "02:00:00:00:00:10", &g, now.Add(time.Hour))
	w.authEvent(t, g, "02:00:00:00:00:10", &username, now)
	og := w.grant(t, "02:00:00:00:00:11", &other.ID, now, nil)
	w.acct(t, other.Username, "02:00:00:00:00:11", now.Add(-time.Hour), &stop)

	s, err := w.app.AccountSubject(ctx, acc.ID)
	fatalf(t, err, "resolving the account")
	_, err = store.Erase(ctx, w.app, w.ret, s)
	fatalf(t, err, "erasing")

	for _, c := range store.PersonalColumns() {
		if !slices.Contains(c.Identifies, store.SubjectAccount) || c.Pseudonymised {
			continue
		}
		value := s.Username
		if c.Column == "account_id" {
			value = s.AccountID
		}
		if n := w.count(t, fmt.Sprintf(`SELECT count(*) FROM %s WHERE %s = $1`,
			pgx.Identifier{c.Table}.Sanitize(), pgx.Identifier{c.Column}.Sanitize()), value); n != 0 {
			t.Errorf("%s.%s still names the erased account in %d row(s)", c.Table, c.Column, n)
		}
	}
	if n := w.count(t, `SELECT count(*) FROM portal_sessions WHERE client_mac = '02:00:00:00:00:10'`); n != 0 {
		t.Error("the portal session bound to the account's grant survived")
	}
	if n := w.count(t, `SELECT count(*) FROM portal_grants WHERE id = $1`, og); n != 1 {
		t.Error("another account's grant was erased")
	}
	if n := w.count(t, `SELECT count(*) FROM accounts WHERE id = $1`, other.ID); n != 1 {
		t.Error("another account was erased")
	}
}

// TestEraseByAccountLeavesAnotherVenuesNamesake: a guest account's username is unique
// per venue, not per deployment (00005, `UNIQUE (tenant_id, site_id, username)`), so
// `room101` at one hotel and `room101` at the next are two people. Erasing one must
// not erase the other's sessions and sign-ins -- an erasure that destroys a
// bystander's records is a second breach, not a thorough first one.
//
// A row is the other venue's when the console would say so: accounting by the RADIUS
// client its NAS address resolves to (`clientForNAS`), post-auth by the access point in
// its Called-Station-Id (`calledStationParts`), authorization events by their own
// `site_id`. A row nothing attributes to any venue is erased: a miss there fails open.
func TestEraseByAccountLeavesAnotherVenuesNamesake(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	stop := now

	there := &store.Site{ID: uniq("site"), Name: "The Other Venue", Environment: "lab"}
	fatalf(t, w.app.Sites().Create(ctx, there), "creating the second venue")
	w.exec(t, `INSERT INTO radius_clients (id, site_id, name, address, source, driver) VALUES
		($1, $2, 'here', '10.31.1.0/24', 'manual', 'coa'), ($3, $4, 'there', '10.31.2.0/24', 'manual', 'coa')`,
		uniq("rc"), w.site.ID, uniq("rc"), there.ID)
	w.exec(t, `INSERT INTO access_points (id, site_id, mac, name) VALUES
		($1, $2, '94:2a:6f:31:00:01', 'here'), ($3, $4, '94:2a:6f:31:00:02', 'there')`,
		uniq("ap"), w.site.ID, uniq("ap"), there.ID)

	name := uniq("room")
	mine := seedAccount(t, w.app, w.site.ID, name)
	theirs := seedAccount(t, w.app, there.ID, name)

	acct := func(nas string) string {
		uid := uniq("acct")
		w.exec(t, `INSERT INTO radacct (acctsessionid, acctuniqueid, username, nasipaddress,
			acctstarttime, acctstoptime, callingstationid)
			VALUES ($1, $1, $2, $3::inet, $4, $5, '02:00:00:00:31:01')`, uid, name, nas, now.Add(-time.Hour), stop)
		return uid
	}
	myAcct, theirAcct, nobodysAcct := acct("10.31.1.7"), acct("10.31.2.7"), acct("10.31.9.7")
	postauth := func(called string) {
		w.exec(t, `INSERT INTO radpostauth (username, reply, calledstationid, callingstationid, authdate)
			VALUES ($1, 'Access-Accept', $2, '02:00:00:00:31:01', $3)`, name, called, now)
	}
	postauth("94-2A-6F-31-00-01:Guest")
	postauth("94-2A-6F-31-00-02:Guest")
	postauth("")

	grantAt := func(site, account string) string {
		id := uniq("grant")
		w.exec(t, `INSERT INTO portal_grants (id, site_id, client_mac, method, account_id, enforced_by, granted_at)
			VALUES ($1, $2, '02:00:00:00:31:01', 'account', $3, 'unenforced', $4)`, id, site, account, now)
		w.exec(t, `INSERT INTO authorization_events (site_id, grant_id, client_mac, method, username, at, delivery, attempts)
			VALUES ($1, $2, '02:00:00:00:31:01', 'account', $3, $4, 'open', 1)`, site, id, name, now)
		return id
	}
	grantAt(w.site.ID, mine.ID)
	theirGrant := grantAt(there.ID, theirs.ID)

	s, err := w.app.AccountSubject(ctx, mine.ID)
	fatalf(t, err, "resolving the account")
	_, err = store.Erase(ctx, w.app, w.ret, s)
	fatalf(t, err, "erasing")

	for what, want := range map[string]struct {
		n    int
		sql  string
		args []any
	}{
		"this venue's accounting":           {0, `SELECT count(*) FROM radacct WHERE acctuniqueid = $1`, []any{myAcct}},
		"accounting no venue claims":        {0, `SELECT count(*) FROM radacct WHERE acctuniqueid = $1`, []any{nobodysAcct}},
		"the other venue's accounting":      {1, `SELECT count(*) FROM radacct WHERE acctuniqueid = $1`, []any{theirAcct}},
		"this venue's post-auth":            {0, `SELECT count(*) FROM radpostauth WHERE calledstationid = '94-2A-6F-31-00-01:Guest'`, nil},
		"post-auth no venue claims":         {0, `SELECT count(*) FROM radpostauth WHERE calledstationid = ''`, nil},
		"the other venue's post-auth":       {1, `SELECT count(*) FROM radpostauth WHERE calledstationid = '94-2A-6F-31-00-02:Guest'`, nil},
		"this venue's authorization events": {0, `SELECT count(*) FROM authorization_events WHERE site_id = $1`, []any{w.site.ID}},
		"the other venue's authorization":   {1, `SELECT count(*) FROM authorization_events WHERE grant_id = $1`, []any{theirGrant}},
		"the other venue's account":         {1, `SELECT count(*) FROM accounts WHERE id = $1`, []any{theirs.ID}},
		"the other venue's grant":           {1, `SELECT count(*) FROM portal_grants WHERE id = $1`, []any{theirGrant}},
	} {
		if got := w.count(t, want.sql, want.args...); got != want.n {
			t.Errorf("%s: %d row(s) after erasing the namesake, want %d", what, got, want.n)
		}
	}
}

// macMatch is the predicate that says "this column names the subject": equality for
// a macaddr column, the normalised comparison for text.
func macMatch(t *testing.T, w *privacyWorld, c store.PersonalColumn) string {
	t.Helper()
	var udt string
	if err := w.owner.QueryRow(context.Background(), `SELECT udt_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = $1 AND column_name = $2`, c.Table, c.Column).Scan(&udt); err != nil {
		t.Fatalf("%s.%s is registered and does not exist: %v", c.Table, c.Column, err)
	}
	col := pgx.Identifier{c.Column}.Sanitize()
	if udt == "macaddr" {
		return col + ` = $1::macaddr`
	}
	norm := `lower(regexp_replace(coalesce(` + col + `, ''), '[^0-9A-Fa-f]', '', 'g')) = $1`
	if strings.Contains(c.Column, "username") {
		// A username names a device only when it is shaped like a MAC (MAC
		// authentication); "aabb-cc00-1122-x" is somebody's username, not the MAC.
		return `lower(coalesce(` + col + `, '')) ~ '^([0-9a-f]{2}[-:]?){5}[0-9a-f]{2}$|^[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}$' AND ` + norm
	}
	return norm
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// ── the registry is complete, and the document agrees with it ────────────────

// personalColumnName is the schema scan: any column whose name or type says it holds
// a MAC, a username, a phone number or an e-mail address. It is deliberately wide;
// a column it catches that is not a guest's data is registered with NotGuestData
// and the reason, which is a visible decision rather than a silence.
var personalColumnName = regexp.MustCompile(`(^|_)mac($|_)|user_?name|phone|msisdn|e_?mail|stationid`)

// TestEveryPersonalColumnInTheSchemaIsRegistered is what makes the registry
// extensible without being optional: a migration that adds `phone` or `email`
// anywhere turns this red until the column is registered with how it is erased.
func TestEveryPersonalColumnInTheSchemaIsRegistered(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	rows, err := ownerPool(t).Query(ctx, `
		SELECT table_name, column_name, udt_name FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name <> 'goose_db_version'
		ORDER BY 1, 2`)
	fatalf(t, err, "reading the schema")
	defer rows.Close()

	registered := map[string]store.PersonalColumn{}
	for _, c := range store.PersonalColumns() {
		registered[c.Table+"."+c.Column] = c
	}
	inSchema := map[string]bool{}
	for rows.Next() {
		var table, column, udt string
		fatalf(t, rows.Scan(&table, &column, &udt), "scanning")
		key := table + "." + column
		inSchema[key] = true
		if udt != "macaddr" && !personalColumnName.MatchString(column) {
			continue
		}
		if _, ok := registered[key]; !ok {
			t.Errorf("%s holds personal data by its name or type and is not in store.PersonalColumns; "+
				"register it with the subject kind that erases it, or NotGuestData and the reason", key)
		}
	}
	fatalf(t, rows.Err(), "iterating")

	for key, c := range registered {
		if !inSchema[key] {
			t.Errorf("%s is registered and is not in the schema", key)
		}
		kinds := len(c.Identifies) > 0
		switch {
		case c.NotGuestData != "" && (kinds || c.WithRow):
			t.Errorf("%s is both guest data and not guest data", key)
		case c.NotGuestData == "" && !kinds && !c.WithRow:
			t.Errorf("%s says neither how it is erased nor why it is not", key)
		}
		for _, k := range c.Identifies {
			if c.CascadeFrom != "" {
				continue
			}
			if !slices.ContainsFunc(store.Erasers(k), func(e store.Eraser) bool { return e.Table == c.Table }) {
				t.Errorf("%s identifies a %s subject and no %s eraser covers %s", key, k, k, c.Table)
			}
		}
	}
}

// TestOperatorDocumentMatchesTheCode holds docs/operator/personal-data.md to the
// registry and to the retention defaults, both ways: the document must exist and
// agree with the code.
func TestOperatorDocumentMatchesTheCode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "operator", "personal-data.md"))
	if err != nil {
		t.Fatalf("the operator document is missing: %v", err)
	}
	doc := string(raw)

	colRow := regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\| `([a-z_]+)` \\|")
	documented := map[string]bool{}
	for _, m := range colRow.FindAllStringSubmatch(doc, -1) {
		documented[m[1]+"."+m[2]] = true
	}
	registered := map[string]bool{}
	for _, c := range store.PersonalColumns() {
		key := c.Table + "." + c.Column
		registered[key] = true
		if !documented[key] {
			t.Errorf("docs/operator/personal-data.md does not list %s", key)
		}
	}
	for key := range documented {
		if !registered[key] {
			t.Errorf("docs/operator/personal-data.md lists %s, which the code does not register", key)
		}
	}

	catRow := regexp.MustCompile("(?m)^\\| `([A-Za-z]+)` \\| ([0-9]+) \\|")
	defaults := map[string]string{}
	for _, m := range catRow.FindAllStringSubmatch(doc, -1) {
		defaults[m[1]] = m[2]
	}
	for _, c := range store.RetentionCategories() {
		if got, want := defaults[c.ID], fmt.Sprint(c.DefaultDays); got != want {
			t.Errorf("the document gives %s a default of %q days; the code's default is %s", c.ID, got, want)
		}
	}
	if len(defaults) != len(store.RetentionCategories()) {
		t.Errorf("the document lists %d retention categories; the code registers %d", len(defaults), len(store.RetentionCategories()))
	}
}

// TestMACSubjectAcceptsOnlyMACs: twelve hex digits inside something longer are not
// a MAC, and erasing on one would erase a device nobody named.
func TestMACSubjectAcceptsOnlyMACs(t *testing.T) {
	for _, ok := range []string{"aa:bb:cc:00:11:22", "AA-BB-CC-00-11-22", "aabb.cc00.1122", "AABBCC001122"} {
		s, err := store.MACSubject(ok)
		if err != nil || s.MACHex != "aabbcc001122" || s.MAC != "aa:bb:cc:00:11:22" {
			t.Errorf("MACSubject(%q) = %+v, %v", ok, s, err)
		}
	}
	for _, bad := range []string{"", "aa:bb:cc:00:11", "cafe-0011-2233-44", "aa:bb-cc:00:11:22", "gg:bb:cc:00:11:22", "user aabbcc001122"} {
		if _, err := store.MACSubject(bad); !errors.Is(err, store.ErrInvalidSubject) {
			t.Errorf("MACSubject(%q) was accepted", bad)
		}
	}
}
