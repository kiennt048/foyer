package nas

// UniFi — the profile whose interesting property is what it does *not* render.
//
// **UNVERIFIED**, like every profile here, though this one claims less than the
// others: it emits standard attributes and no vendor redirect at all.

func init() { RegisterVendorProfile(UniFiProfile{}) }

// UniFiProfile renders the standard set and deliberately renders no redirect.
//
// # Why there is no redirect attribute here
//
// `docs/rules/nas-drivers.md` states the narrow claim: *UniFi's
// captive-portal **authorization** has no RADIUS equivalent.* Making a UniFi
// client captive is a property of the guest WLAN, and letting it through is
// `cmd/stamgr` → `authorize-guest` on the controller — which is
// `UniFiDriver.AuthorizeGuest`, out of band, and not a reply attribute.
//
// So this profile exists to say that in code rather than to add a vendor
// attribute. It is the difference between *"no profile was written for UniFi"*
// and *"a profile was written and it renders no redirect, on purpose"*, and
// those two look identical from `VendorProfileFor` unless one of them is a file.
//
// The standard attributes still matter, and one of them is the reason a UniFi
// deployment needs a reply at all: `docs/rules/radius.md` requires
// `Acct-Interim-Interval` in every Access-Accept, because many NAS send no
// interim updates unless the server asks — and on the controller-API path there
// is no Access-Accept to ask in, which is a gap this profile cannot close and
// NAS registration is required to assert loudly.
type UniFiProfile struct{}

func (UniFiProfile) Vendor() Vendor { return VendorUniFi }

func (UniFiProfile) RenderReply(d ReplyDecision) []ReplyAttribute {
	return StandardReply(d)
}

// Unrenderable names PortalURL for a captive decision, and that is the honest
// answer rather than an omission: a caller asking UniFi to redirect through
// RADIUS is asking for something no attribute carries, and it must find out here
// instead of from a guest who never reached the portal.
func (UniFiProfile) Unrenderable(d ReplyDecision) []string {
	return standardUnrenderable(d)
}

func (UniFiProfile) OperatorPrerequisites(d ReplyDecision) []string {
	need := standardProfile{vendor: VendorUniFi}.OperatorPrerequisites(d)
	if d.Captive {
		need = append(need, "the guest WLAN must have the controller's own captive portal enabled — UniFi's captive-portal authorization has no RADIUS equivalent, so the redirect cannot come from this reply")
	}
	return need
}

func (UniFiProfile) AttributeEvidence() AttributeEvidence { return standardEvidence() }
