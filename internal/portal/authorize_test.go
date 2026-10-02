package portal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/nas"
)

// The failure a guest can see and nothing could report.
//
// Every test in this file drives the same shape: a valid voucher, a device the
// driver confirms present, a grant that is written — and then a controller that
// does not let the device on. Without the authorization record, that combination
// produces:
//
//   - a `303` to the connected page, which is correct and stays;
//   - a spent voucher, which is correct and stays;
//   - a guest reading *You're connected* with no internet;
//   - **nothing anywhere saying so** — not the audit log, which cannot hold it,
//     not the console, and not the guest's own screen.
//
// So the assertions are on the two readers rather than on the response: the
// operator's stream, and what the guest's next status read says.

// shortenBackoff makes the retry pauses negligible for a test.
//
// The pauses are real and are chosen against a controller restarting; a suite
// that waited them out would spend seconds proving arithmetic. What is under test
// is *how many times the controller is asked and why*, which the length of the
// pause does not decide.
func shortenBackoff(t *testing.T) {
	t.Helper()
	original := authorizeBackoff
	authorizeBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { authorizeBackoff = original })
}

// redeemAndDeliver runs the whole path: redeem, then the closure the handler
// would run after the response is on the wire.
func (r *rig) redeemAndDeliver(t *testing.T) Outcome {
	t.Helper()
	out := r.svc.Redeem(context.Background(), r.session, "ABCDEFGHJKMN")
	if out.Grant == nil {
		t.Fatalf("no grant was created: %+v", out)
	}
	if out.Authorize == nil {
		t.Fatal("no authorization closure — the handler would have nothing to run after the response")
	}
	_ = out.Authorize(context.Background())
	return out
}

// onlyRecord is the single authorization outcome, or a stated failure.
//
// Indexing directly would panic under a sabotage that stops the recording, and a
// panic aborts the whole binary — so the run that is supposed to show which locks
// went red shows one stack trace instead.
func (r *rig) onlyRecord(t *testing.T) Authorization {
	t.Helper()
	recorded := r.store.Authorizations()
	if len(recorded) != 1 {
		t.Fatalf("%d authorization records, want exactly 1", len(recorded))
	}
	return recorded[0]
}

// state reads what the guest's connected page would render.
func (r *rig) state(t *testing.T) State {
	t.Helper()
	sess, err := r.store.Session(context.Background(), r.session.TokenHash)
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	_, st, err := r.svc.Status(context.Background(), sess)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return st
}

// ── a failed authorization is reported, the centre ───────────────────────────

// TestAFailedAuthorizationReachesTheOperatorAndTheGuest.
//
// The two failures are driven separately because they are two different people's
// jobs and the record has to say which: a controller that could not be reached is
// somebody checking a cable or a container, and one that answered `401` is
// somebody re-issuing an API key. A record that collapsed them would send the
// wrong person.
func TestAFailedAuthorizationReachesTheOperatorAndTheGuest(t *testing.T) {
	shortenBackoff(t)

	cases := []struct {
		name       string
		authErr    error
		wantKind   FailureKind
		wantStatus int
	}{
		{
			name:     "the controller did not answer",
			authErr:  nas.ErrMockControllerUnreachable,
			wantKind: FailureUnreachable,
		},
		{
			// An expired or revoked controller API key. The most likely
			// *permanent* cause, and the one an operator can fix in a minute
			// once they know it happened.
			name:       "the controller answered and refused",
			authErr:    nas.MockAPIRefusal(401, "api key not valid"),
			wantKind:   FailureRefused,
			wantStatus: 401,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, present(), nil)
			r.driver.authErr = tc.authErr

			out := r.redeemAndDeliver(t)

			// The venue's decision stands: the voucher was claimed and the grant
			// is real. Nothing here is a reason to unspend a code — the guest
			// did redeem it, and a portal that silently un-claimed on a
			// controller outage would be a portal where one code lets two
			// devices on.
			if got := len(r.store.Grants()); got != 1 {
				t.Fatalf("%d grants, want the one that was decided", got)
			}

			// ── the operator can find out ────────────────────────────────
			recorded := r.store.Authorizations()
			if len(recorded) != 1 {
				t.Fatalf("%d authorization records, want 1 — with none, this failure "+
					"is invisible to everybody who could fix it", len(recorded))
			}
			a := recorded[0]
			if a.Delivery != DeliveryFailed {
				t.Errorf("recorded delivery is %q, want %q", a.Delivery, DeliveryFailed)
			}
			if a.Failure != tc.wantKind {
				t.Errorf("recorded failure is %q, want %q", a.Failure, tc.wantKind)
			}
			if a.Status != tc.wantStatus {
				t.Errorf("recorded status is %d, want %d", a.Status, tc.wantStatus)
			}
			// The identifiers an operator has when a guest is standing in front
			// of them. A record nobody can join to a device is a record nobody
			// can use.
			if a.GrantID != out.Grant.ID || a.ClientMAC != r.session.ClientMAC || a.SiteID != r.session.SiteID {
				t.Errorf("record does not name the grant, the MAC and the site: %+v", a)
			}
			if a.VoucherID == "" {
				t.Error("record does not name the voucher that was spent for nothing")
			}

			// ── the guest is not told they succeeded ─────────────────────
			if got := r.state(t); got != StateNotOnNetwork {
				t.Errorf("the guest's connected page would read %q, want %q — "+
					"%q is the product telling somebody they are online while they are not",
					got, StateNotOnNetwork, StateConnected)
			}
		})
	}
}

