package portal

import (
	"errors"
	"time"
)

// The portal's own nouns: a browser bound to a MAC, and the grant that browser
// holds.
//
// ── why a grant is not a session ─────────────────────────────────────────────
//
// `radacct` is FreeRADIUS's and records what the *network* observed. A grant
// records what the *venue decided*, and the two are not the same object and do
// not have the same lifetime: a grant exists the instant a voucher is claimed,
// before any accounting row can exist, and on the controller-API path there may
// be no accounting row ever (`docs/rules/radius.md` — no Access-Accept means
// no channel to ask for interim updates). A product that read a grant out of
// `radacct` would show a guest "not connected" for the whole of the window
// between pressing the button and the first Interim-Update, which on the
// 300-second floor is five minutes of a one-hour voucher.
//
// So grants are ours, they are written by this package, and nothing here ever
// writes `radacct` or `radpostauth`.

// Method is how a guest was let on. The three the contract offers, and no more:
// social login cannot work inside a Captive Network Assistant that keeps no
// cookies and cannot open a window.
type Method string

const (
	MethodVoucher      Method = "voucher"
	MethodAccount      Method = "account"
	MethodClickThrough Method = "click-through"
)

// State is a grant's state as read at a moment.
//
// **It is derived, not stored, and the asymmetry is the point.** Revocation is
// an event: something happened, somebody did it, and it is written down when it
// happens. Expiry is the clock passing a number and nothing has to happen for it
// to be true — so a stored `expired` is a value that is wrong for however long it
// takes a timer to fire, and a timer that does not fire (a restarted process, a
// crashed reaper) leaves a guest reading "connected" beside a timestamp that ran
// out an hour ago.
type State string

const (
	StateConnected State = "connected"
	StateExpired   State = "expired"
	StateRevoked   State = "revoked"

	// StateNotOnNetwork — the venue granted and the network was never opened.
	//
	// It is the one state that is not about the grant at all. `expired` and
	// `revoked` are facts about what the venue decided; this is a fact about
	// what the network did with the decision, and it exists because the two can
	// disagree: invariant 4 requires the `303` to be sent **before** the NAS is
	// asked to authorize, so by the time the controller refuses, the guest is
	// already reading the connected page.
	//
	// Derived from `Delivery`, which is stored — see there.
	StateNotOnNetwork State = "not-on-network"
)

// Delivery is what happened when the network was asked to let this grant's
// device on.
//
// **Stored rather than derived, on the same rule as `RevokedAt`.** Invariant 9
// splits the two: expiry is the clock passing a number and nothing has to happen
// for it to be true, so deriving it is the only honest reading. This is the other
// kind — a call was made, it returned, and that is an event with a moment. There
// is nothing about *now* from which it could be recomputed, and asking the
// controller again to find out would be a second authorization attempt disguised
// as a read.
type Delivery string

const (
	// DeliveryUnknown is the zero value, and it covers **two** situations that
	// this package cannot tell apart and must not pretend to.
	//
	// The first is ordinary and lasts milliseconds: the driver call has not
	// returned yet. The guest's connected page is fetched over a separate
	// request that races it by construction, and the redirect is flushed first
	// precisely so it wins — so this is what a *successful* redemption looks
	// like for the length of one controller round trip.
	//
	// The second is a deployment whose `Authorizations` port is not wired, where
	// nothing will ever record an outcome.
	//
	// Both read as `connected`. *Nothing has told us it failed* is not *it
	// failed*, and rendering an alarming page for the common success case would
	// cost more guests than it saved.
	DeliveryUnknown Delivery = ""

	// DeliveryOpen — the driver reported the client was let through.
	DeliveryOpen Delivery = "open"

	// DeliveryFailed — the driver could not let the client through. The grant is
	// real and the voucher is spent; the device is not on the network.
	DeliveryFailed Delivery = "failed"
)

// Enforcement says who is actually applying the plan's limits.
//
// Stored on the grant rather than derived at read time, because it is a fact
// about the moment the guest was let on: it is the driver's `HardQuota`
// capability as it was when the decision was made. Re-deriving it later would
// report a device's capabilities today against a grant issued last week, and the
// number beside it would not have changed.
type Enforcement string

