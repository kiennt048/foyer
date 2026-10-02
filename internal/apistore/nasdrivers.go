package apistore

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
)

// The second driver seam: the one that is per-**NAS** rather than per-controller.
//
// ── why `Drivers.ForController` could not carry this ─────────────────────────
//
// `Drivers` takes a `store.Controller` and hands back a `NASDriver`, which is the
// right shape for the vendors that have a controller — UniFi, Omada, Ruckus. The CoA
// family does not. `internal/nas/coa_driver.go` says so in as many words: *"there is
// nothing to discover; the device is its own controller"*, and the contract agrees
// from the other side — `ControllerVendor` is `[unifi, omada, ruckus]` while
// `NasDriverKind` also has `mikrotik` and `coa`.
//
// So a MikroTik registered by hand had a `radius_clients` row, a shared secret, a CoA
// port and a driver in `internal/nas` — and **no seam through which the API could
// build it.** Every driver-gated path resolved a controller first and refused when
// there was none, which is correct behaviour on a seam that does not exist and is
// indistinguishable from the device being unsupported.
//
// ── why a second interface rather than a second method on `Drivers` ──────────
//
// `Drivers` is implemented outside this package — `tools/codegen/differ` has one, and
// so do two test harnesses. Adding a method to it would break those builds for a
// capability none of them has an opinion about. A separate interface is additive: a
// `Drivers` that also implements this one is used for both, and one that does not
// gets the real vendor drivers for the per-NAS half.

// NASDrivers builds a driver for one `radius_clients` row.
//
// The secret arrives unsealed for the same reason a controller credential does:
// unsealing is the store's job and speaking RADIUS is the driver's, and neither
// should be able to do the other's.
//
// `sessions` is not optional in any meaningful sense. RFC 5176 has no query in it, so
// a CoA driver has no way to confirm a device is present except Foyer's own
// accounting, and one built without a session source answers
// `nas.ErrNoSessionSource` — **never `true`** — for every presence check
// (`docs/rules/security.md` §4).
type NASDrivers interface {
	ForNAS(c store.RadiusClient, secret string, sessions nas.SessionSource) (nas.NASDriver, error)
}

// ErrNoNASDriver is what a `radius_clients` row Foyer cannot drive directly answers.
var ErrNoNASDriver = errors.New("apistore: no per-NAS driver for this device")

// ErrCoAVendorUnknown is the CoA family's own refusal, and it names a row rather
// than a gap in the drivers.
//
// **`driver: coa` covers Cisco and Aruba.** The two are different `CoAVendor`
// implementations because they identify a session differently — Cisco IOS-XE by a
// `Cisco-AVPair audit-session-id` VSA, Aruba by `Calling-Station-Id` in its own
// spelling — and `internal/nas` is explicit that a MAC in the wrong format
// identifies no session, which a careful NAS answers with Error-Cause 503 and a
// careless one answers by ignoring the request.
//
// So this refuses rather than guessing. Picking one would send Cisco VSAs to Aruba
// gear on half of all deployments and report success on a no-op, which is the exact
// failure `docs/rules/testing.md` says a lab cannot catch: *"ACKing a CoA is
// trivial; faithfully rejecting one is not."*
//
// **`radius_clients.coa_vendor` records it** (migration 00015), written by
// `createNas` through `coaVendorIn`. This error is still needed: the column is
// nullable because *nobody has said* has to stay representable — a `coa` row that
// predates the column, or one written by anything other than the registration
// path, has no vendor, and the honest answer for it is this refusal rather than a
// default. `createNas` is the gate that stops new ones
// being made; this is the gate that covers the rest.
var ErrCoAVendorUnknown = errors.New(
	"apistore: driver 'coa' covers Cisco and Aruba and this device records neither — " +
		"the session identification differs per vendor and guessing it reports success on a no-op")

