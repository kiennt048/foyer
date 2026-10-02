package nas

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// Vendor represents a supported hardware or controller vendor.
type Vendor string

const (
	VendorUniFi    Vendor = "unifi"
	VendorOmada    Vendor = "omada"
	VendorAruba    Vendor = "aruba"
	VendorCisco    Vendor = "cisco"
	VendorRuckus   Vendor = "ruckus"
	VendorMikroTik Vendor = "mikrotik"
	VendorManual   Vendor = "manual"
)

// WalledGardenType defines the driver's pre-authentication traffic filter capability.
//
// Three-valued on purpose: an IP-range-only garden is materially weaker than
// FQDN/SNI and must be documented as such. See docs/rules/nas-drivers.md.
type WalledGardenType string

const (
	WalledGardenFQDN WalledGardenType = "fqdn"
	WalledGardenIP   WalledGardenType = "ip"
	WalledGardenNone WalledGardenType = "none"
)

// Capabilities defines what operations a specific vendor driver can perform.
//
// A capability says what the *driver* can do on the operator's behalf, not what
// the product may offer. A false one may make an action impossible — hide the
// control — or merely manual, in which case the control stays and the price is
// stated beside it. See docs/rules/nas-drivers.md; the narrower reading has
// been the true one five times on this project.
type Capabilities struct {
	CanAuthorizeGuest bool             `json:"canAuthorizeGuest"`
	CanDeauthGuest    bool             `json:"canDeauthGuest"`
	HardQuota         bool             `json:"hardQuota"`
	BandwidthLimits   bool             `json:"bandwidthLimits"`
	WalledGarden      WalledGardenType `json:"walledGarden"`
	LiveReauthorize   bool             `json:"liveReauthorize"`
	RotateSecret      bool             `json:"rotateSecret"`
}

// Capability names, as they appear in Capabilities' JSON tags and therefore in
// the admin contract. Evidence is keyed on these, so a capability and the
// standing of its value cannot drift apart.
const (
	CapCanAuthorizeGuest = "canAuthorizeGuest"
	CapCanDeauthGuest    = "canDeauthGuest"
	CapHardQuota         = "hardQuota"
	CapBandwidthLimits   = "bandwidthLimits"
	CapWalledGarden      = "walledGarden"
	CapLiveReauthorize   = "liveReauthorize"
	CapRotateSecret      = "rotateSecret"
)

// Evidence is the standing of one capability value, using the same three words
// docs/protocol-reality.md uses. A capability nobody has measured is still a
// value the code must return — this is how it says so out loud instead of
// passing a guess off as a fact.
type Evidence string

const (
	// EvidenceMeasured — observed on real equipment, with a date and a method.
	EvidenceMeasured Evidence = "MEASURED"
	// EvidenceDocumented — from an RFC or vendor documentation, with a source.
	EvidenceDocumented Evidence = "DOCUMENTED"
	// EvidenceUnverified — believed, not confirmed. Not usable as a guarantee.
	EvidenceUnverified Evidence = "UNVERIFIED"
)

// CapabilityFact records how a single capability value is known.
type CapabilityFact struct {
	Evidence Evidence `json:"evidence"`
	// Note carries the citation for a DOCUMENTED or MEASURED value, and for an
	// UNVERIFIED one it names the measurement that would settle it. An
	// UNVERIFIED fact with no named measurement is a guess wearing a label.
	Note string `json:"note"`
}

// CapabilityEvidence maps capability names to how their values are known.
type CapabilityEvidence map[string]CapabilityFact

// AuthorizeRequest contains client authorization limits to push to the controller/NAS.
type AuthorizeRequest struct {
	ClientMAC        string
	APMAC            string
	SiteID           string
	TimeLimitMinutes int
	DataUsageLimitMB int
	RxRateLimitKbps  int
	TxRateLimitKbps  int
}

// DeauthorizeRequest contains parameters to revoke client access.
type DeauthorizeRequest struct {
	ClientMAC string
	APMAC     string
	SiteID    string
	Reason    string
}

// DiscoveredAP represents an Access Point discovered from a controller.
type DiscoveredAP struct {
	MAC       string `json:"mac"`
	Name      string `json:"name"`
	IPAddress string `json:"ipAddress,omitempty"`
	Model     string `json:"model,omitempty"`
}

// ProbeResult represents connectivity and capability diagnostics for a controller.
type ProbeResult struct {
	Reachable              bool     `json:"reachable"`
	Version                string   `json:"version,omitempty"`
	PresenceCheckSupported bool     `json:"presenceCheckSupported"`
	Errors                 []string `json:"errors,omitempty"`

	// Flavour is the deployment shape the probe found, never the one the
	// operator was asked to declare. See UniFiDriver.
	Flavour string `json:"flavour,omitempty"`
	// APIBase is the full base URL the vendor API actually answered on, so a
	// support bundle records the path that worked rather than the one we assumed.
	APIBase string `json:"apiBase,omitempty"`
	// AuthMode is which credential the driver will use: "api-key" or "legacy-login".
	AuthMode string `json:"authMode,omitempty"`
	// Warnings are conditions the operator must see but which do not make the
	// controller unusable — a disabled certificate check, a capability nobody
	// has measured, a version below the documented floor.
	//
	// **A kind, not a sentence.** The contract's `ProbeWarning` is a kind plus
	// params, so the only way to fill it from an English sentence would be to
	// match on the prose — a hand-maintained copy with a regular expression on
	// top, which is the failure `docs/rules/database.md` names.
	Warnings []ProbeWarning `json:"warnings,omitempty"`
	// Unverified names the capabilities whose values are not yet measured or
	// documented for this driver, so a registration screen can say so.
	Unverified []string `json:"unverified,omitempty"`

	// VendorSites are the controller's OWN sites, by the id its API matches on.
	//
	// `UniFiDriver.Probe` requests `/v1/sites` anyway, to find the prefix that
	// answers; this carries the body out. Without it discovery has nothing to
	// adopt into, `Controller.vendorSites` stays `[]`, and
	// `discoverFromController` — which refuses without one — is unreachable.
	//
	// It matters more than a list: a UniFi controller's Integration API site id is
	// a UUID, and its LEGACY api uses the name `default`. The legacy name sent to
	// the Integration API is answered `400 'default' is not a valid 'siteId'
	// value` — measured against a real controller, in `foyerctl probe-unifi` and
	// through this driver.
	VendorSites []VendorSite `json:"vendorSites,omitempty"`
}