// TestNothingIsRecordedThatCameFromTheController.
//
// `nas.APIError.Error()` embeds the controller URL and up to 512 bytes of its
// response body. Both are one `fmt.Errorf` away from a log an operator reads
// months later: a controller URL can carry an API key in its query string, and a
// body is text somebody else wrote. The record carries a status integer and a
// classification this package assigned, and there is no field either could arrive
// in — which is the same shape `AuthAttempt` uses to make a password
// unrepresentable rather than merely unwritten.
func TestNothingIsRecordedThatCameFromTheController(t *testing.T) {
	shortenBackoff(t)
	r := newRig(t, present(), nil)
	r.driver.authErr = nas.MockAPIRefusal(403, "token=s3cr3t-from-the-controller")

	r.redeemAndDeliver(t)

	a := r.onlyRecord(t)
	for _, s := range []string{a.Username, a.VoucherID, string(a.Failure), a.NASID, a.APMAC, a.ClientMAC} {
		if s == "" {
			continue
		}
		if s == "token=s3cr3t-from-the-controller" || s == "https://mock-controller.invalid/action" {
			t.Errorf("the controller's own text reached the record: %q", s)
		}
	}
	if a.Status != 403 {
		t.Errorf("status is %d, want the 403 the controller answered", a.Status)
	}
}

// TestASuccessfulAuthorizationRecordsThatTheNetworkWasOpened.
//
// The success path is recorded too, and it has to be. An operator searching a MAC
// and finding **nothing** has learned one of two completely different things —
// *this guest never tried* or *we do not write that* — and `AuthBlindSpot` exists
// in the contract because collapsing those is how an operator concludes a guest is
// lying. A stream that only ever holds failures makes every empty result ambiguous.
func TestASuccessfulAuthorizationRecordsThatTheNetworkWasOpened(t *testing.T) {
	r := newRig(t, present(), nil)

	r.redeemAndDeliver(t)

	recorded := r.store.Authorizations()
	if len(recorded) != 1 {
		t.Fatalf("%d authorization records, want 1", len(recorded))
	}
	if recorded[0].Delivery != DeliveryOpen {
		t.Errorf("recorded delivery is %q, want %q", recorded[0].Delivery, DeliveryOpen)
	}
	if recorded[0].Failure != "" {
		t.Errorf("a success recorded a failure kind: %q", recorded[0].Failure)
	}
	if got := r.state(t); got != StateConnected {
		t.Errorf("the guest's page reads %q after a successful authorization, want %q", got, StateConnected)
	}
}

// TestAGrantNothingHasReportedOnStillReadsConnected.
//
// **The race is by construction and this is the side of it that must not be
// alarming.** The redirect is flushed before the driver is called, so the guest's
// browser fetches `/portal/status` while the controller call is still in flight —
// which is what a *successful* redemption looks like for the length of one round
// trip. Rendering "we could not put you on the network" there would show the worst
// page in the product to the guests it is not about.
//
// `DeliveryUnknown` also covers a deployment whose `Authorizations` port is not
// wired. Both read `connected`: *nothing has told us it failed* is not *it failed*.
func TestAGrantNothingHasReportedOnStillReadsConnected(t *testing.T) {
	r := newRig(t, present(), nil)

	// Redeem without running the closure — the driver has not answered yet.
	out := r.svc.Redeem(context.Background(), r.session, "ABCDEFGHJKMN")
	if out.Grant == nil {
		t.Fatalf("no grant: %+v", out)
	}
	if len(r.store.Authorizations()) != 0 {
		t.Fatal("something was recorded before the driver was called")
	}
	if got := r.state(t); got != StateConnected {
		t.Errorf("a grant mid-flight reads %q, want %q", got, StateConnected)
	}
}

