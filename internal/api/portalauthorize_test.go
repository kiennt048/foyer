package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/portal"
)

// The guest who was told they succeeded, driven over the wire.
//
// `internal/portal` proves the decision; this proves it is wired to the routes,
// and it is the only place that can, because two of the three parts of the
// failure are properties of **separate HTTP requests**:
//
//   - the `303` still goes out, and still goes to the connected page. That is not
//     negotiable and is not what changed — the CNA re-checks captivity only after
//     a document navigation, and a portal that answered anything else here would
//     leave the guest behind a Cancel button that disassociates them.
//   - the truth arrives on the **next** request. `GET /portal/status` is a
//     different exchange, made after the controller has answered, and it is the
//     only channel left once the redirect is on the wire.
//
// A test inside the service could assert neither of those.

// failingAuthorizeRig is a venue whose controller refuses to let anybody on.
//
// `MockAPIRefusal` rather than `Unreachable`: an answered refusal is not retried,
// so this drives the whole path in one round trip instead of waiting out a
// backoff. Which of the two it is, is `internal/portal`'s to prove — here the
// point is that *a* failure reaches two readers it could not reach before.
func failingAuthorizeRig(t *testing.T, fault error) *guestRig {
	t.Helper()
	d := newGuestDriver()
	d.Presences[guestMAC] = true
	d.WithFaults(func(f *nas.MockFaults) { f.Authorize = fault })
	return newGuestRig(t, d)
}

// waitForAuthorizationRecord is an observation with a bound, never the assertion.
//
// The driver call runs **after** the response is flushed, so `client.Do` returns
// while it is still in flight — the race is a property of the ordering rule, not
// a flaky test. `docs/rules/testing.md`: a single sample taken at the moment a
// request is observed is a race by construction; the wait is what makes the
// sample settled, and the assertions on the settled value live in the caller.
func waitForAuthorizationRecord(t *testing.T, g *guestRig) []portal.Authorization {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := g.store.PortalStore.Authorizations()
		if len(got) > 0 {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatal("no authorization outcome was recorded within two seconds — " +
				"this is the silence this test exists for: the guest has a grant, no internet, " +
				"and nobody who could fix it has been told")
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (g *guestRig) status(t *testing.T) gen.PortalStatus {
	t.Helper()
	resp := g.send(http.MethodGet, "/portal/status", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /portal/status answered %d, want 200 — a grant that exists is "+
			"answered with its real state, never hidden behind \"not connected\"", resp.StatusCode)
	}
	var out gen.PortalStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode PortalStatus: %v", err)
	}
	return out
}

// TestAGuestWhoseAuthorizationFailedIsNotToldTheyAreConnected.
//
// The whole failure in one drive: a valid voucher, a device the driver confirms
// present, a controller that then refuses. Before this, the guest got the
// connected page, the voucher was spent, and the failure reached nobody at all —
// there is no `AuditKind` that could hold it and `internal/api`'s invariant 10
// forbids filing it under a near-enough one, so the audit log was never the
// answer. `listAuthAttempts` already declared `source: driver` /
// `kind: authorize-failed` for exactly this and nothing wrote it.
func TestAGuestWhoseAuthorizationFailedIsNotToldTheyAreConnected(t *testing.T) {
	g := failingAuthorizeRig(t, nas.MockAPIRefusal(401, "api key not valid"))
	g.arrive(guestMAC).Body.Close()

	resp := g.send(http.MethodPost, "/portal/voucher", url.Values{"code": {goodCode}, "lang": {"en"}})
	defer resp.Body.Close()

	// **The redirect is unchanged, and that is deliberate.** Nothing about a
	// failed controller call may turn this into a response the CNA does not
	// treat as a captivity re-check: the guest is still mid-flow, and a portal
	// that answered a problem document here would leave them stuck behind a
	// button that throws them off the network.
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status is %d, want 303 even when the authorization fails", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/en/connected/" {
		t.Errorf("Location is %q, want the connected page", loc)
	}

	recorded := waitForAuthorizationRecord(t, g)

	// ── the operator can find out ────────────────────────────────────────
	if len(recorded) != 1 {
		t.Fatalf("%d authorization records, want 1", len(recorded))
	}
	a := recorded[0]
	if a.Delivery != portal.DeliveryFailed {
		t.Errorf("recorded delivery is %q, want %q", a.Delivery, portal.DeliveryFailed)
	}
	if a.Failure != portal.FailureRefused || a.Status != 401 {
		t.Errorf("recorded %q/%d, want %q/401 — an expired API key and an unreachable "+
			"controller are two different people's jobs", a.Failure, a.Status, portal.FailureRefused)
	}
	if a.ClientMAC != guestMAC {
		t.Errorf("record names %q, want the MAC an operator would search by (%q)", a.ClientMAC, guestMAC)
	}

	// ── the guest is not told they succeeded ─────────────────────────────
	st := g.status(t)
	if st.State != gen.PortalStatusStateNotOnNetwork {
		t.Errorf("PortalStatus.state is %q, want %q — %q is the product telling "+
			"somebody they are online while no packet of theirs leaves the access point",
			st.State, gen.PortalStatusStateNotOnNetwork, gen.PortalStatusStateConnected)
	}
	// The plan still travels. What failed is the network, not the sale, and a
	// guest at the counter needs to be able to say what they bought.
	if st.Plan.DurationMinutes == nil {
		t.Error("the plan was dropped from a not-on-network status")
	}
}

