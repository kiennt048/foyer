package nas

import (
	"fmt"
	"sort"
	"time"
)

// VendorProfile — the second abstraction.
//
// `docs/rules/nas-drivers.md` says why there are two: vendor families differ not
// only in *how you let a client in*. They also differ in **how a client is made
// captive in the first place**, and that happens inside the RADIUS reply — not
// out of band.
//
// So `NASDriver` performs out-of-band actions — verify, authorize, revoke — and
// `VendorProfile` renders an abstract decision into the attributes one vendor
// family understands, while the Policy API is answering an Access-Request.
// `internal/aaa` was doing the second job itself, which is the collapse the rule
// forbids.
//
// # Everything in this file and its siblings is UNVERIFIED
//
// There is no Cisco, Aruba, Ruckus or MikroTik device on this project and there
// never has been. `docs/rules/testing.md` is explicit about what a
// hand-written vendor mock is worth — *"a hand-written NAS mock encodes the
// author's assumptions, the same assumptions that produced the bug"* — and a
// rendered reply is worse than a mocked one, because nothing here even round
// trips: the attributes are assembled, asserted against this author's reading of
// a vendor table, and never sent anywhere.
//
// The attribute **names** are DOCUMENTED where a citation exists: the RFCs for
// the standard set, and the vendor table in `docs/rules/nas-drivers.md` for
// the redirect attributes. That any NAS *honours* what is rendered here is
// UNVERIFIED without exception, and `AttributeEvidence` says so per attribute
// with the measurement that would settle it. A profile labelled UNVERIFIED is
// worth shipping; one that implies it was tested is not.

// ReplyDecision is an authorization answer before any vendor has seen it.
//
// It carries what was decided, never how to say it. A field that no profile can
// render is not silently dropped — see VendorProfile.Unrenderable, which is the
// same rule `docs/rules/nas-drivers.md` states for capabilities: never a
// field that silently does nothing.
type ReplyDecision struct {
	// Captive says this client must be sent to the portal rather than let
	// straight onto the network. Only a captive reply carries a redirect, and a
	// profile that rendered one unconditionally would make every authorized
	// guest captive again.
	Captive bool

	// PortalURL is where a captive client must land.
	PortalURL string

	// PreAuthACL names an ACL that must **already exist on the NAS**. Foyer
	// cannot create it: *"the operator configures Foyer, not FreeRADIUS" is
	// true; "the operator configures nothing" is not.* Cisco's
	// `url-redirect-acl` is the case the rule is written about, and a profile
	// that needs one reports it through OperatorPrerequisites.
	PreAuthACL string

	// PreAuthRole is the same shape for role-based platforms: a role the
	// operator has already defined on the controller.
	PreAuthRole string

	// Role is the role an authorized client is placed in.
	Role string

	SessionTimeout  time.Duration
	IdleTimeout     time.Duration
	InterimInterval time.Duration

	// RxKbps and TxKbps are the rate limits as the voucher or account records
	// them, under the names those columns already use.
	//
	// **Whose perspective they are from is UNVERIFIED, and it is not a detail.**
	// `WISPr-Bandwidth-Max-Down` is unambiguous — down is toward the client.
	// MikroTik documents `Mikrotik-Rate-Limit` as `rx-rate[/tx-rate]` from the
	// *router's* point of view, where rx is the client's **upload**. Mapping one
	// onto the other requires knowing which way `RxRateKbps` in a voucher batch
	// was meant, and nothing in this repository records that. Every profile here
	// assumes Rx is toward the client and says so in its evidence; the
	// measurement that settles it is a rate test on real gear, both directions,
	// one vendor at a time.
	RxKbps int
	TxKbps int

	// VLAN is a VLAN id or name for RFC 3580 dynamic assignment.
	VLAN string

	// Class is opaque and echoed back in accounting — how a degraded or
	// otherwise special session is recognised in `radacct` afterwards.
	Class string

	// FilterID names a filter list that must already exist on the NAS
	// (RFC 2865 §5.11). It is a *name*, not a rate: see standardProfile.
	FilterID string
}

