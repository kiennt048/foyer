package nas

import (
	"errors"
	"fmt"
)

// MikroTik — cheap, common in small venues, and the family member that
// separates *authorize* from *reauthorize* most clearly.
//
// **No MikroTik device has been near this project.** The wire numbers are
// DOCUMENTED from FreeRADIUS's own dictionary; the format, the
// direction of the rate pair and the MAC spelling are all still guesses.

func init() {
	RegisterShippedDriver(VendorMikroTik, func() NASDriver {
		d, err := NewCoADriver(CoAConfig{Vendor: MikroTikCoAVendor{}, Host: capabilityReadHost, Timeout: coaReadTimeout})
		if err != nil {
			panic("nas: the MikroTik capability-read driver does not build: " + err.Error())
		}
		return d
	})
}

// MikroTik's IANA Private Enterprise Number and the attribute number of
// Mikrotik-Rate-Limit inside it.
//
// **DOCUMENTED**: `VENDOR Mikrotik 14988` and
// `Mikrotik-Rate-Limit 8`, read from `/usr/share/freeradius/dictionary.mikrotik`
// inside the image `lab/` builds. See FreeRADIUSDictionaryCitation.
//
// The dictionary says how to spell the attribute and nothing about what goes in
// it. **The value's format and the direction of the pair remain the open
// question**, and it is the one that matters most here: a swapped pair is a rate
// limit applied to the wrong direction that looks exactly like one that works.
// See mikrotikRateLimit.
const (
	mikrotikVendorID          uint32 = 14988
	mikrotikRateLimitAttrType uint8  = 8
	mikrotikMACSeparator             = ":"
	mikrotikMACGroup                 = 2
	mikrotikMACUppercase      bool   = true
)

// MikroTikCoAVendor renders MikroTik's half of the RFC 5176 family.
type MikroTikCoAVendor struct{}

func (MikroTikCoAVendor) Vendor() Vendor { return VendorMikroTik }

// Identify prefers Acct-Session-Id, then User-Name, then the MAC.
//
// The MAC is last for the reason it is last on Cisco: its spelling is the least
// measured thing here. RouterOS commonly presents Calling-Station-Id as
// colon-separated uppercase, which is what this renders, and which is a guess.
func (MikroTikCoAVendor) Identify(s Session) (Session, []VendorAttribute, error) {
	ident := Session{NASIPAddress: s.NASIPAddress, NASIdentifier: s.NASIdentifier}
	switch {
	case s.AcctSessionID != "":
		ident.AcctSessionID = s.AcctSessionID
	case s.UserName != "":
		ident.UserName = s.UserName
	default:
		mac := macWith(s.CallingStationID, mikrotikMACSeparator, mikrotikMACGroup, mikrotikMACUppercase)
		if mac == "" {
			return Session{}, nil, fmt.Errorf(
				"nas: mikrotik: nothing to identify the session with — no Acct-Session-Id, no User-Name, and %q is not a MAC",
				s.CallingStationID)
		}
		ident.CallingStationID = mac
	}
	return ident, nil, nil
}

// Reauthorize carries new rate limits to a session that already exists.
//
// # This is the whole of what a MikroTik CoA can do here, and the boundary is real
//
// RFC 5176 changes a session; it does not create one. A hotspot client that has
// not logged in has no session to change, and its authorization is the
// Access-Accept the router asks for when the guest submits the hotspot login —
// `MikroTikProfile`'s half, not this one's. So `CanAuthorizeGuest` is false and
// `AuthorizeGuest` refuses in as many words, while this method does the thing
// the protocol actually offers.
//
// A request with no rate limits in it has nothing to carry. That is refused
// rather than sent: a CoA-Request with only session identification asks the NAS
// to change nothing, and the answers to that are Error-Cause 402 on a careful
// device and an ACK-with-no-effect on a careless one — success on a no-op, which
// is the failure this family is written against.
func (MikroTikCoAVendor) Reauthorize(_ Session, req AuthorizeRequest) ([]VendorAttribute, error) {
	rate := mikrotikRateLimit(req.RxRateLimitKbps, req.TxRateLimitKbps)
	if rate == "" {
		return nil, errors.New(
			"nas: mikrotik: this request carries no rate limit, and Mikrotik-Rate-Limit is the only " +
				"thing a CoA-Request here can change — sending it would ask the NAS to change nothing")
	}
	return []VendorAttribute{
		VendorString(mikrotikVendorID, mikrotikRateLimitAttrType, rate),
	}, nil
}

