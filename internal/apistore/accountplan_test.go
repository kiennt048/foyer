package apistore_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// `createAccount` derives `Plan.enforcedBy`; it does not accept one.
//
// ─────────────────────────────────────────────────────────────────────────────
//
// `AccountCreate.plan` is typed `PlanRequest` rather than `Plan`, because `Plan`
// makes `enforcedBy` **required** — typed that way, a client would not merely be
// permitted to assert who would enforce a guest's limits, it would have to. A
// `planIn` that copied the value straight through would disagree with
// `createVoucherBatch` three files away, which runs `enforcementIn` over the site's
// own devices.
//
// What accepting it buys an attacker, or an honest console with a stale table, is
// one sentence: **a limit that nothing on the network applies, rendered as a cap.**
// `docs/rules/radius.md` is what makes it a security claim rather than a
// cosmetic one — `enforcedBy` decides whether quota is the access point's job, a
// sampled backstop with up to ~3.7 GB of overshoot, or nobody's. A console told
// `nas` shows a hard cap and stops asking.
//
// ── what this test is anchored on, since two of the obvious anchors are vacuous ──
//
// Typing `foyer-sampled` here would be a second copy of `enforcementIn`, which is
// `docs/rules/database.md`'s restated-constraint defect: it drifts the day the
// rule is tightened and reports the repair as a regression. Asserting only "not
// what the client sent" is weaker still — it is satisfied by a server that answers
// one constant.
//
// So the claim asserted is the one actually made: **the account path reads the same
// authority as the batch path.** For each plan, the same limits are sent to
// `createVoucherBatch` in the same site, and the two answers must agree. A third
// plan then requires the answers to differ from each other, so a server that
// answered a single value for everything fails here rather than matching by luck.
func TestAccountPlanEnforcementIsDerivedNotAccepted(t *testing.T) {
	requireDocker(t)
	w := newWorld(t)
	w.signIn()

	// Site A holds two enabled devices: the lobby AP, which has `hardQuota` and
	// `bandwidthLimits`, and the back-office AP, which has neither. The weakest
	// decides — a guest who lands on the one that cannot apply a rate has an
	// unenforced rate, whatever the other one does.
	const hostile = "nas"

	cases := []struct {
		name  string
		plan  map[string]any
		where string
	}{{
		name:  "a data allowance the weakest device cannot cap",
		plan:  map[string]any{"name": "Quota", "dataAllowanceMb": 1024, "enforcedBy": hostile},
		where: w.siteA,
	}, {
		name:  "a rate no device can apply, which nothing can enforce after the fact",
		plan:  map[string]any{"name": "Rate", "downKbps": 4096, "enforcedBy": hostile},
		where: w.siteA,
	}, {
		// Standard RADIUS `Session-Timeout`, gated on no capability, so this one
		// really is `nas` — and it is here so that the cases above cannot be
		// satisfied by a server that has stopped answering `nas` at all.
		name:  "a duration, which every driver here honours",
		plan:  map[string]any{"name": "Duration", "durationMinutes": 60, "enforcedBy": hostile},
		where: w.siteA,
	}}

	answers := make([]gen.PlanEnforcedBy, len(cases))
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			username := uniq("plan")
			acc := decodeAs[gen.Account](t, w.do(http.MethodPost, "/accounts", map[string]any{
				"siteId":   tc.where,
				"username": username,
				"password": randomString() + "Aa1",
				"plan":     tc.plan,
			}, http.StatusCreated))
			if acc.Plan == nil {
				t.Fatal("the account came back with no plan, so there is no enforcement answer to check")
			}

			// Read back rather than trust the 201: the question is what was
			// **stored**, and a handler that composed the right answer and wrote
			// the client's would pass on the response alone.
			var page struct {
				Items []gen.Account `json:"items"`
			}
			w.getJSON("/accounts?siteId="+tc.where+"&q="+username, &page)
			if len(page.Items) != 1 || page.Items[0].Plan == nil {
				t.Fatalf("reading the account back: %+v", page.Items)
			}
			stored := page.Items[0].Plan.EnforcedBy
			if stored != acc.Plan.EnforcedBy {
				t.Fatalf("the creation answered %q and the row holds %q", acc.Plan.EnforcedBy, stored)
			}

			// The same limits, into the operation that has always derived. If the
			// two disagree, there are two copies of the rule again and one of them
			// has drifted — which is the defect this test exists for, one layer up
			// from the value itself.
			batchPlan := map[string]any{}
			for k, v := range tc.plan {
				if k == "enforcedBy" {
					continue
				}
				batchPlan[k] = v
			}
			batch := decodeAs[gen.VoucherBatch](t, w.do(http.MethodPost, "/voucher-batches", map[string]any{
				"siteId": tc.where, "label": uniq("Batch"), "count": 1,
				"expiryMode": "from-first-use", "plan": batchPlan,
			}, http.StatusCreated))
			if stored != batch.Plan.EnforcedBy {
				t.Errorf("the account was stored %q and a voucher batch with the same limits in "+
					"the same site derived %q. Two answers to one question means two copies of "+
					"`enforcementIn`, and the account path is reading the wrong one",
					stored, batch.Plan.EnforcedBy)
			}
			answers[i] = stored
		})
	}

	// Two of the three said something other than the client's claim, and the third
	// agreed with it by derivation rather than by copying. Stated as a difference
	// so that a server answering one constant — including a constant `nas` — fails
	// here instead of matching the hostile value by coincidence.
	if answers[0] == answers[1] && answers[1] == answers[2] {
		t.Fatalf("all three plans were answered %q, so this test cannot tell a derivation "+
			"from a constant", answers[0])
	}
	for i, got := range answers[:2] {
		if got == gen.PlanEnforcedBy(hostile) {
			t.Errorf("case %d sent `enforcedBy: %q` and got it back. Site %s holds a device "+
				"with neither `hardQuota` nor `bandwidthLimits`, so the client asserted a cap "+
				"applied by hardware that was never asked", i, hostile, w.siteA)
		}
	}
}

// decodeAs unmarshals a response body, naming the body when it cannot.
//
// Local to this file rather than a method on `world`: the harness is shared, and a
// helper only this file uses does not belong in it.
func decodeAs[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var into T
	if err := json.Unmarshal(raw, &into); err != nil {
		t.Fatalf("decoding the response: %v: %s", err, raw)
	}
	return into
}