// TestTheReportingPathIsProvenByBreakingIt.
//
// **The authorization-failure signal is the only witness to a failure nobody else can
// see, which makes it the worst thing in this package to take on trust.**
// `docs/rules/testing.md` asks for a lock that has been made to fail; a
// reporting mechanism has to carry its own, because there is nothing downstream
// that would notice it had gone quiet.
//
// So the defect is re-injected here, on every run: the
// recorder is made to refuse, the same flow is driven, and both halves of the
// signal are required to disappear. If this case ever passes with a working
// recorder, the assertions above are measuring something other than the recorder.
func TestTheReportingPathIsProvenByBreakingIt(t *testing.T) {
	shortenBackoff(t)
	r := newRig(t, present(), nil)
	r.driver.authErr = nas.MockAPIRefusal(401, "api key not valid")
	r.store.FailRecording(errors.New("the event store is refusing writes"))

	r.redeemAndDeliver(t)

	// With the recorder broken a failed authorization is silent again, and this
	// is what that looks like from both readers.
	if got := len(r.store.Authorizations()); got != 0 {
		t.Fatalf("%d records survived a broken recorder — this test is not driving the recorder", got)
	}
	if got := r.state(t); got != StateConnected {
		t.Fatalf("the guest reads %q with the recorder broken, want %q — "+
			"the state above is coming from somewhere other than the record", got, StateConnected)
	}
}

// ── the retry, and its bounds ────────────────────────────────────────────────

// TestAControllerThatDidNotAnswerIsAskedAgain.
//
// The retry exists for one cause and this is it: a controller restarting refuses
// TCP for a second or two. The guest cannot recover from that on their own —
// their voucher is already spent — and nothing is waiting on the answer, because
// the response is already on the wire.
func TestAControllerThatDidNotAnswerIsAskedAgain(t *testing.T) {
	shortenBackoff(t)
	r := newRig(t, present(), nil)
	// Down for the first ask, up for the second.
	r.driver.authErrFor = 1
	r.driver.authErr = nas.ErrMockControllerUnreachable

	r.redeemAndDeliver(t)

	if got := r.driver.calls(); got != 2 {
		t.Errorf("the controller was asked %d times, want 2", got)
	}
	a := r.onlyRecord(t)
	if a.Delivery != DeliveryOpen {
		t.Errorf("delivery is %q after the second ask succeeded, want %q", a.Delivery, DeliveryOpen)
	}
	// **The retry is recorded, not hidden.** A retry nobody can see is a second
	// way to be wrong about what happened: an operator reading *the network was
	// opened* has no way to tell a healthy controller from one that missed the
	// first ask, and the second is a controller about to stop answering at all.
	if a.Attempts != 2 {
		t.Errorf("record says %d attempts, want 2 — a retry that leaves no trace is invisible", a.Attempts)
	}
	if got := r.state(t); got != StateConnected {
		t.Errorf("the guest reads %q after a retry that worked, want %q", got, StateConnected)
	}
}

// TestAControllerThatAnsweredIsNotAskedAgain.
//
// A `401` is a decision. Asking twice more gets the same decision, three times as
// fast, and turns one operator's problem into three requests against a controller
// that is already unhappy. The narrow reading of *retry the transient failure* is
// the one that is true: **transient** is the controller having no opinion yet, not
// any failure that might go away.
//
// A `5xx` and a `429` are the exception and are covered below: those are the
// controller saying *not now*, which is the one answered case where asking again
// is the fix rather than the noise.
func TestAControllerThatAnsweredIsNotAskedAgain(t *testing.T) {
	shortenBackoff(t)

	cases := []struct {
		name  string
		err   error
		asked int
	}{
		{"an expired API key", nas.MockAPIRefusal(401, "unauthorized"), 1},
		{"the wrong vendor site id", nas.MockAPIRefusal(404, "no such site"), 1},
		{"a controller that is up but not ready", nas.MockAPIRefusal(503, "starting"), authorizeAttempts},
		{"a controller asking to be asked later", nas.MockAPIRefusal(429, "slow down"), authorizeAttempts},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, present(), nil)
			r.driver.authErr = tc.err

			r.redeemAndDeliver(t)

			if got := r.driver.calls(); got != tc.asked {
				t.Errorf("the controller was asked %d times, want %d", got, tc.asked)
			}
			if got := r.onlyRecord(t).Attempts; got != tc.asked {
				t.Errorf("record says %d attempts, driver saw %d", got, tc.asked)
			}
		})
	}
}

