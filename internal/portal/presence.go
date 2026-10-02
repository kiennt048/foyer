package portal

import (
	"github.com/kiennt048/foyer/internal/nas"
)

// What the portal does about a presence answer.
//
// ── three situations behind one false ────────────────────────────────────────
//
// `NASDriver.VerifyPresence` answers a bool, and the bool is the right gate: it
// is false unless the driver confirmed this MAC is associated, here, now and
// still unauthorized. But three different situations produce `false`, and a
// portal that treats them alike renders all three as **"Device not present on
// network"**:
//
//   - the device really is not here — the guest's own situation, and the only
//     one that sentence describes;
//   - the device is **already authorized** — a guest who is online being told
//     they are not, on a page inviting them to type a voucher they do not need;
//   - the **controller could not be reached** — the venue's outage, rendered as
//     the guest's fault, on the one screen where a guest has no way to tell the
//     difference and no way to escalate.
//
// `PresenceDetailer` exists so a driver can say which. This is the portal
// deciding what to do about each, and it is a separate function from the driver
// so that it can be tested without one.

// Verdict is what the portal does next.
type Verdict int

const (
	// VerdictProceed — the driver confirmed the device. This is the only value
	// that may be followed by a grant.
	VerdictProceed Verdict = iota

	// VerdictAlreadyOn — the device is already authorized on this network.
	//
	// Sent to the connected page rather than refused, and **no voucher is
	// claimed**: burning a single-use code for a guest who is already online is
	// a support call that ends with staff issuing a second voucher.
	VerdictAlreadyOn

	// VerdictRefuse — the driver will not vouch for this device, so no grant may
	// follow.
	//
	// **It is one action over more than one situation**, which is why the
	// sentence the guest reads is not derived from it: a device that never
	// associated and a device associated under an access point the redirect did
	// not name are both refused here and are told different things. `Explain`
	// reads the `nas.Presence` for that reason.
	VerdictRefuse

	// VerdictUnavailable — the driver could not answer.
	//
	// **It is never a yes.** Fail-closed is not negotiable on the grant path: a
	// controller that cannot be reached is a controller that cannot confirm a
	// MAC, and §4 makes confirmation mandatory before any grant. What changes is
	// only what the guest is *told* — the venue's fault, not theirs.
	VerdictUnavailable
)

// Classify turns a driver's answer into what the portal does.
//
// The error is part of the input rather than handled by the caller, because
// "unreachable" arrives as `PresenceUnavailable` **and** a non-nil error, and a
// caller that checked only one of the two would classify an outage as a refusal
// half the time — the three situations above collapsing back into one.
func Classify(p nas.Presence, err error) Verdict {
	switch {
	case err != nil:
		// An error is an outage whatever the outcome says. A driver that
		// returned a confident `OK` beside an error would be a driver bug, and
		// trusting the OK is how a bug becomes a grant.
		return VerdictUnavailable
	case p.Outcome == nas.PresenceUnavailable:
		return VerdictUnavailable
	case p.Outcome == nas.PresenceAlreadyAuthorized:
		return VerdictAlreadyOn
	case p.OK:
		return VerdictProceed
	default:
		return VerdictRefuse
	}
}

// Explain is what the guest is told about an answer that is not a grant.
//
// **It takes the presence and not the verdict, and that is the whole of it.**
// The verdict says what the portal *does*, and two answers the portal handles
// identically — refuse, no grant — are not the same conversation with the person
// holding the phone. A method on `Verdict` would make the sentence a function of
// the action, so every refusal would necessarily get one sentence and the
// sentence would have to be the widest one that fitted.
//
// Two lines, and each matters:
//
//   - `VerdictUnavailable` answers `generic`, never `not-present`. "This device
//     is not on the network" sends a guest to toggle Wi-Fi for twenty minutes;
//     "Something went wrong. Try again." sends them to the counter, which is
//     where somebody can see that the controller is down.
//   - `PresenceAPMismatch` answers `ap-unconfirmed`, never `not-present`. The
//     guest **is** on the network — the controller returned a client for that
//     MAC — and telling them otherwise is the same error one step in: a true
//     statement about a different situation. See `ReasonAPUnconfirmed`.
//
// **`PresenceSiteMismatch` deliberately stays `not-present`, and the line is not
// arbitrary.** Access points inside a site are one network, so "not on this
// network" is false for a device under the wrong one. Sites are different
// networks, so for a client the controller places in another site the sentence
// is true as written.
//
// The unavailable branch asks `Classify` rather than re-testing `err != nil ||
// p.Outcome == PresenceUnavailable`. A second copy of that rule here would be
// free to drift from the one that decides whether a grant happens, and the two
// disagreeing is how a guest gets told their device is missing while the venue's
// controller is down.
func Explain(p nas.Presence, err error) Reason {
	switch {
	case Classify(p, err) == VerdictUnavailable:
		return ReasonGeneric
	case p.Outcome == nas.PresenceAPMismatch:
		return ReasonAPUnconfirmed
	default:
		return ReasonNotPresent
	}
}

// VerifiedPresence is what `Classify` had to see to return VerdictProceed. It
// exists so a caller cannot accidentally treat any other verdict as permission
// by writing `verdict != VerdictRefuse`.
func (v Verdict) MayGrant() bool { return v == VerdictProceed }
