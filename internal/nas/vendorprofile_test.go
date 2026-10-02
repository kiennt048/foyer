package nas

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// What these tests are worth, stated once.
//
// Not one attribute in this package has been sent to a device. These assert that
// a profile renders the shape its own vendor table says, that no profile claims
// more than that, and that the seam holds — nothing more. `docs/rules/testing.md`:
// *"a hand-written NAS mock encodes the author's assumptions."* A rendered reply
// asserted by its own author is the same trap one layer earlier, so the value
// here is in the *negative* cases — what a profile refuses to claim, and what it
// says an operator must already have configured.

func portalDecision() ReplyDecision {
	return ReplyDecision{
		Captive:         true,
		PortalURL:       "https://portal.example.net/en/",
		PreAuthACL:      "ACL-WEBAUTH-REDIRECT",
		PreAuthRole:     "foyer-preauth",
		SessionTimeout:  2 * time.Hour,
		InterimInterval: 300 * time.Second,
		RxKbps:          10000,
		TxKbps:          5000,
		VLAN:            "40",
		Class:           "Foyer:guest",
	}
}

func attrValues(attrs []ReplyAttribute, name string) []string {
	var out []string
	for _, a := range attrs {
		if a.Name == name {
			out = append(out, a.Value)
		}
	}
	return out
}

// TestStandardReplyEmitsOnlyStandards is the base every profile builds on, and
// the assertion is as much about what is absent as what is present.
func TestStandardReplyEmitsOnlyStandards(t *testing.T) {
	got := StandardReply(portalDecision())

	want := map[string]string{
		"Session-Timeout":         "7200",
		"Acct-Interim-Interval":   "300",
		"Class":                   "Foyer:guest",
		"Tunnel-Type":             "VLAN",
		"Tunnel-Medium-Type":      "IEEE-802",
		"Tunnel-Private-Group-Id": "40",
	}
	m, dropped := CollapseToMap(got)
	if len(dropped) != 0 {
		t.Errorf("the standard set does not repeat an attribute name, yet %v was dropped", dropped)
	}
	if len(m) != len(want) {
		t.Fatalf("rendered %v, want exactly %v", m, want)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("%s = %q, want %q", k, m[k], v)
		}
	}

	// No redirect, from any RFC. That absence is the reason VendorProfile exists.
	for _, forbidden := range []string{"Cisco-AVPair", "Aruba-Captive-Portal-URL", "Ruckus-External-Url"} {
		if _, present := m[forbidden]; present {
			t.Errorf("the standard set emitted %s, which is a vendor attribute", forbidden)
		}
	}
}

// TestVLANAttributesAreUntaggedBecauseAColonIsRefused.
//
// RFC 3580 describes tagged attributes and FreeRADIUS spells a tag with a colon,
// but a colon in a key is refused by the Policy API's reply gate as a list
// qualifier — and the refusal drops the *whole* reply as a 503 with a
// credential-leak alarm beside it. A tagged name here would take out every
// dynamic-VLAN reply in the product.
func TestVLANAttributesAreUntaggedBecauseAColonIsRefused(t *testing.T) {
	for _, a := range StandardReply(ReplyDecision{VLAN: "40"}) {
		if strings.ContainsAny(a.Name, ":&") {
			t.Errorf("attribute %q carries a list qualifier and would be refused by the rlm_rest reply gate", a.Name)
		}
	}
}

