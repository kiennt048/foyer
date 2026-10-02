package apistore

import (
	"context"
	"errors"
	"fmt"
	"github.com/kiennt048/foyer/internal/api"
	"net/netip"
	"sort"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
)

// DeviceOperations — the driver seam: everything that leaves the process and talks
// to somebody's network.
//
// It is behind an interface rather than a direct dependency for two reasons that
// `internal/api` already states: the API must be drivable with no controller in the
// room, and `docs/rules/nas-drivers.md` is explicit that a hand-written NAS mock
// encodes the author's assumptions — the same assumptions that produced the bug. So
// the real drivers are here, built from stored credentials, and a fake behind
// `Drivers` is labelled a fake rather than mistaken for coverage.

// ErrNoDriver is what an operation on a controller Foyer cannot drive answers.
var ErrNoDriver = errors.New("apistore: no driver for this controller")

// Drivers builds a NASDriver for a registered controller.
//
// The credential arrives unsealed, because unsealing is the store's and building an
// HTTP client is the driver's, and neither should be able to do the other's job.
type Drivers interface {
	ForController(c store.Controller, credential []byte, vendorSiteID string) (nas.NASDriver, error)
}

// VendorDrivers is the real thing.
type VendorDrivers struct{}

// ForController implements Drivers.
//
// Only UniFi is built, and the other two vendors return `ErrNoDriver` rather than
// something that answers plausibly. Omada and Ruckus have drivers in the design and
// not in `internal/nas`, and a probe that returned `reachable` from a driver that
// does not exist would be exactly the defect the API rewrite was for.
func (VendorDrivers) ForController(c store.Controller, credential []byte, vendorSiteID string) (nas.NASDriver, error) {
	switch gen.ControllerVendor(c.Vendor) {
	case gen.ControllerVendorUnifi:
		cfg := nas.UniFiConfig{
			BaseURL: c.BaseURL,
			Site:    vendorSiteID,
			TLSMode: nas.TLSMode(c.TLSMode),
			// `trust-on-first-use` pins what was seen. A change is a refusal
			// rather than a prompt, which is why the fingerprint travels with the
			// mode: controllers ship self-signed certificates, and every shipped
			// integration answers that by disabling verification entirely.
			PinnedSHA256: deref(c.CertificateFingerprint),
			Flavour:      nas.UniFiFlavour(deref(c.Flavour)),
		}
		switch gen.ControllerAuthMode(c.AuthMode) {
		case gen.ApiKey:
			cfg.APIKey = string(credential)
		default:
			cfg.Username = deref(c.Username)
			cfg.Password = string(credential)
		}
		return nas.NewUniFiDriverWithError(cfg)
	default:
		return nil, fmt.Errorf("%w: %s", ErrNoDriver, c.Vendor)
	}
}

// driverFor unseals the credential and builds the driver.
//
// A controller with no credential still gets a driver: `probeController` has to be
// able to report *what* is missing, and refusing to build one would answer "no
// driver" where the truth is "no API key".
func (a *Adapter) driverFor(ctx context.Context, controllerID, vendorSiteID string) (nas.NASDriver, store.Controller, error) {
	c, err := a.st.Controllers().Get(ctx, controllerID)
	if err != nil {
		return nil, store.Controller{}, out(err)
	}
	var credential []byte
	if c.CredentialSet {
		credential, err = a.st.Controllers().Credential(ctx, controllerID)
		if err != nil && !errors.Is(err, store.ErrSecretUnset) {
			return nil, *c, err
		}
	}
	d, err := a.drivers.ForController(*c, credential, vendorSiteID)
	if err != nil {
		return nil, *c, err
	}
	return d, *c, nil
}