// ReplyAttribute is one rendered pair.
//
// A slice of these rather than a map, and that is load-bearing rather than
// stylistic: **Cisco needs two `Cisco-AVPair` attributes in one reply** —
// `url-redirect` and `url-redirect-acl` are separate attributes with the same
// name — and a `map[string]string` cannot hold them. See CollapseToMap.
type ReplyAttribute struct {
	Name  string
	Value string
}

// AttributeEvidence records how each attribute a profile emits is known to be
// the right one, keyed on attribute name.
//
// Same three words `docs/protocol-reality.md` uses, and the same rule
// CONTRACT.md already states for capabilities: an UNVERIFIED fact must name the
// measurement that would settle it, because a label with no experiment behind it
// is a guess in a costume.
type AttributeEvidence map[string]CapabilityFact

// VendorProfile renders an abstract decision into one vendor's attributes.
type VendorProfile interface {
	Vendor() Vendor

	// RenderReply produces the attributes to send. Order is stable and repeats
	// are permitted.
	RenderReply(d ReplyDecision) []ReplyAttribute

	// Unrenderable names the fields of d this profile has no attribute for,
	// sorted.
	//
	// It is a fact about the profile, never a claim about the vendor: "this
	// profile cannot say it" and "the platform cannot do it" are different
	// statements and only the first one is knowable from here. A caller that
	// offers an operator a control whose value lands in this list is offering a
	// field that silently does nothing.
	Unrenderable(d ReplyDecision) []string

	// OperatorPrerequisites names what must already exist on the NAS for this
	// reply to work, sorted. Empty is a real answer and means nothing is needed.
	OperatorPrerequisites(d ReplyDecision) []string

	// AttributeEvidence says how each attribute this profile emits is known.
	AttributeEvidence() AttributeEvidence
}

// ---------------------------------------------------------------------------
// Registry — the half that decides whether "one file, no core changes" is true
// ---------------------------------------------------------------------------

var vendorProfiles = map[Vendor]VendorProfile{}

// RegisterVendorProfile registers p under its own vendor.
//
// Called from each profile's own `init()`, which is the whole point: the design
// claims a new vendor is one file with no core change, and a registration table
// living here would make that claim false by one line every time. Registering
// from the vendor's file keeps the core closed to modification, and there is a
// test that proves it by adding a vendor from a test file and rendering through
// it — see TestAddingAVendorProfileNeedsNoCoreChange.
func RegisterVendorProfile(p VendorProfile) {
	if p == nil {
		panic("nas: RegisterVendorProfile(nil)")
	}
	v := p.Vendor()
	if _, dup := vendorProfiles[v]; dup {
		panic(fmt.Sprintf("nas: two vendor profiles registered for %q", v))
	}
	vendorProfiles[v] = p
}

// VendorProfileFor returns the profile for a vendor, or the standard RFC-only
// profile where Foyer ships none.
//
// The fallback is the floor rather than a guess, exactly as ShippedCapabilities
// does for the other half of this package: the standard attributes are standards
// and are safe to emit anywhere, and the redirect — the one thing that is *not*
// a standard — comes back from Unrenderable rather than being invented. A caller
// therefore gets a working reply and an honest list of what did not fit, instead
// of a plausible-looking vendor attribute nobody has ever sent.
func VendorProfileFor(v Vendor) VendorProfile {
	if p, ok := vendorProfiles[v]; ok {
		return p
	}
	return standardProfile{vendor: v}
}

