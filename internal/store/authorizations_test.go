package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/store"
)

// The two readers of one fact, and the database's part in keeping them together.
//
// `internal/portal/CONTRACT.md` invariant 10: an implementation that could write the
// operator's `authorize-failed` event without the grant's `delivery` could show the
// console a failure beside a portal showing a green tick, **which is worse than the
// silence it replaces.** The port therefore has one method and the SQL is one
// transaction; these are the tests that make that a property rather than a comment.

// seedGrant writes one grant for a site, as the portal would.
//
// Click-through, because it is the method whose CHECK constraints require neither a
// voucher nor an account: these tests are about what happens *after* a grant exists,
// and seeding a voucher batch to get one would put a second subject in every failure.
func seedGrant(t *testing.T, st *store.Store, siteID string) *store.PortalGrant {
	t.Helper()
	g := &store.PortalGrant{
		ID:        uniq("grant"),
		SiteID:    siteID,
		ClientMAC: "02:44:11:22:33:44",
		Method:    "click-through",
		Plan:      store.Plan{EnforcedBy: "unenforced"},
		GrantedAt: time.Now(),
	}
	fatalf(t, st.Grants().CreateGrant(context.Background(), g), "creating a grant")
	return g
}

func deliveryOf(t *testing.T, st *store.Store, grantID string) *string {
	t.Helper()
	g, err := st.Grants().Grant(context.Background(), grantID)
	fatalf(t, err, "reading the grant back")
	return g.Delivery
}

func eventsFor(t *testing.T, st *store.Store, grantID string) []store.AuthorizationEvent {
	t.Helper()
	page, err := st.Accounting().ListAuthAttempts(context.Background(), store.AuthAttemptFilter{
		Source:      "driver",
		ListOptions: store.ListOptions{Limit: 50},
	})
	fatalf(t, err, "listing the driver stream")
	var out []store.AuthorizationEvent
	for _, a := range page.Items {
		if a.GrantID != nil && *a.GrantID == grantID {
			out = append(out, store.AuthorizationEvent{
				ID: a.ID, SiteID: derefOr(a.SiteID), GrantID: *a.GrantID,
				ClientMAC: derefOr(a.CallingStationID), Delivery: deliveryFromOutcome(a.Outcome),
				Failure: a.Failure, Status: a.Status, Attempts: derefInt(a.Attempts),
			})
		}
	}
	return out
}

func derefOr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func deliveryFromOutcome(outcome string) string {
	if outcome == "accepted" {
		return "open"
	}
	return "failed"
}

// TestRecordingAnAuthorizationWritesBothReaders is the positive half.
//
// It also proves the sequence grant, which is not a formality: `docs/rules/database.md` records
// the case where granting INSERT on a bigserial table left a suite green because
// `nextval()` needed a USAGE the role did not have. `foyer_app` performs this INSERT,
// so a missing `GRANT USAGE ON SEQUENCE` makes this test red and names the sequence.
func TestRecordingAnAuthorizationWritesBothReaders(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	g := seedGrant(t, st, site.ID)

	if d := deliveryOf(t, st, g.ID); d != nil {
		t.Fatalf("a fresh grant already has delivery %q; NULL is the zero value and reads as connected", *d)
	}

	e := &store.AuthorizationEvent{
		SiteID: site.ID, GrantID: g.ID, ClientMAC: g.ClientMAC,
		Method: "click-through", At: time.Now(),
		Delivery: "failed", Attempts: 3,
		Failure: ptrTo("unreachable"),
	}
	fatalf(t, st.Grants().RecordAuthorization(ctx, e), "recording the authorization")

	if e.ID == 0 {
		t.Error("the event was written with no id; the sequence did not run")
	}
	d := deliveryOf(t, st, g.ID)
	if d == nil || *d != "failed" {
		t.Fatalf("the grant's delivery is %v, want failed — the guest's half was not written", d)
	}
	events := eventsFor(t, st, g.ID)
	if len(events) != 1 {
		t.Fatalf("the driver stream holds %d events for this grant, want 1", len(events))
	}
	if events[0].Delivery != "failed" || derefOr(events[0].Failure) != "unreachable" {
		t.Errorf("the event says delivery %q failure %v, want failed/unreachable",
			events[0].Delivery, events[0].Failure)
	}
	if events[0].Attempts != 3 {
		t.Errorf("attempts is %d, want 3 — a retry nobody can see is a second way to be wrong",
			events[0].Attempts)
	}
}

