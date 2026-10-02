package apistore

import (
	"context"
	"errors"
	"time"

	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/identity/otp"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/portal"
	"github.com/kiennt048/foyer/internal/store"
)

// The guest portal's six ports, over PostgreSQL and the real vendor drivers.
//
// ── why this is a type of its own ────────────────────────────────────────────
//
// `Adapter` satisfies the console's fourteen ports by being the same value
// fourteen times, because those interfaces are narrow views of one database. The
// guest surface is not another view of the console: it is a second listener,
// reachable pre-authentication by anyone within radio range, and
// `docs/rules/security.md` §7 separates them for that reason. A separate type
// is what makes "the guest ports" a thing a reader can point at, and it costs one
// struct.
//
// ── what it deliberately cannot do ───────────────────────────────────────────
//
// There is no method here that takes a MAC and returns a session, because
// `portal.SessionStore` has none and the schema has no index that would make one
// cheap. §4 is not a check somewhere in this file; it is the absence of a lookup.
type portalPorts struct {
	a       *Adapter
	limiter portal.Limiter
}

var (
	_ portal.SessionStore   = (*portalPorts)(nil)
	_ portal.GrantStore     = (*portalPorts)(nil)
	_ portal.Credentials    = (*portalPorts)(nil)
	_ portal.SiteResolver   = (*portalPorts)(nil)
	_ portal.Drivers        = (*portalPorts)(nil)
	_ portal.Limiter        = (*portalPorts)(nil)
	_ portal.Authorizations = (*portalPorts)(nil)
)

// PortalPorts hands the guest seam to `api.New`.
//
// **All of it or none of it**, which is what `api.New` enforces: a grant store
// with no driver seam is a portal recording grants for devices nothing confirmed.
// Returning the whole struct rather than letting a caller assemble one is how that
// stays true from this side as well.
func (a *Adapter) PortalPorts() portal.Ports {
	p := &portalPorts{a: a, limiter: a.limiter}
	return portal.Ports{
		Sessions:    p,
		Grants:      p,
		Credentials: p,
		Sites:       p,
		Drivers:     p,
		Limiter:     p,
		// **The one that is easy to leave out.** A nil `Authorizations` gets
		// `portal.NewService`'s log-only recorder, which is not nothing but is one
		// reader out of three: it cannot write `listAuthAttempts`, so the console
		// shows an operator nothing, and it cannot set the grant's `Delivery`, so
		// the guest whose controller refused still reads the connected page -- the
		// reader would exist and production would supply no writer.
		Authorizations: p,
		PhoneCodes:     otp.PortalCodes(a.phone),
		// The RFC 8908 book in PostgreSQL, so a restart or a second process answers
		// an online device from the same entry (portaladdresses.go).
		Addresses: a.addresses,
	}
}

// ── the browser-to-MAC binding ───────────────────────────────────────────────

func (p *portalPorts) CreateSession(ctx context.Context, s portal.Session) error {
	row := store.PortalSession{
		TokenHash:    s.TokenHash,
		SiteID:       s.SiteID,
		NASID:        nilIfEmpty(s.NASID),
		VendorSiteID: nilIfEmpty(s.VendorSiteID),
		ControllerID: nilIfEmpty(s.ControllerID),
		ClientMAC:    s.ClientMAC,
		APMAC:        nilIfEmpty(s.APMAC),
		SSID:         nilIfEmpty(s.SSID),
		OriginalURL:  nilIfEmpty(s.OriginalURL),
		ExpiresAt:    s.ExpiresAt,
	}
	return portalErr(p.a.st.Grants().CreateSession(ctx, &row))
}

