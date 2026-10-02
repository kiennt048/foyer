package nas

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"
)

// CoADriver — the second enforcement family, and the first one that is not a
// controller API.
//
// # What this driver can and cannot be
//
// `NASDriver` was shaped around a controller: something you can *ask*. RFC 5176
// is a one-way protocol — you tell a NAS to end or change a session and it
// answers ACK or NAK. There is no query in it at all. Three consequences run
// through everything below and none of them is a detail:
//
//   - **Presence cannot be checked over CoA.** `VerifyPresence` is mandatory
//     before a grant (`security.md` §4) and this family has no endpoint that
//     answers it. The evidence available instead is Foyer's own accounting: the
//     RADIUS server saw this MAC authenticate from this NAS. That is a
//     `SessionSource`, it is not this package's to implement, and with none
//     configured this driver answers `PresenceUnavailable` **and an error** —
//     never `true`.
//   - **There is nothing to discover.** The device is its own controller. The
//     contract says so too: `ControllerVendor` is `[unifi, omada, ruckus]` and
//     excludes this family because *"MikroTik and the CoA family have no
//     controller — the device is its own, and its record is a Nas registered by
//     hand"*. `DiscoverAccessPoints` returns `ErrNoController` rather than an
//     empty list, because "there are none" and "there is nothing that could
//     enumerate them" are different answers and only one of them is true.
//   - **A probe has to write something.** See Probe: the only way to learn
//     whether a NAS is listening on its CoA port with the secret Foyer holds is
//     to send it a request, and the only request with no side effect is one for
//     a session that cannot exist.
//
// # UNVERIFIED, without exception
//
// There is no Cisco, Aruba or MikroTik device on this project. The RFC 5176
// framing is DOCUMENTED and is asserted byte for byte in the tests; every
// vendor id, attribute number, command string and MAC format below is a guess
// with a measurement named beside it. `docs/rules/testing.md`: *"ACKing a CoA
// is trivial; faithfully rejecting one is not."* The fake NAS these tests drive
// refuses eleven ways and accepts one.

// ErrNoController is what a driver for a device that *is* its own controller
// answers when asked to enumerate one.
var ErrNoController = errors.New("nas: this driver has no controller to enumerate — the device is its own, and its record is a Nas registered by hand")

// ErrNoSessionSource is the fail-closed answer of a CoA driver that was given
// no way to look up a session.
//
// Fail closed rather than fail open, and it is worth saying why in a file about
// a protocol with no query in it: the alternative is a driver that returns
// `true` from `VerifyPresence` because it has no way to say no, which is the
// portal-as-a-public-API-for-authorizing-arbitrary-MACs failure `security.md`
// §4 is entirely about.
var ErrNoSessionSource = errors.New("nas: this CoA driver has no session source, so it cannot confirm any device is present — see security.md §4")

// UnsupportedActionError is a driver refusing an action its family does not
// have, as distinct from one that failed.
//
// Typed and carrying its reason, for the same purpose `CoAError` carries an
// Error-Cause: an admin console has to be able to say *why* a control is absent
// or a call refused, and a string nobody can branch on is how "this vendor
// cannot do that" becomes "something went wrong".
type UnsupportedActionError struct {
	Vendor Vendor
	Action string
	Reason string
}

func (e *UnsupportedActionError) Error() string {
	return fmt.Sprintf("nas: %s cannot %s: %s", e.Vendor, e.Action, e.Reason)
}

// SessionSource resolves a MAC to the session Foyer's own RADIUS server has for
// it.
//
// # This interface is declared here and implemented elsewhere, deliberately
//
// The data is in `radacct`, which belongs to FreeRADIUS and is read through
// `internal/store`. So this package states the shape it needs and takes an implementation, exactly as
// `internal/aaa.PolicyStore` does for the same reason. **Reported rather than
// reached across.**
//
// What it must answer, and the distinction is the same one `PolicyStore`
// insists on: `(Session{}, false, nil)` means *no such session*, and any
// non-nil error means *I could not look*. A driver that cannot tell the two
// apart turns a database outage into "the guest's device is not on the
// network".
type SessionSource interface {
	// ActiveSession returns the live session for a MAC, if the RADIUS server
	// has one. The returned Session should carry every identifier the platform
	// recorded — Acct-Session-Id, NAS-IP-Address, NAS-Identifier, User-Name —
	// because which of them a NAS matches on differs per vendor and this
	// package cannot choose for it.
	ActiveSession(ctx context.Context, mac string) (Session, bool, error)
}