// VendorSite is one of a controller's own sites, as the controller names it.
//
// The ID is whatever the API this driver speaks matches on, which is NOT
// interchangeable between a vendor's APIs: UniFi's Integration API uses a UUID and
// its legacy API uses a short name for the same site. Anything storing one must
// record which API it came from.
type VendorSite struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// WarningKind is what a probe warning *is*, as distinct from how it reads.
//
// The set here is `internal/nas`'s and is deliberately **wider** than the
// contract's `ProbeWarningKind`: a driver reports what it observed, and whether
// a condition can arise through the admin API is a different question answered
// one layer out. WarnCertificateVerificationDisabled is the case — the contract
// argues, correctly, that its `TlsMode` cannot produce it and that a kind
// nothing emits is a catalogue entry nobody can show renders. The driver can
// still be constructed with verification off by `foyerctl` and by `lab/`, and a
// driver that stayed silent about it there would be worse than a kind the API
// drops.
type WarningKind string

const (
	// WarnAccessPointUnconfirmed — the driver cannot check the access point a
	// NAS redirect claimed. security.md §4 asks for MAC **and** AP **and** site
	// **and** still-unauthorized; three of the four are checked and the fourth
	// is not. Unconditional on UniFi, which is the whole reason it must travel:
	// a warning that only appears when something is broken is one an operator
	// learns to expect the absence of.
	WarnAccessPointUnconfirmed WarningKind = "access-point-unconfirmed"
	// WarnVersionBelowFloor — the controller is below the documented floor for
	// the API this driver needs. Params: version, minimumVersion. The second is
	// what turns a verdict into an instruction.
	WarnVersionBelowFloor WarningKind = "version-below-floor"
	// WarnCapabilitiesUnverified — at least one capability value in this answer
	// is believed rather than measured. Params: count. It names no capability
	// itself: ProbeResult.Unverified names which, and two lists of the same
	// thing is how they drift.
	WarnCapabilitiesUnverified WarningKind = "capabilities-unverified"
	// WarnCertificateVerificationDisabled — the driver was built to accept any
	// certificate, so an on-path attacker can read the API key. Params: tlsMode.
	WarnCertificateVerificationDisabled WarningKind = "certificate-verification-disabled"
	// WarnCoAAcknowledgedUnknownSession — the NAS answered Disconnect-ACK to a
	// probe naming a session id it cannot have issued. Params: sessionId.
	//
	// **This kind is the one place the "one file, no core changes" claim visibly
	// does not hold**, and it is stated rather than worked around. A driver that
	// observes a condition no existing kind names
	// has to add one here, because the kinds are the core's vocabulary. The
	// boundary already tolerates it: `apistore.warningsOut` drops any kind the
	// contract does not name rather than coercing it, so this needs no contract
	// change and produces no console copy for a kind nobody wrote copy for.
	WarnCoAAcknowledgedUnknownSession WarningKind = "coa-acknowledged-unknown-session"
)

// ProbeWarning is one condition an operator must see that does not make the
// device unusable.
//
// Kind and Params are the fact. Detail is a sentence for whoever reads a Go
// value directly — `foyerctl probe-unifi`'s output, a log line, a support
// bundle — and it is **not** the wire format: the admin API sends the kind and
// the console owns the copy, so nothing downstream ever parses this string.
// That split is the point. A warning that carries a parameter carries it
// in Params, never formatted into Detail, because the moment a figure exists
// only inside a sentence the only way to get it back is to parse the sentence.
type ProbeWarning struct {
	Kind   WarningKind    `json:"kind"`
	Params map[string]any `json:"params,omitempty"`
	Detail string         `json:"detail,omitempty"`
}

// AccessPointUnconfirmedWarning is the security.md §4 notice, in one place.
//
// One place because several producers have to agree on it: the CoA family
// attaches it to every probe, the UniFi driver attaches it when a controller
// cannot answer the device list, and MockDriver has to stand in for both. A
// literal in the fake would be a second copy of a fact whose authority is one
// file away, and a copy like that sends every capability-gated test down a
// branch no deployment takes.
//
// `reason` says **why this producer could not confirm it**. The UniFi client
// object identifies the access point and the driver confirms it, so on UniFi the
// sentence is about a controller rather than about a gap in the product. A fixed
// wording such as *"the controller's client object carries no field to compare
// it against"* would be false of the CoA family and of UniFi alike — one
// sentence describing three different situations.
//
// Nothing downstream parses this: the console renders its own copy from the
// kind. See ProbeWarning.
func AccessPointUnconfirmedWarning(reason string) ProbeWarning {
	return ProbeWarning{
		Kind:   WarnAccessPointUnconfirmed,
		Params: map[string]any{"reason": reason},
		Detail: "the access point claimed in the NAS redirect was not confirmed: " + reason,
	}
}

// VersionBelowFloorWarning names both versions, because the floor is the half
// that turns a verdict into an instruction.
func VersionBelowFloorWarning(version, minimum string) ProbeWarning {
	return ProbeWarning{
		Kind:   WarnVersionBelowFloor,
		Params: map[string]any{"version": version, "minimumVersion": minimum},
		Detail: fmt.Sprintf("controller version %s is below %s, the documented floor for the API this "+
			"driver needs", version, minimum),
	}
}