// coaVendorFor resolves `radius_clients.coa_vendor` to the implementation that
// speaks that dialect.
//
// **There is no default branch that picks a vendor, and there must never be one.**
// Every other refusal in this package is visible: a bad address is a 422, an
// unreachable controller is a probe that says so. A guessed CoA vendor is invisible
// by construction — the request is well-formed, the NAS answers ACK, and nothing
// happens — so the case this function must get right is the one where it knows
// least.
func coaVendorFor(c store.RadiusClient) (nas.CoAVendor, error) {
	switch deref(c.CoAVendor) {
	case string(gen.Cisco):
		return nas.CiscoCoAVendor{}, nil
	case string(gen.Aruba):
		return nas.ArubaCoAVendor{}, nil
	case "":
		return nil, fmt.Errorf("%w: %s (%s) names no CoA vendor", ErrCoAVendorUnknown, c.Name, c.ID)
	default:
		// A value the contract does not name. Refused rather than coerced, for
		// the same reason as the empty one: `cisco` is not a safe reading of
		// something nobody here recognises.
		return nil, fmt.Errorf("%w: %s (%s) records CoA vendor %q, which is not one of them",
			ErrCoAVendorUnknown, c.Name, c.ID, deref(c.CoAVendor))
	}
}

// VendorDrivers implements NASDrivers as well as Drivers.
var _ NASDrivers = VendorDrivers{}

// ForNAS builds the driver for a device Foyer speaks to directly.
//
// The controller vendors are refused here on purpose rather than falling through to
// `ForController`: a UniFi access point is driven through the controller that adopted
// it, and a caller that got a driver from this seam for one would be holding a driver
// built from the wrong row.
func (VendorDrivers) ForNAS(c store.RadiusClient, secret string, sessions nas.SessionSource) (nas.NASDriver, error) {
	switch gen.NasDriverKind(c.Driver) {
	case gen.NasDriverKindMikrotik:
		return nas.NewCoADriver(coaConfig(c, secret, sessions, nas.MikroTikCoAVendor{}))
	case gen.NasDriverKindCoa:
		vendor, err := coaVendorFor(c)
		if err != nil {
			return nil, err
		}
		return nas.NewCoADriver(coaConfig(c, secret, sessions, vendor))
	default:
		return nil, fmt.Errorf("%w: %s is driven through its controller", ErrNoNASDriver, c.Driver)
	}
}

// coaConfig renders a `radius_clients` row as a CoA driver's configuration.
//
// **The port is a field and not a constant.** 3799 is common and Cisco frequently
// uses 1700; `radius_clients.coa_port` is nullable and zero means the driver's
// default, which is the one place that constant belongs.
//
// `NASIPAddress` comes from `address`, which is `inet NOT NULL`, so it always parses.
// A prefix covering a fleet — Aruba's dynamic-radius-proxy shape — yields its base
// address, which is the address CoA would be sent to and is the honest answer for a
// range whose members are not individually known.
func coaConfig(c store.RadiusClient, secret string, sessions nas.SessionSource, vendor nas.CoAVendor) nas.CoAConfig {
	cfg := nas.CoAConfig{
		Vendor:        vendor,
		Host:          c.Address.Addr().String(),
		Secret:        secret,
		Sessions:      sessions,
		NASIPAddress:  c.Address.Addr(),
		NASIdentifier: deref(c.NASIdentifier),
	}
	if c.CoAPort != nil {
		cfg.Port = *c.CoAPort
	}
	return cfg
}

// NASDriverFor unseals the shared secret and builds the driver for one device.
//
// The id is the **device's** — what `listNas` returns and what the console sends
// back — and the secret comes off `n.RadiusClient`, because rotating a secret acts on
// the clients.conf row rather than on the radio. Twelve
// access points behind one row share one secret, and a driver built per device from
// the wrong id would be built from twelve different ones.
//
// A device with no shared secret gets a refusal rather than a driver holding an empty
// one: a CoA request signed with the empty string is a packet the NAS silently drops,
// and the driver would report a timeout — *the device is unreachable* — for a
// configuration error an operator could fix in one field.
func (a *Adapter) NASDriverFor(ctx context.Context, deviceID string) (nas.NASDriver, store.RadiusClient, error) {
	n, err := a.st.RadiusClients().GetNas(ctx, deviceID)
	if err != nil {
		return nil, store.RadiusClient{}, out(err)
	}
	c := n.RadiusClient
	if c.SharedSecretID == nil {
		return nil, c, fmt.Errorf("%w: %s has no shared secret", ErrNoNASDriver, n.Name)
	}
	secret, err := a.st.SharedSecrets().Reveal(ctx, *c.SharedSecretID)
	if err != nil {
		return nil, c, out(err)
	}
	// The CoA timing an operator set for this client in the console, read per
	// driver build rather than cached, so a change binds on the next request with
	// no restart.
	var d nas.NASDriver
	timing, found, err := a.st.NetworkSettings().CoATimingFor(ctx, c.ID)
	if err != nil {
		return nil, c, out(err)
	}
	if td, ok := a.nasDrivers.(TimedNASDrivers); ok && found {
		d, err = td.ForNASTimed(c, string(secret), a, CoATiming{
			Timeout: time.Duration(timing.TimeoutMs) * time.Millisecond, Retries: timing.Retries,
		})
	} else {
		d, err = a.nasDrivers.ForNAS(c, string(secret), a)
	}
	if err != nil {
		return nil, c, err
	}
	return d, c, nil
}