// ProbeController measures a controller and writes back what it measured.
//
// **The write-back is not the handler's, and that is a reported asymmetry.**
// `AccessPointRegistry` has a `RecordProbe` method and `ControllerRegistry` has
// none, so `probeController` returns its result and nothing persists it — which
// would leave `Controller.reachability` at `unknown` forever, and
// `discoverFromController` refuses on exactly that field. Discovery would therefore
// be unreachable through the API no matter how many times an operator pressed
// *Probe*. Recording it here is the narrowest fix available from this side of the
// seam; the port wants a `RecordControllerProbe` to match the one access points
// already have.
func (a *Adapter) ProbeController(ctx context.Context, c gen.Controller) (gen.ControllerProbe, error) {
	d, stored, err := a.driverFor(ctx, c.Id, "")
	if err != nil {
		if errors.Is(err, ErrNoDriver) {
			return unreachableProbe(err), nil
		}
		return gen.ControllerProbe{}, err
	}

	result, probeErr := d.Probe(ctx)
	probe := controllerProbeOut(result, probeErr, stored)

	// What the probe learned, written back through the repository that owns it.
	// The vendor site list is replaced rather than merged, because a site the
	// controller no longer reports has genuinely gone — and bindings survive by
	// vendor site id.
	measured := store.ControllerProbe{
		Reachability:     string(probe.Reachability),
		Version:          probe.Version,
		VersionSupported: probe.VersionSupported,
		Flavour:          probe.Flavour,
	}
	if probe.VendorSites != nil {
		sites := make([]store.VendorSite, 0, len(*probe.VendorSites))
		for _, vs := range *probe.VendorSites {
			sites = append(sites, store.VendorSite{ID: vs.Id, Name: vs.Name})
		}
		measured.VendorSites = sites
	}
	// trust-on-first-use pins what the first handshake saw.
	//
	// The driver accepts any certificate while nothing is pinned and reports what
	// it accepted; storing it here is what turns "first contact" into a single
	// moment. Without this write every contact stays a first contact and the mode
	// is insecure-skip-verify wearing a better name — which is the failure to
	// watch for, not the acceptance itself.
	//
	// Only when nothing is pinned yet. A stored fingerprint is never overwritten
	// from a probe: a controller presenting a DIFFERENT certificate is exactly what
	// the mode exists to refuse, and re-pinning on sight would make the refusal
	// unreachable. Changing it is an operator's decision, through updateController.
	if stored.TLSMode == string(nas.TLSTrustOnFirstUse) && deref(stored.CertificateFingerprint) == "" {
		if o, ok := d.(interface{ ObservedFingerprint() string }); ok {
			if fp := o.ObservedFingerprint(); fp != "" {
				measured.CertificateFingerprint = &fp
			}
		}
	}

	if err := a.st.Controllers().RecordProbe(ctx, c.Id, measured, a.now()); err != nil {
		return gen.ControllerProbe{}, out(err)
	}
	return probe, nil
}

// controllerProbeOut renders a driver's ProbeResult as the contract's answer.
//
// A failed probe is a `200` describing a failure rather than a `5xx`: the operation
// did what it was asked, and what it found out is that the controller cannot be
// reached. Refusing would leave the console with no way to render *why*.
func controllerProbeOut(r *nas.ProbeResult, err error, c store.Controller) gen.ControllerProbe {
	type check = struct {
		Detail *string                         `json:"detail,omitempty"`
		Kind   gen.ControllerProbeChecksKind   `json:"kind"`
		Result gen.ControllerProbeChecksResult `json:"result"`
	}
	// `warnings` and `unverified` are `required` in the contract, so they are empty
	// and never nil: a nil slice marshals to `null`, which is not an empty array and
	// is the difference between *nothing to warn about* and *this server does not
	// answer that question*.
	probe := gen.ControllerProbe{
		Checks:     []check{},
		Warnings:   []gen.ProbeWarning{},
		Unverified: []string{},
	}

	if r == nil || err != nil {
		probe.Reachability = gen.NasReachabilityUnreachable
		probe.Checks = append(probe.Checks, check{
			Kind:   gen.Reachable,
			Result: gen.ControllerProbeChecksResultFail,
		})
		// A failed probe still carries whatever the driver knew before it dialled.
		// The §4 access-point notice is the case: it is a fact about what this
		// driver can check, not about what the controller answered, and dropping
		// it here would let an outage look like it settled a security question.
		if r != nil {
			probe.Warnings = warningsOut(r.Warnings, nil)
		}
		return probe
	}

	// The capability values this driver believes rather than measures, carried
	// straight through from `nas.ProbeResult.Unverified` — which is itself derived
	// from the driver's own `CapabilityEvidence`, so nothing here restates it.
	if len(r.Unverified) > 0 {
		probe.Unverified = append([]string{}, r.Unverified...)
	}
	// And the warnings. `internal/nas` emits kinds, so this is a kind-to-kind
	// translation with no prose in it.
	probe.Warnings = warningsOut(r.Warnings, probe.Unverified)

	pass := gen.ControllerProbeChecksResultPass
	fail := gen.ControllerProbeChecksResultFail
	reachable := fail
	if r.Reachable {
		reachable = pass
	}
	probe.Checks = append(probe.Checks, check{Kind: gen.Reachable, Result: reachable})

	// A controller that answers but rejects the credential is
	// `credentials-expired`, not `unreachable`. They are different faults with
	// different fixes, and the console's copy for each is different.
	credentialFailed := false
	for _, e := range r.Errors {
		if strings.Contains(strings.ToLower(e), "credential") ||
			strings.Contains(strings.ToLower(e), "unauthor") ||
			strings.Contains(e, "401") || strings.Contains(e, "403") {
			credentialFailed = true
		}
	}
	credential := pass
	if credentialFailed || !c.CredentialSet {
		credential = fail
	}
	probe.Checks = append(probe.Checks, check{Kind: gen.Credential, Result: credential})

	presence := fail
	if r.PresenceCheckSupported {
		presence = pass
	}
	// `docs/rules/security.md` §4 makes presence mandatory before any grant, so
	// this is the check a driver cannot ship without whatever else passes.
	probe.Checks = append(probe.Checks, check{Kind: gen.PresenceCheck, Result: presence})

	switch {
	case !r.Reachable:
		probe.Reachability = gen.NasReachabilityUnreachable
	case credentialFailed:
		probe.Reachability = gen.NasReachabilityCredentialsExpired
	default:
		probe.Reachability = gen.NasReachabilityOk
	}
	if r.Version != "" {
		probe.Version = ptr(r.Version)
	}
	if r.Flavour != "" {
		probe.Flavour = ptr(r.Flavour)
	}
	// The controller's own sites, carried out so ProbeController can store them.
	// Without this, `Controller.vendorSites` stays `[]` and
	// `discoverFromController` — which refuses without one — is unreachable no
	// matter how many times an operator presses Probe.
	if len(r.VendorSites) > 0 {
		type vendorSite = struct {
			Id   string `json:"id"`
			Name string `json:"name"`
		}
		sites := make([]vendorSite, 0, len(r.VendorSites))
		for _, vs := range r.VendorSites {
			sites = append(sites, vendorSite{Id: vs.ID, Name: vs.Name})
		}
		probe.VendorSites = &sites
	}
	return probe
}