// TestStandardProfileWillNotFakeARateLimit.
//
// The Policy API does not emit `Filter-Id = "<rx>/<tx>"`. RFC 2865 §5.11 defines
// Filter-Id as the *name* of a filter list already configured on the NAS, so
// that value is a filter-list name which has to exist on the device — the same
// shape as Cisco's url-redirect-acl. A rate limit therefore reaches Filter-Id
// only when an operator supplies the name, and is otherwise reported rather than
// emitted as something that looks like it works.
func TestStandardProfileWillNotFakeARateLimit(t *testing.T) {
	p := VendorProfileFor(VendorManual)
	d := ReplyDecision{RxKbps: 10000, TxKbps: 5000}

	if got := attrValues(p.RenderReply(d), "Filter-Id"); len(got) != 0 {
		t.Errorf("Filter-Id = %v for a bare rate pair; that is a filter-list name, not a rate", got)
	}
	if !contains(p.Unrenderable(d), "RxKbps/TxKbps") {
		t.Errorf("a rate limit the profile cannot render was dropped silently: %v", p.Unrenderable(d))
	}

	named := ReplyDecision{FilterID: "guest-10m"}
	if got := attrValues(p.RenderReply(named), "Filter-Id"); len(got) != 1 || got[0] != "guest-10m" {
		t.Errorf("Filter-Id = %v for an operator-supplied name, want [guest-10m]", got)
	}
	if pre := p.OperatorPrerequisites(named); len(pre) == 0 || !strings.Contains(pre[0], "guest-10m") {
		t.Errorf("prerequisites = %v; the filter list has to already exist on the NAS and nothing said so", pre)
	}
}

// TestCiscoRendersTwoAVPairs — and the reply cannot be served today.
//
// This is the finding, not the assertion: `url-redirect` and `url-redirect-acl`
// are two Cisco-AVPair attributes with the same name, and the Policy API answers
// `rlm_rest` with a JSON dictionary of pairs, so `Decision.ReplyAttributes` is a
// `map[string]string` that can hold exactly one of them. Rendering is this
// package's job; the wire format belongs to `internal/raddb` and `rlm_rest`. So
// the loss is measured here and reported, never papered over with a joined
// value — which would be a guess about how a NAS parses AV-pairs.
func TestCiscoRendersTwoAVPairs(t *testing.T) {
	attrs := CiscoProfile{}.RenderReply(portalDecision())

	pairs := attrValues(attrs, "Cisco-AVPair")
	if len(pairs) != 2 {
		t.Fatalf("Cisco-AVPair rendered %d times: %v", len(pairs), pairs)
	}
	// The ACL first: if only one can survive the fold it must be the one whose
	// absence is loud. Losing the ACL leaves a client that is not made captive
	// at all, which is a network quietly letting strangers on.
	if !strings.HasPrefix(pairs[0], "url-redirect-acl=") {
		t.Errorf("first AV-pair is %q; the ACL must be first so the fold keeps it", pairs[0])
	}
	if !strings.HasPrefix(pairs[1], "url-redirect=") {
		t.Errorf("second AV-pair is %q", pairs[1])
	}

	_, dropped := CollapseToMap(attrs)
	if len(dropped) != 1 || !strings.HasPrefix(dropped[0], "Cisco-AVPair=url-redirect=") {
		t.Fatalf("dropped = %v; a single-valued reply must lose exactly one AV-pair and name it", dropped)
	}
}

// TestCiscoNamesTheACLItCannotCreate: the rule made into a value a caller can
// render. `url-redirect-acl` names an ACL that must already exist on the NAS.
func TestCiscoNamesTheACLItCannotCreate(t *testing.T) {
	pre := CiscoProfile{}.OperatorPrerequisites(portalDecision())
	if len(pre) == 0 {
		t.Fatal("a Cisco captive reply names an ACL Foyer cannot create and reported no prerequisite")
	}
	var named bool
	for _, p := range pre {
		if strings.Contains(p, "ACL-WEBAUTH-REDIRECT") {
			named = true
		}
	}
	if !named {
		t.Errorf("prerequisites %v do not name the ACL the reply refers to", pre)
	}
}