// Capabilities. The interesting pair is CanAuthorizeGuest false beside
// LiveReauthorize true, and it is the narrow truth rather than an inconsistency:
// this driver can change a live session and cannot create one.
func (MikroTikCoAVendor) Capabilities() Capabilities {
	return Capabilities{
		// See Reauthorize. There is no RFC 5176 request that creates a session.
		CanAuthorizeGuest: false,
		CanDeauthGuest:    true,
		HardQuota:         false,
		// True, and it is the one true BandwidthLimits in the CoA family:
		// `Mikrotik-Rate-Limit` is rendered by the profile into the Access-Accept
		// and by this driver into a CoA-Request, from one function so the two
		// cannot disagree.
		BandwidthLimits: true,
		// The hotspot's walled garden is `ip hotspot walled-garden` on the
		// router. The driver programs nothing.
		WalledGarden:    WalledGardenNone,
		LiveReauthorize: true,
		RotateSecret:    false,
	}
}

func (MikroTikCoAVendor) CapabilityEvidence() CapabilityEvidence {
	const settle = "Settled by: a RouterOS hotspot in lab/ or on a hAP, one CoA-Request per claim, with the " +
		"session read back off the router — and with a throughput test in both directions for the rate pair, " +
		"because MikroTik documents rx-rate/tx-rate from the ROUTER's point of view and this renders " +
		"ReplyDecision.RxKbps as the client-facing direction."
	return CapabilityEvidence{
		CapCanAuthorizeGuest: {
			Evidence: EvidenceUnverified,
			Note: "Believed false: RFC 5176 changes a session and cannot create one, and a hotspot client " +
				"that has not logged in has none. Settled by: whether any CoA-Request moves a RouterOS " +
				"hotspot client from unauthenticated to authenticated — expected no.",
		},
		CapCanDeauthGuest: {
			Evidence: EvidenceUnverified,
			Note:     "RFC 5176 Disconnect-Request is DOCUMENTED; this device, this port and this identifier are not. " + settle,
		},
		CapHardQuota: {
			Evidence: EvidenceUnverified,
			Note: "Believed false: nothing here asks the router to count bytes. Settled by: whether " +
				"Mikrotik-Total-Limit or the hotspot user profile enforces a byte cap the RADIUS reply can set.",
		},
		CapBandwidthLimits: {
			Evidence: EvidenceUnverified,
			Note: "Mikrotik-Rate-Limit is named in MikroTikProfile and rendered identically here. Its vendor " +
				"id (14988) and attribute number (8) are DOCUMENTED (see AttributeEvidence); the VALUE " +
				"format and the DIRECTION of the pair are not, and the direction is the one that fails " +
				"silently — a swapped pair throttles the wrong way and reports success. " + settle,
		},
		CapWalledGarden: {
			Evidence: EvidenceUnverified,
			Note: "The garden is `ip hotspot walled-garden` on the router and no interface here writes it. " +
				"Settled by: whether the RouterOS API can, which would move this to fqdn.",
		},
		CapLiveReauthorize: {
			Evidence: EvidenceUnverified,
			Note: "RFC 5176 §3.1 defines CoA-Request as changing a live session, and a NAS that does not " +
				"implement it answers Error-Cause 405, which this driver surfaces rather than swallowing. " +
				"That RouterOS honours a rate change mid-session is unmeasured. " + settle,
		},
		CapRotateSecret: {
			Evidence: EvidenceUnverified,
			Note: "No interface here pushes a shared secret to a router. The narrow claim: this gates only " +
				"the push. Settled by: whether the RouterOS API sets /radius secret, which would make this true.",
		},
	}
}

// AttributeEvidence: the encoding is settled, the value is not, and here the gap
// between those two is wider than anywhere else in this family.
//
// Cisco's open question is whether a command is honoured — a yes or a no. This
// one's is the **direction of a pair**, and being wrong about it produces a rate
// limit that is applied, reported as applied, and throttling the wrong way.
// Nothing in the ACK, the accounting or the console would say so; only a
// throughput test in both directions can.
func (MikroTikCoAVendor) AttributeEvidence() AttributeEvidence {
	return AttributeEvidence{
		"Mikrotik-Rate-Limit": {
			Evidence: EvidenceDocumented,
			Note: "VENDOR Mikrotik 14988, Mikrotik-Rate-Limit 8 — " + FreeRADIUSDictionaryCitation +
				" ENCODING only. The VALUE format (rx-rate[/tx-rate]) and its DIRECTION are UNVERIFIED: " +
				"MikroTik documents the pair from the ROUTER's point of view, so rx is the client's " +
				"upload — the opposite of WISPr-Bandwidth-Max-Down. mikrotikRateLimit assumes the " +
				"caller's Rx is toward the client and renders it second. Settled by: a RouterOS hotspot " +
				"with a throughput test in BOTH directions, because a swapped pair looks like a working one.",
		},
	}
}