// warningsOut renders a driver's probe warnings as the contract's, and derives
// the one the contract asks for that no driver reports.
//
// **Kind to kind, and nothing else.** The driver's English sentence stays in the
// driver: `ProbeWarning.detail` exists in the contract as a fallback for a client
// that does not know a kind, and filling it from here would put two sources
// behind one sentence — which is how they drift, and is the reason `dashboard.go`
// leaves `Alarm.detail` unset on all eight kinds it emits.
//
// Two rules that look like omissions and are not:
//
//   - **`certificate-verification-disabled` is dropped on purpose.** The contract
//     argues it out: `TlsMode` offers only `verify` and `trust-on-first-use`, so
//     a driver built through this API cannot have verification off, and a kind
//     nothing can emit is a catalogue entry nobody can show renders. `foyerctl`
//     and `lab/` *can* build one, which is why `internal/nas` still reports it —
//     the drop belongs here, at the boundary that knows what the API can produce,
//     rather than in a driver that would then be silent everywhere.
//   - **Any kind the contract does not name is dropped**, never coerced into one
//     it does. A warning rendered as the wrong kind is worse than a missing
//     warning: the console has copy for every kind and would show an operator a
//     confident sentence about something else.
//
// `capabilities-unverified` is derived from the very list the same answer
// carries, so the count and the names cannot disagree. That is the kind-not-prose
// rule applied to the one warning that is a fact about the answer rather than about
// the controller.
func warningsOut(in []nas.ProbeWarning, unverified []string) []gen.ProbeWarning {
	out := []gen.ProbeWarning{}
	for _, w := range in {
		kind := gen.ProbeWarningKind(w.Kind)
		if !kind.Valid() {
			continue
		}
		gw := gen.ProbeWarning{Kind: kind}
		if len(w.Params) > 0 {
			p := map[string]any(w.Params)
			gw.Params = &p
		}
		out = append(out, gw)
	}
	if w, ok := nas.CapabilitiesUnverifiedWarning(unverified); ok {
		p := map[string]any(w.Params)
		out = append(out, gen.ProbeWarning{
			Kind:   gen.ProbeWarningKind(w.Kind),
			Params: &p,
		})
	}
	return out
}

