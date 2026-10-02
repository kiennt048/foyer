package api

import (
	"context"
	"net/netip"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// Access points — the devices Foyer speaks RADIUS to.
//
// Two invariants in this file are the ones that matter, and both are about a
// secret rather than about CRUD.
//
//  1. **The shared secret is returned exactly once and never again.** It appears
//     in `CreateNas201JSONResponse` and in `RotateNasSecret200JSONResponse` and
//     in no other response in the contract. There is deliberately no port method
//     that reads one back, so a later screen cannot be made easier by adding one.
//     There is also no default: FreeRADIUS ships an example secret in its sample
//     configuration and shipped example credentials are how appliance software
//     gets mass-owned.
//  2. **Rotation is refused only where there is no route back** — the driver
//     cannot push *and* the device is unreachable. Neither condition refuses on
//     its own, and that is the narrow reading `docs/rules/nas-drivers.md`
//     records: `RotateSecret == false` makes
//     rotation *manual*, not impossible, and gating **Generate new secret** on it
//     would leave that device unable to rotate a secret at all.

func (s *Server) ListNas(ctx context.Context, request gen.ListNasRequestObject) (gen.ListNasResponseObject, error) {
	if s.ports.AccessPoints == nil {
		return nil, errNotImplemented
	}
	p := request.Params
	page, err := s.ports.AccessPoints.AccessPoints(ctx, string(p.SiteId), text((*string)(p.Q)), windowFrom(p.Cursor, p.Limit))
	if err != nil {
		return nil, err
	}
	items := page.Items
	if items == nil {
		items = []Nas{}
	}
	return gen.ListNas200JSONResponse{Items: items, NextCursor: page.NextCursor}, nil
}

func (s *Server) GetNas(ctx context.Context, request gen.GetNasRequestObject) (gen.GetNasResponseObject, error) {
	if s.ports.AccessPoints == nil {
		return nil, errNotImplemented
	}
	n, err := s.ports.AccessPoints.AccessPoint(ctx, string(request.NasId))
	if err != nil {
		return nil, err
	}
	return gen.GetNas200JSONResponse(n), nil
}

func (s *Server) CreateNas(ctx context.Context, request gen.CreateNasRequestObject) (gen.CreateNasResponseObject, error) {
	if s.ports.AccessPoints == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body

	errs := map[string]string{}
	faults := map[string]gen.FieldFault{}
	if strings.TrimSpace(in.Name) == "" {
		errs["name"] = "Required."
		faults["name"] = gen.FieldFaultRequired
	}
	if strings.TrimSpace(in.SiteId) == "" {
		errs["siteId"] = "Required."
		faults["siteId"] = gen.FieldFaultRequired
	}
	// **The address, refused here rather than by PostgreSQL.**
	//
	// This registers a `clients.conf` row, and FreeRADIUS matches an incoming
	// packet to a client by its source address and nothing else — so a row with
	// no address is a client every Access-Request is discarded for, and
	// `radius_clients.address` is `inet NOT NULL`. Were `ipAddress` optional, an
	// omitted one would reach the column and come back as `/problems/internal`, a
	// 500 for a field the operator simply did not fill in. It is required in
	// `NasCreate`, and both refusals below are field-level so the console can mark
	// the field rather than the form.
	switch addr := strings.TrimSpace(in.IpAddress); {
	case addr == "":
		errs["ipAddress"] = "Required."
		faults["ipAddress"] = gen.FieldFaultRequired
	case !isClientAddress(addr):
		errs["ipAddress"] = "Not an address or a range."
		faults["ipAddress"] = gen.FieldFaultNotAnIpAddress
	}
	// **Which member of the CoA family this is**, refused here because guessing it
	// is the one driver mistake nothing downstream can see.
	//
	// `NasDriverKind` has one value, `coa`, for Cisco and Aruba, and they identify
	// a session differently — Cisco IOS-XE by a `Cisco-AVPair audit-session-id`
	// VSA, Aruba by `Calling-Station-Id` in its own spelling. RFC 5176 §2.3 makes
	// the NAS match on every session-identification attribute in the request, so
	// the wrong one matches no session; a careful device answers `Error-Cause 503`
	// and a careless one **ignores the request and ACKs it**. `docs/rules/
	// testing.md` says that case is exactly what a lab cannot produce, so the
	// registration is where it has to be caught.
	//
	// It cannot be `required` in the schema, because it is required for one of the
	// five drivers. `DRIVER_SHAPES` in `web/contract/src/index.ts` is the one table
	// that says which, and the console's form reads the same one.
	if needsCoAVendor(in.Driver) && in.CoaVendor == nil {
		errs["coaVendor"] = "Required."
		faults["coaVendor"] = gen.FieldFaultRequired
	}
	if len(faults) > 0 {
		return nil, validation(errs, faults)
	}

	// **Access points are discovered, never typed**, for the drivers that have a
	// controller. The redirect a guest arrives with identifies the access point
	// by MAC and nothing else, so Foyer has to resolve a MAC to a device — and
	// only the controller can do that. Registering a UniFi or Omada access point
	// by hand produces a row nothing can ever authorize a guest on.
	if controllerOnly(in.Driver) {
		e := needsController()
		e.Errors = map[string]string{"driver": "Register the controller instead."}
		e.Faults = map[string]gen.FieldFault{"driver": gen.FieldFaultNeedsController}
		e.Params = map[string]any{"driver": string(in.Driver)}
		return nil, e
	}

	n, secret, err := s.ports.AccessPoints.RegisterAccessPoint(ctx, in)
	if err != nil {
		return nil, err
	}
	s.recordIn(ctx, callerFrom(ctx), n.SiteId, gen.AuditKindNasRegistered, gen.AuditSubject{
		Kind: gen.AuditSubjectKindNas, Id: n.Id, Name: ptr(n.Name),
	}, nil)
	// The client table FreeRADIUS reads, written now rather than on the next
	// `foyerctl sync-nas` nobody runs. See handlers_network.go.
	s.syncAfterClientWrite(ctx, gen.RadiusSyncTriggerNasCreated)
	return gen.CreateNas201JSONResponse{Nas: n, SharedSecret: secret}, nil
}

func (s *Server) UpdateNas(ctx context.Context, request gen.UpdateNasRequestObject) (gen.UpdateNasResponseObject, error) {
	if s.ports.AccessPoints == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	n, err := s.ports.AccessPoints.AmendAccessPoint(ctx, string(request.NasId), request.Body.Name, request.Body.Enabled)
	if err != nil {
		return nil, err
	}
	// Disabling a device removes it from the client table; a rename changes its
	// shortname. Both reach FreeRADIUS only through a sync.
	s.syncAfterClientWrite(ctx, gen.RadiusSyncTriggerNasUpdated)
	return gen.UpdateNas200JSONResponse(n), nil
}

func (s *Server) DeleteNas(ctx context.Context, request gen.DeleteNasRequestObject) (gen.DeleteNasResponseObject, error) {
	if s.ports.AccessPoints == nil {
		return nil, errNotImplemented
	}
	n, err := s.ports.AccessPoints.AccessPoint(ctx, string(request.NasId))
	if err != nil {
		return nil, err
	}
	if err := s.ports.AccessPoints.ForgetAccessPoint(ctx, n.Id); err != nil {
		return nil, err
	}
	// Read from the device before it was forgotten, which is the only moment its
	// venue is knowable: the row is gone and the entry still has to say where the
	// access point stood.
	s.recordIn(ctx, callerFrom(ctx), n.SiteId, gen.AuditKindNasDeleted, gen.AuditSubject{
		Kind: gen.AuditSubjectKindNas, Id: n.Id, Name: ptr(n.Name),
	}, nil)
	// A removed client must leave the table FreeRADIUS reads: a decommissioned
	// device holding a valid RADIUS client is a security defect (store.NasSync).
	s.syncAfterClientWrite(ctx, gen.RadiusSyncTriggerNasDeleted)
	return gen.DeleteNas204Response{}, nil
}

// isClientAddress accepts what `radius_clients.address` accepts: one address or
// one range, v4 or v6.
//
// Both, because Aruba's dynamic-radius-proxy toggle decides which of the two an
// operator needs and Foyer cannot see that toggle. The column is `inet` rather
// than `cidr` for the same reason — `cidr` rejects an address with host bits set,
// and a fleet range written the way an operator writes it usually has them.
//
// It parses rather than pattern-matching, and it is deliberately the *same* pair
// of parses the adapter performs before writing the row: a validator that admits
// something the writer then refuses has moved the 500 rather than removed it.
func isClientAddress(value string) bool {
	if _, err := netip.ParsePrefix(value); err == nil {
		return true
	}
	_, err := netip.ParseAddr(value)
	return err == nil
}

// ProbeNas measures capabilities rather than declaring them.
//
// The response carries everything the probe measured, so a client never has to
// re-read the device to render an honest answer — were `messageAuthenticator`
// and `accountingConfigured` only on `Nas`, `probeNas` followed by `getNas` would
// be the only complete rendering and a client that forgot the second call would
// show a device it had half measured.
//
// The probe also writes back what it measured, so a subsequent `getNas` agrees
// with it. Where it measured nothing the record is left exactly as it was: an
// unreachable device does not lose what was last known about it, and the result
// says `not-measured` rather than repeating a stale value as a fresh one.
func (s *Server) ProbeNas(ctx context.Context, request gen.ProbeNasRequestObject) (gen.ProbeNasResponseObject, error) {
	if s.ports.AccessPoints == nil || s.ports.Devices == nil {
		return nil, errNotImplemented
	}
	n, err := s.ports.AccessPoints.AccessPoint(ctx, string(request.NasId))
	if err != nil {
		return nil, err
	}
	probe, err := s.ports.Devices.ProbeAccessPoint(ctx, n)
	if err != nil {
		return nil, err
	}
	if err := s.ports.AccessPoints.RecordProbe(ctx, n.Id, probe); err != nil {
		return nil, err
	}
	return gen.ProbeNas200JSONResponse(probe), nil
}

// GetNasEvidence — how this device's claims are known, and it is two lists
// because they disagree.
//
// Every wire encoding a shipped CoA profile emits is `documented`; every
// capability of that same driver is `unverified`. Merging them would answer *is
// this driver trustworthy*, which is not a question either list asks. The
// sentence an operator needs for a Cisco row is **we know what we will send and
// we do not know what it will do**, and that needs both halves.
func (s *Server) GetNasEvidence(ctx context.Context, request gen.GetNasEvidenceRequestObject) (gen.GetNasEvidenceResponseObject, error) {
	if s.ports.AccessPoints == nil || s.ports.Devices == nil {
		return nil, errNotImplemented
	}
	n, err := s.ports.AccessPoints.AccessPoint(ctx, string(request.NasId))
	if err != nil {
		return nil, err
	}
	ev, err := s.ports.Devices.DriverEvidence(ctx, n)
	if err != nil {
		return nil, err
	}
	return gen.GetNasEvidence200JSONResponse(ev), nil
}

func (s *Server) RotateNasSecret(ctx context.Context, request gen.RotateNasSecretRequestObject) (gen.RotateNasSecretResponseObject, error) {
	if s.ports.AccessPoints == nil || s.ports.Devices == nil {
		return nil, errNotImplemented
	}
	n, err := s.ports.AccessPoints.AccessPoint(ctx, string(request.NasId))
	if err != nil {
		return nil, err
	}

	canPush, class, err := s.ports.Devices.SecretRotation(ctx, n)
	if err != nil {
		return nil, err
	}
	// The one combination with no route back: the new value could only be
	// entered by hand, on a device nothing can reach, so rotating would take it
	// off the air until somebody stands in front of it. Neither condition
	// refuses on its own.
	if !canPush && n.Reachability != gen.NasReachabilityOk {
		e := rotationRefused()
		e.Params = map[string]any{"nasName": n.Name, "nasReachability": string(n.Reachability)}
		return nil, e
	}

	secret, err := s.secret()
	if err != nil {
		return nil, internalProblem()
	}
	if err := s.ports.AccessPoints.StoreRotatedSecret(ctx, n.Id, secret); err != nil {
		return nil, err
	}

	// The class comes from the driver, not from here. A `restart` drops every
	// in-flight EAP conversation and the in-memory TLS session cache, so every
	// corporate laptop on site re-authenticates — which is what the console
	// warns the operator about before they confirm, and is not a figure this
	// package is in any position to invent.
	if canPush {
		if err := s.ports.Devices.PushSecret(ctx, n, secret); err != nil {
			return nil, err
		}
	}

	s.recordIn(ctx, callerFrom(ctx), n.SiteId, gen.AuditKindNasSecretRotated, gen.AuditSubject{
		Kind: gen.AuditSubjectKindNas, Id: n.Id, Name: ptr(n.Name),
	}, map[string]any{"changeClass": string(class), "pushed": canPush})
	// `nas.secret` is the one cleartext copy FreeRADIUS checks against; until the
	// sync it still holds the old secret.
	s.syncAfterClientWrite(ctx, gen.RadiusSyncTriggerNasSecretRotated)

	return gen.RotateNasSecret200JSONResponse{SharedSecret: secret, ChangeClass: class}, nil
}

// needsCoAVendor reports whether this driver is the one whose enum member names a
// family rather than a vendor.
//
// One driver, and it is a closed list rather than a `!= mikrotik` because the
// question is *which member of the CoA family is this*, and MikroTik answering "not
// applicable" is a different fact from Cisco answering "Cisco". A sixth driver added
// tomorrow gets the answer its own vendor decides, not whichever branch it falls
// into here.
func needsCoAVendor(d gen.NasDriverKind) bool {
	return d == gen.NasDriverKindCoa
}

// controllerOnly reports whether this driver's devices can only be adopted from
// a controller.
func controllerOnly(d gen.NasDriverKind) bool {
	switch d {
	case gen.NasDriverKindUnifi, gen.NasDriverKindOmada, gen.NasDriverKindRuckus:
		return true
	default:
		// `coa` and `mikrotik` speak RFC 5176 directly and have no controller to
		// be discovered from, so a hand-registered row is the only way they can
		// exist.
		return false
	}
}