// Session answers ErrNotFound for a token that does not resolve, has expired, or
// never existed.
//
// The three are one answer, and the expiry is in the repository's WHERE clause
// rather than checked here, so a caller cannot tell them apart even by timing the
// difference between a read and a read-plus-comparison.
func (p *portalPorts) Session(ctx context.Context, tokenHash string) (portal.Session, error) {
	row, err := p.a.st.Grants().Session(ctx, tokenHash, p.a.now())
	if err != nil {
		return portal.Session{}, portalErr(err)
	}
	return portal.Session{
		TokenHash:    row.TokenHash,
		SiteID:       row.SiteID,
		NASID:        deref(row.NASID),
		VendorSiteID: deref(row.VendorSiteID),
		ControllerID: deref(row.ControllerID),
		Accounting:   accountingExpectation(row.AccountingConfigured),
		ClientMAC:    row.ClientMAC,
		APMAC:        deref(row.APMAC),
		SSID:         deref(row.SSID),
		OriginalURL:  deref(row.OriginalURL),
		GrantID:      deref(row.GrantID),
		CreatedAt:    row.CreatedAt,
		ExpiresAt:    row.ExpiresAt,
	}, nil
}

func (p *portalPorts) AttachGrant(ctx context.Context, tokenHash, grantID string) error {
	return portalErr(p.a.st.Grants().AttachGrant(ctx, tokenHash, grantID))
}

func (p *portalPorts) ForgetSession(ctx context.Context, tokenHash string) error {
	return portalErr(p.a.st.Grants().ForgetSession(ctx, tokenHash))
}

// ── what the venue decided ───────────────────────────────────────────────────

// CreateGrant writes the decision.
//
// The account path costs one extra read, and the reason is a gap rather than a
// design: `portal.Grant` carries a **username** and `portal_grants` carries an
// **account id**, which its own CHECK constraint requires. `portal.Guest` has the
// id and `Service.Login` drops it on the way to the grant, so this resolves it
// again. The narrow fix is a field on `portal.Grant`.
func (p *portalPorts) CreateGrant(ctx context.Context, g portal.Grant) error {
	row := store.PortalGrant{
		ID:        g.ID,
		SiteID:    g.SiteID,
		NASID:     nilIfEmpty(g.NASID),
		ClientMAC: g.ClientMAC,
		APMAC:     nilIfEmpty(g.APMAC),
		Method:    string(g.Method),
		VoucherID: nilIfEmpty(g.VoucherID),
		Phone:     nilIfEmpty(g.Phone),
		Plan:      planFromPortal(g.Plan),
		GrantedAt: g.GrantedAt,
		ExpiresAt: g.ExpiresAt,
		RevokedAt: g.RevokedAt,
	}
	if g.Method == portal.MethodAccount {
		id, err := p.a.st.Grants().AccountIDForUsername(ctx, g.SiteID, g.Username)
		if err != nil {
			return portalErr(err)
		}
		row.AccountID = &id
	}
	return portalErr(p.a.st.Grants().CreateGrant(ctx, &row))
}

func (p *portalPorts) Grant(ctx context.Context, id string) (portal.Grant, error) {
	row, err := p.a.st.Grants().Grant(ctx, id)
	if err != nil {
		return portal.Grant{}, portalErr(err)
	}
	out := grantOut(*row)
	// The username rather than the id, because that is the field `portal.Grant`
	// has. A grant whose account row was deleted keeps its method and its plan and
	// loses the name, which is the honest answer: the identity is gone.
	if row.AccountID != nil {
		if acc, err := p.a.st.Accounts().Get(ctx, *row.AccountID); err == nil {
			out.Username = acc.Username
		}
	}
	return out, nil
}

// grantOut is a stored grant as the portal's type, everything but the username —
// which `Grant` looks up and the grant export joins, so one conversion serves both.
func grantOut(row store.PortalGrant) portal.Grant {
	plan := planToPortal(row.Plan)
	// **`EnforcedBy` is put back here and nowhere else.** `planToPortal` drops it
	// on purpose, because the value on a batch or an account was decided when that
	// row was created and the grant's is derived at grant time from the driver that
	// will actually be asked to honour it. Reading a *grant* is the one direction
	// where the stored value is the answer — it is a fact about the moment this
	// guest was let on. Dropping it here reported `unenforced` beside a plan with
	// four limits in it, which is the portal telling a guest nothing is capping
	// them while the NAS caps them.
	plan.EnforcedBy = portal.Enforcement(row.Plan.EnforcedBy)

	out := portal.Grant{
		ID:        row.ID,
		SiteID:    row.SiteID,
		ClientMAC: row.ClientMAC,
		APMAC:     deref(row.APMAC),
		NASID:     deref(row.NASID),
		Method:    portal.Method(row.Method),
		VoucherID: deref(row.VoucherID),
		Phone:     deref(row.Phone),
		Plan:      plan,
		GrantedAt: row.GrantedAt,
		ExpiresAt: row.ExpiresAt,
		RevokedAt: row.RevokedAt,
	}
	// A NULL column is `DeliveryUnknown`, which reads as `connected`. That is the
	// answer a grant one millisecond old gets as well, and the two must not be
	// distinguishable: the redirect is flushed before the controller is asked, so
	// every successful redemption spends one round trip looking exactly like this.
	if row.Delivery != nil {
		out.Delivery = portal.Delivery(*row.Delivery)
	}
	return out
}

