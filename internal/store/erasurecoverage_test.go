package store_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/kiennt048/foyer/internal/store"
)

// The erasure, held to the schema rather than to the registry.
//
// A coverage test that chooses the columns it checks by *name* cannot see an
// erasure that leaves, say, `audit_log.subject_name` still holding the MAC and
// the username. These tests choose none: they read every text, inet, MAC, JSON
// and text-array column of every table and view in the schema, and fail on any
// value that still spells the erased identifier. A table added later is scanned
// the day it exists.

// scannedTypes are the column types a guest identifier can be written into.
var scannedTypes = []string{"text", "varchar", "bpchar", "citext", "name", "inet", "cidr",
	"macaddr", "macaddr8", "json", "jsonb", "_text", "_varchar"}

// survivors lists every column of every relation that still holds a value `hit`
// matches, as "table.column (n)". `hit` is a SQL predicate on `v`, the value as text.
func survivors(t *testing.T, w *privacyWorld, hit string, args ...any) []string {
	t.Helper()
	ctx := context.Background()
	rows, err := w.owner.Query(ctx, `
		SELECT c.table_name, c.column_name
		FROM information_schema.columns c
		JOIN information_schema.tables r ON r.table_schema = c.table_schema AND r.table_name = c.table_name
		WHERE c.table_schema = 'public' AND c.table_name <> 'goose_db_version'
		  AND c.udt_name = ANY($1)
		ORDER BY 1, 2`, scannedTypes)
	fatalf(t, err, "listing the schema's columns")
	type col struct{ table, column string }
	var cols []col
	for rows.Next() {
		var c col
		fatalf(t, rows.Scan(&c.table, &c.column), "scanning")
		cols = append(cols, c)
	}
	rows.Close()
	fatalf(t, rows.Err(), "listing")
	if len(cols) < 100 {
		t.Fatalf("only %d text-like columns in the schema; the scan is not reading it", len(cols))
	}
	var found []string
	for _, c := range cols {
		sql := fmt.Sprintf(`SELECT count(*) FROM (SELECT %s::text AS v FROM %s) x WHERE v IS NOT NULL AND (%s)`,
			pgx.Identifier{c.column}.Sanitize(), pgx.Identifier{c.table}.Sanitize(), hit)
		var n int
		fatalf(t, w.owner.QueryRow(ctx, sql, args...).Scan(&n), "scanning %s.%s", c.table, c.column)
		if n > 0 {
			found = append(found, fmt.Sprintf("%s.%s (%d)", c.table, c.column, n))
		}
	}
	return found
}

// macHit matches the MAC in every spelling a NAS sends, case-insensitively, as the
// migration's own pattern does — written out again here so the test does not
// trust the thing it tests.
func macHit(hex string) string {
	return `v ~* '` + strings.Join(strings.Split(hex, ""), "[-:.]?") + `'`
}

func (w *privacyWorld) appendAudit(t *testing.T, kind, subjectKind, subjectID string, subjectName *string, params map[string]any) {
	t.Helper()
	fatalf(t, w.app.Audit().Append(context.Background(), &store.AuditEntry{
		ID: uniq("aud"), ActorID: "op-1", ActorUsername: "linh", Kind: kind,
		SubjectKind: subjectKind, SubjectID: subjectID, SubjectName: subjectName, Params: params,
	}), "appending an audit entry")
}

// auditPseudonyms returns the pseudonyms the audit log now carries, and how many
// entries it has.
func (w *privacyWorld) auditPseudonyms(t *testing.T) (map[string]int, int) {
	t.Helper()
	rows, err := w.owner.Query(context.Background(), `
		SELECT m[1] FROM audit_log,
		LATERAL regexp_matches(coalesce(subject_id, '') || ' ' || coalesce(subject_name, '') || ' ' || params::text,
		                       '(pseudonym:[0-9a-f]{16})', 'g') AS m`)
	fatalf(t, err, "reading pseudonyms")
	out := map[string]int{}
	for rows.Next() {
		var p string
		fatalf(t, rows.Scan(&p), "scanning")
		out[p]++
	}
	rows.Close()
	return out, w.count(t, `SELECT count(*) FROM audit_log`)
}

