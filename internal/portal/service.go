package portal

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/ratelimit"
)

// The guest portal's decisions, with no HTTP in them.
//
// The five contract operations are handlers in `internal/api` because that is
// where the generated `StrictServerInterface` lives. What they must *decide* is
// here, for two reasons that are not tidiness:
//
//   - the ordering rule below — the document before the authorization — is easy
//     to state and easy to lose in a refactor, so it is expressed as a value the
//     handler cannot accidentally run early;
//   - `docs/rules/security.md` §4 is a property of this package's behaviour,
//     not of a route, and a test that drives it through HTTP proves the route
//     as well as the rule while a test that drives it here proves the rule
//     alone. Both exist; this is what makes the second one possible.

// Service holds the ports and the clock.
type Service struct {
	ports Ports
	now   func() time.Time
	newID func() (string, error)
	log   *slog.Logger

	sessionLifetime time.Duration
}

// ServiceConfig is what NewService needs. Everything but Ports has a default.
type ServiceConfig struct {
	Ports Ports

	// Now and NewID exist so a test can make a grant reproducible without the
	// service knowing it is under test.
	Now   func() time.Time
	NewID func() (string, error)

	// SessionLifetime is the pre-authentication time limit §5 requires. It is
	// short on purpose: a pre-auth session has no identity behind it, and the
	// only thing bounding how many can exist is how long each one lives.
	SessionLifetime time.Duration

	// Replicas is how many control-plane processes this deployment runs behind
	// one address. Zero or one means what it says.
	//
	// **It exists so the pre-auth rate limit can refuse to be wrong.** `Limiter`
	// is in-process unless it says otherwise, so N replicas grant N times the
	// configured rate and nothing about the running system would say so — the
	// venue's controller takes N times the load, and the one bound §5 puts on a
	// state with no identity behind it is quietly N times looser. `NewService`
	// refuses that combination. See SharedLimiter.
	Replicas int

	// Logger is where the two things that cannot reach a client are said: a
	// failed authorization with no `Authorizations` port wired, and the
	// single-replica assumption this service is running under. Defaults to
	// `slog.Default()`.
	Logger *slog.Logger
}

// DefaultSessionLifetime bounds the pre-authentication state.
//
// Thirty minutes, and the number comes from the environment rather than from
// taste: the macOS Captive Network Assistant is a window somebody has open while
// they read terms and type a code off a receipt, and it takes its data store
// with it when it closes. A guest who has not finished in half an hour has
// walked away, and their row should not outlive them.
const DefaultSessionLifetime = 30 * time.Minute

