package portal

import (
	"context"
	"time"

	"github.com/kiennt048/foyer/internal/nas"
)

// The seam between the guest portal and everything that persists or talks to
// hardware.
//
// ── why these are declared here and not in internal/store ────────────────────
//
// The same reason `internal/api/ports.go` gives, and it is not a style
// preference: `internal/store` evolves independently of this package. A port
// declared by the side that needs it can be driven end to end against an
// in-memory implementation today — `memory.go`, in this package — and wired to a
// repository later by writing an adapter. Nothing in this package imports
// `internal/store`, and the schema these interfaces need is written down in
// `CONTRACT.md` so that the migration which lands behind them is answering a
// stated requirement rather than being reverse-engineered from Go.
//
// ── why the types are the portal's own and not the contract's ────────────────
//
// This is the opposite of the choice `internal/api/ports.go` made, and the
// difference is real rather than an inconsistency. There, the contract *is* the
// domain model: `Nas`, `Voucher` and `Session` are defined there first. Here
// the central noun — the grant — **is not in the contract at all.** `PortalStatus` is a projection of one, and a port typed on the
// projection could not carry the fields the projection deliberately omits: which
// AP, which NAS, which voucher, when it was revoked. So the grant is declared
// here and `internal/api` maps it onto `PortalStatus` at the edge, which is the
// only place the omission belongs.

// SessionStore holds the browser-to-MAC binding.
//
// There is no method that takes a MAC and returns a session. That is deliberate:
// a lookup by MAC is a lookup by a value the NAS supplies unsigned, and the one
// thing the portal must never do is resolve a browser's state from a parameter
// somebody typed. The token in the cookie is the only key.
type SessionStore interface {
	CreateSession(ctx context.Context, s Session) error
	// Session returns ErrNotFound for a token that does not resolve, has
	// expired, or never existed. A caller cannot tell those apart, and telling
	// them apart would say whether a token was ever valid.
	Session(ctx context.Context, tokenHash string) (Session, error)
	AttachGrant(ctx context.Context, tokenHash, grantID string) error
	// ForgetSession drops the binding. Called when a mid-flow MAC change is
	// detected: the browser gets a new session rather than keeping one whose
	// binding is now in dispute.
	ForgetSession(ctx context.Context, tokenHash string) error
}

// GrantStore holds what the venue decided.
type GrantStore interface {
	CreateGrant(ctx context.Context, g Grant) error
	Grant(ctx context.Context, id string) (Grant, error)
}

// Claim is what a successful voucher redemption returns.
//
// It carries the plan because the plan is on the *batch*, and the portal must
// not go and read the batch itself: the claim and the plan have to come out of
// the same statement or a batch edited between the two would grant a plan the
// voucher was not sold under.
type Claim struct {
	VoucherID string
	BatchID   string
	Plan      Plan
	// ExpiresAt is the voucher's own expiry after the claim, which for a
	// `from-first-use` batch is set *by* the claim. Nil means the plan's
	// duration is the only bound.
	ExpiresAt *time.Time
}

// Guest is a local account that authenticated.
type Guest struct {
	AccountID string
	Username  string
	Plan      Plan
}

// Credentials is the two checks the portal makes before it decides anything.
//
// ── ClaimVoucher is a claim, not a lookup ────────────────────────────────────
//
// It is named for what it does because the naming is the guarantee. §9 requires
// single use "enforced transactionally, not with a read-then-write", and an
// interface offering `FindVoucher` would invite exactly the read-then-write it
// forbids: two guests submitting the same code within the same millisecond both
// read `unused` and both get on. The implementation behind this method is one
// statement that both tests the state and changes it, and `CONTRACT.md` writes
// out the SQL it needs.
//
// ── AuthenticateGuest never returns a verifier ───────────────────────────────
//
// Same rule as `OperatorDirectory.Authenticate` and the same reason as
// `docs/rules/security.md` §2: an interface with a method that returns a
// password hash is an interface somebody calls. It takes the submitted password
// and answers with an account or with nothing.
type Credentials interface {
	ClaimVoucher(ctx context.Context, siteID, code, clientMAC string, at time.Time) (Claim, error)
	AuthenticateGuest(ctx context.Context, siteID, username, password string) (Guest, error)
}