const (
	// EnforcedByNAS — the access point enforces it and the figure is authoritative.
	EnforcedByNAS Enforcement = "nas"
	// EnforcedBySampling — enforced from accounting, with overshoot. The
	// `Acct-Interim-Interval` floor is 300 s, so the overshoot is real and the
	// UI says so.
	EnforcedBySampling Enforcement = "foyer-sampled"
	// EnforcedByNothing — nothing on the network is applying a limit. Either the
	// plan states none, or it states one the driver cannot honour. The UI must
	// not present it as a cap.
	EnforcedByNothing Enforcement = "unenforced"
)

// Plan is what the venue granted. **Not what the guest has used** — that number
// comes from accounting, arrives every five minutes at best, and on some
// deployments never arrives at all.
type Plan struct {
	Name     string
	NameLang string

	DurationMinutes *int
	DataAllowanceMB *int
	UpKbps          *int
	DownKbps        *int

	EnforcedBy Enforcement
}

// Limited reports whether this plan states any limit at all. A plan that states
// none has nothing for anybody to enforce, which is a different situation from a
// limit nobody is applying and is worth being able to tell apart.
func (p Plan) Limited() bool {
	return p.DurationMinutes != nil || p.DataAllowanceMB != nil ||
		p.UpKbps != nil || p.DownKbps != nil
}

// Grant is one decision to let one device onto the network.
type Grant struct {
	ID     string
	SiteID string

	// ClientMAC is normalised — lower case, colon-separated. It is a weak
	// identifier and is treated as one: iOS 14+ and Android 10+ present a
	// per-SSID private address by default, so this is a pseudonym rather than a
	// device (`docs/rules/security.md` §6). The grant binds to the *identity*
	// that was presented; the MAC is how the network finds it again.
	ClientMAC string
	APMAC     string
	NASID     string

	Method Method
	// VoucherID and Username are mutually exclusive and both may be empty:
	// click-through has neither, which is exactly why `VerifyPresence` is
	// load-bearing on its own for that method.
	VoucherID string
	Username  string
	// Phone is the E.164 number a `phone` guest verified, and empty for every
	// other method. It is the identity the grant binds to (§6).
	Phone string

	Plan      Plan
	GrantedAt time.Time
	ExpiresAt *time.Time
	RevokedAt *time.Time

	// Delivery is whether the network was actually opened. Zero value is
	// `DeliveryUnknown` on purpose: a store that does not carry the column yet
	// reads as *nothing has told us it failed*, which is the same answer a
	// grant one millisecond old gets, and both must render `connected`.
	Delivery Delivery
}

// StateAt reads the grant's state at a moment. See State.
//
// The order is deliberate where the last two could both be true. A grant that
// expired an hour ago *and* whose authorization failed is `expired`: the guest's
// time is gone either way, and *your hour ran out* is the sentence that stops
// somebody asking staff to fix a network that was never their problem.
func (g Grant) StateAt(now time.Time) State {
	if g.RevokedAt != nil {
		return StateRevoked
	}
	if g.ExpiresAt != nil && !g.ExpiresAt.After(now) {
		return StateExpired
	}
	if g.Delivery == DeliveryFailed {
		return StateNotOnNetwork
	}
	return StateConnected
}