// TestASuccessfulAuthorizationStillReadsConnectedOverTheWire.
//
// The other half of the same switch, and it is the one that would be expensive to
// get wrong: `not-on-network` is the worst page in the product, and showing it to
// a guest who *is* online would be a self-inflicted support call on every
// redemption. Driven through the same routes so that the two answers come from
// one mechanism rather than from two.
func TestASuccessfulAuthorizationStillReadsConnectedOverTheWire(t *testing.T) {
	g := presentRig(t)
	g.arrive(guestMAC).Body.Close()

	resp := g.send(http.MethodPost, "/portal/voucher", url.Values{"code": {goodCode}, "lang": {"en"}})
	resp.Body.Close()

	recorded := waitForAuthorizationRecord(t, g)
	if recorded[0].Delivery != portal.DeliveryOpen {
		t.Fatalf("recorded delivery is %q, want %q", recorded[0].Delivery, portal.DeliveryOpen)
	}
	if st := g.status(t); st.State != gen.PortalStatusStateConnected {
		t.Errorf("PortalStatus.state is %q for a guest the controller let on, want %q", st.State, gen.PortalStatusStateConnected)
	}
}

// TestTheStateTheGuestSeesComesFromTheContractsOwnEnum.
//
// `PortalStatusState` is generated from `web/contract/openapi.yaml`, and the
// handler casts `portal.State` straight into it. That cast is the seam where a
// value this package believes in and a value the schema declares can silently
// diverge — a `500` on the one screen a guest sees after typing a credential, or
// a state the portal has no copy for and renders as *something went wrong*.
//
// So the four are required to agree in both directions, derived from the
// generated enum rather than typed out here.
func TestTheStateTheGuestSeesComesFromTheContractsOwnEnum(t *testing.T) {
	states := []portal.State{
		portal.StateConnected, portal.StateExpired,
		portal.StateRevoked, portal.StateNotOnNetwork,
	}
	for _, s := range states {
		if !gen.PortalStatusState(s).Valid() {
			t.Errorf("portal.State %q is not a member of the contract's PortalStatusState", s)
		}
	}

	declared := []gen.PortalStatusState{
		gen.PortalStatusStateConnected, gen.PortalStatusStateExpired,
		gen.PortalStatusStateRevoked, gen.PortalStatusStateNotOnNetwork,
	}
	if len(declared) != len(states) {
		t.Fatalf("%d contract states against %d portal states", len(declared), len(states))
	}
	for _, d := range declared {
		found := false
		for _, s := range states {
			if string(s) == string(d) {
				found = true
			}
		}
		if !found {
			t.Errorf("the contract declares %q and internal/portal cannot produce it", d)
		}
	}
}
