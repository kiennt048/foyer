package nas

import "sort"

// Aruba — role-first, where Cisco is ACL-first.
//
// **UNVERIFIED.** No Aruba device has been near this project.

func init() { RegisterVendorProfile(ArubaProfile{}) }

// ArubaProfile renders captive-portal redirection as Aruba VSAs.
//
// Two attributes with distinct names, so unlike Cisco this reply survives the
// single-valued fold intact — which is a fact about Aruba's attribute design,
// not about the wire format being adequate.
type ArubaProfile struct{}

func (ArubaProfile) Vendor() Vendor { return VendorAruba }

// RenderReply emits the standard set plus `Aruba-User-Role` and, for a captive
// client, `Aruba-Captive-Portal-URL`.
//
// The role is the load-bearing half. On AOS the role is what actually confines a
// pre-authentication client; the URL only says where to send it. A reply
// carrying the URL and no role describes a client that is redirected and not
// confined, which is the walled garden absent.
func (ArubaProfile) RenderReply(d ReplyDecision) []ReplyAttribute {
	out := StandardReply(d)

	role := d.Role
	if d.Captive && d.PreAuthRole != "" {
		role = d.PreAuthRole
	}
	if role != "" {
		out = append(out, ReplyAttribute{Name: "Aruba-User-Role", Value: role})
	}
	if d.Captive && d.PortalURL != "" {
		// AOS 8.4+ only. On anything older this attribute is unknown and the
		// redirect has to come from the role's own captive-portal profile,
		// which is configuration on the controller and not something a reply
		// can carry — see OperatorPrerequisites.
		out = append(out, ReplyAttribute{Name: "Aruba-Captive-Portal-URL", Value: d.PortalURL})
	}
	return out
}

func (ArubaProfile) Unrenderable(d ReplyDecision) []string {
	rendered := []string{"Role"}
	if d.Captive {
		rendered = append(rendered, "PortalURL", "PreAuthRole")
	}
	return unrenderableExcept(d, rendered...)
}

// OperatorPrerequisites: a role name is a reference to something the operator
// already defined on the controller, exactly as Cisco's ACL is.
func (ArubaProfile) OperatorPrerequisites(d ReplyDecision) []string {
	need := standardProfile{vendor: VendorAruba}.OperatorPrerequisites(d)
	role := d.Role
	if d.Captive && d.PreAuthRole != "" {
		role = d.PreAuthRole
	}
	if role != "" {
		need = append(need, "user role "+role+" must already exist on the Aruba controller — Foyer names it and cannot create it")
	}
	if d.Captive && d.PortalURL != "" {
		need = append(need, "Aruba-Captive-Portal-URL requires AOS 8.4 or later; on older releases the redirect comes from the role's captive-portal profile instead")
	}
	sort.Strings(need)
	return need
}

func (ArubaProfile) AttributeEvidence() AttributeEvidence {
	ev := standardEvidence()
	ev["Aruba-User-Role"] = CapabilityFact{
		Evidence: EvidenceUnverified,
		Note: "Named in docs/rules/nas-drivers.md's vendor table. No Aruba device has been " +
			"tested. Settled by: an AOS controller in lab/ placing a client in the named role " +
			"from an Access-Accept, with the role's confinement observed from the client.",
	}
	ev["Aruba-Captive-Portal-URL"] = CapabilityFact{
		Evidence: EvidenceUnverified,
		Note: "AOS 8.4+ per docs/rules/nas-drivers.md. Neither the attribute nor the version " +
			"floor has been measured. Settled by: the same lab session, on an 8.4+ controller and " +
			"on one below it, so the floor is observed rather than repeated.",
	}
	return ev
}