func unreachableProbe(err error) gen.ControllerProbe {
	type check = struct {
		Detail *string                         `json:"detail,omitempty"`
		Kind   gen.ControllerProbeChecksKind   `json:"kind"`
		Result gen.ControllerProbeChecksResult `json:"result"`
	}
	_ = err
	return gen.ControllerProbe{
		Reachability: gen.NasReachabilityUnreachable,
		Checks:       []check{{Kind: gen.Reachable, Result: gen.ControllerProbeChecksResultFail}},
		// See `controllerProbeOut`: empty, never nil.
		Warnings:   []gen.ProbeWarning{},
		Unverified: []string{},
	}
}

// Discover reconciles rather than adds.
//
// A device the controller no longer reports is marked missing, never deleted:
// deleting it would drop the MAC that resolves last week's session rows, and an
// access point that dropped off for ten minutes is not the same fact as one an
// operator removed.
//
// The shared secret is created once per site and reused, because that is what a
// controller actually holds: one field in a RADIUS profile, presented by every
// device under it. `clients.conf` is therefore N addresses against one secret.
func (a *Adapter) Discover(ctx context.Context, c gen.Controller, siteID, vendorSiteID string) (gen.DiscoveryResult, error) {
	if vendorSiteID == "" {
		vendorSiteID = vendorSiteFrom(c, siteID)
	}
	d, stored, err := a.driverFor(ctx, c.Id, vendorSiteID)
	if err != nil {
		return gen.DiscoveryResult{}, err
	}
	if siteID == "" {
		// A refusal the operator can act on, not a 500.
		//
		// The contract calls siteId optional and says it "defaults to the
		// controller's" — which is true only once a controller is bound to one, and
		// a freshly registered controller is bound to nothing. Without this, the
		// first discovery on a real controller answers "Something went wrong" for a
		// condition whose entire remedy is naming a site.
		return gen.DiscoveryResult{}, api.ErrNeedsSite
	}

	found, err := d.DiscoverAccessPoints(ctx)
	if err != nil {
		return gen.DiscoveryResult{}, err
	}

	secretID, err := a.secretForSite(ctx, siteID, stored.Name)
	if err != nil {
		return gen.DiscoveryResult{}, err
	}

	devices := make([]store.DiscoveredAP, 0, len(found))
	for _, ap := range found {
		d := store.DiscoveredAP{
			MAC:       ap.MAC,
			Name:      ap.Name,
			State:     string(gen.NasReachabilityUnknown),
			RadiusVia: "self",
		}
		if ap.Model != "" {
			d.Model = ptr(ap.Model)
		}
		if addr, err := netip.ParseAddr(ap.IPAddress); err == nil {
			d.IPAddress = &addr
		} else {
			// No address means no `clients.conf` row can be derived, so the
			// device is adopted with its RADIUS binding unset. An unmatched
			// source address then shows up as a first-class blind spot rather
			// than as a guess that silently drops packets.
			d.RadiusVia = "controller"
		}
		devices = append(devices, d)
	}

	result, err := a.st.AccessPoints().Reconcile(ctx, c.Id, siteID, secretID, stored.Vendor, devices, a.now())
	if err != nil {
		return gen.DiscoveryResult{}, out(err)
	}
	// Binding the controller's site to the Foyer site is what makes the adoption
	// repeatable: without it the next discovery has to be told the vendor site
	// again, and a hardcoded `default` is a filed bug in a shipped integration.
	if vendorSiteID != "" {
		_ = a.st.Controllers().BindVendorSite(ctx, c.Id, vendorSiteID, ptr(siteID))
	}
	return gen.DiscoveryResult{
		Adopted:        result.Adopted,
		Updated:        result.Updated,
		Missing:        result.Missing,
		SharedSecretId: result.SharedSecretID,
		AccessPointIds: ptr(result.AccessPointIDs),
	}, nil
}

func vendorSiteFrom(c gen.Controller, siteID string) string {
	for _, vs := range c.VendorSites {
		if vs.BoundSiteId != nil && *vs.BoundSiteId == siteID {
			return vs.Id
		}
	}
	return ""
}

// secretForSite returns the site's existing shared secret, or mints one.
//
// One per site rather than one per device, because rotating it has to be able to
// tell the operator the truth: *this changes the secret for these eleven devices,
// and you paste the new one into your controller once.*
func (a *Adapter) secretForSite(ctx context.Context, siteID, label string) (string, error) {
	existing, err := a.st.SharedSecrets().ListForSite(ctx, siteID)
	if err != nil {
		return "", out(err)
	}
	if len(existing) > 0 {
		return existing[0].ID, nil
	}
	value, err := a.newSecret()
	if err != nil {
		return "", err
	}
	ss := &store.SharedSecret{ID: a.newID("sec-"), SiteID: siteID, Label: label}
	if err := a.st.SharedSecrets().Create(ctx, ss, []byte(value)); err != nil {
		return "", out(err)
	}
	return ss.ID, nil
}

