package portal

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/nas"
)

// The service, driven against a store it can see the inside of.
//
// The HTTP half is in `internal/api` and drives the same rules through a real
// server. Both exist on purpose: a test that goes through the routes proves the
// routes as well as the rule, and a test that does not can assert on a store —
// which is what makes *"no grant was created"* a statement about grants rather
// than about a status code.

// ── a driver that answers exactly what a test asked it to ────────────────────

type fakeDriver struct {
	mu sync.Mutex

	presence nas.Presence
	err      error

	authorized []Grant
	authErr    error
	// authErrFor bounds how many calls fail, so a controller that is down for
	// one ask and up for the next is drivable. Zero means every call fails,
	// which is what a permanently refusing controller looks like.
	authErrFor int
	hardQuota  bool

	// authorizedAt records when the driver was called, which is the only way to
	// assert the ordering rule the contract states. See the API suite.
	authorizedAt []time.Time

	// verified is the session the driver was asked to confirm presence for, so a
	// test can assert that the same placement carried both calls.
	verified Session

	// placements records what the driver was told about where the guest is. A
	// driver takes the *vendor's* site id and a grant carries Foyer's, so this is
	// what makes "the seam handed over the right site" assertable at all.
	placements []Placement
	clock      func() time.Time
}

func (d *fakeDriver) Verify(_ context.Context, s Session) (nas.Presence, error) {
	d.mu.Lock()
	d.verified = s
	d.mu.Unlock()
	return d.presence, d.err
}

func (d *fakeDriver) Authorize(_ context.Context, p Placement, g Grant) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.authorized = append(d.authorized, g)
	d.placements = append(d.placements, p)
	if d.clock != nil {
		d.authorizedAt = append(d.authorizedAt, d.clock())
	}
	if d.authErrFor > 0 && len(d.authorized) > d.authErrFor {
		return nil
	}
	return d.authErr
}

func (d *fakeDriver) HardQuota(context.Context, Session) bool { return d.hardQuota }

func (d *fakeDriver) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.authorized)
}

// ── a credential store with one voucher and one account ──────────────────────

type fakeCredentials struct {
	mu sync.Mutex

	code      string
	claimed   bool
	plan      Plan
	claims    int
	username  string
	password  string
	disabled  bool
	claimFail error
}

func (c *fakeCredentials) ClaimVoucher(_ context.Context, _, code, _ string, _ time.Time) (Claim, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.claims++
	if c.claimFail != nil {
		return Claim{}, c.claimFail
	}
	if !SameCode(code, c.code) {
		return Claim{}, ErrVoucherUnknown
	}
	// The single-use rule, expressed the way the interface requires: the state
	// is tested and changed together, so a second caller cannot win.
	if c.claimed {
		return Claim{}, ErrVoucherUsed
	}
	c.claimed = true
	return Claim{VoucherID: "voucher-1", BatchID: "batch-1", Plan: c.plan}, nil
}

func (c *fakeCredentials) AuthenticateGuest(_ context.Context, _, username, password string) (Guest, error) {
	if username != c.username || password != c.password || password == "" {
		return Guest{}, ErrBadCredentials
	}
	if c.disabled {
		return Guest{}, ErrAccountDisabled
	}
	return Guest{AccountID: "account-1", Username: username, Plan: c.plan}, nil
}

type fakeSites struct{ place Placement }

func (f fakeSites) Placement(context.Context, string) (Placement, error) { return f.place, nil }

// ── the rig ──────────────────────────────────────────────────────────────────

type rig struct {
	svc     *Service
	store   *MemoryStore
	driver  *fakeDriver
	creds   *fakeCredentials
	session Session
	now     time.Time
}

