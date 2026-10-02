package main

import (
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/api/memory"
)

// A declaration that names a defect has to disappear when the defect is fixed.
//
// ── why this test exists ─────────────────────────────────────────────────────
//
// The harness fails a run on a **declared divergence that stopped happening**,
// because a divergence that vanishes means either the oracle was corrected or the
// Go server drifted towards the defect, and both are worth stopping for. That rule
// is right, and it has one consequence nobody notices until it bites: a declaration
// written against a *fixture* defect turns the run red on the day somebody repairs
// the fixture, and the report says "declared divergence that did not happen" — which
// reads as a regression and is a repair.
//
// A MAC generator that masks the high nibble of the first octet sets bit 5 where it
// means bit 1, and builds a world that claims `macIsRandomised` over
// universally-administered addresses. A hardcoded declaration for that would turn
// `make diff-api` red the moment the generator is fixed.
//
// So the declaration is derived from the world, and this pins both halves of the
// derivation. **The second case is the one that matters**: it is the world after the
// fix, written here rather than waited for.
func TestTheMacDeclarationDisappearsWhenTheFixturesAreCorrected(t *testing.T) {
	// `a8:…` is universally administered — bit 1 of the first octet is clear.
	// `02:…` is locally administered. `internal/store` reads that bit and nothing
	// else, which is all a MAC can be asked.
	const universal, local = "a8:52:12:16:53:2d", "02:11:22:33:44:55"

	cases := []struct {
		name     string
		sessions []gen.Session
		want     int
		wantKey  bool
	}{
		{
			name: "a fixture defect: a universally-administered address claimed as randomised",
			sessions: []gen.Session{
				{Id: "s1", CallingStationId: universal, MacIsRandomised: ptr(true)},
				{Id: "s2", CallingStationId: local, MacIsRandomised: ptr(true)},
			},
			want: 2, wantKey: true,
		},
		{
			name: "corrected fixtures: every claim agrees with the bit",
			sessions: []gen.Session{
				{Id: "s1", CallingStationId: universal, MacIsRandomised: ptr(false)},
				{Id: "s2", CallingStationId: local, MacIsRandomised: ptr(true)},
			},
			want: 1, wantKey: false,
		},
		{
			// The mismatch is a disagreement, not a direction. A world that called a
			// locally-administered address real would be just as wrong, and a check
			// that only looked one way would pass for it.
			name: "the reverse mismatch is a mismatch too",
			sessions: []gen.Session{
				{Id: "s1", CallingStationId: local, MacIsRandomised: ptr(false)},
			},
			want: 2, wantKey: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sessionIdentityDivergences(&memory.World{Sessions: c.sessions})
			if len(got) != c.want {
				t.Fatalf("got %d declarations, want %d", len(got), c.want)
			}
			// The identity declaration is index 0 in every world, because
			// `GetSession` on the wrapping-counter row takes exactly that one.
			if !hasKey(got[0], "identityKind") {
				t.Fatalf("declaration 0 is not the identity one: %v", got[0].Keys)
			}
			found := false
			for _, d := range got {
				if hasKey(d, "macIsRandomised") {
					found = true
					if !strings.Contains(d.Reason, "derived from the world") {
						t.Errorf("the mac declaration does not say it is derived, so a reader\n" +
							"cannot tell it will disappear rather than turn a repair red")
					}
				}
			}
			if found != c.wantKey {
				t.Fatalf("macIsRandomised declared = %v, want %v", found, c.wantKey)
			}
		})
	}
}

func hasKey(d divergence, key string) bool {
	for _, k := range d.Keys {
		if k == key {
			return true
		}
	}
	return false
}