// Placement is where a guest is standing, resolved from the access point the NAS
// named — and it is **three nouns rather than one**.
//
// `docs/rules/nas-drivers.md` and `docs/architecture.md` separate the controller,
// the RADIUS client and the access point because "access point" was one noun doing
// three jobs. This is that model at the portal's seam: the Foyer site is what the
// database keys on, the vendor site is what the controller keys on, and the
// controller is which one to ask. All three are resolved together, from one row,
// because resolving them separately is how they come to disagree.
//
// It carried the first two and not the third, and the cost was not only a repeated
// query: `Verify` and `Authorize` each resolved the controller for themselves, so a
// device confirmed present on one controller could be authorized on another.
type Placement struct {
	SiteID string
	NASID  string
	// VendorSiteID is the controller's own site identifier, which is what
	// `VerifyPresence` and `AuthorizeGuest` take — UniFi's `SiteID` field. It is
	// not the Foyer site id and conflating the two is how a driver ends up
	// looking a client up in the wrong place.
	VendorSiteID string
	// ControllerID names the controller that manages this access point, which is
	// what decides *which driver* answers. Empty means the access point is bound
	// to no controller — 00004 leaves that binding unset for a proxying
	// controller rather than guessing — and a driver seam handed one refuses
	// rather than picking a controller.
	ControllerID string

	// Accounting says whether `radacct` rows can be expected from devices here.
	// It is what makes `unenforced` sayable about a plan that states a limit —
	// see AccountingExpectation.
	Accounting AccountingExpectation
}

// AccountingExpectation is whether accounting will arrive for this placement.
//
// **It is the third input to `enforcement`, and without it the second value was
// always a guess.** `radacct` byte counters advance only on Interim-Update or Stop,
// and a NAS sends none unless the server asks for them in an Access-Accept. On the
// controller-API path there is no Access-Accept at all (`docs/rules/radius.md`),
// so rows exist only where an operator separately attached an accounting profile —
// and a portal reporting `foyer-sampled` there is promising a guest a cap it will
// never receive a byte towards.
//
// Three-valued rather than a bool, because *not measured* is not *not configured*.
// The measurement is `radius_clients.accounting_configured`, written by the NAS
// probe, and it is nullable for exactly this reason.
type AccountingExpectation int

const (
	// AccountingUnknown — nobody has probed this device. `foyer-sampled` is the
	// guess, and it is a guess to close at NAS registration rather than here:
	// answering `unenforced` for an unprobed device would tell an operator a
	// working quota is not being applied.
	AccountingUnknown AccountingExpectation = iota

	// AccountingArrives — a probe found accounting configured.
	AccountingArrives

	// AccountingAbsent — a probe found none. No Interim-Update will arrive, so
	// nothing is sampling and nothing is enforcing.
	AccountingAbsent
)

// SiteResolver answers where a guest is, from an access point MAC.
//
// The AP MAC arrives unsigned from the NAS, so this resolves a *claim* rather
// than a fact — the fact is what the driver confirms afterwards. An unknown AP
// is ErrNotFound and the guest is refused: a portal that fell back to a default
// site for an AP it has never heard of would be a portal that authorizes
// devices on networks it does not manage.
type SiteResolver interface {
	Placement(ctx context.Context, apMAC string) (Placement, error)
}