func newRig(t *testing.T, presence nas.Presence, err error) *rig {
	t.Helper()
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	store := NewMemoryStore()
	store.Now = func() time.Time { return now }
	sixty := 60
	r := &rig{
		store:  store,
		driver: &fakeDriver{presence: presence, err: err},
		creds: &fakeCredentials{
			code:     "ABCDEFGHJKMN",
			plan:     Plan{Name: "Café 1 hour", DurationMinutes: &sixty},
			username: "kim",
			password: "a-long-enough-passphrase",
		},
		now: now,
	}
	svc, e := NewService(ServiceConfig{
		Ports: Ports{
			Sessions:    store,
			Grants:      store,
			Credentials: r.creds,
			Sites:       fakeSites{place: Placement{SiteID: "site-one", NASID: "nas-1"}},
			Drivers:     r.driver,
			// Wired rather than defaulted. `NewService` supplies a log-only
			// recorder when this is nil, and a suite running on that would
			// prove the failure reaches a log line while the two readers that
			// matter stayed silent.
			Authorizations: store,
		},
		Now:   func() time.Time { return now },
		NewID: func() (string, error) { return "grant-1", nil },
	})
	if e != nil {
		t.Fatalf("NewService: %v", e)
	}
	r.svc = svc
	r.session = Session{
		TokenHash: "token-hash", SiteID: "site-one", NASID: "nas-1",
		ClientMAC: "aa:bb:cc:dd:ee:ff", APMAC: "11:22:33:44:55:66",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := store.CreateSession(context.Background(), r.session); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	return r
}

func present() nas.Presence {
	return nas.Presence{OK: true, Outcome: nas.PresenceVerified}
}

// ── the named security test ──────────────────────────────────────────────────

// TestVerifyPresence_NoGrantIsCreatedForAMACTheDriverDidNotConfirm.
//
// `docs/rules/testing.md` names this one, and it is named because the failure
// it guards is not a crash: a portal that skips the check answers `303` to the
// connected page and looks perfect. What is wrong is invisible from the response,
// so the assertion is on the **store** — no grant exists — and on the driver — it
// was never asked to authorize anybody.
//
// It runs over every non-verified outcome the driver can produce, not just
// `not-found`. `already-authorized` and `unavailable` are refusals *for granting*
// even though they are not refusals for the guest, and a fix that only handled
// the obvious one would leave a grant issued off an outage.
//
// `nas.Presence{OK: true}` in place of each refusal — which is what a driver
// answering "present" unconditionally does — turns all four cases red on the
// grant count.
func TestVerifyPresence_NoGrantIsCreatedForAMACTheDriverDidNotConfirm(t *testing.T) {
	cases := []struct {
		name     string
		presence nas.Presence
		err      error
	}{
		{"never associated", nas.Presence{Outcome: nas.PresenceNotFound}, nil},
		{"a different access point", nas.Presence{Outcome: nas.PresenceAPMismatch}, nil},
		{"a different site", nas.Presence{Outcome: nas.PresenceSiteMismatch}, nil},
		{"the controller could not answer", nas.Presence{Outcome: nas.PresenceUnavailable}, errors.New("dial tcp: connection refused")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()

			// Every way in, each against its own empty store, because the
			// assertion is that the store stayed empty.
			attempts := map[string]func(*rig) Outcome{
				"redeemVoucher": func(r *rig) Outcome {
					return r.svc.Redeem(ctx, r.session, "ABCDEFGHJKMN")
				},
				"acceptTerms": func(r *rig) Outcome {
					return r.svc.Agree(ctx, r.session, true, 30)
				},
				"loginWithAccount": func(r *rig) Outcome {
					return r.svc.Login(ctx, r.session, "kim", "a-long-enough-passphrase")
				},
			}
			for what, run := range attempts {
				r := newRig(t, tc.presence, tc.err)
				out := run(r)

				if out.Connected {
					t.Errorf("%s: the guest was connected on a %q presence", what, tc.presence.Outcome)
				}
				if got := r.store.Grants(); len(got) != 0 {
					t.Errorf("%s: %d grant(s) created for a MAC the driver did not confirm: %+v",
						what, len(got), got)
				}
				if n := r.driver.calls(); n != 0 {
					t.Errorf("%s: the driver was asked to authorize %d time(s)", what, n)
				}
				if out.Authorize != nil {
					t.Errorf("%s: a refusal carried a driver call", what)
				}
			}
		})
	}
}

