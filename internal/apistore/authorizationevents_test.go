package apistore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
)

// The failure a guest cannot see and an operator could not find.
//
// A valid voucher, a grant that exists, a `303` already on the wire — and a
// controller that refuses. `internal/portal`'s invariant 4 puts the driver call
// **after** the response, so nothing about that refusal can reach the guest through
// the response they are already reading, and no `radacct` row will ever exist for a
// device that never got on. The audit log cannot hold it either: `AuditEntry`
// requires an `actorId`, and the actor here is a guest with a voucher.
//
// The reader for it is in the contract — `listAuthAttempts`, `source: driver`,
// `kind: authorize-failed` — and migration 00014 is what gives it a writer.
// Everything below drives that path end to end
// against a real PostgreSQL, and none of it is provable without one: the transaction,
// the constraints, the RLS policy and the sequence grant are properties of the
// database.
//
// The driver is `nas.MockDriver` and is labelled a fake. Nothing here is evidence
// about UniFi. What it is evidence about is the schema, the adapter, the ports and
// the two screens that read the result.

// ── waiting for something that happens after the response ────────────────────

// awaitRecording waits, bounded, for the post-response driver call to be recorded.
//
// **The wait is an observation, not the assertion.** The `303` is flushed before the
// controller is asked, so `http.Client.Do` returns while the driver call is still in
// flight — a single read of `portal_grants.delivery` taken the instant the response
// arrives is a race by construction, and `docs/rules/testing.md` has the case
// where exactly that shape produced a test that was 6/6 green alone and 3/3 red
// behind a slower spec. What is asserted is what the settled row says.
//
// **It waits for *either* half, deliberately.** Waiting on the grant's `delivery`
// alone would make a split implementation — one that writes the operator's event and
// not the guest's column — time out here, and the failure would read *nothing was
// recorded* when the truth is *half of it was*. A wait keyed on one of the two things
// under test cannot report a disagreement between them. The two land atomically when
// the transaction is intact, so this costs no flakiness.
func (v *venue) awaitRecording(ctx context.Context, grantID string) {
	v.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if v.delivery(ctx, grantID) != nil || len(v.driverEvents(ctx)) > 0 {
			return
		}
		if time.Now().After(deadline) {
			v.t.Fatalf("nothing was recorded for grant %s within 10s — neither the operator's "+
				"event nor the guest's delivery. Either the Authorizations port is not wired "+
				"or the write failed silently", grantID)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// delivery reads the guest's half straight out of the table.
//
// SQL rather than the adapter, for the reason `grantRow` gives: the adapter is what
// is under test, and a read that went through the same conversion as the write could
// agree with itself about a column that is wrong.
func (v *venue) delivery(ctx context.Context, grantID string) *string {
	v.t.Helper()
	var d *string
	if err := v.owner.QueryRow(ctx,
		`SELECT delivery FROM portal_grants WHERE id = $1`, grantID).Scan(&d); err != nil {
		v.t.Fatalf("reading portal_grants.delivery: %v", err)
	}
	return d
}

// driverEvents is what the console's `listAuthAttempts` shows for this venue.
//
// Read through the adapter port rather than over HTTP because the guest listener this
// venue mounts does not carry the console's routes — and the port is the code the
// console's handler calls, one function away.
func (v *venue) driverEvents(ctx context.Context) []gen.AuthAttempt {
	v.t.Helper()
	source := gen.Driver
	page, err := v.adapter.AuthAttempts(ctx, api.AuthAttemptQuery{
		SiteID: v.site, Source: &source, Window: api.Window{Limit: 50},
	})
	if err != nil {
		v.t.Fatalf("listing auth attempts: %v", err)
	}
	return page.Items
}

// status is what the guest's connected page renders.
func (v *venue) status(t *testing.T) gen.PortalStatus {
	t.Helper()
	resp := v.api(http.MethodGet, "/portal/status", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /portal/status answered %d, want 200", resp.StatusCode)
	}
	var out gen.PortalStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decoding the status: %v", err)
	}
	return out
}

// redeem drives the voucher through the front door and returns the grant it made.
func (v *venue) redeem(ctx context.Context) string {
	v.t.Helper()
	v.arrive(v.guestMAC)
	resp := v.api(http.MethodPost, "/portal/voucher", url.Values{"code": {v.code}, "lang": {"en"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		v.t.Fatalf("redeeming answered %d, want 303", resp.StatusCode)
	}
	grants := v.grants(ctx)
	if len(grants) != 1 {
		v.t.Fatalf("%d grants exist after one redemption, want 1", len(grants))
	}
	return grants[0].ID
}

// ── the vertical slice ───────────────────────────────────────────────────────

// TestAFailedAuthorizationReachesTheOperatorAndTheGuest drives the whole failure.
//
// The three assertions are three different people: the row an operator finds when a
// guest is standing in front of them, the column that stops the portal claiming
// success, and the page the guest is actually reading.
func TestAFailedAuthorizationReachesTheOperatorAndTheGuest(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)
	// A controller that **answered** and said no. `MockAPIRefusal` produces the
	// `*nas.APIError` the product branches on, so this exercises the answered half
	// of `worthRetrying` rather than the transport half.
	v.driver.WithFaults(func(f *nas.MockFaults) {
		f.Authorize = nas.MockAPIRefusal(http.StatusUnauthorized, "api key revoked")
	})

	grantID := v.redeem(ctx)
	v.awaitRecording(ctx, grantID)

	t.Run("the operator finds it", func(t *testing.T) {
		events := v.driverEvents(ctx)
		if len(events) != 1 {
			t.Fatalf("listAuthAttempts?source=driver returned %d rows, want 1 — "+
				"this is the only record of the failure anywhere", len(events))
		}
		e := events[0]
		if e.Source != gen.Driver {
			t.Errorf("source is %q, want driver", e.Source)
		}
		if e.Kind != gen.AuthEventKindAuthorizeFailed {
			t.Errorf("kind is %q, want authorize-failed", e.Kind)
		}
		if e.Outcome != gen.Rejected {
			t.Errorf("outcome is %q, want rejected", e.Outcome)
		}
		if e.SiteId != v.site {
			t.Errorf("siteId is %q, want %q", e.SiteId, v.site)
		}
		if e.CallingStationId == nil || *e.CallingStationId != v.guestMAC {
			t.Errorf("callingStationId is %v, want %q — the MAC is the only identifier "+
				"an operator has when a guest is standing in front of them", e.CallingStationId, v.guestMAC)
		}
		if e.VoucherId == nil || *e.VoucherId != v.voucherID {
			t.Errorf("voucherId is %v, want %q", e.VoucherId, v.voucherID)
		}
		if e.Params == nil {
			t.Fatal("no params: an authorize-failed row with no failure kind tells an operator " +
				"only that something went wrong")
		}
		p := *e.Params
		if p["failure"] != "refused" {
			t.Errorf("params.failure is %v, want refused — a controller that answered has an "+
				"opinion, and that is a different person's job from one that is down", p["failure"])
		}
		if fmt.Sprint(p["status"]) != "401" {
			t.Errorf("params.status is %v, want 401 — an expired or revoked API key", p["status"])
		}
		if _, ok := p["attempts"]; ok {
			t.Errorf("params carries attempts %v for a controller that answered 401; "+
				"a 401 is an opinion and repeating the question repeats the answer", p["attempts"])
		}
	})

	t.Run("no field carries the controller's own text", func(t *testing.T) {
		// `nas.APIError` embeds the URL it was reached at and up to 512 bytes of
		// response body. A URL can carry an API key in its query string and a body
		// is text somebody else wrote; neither belongs in a row read months later.
		// The schema has no column for either, and this is the second gate.
		raw, err := json.Marshal(v.driverEvents(ctx))
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"mock-controller.invalid", "api key revoked", "https://"} {
			if strings.Contains(string(raw), forbidden) {
				t.Errorf("the rendered event contains %q, which came from the controller", forbidden)
			}
		}
	})

	t.Run("the grant says the same thing", func(t *testing.T) {
		d := v.delivery(ctx, grantID)
		if d == nil || *d != "failed" {
			t.Fatalf("portal_grants.delivery is %v, want failed", d)
		}
	})

	t.Run("the guest is not told they are connected", func(t *testing.T) {
		st := v.status(t)
		if st.State != gen.PortalStatusStateNotOnNetwork {
			t.Fatalf("PortalStatus.state is %q, want %q — %q here is a guest reading "+
				"*You're connected* in a café with no internet",
				st.State, gen.PortalStatusStateNotOnNetwork, gen.PortalStatusStateConnected)
		}
	})
}

// ── the two failures are not one ─────────────────────────────────────────────

// TestAControllerThatDidNotAnswerIsRecordedAndRetried.
//
// `unreachable` and `refused` need different people to do different things, and the
// retry is scoped to the one where asking again can change the answer: a controller
// restarting refuses TCP for a second or two, and the guest cannot recover from that
// on their own because their voucher is already spent.
//
// **The attempt count is asserted because a retry nobody can see is a second way to
// be wrong.** An operator reading a bare failure has no way to tell a controller that
// was asked once from one that was asked three times.
func TestAControllerThatDidNotAnswerIsRecordedAndRetried(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)
	// A plain error rather than an `*APIError`: nothing answered. This is the case
	// the retry exists for, and it is the branch `MockAPIRefusal` cannot reach.
	v.driver.WithFaults(func(f *nas.MockFaults) {
		f.Authorize = errors.New("mock: dial tcp: connection refused")
	})
	// Counted with a hook rather than with `AuthorizeCount`, which records only the
	// calls that **succeeded** — a driver that fails every time has an empty log, so
	// the obvious assertion would read zero for three real calls and be satisfied by
	// a product that never retried at all.
	var asked atomic.Int64
	v.driver.WithHooks(func(h *nas.MockHooks) {
		h.OnAuthorize = func() { asked.Add(1) }
	})

	grantID := v.redeem(ctx)
	v.awaitRecording(ctx, grantID)

	events := v.driverEvents(ctx)
	if len(events) != 1 {
		t.Fatalf("listAuthAttempts?source=driver returned %d rows, want 1", len(events))
	}
	e := events[0]
	if e.Kind != gen.AuthEventKindAuthorizeFailed {
		t.Errorf("kind is %q, want authorize-failed", e.Kind)
	}
	if e.Params == nil {
		t.Fatal("no params on a failed authorization")
	}
	p := *e.Params
	if p["failure"] != "unreachable" {
		t.Errorf("params.failure is %v, want unreachable — nothing answered, so this is the "+
			"venue's outage and not a statement about the guest's phone", p["failure"])
	}
	if _, ok := p["status"]; ok {
		t.Errorf("params carries status %v for a controller that never answered; the number "+
			"would have been invented from a transport error", p["status"])
	}
	if fmt.Sprint(p["attempts"]) != "3" {
		t.Errorf("params.attempts is %v, want 3 — the controller was asked three times and "+
			"an operator has no other way to know that", p["attempts"])
	}
	if calls := asked.Load(); calls != 3 {
		t.Errorf("the driver was called %d times, want 3; the recorded count must be the "+
			"count that happened", calls)
	}
}