// ProbeAccessPoint reports what is known about one device, and says what it did not
// measure.
//
// **Three of the four answers are `not-measured`, and that is the honest state of
// the system rather than a stub.** Whether a device enforces `Message-Authenticator`
// and whether it is sending accounting are facts about RADIUS traffic, and nothing
// in this process has sent it an Access-Request: that is `lab/`'s `radclient` suite
// and the four gates in `internal/raddb`. Returning `enforced` here on the strength of the *configured*
// `require_message_authenticator` would be the console reporting a BlastRADIUS
// measurement it never took — the configured value and the observed one are
// different claims, and the alarm is keyed on the observed one.
//
// What *is* measured: the capabilities, from the driver that would carry an
// authorization for this device, and the controller's view of whether the access
// point is up. A device with no controller has neither, and says so.
func (a *Adapter) ProbeAccessPoint(ctx context.Context, n gen.Nas) (gen.NasProbe, error) {
	type check = struct {
		Detail *string                  `json:"detail,omitempty"`
		Name   string                   `json:"name"`
		Result gen.NasProbeChecksResult `json:"result"`
	}
	probe := gen.NasProbe{
		Reachability:         n.Reachability,
		Capabilities:         n.Capabilities,
		MessageAuthenticator: gen.NasProbeMessageAuthenticatorNotMeasured,
		Accounting:           gen.NasProbeAccountingNotMeasured,
		Checks:               []check{},
		// Required, so empty rather than nil — see `controllerProbeOut`.
		Warnings:   []gen.ProbeWarning{},
		Unverified: []string{},
	}
	skip := gen.NasProbeChecksResultSkip
	probe.Checks = append(probe.Checks,
		check{Name: "message-authenticator", Result: skip,
			Detail: ptr("Needs a RADIUS exchange; the lab suite is what measures it.")},
		check{Name: "accounting", Result: skip,
			Detail: ptr("Needs an Interim-Update or a Stop to have arrived.")},
	)

	// ── a device with no controller has a driver of its own ──────────────────
	//
	// Stopping here with *"This device has no controller to ask"* would be true
	// and would read as *"there is nothing to ask"*. The CoA family **is** its own
	// controller: `internal/nas` says so in as many words, and its `Probe` is the
	// one request RFC 5176 has that changes nothing. Stopping would probe a
	// MikroTik or a Cisco registered by hand by asking nobody, and answer with the
	// three `not-measured` results and a shrug.
	if n.ControllerId == nil {
		probe.Checks = append(probe.Checks, check{Name: "controller", Result: skip,
			Detail: ptr("This device has no controller; Foyer speaks to it directly.")})
		return a.probeDirectly(ctx, n, probe)
	}

	d, _, err := a.driverFor(ctx, *n.ControllerId, "")
	if err != nil {
		probe.Reachability = gen.NasReachabilityUnknown
		probe.Checks = append(probe.Checks, check{Name: "controller", Result: gen.NasProbeChecksResultFail})
		return probe, nil
	}

	// Capabilities are the driver's own, measured where `internal/nas` has taken
	// the measurement and conservative where it has not — `CapabilityEvidence` is
	// where an unmeasured `false` says so out loud, and promoting one here without
	// taking that measurement is exactly what the evidence list exists to prevent.
	caps := d.Capabilities()
	// Which of those values the driver cannot back with a measurement or a citation.
	// Read from the driver rather than listed here: a capability decides whether a
	// control **exists at all**, so an unmeasured one presented as a fact is a screen
	// offering something the hardware may not do — a green tick over unmeasured
	// claims, which is what a `NasProbe` with no field to carry this would show.
	if u := nas.UnverifiedCapabilities(d); len(u) > 0 {
		probe.Unverified = u
	}
	// ── the warnings have to be made before they can be carried ──────────────
	//
	// Answering out of `Capabilities()` and `DiscoverAccessPoints()` alone, without
	// calling `Probe()`, would leave the contract's `warnings` an empty array on a
	// path where no driver had been asked to produce one. The console renders the
	// array, so an operator would read a device with no caveats over a driver that
	// has one on every probe.
	//
	// The **error is deliberately not consulted**. Diagnosing a controller is
	// `probeController`'s job and it renders the failure properly; what this path
	// wants is the warnings, and the driver attaches the unconditional ones before
	// it dials — so a controller that could not be reached still reports that its
	// access-point check does not happen, which remains true either way. A nil
	// result yields the derived warning and nothing else.
	result, _ := d.Probe(ctx)
	var reported []nas.ProbeWarning
	if result != nil {
		reported = result.Warnings
	}
	probe.Warnings = warningsOut(reported, probe.Unverified)
	probe.Capabilities = gen.Capabilities{
		LiveReauthorize: caps.LiveReauthorize,
		WalledGarden:    gardenKindOut(caps.WalledGarden),
		HardQuota:       caps.HardQuota,
		BandwidthLimits: caps.BandwidthLimits,
		RotateSecret:    caps.RotateSecret,
		CanDeauthGuest:  caps.CanDeauthGuest,
		// `internal/nas.Capabilities` has no dynamic-VLAN member, so the stored
		// value is carried rather than invented.
		DynamicVlan: n.Capabilities.DynamicVlan,
	}
	probe.Checks = append(probe.Checks, check{Name: "controller", Result: gen.NasProbeChecksResultPass})

	if devices, err := d.DiscoverAccessPoints(ctx); err == nil && n.Mac != nil {
		want := store.NormaliseMAC(*n.Mac)
		probe.Reachability = gen.NasReachabilityUnreachable
		for _, ap := range devices {
			if store.NormaliseMAC(ap.MAC) == want {
				probe.Reachability = gen.NasReachabilityOk
				break
			}
		}
		probe.Checks = append(probe.Checks, check{Name: "present-on-controller",
			Result: passOrFail(probe.Reachability == gen.NasReachabilityOk)})
	}
	return probe, nil
}

