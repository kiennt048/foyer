package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// TestASessionsIdentityKindIsTheRecordedOne: whether a session is a voucher's is
// read from the vouchers and accounts tables, not guessed from the username's
// shape — because the session export masks a voucher code on it, and a guess that
// said `account` exported the code whole.
func TestASessionsIdentityKindIsTheRecordedOne(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()

	w.exec(t, `INSERT INTO radius_clients (id, site_id, name, address, source, driver) VALUES
		($1, $2, 'here', '10.51.1.0/24', 'manual', 'coa')`, uniq("rc"), w.site.ID)
	batch := &store.VoucherBatch{
		ID: uniq("bat"), SiteID: w.site.ID, Label: "Front desk", Count: 2, ExpiryMode: "from-first-use",
		Plan: store.Plan{EnforcedBy: "nas"},
	}
	fatalf(t, w.app.Vouchers().CreateBatch(ctx, batch, []store.NewVoucher{
		{ID: uniq("v"), Code: "ABCDEFGH2345"},
		{ID: uniq("v"), Code: "BCDEFGHJ3456"},
	}), "creating a batch")
	// An account whose name has a voucher code's shape: twelve upper-case alphanumerics.
	seedAccount(t, w.app, w.site.ID, "ROOM12345678")

	want := map[string]string{
		"ABCDEFGH2345":      "voucher", // the code as issued
		"bcdefghj-3456":     "voucher", // the same code, respelled by the NAS — the case the guess missed
		"ROOM12345678":      "account", // looks like a code, is an account
		"ZZZZZZZZ9999":      "voucher", // nobody's, code-shaped: the fallback still masks it
		"02-00-00-00-51-01": "mac-auth",
	}
	for username := range want {
		uid := uniq("acct")
		w.exec(t, `INSERT INTO radacct (acctsessionid, acctuniqueid, username, nasipaddress,
			acctstarttime, acctupdatetime, callingstationid)
			VALUES ($1, $1, $2, '10.51.1.5', $3, $3, '02-00-00-00-51-01')`, uid, username, now.Add(-time.Minute))
	}

	got := map[string]string{}
	err := w.app.Accounting().SessionsStartedBetween(ctx, w.site.ID, now.Add(-time.Hour), now.Add(time.Hour),
		func(s store.Session) error {
			if s.Username != nil {
				got[*s.Username] = s.IdentityKind
			}
			return nil
		})
	fatalf(t, err, "reading the sessions")
	for username, kind := range want {
		if got[username] != kind {
			t.Errorf("%q: identity kind %q, want %q", username, got[username], kind)
		}
	}
}