// TestAnErasedMACSurvivesInNoColumnOfTheSchema — the erasure for a device: the
// MAC is seeded in every table that can hold it, in every spelling, including the
// three audit entries that once survived an erasure (a session disconnect naming the
// Calling-Station-Id, a grant disconnect naming the grant's MAC, and a MAC in
// params). Afterwards no column anywhere spells it, and the audit entries are still
// there, naming one pseudonym.
func TestAnErasedMACSurvivesInNoColumnOfTheSchema(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	stop := now
	const hex = "5e1fa377c04d"
	const colon, dash, dotted = "5e:1f:a3:77:c0:4d", "5E-1F-A3-77-C0-4D", "5e1f.a377.c04d"

	w.acct(t, colon, dash, now.Add(-time.Hour), &stop) // MAC authentication: username too
	w.postauth(t, "someone", dotted, now)
	g := w.grant(t, colon, nil, now, nil)
	w.portalSession(t, colon, &g, now.Add(time.Hour))
	w.authEvent(t, g, colon, nil, now)
	w.exec(t, `INSERT INTO portal_addresses (address, token_hash, client_mac, grant_id, seen_at)
		VALUES ('10.20.0.70', 'tok-erase', $1::macaddr, $2, $3)`, colon, g, now)
	w.exec(t, `INSERT INTO guest_details (grant_id, site_id, name) VALUES ($1, $2, 'Lan')`, g, w.site.ID)
	w.exec(t, `INSERT INTO phone_codes (id, site_id, session_token_hash, client_mac, phone, code_mac, key_id,
		created_at, expires_at, max_attempts)
		VALUES ('pc-erase', $1, 'tok-erase', $2::macaddr, '+84900000001', $3, 'k1', $4, $5, 5)`,
		w.site.ID, colon, make([]byte, 32), now, now.Add(5*time.Minute))
	version := publishConsent(t, w)
	_, err := w.app.GuestData().RecordConsent(ctx, store.ConsentGiven{SiteID: w.site.ID, ClientMAC: colon,
		GrantID: &g, Email: strp("lan@example.test"), Version: version})
	fatalf(t, err, "recording a consent")

	// The three shapes that once survived, as the handlers write them.
	w.appendAudit(t, "session-disconnected", "session", "sess-1", strp(dash), map[string]any{"nasId": "nas-1"})
	w.appendAudit(t, "session-disconnected", "portal-grant", g, strp(colon), map[string]any{"grantId": g})
	w.appendAudit(t, "session-disconnected", "session", "sess-2", strp("x"), map[string]any{"callingStationId": hex})
	_, entries := w.auditPseudonyms(t)

	if found := survivors(t, w, macHit(hex)); len(found) < 8 {
		t.Fatalf("the seed reached only %v; the scan cannot prove anything about tables it did not seed", found)
	}
	s, err := store.MACSubject(dash)
	fatalf(t, err, "parsing the subject")
	rep, err := store.Erase(ctx, w.app, w.ret, s)
	fatalf(t, err, "erasing")

	if found := survivors(t, w, macHit(hex)); len(found) > 0 {
		t.Errorf("after the erasure the MAC is still spelled in: %s", strings.Join(found, ", "))
	}
	ps, after := w.auditPseudonyms(t)
	if after != entries {
		t.Errorf("the audit log had %d entries and has %d: an erasure keeps the events", entries, after)
	}
	if len(ps) != 1 {
		t.Errorf("the erased device is named by %d different pseudonyms, want one: %v", len(ps), ps)
	}
	var pseudonymised int64
	for _, p := range rep.Pseudonymised {
		if p.Table == "audit_log" {
			pseudonymised = p.Rows
		}
	}
	if pseudonymised != 3 {
		t.Errorf("the report says %d audit entries were pseudonymised, want 3: %+v", pseudonymised, rep.Pseudonymised)
	}
}