// TestAnAuthorizationForAMissingGrantWritesNothing is the half that makes the
// transaction load-bearing rather than decorative.
//
// A grant that does not exist cannot have its `delivery` written, so the event must
// not be written either: an operator-visible `authorize-failed` with no guest-visible
// half is exactly the disagreement invariant 10 forbids, in the direction nobody
// thinks to check.
//
// **The refusal must name the grant.** The foreign key would also refuse this INSERT,
// with `23503` and a constraint name — a caller could not tell "there is no such
// grant" from "the event table is misconfigured", and the `UPDATE` going first is
// what makes the answer `ErrNotFound`.
func TestAnAuthorizationForAMissingGrantWritesNothing(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	owner := ownerPool(t)

	var before int
	fatalf(t, owner.QueryRow(ctx, `SELECT count(*) FROM authorization_events`).Scan(&before),
		"counting events before")

	missing := uniq("grant")
	err := st.Grants().RecordAuthorization(ctx, &store.AuthorizationEvent{
		SiteID: site.ID, GrantID: missing, ClientMAC: "02:44:11:22:33:99",
		Method: "click-through", At: time.Now(), Delivery: "failed", Attempts: 1,
		Failure: ptrTo("refused"), Status: ptrTo(401),
	})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("recording against a missing grant answered %v, want ErrNotFound", err)
	}

	var after int
	fatalf(t, owner.QueryRow(ctx, `SELECT count(*) FROM authorization_events`).Scan(&after),
		"counting events after")
	if after != before {
		t.Fatalf("%d event row(s) survived a refused recording; the transaction did not roll back",
			after-before)
	}
}

// TestAnAuthorizationEventCannotBeRewritten.
//
// An event is what happened. `foyer_app` may add one and read them, and may do
// nothing else: correcting a record of a failure after the fact is not a thing the
// product offers, and retention is a sweep the schema owner runs.
//
// Asserted with `requireRefused`, which requires the refusal to name **this table**.
// `docs/rules/database.md`: a negative test whose subject has more than one way to fail is a
// test of whichever guard fires first.
func TestAnAuthorizationEventCannotBeRewritten(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	g := seedGrant(t, st, site.ID)

	e := &store.AuthorizationEvent{
		SiteID: site.ID, GrantID: g.ID, ClientMAC: g.ClientMAC,
		Method: "click-through", At: time.Now(), Delivery: "open", Attempts: 1,
	}
	fatalf(t, st.Grants().RecordAuthorization(ctx, e), "recording the authorization")

	pool := st.Pool()

	t.Run("refuses UPDATE", func(t *testing.T) {
		_, err := pool.Exec(ctx,
			`UPDATE authorization_events SET delivery = 'failed' WHERE id = $1`, e.ID)
		requireRefused(t, err, "UPDATE authorization_events", "authorization_events")
	})

	t.Run("refuses DELETE", func(t *testing.T) {
		_, err := pool.Exec(ctx, `DELETE FROM authorization_events WHERE id = $1`, e.ID)
		requireRefused(t, err, "DELETE FROM authorization_events", "authorization_events")
	})

	// The other half, because a role with no privilege at all would satisfy both
	// assertions above while making the whole feature unreachable.
	t.Run("still reads", func(t *testing.T) {
		var n int
		fatalf(t, pool.QueryRow(ctx,
			`SELECT count(*) FROM authorization_events WHERE id = $1`, e.ID).Scan(&n),
			"reading the event back as the application role")
		if n != 1 {
			t.Fatalf("the application role read %d rows for its own event, want 1", n)
		}
	})
}

// TestTheDatabaseRefusesAnUnexplainedFailure.
//
// Three constraints, each of which stops a row an operator cannot act on. They are in
// the schema rather than in Go because the value lands there and a validator that
// re-implements a rule the database could own is a second source of truth that drifts
// silently (`docs/rules/database.md`).
func TestTheDatabaseRefusesAnUnexplainedFailure(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	site := seedSite(t, st)
	g := seedGrant(t, st, site.ID)

	cases := []struct {
		name  string
		event store.AuthorizationEvent
		why   string
	}{
		{
			name: "a failure with no failure kind",
			event: store.AuthorizationEvent{
				Delivery: "failed", Attempts: 1,
			},
			why: "an operator reading it would know only that something went wrong",
		},
		{
			name: "an open delivery carrying a failure kind",
			event: store.AuthorizationEvent{
				Delivery: "open", Attempts: 1, Failure: ptrTo("refused"),
			},
			why: "the console would have to pick a side of a contradiction",
		},
		{
			name: "a status beside an unreachable controller",
			event: store.AuthorizationEvent{
				Delivery: "failed", Attempts: 1,
				Failure: ptrTo("unreachable"), Status: ptrTo(500),
			},
			why: "nothing answered, so the number was invented from a transport error",
		},
		{
			name: "an attempt count of zero",
			event: store.AuthorizationEvent{
				Delivery: "open", Attempts: 0,
			},
			why: "this row exists because a call returned, so it was asked at least once",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := tc.event
			e.SiteID, e.GrantID, e.ClientMAC, e.Method, e.At = site.ID, g.ID, g.ClientMAC, "click-through", time.Now()
			if err := st.Grants().RecordAuthorization(ctx, &e); err == nil {
				t.Fatalf("the database accepted %s — %s", tc.name, tc.why)
			}
		})
	}
}

