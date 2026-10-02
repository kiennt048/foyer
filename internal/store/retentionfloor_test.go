package store_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kiennt048/foyer/internal/store"
)

// ── an account's consent after its grant has aged out ────────────────────────

// TestEraseByAccountRemovesConsentsWhoseGrantAlreadyAgedOut: grant expired →
// sweep → erase the account → no consent left. A consent found only through its
// grant, which the sweep has removed and SET NULL'd, would leave one row with an
// e-mail and a MAC surviving an erasure whose receipt said 0; migration 00051 is
// what lets the erasure find it without the grant.
func TestEraseByAccountRemovesConsentsWhoseGrantAlreadyAgedOut(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	acc := seedAccount(t, w.app, w.site.ID, uniq("thu"))
	bystander := seedAccount(t, w.app, w.site.ID, uniq("hai"))
	version := publishConsent(t, w)

	consentOn := func(a *store.Account, mac string, granted time.Time) string {
		t.Helper()
		exp := granted.Add(time.Hour)
		g := w.grant(t, mac, &a.ID, granted, &exp)
		_, err := w.app.GuestData().RecordConsent(ctx, store.ConsentGiven{SiteID: w.site.ID, ClientMAC: mac,
			GrantID: &g, Email: strp(a.Username + "@example.test"), Version: version})
		fatalf(t, err, "recording a consent")
		return g
	}
	old := now.Add(-100 * 24 * time.Hour) // past portalGrants' 90 days, inside consents' 730
	aged := consentOn(acc, "02:00:00:00:35:01", old)
	live := consentOn(acc, "02:00:00:00:35:02", now.Add(-time.Hour))
	consentOn(bystander, "02:00:00:00:35:03", old)

	_, err := store.SweepRetention(ctx, store.SweepTargets{App: w.app, Retention: w.ret}, now, discard())
	fatalf(t, err, "sweeping")
	if n := w.count(t, `SELECT count(*) FROM portal_grants WHERE id = $1`, aged); n != 0 {
		t.Fatal("the sweep kept the 100-day-old grant; the test is not reproducing the aged-out case")
	}
	if n := w.count(t, `SELECT count(*) FROM marketing_consents WHERE grant_id IS NULL`); n != 2 {
		t.Fatalf("%d consents lost their grant, want 2 (the aged ones)", n)
	}
	_ = live

	s, err := w.app.AccountSubject(ctx, acc.ID)
	fatalf(t, err, "resolving")
	rep, err := store.Erase(ctx, w.app, w.ret, s)
	fatalf(t, err, "erasing")

	if n := w.count(t, `SELECT count(*) FROM marketing_consents WHERE email = $1`, acc.Username+"@example.test"); n != 0 {
		t.Errorf("%d consent(s) of the erased account survived", n)
	}
	if n := w.count(t, `SELECT count(*) FROM marketing_consents WHERE email = $1`, bystander.Username+"@example.test"); n != 1 {
		t.Error("another account's consent went too")
	}
	for _, r := range rep.Removed {
		if r.Table == "marketing_consents" && r.Rows != 2 {
			t.Errorf("the receipt counts %d consents, want 2 (the aged one and the live one)", r.Rows)
		}
	}
}

// ── the floor is the database's, and the sweep's ─────────────────────────────

// TestTheDatabaseHoldsEveryRetentionFloor: for every registered category, the
// application's own role cannot store a period below the category's minimum, and
// can store the minimum itself. A category whose Go floor and SQL floor disagree
// is red in one direction or the other.
func TestTheDatabaseHoldsEveryRetentionFloor(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	app := w.app.Pool()
	for _, c := range store.RetentionCategories() {
		t.Run(c.ID, func(t *testing.T) {
			if c.MinDays > 0 {
				_, err := app.Exec(ctx, `INSERT INTO retention_policies (category, days) VALUES ($1, $2)
					ON CONFLICT (tenant_id, category) DO UPDATE SET days = EXCLUDED.days`, c.ID, c.MinDays-1)
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "23514" || !strings.Contains(pgErr.ConstraintName, "retention_policies_floor") {
					t.Fatalf("foyer_app stored %d days for %s, below its %d-day floor: %v", c.MinDays-1, c.ID, c.MinDays, err)
				}
			}
			if _, err := app.Exec(ctx, `INSERT INTO retention_policies (category, days) VALUES ($1, $2)
				ON CONFLICT (tenant_id, category) DO UPDATE SET days = EXCLUDED.days`, c.ID, c.MinDays); err != nil {
				t.Fatalf("the floor itself (%d days) was refused for %s: %v", c.MinDays, c.ID, err)
			}
		})
	}
}

// TestTheSweepReappliesTheFloor: even with the CHECK gone — a later migration's
// mistake, a hand-edit by the schema owner — an audit period of one day deletes
// nothing younger than the thirty-day floor.
func TestTheSweepReappliesTheFloor(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	w.exec(t, `ALTER TABLE retention_policies DROP CONSTRAINT retention_policies_floor`)
	w.exec(t, `INSERT INTO retention_policies (category, days) VALUES ('audit', 1)`)
	tenDays := w.audit(t, now.Add(-10*24*time.Hour))
	ancient := w.audit(t, now.Add(-40*24*time.Hour))

	_, err := store.SweepRetention(ctx, store.SweepTargets{App: w.app, Retention: w.ret}, now, discard())
	fatalf(t, err, "sweeping")
	if n := w.count(t, `SELECT count(*) FROM audit_log WHERE id = $1`, tenDays); n != 1 {
		t.Error("a ten-day-old audit entry was deleted under a stored one-day period: the floor is not re-applied")
	}
	if n := w.count(t, `SELECT count(*) FROM audit_log WHERE id = $1`, ancient); n != 0 {
		t.Error("a forty-day-old entry survived: the sweep stopped sweeping audit at all")
	}
}