// CoAVendor is what differs between two members of the CoA family.
//
// It is the same shape as `VendorProfile` and for the same stated reason: the
// design claims a new driver is one file with no core changes, and a switch
// statement in this file would make that false by one arm every time. A vendor
// is a file that builds a `CoAVendor` and hands it to `NewCoADriver`; nothing
// here needs to know it exists. `TestAddingACoAVendorNeedsNoCoreChange` proves
// it by defining a fictional one entirely inside a test file.
type CoAVendor interface {
	Vendor() Vendor

	// Identify renders this vendor's session identification. It returns the
	// standard attributes to send (as a Session, since that is what the encoder
	// takes) and any vendor-specific ones — Cisco IOS-XE identifies a session by
	// `Cisco-AVPair = audit-session-id=…`, which is a VSA.
	//
	// **This is where the MAC format lives**, and it is the guess most likely to
	// be wrong: Calling-Station-Id has no canonical spelling and the vendors
	// disagree. A MAC in the wrong format identifies no session, which the NAS
	// answers with Error-Cause 503 — visible — or by ignoring the request, which
	// is not.
	Identify(s Session) (Session, []VendorAttribute, error)

	// Reauthorize renders the command that makes this vendor's NAS re-run
	// authorization for a live session, carrying whatever of req it can express.
	//
	// It returns an *UnsupportedActionError where the vendor has no such
	// command, and `Capabilities().CanAuthorizeGuest` must agree with it —
	// TestCoACapabilitiesMatchBehaviour holds the two together, because a
	// capability that says one thing while the code does another is a control
	// that either silently does nothing or is hidden for no reason.
	Reauthorize(s Session, req AuthorizeRequest) ([]VendorAttribute, error)

	Capabilities() Capabilities
	CapabilityEvidence() CapabilityEvidence

	// AttributeEvidence says how the **encoding** of each vendor attribute this
	// vendor emits is known, keyed on attribute name. Same shape and same
	// purpose as VendorProfile.AttributeEvidence, for the other abstraction.
	//
	// # It is a separate map from CapabilityEvidence because it answers a
	// # separate question, and conflating them would over-claim
	//
	// *"Cisco-AVPair is vendor 9, attribute 1"* and *"a Cisco NAS honours
	// subscriber:command=reauthenticate"* are two facts of different standing.
	// The first is **DOCUMENTED** — read out of the dictionary FreeRADIUS
	// itself parses. The second is still **UNVERIFIED** and no dictionary can
	// settle it. One `CapabilityFact` carries one label, so a single map would
	// have to round both facts to whichever is louder, and the loud one is the
	// wrong one: an encoding that is right buys nothing if the device ignores
	// the value inside it.
	//
	// So the capability map stays entirely UNVERIFIED and this one records what
	// the measurement actually settled. `TestNoCoAVendorClaimsToBeMeasured` and
	// `TestCoAWireNumbersAreDocumented` hold the two apart.
	AttributeEvidence() AttributeEvidence
}

// FreeRADIUSDictionaryCitation is the source the vendor numbers were read from,
// written once because three vendor files cite it.
//
// **The authority is the file the server itself parses**, not a vendor web page
// and not this author's memory. That is what makes it DOCUMENTED rather than
// believed: FreeRADIUS uses these dictionaries to decode the very packets Foyer
// emits, so a number that disagrees with them would fail in `lab/` before it
// ever reached hardware.
const FreeRADIUSDictionaryCitation = "read from /usr/share/freeradius/dictionary.* inside the " +
	"FreeRADIUS 3.2.x image lab/ builds — the dictionary the server itself parses. " +
	"Recorded in docs/protocol-reality.md."

