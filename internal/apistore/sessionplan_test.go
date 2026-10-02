package apistore_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// `Session.plan` — what the guest on this session was sold.
//
// `radacct` has no plan column and never will: the stock FreeRADIUS column set is
// kept verbatim because it is the project's exit hatch. The plan therefore has to
// be joined from Foyer's own tables, and the only value in an accounting row that
// can key such a join is `radacct.username` — which for a redeemed voucher is the
// code itself, and for a guest account is the account's username. That is not a new
// belief: `identityKind` already reads exactly this, and `sessions.go` already
// resolves a voucher filter through it.
//
// Three properties, and the third is the one a single-site world cannot express.
//
//  1. A voucher session carries **its batch's** plan, compared against the batch
//     read back through the adapter rather than against numbers typed here. The
//     batch is the authority for what a voucher sells; a copy of its figures in a
//     test is a second one that drifts the day `enforcementIn` changes its mind.
//  2. A session whose username is nobody Foyer knows carries **no plan at all** —
//     null, not an empty plan. An empty `Plan` renders `enforcedBy: unenforced`,
//     which is a statement about a limit, and there is no limit here to make a
//     statement about.
//  3. **The join is site-scoped.** Two accounts may share a username across sites —
//     `accounts` is `UNIQUE (tenant_id, site_id, username)`, not unique on the
//     username — so a join that forgets the site resolves one venue's guest against
//     another venue's plan. This seeds exactly that: one username, two sites, two
//     different plans, and asserts each session gets its own.
//
// ── how this fails ───────────────────────────────────────────────────────────
//
// Removing the `attachPlans` call from `Sessions` and `SessionByID` leaves
// `sessionOut` with no `Plan`, and all three subtests report `plan is nil`.
// Property 3 has its own red:
// dropping `AND a.site_id = w.site_id` from the accounts join in
// `internal/store/sessionplan.go` leaves 1 and 2 green and turns 3 red, which is
// what says the second site is doing work rather than sitting there.
func TestASessionCarriesThePlanItsIdentityWasSold(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()

	t.Run("a voucher session carries its batch's plan", func(t *testing.T) {
		batch, err := w.adapter.Batch(ctx, w.batchA)
		if err != nil {
			t.Fatalf("reading the batch back: %v", err)
		}
		got := w.session(t, w.sessionOnline)
		if got.Plan == nil {
			t.Fatalf("session %s authenticated as voucher %s and carries no plan; the "+
				"console's session row has nothing to say about what this guest was sold",
				got.Id, w.voucherCode)
		}
		if !reflect.DeepEqual(*got.Plan, batch.Plan) {
			t.Fatalf("session plan %+v is not the batch's %+v", *got.Plan, batch.Plan)
		}
	})

	t.Run("an identity Foyer does not own carries no plan", func(t *testing.T) {
		got := w.session(t, w.sessionOrphaned)
		if got.Plan != nil {
			t.Fatalf("session %s authenticated as %q — no voucher, no account — and "+
				"carries plan %+v. An invented plan is worse than a null: it states a "+
				"limit nothing agreed to.", got.Id, "someone", *got.Plan)
		}
	})

	t.Run("the join is scoped to the session's own site", func(t *testing.T) {
		username := uniq("shared")
		// One username, two sites, two plans. `accounts` is unique per
		// (tenant, site, username), so this is a row set the schema permits and a
		// deployment produces the first time two venues both call somebody "reception".
		asked := map[string]gen.PlanRequest{
			w.siteA: {DurationMinutes: ptr(60), DataAllowanceMb: ptr(1024)},
			w.siteB: {DurationMinutes: ptr(15), DataAllowanceMb: ptr(64)},
		}
		// What the account came back as, not what was asked for. `enforcedBy` is
		// server-derived by contract, so an expectation typed here would be a second
		// copy of a rule `createAccount` owns — and would go red the day that
		// derivation is tightened, reporting the repair as a regression.
		//
		// It cannot be typed here at all: `AccountCreate.plan` is a `PlanRequest`,
		// which has no `enforcedBy` member, so the comment above is a compile error
		// rather than a discipline.
		plans := map[string]gen.Plan{}
		for site, plan := range asked {
			acc, err := w.adapter.CreateAccount(ctx, gen.AccountCreate{
				SiteId: site, Username: username, Password: randomString() + "Aa1", Plan: ptr(plan),
			})
			if err != nil {
				t.Fatalf("creating the account in %s: %v", site, err)
			}
			if acc.Plan == nil {
				t.Fatalf("the account created in %s came back with no plan", site)
			}
			plans[site] = *acc.Plan
		}
		if reflect.DeepEqual(plans[w.siteA], plans[w.siteB]) {
			t.Fatalf("both sites' accounts ended up on the same plan %+v, so this subtest "+
				"cannot tell a site-scoped join from an unscoped one", plans[w.siteA])
		}

		// One accounting row per site, both authenticated as that username. The NAS
		// addresses are this world's own: `.11` is site A's device, `.13` is site B's.
		ids := map[string]string{w.siteA: w.block + "shared-a", w.siteB: w.block + "shared-b"}
		nasIP := map[string]string{w.siteA: w.block + "11", w.siteB: w.block + "13"}
		for site, id := range ids {
			if _, err := w.owner.Exec(ctx, `
				INSERT INTO radacct (acctuniqueid, acctsessionid, username, nasipaddress,
				                     callingstationid, acctstarttime, acctupdatetime)
				VALUES ($1, $2, $3, $4::inet, '02:00:00:00:00:01', $5, $5)`,
				id, id, username, nasIP[site], time.Now().Add(-10*time.Minute)); err != nil {
				t.Fatalf("seeding the accounting row for %s: %v", site, err)
			}
		}

		for site, id := range ids {
			got := w.session(t, id)
			if got.SiteId != site {
				t.Fatalf("session %s resolved to site %s, not %s — the world is not what "+
					"this assertion needs", id, got.SiteId, site)
			}
			if got.Plan == nil {
				t.Fatalf("session %s in %s carries no plan", id, site)
			}
			want := plans[site]
			if !reflect.DeepEqual(*got.Plan, want) {
				t.Fatalf("session %s is in %s and carries %+v, want %+v — the plan join "+
					"crossed a site boundary, which is one venue's guest wearing another "+
					"venue's allowance", id, site, *got.Plan, want)
			}
		}
	})
}

// session reads one row back through both session ports.
//
// Both, because they are two code paths to one answer — `Sessions` fills a page and
// `SessionByID` reads a row — and a plan attached in one and forgotten in the other
// is a console whose list and whose detail panel disagree about what a guest bought.
func (w *world) session(t *testing.T, id string) gen.Session {
	t.Helper()
	one, err := w.adapter.SessionByID(context.Background(), id)
	if err != nil {
		t.Fatalf("reading session %s: %v", id, err)
	}
	page, err := w.adapter.Sessions(context.Background(), api.SessionQuery{Window: api.Window{Limit: 200}})
	if err != nil {
		t.Fatalf("listing sessions: %v", err)
	}
	for _, s := range page.Items {
		if s.Id != id {
			continue
		}
		if !reflect.DeepEqual(s.Plan, one.Plan) {
			t.Fatalf("session %s carries plan %+v in the list and %+v on its own", id, s.Plan, one.Plan)
		}
		return one
	}
	t.Fatalf("session %s is not in the listing", id)
	return gen.Session{}
}