// TestTheRecordSurvivesAControllerThatAteTheWholeBudget.
//
// **The one case where the record matters most is the one where it would silently
// not happen.** The driver call runs under the caller's deadline; a controller
// that hangs until that deadline leaves nothing for the write that says it hung.
// `deliver` gives the recording its own context and budget for exactly this, and
// a cancelled parent is how that is driven.
func TestTheRecordSurvivesAControllerThatAteTheWholeBudget(t *testing.T) {
	shortenBackoff(t)
	r := newRig(t, present(), nil)
	r.driver.authErr = nas.ErrMockControllerUnreachable

	out := r.svc.Redeem(context.Background(), r.session, "ABCDEFGHJKMN")
	if out.Grant == nil {
		t.Fatalf("no grant: %+v", out)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the budget is gone before the closure is even entered
	_ = out.Authorize(ctx)

	if got := len(r.store.Authorizations()); got != 1 {
		t.Fatalf("%d records after the caller's context expired, want 1 — "+
			"the report died with the thing it was reporting", got)
	}
	if got := r.state(t); got != StateNotOnNetwork {
		t.Errorf("the guest reads %q, want %q", got, StateNotOnNetwork)
	}
}

// ── the in-process rate limit, refused rather than silently multiplied ───────

// TestASecondReplicaWithAnInProcessLimiterIsRefused.
//
// §5 puts three bounds on the pre-authentication state — a byte cap, a time limit
// and a rate limit — because "there is no identity yet, so nothing else bounds
// it". The rate limit is the one this package owns, and an in-process one is only
// the configured rate while exactly one process enforces it.
//
// Two replicas therefore grant **twice** the rate: twice the load on the venue's
// controller, and twice the budget in front of a `WHERE code = $1` that is not a
// constant-time comparison and cannot be made one. Nothing about the running
// system would say so, which is what makes refusing better than starting: a limit
// wrong by a factor of the replica count is worse than a process that will not
// start, because the process that will not start names the limit and the reason.
func TestASecondReplicaWithAnInProcessLimiterIsRefused(t *testing.T) {
	base := func() Ports {
		store := NewMemoryStore()
		return Ports{
			Sessions: store, Grants: store,
			Credentials: &fakeCredentials{},
			Sites:       fakeSites{},
			Drivers:     &fakeDriver{},
			Limiter:     NewTokenBucket(DefaultBurst, DefaultRefill),
		}
	}

	t.Run("one process is the shape v1 ships", func(t *testing.T) {
		for _, replicas := range []int{0, 1} {
			if _, err := NewService(ServiceConfig{Ports: base(), Replicas: replicas}); err != nil {
				t.Errorf("Replicas=%d was refused: %v", replicas, err)
			}
		}
	})

	t.Run("more than one is refused, and the message says why", func(t *testing.T) {
		_, err := NewService(ServiceConfig{Ports: base(), Replicas: 3})
		if err == nil {
			t.Fatal("three replicas behind an in-process limiter were accepted — " +
				"that deployment gets three times the configured pre-authentication rate and nothing says so")
		}
		for _, want := range []string{"3", "rate", "SharedLimiter"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the refusal does not mention %q: %v", want, err)
			}
		}
	})

	t.Run("a limiter that counts across processes is accepted", func(t *testing.T) {
		p := base()
		p.Limiter = sharedLimiter{}
		if _, err := NewService(ServiceConfig{Ports: p, Replicas: 3}); err != nil {
			t.Errorf("a shared limiter was refused: %v", err)
		}
	})

	// The default is what a deployment that declares nothing gets, and it must be
	// the safe one. A `TokenBucket` that answered *shared* — or a `Limiter` whose
	// silence was read as *shared* — would make the refusal above unreachable.
	t.Run("silence means in-process", func(t *testing.T) {
		if limiterIsShared(NewTokenBucket(1, time.Second)) {
			t.Error("TokenBucket claims to count across processes")
		}
		if limiterIsShared(nil) {
			t.Error("a nil limiter was read as shared")
		}
		if !limiterIsShared(sharedLimiter{}) {
			t.Error("a limiter that says it is shared was not believed")
		}
	})
}

// sharedLimiter stands in for the store-backed counter that replaces
// `TokenBucket` in a multi-process deployment. It is the reason `Limiter` is a
// port rather than a concrete type.
type sharedLimiter struct{}

func (sharedLimiter) Allow(string, time.Time) bool { return true }
func (sharedLimiter) SharedAcrossProcesses() bool  { return true }