// CoAConfig builds a CoADriver.
type CoAConfig struct {
	// Vendor is the family member. Required.
	Vendor CoAVendor

	// Host is the NAS's address. For a `radius_clients` row this is
	// `address`, which is `inet NOT NULL` — so it parses, and NASIPAddress is
	// derived from it unless set explicitly.
	Host string

	// Port is the CoA port on this NAS. **Per-NAS, never a constant**: 3799 is
	// common and Cisco frequently uses 1700. Zero means DefaultCoAPort.
	Port int

	// Secret is the RADIUS shared secret for this NAS.
	Secret string

	// Timeout bounds one attempt; Retries is how many retransmits follow the
	// first send. Zero means the CoAClient defaults.
	Timeout time.Duration
	Retries int

	// Sessions resolves a MAC to a live session. **Without it this driver
	// refuses to confirm presence at all** and every action that needs a
	// session identifier fails. See ErrNoSessionSource.
	Sessions SessionSource

	// NASIPAddress and NASIdentifier are how this driver recognises a session
	// as belonging to *this* NAS rather than to another one on the same
	// installation. Derived from Host where it parses as an address.
	//
	// If neither is known the driver cannot make that check and says so rather
	// than passing: a session found for a MAC could be on any device, and a
	// grant issued on that basis authorizes a device that is somewhere else.
	NASIPAddress  netip.Addr
	NASIdentifier string
}

// CoADriver drives one NAS over RFC 5176.
type CoADriver struct {
	vendor   CoAVendor
	client   *CoAClient
	sessions SessionSource

	nasIP netip.Addr
	nasID string
}

// NewCoADriver builds a driver for one NAS. It dials nothing.
func NewCoADriver(cfg CoAConfig) (*CoADriver, error) {
	if cfg.Vendor == nil {
		return nil, errors.New("nas: CoA driver needs a vendor")
	}
	ip := cfg.NASIPAddress
	if !ip.IsValid() {
		// A `radius_clients` row's address is `inet NOT NULL`, so this is the
		// normal path rather than a fallback. A hostname simply leaves it unset
		// and the driver reports that it cannot confirm the NAS.
		if parsed, err := netip.ParseAddr(cfg.Host); err == nil {
			ip = parsed
		}
	}
	return &CoADriver{
		vendor: cfg.Vendor,
		client: &CoAClient{
			Addr:    CoAAddr(cfg.Host, cfg.Port),
			Secret:  cfg.Secret,
			Timeout: cfg.Timeout,
			Retries: cfg.Retries,
		},
		sessions: cfg.Sessions,
		nasIP:    ip,
		nasID:    cfg.NASIdentifier,
	}, nil
}

func (d *CoADriver) Vendor() Vendor { return d.vendor.Vendor() }

func (d *CoADriver) Capabilities() Capabilities { return d.vendor.Capabilities() }

// CapabilityEvidence implements EvidencedDriver.
func (d *CoADriver) CapabilityEvidence() CapabilityEvidence { return d.vendor.CapabilityEvidence() }

// AttributeEvidence reports how this driver's vendor attribute *encodings* are
// known. See CoAVendor.AttributeEvidence for why it is not the capability map.
func (d *CoADriver) AttributeEvidence() AttributeEvidence { return d.vendor.AttributeEvidence() }

// VerifyPresence implements NASDriver.
func (d *CoADriver) VerifyPresence(ctx context.Context, claimedMAC, apMAC, siteID string) (bool, error) {
	p, err := d.VerifyPresenceDetail(ctx, claimedMAC, apMAC, siteID)
	return p.OK, err
}