// TestArubaRendersRoleAndURLUnderDistinctNames — so unlike Cisco this reply
// survives the single-valued fold, which is a fact about Aruba's attribute
// design and not about the wire format being adequate.
func TestArubaRendersRoleAndURLUnderDistinctNames(t *testing.T) {
	m, dropped := CollapseToMap(ArubaProfile{}.RenderReply(portalDecision()))
	if len(dropped) != 0 {
		t.Errorf("an Aruba reply lost %v to the single-valued fold", dropped)
	}
	if m["Aruba-User-Role"] != "foyer-preauth" {
		t.Errorf("Aruba-User-Role = %q, want the pre-auth role for a captive decision", m["Aruba-User-Role"])
	}
	if m["Aruba-Captive-Portal-URL"] != "https://portal.example.net/en/" {
		t.Errorf("Aruba-Captive-Portal-URL = %q", m["Aruba-Captive-Portal-URL"])
	}

	// Not captive: the post-authorization role, and no redirect at all.
	authorized := ReplyDecision{Role: "foyer-guest", SessionTimeout: time.Hour}
	m2, _ := CollapseToMap(ArubaProfile{}.RenderReply(authorized))
	if m2["Aruba-User-Role"] != "foyer-guest" {
		t.Errorf("Aruba-User-Role = %q for an authorized client", m2["Aruba-User-Role"])
	}
	if _, present := m2["Aruba-Captive-Portal-URL"]; present {
		t.Error("an authorized client was sent a captive-portal URL, which makes it captive again")
	}
}

// TestUniFiRendersNoRedirectOnPurpose.
//
// The narrow claim, in code: UniFi's captive-portal *authorization* has no
// RADIUS equivalent. A profile that exists and renders nothing is a different
// statement from no profile at all, and the difference is invisible from
// VendorProfileFor unless one of them is a file.
func TestUniFiRendersNoRedirectOnPurpose(t *testing.T) {
	d := portalDecision()
	m, _ := CollapseToMap(UniFiProfile{}.RenderReply(d))

	for name := range m {
		if strings.Contains(strings.ToLower(name), "redirect") || strings.Contains(strings.ToLower(name), "url") {
			t.Errorf("UniFi rendered %q; its captive-portal authorization has no RADIUS equivalent", name)
		}
	}
	if !contains(UniFiProfile{}.Unrenderable(d), "PortalURL") {
		t.Error("UniFi dropped PortalURL without saying so, which is a field that silently does nothing")
	}
	if m["Acct-Interim-Interval"] != "300" {
		t.Errorf("Acct-Interim-Interval = %q; radius.md requires it in every Access-Accept", m["Acct-Interim-Interval"])
	}
}

// TestMikroTikRateLimitStatesItsDirection: the pair is rendered router-side, so
// the client's download is the router's tx and belongs second.
func TestMikroTikRateLimitStatesItsDirection(t *testing.T) {
	m, _ := CollapseToMap(MikroTikProfile{}.RenderReply(portalDecision()))
	if got := m["Mikrotik-Rate-Limit"]; got != "5000k/10000k" {
		t.Errorf("Mikrotik-Rate-Limit = %q, want %q — rx-rate first, from the router's point of view", got, "5000k/10000k")
	}
	fact := MikroTikProfile{}.AttributeEvidence()["Mikrotik-Rate-Limit"]
	if fact.Evidence != EvidenceUnverified {
		t.Errorf("Mikrotik-Rate-Limit is %s; no MikroTik device has been tested", fact.Evidence)
	}
	if !strings.Contains(strings.ToLower(fact.Note), "router") {
		t.Errorf("the direction assumption is not written down: %q", fact.Note)
	}
}

// TestRuckusRendersWISPrInBitsPerSecond — the unit is the whole point, and a
// kilobit value in a bits-per-second attribute is a rate limit a thousand times
// too tight that nothing reports.
func TestRuckusRendersWISPrInBitsPerSecond(t *testing.T) {
	m, _ := CollapseToMap(RuckusProfile{}.RenderReply(portalDecision()))
	if m["WISPr-Bandwidth-Max-Down"] != "10000000" {
		t.Errorf("WISPr-Bandwidth-Max-Down = %q, want 10 Mbit expressed in bits per second", m["WISPr-Bandwidth-Max-Down"])
	}
	if m["Ruckus-External-Url"] != "https://portal.example.net/en/" {
		t.Errorf("Ruckus-External-Url = %q", m["Ruckus-External-Url"])
	}
}

