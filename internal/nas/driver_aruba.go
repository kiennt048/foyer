package nas

import "fmt"

// Aruba — the CoA family member that can revoke and cannot reauthorize, because
// of a gap in `AuthorizeRequest` rather than anything about Aruba.
//
// **No Aruba device has been near this project.** The wire numbers are
// DOCUMENTED from FreeRADIUS's own dictionary; every behaviour is
// still a guess. The two are kept in separate maps for that reason.

func init() {
	RegisterShippedDriver(VendorAruba, func() NASDriver {
		d, err := NewCoADriver(CoAConfig{Vendor: ArubaCoAVendor{}, Host: capabilityReadHost, Timeout: coaReadTimeout})
		if err != nil {
			panic("nas: the Aruba capability-read driver does not build: " + err.Error())
		}
		return d
	})
}

// Aruba's IANA Private Enterprise Number and the attribute number of
// `Aruba-User-Role` inside it.
//
// **DOCUMENTED**: `VENDOR Aruba 14823` and `Aruba-User-Role 1`, read
// from `/usr/share/freeradius/dictionary.aruba` inside the image `lab/` builds.
// See FreeRADIUSDictionaryCitation.
//
// **Nothing sends either of them yet, and that is not an oversight.** Aruba's
// authorization is the user role, `AuthorizeRequest` has no role field, and
// Reauthorize below refuses for that reason. The numbers are kept because they
// are now facts rather than guesses, and because the moment `AuthorizeRequest`
// gains a `Role` this driver is one line from working — knowing them turns that
// from a research task into an edit.
const (
	arubaVendorID         uint32 = 14823
	arubaUserRoleAttrType uint8  = 1
)

// ArubaCoAVendor renders Aruba's half of the RFC 5176 family.
type ArubaCoAVendor struct{}

func (ArubaCoAVendor) Vendor() Vendor { return VendorAruba }

// Identify keys on the MAC, which is what `docs/rules/nas-drivers.md` says
// Aruba does — *"CallingStationID // MAC — Aruba, UniFi"*.
//
// The spelling is unformatted lowercase, which is a guess. See macWith: this is
// the largest guess in the family and the cheapest to measure.
func (ArubaCoAVendor) Identify(s Session) (Session, []VendorAttribute, error) {
	ident := Session{NASIPAddress: s.NASIPAddress, NASIdentifier: s.NASIdentifier}
	if mac := macWith(s.CallingStationID, "", 12, false); mac != "" {
		ident.CallingStationID = mac
		return ident, nil, nil
	}
	if s.AcctSessionID != "" {
		ident.AcctSessionID = s.AcctSessionID
		return ident, nil, nil
	}
	return Session{}, nil, fmt.Errorf(
		"nas: aruba: nothing to identify the session with — %q is not a MAC and there is no Acct-Session-Id",
		s.CallingStationID)
}

// Reauthorize refuses, and the reason is a finding about a type rather than
// about Aruba.
//
// # `AuthorizeRequest` has no role, and on Aruba the role *is* the authorization
//
// On AOS a client's access is the user role it is in — `ArubaProfile` renders
// `Aruba-User-Role` and calls it *"the load-bearing half"* for exactly this
// reason. Changing a live session's authorization therefore means sending a new
// role in a CoA-Request, and `AuthorizeRequest` carries `TimeLimitMinutes`,
// `DataUsageLimitMB` and two rate limits and **no role at all**. It was shaped
// around UniFi's `authorize-guest`, which takes those four and nothing else.
//
// So this could be made to compile by sending a CoA-Request with only session
// identification in it, which Aruba would answer with Error-Cause 402
// Missing-Attribute if it is careful and by ignoring if it is not. Refusing here
// is the same answer, arrived at without a packet and with a sentence an
// operator can read.
//
// **Reported, not worked around.** Widening `AuthorizeRequest` with a `Role`
// field is a change every driver and every caller sees, and it is a decision for
// whoever owns the portal grant path — not something to slip in from a vendor
// file.
func (ArubaCoAVendor) Reauthorize(_ Session, _ AuthorizeRequest) ([]VendorAttribute, error) {
	return nil, &UnsupportedActionError{
		Vendor: VendorAruba,
		Action: "reauthorize a live session",
		Reason: "on AOS the authorization is the user role, carried as Aruba-User-Role, and " +
			"AuthorizeRequest has no role field — it carries UniFi's four limits and nothing else",
	}
}

