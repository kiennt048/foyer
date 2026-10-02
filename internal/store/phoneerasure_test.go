package store_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kiennt048/foyer/internal/store"
)

func TestPhoneSubjectAcceptsOnlyPhoneNumbers(t *testing.T) {
	for _, ok := range []string{"0912 345 678", "+84 912 345 678", "84912345678", "(091) 234-5678"} {
		s, err := store.PhoneSubject(ok)
		if err != nil || !slices.Equal(s.PhoneDigits, []string{"84912345678", "0912345678"}) {
			t.Errorf("PhoneSubject(%q) = %+v, %v", ok, s, err)
		}
		if s.Masked() != "••••••••678" {
			t.Errorf("PhoneSubject(%q) is named %q", ok, s.Masked())
		}
	}
	for _, bad := range []string{"", "1234567", "0912345678901234", "guest@example.com", "09+12345678", "0912 345 678 ext 9"} {
		if _, err := store.PhoneSubject(bad); err == nil {
			t.Errorf("PhoneSubject(%q) was accepted", bad)
		}
	}
}

// TestEraseByPhoneRemovesThatNumberFromEveryRegisteredTable — the phone half of the
// erasure: phone numbers sit in five tables, and one erasure must reach all of
// them. Every registered column that identifies a phone subject holds no row of it
// afterwards, in whatever spelling the row was written; a bystander keeps theirs.
func TestEraseByPhoneRemovesThatNumberFromEveryRegisteredTable(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	stop := now
	const mac, other = "02:00:00:00:77:01", "02:00:00:00:77:02"

	phoneGrant := func(mac, phone string) string {
		t.Helper()
		id := uniq("grant")
		w.exec(t, `INSERT INTO portal_grants (id, site_id, client_mac, method, enforced_by, granted_at, phone)
			VALUES ($1, $2, $3::macaddr, 'phone', 'unenforced', $4, $5)`, id, w.site.ID, mac, now, phone)
		return id
	}
	mine := phoneGrant(mac, "+84912345678")
	theirs := phoneGrant(other, "+84987654321")
	w.portalSession(t, mac, &mine, now.Add(time.Hour))
	w.exec(t, `INSERT INTO portal_addresses (address, token_hash, client_mac, grant_id, seen_at)
		VALUES ('10.20.0.60', 'tok-phone', $1::macaddr, $2, $3)`, mac, mine, now)
	w.authEvent(t, mine, mac, ptrTo("+84912345678"), now)
	w.authEvent(t, theirs, other, ptrTo("+84987654321"), now)
	// A voucher guest who typed the same number, spelled their own way.
	voucherish := w.grant(t, "02:00:00:00:77:03", nil, now, nil)
	w.exec(t, `INSERT INTO guest_details (grant_id, site_id, phone) VALUES ($1, $2, '0912 345 678')`, voucherish, w.site.ID)
	w.exec(t, `INSERT INTO phone_codes (id, site_id, session_token_hash, client_mac, phone, code_mac, key_id,
		created_at, expires_at, max_attempts)
		VALUES ('pc-1', $1, 'tok-phone', $2::macaddr, '+84912345678', $3, 'k1', $4, $5, 5)`,
		w.site.ID, mac, make([]byte, 32), now, now.Add(5*time.Minute))
	w.acct(t, "0912345678", mac, now.Add(-time.Hour), &stop)
	w.postauth(t, "84912345678", mac, now)
	w.acct(t, "+84987654321", other, now.Add(-time.Hour), &stop)

	s, err := store.PhoneSubject("0912-345-678")
	fatalf(t, err, "parsing the subject")
	rep, err := store.Erase(ctx, w.app, w.ret, s)
	fatalf(t, err, "erasing")

	checked := 0
	for _, c := range store.PersonalColumns() {
		if !slices.Contains(c.Identifies, store.SubjectPhone) || c.Pseudonymised {
			continue
		}
		checked++
		col := pgx.Identifier{c.Column}.Sanitize()
		if n := w.count(t, fmt.Sprintf(`SELECT count(*) FROM %s WHERE regexp_replace(coalesce(%s, ''), '[^0-9]', '', 'g') = ANY($1)`,
			pgx.Identifier{c.Table}.Sanitize(), col), s.PhoneDigits); n != 0 {
			t.Errorf("%s.%s still holds the erased number in %d row(s)", c.Table, c.Column, n)
		}
	}
	if checked < 7 {
		t.Fatalf("only %d registered phone columns were checked", checked)
	}
	for what, sql := range map[string]string{
		"the grant's portal session":     `SELECT count(*) FROM portal_sessions WHERE grant_id = '` + mine + `'`,
		"the grant's address-book entry": `SELECT count(*) FROM portal_addresses WHERE token_hash = 'tok-phone'`,
		"the voucher guest's details":    `SELECT count(*) FROM guest_details WHERE grant_id = '` + voucherish + `'`,
	} {
		if n := w.count(t, sql); n != 0 {
			t.Errorf("%s survived the erasure", what)
		}
	}
	if n := w.count(t, `SELECT count(*) FROM portal_grants WHERE id = $1`, voucherish); n != 1 {
		t.Error("the voucher guest's grant went too: only the details give the number")
	}
	for table, want := range map[string]int{"portal_grants": 2, "authorization_events": 1, "radacct": 1} {
		if n := w.count(t, `SELECT count(*) FROM `+table); n != want {
			t.Errorf("%s holds %d row(s) after erasing somebody else's number, want %d", table, n, want)
		}
	}
	if rep.Total < 7 {
		t.Errorf("the report counts %d rows: %+v", rep.Total, rep.Removed)
	}
}