// Session is one browser, bound to one MAC.
//
// **The binding is the whole point** (`docs/rules/security.md` §4). UniFi
// hands the portal `ap`, `id`, `t`, `url` and `ssid` as unsigned query
// parameters that anyone within radio range can supply with values of their
// choosing, so the portal must decide once — at the front door, against the
// driver — which MAC this browser is, and refuse to let that change mid-flow.
//
// Everything here lives server-side. The Captive Network Assistant's data store
// is ephemeral: cookies, `localStorage`, IndexedDB and service workers are
// memory-only and destroyed when the window closes, so the only thing the client
// carries is an opaque token, and the only thing that token does is name a row.
type Session struct {
	// TokenHash is sha256 of the cookie value. The cookie is a bearer
	// credential for a MAC binding, so a database dump must not contain a live
	// one — the same reason `AdminSession` stores a hash.
	TokenHash string

	SiteID string
	NASID  string
	// VendorSiteID is the controller's own site identifier — UniFi's, not
	// Foyer's. It is carried separately because the driver takes it and the
	// database keys on the other one; a single `SiteID` field would be right for
	// whichever caller was written first.
	VendorSiteID string
	// ControllerID is which controller manages the access point this browser
	// arrived through, resolved at the front door and stored with the rest of the
	// placement. It is here so that the controller which confirmed the device
	// present is the controller which is asked to let it on: resolving it again
	// between those two calls is a window in which they can differ.
	ControllerID string
	// Accounting is the placement's accounting expectation, carried for the same
	// reason as the rest of it: `Plan.EnforcedBy` is a fact about the moment the
	// guest was let on, and this is one of its three inputs.
	Accounting AccountingExpectation
	ClientMAC  string
	APMAC      string
	SSID       string

	// OriginalURL is where the client was going, from the NAS redirect. It is
	// attacker-controlled at source and is allowlisted before it is stored, not
	// only before it is rendered — a value that never enters the database in a
	// dangerous shape cannot be rendered in one by a future reader who forgets.
	OriginalURL string

	// GrantID names the grant this browser holds, once it holds one.
	GrantID string

	// Source is the TCP peer of the request this session was resolved on, as
	// `RemoteAddr` gave it — **never stored, never a header**. `Resolve` and
	// `Lookup` set it on every request, and a session read from the store has it
	// empty. It exists because it is the one thing about a pre-authentication
	// request that its sender cannot choose without completing a handshake from
	// it, which makes it the key the rate limit spends before `VerifyPresence`
	// has vouched for anything (see Service.allowSource).
	Source string

	CreatedAt time.Time
	// ExpiresAt is the pre-authentication time limit §5 asks for. There is no
	// identity yet, so nothing else bounds this row.
	ExpiresAt time.Time
}

// Placement is where this browser is, as one value.
//
// The three ids are stored on the session because they are resolved once, at the
// front door, from one row — and handing them to the driver seam as a value is what
// stops each driver call resolving them again and possibly differently.
func (s Session) Placement() Placement {
	return Placement{
		SiteID:       s.SiteID,
		NASID:        s.NASID,
		VendorSiteID: s.VendorSiteID,
		ControllerID: s.ControllerID,
		Accounting:   s.Accounting,
	}
}

// Errors the ports return. Each one maps onto exactly one `error.*` key in the
// portal's own message catalogue, and `reason_test.go` holds that mapping to the
// catalogue file rather than to a comment.
var (
	// ErrNotFound: no such row. Used by the session and grant stores.
	ErrNotFound = errors.New("portal: not found")

	// The four ways a voucher can fail to be claimable. They are separate
	// errors because the guest is told four different things: "check each
	// character" and "ask staff for another one" are different instructions and
	// the wrong one wastes somebody's time at a counter.
	ErrVoucherUnknown = errors.New("portal: no such voucher")
	ErrVoucherUsed    = errors.New("portal: voucher already used")
	ErrVoucherExpired = errors.New("portal: voucher expired")
	ErrVoucherRevoked = errors.New("portal: voucher revoked")

	// ErrBadCredentials never distinguishes an unknown username from a wrong
	// password. Distinguishing them publishes which accounts exist, on an
	// endpoint reachable by anyone within radio range.
	ErrBadCredentials = errors.New("portal: bad credentials")
	// ErrAccountDisabled is deliberately *not* folded into ErrBadCredentials.
	//
	// The account holder is standing in the venue and the staff can turn it back
	// on; telling them "wrong password" sends them to reset a password that is
	// correct. This does publish that the account exists — to somebody who has
	// already presented its correct password, which is not a disclosure.
	ErrAccountDisabled = errors.New("portal: account disabled")
)