// CapabilitiesUnverifiedWarning is derived from the list it stands for, never
// written beside it.
//
// It returns false when there is nothing to warn about, so a caller cannot
// append an empty one: a warning saying "0 capabilities are unmeasured" is a
// caveat over a controller that has none, and nobody has one of those.
func CapabilitiesUnverifiedWarning(unverified []string) (ProbeWarning, bool) {
	if len(unverified) == 0 {
		return ProbeWarning{}, false
	}
	return ProbeWarning{
		Kind:   WarnCapabilitiesUnverified,
		Params: map[string]any{"count": len(unverified)},
		Detail: fmt.Sprintf("%d capability value(s) in this answer are believed rather than measured: %s",
			len(unverified), strings.Join(unverified, ", ")),
	}, true
}

// PresenceOutcome says why VerifyPresence answered as it did. The bool is what
// the portal gates on; the outcome is what an operator standing in front of a
// guest needs, and "the device is not on this network" and "the device is
// already authorized" are not the same conversation.
type PresenceOutcome string

const (
	PresenceVerified          PresenceOutcome = "verified"
	PresenceNotFound          PresenceOutcome = "not-found"
	PresenceAlreadyAuthorized PresenceOutcome = "already-authorized"
	PresenceSiteMismatch      PresenceOutcome = "site-mismatch"
	PresenceAPMismatch        PresenceOutcome = "ap-mismatch"
	PresenceUnavailable       PresenceOutcome = "unavailable"
)

// Presence is the full answer that VerifyPresence's bool truncates.
type Presence struct {
	OK       bool            `json:"ok"`
	Outcome  PresenceOutcome `json:"outcome"`
	ClientID string          `json:"clientId,omitempty"`
	SiteID   string          `json:"siteId,omitempty"`

	// APConfirmed reports whether the claimed access point was actually checked
	// against the controller's answer, rather than accepted because the answer
	// carried nothing to check it against.
	//
	// security.md §4 asks for MAC + AP + site + still-unauthorized. **On the
	// UniFi path this is a measurement rather than a constant**: the client
	// object carries `uplinkDeviceId` and the site's device list resolves
	// it to the access point's MAC, so a claim that holds sets this true and a
	// claim that is contradicted never reaches here — it is PresenceAPMismatch.
	//
	// False on a passing answer therefore means one specific thing: **nothing was
	// compared.** No access point was claimed, or the controller places this
	// client under no device, or the id it names is not a device in the site. It
	// is not a synonym for "the driver cannot do this", which is what it meant
	// before UniFi could confirm one, and what it still means on the CoA family.
	APConfirmed bool `json:"apConfirmed"`
}

// NASDriver defines the unified contract implemented by all hardware and controller drivers.
type NASDriver interface {
	Vendor() Vendor
	Capabilities() Capabilities
	VerifyPresence(ctx context.Context, claimedMAC, apMAC, siteID string) (bool, error)
	AuthorizeGuest(ctx context.Context, req AuthorizeRequest) error
	DeauthorizeGuest(ctx context.Context, req DeauthorizeRequest) error
	DiscoverAccessPoints(ctx context.Context) ([]DiscoveredAP, error)
	Probe(ctx context.Context) (*ProbeResult, error)
}

// PresenceDetailer is implemented by drivers that can say *why* presence
// verification answered as it did. Optional on purpose: NASDriver keeps the
// two-value shape the portal already calls, and a driver that can do better
// says so by implementing this rather than by widening the interface.
type PresenceDetailer interface {
	VerifyPresenceDetail(ctx context.Context, claimedMAC, apMAC, siteID string) (Presence, error)
}

// EvidencedDriver is implemented by drivers that can say how each capability
// value is known. A driver that does not implement it is treated as having no
// evidence at all — which is the honest reading, not a permissive one.
type EvidencedDriver interface {
	CapabilityEvidence() CapabilityEvidence
}