// Drivers is the hardware seam, and it is one interface with two methods because
// they are two halves of one obligation.
//
// `docs/rules/nas-drivers.md` splits `VendorProfile` (rendering a decision
// into RADIUS attributes) from `NASDriver` (out-of-band actions). Both methods
// here are the second kind: on the controller-API path there is no
// Access-Accept, so making a client captive and letting it through are both
// calls to somebody's controller.
type Drivers interface {
	// Verify is `NASDriver.VerifyPresence`, un-truncated.
	//
	// It returns `nas.Presence` rather than a bool because the bool cannot carry
	// the two answers that are not refusals: **already authorized** is a guest
	// who should be shown the connected page, and **unavailable** is the
	// venue's controller being down, which is not the guest's fault and must not
	// be rendered as though it were. A bool would collapse both into "device not
	// present".
	Verify(ctx context.Context, s Session) (nas.Presence, error)

	// Authorize lets the client through.
	//
	// It takes the grant because the plan is what the driver needs: where
	// `Capabilities.HardQuota` is true the NAS enforces the limits itself and
	// `radacct` becomes reporting rather than enforcement
	// (`docs/rules/radius.md`).
	//
	// **And it takes the placement, because a grant cannot carry one.** A grant
	// records what the venue decided, in Foyer's own terms: its `SiteID` is the
	// Foyer site, and handing that to a controller is exactly the conflation this
	// file warns about above. Passing the placement is what makes the vendor's
	// site and the controller explicit at the seam rather than something the
	// implementation goes and looks up for itself — which it did, from the AP MAC,
	// on every call, having already looked the same row up inside `Verify`.
	Authorize(ctx context.Context, p Placement, g Grant) error

	// HardQuota reports whether the driver for this placement enforces the plan
	// itself. It decides `Plan.EnforcedBy`, which decides whether the console
	// and the portal may present a limit as a cap.
	HardQuota(ctx context.Context, s Session) bool
}

// Authorizations records what happened when the network was asked to let a guest
// on. It is the channel a failed `AuthorizeGuest` had nowhere to be reported
// through.
//
// ── why this rather than the audit log ───────────────────────────────────────
//
// The obvious move is an `AuditKind`, and it is the wrong one. The audit log is a
// record of **what an operator did**: `AuditEntry` requires `actorId` and
// `actorUsername`, and its own description says it holds "what cannot be undone or
// what is security-relevant" among the changes people make. There is no operator
// here. A guest typed a voucher and a controller refused; filing that under an
// operator's log would need an actor to be invented, and `internal/api`'s invariant
// 10 forbids the near-enough kind that would have to be invented with it.
//
// The right surface is `listAuthAttempts`, which declares `AuthSource: driver` —
// *"the controller refusing after every check Foyer owns has passed, which is
// invisible from both of the other two sources"* — and `AuthEventKind:
// authorize-failed`. The contract describes this exact failure; this is its
// writer.
//
// ── why one method and not two ───────────────────────────────────────────────
//
// The operator's record and the guest's page are two readers of **one fact**, and an
// implementation that could write one without the other is an implementation that
// can make them disagree — a console showing `authorize-failed` beside a portal
// showing a green tick, which is worse than the silence this replaces. So `Record`
// owns both writes, and a SQL implementation does them in one transaction.
type Authorizations interface {
	// Record writes the outcome of the post-response authorization call: the
	// event an operator can find, and the grant's `Delivery` a guest's status
	// read renders.
	//
	// It is called after the response is on the wire, so its error reaches
	// nobody. An implementation that cannot write must say so somewhere a person
	// will look; `NewService`'s default says it to the process log.
	Record(ctx context.Context, a Authorization) error
}

// Authorization is one attempt to open the network for one grant.
//
// **Nothing here is a credential and nothing here is free text from a controller.**
// `Failure` is a classification this package assigns and `Status` is an integer the
// controller answered with. The controller's response body and the URL it was
// reached at are deliberately absent: a body is attacker-influenced text and a
// controller URL can carry an API key in its query string, and this object ends up
// in a log an operator reads months later (`docs/rules/security.md` §3 — redact
// secrets from logs).
type Authorization struct {
	GrantID   string
	SiteID    string
	NASID     string
	ClientMAC string
	APMAC     string

	Method Method
	// VoucherID and Username identify what was presented, never how. The
	// contract's `AuthAttempt.username` is "the identity offered, never the
	// credential", and this is the same field.
	VoucherID string
	Username  string

	At time.Time

	// Delivery is DeliveryOpen or DeliveryFailed. Never DeliveryUnknown: this
	// value is written because a call returned.
	Delivery Delivery

	// Attempts is how many times the controller was asked, including the one
	// that succeeded or the last one that failed. It is recorded rather than
	// implied because a retry nobody can see is a second way to be wrong about
	// what happened — see Service.deliver.
	Attempts int

	// Failure is empty on DeliveryOpen. See FailureKind.
	Failure FailureKind

	// Status is the HTTP status a controller answered with, or zero where
	// nothing answered. It is the difference between an expired API key and a
	// wrong vendor site id, which are two different people's jobs.
	Status int
}