// TestAnErasedAccountSurvivesInNoColumnOfTheSchema — the same for an account: its
// username and id, wherever an entry or a row put them.
func TestAnErasedAccountSurvivesInNoColumnOfTheSchema(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	stop := now
	username := uniq("minh")
	acc := seedAccount(t, w.app, w.site.ID, username)

	w.acct(t, username, "02:00:00:00:31:07", now.Add(-time.Hour), &stop)
	w.postauth(t, username, "02:00:00:00:31:07", now)
	g := w.grant(t, "02:00:00:00:31:07", &acc.ID, now, nil)
	w.portalSession(t, "02:00:00:00:31:07", &g, now.Add(time.Hour))
	w.authEvent(t, g, "02:00:00:00:31:07", &username, now)
	version := publishConsent(t, w)
	_, err := w.app.GuestData().RecordConsent(ctx, store.ConsentGiven{SiteID: w.site.ID, ClientMAC: "02:00:00:00:31:07",
		GrantID: &g, Email: strp("minh@example.test"), Version: version})
	fatalf(t, err, "recording a consent")
	// handlers_people.go's shape, and a username in params.
	w.appendAudit(t, "account-credentials-changed", "account", acc.ID, strp(username), map[string]any{"passwordChanged": true})
	w.appendAudit(t, "session-disconnected", "session", "sess-9", strp("02:00:00:00:31:07"), map[string]any{"username": username})
	_, entries := w.auditPseudonyms(t)

	hit := `position(lower($1) in lower(v)) > 0 OR position(lower($2) in lower(v)) > 0`
	if found := survivors(t, w, hit, username, acc.ID); len(found) < 6 {
		t.Fatalf("the seed reached only %v", found)
	}
	s, err := w.app.AccountSubject(ctx, acc.ID)
	fatalf(t, err, "resolving")
	_, err = store.Erase(ctx, w.app, w.ret, s)
	fatalf(t, err, "erasing")

	if found := survivors(t, w, hit, username, acc.ID); len(found) > 0 {
		t.Errorf("after the erasure the account is still named in: %s", strings.Join(found, ", "))
	}
	if _, after := w.auditPseudonyms(t); after != entries {
		t.Errorf("the audit log had %d entries and has %d", entries, after)
	}
}

// TestAnErasedPhoneNumberAndEmailSurviveInNoColumnOfTheSchema — the same for the
// other two kinds, the second of which is new (erasure by e-mail).
func TestAnErasedPhoneNumberAndEmailSurviveInNoColumnOfTheSchema(t *testing.T) {
	t.Run("phone", func(t *testing.T) {
		w := newPrivacyWorld(t)
		ctx := context.Background()
		now := time.Now().UTC()
		stop := now
		id := uniq("grant")
		w.exec(t, `INSERT INTO portal_grants (id, site_id, client_mac, method, enforced_by, granted_at, phone)
			VALUES ($1, $2, '02:00:00:00:32:01', 'phone', 'unenforced', $3, '+84912345678')`, id, w.site.ID, now)
		w.exec(t, `INSERT INTO guest_details (grant_id, site_id, phone) VALUES ($1, $2, '0912 345 678')`, id, w.site.ID)
		w.acct(t, "+84912345678", "02:00:00:00:32:01", now.Add(-time.Hour), &stop)
		w.authEvent(t, id, "02:00:00:00:32:01", ptrTo("+84912345678"), now)
		w.appendAudit(t, "session-disconnected", "portal-grant", id, strp("02:00:00:00:32:01"), map[string]any{"username": "+84912345678"})

		hit := `regexp_replace(v, '[^0-9]', '', 'g') LIKE '%912345678%'`
		if found := survivors(t, w, hit); len(found) < 5 {
			t.Fatalf("the seed reached only %v", found)
		}
		s, err := store.PhoneSubject("0912345678")
		fatalf(t, err, "parsing")
		_, err = store.Erase(ctx, w.app, w.ret, s)
		fatalf(t, err, "erasing")
		if found := survivors(t, w, hit); len(found) > 0 {
			t.Errorf("after the erasure the number is still in: %s", strings.Join(found, ", "))
		}
	})
	t.Run("email", func(t *testing.T) {
		w := newPrivacyWorld(t)
		ctx := context.Background()
		now := time.Now().UTC()
		g := w.grant(t, "02:00:00:00:33:01", nil, now, nil)
		other := w.grant(t, "02:00:00:00:33:02", nil, now, nil)
		w.exec(t, `INSERT INTO guest_details (grant_id, site_id, name, email) VALUES
			($1, $3, 'Lan', 'Lan.Tran@Example.test'), ($2, $3, 'Hoa', 'hoa@example.test')`, g, other, w.site.ID)
		version := publishConsent(t, w)
		_, err := w.app.GuestData().RecordConsent(ctx, store.ConsentGiven{SiteID: w.site.ID, ClientMAC: "02:00:00:00:33:01",
			GrantID: &g, Email: strp("lan.tran@example.test"), Version: version})
		fatalf(t, err, "recording a consent")
		w.appendAudit(t, "marketing-consent-withdrawn", "marketing-consent", "mc-1", nil, map[string]any{"email": "LAN.TRAN@example.test"})

		hit := `position('lan.tran@example.test' in lower(v)) > 0`
		if found := survivors(t, w, hit); len(found) < 3 {
			t.Fatalf("the seed reached only %v", found)
		}
		s, err := store.EmailSubject("  Lan.Tran@example.TEST ")
		fatalf(t, err, "parsing")
		rep, err := store.Erase(ctx, w.app, w.ret, s)
		fatalf(t, err, "erasing")
		if found := survivors(t, w, hit); len(found) > 0 {
			t.Errorf("after the erasure the address is still in: %s", strings.Join(found, ", "))
		}
		if n := w.count(t, `SELECT count(*) FROM guest_details WHERE grant_id = $1`, other); n != 1 {
			t.Error("another guest's details went with it")
		}
		if n := w.count(t, `SELECT count(*) FROM portal_grants WHERE id = $1`, g); n != 1 {
			t.Error("the device's grant went: only what gives the address is erased")
		}
		if rep.Total != 2 || s.Masked() != "l•••@•••.test" {
			t.Errorf("report %+v, masked %q", rep, s.Masked())
		}
	})
}