// ── what happened when the network was asked ─────────────────────────────────

// Record writes the operator's event and the guest's `Delivery`, in one
// transaction.
//
// **One method because they are two readers of one fact.** An implementation that
// could write one without the other could show the console `authorize-failed`
// beside a portal showing a green tick, which `internal/portal/CONTRACT.md`
// invariant 10 judges worse than the silence it replaces. The transaction is the
// repository's; this adapter's only job is to keep the two halves in the one call
// that owns them.
//
// It is called after the response is on the wire, under a context of its own, so
// the error it returns reaches nobody but `Service.record`'s logger. That is why
// the store's refusal has to be a refusal of **both** writes rather than a partial
// one nobody is watching.
func (p *portalPorts) Record(ctx context.Context, a portal.Authorization) error {
	e := store.AuthorizationEvent{
		SiteID:    a.SiteID,
		GrantID:   a.GrantID,
		NASID:     nilIfEmpty(a.NASID),
		ClientMAC: a.ClientMAC,
		APMAC:     nilIfEmpty(a.APMAC),
		Method:    string(a.Method),
		VoucherID: nilIfEmpty(a.VoucherID),
		Username:  nilIfEmpty(a.Username),
		At:        a.At,
		Delivery:  string(a.Delivery),
		Attempts:  a.Attempts,
		Failure:   nilIfEmpty(string(a.Failure)),
	}
	if e.At.IsZero() {
		e.At = p.a.now()
	}
	if e.Attempts < 1 {
		// The column requires at least one, and it is right to: this row exists
		// because a call returned, so the controller was asked at least once. A
		// caller that left the field zero is reporting a count it did not keep,
		// and refusing the write would lose the outcome over a bookkeeping field.
		e.Attempts = 1
	}
	// A status is something that was **answered**. Nothing answered an unreachable
	// controller, and `classify` reports zero there — a number stored beside
	// `unreachable` would be invented from a transport error, which is what the
	// table's own CHECK refuses.
	if a.Status > 0 && a.Failure == portal.FailureRefused {
		e.Status = ptr(a.Status)
	}
	return portalErr(p.a.st.Grants().RecordAuthorization(ctx, &e))
}

// ── credentials ──────────────────────────────────────────────────────────────

// ClaimVoucher is a claim, not a lookup: one UPDATE that tests the state and
// changes it, joined to the batch so the plan comes out of the same statement.
//
// Zero rows means somebody else won, or the code was never claimable, and the two
// are indistinguishable from the claim itself. **Which sentence the guest reads is
// decided by a second, read-only query** that changes nothing and gates nothing —
// see `store.GrantRepo.VoucherStateFor`.
func (p *portalPorts) ClaimVoucher(ctx context.Context, siteID, code, clientMAC string, at time.Time) (portal.Claim, error) {
	claim, err := p.a.st.Grants().ClaimVoucher(ctx, siteID, code, clientMAC, at)
	switch {
	case err == nil:
		return portal.Claim{
			VoucherID: claim.VoucherID,
			BatchID:   claim.BatchID,
			Plan:      planToPortal(claim.Plan),
			ExpiresAt: claim.ExpiresAt,
		}, nil
	case errors.Is(err, store.ErrNotFound):
		return portal.Claim{}, p.whyNotClaimable(ctx, siteID, code, at)
	default:
		// A store that errored is not a guest who mistyped. The service turns
		// anything unrecognised into `generic`, which sends somebody to the
		// counter rather than to check twelve characters that were never wrong.
		return portal.Claim{}, err
	}
}

