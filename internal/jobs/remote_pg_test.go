package jobs_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/kiennt048/foyer/internal/jobs"
	"github.com/kiennt048/foyer/internal/store"
)

// TestAnErasureThroughTheDoorRemovesTheRetainedRows drives the maintenance split end
// to end on PostgreSQL 16: the control plane's `store.Erase` is given the door's
// client instead of a retention connection, and the FreeRADIUS rows naming the MAC
// are gone afterwards — erased by the retention connection only the maintenance
// side holds — with the receipt counting them.
func TestAnErasureThroughTheDoorRemovesTheRetainedRows(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	const mac = "02:0e:0e:0e:0e:01"
	sess := "erase-door-" + randomString()
	w.exec(t, `INSERT INTO radacct (acctsessionid, acctuniqueid, nasipaddress, acctstarttime, acctstoptime, callingstationid)
		VALUES ($1, $1, '10.99.0.1', now() - interval '2 hours', now() - interval '1 hour', $2)`, sess, mac)

	h, err := jobs.NewServer(jobs.ServerConfig{Token: token, App: w.app, Retention: w.retention, Logger: quiet()})
	must(t, err, "building the door")
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	client, err := jobs.NewRemoteClient(ts.URL, token)
	must(t, err, "building the client")

	s, err := store.MACSubject(mac)
	must(t, err, "parsing the MAC")
	rep, err := store.Erase(ctx, w.app, client, s)
	must(t, err, "erasing through the door")
	if n := w.count(t, `SELECT count(*) FROM radacct WHERE acctsessionid = $1`, sess); n != 0 {
		t.Fatalf("after an erasure through the door %d radacct row(s) still name the MAC", n)
	}
	var counted int64
	for _, r := range rep.Removed {
		if r.Table == "radacct" {
			counted = r.Rows
		}
	}
	if counted < 1 {
		t.Fatalf("the receipt counts %d radacct rows, want the one removed: %+v", counted, rep.Removed)
	}
}
