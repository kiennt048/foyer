package apistore

import (
	"context"
	"errors"
	"net/netip"
	"strings"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// AccessPointRegistry — the devices Foyer speaks RADIUS to.
//
// The read model is `radius_clients LEFT JOIN access_points`, one row per device,
// and `Nas.RadiusClient.ID` always reaches the `clients.conf` row because rotating
// a secret acts on the client rather than on the device. That is the whole reason
// the store keeps three nouns where the contract keeps one.

// ErrAddressRequired is what registering a NAS with no address answers.
//
// `NasCreate.ipAddress` is required in the contract and `NOT NULL` in
// `radius_clients.address`. If it were optional, an omitted address would reach the
// column and come back as `/problems/internal` — a 500 for what is a client
// mistake. FreeRADIUS matches a packet to a client by its source address and
// nothing else, so a `clients.conf` row with no address is one every Access-Request
// is discarded for. `internal/api`'s `CreateNas` refuses a blank or an unparseable
// one as a field-level `422` before the request reaches this adapter.
//
// This stays as the backstop, because a port that trusts its caller to validate is
// a port that answers a 500 the day a second caller appears. One gap remains: a
// port has no way to *say* which field it refused
// — `internal/api`'s `*apiError` is unexported and `ErrNotFound`/`ErrConflict` are
// the only errors a port can return with a meaning — so if this one ever fires it
// is still an internal error rather than a field refusal.
var ErrAddressRequired = errors.New("apistore: a NAS needs an address to be a clients.conf row")

// AccessPoints lists the device view.
//
// **The `q` field set differs from `internal/api/memory`'s**, and the difference is
// the repository's: `ListNas` matches name, client address and MAC, where the fake
// matches name, address, NAS-Identifier, model and driver. Both are defensible —
// the store's is the set that is indexed — but they are not the same set, and a
// search for a model number finds rows in one and not the other. Reported rather
// than patched over by re-filtering here, which would break paging to fix a
// difference the repository should decide.
func (a *Adapter) AccessPoints(ctx context.Context, siteID, search string, w api.Window) (api.Page[gen.Nas], error) {
	return fill(w,
		func(cursor string, limit int) (store.Page[store.Nas], error) {
			return a.st.RadiusClients().ListNas(ctx, listOptions(siteID, search, cursor, limit))
		},
		func(n store.Nas) (gen.Nas, bool) { return nasOut(n), true },
	)
}

func (a *Adapter) AccessPoint(ctx context.Context, id string) (gen.Nas, error) {
	n, err := a.st.RadiusClients().GetNas(ctx, id)
	if err != nil {
		return gen.Nas{}, out(err)
	}
	return nasOut(*n), nil
}

// RegisterAccessPoint creates the `clients.conf` row and the secret it presents.
//
// Two rows rather than one, because the secret is a record in its own right: it
// belongs to a controller-level RADIUS profile and is presented by every device
// using that profile. A device registered by hand has a profile of one, so it gets
// its own secret — and the label says where the operator has to paste it.
//
// The secret is returned to the caller and never stored in a form anything can read
// back: `shared_secrets.secret` is envelope-encrypted, and there is no port method
// that reveals one. `createNas` and `rotateNasSecret` are the only responses in the
// contract that carry a secret at all.
func (a *Adapter) RegisterAccessPoint(ctx context.Context, in gen.NasCreate) (gen.Nas, string, error) {
	// `NasCreate.ipAddress` became required in the contract, so this is a value
	// rather than a pointer now. `parseClientAddress` keeps its pointer signature
	// because it still has to answer for an empty string.
	prefix, err := parseClientAddress(&in.IpAddress)
	if err != nil {
		return gen.Nas{}, "", err
	}

	secret, err := a.newSecret()
	if err != nil {
		return gen.Nas{}, "", err
	}
	ss := &store.SharedSecret{
		ID:     a.newID("sec-"),
		SiteID: in.SiteId,
		Label:  strings.TrimSpace(in.Name),
	}
	if err := a.st.SharedSecrets().Create(ctx, ss, []byte(secret)); err != nil {
		return gen.Nas{}, "", out(err)
	}

	client := &store.RadiusClient{
		ID:             a.newID("rc-"),
		SiteID:         in.SiteId,
		Name:           in.Name,
		Description:    in.Description,
		Address:        prefix,
		Source:         string(gen.Manual),
		Driver:         string(in.Driver),
		SharedSecretID: ptr(ss.ID),
		NASIdentifier:  in.NasIdentifier,
		CoAPort:        in.CoaPort,
		// **Written only for the driver that reads it.** `coaVendor` says which of
		// two RFC 5176 dialects to speak, so it means nothing on a device that does
		// not speak CoA — and a stored value nothing reads is the shape
		// `docs/rules/security.md` §3 warns about, a column that looks like a
		// switch and configures nothing. `createNas` refuses a `coa` device that
		// names none; this drops one a `unifi` device named.
		CoAVendor: coaVendorIn(in.Driver, in.CoaVendor),
		// Both BlastRADIUS switches on, never FreeRADIUS's `auto`, which exists
		// for legacy compatibility and therefore silently leaves the weakest gear
		// unprotected (CVE-2024-3596).
		RequireMsgAuth:  true,
		LimitProxyState: true,
		// Nothing has been measured about this device yet. Capabilities are
		// measured, not declared: a registration form that asserted them would
		// let the console offer controls the hardware ignores.
		MessageAuthState: string(gen.NasMessageAuthenticatorUnknown),
		Reachability:     string(gen.NasReachabilityUnknown),
		Capabilities:     store.Capabilities{WalledGarden: string(gen.GardenKindNone)},
		Enabled:          true,
	}
	if err := a.st.RadiusClients().Create(ctx, client); err != nil {
		// The secret has no client to belong to. Leaving it would accumulate
		// unreferenced key material on every refused registration.
		_ = a.st.SharedSecrets().Delete(ctx, ss.ID)
		return gen.Nas{}, "", out(err)
	}

	n, err := a.st.RadiusClients().GetNas(ctx, client.ID)
	if err != nil {
		return gen.Nas{}, "", out(err)
	}
	return nasOut(*n), secret, nil
}

// coaVendorIn decides what `radius_clients.coa_vendor` should hold.
//
// It is the only writer of that column, and it writes it for one driver. The
// registration handler is what refuses a `coa` device that names no vendor —
// field-level, so the console can mark the field — and this is deliberately not a
// second copy of that refusal: a nil here on a `coa` row is a row that will be
// refused a driver by name, which is the behaviour the whole field exists for and
// is exercised in `nasdrivers_test.go`. Silently defaulting one would put the guess
// back, one layer down from where it was removed.
func coaVendorIn(driver gen.NasDriverKind, vendor *gen.CoaVendor) *string {
	if driver != gen.NasDriverKindCoa || vendor == nil {
		return nil
	}
	return ptr(string(*vendor))
}

// parseClientAddress accepts a host or a CIDR range.
//
// Both, because Aruba's dynamic-radius-proxy toggle decides which an operator needs
// and Foyer cannot see that toggle. A bare address becomes a single-host prefix,
// which is what `clients.conf` means by one device.
func parseClientAddress(raw *string) (netip.Prefix, error) {
	value := strings.TrimSpace(deref(raw))
	if value == "" {
		return netip.Prefix{}, ErrAddressRequired
	}
	if p, err := netip.ParsePrefix(value); err == nil {
		return p, nil
	}
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Prefix{}, ErrAddressRequired
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// AmendAccessPoint renames or disables a device.
//
// It patches the `clients.conf` row, which is where both fields live: the access
// point's own name is discovery's to write, and an operator renaming a device is
// naming the thing they registered.
func (a *Adapter) AmendAccessPoint(ctx context.Context, id string, name *string, enabled *bool) (gen.Nas, error) {
	n, err := a.st.RadiusClients().GetNas(ctx, id)
	if err != nil {
		return gen.Nas{}, out(err)
	}
	if err := a.st.RadiusClients().Update(ctx, n.RadiusClient.ID, store.RadiusClientPatch{
		Name:    name,
		Enabled: enabled,
	}); err != nil {
		return gen.Nas{}, out(err)
	}
	fresh, err := a.st.RadiusClients().GetNas(ctx, id)
	if err != nil {
		return gen.Nas{}, out(err)
	}
	return nasOut(*fresh), nil
}

// ForgetAccessPoint removes the device.
//
// The `clients.conf` row goes, because that is what stops FreeRADIUS answering an
// address. An access point row behind it keeps its own row with
// `radius_client_id NULL`: the device still exists and simply has no resolved
// address any more, and its MAC is what resolves last week's session rows.
func (a *Adapter) ForgetAccessPoint(ctx context.Context, id string) error {
	n, err := a.st.RadiusClients().GetNas(ctx, id)
	if err != nil {
		return out(err)
	}
	return out(a.st.RadiusClients().Delete(ctx, n.RadiusClient.ID))
}

// RecordProbe writes back only what the probe measured.
//
// `not-measured` leaves the stored value alone. An unreachable device does not lose
// what was last known about it, and a probe that could not reach it must not
// overwrite `enforced` with `unknown` — that would read as *this device stopped
// enforcing Message-Authenticator*, which is a BlastRADIUS alarm nobody should be
// woken by.
func (a *Adapter) RecordProbe(ctx context.Context, id string, p gen.NasProbe) error {
	n, err := a.st.RadiusClients().GetNas(ctx, id)
	if err != nil {
		return out(err)
	}
	measured := store.ClientProbe{
		Reachability: string(p.Reachability),
		Capabilities: capabilitiesIn(p.Capabilities),
		// Unchanged unless the probe measured it. The repository writes this
		// column unconditionally, so the previous value is passed back rather
		// than a fresh `unknown`.
		MessageAuthenticator: n.MessageAuthState,
	}
	if p.MessageAuthenticator != gen.NasProbeMessageAuthenticatorNotMeasured {
		measured.MessageAuthenticator = string(p.MessageAuthenticator)
	}
	if p.Accounting != gen.NasProbeAccountingNotMeasured {
		measured.AccountingConfigured = ptr(p.Accounting == gen.NasProbeAccountingConfigured)
	}
	return out(a.st.RadiusClients().RecordProbe(ctx, n.RadiusClient.ID, measured, a.now()))
}

// StoreRotatedSecret persists a secret the caller generated.
//
// It rotates the shared secret the device's client row references, which is a set
// rather than a row: every `clients.conf` entry on that RADIUS profile now presents
// the new value. That is the truth about how the secret is configured on a
// controller — one field, N devices — and it is the reason `shared_secrets` is its
// own table.
//
// A device with no secret at all gets one created and attached, so that rotation on
// a row that predates its profile does not silently succeed while changing nothing.
func (a *Adapter) StoreRotatedSecret(ctx context.Context, id, secret string) error {
	n, err := a.st.RadiusClients().GetNas(ctx, id)
	if err != nil {
		return out(err)
	}
	if n.SharedSecretID != nil {
		return out(a.st.SharedSecrets().Rotate(ctx, *n.SharedSecretID, []byte(secret)))
	}
	ss := &store.SharedSecret{ID: a.newID("sec-"), SiteID: n.SiteID, Label: n.Name}
	if err := a.st.SharedSecrets().Create(ctx, ss, []byte(secret)); err != nil {
		return out(err)
	}
	return out(a.st.RadiusClients().Update(ctx, n.RadiusClient.ID, store.RadiusClientPatch{
		SharedSecretID: ptr(ptr(ss.ID)),
	}))
}