// TestAVoucherIsNotBurnedForADeviceThatIsNotThere.
//
// The order inside `Redeem` is presence first, then shape, then claim, and this
// is the half of that ordering a guest feels. A portal that claimed first and
// checked second would consume a single-use code for a device that never got
// online, and the guest would be standing at a counter being told their voucher
// is already used.
func TestAVoucherIsNotBurnedForADeviceThatIsNotThere(t *testing.T) {
	r := newRig(t, nas.Presence{Outcome: nas.PresenceNotFound}, nil)
	r.svc.Redeem(context.Background(), r.session, "ABCDEFGHJKMN")
	if r.creds.claims != 0 {
		t.Errorf("the store was asked to claim a voucher %d time(s) for an absent device", r.creds.claims)
	}
	if r.creds.claimed {
		t.Error("the voucher is marked used and nobody got online with it")
	}
}

// ── already authorized, and unavailable ──────────────────────────────────────

// TestAlreadyAuthorizedReachesTheConnectedPageAndBurnsNothing.
//
// An already-authorized guest must not be told "Device not present on network":
// a person who is online being told they are not, on a page inviting them to
// type a voucher they do not need. They reach the connected page — and, the part
// that costs money if it is wrong, **no voucher is claimed** on the way.
func TestAlreadyAuthorizedReachesTheConnectedPageAndBurnsNothing(t *testing.T) {
	r := newRig(t, nas.Presence{Outcome: nas.PresenceAlreadyAuthorized}, nil)
	out := r.svc.Redeem(context.Background(), r.session, "ABCDEFGHJKMN")
	if !out.Connected {
		t.Fatalf("an already-authorized guest was refused with %q", out.Reason)
	}
	if r.creds.claims != 0 {
		t.Errorf("a voucher was claimed for a guest who was already online (%d claims)", r.creds.claims)
	}
	if out.Grant != nil {
		t.Error("a second grant was created for a device the driver reports as already on")
	}
}

// TestAnOutageIsNotTheGuestsFault.
//
// `not-present` sends somebody to toggle Wi-Fi for twenty minutes. `generic`
// sends them to the counter, which is where a person can see that the controller
// is down. The difference is one string and it is the whole of the fix.
func TestAnOutageIsNotTheGuestsFault(t *testing.T) {
	r := newRig(t, nas.Presence{Outcome: nas.PresenceUnavailable}, errors.New("i/o timeout"))
	out := r.svc.Agree(context.Background(), r.session, true, 30)
	if out.Connected {
		t.Fatal("an unreachable controller let somebody onto the network — the check must fail closed")
	}
	if out.Reason != ReasonGeneric {
		t.Errorf("reason is %q, want %q — the venue's outage must not be reported as the guest's device",
			out.Reason, ReasonGeneric)
	}
}

// TestAnErrorBesideAConfidentYesIsStillAnOutage.
//
// A driver that returned `OK: true` beside a non-nil error would be a driver bug.
// Trusting the `OK` is how a driver bug becomes a grant, so `Classify` reads the
// error first and this pins that ordering.
func TestAnErrorBesideAConfidentYesIsStillAnOutage(t *testing.T) {
	r := newRig(t, nas.Presence{OK: true, Outcome: nas.PresenceVerified}, errors.New("truncated response"))
	out := r.svc.Agree(context.Background(), r.session, true, 30)
	if out.Connected {
		t.Fatal("a driver error was overridden by the OK beside it")
	}
	if len(r.store.Grants()) != 0 {
		t.Error("a grant exists off an errored presence check")
	}
}

// ── the happy paths, and what they record ────────────────────────────────────

func TestRedeemGrantsAndDefersTheDriverCall(t *testing.T) {
	r := newRig(t, present(), nil)
	out := r.svc.Redeem(context.Background(), r.session, "abcd-efgh-jkmn")
	if !out.Connected || out.Grant == nil {
		t.Fatalf("redeem refused a good code: %q", out.Reason)
	}
	// The grant exists; the driver has not been called. That separation is the
	// ordering rule: the response goes out first and the handler runs this after.
	if r.driver.calls() != 0 {
		t.Error("the driver was called inside the service — the document must be written first")
	}
	if out.Authorize == nil {
		t.Fatal("a grant with no driver call attached — the guest would be recorded and never let on")
	}
	if err := out.Authorize(context.Background()); err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if r.driver.calls() != 1 {
		t.Errorf("the driver was called %d times", r.driver.calls())
	}

	// The session now names the grant, which is what `getPortalStatus` resolves
	// through — never through the MAC.
	sess, err := r.store.Session(context.Background(), "token-hash")
	if err != nil {
		t.Fatalf("Session: %v", err)
	}
	if sess.GrantID != "grant-1" {
		t.Errorf("the grant was not bound to the browser: %q", sess.GrantID)
	}
}

