package store_test

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// Reading accounting is the one place the three-noun model has to pay for itself at
// query time: a `radacct` row names an IP address and nothing else, and everything
// the console shows beside it -- which site, which device, which SSID -- comes from
// resolving that address.

func seedAccounting(t *testing.T, siteID, uniqueID, nasIP, calling, called string, in, out *int64, start, update time.Time, stop *time.Time) {
	t.Helper()
	seedAccountingEvery(t, siteID, uniqueID, nasIP, calling, called, in, out, start, update, stop, 300)
}

// The same, with the NAS's requested `Acct-Interim-Interval` chosen by the caller.
//
// It is a parameter because 300 cannot express the row that matters to
// `TestTheOrphanCountIsDatedOverTheRowsItCounts`: `sessionState` orphans a row after
// `2 * max(coalesce(acctinterval, 300), 300)`, so at 300 every row is judged at ten
// minutes and an *online* row can never be older than an *orphaned* one. A world
// that cannot order those two ways round cannot tell a `min` over the counted rows
// from a `min` over their union -- an input the harness cannot be wrong about
// (`docs/rules/testing.md`, Principles).
func seedAccountingEvery(t *testing.T, siteID, uniqueID, nasIP, calling, called string, in, out *int64, start, update time.Time, stop *time.Time, interval int) {
	t.Helper()
	owner := ownerPool(t)
	_, err := owner.Exec(context.Background(), `
		INSERT INTO radacct
			(acctsessionid, acctuniqueid, username, nasipaddress, acctstarttime,
			 acctupdatetime, acctstoptime, acctinterval, acctinputoctets, acctoutputoctets,
			 calledstationid, callingstationid)
		VALUES ($1, $1, 'FOYERTEST1234', $2::inet, $3, $4, $5, $10, $6, $7, $8, $9)`,
		uniqueID, nasIP, start, update, stop, in, out, called, calling, interval)
	fatalf(t, err, "seeding radacct as the owner")
}

// FreeRADIUS matches a client by longest prefix, and so does this. A site whose
// twelve access points sit behind one 10.50.0.0/24 clients.conf entry has twelve
// sessions that all resolve to that entry -- and a /32 added for one of them wins
// for that one.
func TestSessionsResolveTheirClientByLongestPrefix(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	secret := mustSecret(t, st, site.ID)

	fleet := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site.ID, Name: "Fleet", Address: netip.MustParsePrefix("10.50.0.0/24"),
		Source: "manual", Driver: "unifi", SharedSecretID: &secret.ID, Enabled: true,
	}
	fatalf(t, st.RadiusClients().Create(ctx, fleet), "creating the fleet client")

	specific := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site.ID, Name: "Terrace", Address: netip.MustParsePrefix("10.50.0.9/32"),
		Source: "manual", Driver: "unifi", SharedSecretID: &secret.ID, Enabled: true,
	}
	fatalf(t, st.RadiusClients().Create(ctx, specific), "creating the specific client")

	now := time.Now()
	inside := uniq("sess")
	seedAccounting(t, site.ID, inside, "10.50.0.4", "02:11:22:33:44:55",
		"94-2A-6F-D0-30-57:foyer-guest", ptrInt64(1024), ptrInt64(2048), now, now, nil)

	exact := uniq("sess")
	seedAccounting(t, site.ID, exact, "10.50.0.9", "02:11:22:33:44:66",
		"94-2A-6F-D0-30-58:foyer-guest", ptrInt64(1), ptrInt64(1), now, now, nil)

	got, err := st.Accounting().GetSession(ctx, inside)
	fatalf(t, err, "reading the session inside the range")
	if got.NasID != fleet.ID {
		t.Fatalf("10.50.0.4 resolved to %q, want the /24", got.NasID)
	}
	if got.SiteID != site.ID {
		t.Fatalf("session resolved to site %q", got.SiteID)
	}
	if got.SSID == nil || *got.SSID != "foyer-guest" {
		t.Fatalf("SSID is %v, want foyer-guest parsed out of Called-Station-Id", got.SSID)
	}

	got, err = st.Accounting().GetSession(ctx, exact)
	fatalf(t, err, "reading the session with an exact client")
	if got.NasID != specific.ID {
		t.Fatalf("10.50.0.9 resolved to %q, want the /32 -- longest prefix must win", got.NasID)
	}
}