// UnverifiedCapabilities lists, sorted, the capability names whose values this
// driver cannot back with a measurement or a citation.
//
// It exists so a registration screen can refuse to present an unmeasured
// capability as a fact, and so a test can fail the day somebody promotes one
// without taking the measurement.
func UnverifiedCapabilities(d NASDriver) []string {
	ev, ok := d.(EvidencedDriver)
	if !ok {
		return nil
	}
	var names []string
	for name, fact := range ev.CapabilityEvidence() {
		if fact.Evidence == EvidenceUnverified {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Registry manages driver factory instances.
type Registry struct {
	mu      sync.RWMutex
	drivers map[string]NASDriver
}

var defaultRegistry = &Registry{
	drivers: make(map[string]NASDriver),
}

// Register registers a driver instance for a controller/NAS ID.
func Register(id string, driver NASDriver) {
	defaultRegistry.mu.Lock()
	defer defaultRegistry.mu.Unlock()
	defaultRegistry.drivers[id] = driver
}

// Get retrieves a driver by ID.
func Get(id string) (NASDriver, error) {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()
	driver, exists := defaultRegistry.drivers[id]
	if !exists {
		return nil, fmt.Errorf("driver not found for id %s", id)
	}
	return driver, nil
}

// capabilityReadURL is a placeholder base URL for a driver built only to read
// its capabilities off. Nothing dials it: NewUniFiDriver assembles an HTTP
// client and Capabilities() returns constants.
const capabilityReadURL = "https://capabilities.invalid"

// ---------------------------------------------------------------------------
// The shipped-driver registry
// ---------------------------------------------------------------------------

// shippedDrivers maps a vendor to a factory that builds Foyer's driver for it,
// configured only well enough to read its own claims off.
//
// # A registry, not a switch statement per question
//
// `docs/rules/nas-drivers.md` claims *"a new driver is one file with no core
// changes"*. A `switch vendor` in each of `ShippedCapabilities`,
// `ShippedCapabilityEvidence` and `ShippedProbeWarnings` would make that false:
// adding a driver family would mean editing all three, in `driver.go`, the core.
// And `MockDriver` derives its capabilities from these functions, so a vendor
// with a real driver and a `default:` arm would produce a fake claiming the floor
// while the driver claims otherwise — a mock more capable than the driver.
//
// So this does what `RegisterVendorProfile` does for the other half of this
// package: registration from each driver's own `init()`, and no table here to
// append to. The three functions keep their signatures and read this
// map. `TestAddingACoAVendorNeedsNoCoreChange` proves the claim by building a
// fictional vendor's driver entirely inside a test file.
var shippedDrivers = map[Vendor]func() NASDriver{}

// RegisterShippedDriver records the factory for Foyer's driver for one vendor.
// Called from that driver's own file, never from here.
//
// The factory must not dial anything: it is called to read constants off, from
// tests and from a registration screen, and a factory that opened a socket would
// make `ShippedCapabilities` a network call.
func RegisterShippedDriver(v Vendor, build func() NASDriver) {
	if build == nil {
		panic("nas: RegisterShippedDriver(nil)")
	}
	if _, dup := shippedDrivers[v]; dup {
		panic(fmt.Sprintf("nas: two shipped drivers registered for %q", v))
	}
	shippedDrivers[v] = build
}

// ShippedVendors lists, sorted, the vendors Foyer ships a driver for.
func ShippedVendors() []Vendor {
	out := make([]Vendor, 0, len(shippedDrivers))
	for v := range shippedDrivers {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// shippedDriver builds the driver for a vendor, or nil where Foyer ships none.
func shippedDriver(vendor Vendor) NASDriver {
	build, ok := shippedDrivers[vendor]
	if !ok {
		return nil
	}
	return build()
}

// ShippedCapabilities reports what Foyer's built driver for a vendor claims it
// can do — read off that driver rather than restated here.
//
// Derive the stand-in from what it stands in for. A literal here is a second
// copy of a fact whose authority is one file away, and a literal in
// newMockDriver can be wrong on both fields that decide whether an admin control
// exists at all: claiming WalledGardenFQDN, the strongest of the three, where the
// UniFi driver reports WalledGardenNone, the weakest; and claiming
// RotateSecret, which no vendor in the design can do. Every Go test that reached
// a capability gate through the mock would then exercise the branch no
// deployment takes.
func ShippedCapabilities(vendor Vendor) Capabilities {
	if d := shippedDriver(vendor); d != nil {
		return d.Capabilities()
	}
	// Foyer has no driver for this vendor, so there is nothing to derive a
	// claim from and inventing one is the defect this function exists to
	// stop. A driver that does not exist can do nothing on the operator's
	// behalf.
	return Capabilities{WalledGarden: WalledGardenNone}
}

// ShippedCapabilityEvidence reports how the built driver for a vendor knows what
// it claims, or nil where Foyer ships no driver for it.
//
// Same rule as ShippedCapabilities, applied to the second thing a registration
// screen reads. A fake whose probe says "no unmeasured capabilities here" is
// agreeing with the operator about three values nobody has measured.
func ShippedCapabilityEvidence(vendor Vendor) CapabilityEvidence {
	d := shippedDriver(vendor)
	if d == nil {
		return nil
	}
	ev, ok := d.(EvidencedDriver)
	if !ok {
		return nil
	}
	return ev.CapabilityEvidence()
}

// ShippedProbeWarnings reports the warnings the built driver for a vendor
// attaches to **every** probe, whatever the controller answers.
//
// Same rule as ShippedCapabilities, applied to the third thing a registration
// screen reads. A fake whose probe carries no unconditional warning is a
// controller with no caveats, and nobody has one of those — on UniFi the
// access-point check security.md §4 requires is not performed at all on the CoA
// family, and that is true before the driver has dialled anything.
//
// **It is not true of every driver, and the emptiness is a result.** The UniFi
// driver confirms the access point, so its answer is nil and its probe raises the
// notice only against a controller that cannot resolve one. A caller that reads a non-empty list as "this vendor is fine" and an
// empty one as "nothing was asked" has it backwards — see
// ShippedConfirmsAccessPoint, which is the other half of the same fact.
//
// Nothing here dials: the warnings are assembled from what the driver knows
// about itself, which is why they can be read off a driver pointed at an
// address that does not resolve.
func ShippedProbeWarnings(vendor Vendor) []ProbeWarning {
	if w, ok := shippedDriver(vendor).(unconditionalWarner); ok {
		return w.unconditionalWarnings()
	}
	// No driver, or one that reports no unconditional caveat. Nothing observes
	// anything and inventing a caveat is the same defect as inventing a
	// capability.
	return nil
}

// ShippedConfirmsAccessPoint reports whether the built driver for a vendor has
// any mechanism for security.md §4's access-point clause.
//
// It is the half that makes an empty `ShippedProbeWarnings` readable. For a
// driver that cannot confirm an access point, "carries the §4 notice" and
// "cannot check the access point" are the same statement; for one that can, they
// come apart: an absent notice means *this driver checks* on UniFi and *somebody
// forgot* on a driver that does not — and nothing in the list distinguishes
// them. This does, and the registry test cross-checks the two against each
// other rather than against a table of expectations.
//
// A vendor with no driver answers false: it confirms nothing because it is
// nothing.
func ShippedConfirmsAccessPoint(vendor Vendor) bool {
	c, ok := shippedDriver(vendor).(accessPointConfirmer)
	return ok && c.canConfirmAccessPoint()
}

// accessPointConfirmer is a driver that can say whether it has a mechanism for
// checking the access point a NAS redirect claims.
//
// Unexported, and about the *mechanism* rather than about a controller: a UniFi
// driver whose controller is down still has the mechanism, and reports the
// outage through its probe. Deciding it per call would make this a second,
// weaker copy of what VerifyPresenceDetail already answers.
type accessPointConfirmer interface {
	canConfirmAccessPoint() bool
}

// unconditionalWarner is a driver that can report the warnings it attaches to
// every probe **without dialling**, so a registration screen and a fake can both
// read them off it.
//
// Unexported on purpose, and the consequence is worth stating rather than
// discovering: a driver defined outside `internal/nas` cannot implement it, and
// therefore gets nil here. That is not a gap to close — every driver in the
// design lives in this package, and exporting the method would put it on the
// `NASDriver` interface for the sake of a value only this package's own
// stand-in reads.
type unconditionalWarner interface {
	unconditionalWarnings() []ProbeWarning
}

// MockDriver provides an in-memory controllable driver for testing and lab environments.
//
// Two of its defaults were chosen against the failure they can hide, and both
// are worth reading before trusting a test that uses this.
//
// Read DefaultPresent first. A mock answering "yes, that device is here" to
// every MAC encodes the assumption VerifyPresence exists to refuse, and
// docs/rules/testing.md is explicit that a hand-written NAS mock encodes the
// author's assumptions. NewMockDriver keeps the permissive default because
// callers already depend on it; NewStrictMockDriver is the one to reach for when
// the test is about a refusal.
//
// Read Caps second, and note that it is not a literal. It comes from
// ShippedCapabilities, so a test that reaches a capability gate through this
// mock reaches the branch a deployment reaches. A test that needs a capability
// the shipped driver does not have says so with WithCapabilities, in one place,
// where the assumption is visible to whoever reads the failure.
type MockDriver struct {
	// mu guards every field below it that a call reads or writes.
	//
	// A mock in a unit test needs none of this — it is configured, then used,
	// from one goroutine. It exists for the two suites that drive a live
	// `httptest` server: the records are written from whatever goroutine is
	// serving, and a test that flips an answer mid-suite races a handler
	// goroutine that outlives the response it already sent. Both shapes are
	// real here — two guests redeeming one voucher in the same millisecond, and
	// a refusal test that then makes the driver permissive to prove its own
	// assertion could ever have been red.
	//
	// **Set fields through the `With…` methods when a server is running**; the
	// exported fields stay exported because setup writes them before anything
	// serves, which is safe and reads better than five more methods.
	mu sync.Mutex

	VendorType     Vendor
	Caps           Capabilities
	Evidence       CapabilityEvidence
	Presences      map[string]bool
	DefaultPresent bool
	Authorized     map[string]AuthorizeRequest
	Deauthorized   map[string]string
	APList         []DiscoveredAP
	ProbeOutcome   *ProbeResult

	// authorizeLog is every AuthorizeRequest in the order it arrived, which the
	// Authorized map cannot express: it is keyed by MAC, so a second
	// authorization of one device overwrites the first and two of them count as
	// one. "The driver was asked once" and "the driver was asked twice for the
	// same MAC" are different findings, and the second is the one a double
	// submit produces. Read it with AuthorizeLog.
	authorizeLog []AuthorizeRequest

	// hooks run inside the driver call. See MockHooks.
	hooks MockHooks

	// Clients is what this mock's controller actually knows, and it is the half
	// Presences cannot express. A bool answers "is it here"; a controller
	// answers "here, in this site, on this AP, and already let in" — four facts,
	// four different conversations with a guest standing in front of you. See
	// MockClient.
	Clients map[string]MockClient

	// Faults are the failures this mock can be asked to produce. Every call on
	// NASDriver could fail against a real controller and none of them could
	// here, and a fake that cannot fail leaves every failure branch in
	// the product uncovered, including the one that decides whether a CoA
	// fallback is justified.
	Faults MockFaults
}

// MockClient is one client this mock's controller knows about.
//
// It exists because VerifyPresenceDetail could produce two of the six
// PresenceOutcome values — verified and not-found — while the shipped UniFi
// driver produces four. Three of the missing ones are distinctions the product
// already draws: an already-authorized guest must reach the connected page
// rather than being told the device is not on the network, and an unreachable
// controller must not read as the guest's fault. Neither could be driven from
// the shipped fake, so two duplicate hand-rolled fakes were written instead.
//
// Every field is checked only when it carries something to check against, which
// is the same rule Presence.APConfirmed states: an answer that carried nothing
// to compare is not a pass, it is an unchecked claim, and it is reported as one.
type MockClient struct {
	// ID is the controller's own identifier for the client, returned as
	// Presence.ClientID. UniFi's action endpoint takes this and never the MAC.
	ID string

	// SiteID is the site the controller has this client in. Empty means the
	// controller does not place it and the claimed site is not checked; a value
	// that differs from the claimed one is PresenceSiteMismatch.
	SiteID string

	// APMAC is the access point the controller says it is associated with.
	// Empty is not an oversight — it is the UniFi Integration API's own
	// situation, where nothing in the client object is known to identify an
	// access point. Empty leaves APConfirmed false; a value that differs
	// from the claimed one is PresenceAPMismatch.
	APMAC string

	// Authorized is whether the controller has already let this client in.
	// PresenceAlreadyAuthorized is not a refusal, and a caller that renders it
	// as one tells a guest who is already online that they are not on the
	// network.
	Authorized bool
}

// MockFaults injects failures into a MockDriver, one field per call plus the
// one that is not per-call.
//
// The split is the point. Unreachable is a property of the controller and
// therefore fails everything at once — a controller you cannot reach does not
// answer presence and refuse a deauthorization. The per-call fields are for a
// controller that is up and refusing one thing.
type MockFaults struct {
	// Unreachable makes every call fail the way an unreachable controller does:
	// VerifyPresenceDetail answers PresenceUnavailable *with* a non-nil error
	// rather than a bare false, the action calls fail, DiscoverAccessPoints
	// fails, and Probe reports not reachable.
	Unreachable error

	// Authorize fails AuthorizeGuest only.
	Authorize error

	// Deauthorize fails DeauthorizeGuest only. This is the fault that drives
	// /problems/session-end-failed, which had no mock-driven route to it.
	Deauthorize error

	// Discover fails DiscoverAccessPoints only.
	Discover error

	// Probe fails Probe only. Distinct from Unreachable: a controller can answer
	// a probe and still be refusing the credential it was given.
	Probe error
}

// MockHooks are functions that run **inside** a driver call, before it does
// anything.
//
// A recorded call says the driver was asked. A hook says *when*, which is a
// different claim and the one the portal's ordering rule needs: Android
// destroys the login WebView the instant the network validates, so nothing the
// guest must see may be written after the grant call. A test proves that by
// blocking inside AuthorizeGuest and requiring the 303 to arrive anyway — and
// it cannot be written against a fake that only counts.
//
// The hook runs before the faults are consulted, so a test can observe a call
// that is about to fail.
type MockHooks struct {
	// OnAuthorize runs at the top of AuthorizeGuest. It may block.
	OnAuthorize func()
	// OnDeauthorize runs at the top of DeauthorizeGuest.
	OnDeauthorize func()
}

// WithHooks sets what runs inside this mock's calls, following
// WithCapabilities' precedent of one written line rather than a struct literal
// spread over a test.
func (m *MockDriver) WithHooks(mutate func(*MockHooks)) *MockDriver {
	m.mu.Lock()
	defer m.mu.Unlock()
	mutate(&m.hooks)
	return m
}

// AuthorizeLog returns a copy of every AuthorizeRequest this mock received, in
// order. Safe to call while requests are in flight.
func (m *MockDriver) AuthorizeLog() []AuthorizeRequest {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]AuthorizeRequest(nil), m.authorizeLog...)
}

// AuthorizeCount is AuthorizeLog's length, for the common assertion.
func (m *MockDriver) AuthorizeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.authorizeLog)
}

// ErrMockControllerUnreachable is the default transport failure this mock
// produces.
//
// It is deliberately not a copy of any shipped driver's wording. A fake that
// reproduces the real one's message strings is a second copy of a fact whose
// authority is one file away, and it goes stale silently; a test that needs the
// real wording should assert against the real driver. What a caller may rely on
// here is the *shape* — a non-nil error beside PresenceUnavailable — which is
// the thing the product branches on.
var ErrMockControllerUnreachable = errors.New("mock: controller unreachable")

// MockAPIRefusal returns the error shape of a controller that answered and
// refused, as opposed to one that could not be reached.
//
// The two are not interchangeable and a driver decides something on the
// difference: UniFi's DeauthorizeGuest falls back to RFC 5176 Disconnect only
// when the controller could not be reached, never when it answered and said no,
// because disconnecting anyway would be acting on our own opinion of its
// answer. A fake with only one failure mode cannot drive both sides of that
// branch, so this produces the *APIError a caller matches with errors.As.
func MockAPIRefusal(status int, detail string) error {
	return &APIError{
		Method: http.MethodPost,
		URL:    "https://mock-controller.invalid/action",
		Status: status,
		Body:   detail,
	}
}

// NewMockDriver creates a new mock driver that answers present for any MAC not
// listed in Presences.
func NewMockDriver(vendor Vendor) *MockDriver {
	m := newMockDriver(vendor)
	m.DefaultPresent = true
	return m
}

// NewStrictMockDriver creates a mock driver that answers *not* present for any
// MAC not listed in Presences — the default a security test wants, because the
// failure it guards is a grant issued for a device that never associated.
func NewStrictMockDriver(vendor Vendor) *MockDriver {
	return newMockDriver(vendor)
}

// WithCapabilities raises this mock above what the shipped driver for its vendor
// reports, and returns it so a test can say what it is assuming in one line.
//
// The opt-in is deliberately asymmetric. A test that wrongly assumes a
// capability is absent fails loudly and gets fixed; a test that wrongly assumes
// one is present passes forever against a product nobody ships. So the default
// is the honest low one and the raise is a written sentence:
//
//	d := nas.NewMockDriver(nas.VendorUniFi).WithCapabilities(func(c *nas.Capabilities) {
//		c.RotateSecret = true // this test is about the push path; no shipped driver reaches it
//	})
//
// The evidence is left alone on purpose: raising a value in a test does not
// measure it, and CapabilityEvidence is where that distinction lives.
func (m *MockDriver) WithCapabilities(mutate func(*Capabilities)) *MockDriver {
	m.mu.Lock()
	defer m.mu.Unlock()
	mutate(&m.Caps)
	return m
}

// WithDefaultPresent changes the permissive default **while the mock is in
// use**, which the field on its own cannot do safely.
//
// It exists for one shape and it is worth naming, because it is the only reason
// this type has a mutex at all: a refusal test that then makes the driver
// answer *present*, re-submits the identical request, and requires a grant to
// appear — the sabotage a lock carries inside itself so that "this refusal was
// the presence check" is re-derived on every run rather than asserted once.
// A `httptest` handler goroutine from the previous request may still be
// finishing when the switch is thrown, and a bare field write there is a data
// race the race detector reports as a test failure.
func (m *MockDriver) WithDefaultPresent(v bool) *MockDriver {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.DefaultPresent = v
	return m
}

// WithClient tells this mock what its controller knows about one MAC, and
// returns it so a test reads as one line.
//
//	d := nas.NewStrictMockDriver(nas.VendorUniFi).
//		WithClient(mac, nas.MockClient{Authorized: true}) // already online
//
// It takes precedence over Presences and DefaultPresent, because it is the
// richer answer and the bool is the thing it exists to stop truncating.
func (m *MockDriver) WithClient(mac string, c MockClient) *MockDriver {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Clients == nil {
		m.Clients = make(map[string]MockClient)
	}
	m.Clients[normalizeMAC(mac)] = c
	return m
}

// WithFaults sets what this mock fails at, following WithCapabilities'
// precedent: the assumption is a written sentence in one place rather than a
// struct literal spread over a test.
//
//	d := nas.NewMockDriver(nas.VendorUniFi).WithFaults(func(f *nas.MockFaults) {
//		f.Deauthorize = nas.MockAPIRefusal(409, "session already ended")
//	})
func (m *MockDriver) WithFaults(mutate func(*MockFaults)) *MockDriver {
	m.mu.Lock()
	defer m.mu.Unlock()
	mutate(&m.Faults)
	return m
}

// WithControllerUnreachable makes every call to this mock fail the way a
// controller nobody can reach fails. Pass nil for ErrMockControllerUnreachable.
//
// One knob rather than five, because "the controller is unreachable" is one
// fact about the world and a fake that lets a test set it for presence but not
// for revocation is describing a controller that does not exist.
func (m *MockDriver) WithControllerUnreachable(err error) *MockDriver {
	if err == nil {
		err = ErrMockControllerUnreachable
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Faults.Unreachable = err
	return m
}

func newMockDriver(vendor Vendor) *MockDriver {
	evidence := ShippedCapabilityEvidence(vendor)
	m := &MockDriver{
		VendorType:   vendor,
		Caps:         ShippedCapabilities(vendor),
		Evidence:     evidence,
		Presences:    make(map[string]bool),
		Authorized:   make(map[string]AuthorizeRequest),
		Deauthorized: make(map[string]string),
		APList: []DiscoveredAP{
			{MAC: "00:11:22:33:44:55", Name: "Mock AP 1", IPAddress: "192.168.1.10", Model: "U6-Pro"},
		},
	}
	m.ProbeOutcome = &ProbeResult{
		Reachable:              true,
		Version:                "9.1.105",
		PresenceCheckSupported: true,
		// Derived, not omitted. The shipped UniFi probe names three capabilities
		// it cannot back with a measurement; a fake that names none is a
		// controller with no caveats, which is not a controller anyone has.
		Unverified: UnverifiedCapabilities(m),
	}
	return m
}

// probeWarnings is what this fake reports, assembled from the vendor's shipped
// caveats **and from this fake's own world**.
//
// `ShippedProbeWarnings(vendor)` alone would be right only if every driver's §4
// access-point answer were a constant. It is not: the shipped UniFi driver
// confirms the access point and raises the notice only against a controller that
// cannot resolve one. A fake that went on copying the
// vendor's list would report *no caveats* for a mock controller that places no
// client under any access point — which is exactly what `NewMockDriver` is.
//
// So the notice is derived from the same thing the mock's own
// `VerifyPresenceDetail` derives `APConfirmed` from: whether any client here
// carries an access point to compare against. That is not a second copy of the
// driver's facts — the authority for what this fake can confirm is this fake.
//
// The shipped list still comes first and is not duplicated, so a CoA-family mock
// carries that family's unconditional notice and nothing is emitted twice.
func (m *MockDriver) probeWarnings() []ProbeWarning {
	out := append([]ProbeWarning(nil), ShippedProbeWarnings(m.VendorType)...)
	for _, w := range out {
		if w.Kind == WarnAccessPointUnconfirmed {
			return out
		}
	}
	if m.canConfirmAccessPoint() {
		return out
	}
	return append(out, AccessPointUnconfirmedWarning(
		"this mock controller places no client under an access point, so there is nothing here to "+
			"compare a claimed one against"))
}

// canConfirmAccessPoint reports whether this fake holds anything that could
// confirm an access-point claim.
//
// A mock with no client records answers every presence question by default and
// therefore confirms nothing; one carrying a MockClient with an APMAC compares
// for real, and its presence answers already say so. Reading the same field for
// both keeps the probe and the presence answer from disagreeing about the same
// controller.
func (m *MockDriver) canConfirmAccessPoint() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.Clients {
		if c.APMAC != "" {
			return true
		}
	}
	return false
}

// CapabilityEvidence implements EvidencedDriver.
func (m *MockDriver) CapabilityEvidence() CapabilityEvidence {
	return m.Evidence
}

func (m *MockDriver) Vendor() Vendor {
	return m.VendorType
}

func (m *MockDriver) Capabilities() Capabilities {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Caps
}

// faults snapshots what this mock is currently failing at.
func (m *MockDriver) faults() MockFaults {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Faults
}

func (m *MockDriver) VerifyPresence(ctx context.Context, claimedMAC, apMAC, siteID string) (bool, error) {
	p, err := m.VerifyPresenceDetail(ctx, claimedMAC, apMAC, siteID)
	return p.OK, err
}

// VerifyPresenceDetail implements PresenceDetailer for the mock.
//
// Three sources, most specific first: an unreachable controller answers nothing
// at all, then Clients — which is the only one that can produce the four
// outcomes a bool cannot — then the Presences/DefaultPresent pair every existing
// caller already uses, unchanged.
// The read is taken under the lock, because a test may flip an answer while a
// server is serving. WithDefaultPresent says why that is not hypothetical.
func (m *MockDriver) VerifyPresenceDetail(_ context.Context, claimedMAC, apMAC, siteID string) (Presence, error) {
	m.mu.Lock()
	unreachable := m.Faults.Unreachable
	client, known := m.Clients[normalizeMAC(claimedMAC)]
	present, listed := m.Presences[claimedMAC]
	byDefault := m.DefaultPresent
	m.mu.Unlock()

	if unreachable != nil {
		// Outcome *and* error, never a bare false. A caller reading only the
		// bool would classify a venue outage as a refusal and tell the guest
		// their device is not on the network.
		return Presence{Outcome: PresenceUnavailable, SiteID: siteID}, unreachable
	}
	if known {
		return client.presence(apMAC, siteID), nil
	}
	if listed {
		if !present {
			return Presence{Outcome: PresenceNotFound, SiteID: siteID}, nil
		}
		return Presence{OK: true, Outcome: PresenceVerified, SiteID: siteID}, nil
	}
	if byDefault {
		return Presence{OK: true, Outcome: PresenceVerified, SiteID: siteID}, nil
	}
	return Presence{Outcome: PresenceNotFound, SiteID: siteID}, nil
}

// presence resolves one known client against what the redirect claimed.
//
// SiteID in the answer is always the site the question was asked about, never
// the one the controller believes: the mismatch is reported as an outcome, and a
// caller that echoed the controller's site back into a portal page would be
// handing an attacker with an unsigned query string a directory of site names.
func (c MockClient) presence(apMAC, siteID string) Presence {
	p := Presence{ClientID: c.ID, SiteID: siteID}
	if c.SiteID != "" && siteID != "" && c.SiteID != siteID {
		p.Outcome = PresenceSiteMismatch
		return p
	}
	if c.Authorized {
		p.Outcome = PresenceAlreadyAuthorized
		return p
	}
	if c.APMAC != "" && apMAC != "" && normalizeMAC(c.APMAC) != normalizeMAC(apMAC) {
		p.Outcome = PresenceAPMismatch
		return p
	}
	p.OK = true
	p.Outcome = PresenceVerified
	// Confirmed only when both sides carried an access point. Anything else is
	// an unchecked claim and says so rather than passing quietly.
	p.APConfirmed = c.APMAC != "" && apMAC != ""
	return p
}

// AuthorizeGuest records an authorization, and refuses a MAC this mock does not
// hold — which is what the shipped driver does and for the same reason.
//
// UniFi's AuthorizeGuest resolves the client on the controller before it can act
// (`unifi: no client %s in site %s`), so a device the controller has never seen
// cannot be authorized. A mock that authorized anything was agreeing to an
// operation the real one refuses. Under NewMockDriver every MAC is present, so
// this changes nothing; under NewStrictMockDriver it is the same refusal
// VerifyPresence makes, one call later.
func (m *MockDriver) AuthorizeGuest(ctx context.Context, req AuthorizeRequest) error {
	if hook := m.onAuthorize(); hook != nil {
		hook()
	}
	if req.ClientMAC == "" {
		return errors.New("client MAC is required")
	}
	faults := m.faults()
	if err := faults.Unreachable; err != nil {
		return err
	}
	if err := faults.Authorize; err != nil {
		return err
	}
	if present, _ := m.VerifyPresenceDetail(ctx, req.ClientMAC, req.APMAC, req.SiteID); !present.OK {
		return fmt.Errorf("mock: no client %s in site %s", req.ClientMAC, req.SiteID)
	}
	m.mu.Lock()
	m.Authorized[req.ClientMAC] = req
	m.authorizeLog = append(m.authorizeLog, req)
	m.mu.Unlock()
	return nil
}

func (m *MockDriver) onAuthorize() func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hooks.OnAuthorize
}