// NewService reports what is missing rather than panicking in a handler.
func NewService(cfg ServiceConfig) (*Service, error) {
	var missing []string
	if cfg.Ports.Sessions == nil {
		missing = append(missing, "Sessions")
	}
	if cfg.Ports.Grants == nil {
		missing = append(missing, "Grants")
	}
	if cfg.Ports.Credentials == nil {
		missing = append(missing, "Credentials")
	}
	if cfg.Ports.Sites == nil {
		missing = append(missing, "Sites")
	}
	// Drivers has no default and never will.
	//
	// A nil driver seam is the one misconfiguration that must not be tolerated
	// here, because the tolerant version of this code — "if the driver is nil,
	// skip the check" — is a portal that authorizes any MAC anybody names, which is
	// what a `VerifyPresence` call guarded by `if driver != nil` amounts to. It is
	// refused at construction instead.
	if cfg.Ports.Drivers == nil {
		missing = append(missing, "Drivers")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("portal: these ports are required and are nil: %s", strings.Join(missing, ", "))
	}

	// The pre-authentication rate limit is one of the three bounds §5 puts on a
	// state that has no identity behind it, and an in-process one is only the
	// configured rate while exactly one process enforces it. A deployment that
	// has told us it runs several is refused rather than silently granted the
	// multiple — a limit wrong by a factor of the replica count is worse than a
	// process that will not start, because the process that will not start says
	// which limit and why.
	if cfg.Replicas > 1 && !limiterIsShared(cfg.Ports.Limiter) {
		return nil, fmt.Errorf(
			"portal: %d control-plane replicas were declared and the pre-authentication rate limiter is in-process, "+
				"which would grant %d times the configured rate; wire a Limiter that implements SharedLimiter",
			cfg.Replicas, cfg.Replicas)
	}

	s := &Service{
		ports:           cfg.Ports,
		now:             cfg.Now,
		newID:           cfg.NewID,
		log:             cfg.Logger,
		sessionLifetime: cfg.SessionLifetime,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.newID == nil {
		s.newID = newGrantID
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.sessionLifetime <= 0 {
		s.sessionLifetime = DefaultSessionLifetime
	}
	// A failed authorization must not be silent, and nil is the shape it was
	// silent in. The log-only recorder reaches one reader out of three and says
	// so; see logAuthorizations.
	if s.ports.Authorizations == nil {
		s.ports.Authorizations = logAuthorizations{log: s.log}
		s.log.Warn("portal: no Authorizations port is wired, so a failed authorization " +
			"reaches the process log only — not listAuthAttempts, and not the guest's connected page")
	}
	if s.ports.Addresses == nil {
		s.ports.Addresses = NewAddressBook()
	}
	if !limiterIsShared(s.ports.Limiter) {
		// Said out loud even where it is correct. The assumption is invisible
		// from outside the process and it is the thing that stops being true the
		// day somebody scales the deployment, which is a day nobody revisits
		// this file on.
		s.log.Info("portal: the pre-authentication rate limit is in-process; " +
			"the configured rate holds only while exactly one control-plane process is running")
	}
	return s, nil
}

// Ports exposes the seam, for a handler that needs the session store directly.
func (s *Service) Ports() Ports { return s.ports }

// Now is the service's clock, so a caller renders timestamps from the same one.
func (s *Service) Now() time.Time { return s.now() }

// ── the outcome of an authenticating operation ───────────────────────────────

// Outcome is what one of the three authenticating operations decided.
//
// **`Authorize` is the ordering rule, made into a value.** The contract says the
// success document is sent in full *before* the NAS is asked to authorize the
// client, because Android destroys the login WebView the moment the network
// validates and anything written after that may never be seen. A handler that
// held a driver call inline would satisfy that rule only by remembering to; a
// handler holding a closure it must pass to the response writer satisfies it by
// construction, and `handlers_portal.go` is where the closure is run.
type Outcome struct {
	// Connected: send the guest to the connected page.
	Connected bool

	// Grant is what was created, or nil. It is nil on `Connected` when the
	// driver reported the device was **already authorized**: that guest is
	// online and no voucher was claimed for them.
	Grant *Grant

	// Authorize is the out-of-band driver call. Nil unless Grant is non-nil.
	Authorize func(ctx context.Context) error

	// NotOffered: this venue does not offer the method. `acceptTerms` only, and
	// it answers `404` rather than granting — an endpoint that works while its
	// button is hidden is an endpoint somebody finds.
	NotOffered bool

	// Reason is why the guest is not connected. Meaningless unless Connected
	// and NotOffered are both false.
	Reason Reason
}

func refuse(r Reason) Outcome { return Outcome{Reason: r} }
func alreadyOn() Outcome      { return Outcome{Connected: true} }
func notOffered() Outcome     { return Outcome{NotOffered: true} }
func granted(g *Grant, f func(context.Context) error) Outcome {
	return Outcome{Connected: true, Grant: g, Authorize: f}
}

// ── presence ─────────────────────────────────────────────────────────────────

// Presence runs the check §4 makes mandatory and classifies the answer.
//
// Every path that can create a grant goes through here. There is no second call
// site and no flag that skips it.
func (s *Service) Presence(ctx context.Context, sess Session) Verdict {
	v, _ := s.presence(ctx, sess)
	return v
}

// presence keeps both halves of one driver answer.
//
// The verdict is what the portal does; the reason is what the guest is told, and
// since `Explain` reads the outcome the two are no longer derivable from each
// other — two answers that both refuse get different sentences. They come back
// together because they are read off **one** `nas.Presence`: recovering the
// second half by asking again would be a second HTTP call to the venue's
// controller on every refusal, and a second call can answer differently.
func (s *Service) presence(ctx context.Context, sess Session) (Verdict, Reason) {
	p, err := s.ports.Drivers.Verify(ctx, sess)
	return Classify(p, err), Explain(p, err)
}

// Look is `getPortalContext`'s presence check, and it has **its own rate-limit
// budget**.
//
// Two buckets rather than one, because they bound different things. The
// authenticating operations are bounded against guessing a voucher or a password;
// this is bounded against a guest reloading a page turning into an HTTP request to
// the venue's controller. Sharing a bucket would let a handful of page reloads
// spend the budget a guest needs to actually type their code.
//
// Reaching the limit answers `VerdictUnavailable` — this request did not
// determine presence, which is exactly what happened — so the portal renders the
// venue's page and nothing is granted. The alternative, refusing, would tell a
// guest their device is not on a network they are standing in.
func (s *Service) Look(ctx context.Context, sess Session) Verdict {
	if !s.allowSource("look", sess) {
		return VerdictUnavailable
	}
	return s.Presence(ctx, sess)
}

// ── the three authenticating operations ──────────────────────────────────────

// Redeem claims a voucher and grants.
//
// The order is presence, then shape, then claim, and the first two are before
// the claim on purpose. A device that is not on the network must not consume a
// single-use code, and a submission that cannot be a voucher must not reach a
// lookup at all.
func (s *Service) Redeem(ctx context.Context, sess Session, submitted string) Outcome {
	if !s.allowSource("auth", sess) {
		return refuse(ReasonGeneric)
	}

	switch v, why := s.presence(ctx, sess); {
	case v == VerdictAlreadyOn:
		return alreadyOn()
	case !v.MayGrant():
		return refuse(why)
	}
	// Only now, with the controller's word that this MAC is on the network, does
	// it get a budget of its own (see KeyDevice).
	if !s.allowDevice("auth", sess) {
		return refuse(ReasonGeneric)
	}

	if !CodeShaped(submitted) {
		return refuse(ReasonShape)
	}

	now := s.now()
	claim, err := s.ports.Credentials.ClaimVoucher(ctx, sess.SiteID, NormaliseCode(submitted), sess.ClientMAC, now)
	if err != nil {
		return refuse(voucherReason(err))
	}

	g := Grant{
		SiteID:    sess.SiteID,
		ClientMAC: sess.ClientMAC,
		APMAC:     sess.APMAC,
		NASID:     sess.NASID,
		Method:    MethodVoucher,
		VoucherID: claim.VoucherID,
		Plan:      claim.Plan,
		GrantedAt: now,
		ExpiresAt: expiry(now, claim.Plan, claim.ExpiresAt),
	}
	return s.issue(ctx, sess, g)
}

// Agree is click-through: the consent is the whole of the authentication.
//
// Which makes `VerifyPresence` the only thing standing between an arbitrary MAC
// and a grant. Every other method has a credential behind it; this one has a
// button, so a check that was already mandatory is load-bearing on its own here.
//
// `minutes` comes from the venue's settings and is never defaulted. A mode with
// no credential and no duration is unmetered public internet handed out on one
// tap, which is a decision a venue makes deliberately or not at all.
func (s *Service) Agree(ctx context.Context, sess Session, offered bool, minutes int) Outcome {
	if !offered || minutes < 1 {
		return notOffered()
	}
	if !s.allowSource("auth", sess) {
		return refuse(ReasonGeneric)
	}

	switch v, why := s.presence(ctx, sess); {
	case v == VerdictAlreadyOn:
		return alreadyOn()
	case !v.MayGrant():
		return refuse(why)
	}
	// Only now, with the controller's word that this MAC is on the network, does
	// it get a budget of its own (see KeyDevice).
	if !s.allowDevice("auth", sess) {
		return refuse(ReasonGeneric)
	}

	now := s.now()
	plan := Plan{DurationMinutes: &minutes}
	g := Grant{
		SiteID:    sess.SiteID,
		ClientMAC: sess.ClientMAC,
		APMAC:     sess.APMAC,
		NASID:     sess.NASID,
		Method:    MethodClickThrough,
		Plan:      plan,
		GrantedAt: now,
		ExpiresAt: expiry(now, plan, nil),
	}
	return s.issue(ctx, sess, g)
}

// Login authenticates a local account and grants.
func (s *Service) Login(ctx context.Context, sess Session, username, password string) Outcome {
	if !s.allowSource("auth", sess) {
		return refuse(ReasonGeneric)
	}

	switch v, why := s.presence(ctx, sess); {
	case v == VerdictAlreadyOn:
		return alreadyOn()
	case !v.MayGrant():
		return refuse(why)
	}
	// Only now, with the controller's word that this MAC is on the network, does
	// it get a budget of its own (see KeyDevice).
	if !s.allowDevice("auth", sess) {
		return refuse(ReasonGeneric)
	}

	guest, err := s.ports.Credentials.AuthenticateGuest(ctx, sess.SiteID,
		strings.ToLower(strings.TrimSpace(username)), password)
	if err != nil {
		switch {
		case errors.Is(err, ErrAccountDisabled):
			return refuse(ReasonDisabled)
		case errors.Is(err, ErrBadCredentials):
			return refuse(ReasonBadCredentials)
		default:
			return refuse(ReasonGeneric)
		}
	}

	now := s.now()
	g := Grant{
		SiteID:    sess.SiteID,
		ClientMAC: sess.ClientMAC,
		APMAC:     sess.APMAC,
		NASID:     sess.NASID,
		Method:    MethodAccount,
		Username:  guest.Username,
		Plan:      guest.Plan,
		GrantedAt: now,
		ExpiresAt: expiry(now, guest.Plan, nil),
	}
	return s.issue(ctx, sess, g)
}

// Status is the grant this browser holds.
//
// It resolves through the session's `GrantID` and never through the MAC. A
// lookup by MAC would answer for whatever grant most recently mentioned that
// address, which on a network where addresses are per-SSID pseudonyms is a way
// to read somebody else's plan by claiming their MAC.
func (s *Service) Status(ctx context.Context, sess Session) (Grant, State, error) {
	if sess.GrantID == "" {
		return Grant{}, "", ErrNotFound
	}
	g, err := s.ports.Grants.Grant(ctx, sess.GrantID)
	if err != nil {
		return Grant{}, "", err
	}
	// A grant that has ended is a fact the guest is owed, so it is answered
	// rather than hidden behind "not connected". The portal decides what to say
	// about it — a guest whose voucher was revoked while they were reading needs
	// to know that, not a green tick.
	return g, g.StateAt(s.now()), nil
}

// ── the parts the three share ────────────────────────────────────────────────

// issue records the grant, binds it to this browser, and hands back the driver
// call for the handler to run after the response.
func (s *Service) issue(ctx context.Context, sess Session, g Grant) Outcome {
	id, err := s.newID()
	if err != nil {
		return refuse(ReasonGeneric)
	}
	g.ID = id
	g.Plan.EnforcedBy = enforcement(g.Plan, s.ports.Drivers.HardQuota(ctx, sess), sess.Accounting)

	if err := s.ports.Grants.CreateGrant(ctx, g); err != nil {
		return refuse(ReasonGeneric)
	}
	if err := s.ports.Sessions.AttachGrant(ctx, sess.TokenHash, g.ID); err != nil {
		// The grant exists and the browser cannot find it again. The guest is
		// still let on — the network is what they came for — and the connected
		// page will answer `no-grant`, which is a worse page than it should be
		// and is not a reason to refuse the network.
		_ = err
	}
	// And to the addresses this browser was seen from, so the operating system's
	// RFC 8908 query finds the grant after the thirty-minute session row that
	// earned it has expired.
	s.ports.Addresses.AttachGrant(sess.TokenHash, g.ID, s.now())

	grant := g
	// The placement resolved at the front door, carried rather than looked up
	// again: the driver takes the *vendor's* site id and the grant carries
	// Foyer's, and the controller that confirmed this device present is the one
	// that has to be asked to let it on.
	place := sess.Placement()
	return granted(&grant, func(after context.Context) error {
		return s.deliver(after, place, grant)
	})
}

// enforcement decides who is applying the plan's limits. Recorded on the grant;
// see Enforcement.
//
// **The third value has to be derivable here.** A plan
// with limits behind a driver that cannot enforce them is `foyer-sampled` only if
// accounting actually arrives: `radacct` byte counters advance on Interim-Update or
// Stop and a NAS sends neither unless the server asks in an Access-Accept, and on
// the controller-API path there is no Access-Accept at all, so rows exist only where
// an operator separately attached an accounting profile (`docs/rules/radius.md`).
// Without the fact, the portal would report `foyer-sampled` where the truth is
// `unenforced` — telling a guest their 500 MB is being sampled every five minutes
// on a device from which no counter will ever arrive.
//
// The fact is a NAS measurement, `radius_clients.accounting_configured`, and it
// travels on the placement. It is three-valued because *not measured* is not *not
// configured*: an unprobed device stays `foyer-sampled`, which is the guess
// `radius.md` asks to be closed at registration rather than papered over here.
//
// Accounting is irrelevant where the NAS enforces the plan itself. There `radacct`
// is reporting rather than enforcement, so its absence costs a figure and not a cap.
func enforcement(p Plan, hardQuota bool, accounting AccountingExpectation) Enforcement {
	switch {
	case !p.Limited():
		return EnforcedByNothing
	case hardQuota:
		return EnforcedByNAS
	case accounting == AccountingAbsent:
		// A limit stated and nothing on the network applying it. The console and
		// the portal must not present it as a cap.
		return EnforcedByNothing
	default:
		return EnforcedBySampling
	}
}

// expiry is when the grant ends.
//
// The voucher's own expiry wins when it is sooner, which is what makes a
// `from-first-use` batch mean what it says: the plan's duration is how long the
// guest gets, and the voucher's expiry is when the venue stops honouring the
// code at all.
func expiry(now time.Time, p Plan, voucherExpiry *time.Time) *time.Time {
	var end *time.Time
	if p.DurationMinutes != nil {
		t := now.Add(time.Duration(*p.DurationMinutes) * time.Minute)
		end = &t
	}
	if voucherExpiry != nil && (end == nil || voucherExpiry.Before(*end)) {
		t := *voucherExpiry
		end = &t
	}
	return end
}

// allowSource is the pre-authentication rate limit **before** anything has been
// verified, and it is keyed on the one thing the caller cannot simply type: the
// connection source (`Session.Source`, the TCP peer). A flood of claimed MACs from
// one laptop is one bucket, sized as a flood bound, and it is what bounds the
// controller calls that presence checks cost. See KeySource.
//
// A nil Limiter permits everything, which is the correct default for a unit test
// and the wrong one for a deployment — `internal/api` supplies one.
func (s *Service) allowSource(bucket string, sess Session) bool {
	if s.ports.Limiter == nil {
		return true
	}
	return s.ports.Limiter.Allow(KeySource+bucket+":"+ratelimit.ClientKey(sess.Source), s.now())
}

// allowDevice is the per-guest budget, spent only **after** `VerifyPresence` has
// confirmed the MAC — so a MAC nobody vouched for never occupies a bucket — and
// keyed on the MAC together with its source, so that a stranger claiming a present
// guest's MAC spends their own budget rather than the guest's. See KeyDevice.
func (s *Service) allowDevice(bucket string, sess Session) bool {
	if s.ports.Limiter == nil {
		return true
	}
	return s.ports.Limiter.Allow(KeyDevice+bucket+":"+sess.ClientMAC+"|"+ratelimit.ClientKey(sess.Source), s.now())
}

// voucherReason maps a claim failure onto copy the portal has.
//
// Anything unrecognised becomes `generic` rather than `not-found`. A store that
// errored is not a guest who mistyped, and telling them it is sends somebody to
// check twelve characters that were never the problem.
func voucherReason(err error) Reason {
	switch {
	case errors.Is(err, ErrVoucherUnknown), errors.Is(err, ErrNotFound):
		return ReasonNotFound
	case errors.Is(err, ErrVoucherUsed):
		return ReasonAlreadyUsed
	case errors.Is(err, ErrVoucherExpired):
		return ReasonExpired
	case errors.Is(err, ErrVoucherRevoked):
		return ReasonRevoked
	default:
		return ReasonGeneric
	}
}