// VerifyPresenceDetail implements PresenceDetailer.
//
// # What it checks, against the four `security.md` §4 asks for
//
//	the MAC is really associated  — yes, from Foyer's own accounting
//	on the claimed AP           — NO. Not available on this family.
//	in the claimed site         — NO. This family has no site.
//	and still unauthorized      — NO. Accounting cannot tell captive from authorized.
//
// One of four, plus one §4 does not list and this family can make: the session
// is on **this** NAS. That is a real check and it is the one that stops a
// session on another device in the same installation from authorizing a grant
// here.
//
// So `APConfirmed` is false on every answer, `Probe` carries the unconfirmed
// warning unconditionally, and a caller that needs all four does not have them.
// Saying so is the whole job; the alternative was a driver that returns true
// because it has nothing to say no with.
func (d *CoADriver) VerifyPresenceDetail(ctx context.Context, claimedMAC, apMAC, siteID string) (Presence, error) {
	if d.sessions == nil {
		return Presence{Outcome: PresenceUnavailable, SiteID: siteID}, ErrNoSessionSource
	}
	if !d.nasIP.IsValid() && d.nasID == "" {
		return Presence{Outcome: PresenceUnavailable, SiteID: siteID}, errors.New(
			"nas: this CoA driver knows neither its NAS-IP-Address nor its NAS-Identifier, " +
				"so a session found for this MAC could be on any device")
	}
	s, ok, err := d.sessions.ActiveSession(ctx, normalizeMAC(claimedMAC))
	if err != nil {
		// "I could not look" is not "it is not there". Reporting it as a
		// refusal tells a guest their device is off the network because a
		// database was slow.
		return Presence{Outcome: PresenceUnavailable, SiteID: siteID}, err
	}
	if !ok {
		return Presence{Outcome: PresenceNotFound, SiteID: siteID}, nil
	}
	switch ours, compared := d.sessionIsOurs(s); {
	case !compared:
		// Nothing could be compared, which is not a refusal and not a pass. An
		// operator told "site mismatch" here would go looking for a device that
		// is probably sitting in front of them; the truth is that the accounting
		// row carries no NAS identifier to weigh.
		return Presence{Outcome: PresenceUnavailable, SiteID: siteID}, fmt.Errorf(
			"nas: the session for %s names no NAS, so it cannot be confirmed as belonging to this one",
			normalizeMAC(claimedMAC))
	case !ours:
		// Not PresenceNotFound: the session exists and is somewhere else. The
		// closest outcome the type has is the site mismatch, and that is what a
		// NAS mismatch is on a family with no sites.
		return Presence{Outcome: PresenceSiteMismatch, SiteID: siteID}, nil
	}
	return Presence{
		OK:      true,
		Outcome: PresenceVerified,
		SiteID:  siteID,
		// Never true on this family. There is nothing in an accounting row this
		// package models that names an access point.
		APConfirmed: false,
	}, nil
}

// sessionIsOurs compares whichever NAS identifier both sides carry, and reports
// separately whether any comparison happened at all.
//
// # Two return values, because "not ours" and "cannot tell" are different answers
//
// Folded into one `false`, a sabotage — making the unmade comparison count as a
// pass — **stays green**, the signature `docs/rules/testing.md` warns about:
// nothing measures the distinction. Worse, a refusal then arrives as
// `PresenceSiteMismatch`, which tells an operator the device is somewhere else
// when the truth is that nothing could be checked.
//
// So: `ok` is a comparison that succeeded, `compared` is whether one was made,
// and only both together are evidence. An accounting row with no NAS-Identifier
// weighed against a configured one is an **unchecked claim**, which is the rule
// `Presence.APConfirmed` already states for the access point.
func (d *CoADriver) sessionIsOurs(s Session) (ok, compared bool) {
	if d.nasIP.IsValid() && s.NASIPAddress.IsValid() {
		if d.nasIP != s.NASIPAddress {
			return false, true
		}
		compared = true
	}
	if d.nasID != "" && s.NASIdentifier != "" {
		if !strings.EqualFold(d.nasID, s.NASIdentifier) {
			return false, true
		}
		compared = true
	}
	return compared, compared
}

// resolveSession looks up the session a request is about and renders this
// vendor's identification for it.
//
// It returns both the session as the accounting store has it and the
// identification the vendor chose to send, because they are different things
// and each has a consumer: `CoAVendor.Reauthorize` reads the first — a vendor
// may need a field it did not choose to identify on — and the encoder takes the
// second.
func (d *CoADriver) resolveSession(ctx context.Context, mac string) (Session, Session, []VendorAttribute, error) {
	if mac == "" {
		return Session{}, Session{}, nil, errors.New("nas: client MAC is required")
	}
	if d.sessions == nil {
		return Session{}, Session{}, nil, ErrNoSessionSource
	}
	found, ok, err := d.sessions.ActiveSession(ctx, normalizeMAC(mac))
	if err != nil {
		return Session{}, Session{}, nil, err
	}
	if !ok {
		// The same fact a NAS would answer with Error-Cause 503, found one hop
		// earlier and at no cost to anyone's network. Typed identically, so a
		// caller branching on Session-Context-Not-Found does not have to know
		// which side noticed.
		return Session{}, Session{}, nil, &CoAError{
			Code:          codeDisconnectNAK,
			ErrorCause:    ErrorCauseSessionContextNotFound,
			HasErrorCause: true,
		}
	}
	switch ours, compared := d.sessionIsOurs(found); {
	case !compared:
		return Session{}, Session{}, nil, fmt.Errorf(
			"nas: the session for %s names no NAS, so it cannot be confirmed as belonging to this one",
			normalizeMAC(mac))
	case !ours:
		return Session{}, Session{}, nil, fmt.Errorf("nas: the session for %s is on another NAS", normalizeMAC(mac))
	}
	ident, vsas, err := d.vendor.Identify(found)
	if err != nil {
		return Session{}, Session{}, nil, err
	}
	return found, ident, vsas, nil
}

