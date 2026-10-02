package nas

import (
	"fmt"
	"time"
)

// Cisco — the CoA family member that can actually reauthorize, and the reason
// Vendor-Specific encoding had to exist before any of this could be built.
//
// **No Cisco device has been near this project**, and nothing below changes
// that. What is known is narrower and is worth keeping narrow: the
// *wire numbers* are DOCUMENTED, read from FreeRADIUS's own dictionary. Every
// behaviour — the command strings, the MAC spelling, whether the NAS honours any
// of it — is still a guess with the measurement that would settle it named
// beside it.

func init() {
	RegisterShippedDriver(VendorCisco, func() NASDriver {
		d, err := NewCoADriver(CoAConfig{Vendor: CiscoCoAVendor{}, Host: capabilityReadHost, Timeout: coaReadTimeout})
		if err != nil {
			panic("nas: the Cisco capability-read driver does not build: " + err.Error())
		}
		return d
	})
}

// Cisco's IANA Private Enterprise Number and the attribute number of
// Cisco-AVPair inside it.
//
// **DOCUMENTED**: `VENDOR Cisco 9` and `Cisco-AVPair 1`, read out of
// `/usr/share/freeradius/dictionary.cisco` inside the image `lab/` builds. See
// FreeRADIUSDictionaryCitation.
//
// The measurement that settles them needs one command and no hardware. **It
// matters because a wrong number fails silently:** it produces a syntactically
// valid attribute the NAS ignores, so the device answers ACK and does nothing —
// through the one channel a typed `Error-Cause` cannot see. No amount of correct
// NAK handling would surface it.
//
// What the dictionary settles ends here. That a Cisco NAS *honours* the AV-pair
// values below is still UNVERIFIED, and no dictionary can say otherwise.
const (
	ciscoVendorID       uint32 = 9
	ciscoAVPairAttrType uint8  = 1
)

// Cisco AV-pair commands. `docs/protocol-reality.md` names the first one, as
// UNVERIFIED; the other two are this author's reading of Cisco's subscriber-service AV-pairs and nothing more.
const (
	ciscoCmdReauthenticate   = "subscriber:command=reauthenticate"
	ciscoReauthTypeRerun     = "subscriber:reauthenticate-type=rerun"
	ciscoAVAuditSessionIDKey = "audit-session-id"
)

// CiscoCoAVendor renders Cisco's half of the RFC 5176 family.
type CiscoCoAVendor struct{}

func (CiscoCoAVendor) Vendor() Vendor { return VendorCisco }

// Identify prefers `audit-session-id` and falls back to Acct-Session-Id, then
// to the MAC.
//
// The order is the point and it is not arbitrary. `docs/rules/nas-drivers.md`
// lists `AuditSessionID` as *"Cisco IOS-XE: Cisco-AVPair audit-session-id"* and
// `AcctSessionID` as the *"Cisco fallback"* — in that order — and the reason
// shows up one function down: the MAC is the identifier whose spelling nobody
// here has measured, so it is the last resort rather than the first.
//
// Only one identifier is sent, not all of them. RFC 5176 §2.3 requires a NAS to
// match on **every** session-identification attribute in the request, so sending
// three is three chances to disagree with the device about a format and none to
// agree. That is the opposite of `Session`'s "carry all of it" rule, and the two
// are not in conflict: *carry* all of it, *send* the one this platform keys on.
func (CiscoCoAVendor) Identify(s Session) (Session, []VendorAttribute, error) {
	// NAS-IP-Address and NAS-Identifier travel where present. They identify the
	// device being asked rather than the session, so they never widen a match.
	ident := Session{NASIPAddress: s.NASIPAddress, NASIdentifier: s.NASIdentifier}

	if s.AuditSessionID != "" {
		return ident, []VendorAttribute{
			VendorString(ciscoVendorID, ciscoAVPairAttrType,
				avPair(ciscoAVAuditSessionIDKey, s.AuditSessionID)),
		}, nil
	}
	if s.AcctSessionID != "" {
		ident.AcctSessionID = s.AcctSessionID
		return ident, nil, nil
	}
	if mac := macWith(s.CallingStationID, ".", 4, false); mac != "" {
		// IOS-XE's default spelling. It is a device setting, not a constant, and
		// this is the guess most likely to cost a hardware session.
		ident.CallingStationID = mac
		return ident, nil, nil
	}
	return Session{}, nil, fmt.Errorf(
		"nas: cisco: nothing to identify the session with — no audit-session-id, no Acct-Session-Id, and %q is not a MAC",
		s.CallingStationID)
}

// Reauthorize renders the command that makes an IOS-XE subscriber session re-run
// authorization.
//
// It carries **no limits**, and that is a fact about the platform rather than an
// omission: the authorization itself comes back in the Access-Accept that the
// reauthentication triggers, rendered by `CiscoProfile`. So the two abstractions
// meet here — the driver makes the NAS ask, and the profile answers.
//
// `reauthenticate-type=rerun` rather than `last` because a portal login has
// changed what the answer should be. Asking for the *last* answer would replay
// the captive one and leave the guest looking at the portal they just finished.
func (CiscoCoAVendor) Reauthorize(_ Session, _ AuthorizeRequest) ([]VendorAttribute, error) {
	return []VendorAttribute{
		VendorString(ciscoVendorID, ciscoAVPairAttrType, ciscoCmdReauthenticate),
		VendorString(ciscoVendorID, ciscoAVPairAttrType, ciscoReauthTypeRerun),
	}, nil
}