// A session whose NAS matches no client is not "no problem", it is a blind spot: the
// Access-Request was discarded without a reply, the guest saw a network that did not
// work, and nothing in the console would say so.
func TestUnmatchedNASAddressesAreCountable(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)

	orphan := uniq("sess")
	now := time.Now()
	seedAccounting(t, site.ID, orphan, "203.0.113.77", "02:99:88:77:66:55", "", nil, nil, now, now, nil)

	if _, err := st.Accounting().GetSession(ctx, orphan); err != store.ErrNotFound {
		t.Fatalf("a session from an unknown client appeared in the console's view: %v", err)
	}

	addrs, err := st.Accounting().UnmatchedClientAddresses(ctx)
	fatalf(t, err, "listing unmatched addresses")
	found := false
	for _, a := range addrs {
		if a == "203.0.113.77" {
			found = true
		}
	}
	if !found {
		t.Fatal("the unmatched NAS address is invisible; the blind spot cannot be reported")
	}
}

// The 32-bit counter wrap, and the reason no figure derived from these columns may
// be a bare subtraction.
func TestCountersWrappedTravelsWithTheFigure(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	secret := mustSecret(t, st, site.ID)

	client := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site.ID, Name: "Wrap", Address: netip.MustParsePrefix("10.60.0.1/32"),
		Source: "manual", Driver: "unifi", SharedSecretID: &secret.ID, Enabled: true,
	}
	fatalf(t, st.RadiusClients().Create(ctx, client), "creating a client")

	now := time.Now()

	small := uniq("sess")
	seedAccounting(t, site.ID, small, "10.60.0.1", "02:00:00:00:0a:01", "", ptrInt64(4096), ptrInt64(8192), now, now, nil)

	// One gigaword plus a little: what Acct-Input-Gigawords=1 combines to. On a
	// server that ignores gigawords this reads as 4 096 and the session's total
	// appears to have dropped by 4 294 967 296.
	wrapped := uniq("sess")
	seedAccounting(t, site.ID, wrapped, "10.60.0.1", "02:00:00:00:0a:02", "",
		ptrInt64(4294967296+4096), ptrInt64(512), now, now, nil)

	got, err := st.Accounting().GetSession(ctx, small)
	fatalf(t, err, "reading the small session")
	if got.CountersWrapped {
		t.Fatal("a session under 4 GiB is reported as wrapped")
	}

	got, err = st.Accounting().GetSession(ctx, wrapped)
	fatalf(t, err, "reading the wrapped session")
	if !got.CountersWrapped {
		t.Fatal("a session past 2^32 is not reported as wrapped; a raw NAS counter and this figure disagree and nothing says why")
	}
	if got.InputOctets == nil || *got.InputOctets != 4294967296+4096 {
		t.Fatalf("input octets are %v; the 64-bit value must survive", got.InputOctets)
	}
}

// A session with no Acct-Stop stays open forever if nothing reaps it. It is not
// online, and calling it online is the classic RADIUS accounting failure.
func TestOpenSessionWithNoUpdatesIsOrphanedNotOnline(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	secret := mustSecret(t, st, site.ID)

	client := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site.ID, Name: "Orphans", Address: netip.MustParsePrefix("10.70.0.1/32"),
		Source: "manual", Driver: "unifi", SharedSecretID: &secret.ID, Enabled: true,
	}
	fatalf(t, st.RadiusClients().Create(ctx, client), "creating a client")

	now := time.Now()

	live := uniq("sess")
	seedAccounting(t, site.ID, live, "10.70.0.1", "02:00:00:00:0b:01", "", ptrInt64(1), ptrInt64(1), now.Add(-time.Hour), now, nil)

	// Forty days open, twenty minutes since the last interim update. Twice the
	// 300-second floor is ten minutes, so this is past it.
	stale := uniq("sess")
	seedAccounting(t, site.ID, stale, "10.70.0.1", "02:00:00:00:0b:02", "",
		ptrInt64(1), ptrInt64(1), now.AddDate(0, 0, -40), now.Add(-20*time.Minute), nil)

	closed := uniq("sess")
	stop := now.Add(-30 * time.Minute)
	seedAccounting(t, site.ID, closed, "10.70.0.1", "02:00:00:00:0b:03", "",
		ptrInt64(1), ptrInt64(1), now.Add(-2*time.Hour), now.Add(-31*time.Minute), &stop)

	for id, want := range map[string]string{live: "online", stale: "orphaned", closed: "ended"} {
		got, err := st.Accounting().GetSession(ctx, id)
		fatalf(t, err, "reading session %s", id)
		if got.State != want {
			t.Fatalf("session %s is %q, want %q", id, got.State, want)
		}
	}

	counts, err := st.Accounting().CountSessions(ctx, site.ID)
	fatalf(t, err, "counting sessions")
	if counts.Online != 1 || counts.Orphaned != 1 {
		t.Fatalf("counts are online=%d orphaned=%d, want 1/1", counts.Online, counts.Orphaned)
	}
	if counts.OldestOnlineUpdate == nil {
		t.Fatal("no freshness timestamp for open sessions; a figure with no freshness is the failure this design exists to avoid")
	}
	// **The timestamp is taken over the rows the figure counts.**
	//
	// `Online` counts one row here and it was updated `now`; the twenty-minute-old
	// row is `Orphaned`, which is a different figure. A `min` over both makes the
	// adapter report the online
	// count as twenty minutes stale while it is seconds old, and the console renders
	// that sentence beside the number because `docs/rules/frontend.md` requires it to.
	//
	// Five minutes rather than a comparison against `live`'s exact timestamp: the
	// row is written with the test's own clock and read back through PostgreSQL's,
	// and the defect this catches is twenty minutes wide.
	if age := time.Since(*counts.OldestOnlineUpdate); age > 5*time.Minute {
		t.Fatalf("the online count's sample is %s old, but the only session it counts was updated now: "+
			"the timestamp was taken over rows the figure does not count", age)
	}
}