// AuthorizeGuest asks the NAS to re-run authorization for a live session.
//
// It is not UniFi's `authorize-guest`, and the difference is the reason the two
// abstractions exist. There is no RFC 5176 request that *creates* a session: a
// CoA-Request changes one that already exists. So what "authorize" means on this
// family is *the Policy API's next answer for this session now applies* — the
// authorization itself is rendered by `VendorProfile` into the Access-Accept,
// and this only makes the NAS go and ask.
//
// A vendor with no such command refuses through an *UnsupportedActionError, and
// its Capabilities().CanAuthorizeGuest is false. Those two are held together by
// a test rather than by care.
func (d *CoADriver) AuthorizeGuest(ctx context.Context, req AuthorizeRequest) error {
	if !d.vendor.Capabilities().CanAuthorizeGuest {
		// Ask the vendor why before answering generically. Two vendors report
		// this capability false for two different reasons — MikroTik because
		// there is no session to create, Aruba because AuthorizeRequest has no
		// role field — and an operator reading "this cannot be done" learns
		// nothing they can act on. The capability stays authoritative; only the
		// sentence comes from the vendor.
		if _, err := d.vendor.Reauthorize(Session{}, req); err != nil {
			var unsupported *UnsupportedActionError
			if errors.As(err, &unsupported) {
				return unsupported
			}
		}
		return &UnsupportedActionError{
			Vendor: d.Vendor(),
			Action: "authorize a guest",
			Reason: "RFC 5176 changes a session that already exists and cannot create one; " +
				"on this platform the authorization is the Access-Accept, which is VendorProfile's half",
		}
	}
	return d.Reauthorize(ctx, req)
}

// Reauthorize is the fourth action `docs/rules/nas-drivers.md` names —
// *"verify, authorize, revoke, reauthorize"* — and the one `NASDriver` does not
// have.
//
// # This is a finding about the abstraction, recorded here rather than worked around
//
// `Capabilities.LiveReauthorize` gates an *apply now* control in the admin
// console, and until this method existed there was no interface method for that
// control to call. The gap was invisible while UniFi was the only driver,
// because UniFi reported `LiveReauthorize: false` until its re-authorization was
// measured; UniFi implements Reauthorizer too.
//
// It is an **optional** interface rather than a widening of `NASDriver`,
// following the precedent this package already set with `PresenceDetailer` and
// `EvidencedDriver`: a driver that can do better says so by implementing
// something, and every existing caller keeps compiling.
func (d *CoADriver) Reauthorize(ctx context.Context, req AuthorizeRequest) error {
	if !d.vendor.Capabilities().LiveReauthorize {
		return &UnsupportedActionError{
			Vendor: d.Vendor(),
			Action: "reauthorize a live session",
			Reason: "this vendor reports LiveReauthorize false; its own Reauthorize says why",
		}
	}
	found, ident, identAttrs, err := d.resolveSession(ctx, req.ClientMAC)
	if err != nil {
		return err
	}
	cmd, err := d.vendor.Reauthorize(found, req)
	if err != nil {
		return err
	}
	return d.client.CoA(ctx, ident, append(identAttrs, cmd...)...)
}

// DeauthorizeGuest ends the session. This is the half of RFC 5176 every member
// of the family implements, and the half the protocol was named after.
func (d *CoADriver) DeauthorizeGuest(ctx context.Context, req DeauthorizeRequest) error {
	_, ident, identAttrs, err := d.resolveSession(ctx, req.ClientMAC)
	if err != nil {
		return err
	}
	return d.client.Disconnect(ctx, ident, identAttrs...)
}

// DiscoverAccessPoints implements NASDriver by refusing.
//
// An empty list would say "this NAS has no access points", which is false: it
// has exactly one and it is the NAS. `ErrNoController` says the true thing.
func (d *CoADriver) DiscoverAccessPoints(context.Context) ([]DiscoveredAP, error) {
	return nil, ErrNoController
}

