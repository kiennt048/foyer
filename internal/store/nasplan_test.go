package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/kiennt048/foyer/internal/store"
)

// A read-only dry run of the projection, which nothing outside this package could
// compute.
//
// `wantedRows` and `nasName` are unexported, so `foyerctl doctor` answered "is the
// projection current?" by parsing both sides back into a `netip.Prefix` and
// comparing those. That comparison **cannot disagree about anything except the
// address**: it reads as current for a row carrying a shortname and a shared secret
// nothing in the system of record produces, which is the drift an operator actually
// calls about — one access point refused with "Shared secret is incorrect" while
// every check is green.
func TestPlanSeesTheDriftAnAddressComparisonCannot(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()

	f.client(t, "10.95.0.1/32")
	f.sync(t)

	plan, err := f.st.NasProjection().Plan(ctx)
	fatalf(t, err, "planning a projection that is already current")
	if !plan.SecretsCompared {
		t.Fatal("the fixture holds a keyring, so the secret comparison must have run")
	}
	if plan.Unchanged != 1 || len(plan.Insert) != 0 || len(plan.Update) != 0 || len(plan.Remove) != 0 {
		t.Fatalf("a projection just written is not reported current: %+v", plan)
	}

	// What a rotation looks like from the projection's side before a sync runs.
	// Written straight into `nas` because there is no repository path that would
	// produce it — which is the point: it is drift, not a write.
	_, err = f.owner.Exec(ctx,
		`UPDATE nas SET secret = 'a-secret-nothing-produces' WHERE nasname = '10.95.0.1'`)
	fatalf(t, err, "drifting the projected secret")

	plan, err = f.st.NasProjection().Plan(ctx)
	fatalf(t, err, "planning against the drifted projection")

	// The halves that stay equal are the ones the previous check compared. If
	// either of these were non-zero this world would not be the one the finding is
	// about.
	if len(plan.Insert) != 0 || len(plan.Remove) != 0 {
		t.Fatalf("the two sides disagree about an address, so this is not the invisible drift: %+v", plan)
	}
	if len(plan.Update) != 1 {
		t.Fatalf("the drifted row is not reported as an update: %+v", plan)
	}
	if got := plan.Update[0]; !slices.Contains(got.Differs, "shared secret") {
		t.Errorf("the update names %v and not the shared secret", got.Differs)
	}
	// The plan reaches a terminal and a support bundle.
	if slices.Contains(plan.Update[0].Differs, "a-secret-nothing-produces") {
		t.Error("the plan carries the value of a shared secret")
	}

	t.Run("without a keyring it says which half it could not compare", func(t *testing.T) {
		// No Keys: the process can still read `radius_clients` and `nas`, and
		// cannot unseal anything.
		blind, err := store.Open(ctx, store.Config{URL: ownerURL(t), TenantID: f.tenant, MaxConns: 2})
		fatalf(t, err, "opening a store with no keyring")
		defer blind.Close()

		p, err := blind.NasProjection().Plan(ctx)
		fatalf(t, err, "planning without a keyring")
		if p.SecretsCompared {
			t.Fatal("a process with no keyring claims it compared shared secrets")
		}
		if len(p.Update) != 0 || p.Unchanged != 1 {
			t.Fatalf("the keyless plan saw something other than the four columns it can read: %+v", p)
		}
		// Which is exactly why the flag exists: this plan and a plan over a
		// current projection are otherwise the same answer.
	})
}

// The half of a projection that is easy to leave out and impossible to notice: a row
// nothing produces any more is a RADIUS client no operator can revoke.
func TestPlanNamesARowTheSystemOfRecordNoLongerProduces(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()

	c := f.client(t, "10.96.0.1/32")
	f.sync(t)

	no := false
	fatalf(t, f.st.RadiusClients().Update(ctx, c.ID, store.RadiusClientPatch{Enabled: &no}),
		"decommissioning the client")

	plan, err := f.st.NasProjection().Plan(ctx)
	fatalf(t, err, "planning after a decommission")
	if !slices.Contains(plan.Remove, "10.96.0.1") {
		t.Fatalf("the row left behind by a decommissioned client is not named: %+v", plan)
	}
}

// The dry run and the write agree, or the dry run is worse than nothing: a plan
// nobody trusts is a plan nobody reads.
func TestPlanAndSyncAgree(t *testing.T) {
	f := newSyncFixture(t)
	ctx := context.Background()

	f.client(t, "10.97.0.1/32")
	f.client(t, "10.97.0.2/32")
	f.client(t, "10.97.0.3/32", func(rc *store.RadiusClient) { rc.SharedSecretID = nil })

	plan, err := f.st.NasProjection().Plan(ctx)
	fatalf(t, err, "planning")

	res := f.sync(t)
	if res.Inserted != len(plan.Insert) {
		t.Errorf("the plan said %d insert(s) and the sync did %d", len(plan.Insert), res.Inserted)
	}
	if res.Updated != len(plan.Update) {
		t.Errorf("the plan said %d update(s) and the sync did %d", len(plan.Update), res.Updated)
	}
	// Names on both sides, not counts. `NasPlan.Remove` and `NasSync.Removed` are
	// both names, so this compares the sets rather than their
	// sizes — two answers of the same length naming different rows is exactly the
	// disagreement a count cannot report.
	if !sameStrings(res.Removed, plan.Remove) {
		t.Errorf("the plan said it would remove %v and the sync removed %v", plan.Remove, res.Removed)
	}
	if res.Unchanged != plan.Unchanged {
		t.Errorf("the plan said %d unchanged and the sync said %d", plan.Unchanged, res.Unchanged)
	}
	if len(res.Skipped) != len(plan.Skipped) {
		t.Errorf("the plan skipped %d and the sync skipped %d", len(plan.Skipped), len(res.Skipped))
	}

	// And the plan is read-only: running it twice reports the same work, because
	// a diagnostic that repairs the thing it diagnoses cannot be run twice to see
	// whether the repair held.
	after, err := f.st.NasProjection().Plan(ctx)
	fatalf(t, err, "planning after the sync")
	if len(after.Insert) != 0 || len(after.Update) != 0 || len(after.Remove) != 0 {
		t.Fatalf("the projection is not current after a sync the plan predicted: %+v", after)
	}
}

// sameStrings compares two name sets without caring about order.
//
// Both sides are sorted by their producers today. Comparing as sets anyway,
// because the assertion is *the plan named exactly what the sync removed* — an
// ordering difference between two correct answers is not that, and a test that
// failed on one would send somebody to fix a sort.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}
