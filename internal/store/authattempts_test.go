package store_test

import (
	"context"
	"testing"

	"github.com/kiennt048/foyer/internal/store"
)

// A site scope on `radpostauth` is a security property, not a filter.
//
// The table keeps the stock FreeRADIUS column set verbatim -- it is the project's
// exit hatch -- so it carries **no NAS address and no tenant**. There is nothing to
// join a client on, which is why `ListSessions`'s longest-prefix match has no
// counterpart here, and why the obvious repository answers a site-scoped question
// with every site's usernames and MACs.
//
// `Called-Station-Id` is the one identifier the row does carry, and
// `access_points.mac` resolves it to a device and a site. That resolution lived in
// `internal/apistore` for a while, which meant the repository could not be asked a
// scoped question at all: `ListOptions.SiteID` was a field on the filter that
// nothing read -- a switch that looks like a switch and is not.
//
// Two halves, and the second is the one a scoping fix usually breaks: a row whose
// called-station resolves to nothing is **excluded** when a site is asked for,
// because including it would be the leak, and **visible** when none is, because an
// attempt from a device nothing recognises is the ordinary symptom of the most
// common real fault.
func TestAnAuthAttemptSiteScopeIsAppliedByTheRepository(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	owner := ownerPool(t)

	siteA := seedSite(t, st)
	siteB := seedSite(t, st)
	seedDeviceWorld(t, st, siteA, []string{"94:2a:6f:e1:04:01"}, []string{"10.85.0.11"})
	seedDeviceWorld(t, st, siteB, []string{"94:2a:6f:e1:05:01"}, []string{"10.86.0.11"})

	apA, err := st.AccessPoints().GetByMAC(ctx, "94:2a:6f:e1:04:01")
	fatalf(t, err, "resolving site A's access point")

	// One prefix for this test's rows, because `radpostauth` has no tenant and the
	// container is shared: every other test's attempts are in the same table.
	prefix := uniq("attempt")
	rows := []struct{ username, called string }{
		{prefix + "-lobby", "94-2A-6F-E1-04-01:Guest"},
		{prefix + "-warehouse", "94:2a:6f:e1:05:01:Staff"},
		// A device nothing recognises. The most common real fault looks exactly
		// like this row.
		{prefix + "-stranger", "ff:ff:ff:ff:ff:ff:Unknown"},
	}
	for _, r := range rows {
		_, err := owner.Exec(ctx, `
			INSERT INTO radpostauth (username, reply, callingstationid, calledstationid, authdate)
			VALUES ($1, 'Access-Reject', '02:11:22:33:44:05', $2, now())`, r.username, r.called)
		fatalf(t, err, "seeding a post-auth row")
	}

	usernames := func(p store.Page[store.AuthAttempt]) map[string]store.AuthAttempt {
		out := map[string]store.AuthAttempt{}
		for _, a := range p.Items {
			out[a.Username] = a
		}
		return out
	}

	scoped, err := st.Accounting().ListAuthAttempts(ctx, store.AuthAttemptFilter{
		ListOptions: store.ListOptions{SiteID: siteA.ID, Query: prefix, Limit: 50},
	})
	fatalf(t, err, "listing site A's attempts")
	got := usernames(scoped)

	if _, ok := got[prefix+"-warehouse"]; ok {
		t.Error("site B's username and MAC were returned under a scope for site A")
	}
	if _, ok := got[prefix+"-stranger"]; ok {
		t.Error("an attempt on an unrecognised access point was returned under a site scope, which is the leak")
	}
	lobby, ok := got[prefix+"-lobby"]
	if !ok {
		t.Fatal("site A's own attempt did not survive a scope for site A, so the scope is not a filter but a wall")
	}
	if lobby.SiteID == nil || *lobby.SiteID != siteA.ID {
		t.Errorf("siteId = %v, want %q resolved from the called-station", lobby.SiteID, siteA.ID)
	}
	if lobby.NasID == nil || *lobby.NasID != apA.ID {
		t.Errorf("nasId = %v, want the device %q; without it the operator cannot tell where the attempt happened",
			lobby.NasID, apA.ID)
	}

	unscoped, err := st.Accounting().ListAuthAttempts(ctx, store.AuthAttemptFilter{
		ListOptions: store.ListOptions{Query: prefix, Limit: 50},
	})
	fatalf(t, err, "listing every attempt")
	all := usernames(unscoped)
	for _, r := range rows {
		if _, ok := all[r.username]; !ok {
			t.Errorf("%s is invisible with no site asked for, so the fault it signals cannot be diagnosed", r.username)
		}
	}
}

// MAC coverage is per site, because the answer changes what the operator is told to
// do about it.
//
// The stock FreeRADIUS post-auth query leaves the station-id columns empty, so
// `not-recorded` means "go and look at the RADIUS configuration". Answered
// deployment-wide, one venue running stock configuration makes every other venue
// report a fault it does not have.
func TestMacCoverageIsAnsweredPerSite(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	owner := ownerPool(t)

	recording := seedSite(t, st)
	silent := seedSite(t, st)
	seedDeviceWorld(t, st, recording, []string{"94:2a:6f:e1:06:01"}, []string{"10.87.0.11"})
	seedDeviceWorld(t, st, silent, []string{"94:2a:6f:e1:07:01"}, []string{"10.88.0.11"})

	_, err := owner.Exec(ctx, `
		INSERT INTO radpostauth (username, reply, callingstationid, calledstationid, authdate)
		VALUES ($1, 'Access-Accept', '02:11:22:33:44:06', '94:2a:6f:e1:06:01:Guest', now())`, uniq("guest"))
	fatalf(t, err, "seeding an attempt that carries a MAC")

	// The stock query's own behaviour: a row with the station-id columns left empty.
	_, err = owner.Exec(ctx, `
		INSERT INTO radpostauth (username, reply, calledstationid, authdate)
		VALUES ($1, 'Access-Accept', '94:2a:6f:e1:07:01:Guest', now())`, uniq("guest"))
	fatalf(t, err, "seeding an attempt with no MAC")

	got, err := st.Accounting().MacCoverage(ctx, recording.ID)
	fatalf(t, err, "reading the recording site's coverage")
	if got != "recorded" {
		t.Errorf("the site whose attempts carry MACs reports %q", got)
	}

	got, err = st.Accounting().MacCoverage(ctx, silent.ID)
	fatalf(t, err, "reading the silent site's coverage")
	if got != "not-recorded" {
		t.Errorf("the site whose attempts carry no MAC reports %q, so the fault is invisible where it is", got)
	}
}
