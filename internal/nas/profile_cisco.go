package nas

import "sort"

// Cisco — the vendor the rule warns about twice, and the only one whose reply
// the current wire format cannot carry.
//
// **UNVERIFIED.** No Cisco device has been near this project. What follows is
// the vendor table in `docs/rules/nas-drivers.md` rendered into code, and
// nothing more.

func init() { RegisterVendorProfile(CiscoProfile{}) }

// Cisco AV-pair keys. These are AV-pair *keys* carried inside a Cisco-AVPair
// attribute, not attribute names — which is the distinction the rules file makes
// when it says "Cisco AV-pairs, **not a standard**".
const (
	ciscoAVURLRedirect    = "url-redirect"
	ciscoAVURLRedirectACL = "url-redirect-acl"
)

// CiscoProfile renders captive-portal redirection as Cisco AV-pairs.
type CiscoProfile struct{}

func (CiscoProfile) Vendor() Vendor { return VendorCisco }

// RenderReply emits the standard set plus up to two Cisco-AVPair attributes.
//
// The ACL is emitted first on purpose. `CollapseToMap` keeps the first
// occurrence of a repeated name, and if exactly one of the two can survive the
// fold it must be the one whose absence is *loud*: without `url-redirect-acl`
// the NAS has no redirect ACL to apply and the guest is not made captive at all,
// which is a network that quietly lets strangers on. Losing `url-redirect`
// instead leaves a captive client with no portal to reach — broken, obviously,
// and safe.
func (CiscoProfile) RenderReply(d ReplyDecision) []ReplyAttribute {
	out := StandardReply(d)
	if !d.Captive {
		return out
	}
	if d.PreAuthACL != "" {
		out = append(out, ReplyAttribute{Name: "Cisco-AVPair", Value: avPair(ciscoAVURLRedirectACL, d.PreAuthACL)})
	}
	if d.PortalURL != "" {
		out = append(out, ReplyAttribute{Name: "Cisco-AVPair", Value: avPair(ciscoAVURLRedirect, d.PortalURL)})
	}
	return out
}

func (CiscoProfile) Unrenderable(d ReplyDecision) []string {
	rendered := []string{"PreAuthACL"}
	if d.Captive {
		rendered = append(rendered, "PortalURL")
	}
	return unrenderableExcept(d, rendered...)
}

// OperatorPrerequisites is where the rule *"the operator configures Foyer, not
// FreeRADIUS" is true; "the operator configures nothing" is not* becomes a value
// a caller can render.
//
// `url-redirect-acl` names an ACL that must already exist on the Cisco NAS.
// Foyer cannot create it, cannot verify it exists, and a reply naming an absent
// ACL fails on the device with nothing in Foyer's logs to say why.
func (CiscoProfile) OperatorPrerequisites(d ReplyDecision) []string {
	need := standardProfile{vendor: VendorCisco}.OperatorPrerequisites(d)
	if d.Captive && d.PreAuthACL != "" {
		need = append(need, "ACL "+d.PreAuthACL+" must already exist on the Cisco NAS — url-redirect-acl names it and Foyer cannot create it")
	}
	sort.Strings(need)
	return need
}

func (CiscoProfile) AttributeEvidence() AttributeEvidence {
	ev := standardEvidence()
	ev["Cisco-AVPair"] = CapabilityFact{
		Evidence: EvidenceUnverified,
		Note: "url-redirect and url-redirect-acl are Cisco AV-pairs, not a standard " +
			"(docs/rules/nas-drivers.md). No Cisco device has been tested. Settled by: a " +
			"WLC or IOS-XE NAS in lab/ answering a MAC-auth Access-Request, with the redirect " +
			"observed on a client — and by measuring whether two Cisco-AVPair attributes in one " +
			"Access-Accept are both honoured, which the current single-valued reply cannot even send.",
	}
	return ev
}