// RegisteredVendorProfiles lists, sorted, the vendors with a profile of their
// own. Everything else falls back to the standard one.
func RegisteredVendorProfiles() []Vendor {
	out := make([]Vendor, 0, len(vendorProfiles))
	for v := range vendorProfiles {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ---------------------------------------------------------------------------
// The standard set — the attributes that really are standards
// ---------------------------------------------------------------------------

// StandardReply renders the attributes defined by RFC rather than by a vendor.
//
// Every vendor profile builds on this and appends its own, because the split is
// the honest one: `Session-Timeout` is a standard and `url-redirect` is a Cisco
// AV-pair, and a design that treats them alike is the design this file exists to
// undo.
//
// Two mechanical notes that are easy to get wrong:
//
//   - **VLAN attributes are emitted untagged.** RFC 3580 §3.31 describes tagged
//     `Tunnel-Type` / `Tunnel-Medium-Type` / `Tunnel-Private-Group-Id`, and
//     FreeRADIUS spells a tag with a colon (`Tunnel-Type:0`). A colon cannot
//     appear in a key crossing the `rlm_rest` boundary: `aaa.ForbiddenReplyAttribute`
//     refuses every list qualifier by shape, so a tagged name would be read as a
//     credential-leak attempt and the **whole reply dropped as a 503**. Untagged
//     is what is emitted and what FreeRADIUS accepts.
//   - **Acct-Interim-Interval is not clamped here.** RFC 2869 §5.16 defines it
//     and `docs/rules/radius.md` records a widely-implemented 300-second
//     floor, but silently raising a value the operator chose is the agreeable
//     mistake in reverse — the caller picks it, and the floor is documented
//     where the caller can see it.
func StandardReply(d ReplyDecision) []ReplyAttribute {
	var out []ReplyAttribute
	add := func(name, value string) {
		if value != "" {
			out = append(out, ReplyAttribute{Name: name, Value: value})
		}
	}

	if d.SessionTimeout > 0 {
		add("Session-Timeout", seconds(d.SessionTimeout))
	}
	if d.IdleTimeout > 0 {
		add("Idle-Timeout", seconds(d.IdleTimeout))
	}
	if d.InterimInterval > 0 {
		add("Acct-Interim-Interval", seconds(d.InterimInterval))
	}
	add("Class", d.Class)
	add("Filter-Id", d.FilterID)
	if d.VLAN != "" {
		add("Tunnel-Type", "VLAN")
		add("Tunnel-Medium-Type", "IEEE-802")
		add("Tunnel-Private-Group-Id", d.VLAN)
	}
	return out
}

func seconds(d time.Duration) string {
	return fmt.Sprintf("%d", int(d.Round(time.Second)/time.Second))
}

// standardEvidence is the citation set for StandardReply, and it is the one
// place in this package where DOCUMENTED is used without a vendor caveat.
func standardEvidence() AttributeEvidence {
	return AttributeEvidence{
		"Session-Timeout":         {Evidence: EvidenceDocumented, Note: "RFC 2865 §5.27."},
		"Idle-Timeout":            {Evidence: EvidenceDocumented, Note: "RFC 2865 §5.28."},
		"Acct-Interim-Interval":   {Evidence: EvidenceDocumented, Note: "RFC 2869 §5.16. A 300-second floor is widely implemented; see docs/rules/radius.md."},
		"Class":                   {Evidence: EvidenceDocumented, Note: "RFC 2865 §5.25 — opaque, echoed back in accounting."},
		"Filter-Id":               {Evidence: EvidenceDocumented, Note: "RFC 2865 §5.11 — the NAME of a filter list already present on the NAS, which Foyer cannot create."},
		"Tunnel-Type":             {Evidence: EvidenceDocumented, Note: "RFC 3580 §3.31. Emitted untagged: a FreeRADIUS tag needs a colon, and a colon in a key is refused by the rlm_rest reply gate."},
		"Tunnel-Medium-Type":      {Evidence: EvidenceDocumented, Note: "RFC 3580 §3.31."},
		"Tunnel-Private-Group-Id": {Evidence: EvidenceDocumented, Note: "RFC 3580 §3.31 — the VLAN. UNVERIFIED that any specific NAS here honours it; no device has been tested."},
	}
}

// standardUnrenderable is what the RFC set alone cannot say, and it is the base
// every vendor profile subtracts from.
//
// The captive-portal redirect heads the list because it is the whole reason this
// abstraction exists: there is no standard attribute for it, in any RFC.
func standardUnrenderable(d ReplyDecision) []string {
	var missing []string
	if d.Captive && d.PortalURL != "" {
		missing = append(missing, "PortalURL")
	}
	if d.PreAuthACL != "" {
		missing = append(missing, "PreAuthACL")
	}
	if d.PreAuthRole != "" {
		missing = append(missing, "PreAuthRole")
	}
	if d.Role != "" {
		missing = append(missing, "Role")
	}
	if d.RxKbps > 0 || d.TxKbps > 0 {
		missing = append(missing, "RxKbps/TxKbps")
	}
	sort.Strings(missing)
	return missing
}

// standardProfile is the RFC-only profile, used directly for vendors Foyer
// ships no profile for.
//
// It renders no rate limit at all, deliberately: it does not emit
// `Filter-Id = "<rx>/<tx>"`, because RFC 2865 §5.11 defines
// Filter-Id as *the name of a filter list already configured on the NAS* — so
// `"10000/5000"` is a filter-list name that has to exist on the device, and
// nothing has ever been observed treating it as a rate pair. It is the same
// shape as Cisco's `url-redirect-acl`: a name Foyer cannot create. So a rate
// limit reaches Filter-Id only when an operator supplies the name themselves,
// through ReplyDecision.FilterID, and otherwise it is reported Unrenderable
// rather than emitted as something that looks like it works.
type standardProfile struct{ vendor Vendor }

func (p standardProfile) Vendor() Vendor { return p.vendor }

func (p standardProfile) RenderReply(d ReplyDecision) []ReplyAttribute {
	return StandardReply(d)
}

func (p standardProfile) Unrenderable(d ReplyDecision) []string {
	return standardUnrenderable(d)
}

func (p standardProfile) OperatorPrerequisites(d ReplyDecision) []string {
	var need []string
	if d.FilterID != "" {
		need = append(need, "filter list "+d.FilterID+" must already exist on the NAS (RFC 2865 §5.11)")
	}
	if d.VLAN != "" {
		need = append(need, "VLAN "+d.VLAN+" must already be configured and trunked to the NAS")
	}
	sort.Strings(need)
	return need
}

func (p standardProfile) AttributeEvidence() AttributeEvidence { return standardEvidence() }

// ---------------------------------------------------------------------------
// Serving a rendered reply over the current rlm_rest wire format
// ---------------------------------------------------------------------------

// CollapseToMap folds a rendered reply into the single-valued dictionary the
// Policy API serves today, and names every attribute that could not survive the
// fold.
//
// **The second return value is a finding, not a nicety.** `rlm_rest` is answered
// with a JSON dictionary of pairs, so `Decision.ReplyAttributes` is a
// `map[string]string` — and Cisco's captive-portal reply is *two* `Cisco-AVPair`
// attributes, `url-redirect` and `url-redirect-acl`. One of them is lost by
// construction. The wire format is `internal/raddb`'s and `rlm_rest`'s, not this
// package's, so this reports the loss loudly rather than inventing an encoding:
// a joined value would be a guess about how a NAS parses AV-pairs, and a
// silently dropped one is the failure `docs/rules/nas-drivers.md` calls
// reporting success on a no-op.
//
// The first occurrence of a name wins, so the order RenderReply chose decides
// which attribute survives, and the caller is told what did not.
func CollapseToMap(attrs []ReplyAttribute) (map[string]string, []string) {
	out := make(map[string]string, len(attrs))
	var dropped []string
	for _, a := range attrs {
		if _, seen := out[a.Name]; seen {
			dropped = append(dropped, a.Name+"="+a.Value)
			continue
		}
		out[a.Name] = a.Value
	}
	sort.Strings(dropped)
	return out, dropped
}

// avPair is the shared shape of a vendor attribute whose value is itself a
// `key=value` string, which is how Cisco carries everything.
func avPair(key, value string) string {
	return key + "=" + value
}

// unrenderableExcept subtracts what this profile *can* render from the standard
// list, so a vendor file states only what it adds.
//
// Derive the list rather than restating it: a profile that hand-wrote its own
// Unrenderable would be a second copy of standardUnrenderable's field set, and
// it would go stale the day ReplyDecision gains a field — still passing, still
// printing a reason, and now describing a decision shape that no longer exists.
func unrenderableExcept(d ReplyDecision, rendered ...string) []string {
	can := make(map[string]bool, len(rendered))
	for _, r := range rendered {
		can[r] = true
	}
	var missing []string
	for _, m := range standardUnrenderable(d) {
		if !can[m] {
			missing = append(missing, m)
		}
	}
	return missing
}

// kbps renders a rate for attributes that take kilobits per second.
func kbps(v int) string { return fmt.Sprintf("%d", v) }

// bps renders a rate for attributes that take bits per second, which WISPr does.
func bps(v int) string { return fmt.Sprintf("%d", v*1000) }

// VendorForDriver maps a device's driver — and, for the CoA family, its chosen
// vendor — onto the vendor whose profile renders its replies.
//
// **It lives here rather than in each caller**, because two callers with two
// copies of this switch is the shape `docs/rules/testing.md` names four times:
// a stale second copy of a rule whose authority was sitting right there. One of
// those four was a capability set beside `unifi.go`, one package away from this
// line.
//
// `false` is the one answer that must never be defaulted: a `coa` row naming no
// vendor. Cisco and Aruba identify a session differently and a wrong guess is **an
// ACK with no effect** — the one failure a fake NAS cannot produce, because a
// device that ignores what it cannot match answers success.
//
// `ruckus` resolves deliberately. Foyer ships no Ruckus driver, so
// `ShippedCapabilityEvidence` answers nothing for it and `VendorProfileFor` falls
// back to the standard RFC-only profile — which is the honest *we would send only
// what is a standard*, and not the same statement as *nobody chose a vendor*.
func VendorForDriver(driver, coaVendor string) (Vendor, bool) {
	switch driver {
	case "unifi":
		return VendorUniFi, true
	case "omada":
		return VendorOmada, true
	case "mikrotik":
		return VendorMikroTik, true
	case "ruckus":
		return VendorRuckus, true
	case "coa":
		switch coaVendor {
		case "cisco":
			return VendorCisco, true
		case "aruba":
			return VendorAruba, true
		}
		return "", false
	}
	return "", false
}

// EvidenceFor returns the two evidence maps for a vendor, together.
//
// Together rather than separately because the whole point is that they disagree:
// every wire encoding a shipped profile emits is DOCUMENTED, every capability of
// the CoA family is UNVERIFIED, and a caller that fetched one of them alone would
// be able to render a screen that says only half of that.
func EvidenceFor(v Vendor) (CapabilityEvidence, AttributeEvidence) {
	return ShippedCapabilityEvidence(v), VendorProfileFor(v).AttributeEvidence()
}

// EvidenceLower is the contract's spelling of an Evidence value.
//
// `docs/protocol-reality.md` shouts these three words and this package keeps that
// spelling, because the whole point of the label is that it is loud in a source
// file. The contract's enum is lowercase, as every enum in that document is.
//
// **The conversion is a function and not a cast**, and that is the finding rather
// than a style note: `gen.EvidenceStanding(fact.Evidence)` compiles, produces
// `"UNVERIFIED"`, and is not a member of the enum — so the server would have
// answered with a value no client has a branch for, and Go's own generated
// `Valid()` is not called on a response. It was caught by dumping the real values
// while building the fixture, not by any test.
func EvidenceLower(e Evidence) string {
	switch e {
	case EvidenceMeasured:
		return "measured"
	case EvidenceDocumented:
		return "documented"
	case EvidenceUnverified:
		return "unverified"
	}
	// An Evidence value with no contract spelling. Returning the raw string would
	// put a non-member in a response; the honest answer is the weakest standing,
	// because a fact nobody can classify is not one to present as known.
	return "unverified"
}
