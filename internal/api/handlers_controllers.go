package api

import (
	"context"
	"net/url"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// Controllers — the management plane an operator actually possesses.
//
// It is a separate noun from `Nas` because Ubiquiti's own external-portal API is
// entirely controller- and site-scoped, and the access point appears in it only as a MAC
// in a query string. Controller, RADIUS client and access point are three
// nouns, not one.
//
// The credential never comes back out. `Controller.credentialSet` is a boolean
// for the same reason `Nas.secretSet` is: it is a password to somebody's
// network, and an operation that returns one is an operation that leaks one.

func (s *Server) ListControllers(ctx context.Context, request gen.ListControllersRequestObject) (gen.ListControllersResponseObject, error) {
	if s.ports.Controllers == nil {
		return nil, errNotImplemented
	}
	list, err := s.ports.Controllers.ControllersInSite(ctx, string(request.Params.SiteId))
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []Controller{}
	}
	return gen.ListControllers200JSONResponse(list), nil
}

func (s *Server) GetController(ctx context.Context, request gen.GetControllerRequestObject) (gen.GetControllerResponseObject, error) {
	if s.ports.Controllers == nil {
		return nil, errNotImplemented
	}
	c, err := s.ports.Controllers.Controller(ctx, request.ControllerId)
	if err != nil {
		return nil, err
	}
	return gen.GetController200JSONResponse(c), nil
}