// Reauthorizer is the fourth action, as an optional interface.
//
// Declared beside the driver that implements it rather than in `driver.go`,
// because the point of an optional interface is that the core does not have to
// know. A caller asks with a type assertion; a driver that cannot answers by
// not implementing it.
type Reauthorizer interface {
	Reauthorize(ctx context.Context, req AuthorizeRequest) error
}

// coaProbeUserName is the User-Name a probe sends. It exists so that a NAS
// matching on User-Name rather than Acct-Session-Id also fails to match.
const coaProbeUserName = "foyer-probe"

// Probe asks the NAS to disconnect a session that cannot exist, and reads the
// refusal.
//
// # A probe on this family has to write, and this is the request with no effect
//
// There is no query in RFC 5176. The four things an operator needs to know —
// is anything listening on this port, is it speaking RADIUS, does the shared
// secret match, does it implement Disconnect-Request — are all answered by
// sending one request and looking at what comes back, and the only request
// guaranteed to change nothing is one naming a session that does not exist.
// The session id is 32 random hex characters behind a `foyer-probe-` prefix.
//
// # The four outcomes, and the one that is a warning rather than an error
//
//	NAK          the device answered, the secret verified, it implements this. Best case.
//	ACK          it claims to have disconnected a session that cannot exist — see below.
//	bad auth     something answered and the shared secret does not match. The most useful
//	             single diagnostic this protocol produces.
//	silence      ambiguous, and reported as ambiguous: nothing listening, a firewall, the
//	             wrong port (Cisco frequently uses 1700), or a NAS that ignores unknown
//	             sessions rather than NAKing them. **UNVERIFIED which.**
//
// An ACK to a session that cannot exist is the shape of the failure this whole
// package is written against — `docs/rules/nas-drivers.md`: *"A driver that
// ignores these reports success on a no-op."* Here the device is doing it. It
// is a warning rather than an error because the device is plainly reachable;
// what it means is that **no revocation through this driver can be trusted to
// have revoked anything**, since success is what it says either way.
func (d *CoADriver) Probe(ctx context.Context) (*ProbeResult, error) {
	res := &ProbeResult{
		APIBase:  d.client.Addr,
		AuthMode: "radius-shared-secret",
		// Presence on this family is Foyer's own accounting, not the device's.
		PresenceCheckSupported: d.sessions != nil,
		Unverified:             UnverifiedCapabilities(d),
	}
	res.Warnings = append(res.Warnings, d.unconditionalWarnings()...)
	if w, ok := CapabilitiesUnverifiedWarning(res.Unverified); ok {
		res.Warnings = append(res.Warnings, w)
	}

	id := probeSessionID()
	err := d.client.Disconnect(ctx, Session{AcctSessionID: id, UserName: coaProbeUserName})

	var coaErr *CoAError
	switch {
	case err == nil:
		res.Reachable = true
		res.Warnings = append(res.Warnings, CoAAcknowledgedUnknownSessionWarning(id))
		return res, nil
	case errors.As(err, &coaErr):
		// A refusal is the healthy answer, whatever the cause.
		res.Reachable = true
		return res, nil
	case errors.Is(err, ErrCoABadAuthenticator):
		// Reachable on purpose: something answered on that port. The failure is
		// the credential, and an operator told "unreachable" would go and check
		// cabling.
		res.Reachable = true
		res.Errors = append(res.Errors, err.Error())
		return res, err
	case errors.Is(err, ErrCoATimeout):
		res.Errors = append(res.Errors, fmt.Sprintf(
			"no answer from %s within the retransmit budget. UDP silence is ambiguous: nothing "+
				"listening, a firewall, the wrong port (3799 is common, Cisco frequently uses 1700), "+
				"or a NAS that ignores a request for an unknown session instead of refusing it. "+
				"Which one is UNVERIFIED — no device has been measured.", d.client.Addr))
		return res, err
	default:
		res.Errors = append(res.Errors, err.Error())
		return res, err
	}
}

