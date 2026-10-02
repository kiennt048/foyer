package apistore_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// Site scoping is a security property, not a filter.
//
// **A scoping bug is invisible in a single-site fixture world**, which is the world
// almost every test here would otherwise build: one venue, one set of rows, and
// every query trivially correct. So this file seeds two sites and asserts the
// negative on every site-scoped surface — an operator working in one venue must not
// be able to read another's sessions, vouchers, accounts, devices or authentication
// attempts through any operation.
//
// The `radpostauth` case is the one that was actually broken by construction and is
// the reason this file exists in this shape: that table carries **no NAS address and
// no tenant**, so `AuthAttemptFilter` takes no site, and the obvious adapter — pass
// the query through, return what comes back — answers a site-scoped request with
// every site's authentication attempts. Usernames and MACs, across venues, on the
// screen an operator opens when a guest is standing in front of them.

func TestASiteScopeIsNotAFilter(t *testing.T) {
	w := newWorld(t)
	w.signIn()

	t.Run("sessions", func(t *testing.T) {
		var page struct {
			Items []gen.Session `json:"items"`
		}
		w.getJSON("/sessions?siteId="+w.siteA, &page)
		for _, s := range page.Items {
			if s.SiteId != w.siteA {
				t.Fatalf("session %s belongs to %s and was returned under a scope for %s", s.Id, s.SiteId, w.siteA)
			}
		}
		if len(page.Items) == 0 {
			t.Fatal("no sessions at all, so this assertion is vacuous")
		}
	})

	t.Run("vouchers", func(t *testing.T) {
		var page struct {
			Items []gen.Voucher `json:"items"`
		}
		w.getJSON("/vouchers?siteId="+w.siteB, &page)
		if len(page.Items) != 2 {
			t.Fatalf("site B has two vouchers, got %d — a wider scope or a narrower one", len(page.Items))
		}
		for _, v := range page.Items {
			if v.BatchId == w.batchA {
				t.Fatalf("voucher %s is in site A's batch and was returned under a scope for site B", v.Id)
			}
		}
	})

	t.Run("nas", func(t *testing.T) {
		var page struct {
			Items []gen.Nas `json:"items"`
		}
		w.getJSON("/nas?siteId="+w.siteB, &page)
		if len(page.Items) != 1 {
			t.Fatalf("site B has one device, got %d", len(page.Items))
		}
		if page.Items[0].Id != w.deviceUnreachable {
			t.Fatalf("site B's device is %s, got %s", w.deviceUnreachable, page.Items[0].Id)
		}
	})

	t.Run("accounts", func(t *testing.T) {
		var page struct {
			Items []gen.Account `json:"items"`
		}
		w.getJSON("/accounts?siteId="+w.siteA, &page)
		if len(page.Items) != 1 {
			t.Fatalf("site A has one account, got %d", len(page.Items))
		}
		if page.Items[0].SiteId != w.siteA {
			t.Fatalf("an account from %s was returned under a scope for %s", page.Items[0].SiteId, w.siteA)
		}
	})

	// The one that has no column to scope on.
	t.Run("auth attempts", func(t *testing.T) {
		var page struct {
			Items       []gen.AuthAttempt   `json:"items"`
			MacCoverage gen.MacCoverage     `json:"macCoverage"`
			BlindSpots  []gen.AuthBlindSpot `json:"blindSpots"`
		}
		w.getJSON("/auth-attempts?siteId="+w.siteA, &page)
		if len(page.Items) != 2 {
			t.Fatalf("site A recorded two attempts, got %d: %+v", len(page.Items), page.Items)
		}
		for _, at := range page.Items {
			if at.SiteId != w.siteA {
				t.Fatalf("attempt %s resolved to %s and was returned under a scope for %s", at.Id, at.SiteId, w.siteA)
			}
			if at.Username != nil && *at.Username == "warehouse-guest" {
				t.Fatal("site B's guest reached a site-A-scoped list of authentication attempts")
			}
			if at.NasId == nil {
				t.Errorf("attempt %s did not resolve a device, so the operator cannot tell where it happened", at.Id)
			}
			// The only two kinds a `radpostauth` row can honestly claim. There is
			// deliberately no `radius-reject-because-*`: inventing a reason would
			// be the API asserting something it did not observe.
			if at.Kind != gen.AuthEventKindRadiusAccept && at.Kind != gen.AuthEventKindRadiusReject {
				t.Errorf("kind = %q, and a post-auth row records only the reply", at.Kind)
			}
			if at.Source != gen.Radius {
				t.Errorf("source = %q for a row written by the RADIUS layer", at.Source)
			}
		}

		// The envelope's two other members are the whole point of the operation:
		// an empty result means *we have no record*, and a console that renders
		// that as *nothing went wrong* is the failure it exists to prevent.
		if page.MacCoverage != gen.MacCoverageRecorded {
			t.Errorf("macCoverage = %q, want recorded — the seeded rows carry calling stations", page.MacCoverage)
		}
		if !containsFunc(page.BlindSpots, func(b gen.AuthBlindSpot) bool { return b.Kind == gen.UnknownRadiusClient }) {
			t.Error("no unknown-radius-client blind spot, but a radacct row has a NAS address in no clients.conf entry")
		}
	})
}