// TestASuccessfulAuthorizationIsRecordedToo.
//
// The stream is not a failure log. Without this half, `delivery` would only ever be
// written on the unhappy path and NULL would mean two things forever — and an
// operator could not tell *the network was opened* from *nothing has told us*.
func TestASuccessfulAuthorizationIsRecordedToo(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)

	grantID := v.redeem(ctx)
	v.awaitRecording(ctx, grantID)

	if d := v.delivery(ctx, grantID); d == nil || *d != "open" {
		t.Fatalf("portal_grants.delivery is %v, want open", d)
	}
	events := v.driverEvents(ctx)
	if len(events) != 1 {
		t.Fatalf("listAuthAttempts?source=driver returned %d rows, want 1", len(events))
	}
	if events[0].Outcome != gen.Accepted {
		t.Errorf("outcome is %q, want accepted", events[0].Outcome)
	}
	if events[0].Kind != gen.AuthEventKindGrantCreated {
		t.Errorf("kind is %q, want grant-created", events[0].Kind)
	}
	if st := v.status(t); st.State != gen.PortalStatusStateConnected {
		t.Errorf("PortalStatus.state is %q, want connected", st.State)
	}
}

// ── the disagreement, and the sabotage that proves it can be seen ────────────

// readersDisagreement compares the two readers of one fact and describes the
// disagreement, or returns "" when they agree.
//
// It returns a sentence rather than failing, so that a test can require it to be
// **empty on the real path and non-empty after a deliberate split.** A checker that
// could only fail could not be shown to work.
func (v *venue) readersDisagreement(ctx context.Context, grantID string) string {
	v.t.Helper()

	var console string
	for _, e := range v.driverEvents(ctx) {
		// The events are newest first, so the first match is the current answer.
		if e.CallingStationId != nil && *e.CallingStationId == v.guestMAC {
			console = string(e.Kind)
			break
		}
	}
	guest := "connected"
	if d := v.delivery(ctx, grantID); d != nil && *d == "failed" {
		guest = "not-on-network"
	}

	switch {
	case console == "" && v.delivery(ctx, grantID) != nil:
		return fmt.Sprintf("the guest's page reads %q for grant %s and the console has no "+
			"event at all — the operator has nothing to look at when the guest reaches the counter",
			guest, grantID)
	case console == string(gen.AuthEventKindAuthorizeFailed) && guest != "not-on-network":
		return fmt.Sprintf("the console shows %q for grant %s while the guest's page reads %q",
			console, grantID, guest)
	case console == string(gen.AuthEventKindGrantCreated) && guest != "connected":
		return fmt.Sprintf("the console shows %q for grant %s while the guest's page reads %q",
			console, grantID, guest)
	default:
		return ""
	}
}