// whyNotClaimable chooses the sentence.
//
// Four errors rather than one, because "check each character" and "ask staff for
// another one" are different instructions. A code that belongs to another site is
// reported unknown, the same as one that never existed: an answer that
// distinguished them would tell a stranger which codes exist somewhere else.
func (p *portalPorts) whyNotClaimable(ctx context.Context, siteID, code string, at time.Time) error {
	state, err := p.a.st.Grants().VoucherStateFor(ctx, siteID, code, at)
	if err != nil {
		return portal.ErrVoucherUnknown
	}
	switch {
	case !state.Known:
		return portal.ErrVoucherUnknown
	case state.State == "revoked":
		return portal.ErrVoucherRevoked
	case state.State == "expired", state.Expired:
		return portal.ErrVoucherExpired
	case state.State == "in-use", state.State == "exhausted":
		return portal.ErrVoucherUsed
	default:
		// `unused`, still unexpired, and the claim found nothing: another request
		// took it between the two statements. That guest is being told the truth
		// about the code — it is in use — by whoever won.
		return portal.ErrVoucherUsed
	}
}

// AuthenticateGuest takes the submitted password and answers with an account or
// with nothing.
//
// There is no method here that returns a verifier, for the reason
// `docs/rules/security.md` §2 gives about the Policy API: an interface with a
// method that hands back a password hash is an interface somebody calls.
//
// **The password is checked before `enabled`**, and the order is deliberate. A
// disabled account is told so, because the holder is standing in the venue and the
// staff can turn it back on — and that answer only reaches somebody who has
// already presented the correct password, which is not a disclosure.
func (p *portalPorts) AuthenticateGuest(ctx context.Context, siteID, username, password string) (portal.Guest, error) {
	acc, err := p.a.st.Accounts().GetByUsername(ctx, siteID, username)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// An unknown username and a wrong password are one answer.
			// Distinguishing them publishes which accounts exist — **and so does
			// answering faster**. Returning at once here would bring an unknown
			// username back an argon2id earlier than a known one, and a stopwatch
			// on the portal would be a username oracle the identical refusal
			// exists to remove. It pays the same verification the
			// console's `Authenticate` pays, against the same decoy.
			p.a.burnAVerification(password)
			return portal.Guest{}, portal.ErrBadCredentials
		}
		return portal.Guest{}, err
	}
	ok, err := identity.VerifyPassword(password, acc.PasswordHash)
	if err != nil || !ok {
		return portal.Guest{}, portal.ErrBadCredentials
	}
	if !acc.Enabled {
		return portal.Guest{}, portal.ErrAccountDisabled
	}
	if err := p.a.st.Accounts().RecordLogin(ctx, acc.ID, p.a.now()); err != nil {
		// The guest authenticated. A failed timestamp write is not a reason to
		// keep them off the network they paid for.
		_ = err
	}
	return portal.Guest{AccountID: acc.ID, Username: acc.Username, Plan: planToPortal(acc.Plan)}, nil
}

// ── where a guest is standing ────────────────────────────────────────────────

// Placement resolves the access point MAC the NAS named.
//
// An unknown access point is `portal.ErrNotFound` and no session is minted. The AP
// MAC arrives unsigned, so what this resolves is a *claim*; the fact is what the
// driver confirms afterwards.
func (p *portalPorts) Placement(ctx context.Context, apMAC string) (portal.Placement, error) {
	if apMAC == "" {
		return portal.Placement{}, portal.ErrNotFound
	}
	place, err := p.a.st.Grants().PlacementByAPMAC(ctx, apMAC)
	if err != nil {
		return portal.Placement{}, portalErr(err)
	}
	return portal.Placement{
		SiteID:       place.SiteID,
		NASID:        deref(place.NASID),
		VendorSiteID: deref(place.VendorSiteID),
		// The third noun. `GuestPlacement` carries it; dropping it here would
		// make every driver call resolve this same row again for itself.
		ControllerID: deref(place.ControllerID),
		Accounting:   accountingExpectation(place.AccountingConfigured),
	}, nil
}