// TestNoVendorAttributeClaimsToBeMeasured is the honesty lock for the whole
// package, and it is derived rather than listed: every attribute a profile emits
// that is not in the RFC set must be UNVERIFIED, and its note must name the
// measurement that would settle it. An UNVERIFIED fact with no experiment behind
// it is a guess in a costume — the same rule CONTRACT.md already applies to
// capabilities, and TestUniFiCapabilityEvidence already fails on an empty note.
func TestNoVendorAttributeClaimsToBeMeasured(t *testing.T) {
	rfcSet := standardEvidence()

	for _, v := range RegisteredVendorProfiles() {
		p := VendorProfileFor(v)
		for name, fact := range p.AttributeEvidence() {
			if _, isRFC := rfcSet[name]; isRFC {
				continue
			}
			if fact.Evidence == EvidenceMeasured {
				t.Errorf("%s claims %s for %s; no device of that vendor exists on this project", v, fact.Evidence, name)
			}
			if fact.Evidence != EvidenceUnverified {
				t.Errorf("%s: %s is %s, and a vendor attribute nobody has sent is UNVERIFIED", v, name, fact.Evidence)
			}
			if strings.TrimSpace(fact.Note) == "" {
				t.Errorf("%s: %s is UNVERIFIED with no note, which is a guess wearing a label", v, name)
			}
			if !strings.Contains(strings.ToLower(fact.Note), "settled by") {
				t.Errorf("%s: %s names no measurement that would settle it: %q", v, name, fact.Note)
			}
		}
	}
}

// TestEveryRenderedAttributeCarriesEvidence: a profile cannot emit an attribute
// it has said nothing about. Without this, adding a vendor attribute and
// forgetting its evidence entry passes, and the package's one honesty claim goes
// quiet exactly where a new guess enters.
func TestEveryRenderedAttributeCarriesEvidence(t *testing.T) {
	d := portalDecision()
	for _, v := range RegisteredVendorProfiles() {
		p := VendorProfileFor(v)
		ev := p.AttributeEvidence()
		for _, a := range p.RenderReply(d) {
			if _, ok := ev[a.Name]; !ok {
				t.Errorf("%s renders %s and its evidence says nothing about it", v, a.Name)
			}
		}
	}
}

