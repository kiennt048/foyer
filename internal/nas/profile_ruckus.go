package nas

import "sort"

// Ruckus — the vendor whose *out-of-band* half is the trap, and whose reply half
// is the simplest here.
//
// **UNVERIFIED.** No Ruckus device has been near this project.

func init() { RegisterVendorProfile(RuckusProfile{}) }

// RuckusProfile renders captive-portal redirection as `Ruckus-External-Url`.
//
// # The half a reader is most likely to get wrong is not in this file
//
// `docs/rules/nas-drivers.md`: **Ruckus is not a CoA vendor for captive
// portal.** SmartZone's external-portal model is the Northbound Interface JSON
// API, and PacketFence documents that for MAC-auth in non-proxy mode the
// deauthentication method must be set to HTTP/HTTPS so the Northbound API is
// used *instead of* RADIUS Disconnect — otherwise "the code will try to use
// RADIUS by default and fail to disconnect the user."
//
// That is a statement about `NASDriver`, not about this profile: making a client
// captive still happens in the RADIUS reply, and it is exactly the separation
// the two abstractions exist for. A `RuckusDriver` built on `CoAClient` would be
// a driver that reports success on a no-op, which is the failure the CoA rules
// name. None exists, and this comment is here so the next person does not write
// one by symmetry with Cisco and Aruba.
type RuckusProfile struct{}

func (RuckusProfile) Vendor() Vendor { return VendorRuckus }

func (RuckusProfile) RenderReply(d ReplyDecision) []ReplyAttribute {
	out := StandardReply(d)
	if d.Captive && d.PortalURL != "" {
		out = append(out, ReplyAttribute{Name: "Ruckus-External-Url", Value: d.PortalURL})
	}
	// WISPr is the nearest thing to a cross-vendor rate limit and Ruckus is
	// among the platforms that read it. Down is toward the client, which is the
	// one direction convention that is not ambiguous — the ambiguity is which
	// way ReplyDecision.RxKbps was meant, and that is stated on the field.
	if d.RxKbps > 0 {
		out = append(out, ReplyAttribute{Name: "WISPr-Bandwidth-Max-Down", Value: bps(d.RxKbps)})
	}
	if d.TxKbps > 0 {
		out = append(out, ReplyAttribute{Name: "WISPr-Bandwidth-Max-Up", Value: bps(d.TxKbps)})
	}
	return out
}

func (RuckusProfile) Unrenderable(d ReplyDecision) []string {
	rendered := []string{"RxKbps/TxKbps"}
	if d.Captive {
		rendered = append(rendered, "PortalURL")
	}
	return unrenderableExcept(d, rendered...)
}

func (RuckusProfile) OperatorPrerequisites(d ReplyDecision) []string {
	need := standardProfile{vendor: VendorRuckus}.OperatorPrerequisites(d)
	if d.Captive && d.PortalURL != "" {
		need = append(need, "the WLAN must be configured for an external portal; revocation goes through the Northbound Interface JSON API, never RADIUS Disconnect")
	}
	sort.Strings(need)
	return need
}

func (RuckusProfile) AttributeEvidence() AttributeEvidence {
	ev := standardEvidence()
	ev["Ruckus-External-Url"] = CapabilityFact{
		Evidence: EvidenceUnverified,
		Note: "Named in docs/rules/nas-drivers.md's vendor table. No Ruckus device has been " +
			"tested. Settled by: a SmartZone or ZoneDirector in lab/ redirecting a client to the " +
			"URL from an Access-Accept.",
	}
	ev["WISPr-Bandwidth-Max-Down"] = CapabilityFact{
		Evidence: EvidenceUnverified,
		Note: "WISPr 1.0 defines it in bits per second and down is toward the client. Whether " +
			"Ruckus honours it, and whether ReplyDecision.RxKbps is the client's download, are " +
			"both unmeasured. Settled by: a two-direction rate test on real gear.",
	}
	ev["WISPr-Bandwidth-Max-Up"] = ev["WISPr-Bandwidth-Max-Down"]
	return ev
}