// TestTheTwoReadersCannotDisagree.
//
// **One port method writes both, and a SQL implementation does them in one
// transaction**, because an implementation that could write one without the other
// could show the console `authorize-failed` beside a portal showing a green tick —
// which `internal/portal/CONTRACT.md` invariant 10 judges worse than the silence it
// replaces.
//
// The first subtest is the real path and must agree. The other two **split the write
// by hand, in both directions**, and require the checker to say so — because a
// consistency check that has never been red only looks like a check. The split is
// re-injected on every run rather than recorded once.
func TestTheTwoReadersCannotDisagree(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)
	v.driver.WithFaults(func(f *nas.MockFaults) {
		f.Authorize = nas.MockAPIRefusal(http.StatusForbidden, "site not permitted")
	})

	grantID := v.redeem(ctx)
	v.awaitRecording(ctx, grantID)

	t.Run("the real path agrees", func(t *testing.T) {
		if why := v.readersDisagreement(ctx, grantID); why != "" {
			t.Fatalf("the two readers disagree after an ordinary recording: %s", why)
		}
	})

	t.Run("the operator's half without the guest's is seen", func(t *testing.T) {
		// Exactly what a split implementation produces when the event write lands
		// and the grant update does not.
		if _, err := v.owner.Exec(ctx,
			`UPDATE portal_grants SET delivery = 'open' WHERE id = $1`, grantID); err != nil {
			t.Fatalf("splitting the write: %v", err)
		}
		why := v.readersDisagreement(ctx, grantID)
		if why == "" {
			t.Fatal("the console says authorize-failed and the guest's page says connected, " +
				"and the check reported nothing — it is not measuring the disagreement")
		}
		if !containsAll(why, "authorize-failed", "connected", grantID) {
			t.Errorf("the disagreement was reported as %q, which does not name both readers", why)
		}
	})

	t.Run("the guest's half without the operator's is seen", func(t *testing.T) {
		// The other direction: the grant is marked and nothing records why. An
		// operator has a guest at the counter and no row to look at.
		if _, err := v.owner.Exec(ctx,
			`DELETE FROM authorization_events WHERE grant_id = $1`, grantID); err != nil {
			t.Fatalf("splitting the write: %v", err)
		}
		if _, err := v.owner.Exec(ctx,
			`UPDATE portal_grants SET delivery = 'failed' WHERE id = $1`, grantID); err != nil {
			t.Fatalf("splitting the write: %v", err)
		}
		why := v.readersDisagreement(ctx, grantID)
		if why == "" {
			t.Fatal("the guest's page reads not-on-network and the console has no event, " +
				"and the check reported nothing")
		}
		if !containsAll(why, "no event", grantID) {
			t.Errorf("the disagreement was reported as %q, which does not name the missing half", why)
		}
	})
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if !strings.Contains(s, p) {
			return false
		}
	}
	return true
}