// TestNoProfileEmitsAnAttributeThePolicyAPIWouldRefuse.
//
// The two gates are in different packages and neither imports the other's test,
// so this is the only place they meet. `aaa.ForbiddenReplyAttribute` refuses any
// key containing a list qualifier and any credential-shaped name, and it drops
// the **whole reply** on a violation — so a vendor attribute that trips it takes
// out every reply for that vendor, as a 503 with a credential-leak alarm beside
// it and nothing saying why.
//
// The predicate is restated here rather than imported because `internal/aaa`
// imports this package and the dependency cannot run both ways. It is kept
// deliberately narrower than the real one — shape only — so that it can never
// disagree with it about a name the real gate allows.
func TestNoProfileEmitsAnAttributeThePolicyAPIWouldRefuse(t *testing.T) {
	credentialish := []string{"pass", "secret", "credenti", "cleartext", "hash", "chap", "key", "cert"}

	for _, v := range RegisteredVendorProfiles() {
		for _, a := range VendorProfileFor(v).RenderReply(portalDecision()) {
			if strings.ContainsAny(a.Name, ":&") {
				t.Errorf("%s emits %q, which carries a list qualifier and would drop the whole reply", v, a.Name)
			}
			lower := strings.ToLower(a.Name)
			for _, bad := range credentialish {
				if strings.Contains(lower, bad) {
					t.Errorf("%s emits %q, which the Policy API's reply gate reads as credential material", v, a.Name)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// The abstraction test the rules ask for
// ---------------------------------------------------------------------------

// gigabitPointProfile is a fictional vendor, defined entirely inside this test
// file: a struct, an evidence map, and a registration.
//
// `docs/rules/nas-drivers.md`: *"The design claims a new driver is one file
// with no core changes. If you find yourself changing the core to add one, that
// is a finding about the abstraction — report it rather than working around
// it."* This file is the experiment for the VendorProfile half of that claim.
// Nothing in vendorprofile.go was touched to add it, and nothing had to be:
// registration happens from the vendor's own init, so the core has no table to
// append to.
type gigabitPointProfile struct{}

func (gigabitPointProfile) Vendor() Vendor { return Vendor("gigabitpoint") }

func (gigabitPointProfile) RenderReply(d ReplyDecision) []ReplyAttribute {
	out := StandardReply(d)
	if d.Captive && d.PortalURL != "" {
		out = append(out, ReplyAttribute{Name: "GigabitPoint-Redirect", Value: d.PortalURL})
	}
	return out
}

func (gigabitPointProfile) Unrenderable(d ReplyDecision) []string {
	if d.Captive {
		return unrenderableExcept(d, "PortalURL")
	}
	return standardUnrenderable(d)
}

func (gigabitPointProfile) OperatorPrerequisites(d ReplyDecision) []string {
	return standardProfile{vendor: Vendor("gigabitpoint")}.OperatorPrerequisites(d)
}

func (gigabitPointProfile) AttributeEvidence() AttributeEvidence {
	ev := standardEvidence()
	ev["GigabitPoint-Redirect"] = CapabilityFact{
		Evidence: EvidenceUnverified,
		Note:     "A fictional vendor used to exercise the seam. Settled by: nothing — this vendor does not exist.",
	}
	return ev
}

func init() { RegisterVendorProfile(gigabitPointProfile{}) }

// TestAddingAVendorProfileNeedsNoCoreChange is the answer, executed rather than
// asserted in prose.
func TestAddingAVendorProfileNeedsNoCoreChange(t *testing.T) {
	p := VendorProfileFor(Vendor("gigabitpoint"))
	if p.Vendor() != Vendor("gigabitpoint") {
		t.Fatalf("VendorProfileFor returned %q; the registration from the vendor's own init did not take", p.Vendor())
	}
	m, _ := CollapseToMap(p.RenderReply(portalDecision()))
	if m["GigabitPoint-Redirect"] != "https://portal.example.net/en/" {
		t.Errorf("the new vendor's own attribute did not render: %v", m)
	}
	// It inherits the standard set without restating it, which is the other half
	// of "one file": a profile that had to re-derive Session-Timeout would be a
	// second copy of an RFC.
	if m["Session-Timeout"] != "7200" {
		t.Errorf("Session-Timeout = %q for a vendor that never mentions it", m["Session-Timeout"])
	}
}

// TestUnregisteredVendorGetsTheFloorNotAGuess: the same rule
// ShippedCapabilities follows for the other half of this package.
func TestUnregisteredVendorGetsTheFloorNotAGuess(t *testing.T) {
	p := VendorProfileFor(Vendor("no-such-vendor"))
	d := portalDecision()

	m, _ := CollapseToMap(p.RenderReply(d))
	if len(m) == 0 {
		t.Fatal("a vendor with no profile got no reply at all; the standard attributes are standards and are safe anywhere")
	}
	if !contains(p.Unrenderable(d), "PortalURL") {
		t.Errorf("a vendor with no profile silently dropped the redirect: %v", p.Unrenderable(d))
	}
	// And it invents nothing.
	for name := range m {
		if strings.Contains(name, "-") && !strings.HasPrefix(name, "Session") &&
			!strings.HasPrefix(name, "Idle") && !strings.HasPrefix(name, "Acct") &&
			!strings.HasPrefix(name, "Tunnel") && !strings.HasPrefix(name, "Filter") {
			t.Errorf("a vendor with no profile emitted %q", name)
		}
	}
}

// TestOmadaHasNoProfileAndSaysSo pins one real gap rather than letting the
// fallback hide it. Omada is in the enforcement table as a controller-API
// driver, and Foyer ships neither a driver nor a profile for it.
func TestOmadaHasNoProfileAndSaysSo(t *testing.T) {
	for _, v := range RegisteredVendorProfiles() {
		if v == VendorOmada {
			t.Fatalf("a profile for %s was registered; if that is deliberate, this test is the thing to update, with the vendor table beside it", v)
		}
	}
	if got := VendorProfileFor(VendorOmada).Vendor(); got != VendorOmada {
		t.Errorf("the fallback profile reports vendor %q, so a log line naming it would name the wrong device", got)
	}
}

func contains(list []string, want string) bool {
	i := sort.SearchStrings(list, want)
	if i < len(list) && list[i] == want {
		return true
	}
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