func (m *MockDriver) onDeauthorize() func() {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hooks.OnDeauthorize
}

// DeauthorizeGuest records a revocation, and can refuse — which it could not.
//
// Three ways to fail, and each one is a branch the product has:
//
//   - no MAC, which the shipped driver refuses outright;
//   - the controller is unreachable, which is the only failure that justifies a
//     CoA fallback, or an injected per-call fault, which is what an operable
//     controller answering "no" looks like (MockAPIRefusal);
//   - a client this controller has never seen. The shipped driver resolves the
//     client before it can act and cannot revoke one that is not there, so a
//     fake returning nil was agreeing to an operation the real one refuses.
//     Under NewMockDriver every MAC is present and this changes nothing; under
//     NewStrictMockDriver it is the same refusal AuthorizeGuest already makes.
func (m *MockDriver) DeauthorizeGuest(ctx context.Context, req DeauthorizeRequest) error {
	if hook := m.onDeauthorize(); hook != nil {
		hook()
	}
	if req.ClientMAC == "" {
		return errors.New("client MAC is required")
	}
	faults := m.faults()
	if err := faults.Unreachable; err != nil {
		return err
	}
	if err := faults.Deauthorize; err != nil {
		return err
	}
	// Not presence.OK: an already-authorized client is precisely the one a
	// revocation is for, and it answers OK == false.
	p, _ := m.VerifyPresenceDetail(ctx, req.ClientMAC, req.APMAC, req.SiteID)
	if p.Outcome == PresenceNotFound {
		return fmt.Errorf("mock: no client %s in site %s", req.ClientMAC, req.SiteID)
	}
	m.mu.Lock()
	m.Deauthorized[req.ClientMAC] = req.Reason
	delete(m.Authorized, req.ClientMAC)
	m.mu.Unlock()
	return nil
}