// accountingExpectation maps the NAS probe's three-valued measurement.
//
// Nil is `AccountingUnknown` and not `AccountingAbsent`, which is the whole reason
// the column is nullable: *nobody has measured this* and *this device sends no
// accounting* are different facts, and reporting a plan `unenforced` on the first
// would tell an operator a working quota is not being applied.
func accountingExpectation(configured *bool) portal.AccountingExpectation {
	switch {
	case configured == nil:
		return portal.AccountingUnknown
	case *configured:
		return portal.AccountingArrives
	default:
		return portal.AccountingAbsent
	}
}

// ── the hardware seam ────────────────────────────────────────────────────────

// Verify is `NASDriver.VerifyPresence`, un-truncated.
//
// A driver that cannot be built is `PresenceUnavailable` **and** a non-nil error,
// which is the pair `portal.Classify` reads: reporting only one of them would have
// this venue's outages classified as refusals half the time, and a refusal tells
// the guest their phone is not on a network they are standing in.
//
// It never answers yes on the strength of anything but a driver. A controller Foyer
// cannot reach is a controller that cannot confirm a MAC, and §4 makes confirmation
// mandatory before any grant.
func (p *portalPorts) Verify(ctx context.Context, s portal.Session) (nas.Presence, error) {
	d, err := p.driverFor(ctx, s.Placement())
	if err != nil {
		return nas.Presence{Outcome: nas.PresenceUnavailable}, err
	}
	if det, ok := d.(nas.PresenceDetailer); ok {
		return det.VerifyPresenceDetail(ctx, s.ClientMAC, s.APMAC, s.VendorSiteID)
	}
	// A driver that can only answer a bool can produce `verified` or `not-found`
	// and nothing else, so an outage behind one still reads to the guest as a
	// refusal. That is a property of the driver; the fix is to implement the
	// optional interface, which UniFi does.
	ok, err := d.VerifyPresence(ctx, s.ClientMAC, s.APMAC, s.VendorSiteID)
	if err != nil {
		return nas.Presence{Outcome: nas.PresenceUnavailable}, err
	}
	if !ok {
		return nas.Presence{Outcome: nas.PresenceNotFound}, nil
	}
	return nas.Presence{OK: true, Outcome: nas.PresenceVerified}, nil
}

// Authorize lets the client through.
//
// **The placement is passed in rather than resolved here.** `portal.Grant` carries
// Foyer's `SiteID`, and handing that to a UniFi controller is precisely the
// conflation `internal/portal/ports.go` warns about — the lookup lands in the wrong
// place on a controller that manages more than one of the vendor's sites. And an
// Authorize that re-resolved the access point for itself would cost two round
// trips — once for the vendor site and once inside `driverFor` for the controller —
// and, worse, resolve the row separately from `Verify`, so a device could be
// confirmed present on one controller and authorized on another.
func (p *portalPorts) Authorize(ctx context.Context, place portal.Placement, g portal.Grant) error {
	d, err := p.driverFor(ctx, place)
	if err != nil {
		return err
	}

	req := nas.AuthorizeRequest{ClientMAC: g.ClientMAC, APMAC: g.APMAC, SiteID: place.VendorSiteID}
	// The plan reaches the NAS only where the NAS is the thing enforcing it.
	// Sending the numbers otherwise would be asking a device to promise something
	// its own driver has already said it cannot do.
	if g.Plan.EnforcedBy == portal.EnforcedByNAS {
		if g.Plan.DurationMinutes != nil {
			req.TimeLimitMinutes = *g.Plan.DurationMinutes
		}
		if g.Plan.DataAllowanceMB != nil {
			req.DataUsageLimitMB = *g.Plan.DataAllowanceMB
		}
		if g.Plan.UpKbps != nil {
			req.TxRateLimitKbps = *g.Plan.UpKbps
		}
		if g.Plan.DownKbps != nil {
			req.RxRateLimitKbps = *g.Plan.DownKbps
		}
	}
	return d.AuthorizeGuest(ctx, req)
}