// ── the streams stay apart ───────────────────────────────────────────────────

// TestTheTwoAuthenticationStreamsAreUnionedAndSeparable.
//
// `listAuthAttempts` answers one operation over two tables. Three things have to hold
// at once and each is a different way to be wrong: an unfiltered query shows both, a
// filtered one shows exactly its own stream, and the ids do not collide — the two
// tables have independent sequences, so `radpostauth` row 7 and
// `authorization_events` row 7 would otherwise be one identifier for two events, and
// a console keying rows on it would render one and drop the other.
func TestTheTwoAuthenticationStreamsAreUnionedAndSeparable(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)
	v.driver.WithFaults(func(f *nas.MockFaults) {
		f.Authorize = nas.MockAPIRefusal(http.StatusNotFound, "no such site")
	})

	// A RADIUS attempt for the same venue, written as the schema owner because
	// `foyer_app` holds SELECT on radpostauth and nothing else.
	if _, err := v.owner.Exec(ctx, `
		INSERT INTO radpostauth (username, reply, callingstationid, calledstationid, authdate)
		VALUES ($1, 'Access-Reject', $2, $3, now())`,
		uniq("eap"), v.guestMAC, v.apMAC+":Venue Guest"); err != nil {
		t.Fatalf("seeding a post-auth row: %v", err)
	}

	grantID := v.redeem(ctx)
	v.awaitRecording(ctx, grantID)

	all, err := v.adapter.AuthAttempts(ctx, api.AuthAttemptQuery{
		SiteID: v.site, Window: api.Window{Limit: 50},
	})
	if err != nil {
		t.Fatalf("listing every attempt: %v", err)
	}
	seen := map[gen.AuthSource]int{}
	ids := map[string]int{}
	for _, a := range all.Items {
		seen[a.Source]++
		ids[a.Id]++
	}
	if seen[gen.Radius] == 0 {
		t.Error("the unfiltered listing has no radius rows")
	}
	if seen[gen.Driver] == 0 {
		t.Error("the unfiltered listing has no driver rows — the union is the whole point, " +
			"and an operator looking at the default view is the one who needs it")
	}
	for id, n := range ids {
		if n > 1 {
			t.Errorf("id %q appears %d times; the two streams share one id namespace", id, n)
		}
	}
	// **The duplicate check above is not the assertion, because it only fires by
	// luck.** The two sequences are independent and this container is shared, so
	// whether `radpostauth` row 7 and `authorization_events` row 7 both land in one
	// page depends on how many other tests have run — an id space that has been
	// collapsed would go unnoticed on most runs. What is asserted is the property:
	// every id names the stream it came from.
	for _, a := range all.Items {
		if !strings.HasPrefix(a.Id, string(a.Source)+"-") {
			t.Errorf("id %q does not name its stream %q; the two tables have independent "+
				"sequences and the contract has one id space", a.Id, a.Source)
		}
	}

	radius := gen.Radius
	only, err := v.adapter.AuthAttempts(ctx, api.AuthAttemptQuery{
		SiteID: v.site, Source: &radius, Window: api.Window{Limit: 50},
	})
	if err != nil {
		t.Fatalf("listing the radius stream: %v", err)
	}
	for _, a := range only.Items {
		if a.Source != gen.Radius {
			t.Errorf("source=radius returned a %q row", a.Source)
		}
	}

	// `portal` still has no writer, and answering it with another layer's rows
	// would be the API claiming an event came from a place that never wrote one.
	portalSource := gen.Portal
	none, err := v.adapter.AuthAttempts(ctx, api.AuthAttemptQuery{
		SiteID: v.site, Source: &portalSource, Window: api.Window{Limit: 50},
	})
	if err != nil {
		t.Fatalf("listing the portal stream: %v", err)
	}
	if len(none.Items) != 0 {
		t.Errorf("source=portal returned %d rows; nothing writes that stream", len(none.Items))
	}
}