func (s *Server) CreateController(ctx context.Context, request gen.CreateControllerRequestObject) (gen.CreateControllerResponseObject, error) {
	if s.ports.Controllers == nil {
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
	if fault, ok := badOrigin(in.BaseUrl); ok {
		errs["baseUrl"] = "Not an address Foyer can reach."
		faults["baseUrl"] = fault
	}
	// The credential the auth mode actually needs. An `api-key` controller with
	// a username and no key is a registration that will fail on the first probe
	// with "the key is wrong", which is the failure this refusal exists to
	// pre-empt.
	switch in.AuthMode {
	case gen.ApiKey:
		if in.ApiKey == nil || *in.ApiKey == "" {
			errs["apiKey"] = "Required."
			faults["apiKey"] = gen.FieldFaultRequired
		}
	case gen.LocalAdmin, gen.HotspotOperator:
		if in.Username == nil || *in.Username == "" {
			errs["username"] = "Required."
			faults["username"] = gen.FieldFaultRequired
		}
		if in.Password == nil || *in.Password == "" {
			errs["password"] = "Required."
			faults["password"] = gen.FieldFaultRequired
		}
	}
	if len(faults) > 0 {
		return nil, validation(errs, faults)
	}

	who := callerFrom(ctx)
	c, err := s.ports.Controllers.RegisterController(ctx, in, "")
	if err != nil {
		return nil, err
	}
	s.record(ctx, who, gen.AuditKindControllerRegistered, gen.AuditSubject{
		Kind: gen.AuditSubjectKindController, Id: c.Id, Name: ptr(c.Name),
	}, nil)
	return gen.CreateController201JSONResponse(c), nil
}

func (s *Server) UpdateController(ctx context.Context, request gen.UpdateControllerRequestObject) (gen.UpdateControllerResponseObject, error) {
	if s.ports.Controllers == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	if request.Body.BaseUrl != nil {
		if fault, ok := badOrigin(*request.Body.BaseUrl); ok {
			return nil, validation(
				map[string]string{"baseUrl": "Not an address Foyer can reach."},
				map[string]gen.FieldFault{"baseUrl": fault},
			)
		}
	}
	c, err := s.ports.Controllers.AmendController(ctx, request.ControllerId, *request.Body)
	if err != nil {
		return nil, err
	}
	// The `controller-updated` entry is written because this operation can
	// replace the stored credential, the base URL or the TLS mode, and a
	// controller whose API key was swapped is a controller somebody else may now
	// be driving.
	//
	// `changed` names which members moved, and it is read from the request rather
	// than from a diff of the stored object: an entry that records that
	// *something* changed is the shape of a log nobody trusts, and the console's
	// copy for `operator-updated` already renders exactly this list. **No value
	// travels** — `apiKey` and `password` are write-only in this contract and a
	// log is the last place either should reappear.
	s.record(ctx, callerFrom(ctx), gen.AuditKindControllerUpdated, gen.AuditSubject{
		Kind: gen.AuditSubjectKindController, Id: c.Id, Name: ptr(c.Name),
	}, map[string]any{
		"controllerName": c.Name,
		"changed":        controllerFieldsChanged(*request.Body),
	})
	return gen.UpdateController200JSONResponse(c), nil
}

func (s *Server) DeleteController(ctx context.Context, request gen.DeleteControllerRequestObject) (gen.DeleteControllerResponseObject, error) {
	if s.ports.Controllers == nil {
		return nil, errNotImplemented
	}
	c, err := s.ports.Controllers.Controller(ctx, request.ControllerId)
	if err != nil {
		return nil, err
	}
	// Refused while access points discovered from it are still registered.
	// Deleting it would leave devices Foyer speaks RADIUS to with no way to
	// authorize a guest on them and no record of where they came from — present
	// in `clients.conf`, invisible in the console, impossible to diagnose.
	adopted, err := s.ports.Controllers.AccessPointsFrom(ctx, c.Id)
	if err != nil {
		return nil, err
	}
	if adopted > 0 {
		return nil, controllerInUse(adopted)
	}
	if err := s.ports.Controllers.ForgetController(ctx, c.Id); err != nil {
		return nil, err
	}
	s.record(ctx, callerFrom(ctx), gen.AuditKindControllerDeleted, gen.AuditSubject{
		Kind: gen.AuditSubjectKindController, Id: c.Id, Name: ptr(c.Name),
	}, nil)
	return gen.DeleteController204Response{}, nil
}

func (s *Server) ProbeController(ctx context.Context, request gen.ProbeControllerRequestObject) (gen.ProbeControllerResponseObject, error) {
	if s.ports.Controllers == nil || s.ports.Devices == nil {
		return nil, errNotImplemented
	}
	c, err := s.ports.Controllers.Controller(ctx, request.ControllerId)
	if err != nil {
		return nil, err
	}
	probe, err := s.ports.Devices.ProbeController(ctx, c)
	if err != nil {
		return nil, err
	}
	return gen.ProbeController200JSONResponse(probe), nil
}

func (s *Server) DiscoverFromController(ctx context.Context, request gen.DiscoverFromControllerRequestObject) (gen.DiscoverFromControllerResponseObject, error) {
	if s.ports.Controllers == nil || s.ports.Devices == nil {
		return nil, errNotImplemented
	}
	c, err := s.ports.Controllers.Controller(ctx, request.ControllerId)
	if err != nil {
		return nil, err
	}
	// A controller that has not been probed, or is not reachable, cannot be
	// read. Discovering from one would produce an empty result that looks
	// exactly like "this controller has no access points" — and discovery is
	// reconciling, so an empty read is what marks every device missing.
	if c.Reachability != gen.NasReachabilityOk {
		e := controllerUnreachable()
		e.Params = map[string]any{"name": c.Name, "reachability": string(c.Reachability)}
		return nil, e
	}

	var siteID, vendorSiteID string
	if request.Body != nil {
		if request.Body.SiteId != nil {
			siteID = *request.Body.SiteId
		}
		if request.Body.VendorSiteId != nil {
			vendorSiteID = *request.Body.VendorSiteId
		}
	}
	result, err := s.ports.Devices.Discover(ctx, c, siteID, vendorSiteID)
	if err != nil {
		return nil, err
	}
	// **`AuditKind` `controller-discovery-run`.**
	//
	// One entry for one run, whose subject is the controller. `nas-registered`
	// is the only near-enough other value and it is wrong twice over — it would claim
	// one entry for what may be nine adoptions, and it would name a device as the
	// subject of a change made to a controller. The counts are what make the row
	// worth reading: *adopted nine* and *adopted none* are different afternoons.
	//
	// **The one controller kind that carries a venue.** Registering, amending and
	// deleting a controller are the deployment's — migration `00011` made the
	// vendor-site binding per controller, so one of them can feed several venues
	// and naming one on those rows would be inventing a fact. A discovery run is
	// the opposite: the request says which venue it adopts into, and that is the
	// venue whose access points changed underneath the operator.
	s.recordIn(ctx, callerFrom(ctx), siteID, gen.AuditKindControllerDiscoveryRun, gen.AuditSubject{
		Kind: gen.AuditSubjectKindController, Id: c.Id, Name: ptr(c.Name),
	}, map[string]any{
		"controllerName": c.Name,
		"adopted":        result.Adopted,
		"updated":        result.Updated,
		"missing":        result.Missing,
	})
	// Adopted access points are RADIUS clients; they reach FreeRADIUS by the sync.
	s.syncAfterClientWrite(ctx, gen.RadiusSyncTriggerControllerDiscovery)
	return gen.DiscoverFromController200JSONResponse(result), nil
}

// badOrigin rejects anything that is not an http(s) origin.
//
// The scheme allowlist is the same rule §4 states for the portal's return URL,
// applied at the other end: this value becomes the target of requests Foyer
// makes from inside the operator's network, so a `file://` or a userinfo-bearing
// URL here is a request Foyer would make on somebody's behalf.
func badOrigin(raw string) (gen.FieldFault, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return gen.FieldFaultRequired, true
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return gen.FieldFaultNotAnOrigin, true
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return gen.FieldFaultNotAnOrigin, true
	}
	if u.User != nil {
		return gen.FieldFaultNotAnOrigin, true
	}
	return "", false
}

// controllerFieldsChanged names the members an amendment carried, in a stable
// order, and never their values.
//
// Stable because the audit log is read by comparing rows: a list whose order
// depends on Go's map iteration makes two identical changes look different. And
// values are excluded on purpose — `apiKey` and `password` are write-only in this
// contract, so naming the field is the whole of what may be recorded.
func controllerFieldsChanged(in gen.ControllerUpdate) []string {
	var changed []string
	if in.Name != nil {
		changed = append(changed, "name")
	}
	if in.BaseUrl != nil {
		changed = append(changed, "baseUrl")
	}
	if in.ControllerId != nil {
		changed = append(changed, "controllerId")
	}
	if in.TlsMode != nil {
		changed = append(changed, "tlsMode")
	}
	if in.ApiKey != nil {
		changed = append(changed, "apiKey")
	}
	if in.Password != nil {
		changed = append(changed, "password")
	}
	if changed == nil {
		return []string{}
	}
	return changed
}