// probeDirectly is `ProbeAccessPoint` for a device Foyer drives itself.
//
// **This is the caller the per-NAS seam needs.** A
// `NASDriverFor` that is tested and reachable from nothing an operator can press is
// indistinguishable from the vendor being unsupported: a complete MikroTik driver
// with no route to it.
//
// Two things separate this from the controller path above and both are honest
// differences rather than shortcuts:
//
//   - **The probe result decides reachability here.** On the controller path the
//     error is deliberately ignored, because `probeController` diagnoses that
//     device properly and this call only wants the warnings. On this family there
//     is no second probe: this is the only thing that ever dials the device, so
//     dropping the answer would leave `reachability` at whatever was last written.
//   - **It is a write, and RFC 5176 leaves no alternative.** The request is a
//     Disconnect naming a session id that cannot exist, which is the one request in
//     the protocol guaranteed to change nothing. `internal/nas` argues it out; the
//     alternative is a probe that measures nothing and says `ok`.
//
// A device with no driver — a `coa` row naming no vendor, a device with no shared
// secret — fails the driver check and keeps whatever reachability was already
// recorded. It is **not** marked unreachable: nothing was measured about the
// network, and *Foyer cannot build a driver for this row* is an answer about the
// row.
func (a *Adapter) probeDirectly(ctx context.Context, n gen.Nas, probe gen.NasProbe) (gen.NasProbe, error) {
	type check = struct {
		Detail *string                  `json:"detail,omitempty"`
		Name   string                   `json:"name"`
		Result gen.NasProbeChecksResult `json:"result"`
	}

	d, _, err := a.NASDriverFor(ctx, n.Id)
	if err != nil {
		probe.Checks = append(probe.Checks, check{
			Name:   "driver",
			Result: gen.NasProbeChecksResultFail,
			Detail: ptr(directDriverRefusal(err)),
		})
		return probe, nil
	}
	probe.Checks = append(probe.Checks, check{Name: "driver", Result: gen.NasProbeChecksResultPass})

	caps := d.Capabilities()
	if u := nas.UnverifiedCapabilities(d); len(u) > 0 {
		probe.Unverified = u
	}
	probe.Capabilities = gen.Capabilities{
		LiveReauthorize: caps.LiveReauthorize,
		WalledGarden:    gardenKindOut(caps.WalledGarden),
		HardQuota:       caps.HardQuota,
		BandwidthLimits: caps.BandwidthLimits,
		RotateSecret:    caps.RotateSecret,
		CanDeauthGuest:  caps.CanDeauthGuest,
		// `internal/nas.Capabilities` has no dynamic-VLAN member, so the stored
		// value is carried rather than invented. Same as the controller path.
		DynamicVlan: n.Capabilities.DynamicVlan,
	}

	result, _ := d.Probe(ctx)
	var reported []nas.ProbeWarning
	if result != nil {
		reported = result.Warnings
	}
	probe.Warnings = warningsOut(reported, probe.Unverified)

	// A nil result is a driver that could not answer at all, which is a different
	// fact from one that answered "not reachable" — so the reachability already on
	// record is left alone rather than overwritten with a guess.
	if result != nil {
		probe.Reachability = gen.NasReachabilityUnreachable
		if result.Reachable {
			probe.Reachability = gen.NasReachabilityOk
		}
		probe.Checks = append(probe.Checks, check{
			Name:   "reachable",
			Result: passOrFail(result.Reachable),
		})
	}
	return probe, nil
}