// A mock this scenario has already driven cannot answer the question it is asked.
//
// Measured: three invocations against one long-lived mock give six
// differences on the third, and every one of them the earlier runs' own rotations
// and revocations — `secretLastUsedAt`, voucher-batch counts, `freshness.state`.
// They name real fields and read exactly like findings, which is why the harness
// refuses rather than reporting them.
//
// The detector is only as good as its agreement with the scenario, so the case that
// matters is the negative one: a **clean** world must not trip it, or the harness
// refuses every run and somebody reaches for `-dirty-mock-ok` permanently.
func TestAMutatedMockIsRecognised(t *testing.T) {
	clean := func() *memory.World {
		return &memory.World{
			Accounts:  []gen.Account{{Id: "acc-1", Username: "nguyen.thi.mai"}},
			Operators: []gen.Operator{{Id: "op-1", Username: "dang.quoc.viet"}},
			Batches:   []gen.VoucherBatch{{Id: "batch-daily", Label: "In hằng ngày — quầy"}},
			Garden: map[string]gen.WalledGarden{"site-hcm": {Entries: []gen.GardenEntry{
				{Id: "garden-portal", Value: "portal.foyer.local"},
			}}},
		}
	}

	if got := mockAlreadyDriven(clean()); len(got) != 0 {
		t.Fatalf("a freshly started mock was called mutated: %v", got)
	}

	// One marker per mutation the scenario performs. Each is checked on its own,
	// because a detector that only fires when all four are present would miss a
	// run that failed partway through — which is exactly when the world is most
	// confusing and the operator most wants to be told.
	cases := []struct {
		name string
		dirt func(*memory.World)
	}{
		{"the account it creates", func(w *memory.World) {
			w.Accounts = append(w.Accounts, gen.Account{Id: "x", Username: harnessAccountName})
		}},
		{"the operator it creates", func(w *memory.World) {
			w.Operators = append(w.Operators, gen.Operator{Id: "x", Username: harnessOperatorName})
		}},
		{"the batch it creates", func(w *memory.World) {
			w.Batches = append(w.Batches, gen.VoucherBatch{Id: "x", Label: harnessBatchLabel})
		}},
		{"the garden entry it creates", func(w *memory.World) {
			g := w.Garden["site-hcm"]
			g.Entries = append(g.Entries, gen.GardenEntry{Id: "x", Value: harnessGardenValue})
			w.Garden["site-hcm"] = g
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := clean()
			c.dirt(w)
			if got := mockAlreadyDriven(w); len(got) != 1 {
				t.Fatalf("got %v, want exactly one marker", got)
			}
		})
	}
}