func TestRedeemIsSingleUse(t *testing.T) {
	r := newRig(t, present(), nil)
	if out := r.svc.Redeem(context.Background(), r.session, "ABCDEFGHJKMN"); !out.Connected {
		t.Fatalf("first redemption refused: %q", out.Reason)
	}
	out := r.svc.Redeem(context.Background(), r.session, "ABCDEFGHJKMN")
	if out.Connected {
		t.Fatal("the same code was redeemed twice")
	}
	if out.Reason != ReasonAlreadyUsed {
		t.Errorf("reason is %q, want %q", out.Reason, ReasonAlreadyUsed)
	}
}

// TestAMalformedCodeNeverReachesTheStore.
//
// `error.shape` — "a voucher code is 12 characters" — is an instruction a guest
// can act on. `error.not-found` sends them to check twelve characters that were
// never the problem, and it costs a store lookup to say it.
func TestAMalformedCodeNeverReachesTheStore(t *testing.T) {
	r := newRig(t, present(), nil)
	out := r.svc.Redeem(context.Background(), r.session, "ABC")
	if out.Reason != ReasonShape {
		t.Errorf("reason is %q, want %q", out.Reason, ReasonShape)
	}
	if r.creds.claims != 0 {
		t.Errorf("a submission that cannot be a voucher reached the store %d time(s)", r.creds.claims)
	}
}

func TestClickThroughIsRefusedWhereTheVenueDidNotOfferIt(t *testing.T) {
	r := newRig(t, present(), nil)
	if out := r.svc.Agree(context.Background(), r.session, false, 30); !out.NotOffered {
		t.Error("click-through worked at a venue that has not enabled it")
	}
	// Offered with no duration is the same refusal. A mode with no credential
	// and no duration is unmetered public internet handed out on one tap.
	if out := r.svc.Agree(context.Background(), r.session, true, 0); !out.NotOffered {
		t.Error("click-through granted with no duration configured")
	}
	if len(r.store.Grants()) != 0 {
		t.Error("a grant exists for a method the venue does not offer")
	}
}

func TestADisabledAccountIsToldSoAndAnUnknownOneIsNot(t *testing.T) {
	ctx := context.Background()

	r := newRig(t, present(), nil)
	r.creds.disabled = true
	if out := r.svc.Login(ctx, r.session, "kim", "a-long-enough-passphrase"); out.Reason != ReasonDisabled {
		t.Errorf("a disabled account got %q — the holder is in the venue and staff can turn it back on", out.Reason)
	}

	r = newRig(t, present(), nil)
	if out := r.svc.Login(ctx, r.session, "nobody", "whatever"); out.Reason != ReasonBadCredentials {
		t.Errorf("an unknown username got %q, want %q — anything else is an account oracle",
			out.Reason, ReasonBadCredentials)
	}
	r = newRig(t, present(), nil)
	if out := r.svc.Login(ctx, r.session, "kim", "wrong"); out.Reason != ReasonBadCredentials {
		t.Errorf("a wrong password got %q", out.Reason)
	}
}

// ── the plan, and who is enforcing it ────────────────────────────────────────