// HardQuota is the driver's computed capability, not the column the console reads.
//
// The two are different claims, and treating them as one shows an operator a
// control the driver cannot honour. `Capabilities.HardQuota` here decides `Plan.EnforcedBy`, which is stored on
// the grant as a fact about the moment the guest was let on — so it has to be what
// the thing that will actually be asked to honour the plan reports, now.
//
// A driver that cannot be built answers false, which reads as `foyer-sampled`
// rather than `nas` for a limited plan. It cannot reach a grant: the same failure
// makes `Verify` unavailable, and nothing is granted on an unavailable verdict.
func (p *portalPorts) HardQuota(ctx context.Context, s portal.Session) bool {
	d, err := p.driverFor(ctx, s.Placement())
	if err != nil {
		return false
	}
	return d.Capabilities().HardQuota
}

// driverFor builds the driver for the controller named by the placement.
//
// The placement is resolved from the AP MAC rather than from the RADIUS client,
// because an access point behind a proxying controller has no `radius_clients` row
// at all — 00004 leaves that binding unset rather than guessing — and such a device
// is exactly the one a controller-API driver still has to be able to authorize.
// That resolution happens once, at the front door, and is carried on the
// session; this function performs no query at all.
//
// An empty ControllerID is `ErrNoDriver` and not a lookup. It is the same fact a
// NULL `controller_id` states: this access point is bound to no
// controller, so there is nobody to ask. Failing closed is the only safe direction —
// guessing a controller is authorizing a device against a venue that did not confirm
// it.
func (p *portalPorts) driverFor(ctx context.Context, place portal.Placement) (nas.NASDriver, error) {
	if place.ControllerID == "" {
		return nil, ErrNoDriver
	}
	d, _, err := p.a.driverFor(ctx, place.ControllerID, place.VendorSiteID)
	return d, err
}

// ── the pre-authentication rate limit ────────────────────────────────────────

// Allow is the rate limit §5 asks for, and it is the adapter's so that the two
// `portal.Service` values `cmd/foyer` builds share one budget.
//
// **In-process, which is a stated limit rather than an oversight**: a deployment
// running two control-plane replicas gets twice the configured rate and nothing
// here would say so. `portal.Limiter` is a port precisely so the replacement is a
// shared counter rather than an edit to a handler.
func (p *portalPorts) Allow(key string, at time.Time) bool {
	if p.limiter == nil {
		return true
	}
	return p.limiter.Allow(key, at)
}

// ── conversions ──────────────────────────────────────────────────────────────

// planToPortal maps a stored plan onto the portal's.
//
// `EnforcedBy` is deliberately dropped on the way in. The value stored on a batch
// or an account was decided when that row was created, and the grant's is derived
// at grant time from the driver that will actually be asked to honour the plan —
// which may be a device registered afterwards with different capabilities.
func planToPortal(p store.Plan) portal.Plan {
	return portal.Plan{
		Name:            deref(p.Name),
		NameLang:        deref(p.NameLang),
		DurationMinutes: p.DurationMinutes,
		DataAllowanceMB: p.DataAllowanceMB,
		UpKbps:          p.UpKbps,
		DownKbps:        p.DownKbps,
	}
}

// planFromPortal maps the portal's plan onto a stored one, `EnforcedBy` included:
// on this direction it is the grant's own value and is the whole point of the
// column.
func planFromPortal(p portal.Plan) store.Plan {
	return store.Plan{
		Name:            nilIfEmpty(p.Name),
		NameLang:        nilIfEmpty(p.NameLang),
		DurationMinutes: p.DurationMinutes,
		DataAllowanceMB: p.DataAllowanceMB,
		UpKbps:          p.UpKbps,
		DownKbps:        p.DownKbps,
		EnforcedBy:      string(p.EnforcedBy),
	}
}

// portalErr translates the store's sentinels into the portal's.
//
// Only `ErrNotFound` has a meaning across this seam; everything else passes through
// and the service turns it into `generic`. A store error that reached a guest would
// carry a table name and a constraint.
func portalErr(err error) error {
	if err != nil && errors.Is(err, store.ErrNotFound) {
		return portal.ErrNotFound
	}
	return err
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