// **The second half: the orphan count dates itself, over its own rows.**
//
// The same rule as above -- a freshness is derived over exactly the rows its figure
// counts -- applied to the other figure, and it is the half that could not be
// expressed at all until `Overview.orphanedSessions` widened from a bare integer to
// a `Metric`. The integer says three sessions are unaccounted for. Only the
// timestamp says whether an access point rebooted half an hour ago or nothing has
// reaped these since July, and those are different faults with different repairs.
//
// ── the world, and why it is shaped like this ────────────────────────────────
//
// The obvious world -- one online row and one orphan -- cannot fail. Orphans are
// older than online rows *by construction* when every row shares the 300-second
// floor, so `min` over the orphans and `min` over their union are the same
// timestamp and the sabotage that ports the orphans-only defect onto this figure
// passes. Measured, not assumed: with that world, the union sabotage leaves the
// test green.
//
// What breaks the tie is `acctinterval`. A NAS that asked for an hour keeps a row
// `online` for two hours, so `quiet` below is **older than either orphan** while
// still being counted by `Online`. That one row is what makes the three ways this
// can be wrong produce three different answers:
//
//	correct (min over orphans)      35 minutes
//	min over online ∪ orphaned      90 minutes   <- the original defect, ported
//	max over orphans                20 minutes   <- the wrong end of the range
//	the online figure's timestamp   90 minutes   <- copied from the field above
func TestTheOrphanCountIsDatedOverTheRowsItCounts(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	secret := mustSecret(t, st, site.ID)

	client := &store.RadiusClient{
		ID: uniq("rc"), SiteID: site.ID, Name: "Orphan clock", Address: netip.MustParsePrefix("10.71.0.1/32"),
		Source: "manual", Driver: "unifi", SharedSecretID: &secret.ID, Enabled: true,
	}
	fatalf(t, st.RadiusClients().Create(ctx, client), "creating a client")

	now := time.Now()
	const ip = "10.71.0.1"

	// Asked for an hour, silent for ninety minutes: `2 * 3600` has not elapsed, so
	// this is `online` -- and it is the oldest row in the site.
	quiet := uniq("sess")
	seedAccountingEvery(t, site.ID, quiet, ip, "02:00:00:00:0c:01", "",
		ptrInt64(1), ptrInt64(1), now.Add(-3*time.Hour), now.Add(-90*time.Minute), nil, 3600)

	// Two orphans, twenty and thirty-five minutes silent against the ten-minute
	// threshold their own interval gives them. Two rather than one so that `min`
	// and `max` over the counted rows are different answers.
	recent := uniq("sess")
	seedAccountingEvery(t, site.ID, recent, ip, "02:00:00:00:0c:02", "",
		ptrInt64(1), ptrInt64(1), now.Add(-2*time.Hour), now.Add(-20*time.Minute), nil, 300)
	older := uniq("sess")
	seedAccountingEvery(t, site.ID, older, ip, "02:00:00:00:0c:03", "",
		ptrInt64(1), ptrInt64(1), now.Add(-2*time.Hour), now.Add(-35*time.Minute), nil, 300)

	for id, want := range map[string]string{quiet: "online", recent: "orphaned", older: "orphaned"} {
		got, err := st.Accounting().GetSession(ctx, id)
		fatalf(t, err, "reading session %s", id)
		if got.State != want {
			t.Fatalf("session %s is %q, want %q -- the world this test needs is not the world it built", id, got.State, want)
		}
	}

	counts, err := st.Accounting().CountSessions(ctx, site.ID)
	fatalf(t, err, "counting sessions")
	if counts.Online != 1 || counts.Orphaned != 2 {
		t.Fatalf("counts are online=%d orphaned=%d, want 1/2", counts.Online, counts.Orphaned)
	}
	if counts.OldestOrphanUpdate == nil {
		t.Fatal("two sessions are orphaned and the count carries no timestamp: the one useful " +
			"sentence about an orphan count is when the oldest of them was last heard from")
	}

	// **The non-vacuity half.** This world is only evidence while the online figure
	// is older than the orphan figure -- that inversion is the whole reason the
	// hour-interval row is here, and without it every assertion below passes under
	// the rule this test rejects.
	if counts.OldestOnlineUpdate == nil || !counts.OldestOnlineUpdate.Before(*counts.OldestOrphanUpdate) {
		t.Fatal("the oldest online row is no longer older than the oldest orphan, so this world " +
			"cannot distinguish a min over the counted rows from a min over their union: " +
			"it is not evidence for either")
	}

	age := time.Since(*counts.OldestOrphanUpdate)
	// Between the two orphans (20 and 35 minutes) and below the online row (90).
	// Every wrong answer this can give lands outside it.
	if age < 25*time.Minute || age > 60*time.Minute {
		t.Fatalf("the orphan count's sample is %s old; the older of the two rows it counts was last "+
			"heard from 35 minutes ago. 20 minutes is the newest of them rather than the oldest, "+
			"and 90 minutes is a timestamp taken over rows this figure does not count", age)
	}
}