// directDriverRefusal turns a refusal into a sentence an operator can act on.
//
// **Never the error's own text.** These carry a package name and sometimes a row
// id, and `internal/api`'s rule is that an API error shows a human message and never
// a raw error or an internal path. The two conditions below are both things the
// operator fixes in a field, so each one names the field.
func directDriverRefusal(err error) string {
	switch {
	case errors.Is(err, ErrCoAVendorUnknown):
		return "This device is registered as CoA and records no vendor, so Foyer cannot " +
			"tell Cisco from Aruba. The two identify a session differently and the wrong " +
			"one is answered with an acknowledgement and no effect, so nothing was sent."
	case errors.Is(err, ErrNoNASDriver):
		return "Foyer has no way to speak to this device directly: it has no shared secret, " +
			"or its driver is one that is driven through a controller."
	default:
		return "Foyer could not build a driver for this device."
	}
}

func passOrFail(ok bool) gen.NasProbeChecksResult {
	if ok {
		return gen.NasProbeChecksResultPass
	}
	return gen.NasProbeChecksResultFail
}

// SecretRotation answers both questions rotation needs, together.
//
// `canPush` is `Capabilities.RotateSecret`, and the narrow reading is the true one:
// false means rotation is **manual**, not impossible. Gating *generate a new
// secret* on it would leave a site whose driver cannot push with no way to rotate a
// secret at all — the same over-broad reading as *UniFi cannot do CoA*.
//
// `class` comes from the classifier rather than from here. See ChangeClassifier.
// ── the column is a claim, the driver is the fact ────────────────────────────
//
// `n.Capabilities.RotateSecret` alone is not enough, because that column is
// **written** — by a registration, by a discovery run, by a fixture. `PushSecret`'s
// premise is about drivers, not about columns, so reading the column alone would
// send an operator whose row claims `true` straight into it: the control shown,
// the secret generated and stored, and the answer `/problems/internal`. A `500`
// after a write is the worst shape this could take, because the operator cannot tell whether the device now
// has a secret nobody can read.
//
// Pushing therefore requires **both**: the stored capability the console gated its
// control on, and a driver that actually implements it. The conjunction rather
// than the driver alone, because the column is what the console showed the
// operator and an answer that ignored it would be a different kind of surprise.
//
// Where they disagree the answer is *manual*, which is the narrow reading:
// rotation still happens, the new secret
// is generated and stored and returned once, and the operator enters it by hand.
// The alternative — refusing — would leave the device unable to rotate at all.
// ── "no controller" does not mean "no driver" ───────────────────────────────
//
// Short-circuiting `n.ControllerId == nil` to *manual* would be the right answer
// for the wrong reason: a device with no controller has a driver of its own, and
// asking it is the whole point of the conjunction above. No shipped driver reports
// `RotateSecret: true` today, so the answer is the same either way — but it is an
// answer rather than a coincidence, so the day one does, this reads it.
func (a *Adapter) SecretRotation(ctx context.Context, n gen.Nas) (bool, gen.ChangeClass, error) {
	class := a.changes.SecretChangeClass(n.Driver)
	if !n.Capabilities.RotateSecret {
		return false, class, nil
	}
	d, err := a.deviceDriver(ctx, n)
	if err != nil {
		// A device Foyer cannot build a driver for cannot be pushed to. That is
		// not a failure of the rotation — the secret is still Foyer's to
		// generate — so it is reported as manual rather than as an error.
		return false, class, nil
	}
	return d.Capabilities().RotateSecret, class, nil
}

