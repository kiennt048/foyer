package api

import (
	"context"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The dashboard, the site switcher and the alarm list.
//
// Every figure here travels with its own freshness, and that is not decoration:
// `Overview.clientsOnline` is derived from `radacct`, whose byte and session
// counters advance only on Interim-Update or Stop, with a widely-implemented
// 300-second floor — and on the controller-API path there may be no accounting
// at all. A bare number on this screen is the console asserting something the
// network never told it. `Metric.value` is nullable for exactly that reason: a
// figure that could not be sampled is absent, never zero.

func (s *Server) ListSites(ctx context.Context, _ gen.ListSitesRequestObject) (gen.ListSitesResponseObject, error) {
	if s.ports.Sites == nil {
		return nil, errNotImplemented
	}
	sites, err := s.ports.Sites.Sites(ctx)
	if err != nil {
		return nil, err
	}
	if sites == nil {
		sites = []Site{}
	}
	return gen.ListSites200JSONResponse(sites), nil
}

// CreateSite is the operation without which a fresh deployment goes no further.
//
// `createNas` requires a `siteId` and refuses `422` without one, a migrated
// database holds zero sites, and nothing else in the product puts the first row
// in — so without this a fresh install stops at step one. It is a contract
// operation rather than a seeded migration row because the name is the
// **venue's** name, which is the one fact about a deployment only the operator
// has.
//
// **One venue per install**, and the second call is refused
// `409 /problems/one-site-per-install` in `internal/apistore`.
//
// The reasoning ends at the protocol rather than at product scope. `nas.nasname`
// is globally unique because FreeRADIUS's client list is one flat namespace with
// no tenant in it, so two venues on private-range networks both using
// `192.168.1.1` can never both be projected. A second customer is a second
// installation. The refusal is this **operation's**, not the schema's: `sites`
// still permits many rows and the tenant seam stays as insurance.
//
// **Audited, with a kind of its own.** `AuditKind` is a closed enum, and
// invariant 10 says an action with no member is left unrecorded rather than filed
// under a near-enough one — `nas-registered` would name a device as the subject
// of a change that made a venue. This is the operation every installation
// performs first, so without an entry the log that records every later change to
// a venue would say nothing about where the venue came from. `site-created` is in
// the contract for that reason, and a kind nothing emits is the same defect one
// step along — hence the call below and `TestCreateSiteIsRecorded`.
func (s *Server) CreateSite(ctx context.Context, request gen.CreateSiteRequestObject) (gen.CreateSiteResponseObject, error) {
	if s.ports.Sites == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body

	// The venue's name, and nothing stands in for it. A blank one is refused here
	// rather than stored, because `sites.name` is `text NOT NULL` and "" satisfies
	// that — the database would accept an unnamed venue and the site switcher
	// would render a gap.
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return nil, validation(
			map[string]string{"name": "Required."},
			map[string]gen.FieldFault{"name": gen.FieldFaultRequired},
		)
	}
	site, err := s.ports.Sites.CreateSite(ctx, in)
	if err != nil {
		return nil, err
	}
	// No params: the venue's name is in `subject.name`, where every kind's subject
	// name is, and `SiteCreate` carries nothing else. A `params.name` beside it
	// would be a second copy of the one member this schema is denormalised for.
	//
	// **`siteId` is the venue this created**, so the venue's own log begins with
	// where the venue came from. Which kinds carry a site is decided per kind, for
	// every kind at once, and `auditScopes` is where those decisions live.
	s.recordIn(ctx, callerFrom(ctx), site.Id, gen.AuditKindSiteCreated, gen.AuditSubject{
		Kind: gen.AuditSubjectKindSite, Id: site.Id, Name: ptr(site.Name),
	}, nil)
	return gen.CreateSite201JSONResponse(site), nil
}

func (s *Server) GetOverview(ctx context.Context, request gen.GetOverviewRequestObject) (gen.GetOverviewResponseObject, error) {
	if s.ports.Dashboard == nil {
		return nil, errNotImplemented
	}
	o, err := s.ports.Dashboard.Overview(ctx, string(request.Params.SiteId))
	if err != nil {
		return nil, err
	}
	return gen.GetOverview200JSONResponse(o), nil
}

func (s *Server) ListAlarms(ctx context.Context, request gen.ListAlarmsRequestObject) (gen.ListAlarmsResponseObject, error) {
	if s.ports.Dashboard == nil {
		return nil, errNotImplemented
	}
	alarms, err := s.ports.Dashboard.Alarms(ctx, string(request.Params.SiteId))
	if err != nil {
		return nil, err
	}
	if alarms == nil {
		alarms = []Alarm{}
	}
	return gen.ListAlarms200JSONResponse(alarms), nil
}