// TestEmailSubjectIsTheFormsShapeCheck — the same shape the portal accepts.
func TestEmailSubjectIsTheFormsShapeCheck(t *testing.T) {
	for _, ok := range []string{"a@b.vn", " Lan@Example.test "} {
		if _, err := store.EmailSubject(ok); err != nil {
			t.Errorf("EmailSubject(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{"", "lan", "@example.test", "lan@", "lan@example", "a b@c.vn", "a@b@c.vn", strings.Repeat("a", 250) + "@b.vn"} {
		if _, err := store.EmailSubject(bad); !errors.Is(err, store.ErrInvalidSubject) {
			t.Errorf("EmailSubject(%q) was accepted", bad)
		}
	}
}

// TestTheAuditPseudonymisationCanOnlyRedactGuestEntries is the privilege half of
// 00052: the application gains EXECUTE on one function and no UPDATE, and the
// function cannot be turned on a venue's or an operator's name, nor write anything
// but a pseudonym.
func TestTheAuditPseudonymisationCanOnlyRedactGuestEntries(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	w.appendAudit(t, "operator-created", "operator", "op-7", strp("room101"), map[string]any{"username": "room101"})
	w.appendAudit(t, "account-credentials-changed", "account", "acc-7", strp("room101"), nil)
	app := w.app.Pool()

	var n int64
	fatalf(t, app.QueryRow(ctx, `SELECT foyer_audit_pseudonymise(NULL, ARRAY['room101'], 'pseudonym:0123456789abcdef')`).Scan(&n),
		"calling the function as the application")
	if n != 1 {
		t.Errorf("rewrote %d entries, want 1 (the account's)", n)
	}
	if got := w.count(t, `SELECT count(*) FROM audit_log WHERE subject_kind = 'operator' AND subject_name = 'room101' AND params->>'username' = 'room101'`); got != 1 {
		t.Error("the operator's entry was rewritten: a literal may only be redacted on a guest's entry")
	}
	for what, call := range map[string]string{
		"a pseudonym of another shape": `SELECT foyer_audit_pseudonymise(NULL, ARRAY['room101'], 'anything I like')`,
		"a one-character identifier":   `SELECT foyer_audit_pseudonymise(NULL, ARRAY['r'], 'pseudonym:0123456789abcdef')`,
		"a MAC that is not one":        `SELECT foyer_audit_pseudonymise('.*', NULL, 'pseudonym:0123456789abcdef')`,
	} {
		_, err := app.Exec(ctx, call)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "22023" {
			t.Errorf("%s was accepted: %v", what, err)
		}
	}
	_, err := app.Exec(ctx, `UPDATE audit_log SET subject_name = 'pseudonym:0123456789abcdef'`)
	requireRefused(t, err, "foyer_app UPDATE audit_log", "audit_log")
}

// TestTheAuditPseudonymIsKeyedAndStable: one subject, one pseudonym under one KEK;
// another KEK, another pseudonym; and it never contains the identifier.
func TestTheAuditPseudonymIsKeyedAndStable(t *testing.T) {
	s, err := store.MACSubject("aa:bb:cc:00:11:22")
	fatalf(t, err, "parsing")
	k1, k2 := testKeyring(t), testKeyring(t)
	a, err := s.AuditPseudonym(k1)
	fatalf(t, err, "pseudonym")
	b, _ := s.AuditPseudonym(k1)
	c, _ := s.AuditPseudonym(k2)
	if a != b {
		t.Errorf("the same subject under the same key is %q and %q", a, b)
	}
	if a == c {
		t.Error("two keys give the same pseudonym: it is not keyed")
	}
	if len(a) != len("pseudonym:")+16 || strings.Contains(a, "aabbcc") {
		t.Errorf("pseudonym %q", a)
	}
}

// ── An erasure whose record cannot be written does not happen on Foyer's side

// TestAnErasureWhoseAuditEntryCannotBeWrittenRollsBack — the entry is written in the
// same transaction as the application's erasers, so a failure to write it leaves
// the grant, the session and the account where they were.
func TestAnErasureWhoseAuditEntryCannotBeWrittenRollsBack(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	username := uniq("an")
	acc := seedAccount(t, w.app, w.site.ID, username)
	g := w.grant(t, "02:00:00:00:34:01", &acc.ID, now, nil)
	w.portalSession(t, "02:00:00:00:34:01", &g, now.Add(time.Hour))
	w.appendAudit(t, "account-credentials-changed", "account", acc.ID, strp(username), nil)
	s, err := w.app.AccountSubject(ctx, acc.ID)
	fatalf(t, err, "resolving")

	for name, record := range map[string]func(store.ErasureReport) (*store.AuditEntry, error){
		"the entry cannot be built": func(store.ErasureReport) (*store.AuditEntry, error) { return nil, errors.New("disk full") },
		// A duplicate id: the INSERT itself is refused inside the transaction.
		"the entry cannot be written": func(store.ErasureReport) (*store.AuditEntry, error) {
			var id string
			fatalf(t, w.owner.QueryRow(ctx, `SELECT id FROM audit_log LIMIT 1`).Scan(&id), "reading an id")
			return &store.AuditEntry{ID: id, ActorID: "op-1", ActorUsername: "linh", Kind: "data-subject-erased",
				SubjectKind: "data-subject", SubjectID: "era-1"}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.EraseRecorded(ctx, w.app, w.ret, s, record); err == nil {
				t.Fatal("the erasure succeeded with no audit entry")
			}
			for what, sql := range map[string]string{
				"the account":         `SELECT count(*) FROM accounts WHERE id = '` + acc.ID + `'`,
				"its grant":           `SELECT count(*) FROM portal_grants WHERE id = '` + g + `'`,
				"the grant's session": `SELECT count(*) FROM portal_sessions WHERE grant_id = '` + g + `'`,
				"the named entry":     `SELECT count(*) FROM audit_log WHERE subject_name = '` + username + `'`,
			} {
				if n := w.count(t, sql); n != 1 {
					t.Errorf("%s: %d row(s) after a failed, unrecorded erasure, want 1", what, n)
				}
			}
		})
	}

	// And the recorded path writes exactly one entry, in the same commit.
	before := w.count(t, `SELECT count(*) FROM audit_log WHERE kind = 'data-subject-erased'`)
	_, err = store.EraseRecorded(ctx, w.app, w.ret, s, func(rep store.ErasureReport) (*store.AuditEntry, error) {
		return &store.AuditEntry{ID: uniq("aud"), ActorID: "op-1", ActorUsername: "linh", Kind: "data-subject-erased",
			SubjectKind: "data-subject", SubjectID: "era-2", SubjectName: strp(rep.Subject.Masked()),
			Params: map[string]any{"total": rep.Total}}, nil
	})
	fatalf(t, err, "erasing, recorded")
	if n := w.count(t, `SELECT count(*) FROM audit_log WHERE kind = 'data-subject-erased'`); n != before+1 {
		t.Errorf("%d erasure entries, want %d", n, before+1)
	}
}