// Capabilities. Revocation only, and every false below has a named cause rather
// than being a shrug.
func (ArubaCoAVendor) Capabilities() Capabilities {
	return Capabilities{
		// False because there is nothing to send, not because Aruba cannot.
		// See Reauthorize.
		CanAuthorizeGuest: false,
		CanDeauthGuest:    true,
		HardQuota:         false,
		// `ArubaProfile` renders no rate attribute and this driver sends none.
		BandwidthLimits: false,
		// The pre-authentication role confines the client and the operator
		// defines it on the controller. The driver programs nothing.
		WalledGarden:    WalledGardenNone,
		LiveReauthorize: false,
		RotateSecret:    false,
	}
}

func (ArubaCoAVendor) CapabilityEvidence() CapabilityEvidence {
	const settle = "Settled by: an AOS controller in lab/ answering a Disconnect-Request keyed on the MAC, " +
		"with the spelling of Calling-Station-Id read out of an Access-Request the controller itself sent."
	return CapabilityEvidence{
		CapCanAuthorizeGuest: {
			Evidence: EvidenceUnverified,
			Note: "Believed false, and for a reason inside Foyer rather than inside Aruba: AuthorizeRequest " +
				"has no role field and on AOS the role is the authorization. Settled by: deciding whether " +
				"AuthorizeRequest gains a Role — a product decision, not a measurement.",
		},
		CapCanDeauthGuest: {
			Evidence: EvidenceUnverified,
			Note:     "RFC 5176 Disconnect-Request is DOCUMENTED; this device, this port and this identifier are not. " + settle,
		},
		CapHardQuota: {
			Evidence: EvidenceUnverified,
			Note: "Believed false: nothing here asks an AOS controller to count bytes for a session. " +
				"Settled by: whether a role can carry a byte cap the controller enforces.",
		},
		CapBandwidthLimits: {
			Evidence: EvidenceUnverified,
			Note: "Believed false because ArubaProfile renders no rate attribute. Settled by: whether AOS " +
				"honours a per-session rate from a RADIUS attribute rather than only from a role's own policy.",
		},
		CapWalledGarden: {
			Evidence: EvidenceUnverified,
			Note: "The pre-authentication role is the garden and the operator defines it. Settled by: whether " +
				"any AOS API creates a role, which would move this off none.",
		},
		CapLiveReauthorize: {
			Evidence: EvidenceUnverified,
			Note: "Follows CanAuthorizeGuest: with no role to carry there is nothing to send, so a " +
				"CoA-Request here would ask AOS to change nothing. Settled by: the same product decision " +
				"about a Role field on AuthorizeRequest, and then by an AOS controller in lab/ moving a " +
				"live client between two roles from a CoA-Request.",
		},
		CapRotateSecret: {
			Evidence: EvidenceUnverified,
			Note: "No interface here pushes a shared secret to an Aruba controller. The narrow claim: this " +
				"gates only the push. Settled by: whether AOS's management API sets a RADIUS secret.",
		},
	}
}

// AttributeEvidence records what the dictionary settled for Aruba, including for
// an attribute this driver does not yet emit.
//
// Recording an encoding nothing sends is the honest shape rather than a tidy
// one. The fact is known; the reason it is unused is a missing field on
// `AuthorizeRequest` rather than a missing measurement. Separating those two is
// the difference between *"nobody has worked out how"* and *"we know how and the
// type cannot carry it"*, and only the second is actionable.
func (ArubaCoAVendor) AttributeEvidence() AttributeEvidence {
	return AttributeEvidence{
		"Aruba-User-Role": {
			Evidence: EvidenceDocumented,
			Note: "VENDOR Aruba 14823, Aruba-User-Role 1 — " + FreeRADIUSDictionaryCitation +
				" ENCODING only, and NOT CURRENTLY EMITTED: AuthorizeRequest has no role field, so " +
				"there is no role to put in it. That AOS moves a live session between roles on a " +
				"CoA-Request carrying this attribute is UNVERIFIED, as is the MAC spelling this vendor " +
				"identifies on. Settled by: the product decision about a Role field, then an AOS " +
				"controller answering one.",
		},
	}
}