// FailureKind is which of the two ways an authorization failed.
//
// The split is the same one `nas.MockFaults` draws and it exists for the same
// reason: a driver decides real behaviour on it. A controller that **answered and
// refused** has an opinion, and asking again gets the same opinion; a controller
// that **could not be reached** has no opinion yet, and asking again is the whole
// of the fix for the most common cause. See Service.deliver.
type FailureKind string

const (
	// FailureUnreachable — nothing answered. The controller is down, restarting,
	// or the route to it is gone.
	FailureUnreachable FailureKind = "unreachable"

	// FailureRefused — it answered and said no. `Authorization.Status` carries
	// what it said.
	FailureRefused FailureKind = "refused"
)

// Limiter bounds the pre-authentication state.
//
// §5: "Quota the pre-auth state: per-MAC byte cap, rate limit, time limit. There
// is no identity yet, so nothing else bounds it." The byte cap belongs to the
// walled garden and is a data-plane concern; the time limit is
// `Session.ExpiresAt`; **this is the rate limit**, and it is the one of the three
// that this package can enforce and must.
//
// It is a port rather than a concrete type because a deployment with more than
// one control-plane process needs a shared counter, and an in-process one would
// then permit N times the configured rate without anything saying so.
type Limiter interface {
	// Allow reports whether one more attempt may be made against this key, and
	// consumes the budget if so.
	Allow(key string, at time.Time) bool
}

// SharedLimiter is a Limiter whose budget is shared across processes.
//
// **A Limiter that does not implement this is assumed in-process**, and the
// assumption fails closed: `NewService` refuses to construct a service that has
// been told it is one of several replicas while its limiter cannot count across
// them. Two replicas behind one address grant **twice** the configured pre-auth
// rate, and §5 is explicit that the rate limit is one of only three things
// bounding a state that has no identity behind it. A limit wrong by a factor of
// the replica count is worse than a process that will not start, because the
// process that will not start says so.
//
// It is a separate interface rather than a method on `Limiter` so that adding it
// does not break the implementations that already exist — `TokenBucket` here and
// the adapter in `internal/apistore`, both of which are in-process and both of
// which are correctly refused.
//
// Declaring it is a claim about the counter, not about the type. A limiter that
// returned true while keeping its buckets in a map would defeat the check
// entirely, which is why `TokenBucket` implements it and answers **false**: the
// honest answer stated out loud is worth more than the absence of a method.
type SharedLimiter interface {
	Limiter
	SharedAcrossProcesses() bool
}

// limiterIsShared reads the claim, defaulting to *not shared*.
func limiterIsShared(l Limiter) bool {
	s, ok := l.(SharedLimiter)
	return ok && s.SharedAcrossProcesses()
}

// Ports is the whole seam in one value. A nil member is not a panic waiting to
// happen: `NewService` reports which are missing rather than discovering it in a
// handler.
type Ports struct {
	Sessions    SessionStore
	Grants      GrantStore
	Credentials Credentials
	Sites       SiteResolver
	Drivers     Drivers
	Limiter     Limiter

	// Authorizations is where a failed `AuthorizeGuest` is reported. Nil gets
	// `NewService`'s log-only recorder rather than nothing — the failure is
	// never silent, but only a wired port makes it queryable by an operator and
	// visible to the guest. See Authorizations.
	Authorizations Authorizations

	// Addresses is how the RFC 8908 endpoint finds a device that carries no
	// cookie. Nil gets `NewService`'s own in-process book — which is only right
	// when one `Service` serves both the front door and the endpoint. `cmd/foyer`
	// builds two (the API server's and the front door's), so it wires one book
	// into both. See address.go.
	Addresses AddressBook
	// PhoneCodes is phone sign-in. Nil means the method is not offered anywhere,
	// and both of its operations answer `404` — see phone.go.
	PhoneCodes PhoneCodes
}