// deviceDriver resolves whichever of the two seams this device is behind.
//
// **The controller wins where there is one**, and that is not a preference: a UniFi
// access point is driven through the controller that adopted it, and a per-NAS
// driver built from its `radius_clients` row would be a CoA client aimed at a radio
// that has no CoA listener and no shared secret an operator ever typed. `ForNAS`
// refuses the controller vendors for the same reason from the other side, so the
// two seams cannot both answer for one device.
func (a *Adapter) deviceDriver(ctx context.Context, n gen.Nas) (nas.NASDriver, error) {
	if n.ControllerId != nil {
		d, _, err := a.driverFor(ctx, *n.ControllerId, "")
		return d, err
	}
	d, _, err := a.NASDriverFor(ctx, n.Id)
	return d, err
}

// PushSecret writes a new shared secret to the device.
//
// **No driver in `internal/nas` can do this**, and every one of them reports
// `RotateSecret: false`, so `rotateNasSecret` never calls it: the API asks
// `SecretRotation` first and pushes only when it says yes. Reaching this function
// therefore means a driver has started claiming a capability it has not
// implemented, which is a defect worth an error rather than a silent success — a
// rotation that reported success and changed nothing on the device takes the device
// off the air at the next restart.
func (a *Adapter) PushSecret(_ context.Context, n gen.Nas, _ string) error {
	return fmt.Errorf("apistore: driver %q reports it can push a shared secret and cannot", n.Driver)
}

// EndSession, and the question before it, live in disconnect.go.

// ── how a device's claims are known ──────────────────────────────────────────

// DriverEvidence carries the CoA family's evidence to the console: that family
// ships with every capability UNVERIFIED, and **the label has to reach the
// console**, not live in the code.
//
// **Two lists, because they disagree, and the disagreement is the fact.** Every
// wire encoding a shipped profile emits is `documented` — the attribute names come
// from FreeRADIUS's own dictionaries and the vendor tables. Every capability of
// that same driver is `unverified`, because no Cisco, Aruba, MikroTik or Ruckus
// device has ever existed on this project and *an encoding that is right buys
// nothing if the device ignores the value inside it*. A client that merged them
// would answer *is this driver trustworthy*, which is not a question either list
// asks.
//
// **Nothing here builds a driver or reveals a secret.** The facts are properties
// of the vendor, identical before and after any packet, so this answers for a
// device nothing can reach — which is exactly the device an operator is looking at
// when they want to know what Foyer would send it.
func (a *Adapter) DriverEvidence(_ context.Context, n gen.Nas) (gen.DriverEvidence, error) {
	out := gen.DriverEvidence{
		Profile: gen.Resolved,
		// Non-nil, always. `docs/rules/frontend.md`: an absent array arrives as
		// `undefined`, a component reads that as *nothing to show*, and *we did not
		// look* becomes *there was nothing wrong*.
		Capabilities: []gen.EvidenceFact{},
		Attributes:   []gen.EvidenceFact{},
	}

	coaVendor := ""
	if n.CoaVendor != nil {
		coaVendor = string(*n.CoaVendor)
	}
	vendor, ok := nas.VendorForDriver(string(n.Driver), coaVendor)
	if !ok {
		// A `coa` row with no vendor. Cisco and Aruba identify a session
		// differently and a wrong guess is an ACK with no effect — the one failure
		// a fake NAS cannot produce — so "nobody said which vendor" is
		// representable and refused rather than defaulted.
		out.Profile = gen.VendorNotChosen
		return out, nil
	}

	caps, attrs := nas.EvidenceFor(vendor)
	out.Capabilities = factsOut(caps)
	out.Attributes = factsOut(attrs)
	return out, nil
}

// factsOut renders an evidence map as a sorted list.
//
// Sorted by subject so the console renders a stable order and a snapshot diff is
// readable — the same reason `UnverifiedCapabilities` sorts.
func factsOut[M ~map[string]nas.CapabilityFact](m M) []gen.EvidenceFact {
	out := make([]gen.EvidenceFact, 0, len(m))
	for subject, fact := range m {
		out = append(out, gen.EvidenceFact{
			Subject:  subject,
			Evidence: standingOut(fact.Evidence),
			Note:     fact.Note,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Subject < out[j].Subject })
	return out
}

// standingOut spells an evidence value the way the contract does. See
// nas.EvidenceLower — a cast compiles and answers with a non-member.
func standingOut(e nas.Evidence) gen.EvidenceStanding {
	return gen.EvidenceStanding(nas.EvidenceLower(e))
}