// TestAuthorizationEventsAreTenantIsolated.
//
// Every table in this schema carries a row-level policy except `nas`, and the reason
// `nas` is exempt does not apply here: nothing outside Foyer reads this table.
func TestAuthorizationEventsAreTenantIsolated(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	owner := ownerPool(t)
	_, err := owner.Exec(ctx, `
		INSERT INTO tenants (id, slug, name) VALUES ($1, 'second', 'Second')
		ON CONFLICT (id) DO NOTHING`, secondTenant)
	fatalf(t, err, "creating a second tenant")

	first := storeAsRole(t, store.DefaultAppRole, store.DefaultTenantID)
	second := storeAsRole(t, store.DefaultAppRole, secondTenant)

	site := seedSite(t, first)
	g := seedGrant(t, first, site.ID)
	e := &store.AuthorizationEvent{
		SiteID: site.ID, GrantID: g.ID, ClientMAC: g.ClientMAC,
		Method: "click-through", At: time.Now(), Delivery: "failed", Attempts: 1,
		Failure: ptrTo("refused"), Status: ptrTo(403),
	}
	fatalf(t, first.Grants().RecordAuthorization(ctx, e), "recording in tenant A")

	page, err := second.Accounting().ListAuthAttempts(ctx, store.AuthAttemptFilter{
		Source: "driver", ListOptions: store.ListOptions{Limit: 100},
	})
	fatalf(t, err, "listing the driver stream as tenant B")
	for _, a := range page.Items {
		if a.GrantID != nil && *a.GrantID == g.ID {
			t.Fatalf("tenant B can read tenant A's authorization event for grant %s", g.ID)
		}
	}
}

// TestTheUnionPagesThroughBothStreamsExactlyOnce.
//
// A union with one cursor is where paging goes wrong quietly, and the two ways it
// does look nothing alike: a cursor that does not advance makes
// `internal/apistore`'s `fill` loop until the process is killed, and a cursor that
// advances past rows drops the events an operator was looking for.
//
// So this walks the whole set two rows at a time and requires **every row exactly
// once, newest first**. The page size is deliberately smaller than either stream, so
// at least one page has to resume in the middle of one of them.
func TestTheUnionPagesThroughBothStreamsExactlyOnce(t *testing.T) {
	st := appStore(t)
	ctx := context.Background()
	owner := ownerPool(t)
	site := seedSite(t, st)

	// A device for the site scope to resolve `Called-Station-Id` through.
	seedDeviceWorld(t, st, site, []string{"94:2a:6f:e2:07:01"}, []string{"10.89.0.11"})

	want := map[string]bool{}
	for i := 0; i < 3; i++ {
		user := uniq("radius")
		_, err := owner.Exec(ctx, `
			INSERT INTO radpostauth (username, reply, callingstationid, calledstationid, authdate)
			VALUES ($1, 'Access-Reject', '02:77:00:00:00:01', $2, now())`,
			user, "94-2A-6F-E2-07-01:Guest")
		fatalf(t, err, "seeding a post-auth row")
		want["radius:"+user] = false
	}
	for i := 0; i < 3; i++ {
		g := seedGrant(t, st, site.ID)
		e := &store.AuthorizationEvent{
			SiteID: site.ID, GrantID: g.ID, ClientMAC: g.ClientMAC,
			Method: "click-through", At: time.Now(), Delivery: "failed", Attempts: 1,
			Failure: ptrTo("unreachable"),
			// The username is what this test keys on, and it is a field a driver row
			// is allowed to carry: the identity offered, never the credential.
			Username: ptrTo(uniq("driver")),
		}
		fatalf(t, st.Grants().RecordAuthorization(ctx, e), "recording an authorization")
		want["driver:"+*e.Username] = false
	}

	cursor := ""
	var last time.Time
	for page := 0; ; page++ {
		if page > 20 {
			t.Fatal("the cursor did not reach the end in twenty pages of two; it is not " +
				"advancing, which inside `fill` is a loop that never returns")
		}
		got, err := st.Accounting().ListAuthAttempts(ctx, store.AuthAttemptFilter{
			ListOptions: store.ListOptions{SiteID: site.ID, Cursor: cursor, Limit: 2},
		})
		fatalf(t, err, "reading page %d", page)
		for _, a := range got.Items {
			if !last.IsZero() && a.At.After(last) {
				t.Errorf("row %s/%d is newer than the row before it; the union is not sorted",
					a.Source, a.ID)
			}
			last = a.At
			key := a.Source + ":" + a.Username
			seen, tracked := want[key]
			if !tracked {
				continue // another test's rows: the container and its schema are shared
			}
			if seen {
				t.Errorf("%s was returned twice; the cursor went backwards", key)
			}
			want[key] = true
		}
		if got.NextCursor == nil {
			break
		}
		cursor = *got.NextCursor
	}

	for key, seen := range want {
		if !seen {
			t.Errorf("%s never appeared; the cursor skipped it", key)
		}
	}
}

func ptrTo[T any](v T) *T { return &v }