func (m *MockDriver) DiscoverAccessPoints(ctx context.Context) ([]DiscoveredAP, error) {
	faults := m.faults()
	if err := faults.Unreachable; err != nil {
		return nil, err
	}
	if err := faults.Discover; err != nil {
		return nil, err
	}
	return m.APList, nil
}

// Probe reports what this mock's controller answers.
//
// An unreachable controller returns a result *and* an error: the result is what
// a registration screen renders, and dropping it would leave the operator with
// an error string where the diagnostics belong.
//
// The failure results still carry the warnings, and that is not tidiness: a
// controller nobody can reach has not answered the access-point question, it has
// failed to be asked it, and a fake that dropped the notice on failure would be
// saying that an outage settles a security question it does not settle.
//
// The warnings are computed here rather than stored on ProbeOutcome, because
// they now depend on the mock's own world and a caller may populate that after
// construction — `WithClient` after `NewMockDriver` would otherwise leave the
// probe describing the controller this fake was before the test set it up.
func (m *MockDriver) Probe(ctx context.Context) (*ProbeResult, error) {
	faults := m.faults()
	if err := faults.Unreachable; err != nil {
		return &ProbeResult{
			Reachable: false, Errors: []string{err.Error()},
			Warnings: m.probeWarnings(),
		}, err
	}
	if err := faults.Probe; err != nil {
		return &ProbeResult{
			Reachable: true, Errors: []string{err.Error()},
			Warnings: m.probeWarnings(),
		}, err
	}
	if m.ProbeOutcome == nil {
		return nil, nil
	}
	out := *m.ProbeOutcome
	out.Warnings = m.probeWarnings()
	return &out, nil
}