// TestEnforcedByIsDerivedFromTheDriverThatWillHonourIt.
//
// `Plan.enforcedBy` decides whether a console and a portal may present a limit as
// a cap. The console cannot know it — a batch is redeemable anywhere in a site —
// so it is derived at grant time from the driver that is about to be asked.
func TestEnforcedByIsDerivedFromTheDriverThatWillHonourIt(t *testing.T) {
	ctx := context.Background()

	r := newRig(t, present(), nil)
	r.driver.hardQuota = true
	out := r.svc.Agree(ctx, r.session, true, 30)
	if out.Grant.Plan.EnforcedBy != EnforcedByNAS {
		t.Errorf("enforcedBy is %q behind a driver with hard quota, want %q", out.Grant.Plan.EnforcedBy, EnforcedByNAS)
	}

	r = newRig(t, present(), nil)
	r.driver.hardQuota = false
	out = r.svc.Agree(ctx, r.session, true, 30)
	if out.Grant.Plan.EnforcedBy != EnforcedBySampling {
		t.Errorf("enforcedBy is %q behind a driver that cannot enforce, want %q", out.Grant.Plan.EnforcedBy, EnforcedBySampling)
	}

	// A plan with nothing in it has nothing for anybody to enforce, which is a
	// different statement from a limit nobody is applying.
	r = newRig(t, present(), nil)
	r.creds.plan = Plan{}
	r.driver.hardQuota = true
	out = r.svc.Redeem(ctx, r.session, "ABCDEFGHJKMN")
	if out.Grant.Plan.EnforcedBy != EnforcedByNothing {
		t.Errorf("an unlimited plan reports %q, want %q", out.Grant.Plan.EnforcedBy, EnforcedByNothing)
	}
	if out.Grant.ExpiresAt != nil {
		t.Error("an unlimited plan produced an expiry")
	}
}

// ── state is read from the clock ─────────────────────────────────────────────

// TestGrantStateIsDerivedRatherThanStored.
//
// A stored `expired` is wrong for as long as it takes a timer to fire, and a
// timer that never fires — a restarted process, a crashed reaper — leaves a guest
// reading "connected" beside a timestamp that ran out an hour ago. Revocation is
// the opposite: it is an event and it is written when it happens.
func TestGrantStateIsDerivedRatherThanStored(t *testing.T) {
	end := time.Date(2026, 8, 26, 10, 0, 0, 0, time.UTC)
	g := Grant{ExpiresAt: &end}

	if got := g.StateAt(end.Add(-time.Minute)); got != StateConnected {
		t.Errorf("before expiry: %q", got)
	}
	if got := g.StateAt(end); got != StateExpired {
		t.Errorf("at the instant of expiry: %q, want %q — a plan that runs out at 10:00 is over at 10:00", got, StateExpired)
	}
	if got := g.StateAt(end.Add(time.Hour)); got != StateExpired {
		t.Errorf("after expiry: %q", got)
	}

	revoked := end.Add(-time.Hour)
	g.RevokedAt = &revoked
	if got := g.StateAt(end.Add(-time.Minute)); got != StateRevoked {
		t.Errorf("a revoked grant reads %q — revocation outranks the clock", got)
	}
}

// ── the pre-authentication rate limit ────────────────────────────────────────

// TestTheRateLimitRefuses_AndThenRecovers.
//
// Both halves, and the second is the one worth writing. A limiter whose recovery
// has never been exercised might be a permanent lockout, and locking a café's
// guests off the network for the rest of the day is a worse failure than the one
// this prevents. The clock is a parameter so the recovery can be watched rather
// than waited for.
func TestTheRateLimitRefuses_AndThenRecovers(t *testing.T) {
	at := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	lim := NewTokenBucket(3, 10*time.Second)

	for i := 0; i < 3; i++ {
		if !lim.Allow("auth:aa:bb", at) {
			t.Fatalf("refused attempt %d of a burst of 3", i+1)
		}
	}
	if lim.Allow("auth:aa:bb", at) {
		t.Fatal("a fourth attempt in the same instant was allowed")
	}
	// A different device is not affected. Keying on something shared would make
	// one laptop able to lock out a whole venue.
	if !lim.Allow("auth:cc:dd", at) {
		t.Error("one device's budget refused another device")
	}

	if lim.Allow("auth:aa:bb", at.Add(9*time.Second)) {
		t.Error("a token came back early")
	}
	if !lim.Allow("auth:aa:bb", at.Add(10*time.Second)) {
		t.Error("the bucket never refilled — this limiter is a permanent lockout")
	}
}