// Capabilities — three of the seven are false because Cisco's answer to them is
// configuration on the device that Foyer names and cannot create.
func (CiscoCoAVendor) Capabilities() Capabilities {
	return Capabilities{
		// The reauthenticate command exists and its refusal is observable: a NAS
		// that does not implement CoA-Request answers Error-Cause 405 and this
		// driver returns it as a *CoAError. That observability is why this is
		// true while still unmeasured — an unmeasured claim you can watch fail is
		// a different thing from one you cannot.
		CanAuthorizeGuest: true,
		CanDeauthGuest:    true,
		// Cisco enforces a Session-Timeout from the Access-Accept; nothing here
		// makes the NAS count bytes on Foyer's behalf.
		HardQuota: false,
		// `CiscoProfile` renders no rate attribute and this driver sends none,
		// so there is nowhere for the numbers to go. Showing the fields would be
		// a control that silently does nothing.
		BandwidthLimits: false,
		// `url-redirect-acl` names an ACL that must already exist on the NAS.
		// The device has a garden; the *driver* cannot program one, which is
		// what this field claims. Same narrow reading as UniFi's.
		WalledGarden:    WalledGardenNone,
		LiveReauthorize: true,
		RotateSecret:    false,
	}
}

func (CiscoCoAVendor) CapabilityEvidence() CapabilityEvidence {
	const settle = "Settled by: a Cisco WLC or IOS-XE NAS in lab/, one CoA-Request per claim, with the " +
		"session state read off the device afterwards rather than inferred from the ACK."
	return CapabilityEvidence{
		CapCanAuthorizeGuest: {
			Evidence: EvidenceUnverified,
			Note: "Cisco-AVPair = " + ciscoCmdReauthenticate + " is named in docs/protocol-reality.md. " +
				"The attribute's ENCODING is DOCUMENTED (see " +
				"AttributeEvidence); the COMMAND STRING is not, and that a subscriber session re-runs " +
				"authorization when it arrives is not. " + settle,
		},
		CapCanDeauthGuest: {
			Evidence: EvidenceUnverified,
			Note: "RFC 5176 Disconnect-Request is DOCUMENTED; that this device implements it, on this " +
				"port, matching a session on the identifier this driver chose, is not. " + settle,
		},
		CapHardQuota: {
			Evidence: EvidenceUnverified,
			Note: "Believed false: no CoA attribute here asks the NAS to count bytes. " +
				"Settled by: whether an IOS-XE subscriber session honours a byte cap at all, and from which attribute.",
		},
		CapBandwidthLimits: {
			Evidence: EvidenceUnverified,
			Note: "Believed false because neither CiscoProfile nor this driver renders a rate attribute. " +
				"Settled by: whether Cisco's QoS AV-pairs can carry a per-session rate from a RADIUS reply, " +
				"which would make this a profile change and not a driver one.",
		},
		CapWalledGarden: {
			Evidence: EvidenceUnverified,
			Note: "The device has a garden — url-redirect-acl names one — and no interface here programs it. " +
				"Settled by: whether any Cisco management API can create an ACL, which would move this to ip-range.",
		},
		CapLiveReauthorize: {
			Evidence: EvidenceUnverified,
			Note: "RFC 5176 §3.1 defines CoA-Request as changing a live session and gives a NAS that does not " +
				"implement it Error-Cause 405 to say so, which this driver surfaces. That the device honours " +
				"the reauthenticate command is unmeasured. " + settle,
		},
		CapRotateSecret: {
			Evidence: EvidenceUnverified,
			Note: "No interface here pushes a shared secret to a Cisco NAS. The narrow claim: this gates only " +
				"the push — generating a secret stays Foyer's. Settled by: whether IOS-XE's management API can " +
				"set a RADIUS secret without a full config push.",
		},
	}
}

// capabilityReadHost is the placeholder a driver built only to read its own
// claims off is pointed at, mirroring capabilityReadURL for the HTTP drivers.
// Nothing dials it: Capabilities() returns constants.
const capabilityReadHost = "capabilities.invalid"

// coaReadTimeout keeps a capability-read driver from ever waiting on a network
// if somebody does call through one by mistake.
const coaReadTimeout = time.Millisecond

// AttributeEvidence records how Cisco-AVPair's encoding is known, as distinct
// from what a device does with the values inside it.
//
// The split is the whole point of this map existing. The wire numbers are
// DOCUMENTED against FreeRADIUS's own dictionary; the AV-pair *keys* Foyer puts
// in the value — `audit-session-id`, `subscriber:command=reauthenticate`,
// `subscriber:reauthenticate-type=rerun` — are a reading of Cisco's
// subscriber-service documentation and remain UNVERIFIED. The dictionary knows
// how to spell the attribute. It has no opinion about what belongs in it.
func (CiscoCoAVendor) AttributeEvidence() AttributeEvidence {
	return AttributeEvidence{
		"Cisco-AVPair": {
			Evidence: EvidenceDocumented,
			Note: "VENDOR Cisco 9, Cisco-AVPair 1 — " + FreeRADIUSDictionaryCitation +
				" This covers the ENCODING only. The AV-pair keys carried inside the value " +
				"(audit-session-id, " + ciscoCmdReauthenticate + ", " + ciscoReauthTypeRerun +
				") are UNVERIFIED, as is the MAC spelling this vendor falls back to. " +
				"Settled by: a Cisco WLC or IOS-XE NAS answering one CoA-Request per claim.",
		},
	}
}