// TestTheAuthAttemptScopeCanFail re-injects the defect from the data on every run
// rather than trusting that it once failed.
//
// **The naive adapter is written out below**, in the test, and the same assertion
// the real one passes is run against it. If it passed too, the assertion above would
// be measuring nothing — a mechanism that looks like a check and is not.
//
// Two things are asserted: the naive path returns site B's attempt under a scope for
// site A (so the leak is real), and the real port does not (so the fix is real). A
// test that only asserted the second could be satisfied by a world with one site in
// it.
func TestTheAuthAttemptScopeCanFail(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	// The naive implementation: hand the query to the repository and return what
	// comes back, without passing the scope on. The filter has a `SiteID` member
	// now — it is what the port sets — and a caller that forgets it still gets
	// every site's rows, which is exactly the mistake this file exists to catch.
	naive, err := w.st.Accounting().ListAuthAttempts(ctx, store.AuthAttemptFilter{
		ListOptions: store.ListOptions{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	leaked := false
	for _, at := range naive.Items {
		if at.Username == "warehouse-guest" {
			leaked = true
		}
	}
	if !leaked {
		t.Fatal("the naive path did not leak site B's attempt, so the assertion in TestASiteScopeIsNotAFilter proves nothing")
	}

	// The real port, asked the same question with a scope.
	scoped, err := w.adapter.AuthAttempts(ctx, api.AuthAttemptQuery{
		SiteID: w.siteA,
		Window: api.Window{Limit: 50},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range scoped.Items {
		if at.Username != nil && *at.Username == "warehouse-guest" {
			t.Fatal("site B's attempt survived a site-A scope through the port")
		}
	}
	if len(scoped.Items) == 0 {
		t.Fatal("the scoped query returned nothing at all, which passes the negative assertion for the wrong reason")
	}
}

// TestUnresolvedAttemptsAreVisibleWithoutAScope is the other half, and it is the
// half a scoping fix usually breaks.
//
// A `radpostauth` row whose `Called-Station-Id` resolves to no access point is
// excluded from a site-scoped list — including it would be the leak — but it must
// still be reachable when no site is asked for. Those rows are the ones that matter
// most: an attempt from a device nothing recognises is the ordinary symptom of the
// most common real fault, and a console that dropped them would answer "why can't
// this guest get online" with silence.
func TestUnresolvedAttemptsAreVisibleWithoutAScope(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	if _, err := w.owner.Exec(ctx, `
		INSERT INTO radpostauth (username, reply, callingstationid, calledstationid)
		VALUES ('stranger', 'Access-Reject', '02:de:ad:be:ef:01', 'ff:ff:ff:ff:ff:ff:Unknown')`); err != nil {
		t.Fatal(err)
	}

	scoped, err := w.adapter.AuthAttempts(ctx, api.AuthAttemptQuery{SiteID: w.siteA, Window: api.Window{Limit: 50}})
	if err != nil {
		t.Fatal(err)
	}
	if containsFunc(scoped.Items, func(a gen.AuthAttempt) bool { return a.Username != nil && *a.Username == "stranger" }) {
		t.Fatal("an attempt on an unrecognised access point was returned under a site scope, which is the leak")
	}

	unscoped, err := w.adapter.AuthAttempts(ctx, api.AuthAttemptQuery{Window: api.Window{Limit: 50}})
	if err != nil {
		t.Fatal(err)
	}
	if !containsFunc(unscoped.Items, func(a gen.AuthAttempt) bool { return a.Username != nil && *a.Username == "stranger" }) {
		t.Fatal("an attempt on an unrecognised access point is invisible everywhere, so the fault it signals cannot be diagnosed")
	}
}

// TestAnAuditEntryCarriesTheVenueItsChangeBelongsTo — the one site-scoped surface
// `TestASiteScopeIsNotAFilter` above could not assert on.
//
// **`audit_log.site_id` and the index over it exist from migration 00003**, and
// `AuditRepo.List` filters on them: `WHERE ($1 = ” OR site_id = $1)`. That is worth
// nothing without a writer. A `Server.record` that built every entry without a site
// would leave the column uniformly NULL, the filter would answer every venue with
// **zero rows**, and `AuditEntry.siteId` — the member the console renders its venue
// column from — would be empty on every row a real backend served while `web/mock`
// filled it in. Two servers, one contract, opposite answers.
//
// The negative half is the one a careless repair breaks. Not every change belongs to
// a venue: an operator is created for the deployment, not for a café, and the honest
// value there is null. A fix that stamped the caller's current site onto every entry
// would satisfy the positive assertion and quietly file a deployment-wide change
// under whichever venue the operator happened to be looking at.
//
// Without a site on the entry, the first assertion reports `the site-A scope
// returned 0 entries` — the batch is created, the entry is written, and the
// venue's own filter cannot see it.
func TestAnAuditEntryCarriesTheVenueItsChangeBelongsTo(t *testing.T) {
	w := newWorld(t)
	w.signIn()
	ctx := context.Background()

	// Three changes, driven over HTTP rather than through the adapter, because
	// `Server.record` is the thing under test and only a handler calls it.
	//
	// Two site-scoped kinds rather than one: `voucher-batch-created` takes its venue
	// from the batch that was created and `garden-entry-added` from the path, so a
	// repair that reaches only one of the two ways a site arrives is still red.
	var batch gen.VoucherBatch
	if err := json.Unmarshal(w.do(http.MethodPost, "/voucher-batches", map[string]any{
		"siteId": w.siteA, "label": "Audit scope " + uniq("b"), "count": 1,
		"expiryMode": string(gen.FromFirstUse),
		"plan":       map[string]any{"durationMinutes": 60},
	}, http.StatusCreated), &batch); err != nil {
		t.Fatalf("creating the batch: %v", err)
	}
	var garden gen.GardenEntry
	if err := json.Unmarshal(w.do(http.MethodPost, "/sites/"+w.siteB+"/walled-garden", map[string]any{
		"kind": string(gen.GardenEntryKindFqdn), "value": uniq("scope") + ".example",
	}, http.StatusCreated), &garden); err != nil {
		t.Fatalf("adding the garden entry: %v", err)
	}
	var operator gen.Operator
	if err := json.Unmarshal(w.do(http.MethodPost, "/operators", map[string]any{
		"username": uniq("scoped-op"), "password": randomString() + "Aa1",
		"role": string(gen.Staff),
	}, http.StatusCreated), &operator); err != nil {
		t.Fatalf("creating the operator: %v", err)
	}

	// Vacuity guard, and it is not ceremony: every assertion below is a presence or
	// an absence in a filtered page, and an empty log satisfies all three absences
	// perfectly. `Query` narrows to this world's own actor — a username no other
	// world in this package uses — because the audit table is shared between worlds.
	// It is deliberately not the site filter, which is the thing under test.
	mine := w.audit(ctx, store.AuditFilter{ListOptions: store.ListOptions{Query: w.adminUser, Limit: 200}})
	for _, want := range []struct {
		kind    gen.AuditKind
		subject string
	}{
		{gen.AuditKindVoucherBatchCreated, batch.Id},
		{gen.AuditKindGardenEntryAdded, garden.Id},
		{gen.AuditKindOperatorCreated, operator.Id},
	} {
		if findEntry(mine, string(want.kind), want.subject) == nil {
			t.Fatalf("no %s entry for %s was written at all, so every assertion below is vacuous",
				want.kind, want.subject)
		}
	}

	// The filter that is unusable without a site on the entry.
	scoped := w.audit(ctx, store.AuditFilter{ListOptions: store.ListOptions{SiteID: w.siteA, Limit: 200}})
	if len(scoped) == 0 {
		t.Fatalf("the site-A scope returned 0 entries: %d changes are recorded for this operator "+
			"and not one of them names the venue it happened in", len(mine))
	}
	if findEntry(scoped, string(gen.AuditKindVoucherBatchCreated), batch.Id) == nil {
		t.Errorf("the batch created in site A is missing from site A's own audit scope")
	}
	if findEntry(scoped, string(gen.AuditKindGardenEntryAdded), garden.Id) != nil {
		t.Errorf("site B's garden entry was returned under a scope for site A")
	}
	// The half a stamp-the-current-site repair breaks.
	if findEntry(scoped, string(gen.AuditKindOperatorCreated), operator.Id) != nil {
		t.Errorf("creating an operator was filed under a venue — operators are the " +
			"deployment's, and a site here is a fact nobody recorded")
	}

	// And the deployment-wide entry stays reachable when no venue is asked for,
	// which is `TestUnresolvedAttemptsAreVisibleWithoutAScope`'s argument one table
	// along: a change belonging to no venue must not become a change nobody can
	// find. Read back through the contract, because `AuditEntry.siteId` is what the
	// console renders and `null` there is the member's documented value.
	var page struct {
		Items []gen.AuditEntry `json:"items"`
	}
	w.getJSON("/audit?limit=200", &page)
	found := false
	for _, e := range page.Items {
		if e.Subject == nil || e.Subject.Id != operator.Id || e.Kind != gen.AuditKindOperatorCreated {
			continue
		}
		found = true
		if e.SiteId != nil {
			t.Errorf("operator-created carries siteId %q on the wire; the contract's value for a "+
				"change that is not a venue's is null", *e.SiteId)
		}
	}
	if !found {
		t.Error("the operator's creation is absent from the unscoped log too, so it is recorded nowhere")
	}
	for _, e := range page.Items {
		if e.Subject != nil && e.Subject.Id == batch.Id && e.Kind == gen.AuditKindVoucherBatchCreated {
			if e.SiteId == nil || *e.SiteId != w.siteA {
				t.Errorf("the batch entry reaches the console with siteId %v, want %q — this is the "+
					"member the audit screen renders its venue column from", e.SiteId, w.siteA)
			}
		}
	}
}

// audit reads the log through the repository, failing the test rather than the
// caller.
//
// The repository rather than the port, because `api.AuditQuery` has no site member:
// `listAudit` offers no venue filter and deliberately offers none — the console's
// audit screen says so in as many words, since a good half of the entries have no
// venue and filtering by the header's switcher would hide exactly the ones an
// administrator opens the screen for. The scope is a **property of the row**, and
// this is where a row's properties are asserted.
func (w *world) audit(ctx context.Context, f store.AuditFilter) []store.AuditEntry {
	w.t.Helper()
	page, err := w.st.Audit().List(ctx, f)
	if err != nil {
		w.t.Fatalf("reading the audit log: %v", err)
	}
	return page.Items
}

func findEntry(entries []store.AuditEntry, kind, subjectID string) *store.AuditEntry {
	for i := range entries {
		if entries[i].Kind == kind && entries[i].SubjectID == subjectID {
			return &entries[i]
		}
	}
	return nil
}