// TestTheRateLimitStopsAnAuthenticatingOperation.
//
// Wired end to end, because a limiter nothing consults is a limiter.
func TestTheRateLimitStopsAnAuthenticatingOperation(t *testing.T) {
	r := newRig(t, present(), nil)
	ports := r.svc.Ports()
	ports.Limiter = NewTokenBucket(1, time.Hour)
	svc, err := NewService(ServiceConfig{
		Ports: ports,
		Now:   func() time.Time { return r.now },
		NewID: func() (string, error) { return "grant-1", nil },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	ctx := context.Background()
	if out := svc.Login(ctx, r.session, "kim", "wrong"); out.Reason != ReasonBadCredentials {
		t.Fatalf("first attempt: %q", out.Reason)
	}
	out := svc.Login(ctx, r.session, "kim", "wrong")
	if out.Reason != ReasonGeneric {
		t.Errorf("second attempt got %q — the limiter is not in the path", out.Reason)
	}
}

// ── construction ─────────────────────────────────────────────────────────────

// TestAServiceWithNoDriverRefusesToExist.
//
// The tolerant version of a presence check — "if the driver is nil, skip it" —
// is a portal that authorizes any MAC anybody names. There is no configuration of
// this service that has a grant store and no driver.
func TestAServiceWithNoDriverRefusesToExist(t *testing.T) {
	store := NewMemoryStore()
	_, err := NewService(ServiceConfig{Ports: Ports{
		Sessions:    store,
		Grants:      store,
		Credentials: &fakeCredentials{},
		Sites:       fakeSites{},
	}})
	if err == nil {
		t.Fatal("a service with no driver was built — every grant it issues is unverified")
	}
	if !strings.Contains(err.Error(), "Drivers") {
		t.Errorf("the error does not name what is missing: %v", err)
	}
}

// TestReadingThePageDoesNotSpendTheBudgetForTypingACode.
//
// Two buckets, and this is why. `getPortalContext` calls the driver, so it has to
// be bounded — a guest reloading a page must not turn into unbounded HTTP requests
// to the venue's controller. But sharing one bucket with the authenticating
// operations means a handful of reloads spend the budget the guest needs to type
// the code they came in with, and the portal answers "something went wrong" to
// somebody who has done nothing wrong.
func TestReadingThePageDoesNotSpendTheBudgetForTypingACode(t *testing.T) {
	r := newRig(t, present(), nil)
	ports := r.svc.Ports()
	ports.Limiter = NewTokenBucket(2, time.Hour)
	svc, err := NewService(ServiceConfig{
		Ports: ports,
		Now:   func() time.Time { return r.now },
		NewID: func() (string, error) { return "grant-1", nil },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	ctx := context.Background()
	// Spend the read budget entirely.
	for i := 0; i < 4; i++ {
		svc.Look(ctx, r.session)
	}
	// The code still goes through.
	if out := svc.Redeem(ctx, r.session, "ABCDEFGHJKMN"); !out.Connected {
		t.Fatalf("a guest who reloaded the page could not redeem: %q", out.Reason)
	}
}

// TestTheReadPathIsBoundedAtAll.
//
// The other half: a budget that is never reached is not a budget. Reaching it
// answers `VerdictUnavailable` — this request did not determine presence, which is
// what happened — rather than a refusal, which would tell a guest their device is
// not on a network they are standing in.
func TestTheReadPathIsBoundedAtAll(t *testing.T) {
	r := newRig(t, present(), nil)
	ports := r.svc.Ports()
	ports.Limiter = NewTokenBucket(1, time.Hour)
	svc, err := NewService(ServiceConfig{
		Ports: ports,
		Now:   func() time.Time { return r.now },
	})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}

	// The read path spends only the connection source's budget — there is nothing
	// verified to key a device on before the presence check it pays for — and a
	// source is sized as `SourceShare` guests.
	ctx := context.Background()
	for i := 0; i < SourceShare; i++ {
		if v := svc.Look(ctx, r.session); v != VerdictProceed {
			t.Fatalf("look %d inside the source's burst was refused: %v", i+1, v)
		}
	}
	if v := svc.Look(ctx, r.session); v != VerdictUnavailable {
		t.Errorf("the read path is unbounded: look %d answered %v", SourceShare+1, v)
	}
}