// TestTheDriverStreamIsScopedToItsSite.
//
// The same rule the RADIUS half carries, and it needs its own assertion: this table
// has a `site_id` column rather than a `Called-Station-Id` join, so it is a different
// mechanism answering the same question. `listAuthAttempts?siteId=` is the screen an
// operator opens with a guest in front of them, and it carries usernames and MACs.
func TestTheDriverStreamIsScopedToItsSite(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)
	v.driver.WithFaults(func(f *nas.MockFaults) {
		f.Authorize = nas.MockAPIRefusal(http.StatusUnauthorized, "revoked")
	})

	grantID := v.redeem(ctx)
	v.awaitRecording(ctx, grantID)

	other := uniq("site")
	if err := v.st.Sites().Create(ctx, &store.Site{
		ID: other, Name: "The other venue", Environment: "lab",
	}); err != nil {
		t.Fatalf("creating a second site: %v", err)
	}
	source := gen.Driver
	page, err := v.adapter.AuthAttempts(ctx, api.AuthAttemptQuery{
		SiteID: other, Source: &source, Window: api.Window{Limit: 50},
	})
	if err != nil {
		t.Fatalf("listing the other site's driver stream: %v", err)
	}
	for _, a := range page.Items {
		if a.CallingStationId != nil && *a.CallingStationId == v.guestMAC {
			t.Fatalf("a second site's listing carries this venue's guest MAC %q for grant %s",
				v.guestMAC, grantID)
		}
	}
}