// Every declaration that names a property of the world computes it.
//
// Two examples. `macIsRandomised` typed against a fixture defect would turn the
// fixture's repair red. A `pgseed.go` that restated `controller_vendor_sites`'
// unique index in Go would go on refusing bindings for a rule a migration had
// narrowed — printing a line that named an index no longer saying that. Both are the same mistake: **a declaration is a claim about
// the run in front of it, and a claim that cannot change with the world is a claim
// about a world that no longer exists.**
//
// So the sweep's result is pinned rather than asserted. Each case below is the world
// *after* the gap it describes is closed, which is the state no fixture set produces
// today and therefore the one nobody would otherwise write down.
func TestDeclarationsAboutTheWorldAreDerivedFromIt(t *testing.T) {
	t.Run("apName and the access-point columns retire when every device has a MAC", func(t *testing.T) {
		withMACs := &memory.World{Nas: []gen.Nas{
			{Id: "nas-1", Mac: ptr("94:2a:6f:d0:30:57"), IpAddress: ptr("10.20.0.11")},
		}}
		if got := apLessDivergence(withMACs); len(got) != 0 {
			t.Errorf("apLess declared over a world of access points: %v", got)
		}
		if got := sessionShapeDivergences(withMACs); len(got) != 1 {
			t.Errorf("got %d session-shape declarations, want only the orphan rule", len(got))
		}
		apLess := &memory.World{Nas: []gen.Nas{{Id: "nas-mt-1", IpAddress: ptr("10.30.0.1")}}}
		if got := apLessDivergence(apLess); len(got) != 1 {
			t.Errorf("apLess not declared over a world with a bare NAS: %v", got)
		}
	})

	t.Run("the invented address retires when the oracle gives every device one", func(t *testing.T) {
		addressed := &memory.World{Nas: []gen.Nas{{Id: "nas-1", IpAddress: ptr("10.20.0.11")}}}
		if got := inventedAddressDivergence(addressed); len(got) != 0 {
			t.Errorf("declared over a world needing no invented address: %v", got)
		}
		if got := inventedAddressDivergence(&memory.World{Nas: []gen.Nas{{Id: "nas-new-1"}}}); len(got) != 1 {
			t.Errorf("not declared over a world with an addressless device: %v", got)
		}
	})

	t.Run("the auth-attempt streams retire when radpostauth can carry them all", func(t *testing.T) {
		radiusOnly := &memory.World{Attempts: []gen.AuthAttempt{
			{Id: "a1", Source: gen.Radius}, {Id: "a2", Source: gen.Radius},
		}}
		if got := nonRadiusAttemptDivergence(radiusOnly); len(got) != 0 {
			t.Errorf("declared over a world of RADIUS rows only: %v", got)
		}
		mixed := &memory.World{Attempts: []gen.AuthAttempt{
			{Id: "a1", Source: gen.Radius}, {Id: "a2", Source: gen.Portal},
		}}
		got := nonRadiusAttemptDivergence(mixed)
		if len(got) != 1 {
			t.Fatalf("not declared over a world with a portal event: %v", got)
		}
		// The count is measured, not typed — that is the whole point of deriving it.
		if !strings.Contains(got[0].Reason, "1 portal") {
			t.Errorf("the reason does not carry the measured count:\n%s", got[0].Reason)
		}
	})

	t.Run("sessionEnded retires when the world records the code a session used", func(t *testing.T) {
		unrecorded := &memory.World{
			Vouchers: []gen.Voucher{{Id: "v-1", Code: "MWCBAMCXP8W8"}},
			Sessions: []gen.Session{{Id: "s-1"}},
		}
		if got := unrecordedVoucherSessionDivergence(unrecorded, "v-1"); len(got) != 1 {
			t.Errorf("not declared over a world that records neither link: %v", got)
		}
		// Either link is enough for the adapter to find the session, so either
		// must retire the declaration. Both are checked because `sessions.go`
		// carries both precisely for failing in different directions.
		byUsername := &memory.World{
			Vouchers: []gen.Voucher{{Id: "v-1", Code: "MWCBAMCXP8W8"}},
			Sessions: []gen.Session{{Id: "s-1", Username: ptr("MWCBAMCXP8W8")}},
		}
		if got := unrecordedVoucherSessionDivergence(byUsername, "v-1"); len(got) != 0 {
			t.Errorf("declared although User-Name carries the code: %v", got)
		}
		byPointer := &memory.World{
			Vouchers: []gen.Voucher{{Id: "v-1", Code: "MWCBAMCXP8W8", CurrentSessionId: ptr("s-1")}},
			Sessions: []gen.Session{{Id: "s-1"}},
		}
		if got := unrecordedVoucherSessionDivergence(byPointer, "v-1"); len(got) != 0 {
			t.Errorf("declared although the voucher names its session: %v", got)
		}
	})
}