// iOS 14+ and Android 10+ present a private MAC per SSID by default, so a randomised
// address is the ordinary case rather than an anomaly -- and the console must never
// present one as an identity.
func TestRandomisedMACsAreFlagged(t *testing.T) {
	cases := map[string]bool{
		"02:11:22:33:44:55": true,  // locally administered
		"94:2a:6f:d0:30:57": false, // Ubiquiti OUI
		"06-AA-BB-CC-DD-EE": true,
		"942a6fd03057":      false,
		"":                  false,
	}
	for mac, want := range cases {
		if got := store.MACIsLocallyAdministered(mac); got != want {
			t.Errorf("MACIsLocallyAdministered(%q) = %v, want %v", mac, got, want)
		}
	}
}

// The stock post-auth query leaves the station-id columns empty, so a stock
// deployment records no MAC on any attempt -- and the MAC is the only identifier an
// operator has when a guest is standing in front of them.
func TestMacCoverageDistinguishesEmptyFromUnrecorded(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	owner := ownerPool(t)

	// Foyer's generated query populates them.
	_, err := owner.Exec(ctx, `
		INSERT INTO radpostauth (username, reply, callingstationid, calledstationid, authdate)
		VALUES ($1, 'Access-Reject', '02:00:00:00:0c:01', 'AA-BB-CC-DD-EE-FF:guest', now())`, uniq("u"))
	fatalf(t, err, "seeding radpostauth")

	coverage, err := st.Accounting().MacCoverage(ctx, "")
	fatalf(t, err, "reading MAC coverage")
	if coverage != "recorded" {
		t.Fatalf("coverage is %q with a MAC-bearing row present", coverage)
	}

	page, err := st.Accounting().ListAuthAttempts(ctx, store.AuthAttemptFilter{
		ListOptions: store.ListOptions{Limit: 10},
		Outcome:     "rejected",
	})
	fatalf(t, err, "listing auth attempts")
	if len(page.Items) == 0 {
		t.Fatal("no rejected attempts listed")
	}
	for _, a := range page.Items {
		if a.Outcome != "rejected" {
			t.Fatalf("outcome filter let a %q through", a.Outcome)
		}
	}
}

func ptrInt64(v int64) *int64 { return &v }
