package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// TestAnUnlimitedGrantIsAgedFromWhenItEnded: the portalGrants period counts from
// when a grant stopped meaning anything, and a grant with no expiry and no
// revocation has not stopped while its device is still heard from at the venue.
//
// An earlier sweep dated such a grant by `granted_at`, so a guest on an
// unlimited grant who was online right now lost the console's record of it on day
// 91.
func TestAnUnlimitedGrantIsAgedFromWhenItEnded(t *testing.T) {
	w := newPrivacyWorld(t)
	ctx := context.Background()
	now := time.Now().UTC()
	longAgo := now.Add(-400 * 24 * time.Hour)

	there := &store.Site{ID: uniq("site"), Name: "The Other Venue", Environment: "lab"}
	fatalf(t, w.app.Sites().Create(ctx, there), "creating the second venue")
	w.exec(t, `INSERT INTO radius_clients (id, site_id, name, address, source, driver) VALUES
		($1, $2, 'here', '10.41.1.0/24', 'manual', 'coa'), ($3, $4, 'there', '10.41.2.0/24', 'manual', 'coa')`,
		uniq("rc"), w.site.ID, uniq("rc"), there.ID)
	session := func(nas, calling string, start time.Time, update, stop *time.Time) {
		t.Helper()
		uid := uniq("acct")
		w.exec(t, `INSERT INTO radacct (acctsessionid, acctuniqueid, username, nasipaddress,
			acctstarttime, acctupdatetime, acctstoptime, callingstationid)
			VALUES ($1, $1, 'guest', $2, $3, $4, $5, $6)`, uid, nas, start, update, stop, calling)
	}
	yesterday := now.Add(-24 * time.Hour)
	anHourAgo := now.Add(-time.Hour)

	// Online now at this venue, under a NAS spelling of its MAC the grant does not use.
	online := w.grant(t, "02:00:00:00:10:01", nil, longAgo, nil)
	session("10.41.1.5", "02-00-00-00-10-01", yesterday, &anHourAgo, nil)

	// Heard from yesterday, session closed: inside the period.
	recent := w.grant(t, "02:00:00:00:10:02", nil, longAgo, nil)
	session("10.41.1.5", "020000001002", yesterday.Add(-time.Hour), nil, &yesterday)

	// Online right now — but at the OTHER venue, which keeps nothing alive here.
	elsewhere := w.grant(t, "02:00:00:00:10:03", nil, longAgo, nil)
	session("10.41.2.5", "02:00:00:00:10:03", yesterday, &anHourAgo, nil)

	// Superseded: granted again at this venue 300 days ago, so the old grant ended then
	// even though the device is online now under the new one.
	superseded := w.grant(t, "02:00:00:00:10:04", nil, longAgo, nil)
	successor := w.grant(t, "02:00:00:00:10:04", nil, now.Add(-300*24*time.Hour), nil)
	session("10.41.1.5", "02:00:00:00:10:04", yesterday, &anHourAgo, nil)

	// Never heard from at all.
	silent := w.grant(t, "02:00:00:00:10:05", nil, longAgo, nil)

	_, err := store.SweepRetention(ctx, store.SweepTargets{App: w.app, Retention: w.ret}, now, discard())
	fatalf(t, err, "sweeping")

	has := func(id string) bool {
		return w.count(t, `SELECT count(*) FROM portal_grants WHERE id = $1`, id) == 1
	}
	for _, c := range []struct {
		what string
		id   string
		want bool
	}{
		{"an unlimited grant whose device is online now", online, true},
		{"an unlimited grant whose device was here yesterday", recent, true},
		{"an unlimited grant whose device is only online at another venue", elsewhere, false},
		{"an unlimited grant superseded 300 days ago", superseded, false},
		{"the grant that superseded it, still in use", successor, true},
		{"an unlimited grant never heard from", silent, false},
	} {
		if got := has(c.id); got != c.want {
			t.Errorf("%s: kept = %v, want %v", c.what, got, c.want)
		}
	}
}