// unconditionalWarnings are what this driver reports on every probe, whatever
// the device answers.
//
// Unconditional for the reason `ShippedProbeWarnings` already records: a warning
// that only appears when something else is broken is one an operator learns to
// expect the absence of. Three of `security.md` §4's four checks are not made on
// this family and that is true before anything is dialled.
// canConfirmAccessPoint is false on this family and stays false.
//
// Nothing in RFC 5176 asks a NAS anything, and no accounting attribute this
// package models names an access point. UniFi's answer is different because its
// client object carries `uplinkDeviceId`; this one cannot change without a second source of session
// data, which would be a different driver.
func (d *CoADriver) canConfirmAccessPoint() bool { return false }

func (d *CoADriver) unconditionalWarnings() []ProbeWarning {
	w := []ProbeWarning{
		AccessPointUnconfirmedWarning(
			"no accounting attribute this package models identifies an access point, and RFC 5176 " +
				"has no query at all — so the AP, the site and still-unauthorized are all unchecked " +
				"on this family; only the MAC and the NAS are"),
	}
	if d.sessions == nil {
		w = append(w, ProbeWarning{
			Kind:   WarnCapabilitiesUnverified,
			Params: map[string]any{"count": 1},
			Detail: "this driver has no session source, so it cannot confirm any device is present " +
				"and every action that needs a session identifier will fail — see security.md §4",
		})
	}
	return w
}

// probeSessionID is a session id no NAS can have issued.
func probeSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail in practice, and a probe that cannot make a
		// random id must not fall back to a fixed one — a fixed probe id is a
		// session id somebody's NAS could eventually issue.
		return "foyer-probe-norandom"
	}
	return "foyer-probe-" + hex.EncodeToString(b[:])
}

// ---------------------------------------------------------------------------
// MAC spelling — shared mechanics, per-vendor choice
// ---------------------------------------------------------------------------

// macHex returns a MAC's twelve lowercase hex digits, or "" if the input is not
// twelve hex digits once punctuation is removed.
//
// Empty rather than a best effort on purpose: every caller here is rendering a
// session identifier, and a malformed one matches nothing. Returning the input
// unchanged would put an unparseable string on the wire and turn a bad value
// into a NAK the operator has to decode.
func macHex(mac string) string {
	var out []rune
	for _, r := range strings.ToLower(strings.TrimSpace(mac)) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			out = append(out, r)
		}
	}
	if len(out) != 12 {
		return ""
	}
	return string(out)
}

// macWith spells a MAC with `sep` every `group` hex digits.
//
// # Every vendor spells this differently and none of it is measured
//
// Calling-Station-Id has no canonical form. Cisco IOS-XE defaults to
// `aabb.ccdd.eeff` and has a `radius-server attribute 31 mac format` knob with
// at least four settings; Aruba is commonly unformatted; RouterOS is commonly
// colon-separated uppercase. **A MAC in the wrong spelling identifies no
// session**, and the good case is a NAK 503 the driver reports. The bad case is
// a NAS that ignores a request it cannot match, which is silence, which this
// client reports as a timeout — indistinguishable from the device being off.
//
// This is the single largest guess in the CoA family and it is the first thing
// a hardware session should measure, one vendor at a time, by reading
// Calling-Station-Id out of an Access-Request the device actually sent.
func macWith(mac, sep string, group int, upper bool) string {
	hexDigits := macHex(mac)
	if hexDigits == "" {
		return ""
	}
	if upper {
		hexDigits = strings.ToUpper(hexDigits)
	}
	if sep == "" {
		return hexDigits
	}
	var parts []string
	for i := 0; i < len(hexDigits); i += group {
		parts = append(parts, hexDigits[i:i+group])
	}
	return strings.Join(parts, sep)
}

// CoAAcknowledgedUnknownSessionWarning is the ACK-to-a-nonexistent-session
// notice, in one place so a fake standing in for this driver cannot restate it
// wrongly — the same rule AccessPointUnconfirmedWarning follows.
func CoAAcknowledgedUnknownSessionWarning(sessionID string) ProbeWarning {
	return ProbeWarning{
		Kind:   WarnCoAAcknowledgedUnknownSession,
		Params: map[string]any{"sessionId": sessionID},
		Detail: "the NAS acknowledged a Disconnect-Request for a session id it cannot have issued. " +
			"RFC 5176 §3.1 expects Error-Cause 503 Session-Context-Not-Found. A device that ACKs " +
			"whatever it is sent reports success on a no-op, so no revocation through this driver " +
			"can be trusted to have revoked anything.",
	}
}
