package nas

import "sort"

// MikroTik — no RADIUS redirect attribute, and a rate limit whose direction is
// the one thing worth reading twice.
//
// **UNVERIFIED.** No MikroTik device has been near this project.

func init() { RegisterVendorProfile(MikroTikProfile{}) }

// MikroTikProfile renders `Mikrotik-Rate-Limit` and nothing else beyond the
// standard set.
//
// # There is no redirect attribute, and that is not an omission
//
// MikroTik's hotspot makes a client captive by intercepting it at the router;
// where the guest lands is hotspot configuration on the device, not a value a
// RADIUS reply carries. `docs/rules/nas-drivers.md` places MikroTik in the
// RFC 5176 CoA family for enforcement, which is `NASDriver`'s half. So a captive
// decision reports PortalURL Unrenderable here — the same shape as UniFi, for a
// completely different reason.
//
// # The direction of the rate pair
//
// MikroTik documents `Mikrotik-Rate-Limit` as `rx-rate[/tx-rate]` **from the
// router's point of view**, so `rx` is the client's *upload*. That is the
// opposite of WISPr's `Max-Down`, and it is why the same two numbers cannot be
// copied between the two profiles without knowing which perspective
// ReplyDecision.RxKbps was recorded in. Nothing in this repository records that,
// so this profile states its assumption in AttributeEvidence rather than burying
// it in a format string.
type MikroTikProfile struct{}

func (MikroTikProfile) Vendor() Vendor { return VendorMikroTik }

func (MikroTikProfile) RenderReply(d ReplyDecision) []ReplyAttribute {
	out := StandardReply(d)
	if v := mikrotikRateLimit(d.RxKbps, d.TxKbps); v != "" {
		out = append(out, ReplyAttribute{Name: "Mikrotik-Rate-Limit", Value: v})
	}
	return out
}

// mikrotikRateLimit renders the `rx-rate[/tx-rate]` pair, and it is one function
// because two things render it.
//
// `MikroTikProfile` puts it in an Access-Accept and `MikroTikCoAVendor` puts it
// in a CoA-Request, and those are the same attribute on the same device. Two
// copies of a format string is the stale-second-copy failure
// `docs/rules/testing.md` names — with the extra twist that here the copies
// would drift *silently and asymmetrically*: a guest would get one rate at login
// and a different one the moment an operator pressed apply.
//
// Assumed: the caller's Rx is toward the client, so it is the *router's* tx and
// belongs second. MikroTik documents the format from the router's point of view.
// Stated here and in both evidence maps rather than buried in a format string,
// because a silently swapped pair is a rate limit on the wrong direction that
// looks like it works.
func mikrotikRateLimit(rxKbps, txKbps int) string {
	if rxKbps <= 0 && txKbps <= 0 {
		return ""
	}
	return kbps(txKbps) + "k/" + kbps(rxKbps) + "k"
}

func (MikroTikProfile) Unrenderable(d ReplyDecision) []string {
	return unrenderableExcept(d, "RxKbps/TxKbps")
}

func (MikroTikProfile) OperatorPrerequisites(d ReplyDecision) []string {
	need := standardProfile{vendor: VendorMikroTik}.OperatorPrerequisites(d)
	if d.Captive {
		need = append(need, "the hotspot server and its login page must already be configured on the router — no RADIUS attribute carries the portal URL")
	}
	sort.Strings(need)
	return need
}

func (MikroTikProfile) AttributeEvidence() AttributeEvidence {
	ev := standardEvidence()
	ev["Mikrotik-Rate-Limit"] = CapabilityFact{
		Evidence: EvidenceUnverified,
		Note: "MikroTik documents the format as rx-rate[/tx-rate] from the ROUTER's point of " +
			"view, so rx is the client's upload — the opposite of WISPr-Bandwidth-Max-Down. " +
			"This profile assumes ReplyDecision.RxKbps is toward the client and renders it " +
			"second. Neither the format nor the assumption has been measured. Settled by: a " +
			"RouterOS hotspot in lab/ with a throughput test in both directions.",
	}
	return ev
}