// CoATiming is one RADIUS client's CoA timing, as an operator set it.
type CoATiming struct {
	Timeout time.Duration
	Retries int
}

// TimedNASDrivers is a NASDrivers that also takes the CoA timing stored for the
// client. A separate interface for the reason `NASDrivers` is one: implementations
// outside this package need not grow a method for a setting they have no view on,
// and one that lacks it builds its drivers with the defaults.
type TimedNASDrivers interface {
	ForNASTimed(c store.RadiusClient, secret string, sessions nas.SessionSource, t CoATiming) (nas.NASDriver, error)
}

var _ TimedNASDrivers = VendorDrivers{}

// ForNASTimed is ForNAS with the stored timing applied.
func (VendorDrivers) ForNASTimed(c store.RadiusClient, secret string, sessions nas.SessionSource, t CoATiming) (nas.NASDriver, error) {
	var vendor nas.CoAVendor
	switch gen.NasDriverKind(c.Driver) {
	case gen.NasDriverKindMikrotik:
		vendor = nas.MikroTikCoAVendor{}
	case gen.NasDriverKindCoa:
		v, err := coaVendorFor(c)
		if err != nil {
			return nil, err
		}
		vendor = v
	default:
		return VendorDrivers{}.ForNAS(c, secret, sessions)
	}
	cfg := coaConfig(c, secret, sessions, vendor)
	cfg.Timeout, cfg.Retries = t.Timeout, t.Retries
	return nas.NewCoADriver(cfg)
}

// ── the session source a CoA driver has instead of a query ───────────────────

// Adapter is the `nas.SessionSource` the CoA family needs.
//
// It is the adapter itself rather than a type of its own because the data is one
// repository read and the interface is one method; a wrapper would add a name and no
// boundary.
var _ nas.SessionSource = (*Adapter)(nil)

// ActiveSession answers what Foyer's own RADIUS server knows about a MAC.
//
// **This is the evidence a CoA driver has instead of a presence endpoint.** RFC 5176
// is one-way — you tell a NAS to end or change a session and it answers ACK or NAK —
// so there is nothing to ask, and `docs/rules/security.md` §4 still requires
// presence to be confirmed before a grant. What is available instead is the fact that this MAC
// authenticated through this NAS and its session is still open.
//
// The three-way contract is the load-bearing part and it is the same one
// `aaa.PolicyStore` insists on: `(zero, false, nil)` is *no such session*, and any
// non-nil error is *I could not look*. A caller that folded them together would
// answer a database outage with "the guest's device is not on the network" — sending
// somebody to toggle Wi-Fi for twenty minutes over an outage the venue could see.
func (a *Adapter) ActiveSession(ctx context.Context, mac string) (nas.Session, bool, error) {
	row, err := a.st.Accounting().ActiveSessionForMAC(ctx, mac)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nas.Session{}, false, nil
	case err != nil:
		return nas.Session{}, false, err
	}

	s := nas.Session{
		AcctSessionID:    row.AcctSessionID,
		CallingStationID: row.CallingStationID,
		UserName:         row.UserName,
		SiteID:           row.SiteID,
	}
	// `NASIdentifier` and `AuditSessionID` stay empty, and that is a fact about the
	// table rather than an omission here: the stock FreeRADIUS column set carries
	// neither, and it is kept verbatim because it is the project's exit hatch. See
	// `store.RadiusSession` for why filling them in from `radius_clients` would make
	// the driver's own device check compare a value against a copy of itself.
	if addr, err := netip.ParseAddr(row.NASIPAddress); err == nil {
		s.NASIPAddress = addr
	}
	return s, true, nil
}