// A skip whose reason has expired must fail the run, and a legitimate one must not.
//
// ── why this is a test and not a note ────────────────────────────────────────
//
// The mechanism it locks guards against a skip whose reason — say, *"not
// implemented in Go"* for an operation `internal/api` does implement — has stopped
// being true. Nothing goes red when that happens, because **a skip removes the
// coverage that would have gone red and leaves a sentence in its place** — a thing
// that looks like a check and is not one.
//
// A mechanism against that has two ways to be worthless and this asserts both, in
// the same test, on purpose:
//
//   - it can fail to catch the thing it was written for, which the first two cases
//     re-derive on every run rather than recording once;
//   - **it can flatten the legitimate skips**, which is the more likely failure and
//     the more expensive one. There are twenty-odd `Skip:` sites in `scenario.go`
//     and most are correct — `SignOut` would end the session the rest of the
//     scenario depends on, `CreateController` mints ids that embed `Date.now()`.
//     A validator that made those red would trade one silence for a wall of noise
//     and be turned off within the week. The last case is the whole real scenario.
func TestASkipMustNameAConditionAndNotAPhase(t *testing.T) {
	// `ListSites` rather than a portal operation: it is unambiguously implemented,
	// it is driven by the real scenario, and using it keeps this test independent
	// of whether the guest surface happens to be drivable in the world at hand.
	const implemented = "ListSites"

	cases := []struct {
		name    string
		steps   []step
		wantErr string
	}{
		{
			name: "a skip claiming an implemented operation is unbuilt",
			steps: append(everyOperationDriven(t),
				step{Op: implemented, Skip: "not implemented in Go — needs a real VerifyPresence"}),
			wantErr: "`internal/api` implements it",
		},
		{
			name: "a skip that dates itself to a phase",
			steps: append(everyOperationDriven(t),
				step{Op: implemented, Skip: "the fixture world cannot express this until phase 4"}),
			wantErr: "A phase is not a condition a run can evaluate",
		},
		{
			name: "an operation in neither list",
			// An operation neither driven nor skipped would print `NO REASON
			// RECORDED` in the report while nothing failed.
			steps:   dropOne(everyOperationDriven(t), implemented),
			wantErr: "appear in neither list",
		},
		{
			name: "a skip naming a condition, which must pass",
			steps: append(everyOperationDriven(t), step{
				Op: implemented,
				Skip: "would end the session the rest of the scenario depends on; covered by " +
					"internal/api's own suite instead",
			}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSkips(tc.steps)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("a legitimate scenario was refused: %v", err)
			case tc.wantErr == "":
			case err == nil:
				t.Fatalf("the harness accepted a skip it must refuse (wanted %q)", tc.wantErr)
			case !strings.Contains(err.Error(), tc.wantErr):
				t.Fatalf("refused for the wrong reason\n got: %v\nwant it to mention: %q", err, tc.wantErr)
			}
		})
	}
}

// TestTheRealScenarioPassesItsOwnSkipRules is the case the four above cannot make.
//
// Everything else here is a hand-built list. This one is `buildScenario` over a
// world with the shape the fixtures have, which is what actually ships — and it is
// the assertion that the validator does not flatten the twenty-odd legitimate
// skips. It is also what would have gone red on the day the five portal reasons
// expired, had it existed.
func TestTheRealScenarioPassesItsOwnSkipRules(t *testing.T) {
	steps, err := buildScenario(worldForSkipTest())
	if err != nil {
		t.Fatalf("buildScenario: %v", err)
	}
	if err := validateSkips(steps); err != nil {
		t.Fatalf("the shipped scenario does not satisfy its own skip rules: %v", err)
	}
}

// everyOperationDriven is one driven step per contract operation.
//
// Read off the generated interface rather than listed, so a new operation makes
// these cases exercise it without an edit here — the same rule `report`'s
// denominator follows.
func everyOperationDriven(t *testing.T) []step {
	t.Helper()
	ops := generatedOperations()
	out := make([]step, 0, len(ops))
	for _, op := range ops {
		out = append(out, step{Op: op, Method: "GET", Path: "/" + op})
	}
	return out
}

func dropOne(steps []step, op string) []step {
	out := make([]step, 0, len(steps))
	for _, s := range steps {
		if s.Op != op {
			out = append(out, s)
		}
	}
	return out
}

// worldForSkipTest is the minimum `buildScenario` needs: one site, and enough of
// the rest that its `find…` helpers do not all come back empty.
func worldForSkipTest() *memory.World {
	return &memory.World{
		Sites:     []gen.Site{{Id: "site-1", Name: "Venue"}},
		Operators: []gen.Operator{{Id: "op-1", Username: "admin", Role: gen.Administrator, Enabled: true}},
	}
}
