package apistore

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
)

// The portal's open authorizations and their controller-side disconnect.
// internal/api/ports_portalgrants.go says why; this is
// `resolveSessionTarget`'s controller branch with a grant in place of a session.

var _ api.PortalGrantStore = (*Adapter)(nil)

// grantTarget is the driver that ends a grant and the request it takes.
type grantTarget struct {
	driver nas.NASDriver
	req    nas.DeauthorizeRequest
}

// resolveGrantTarget builds the controller's driver for a grant.
//
// Only a controller can end a grant: it authorized the guest, and it is the one
// place that authorization can be withdrawn. A guest behind the RFC 5176 family
// is a RADIUS session and is ended from `disconnectSession`, so a grant whose
// device has no controller is refused — with the RADIUS client named when one is
// known — rather than sent a Disconnect-Request keyed on nothing.
func (a *Adapter) resolveGrantTarget(ctx context.Context, g store.GrantTarget) (grantTarget, error) {
	refusal := func() error {
		if g.ClientDriver == nil || g.ClientName == nil {
			return api.ErrDeviceCannotDisconnect
		}
		return &api.DisconnectRefusal{Driver: gen.NasDriverKind(*g.ClientDriver), NasName: *g.ClientName}
	}
	if g.ControllerID == nil {
		return grantTarget{}, refusal()
	}
	c, err := a.st.Controllers().Get(ctx, *g.ControllerID)
	if err != nil {
		return grantTarget{}, out(err)
	}
	vendorSite := vendorSiteFor(*c, g.SiteID)
	d, _, err := a.driverFor(ctx, *g.ControllerID, vendorSite)
	switch {
	case errors.Is(err, ErrNoDriver):
		// Omada and Ruckus controllers: registered, and not driven by Foyer.
		return grantTarget{}, &api.DisconnectRefusal{Driver: gen.NasDriverKind(c.Vendor), NasName: c.Name}
	case err != nil:
		return grantTarget{}, err
	}
	if !d.Capabilities().CanDeauthGuest {
		return grantTarget{}, &api.DisconnectRefusal{Driver: gen.NasDriverKind(c.Vendor), NasName: c.Name}
	}
	req := nas.DeauthorizeRequest{ClientMAC: g.ClientMAC, SiteID: vendorSite, Reason: "revoked"}
	if g.APMAC != nil {
		req.APMAC = *g.APMAC
	}
	return grantTarget{driver: d, req: req}, nil
}

func (a *Adapter) authorizationOut(g store.GrantTarget, canDisconnect bool) gen.PortalAuthorization {
	p := gen.PortalAuthorization{
		Id: g.ID, SiteId: g.SiteID, ClientMac: g.ClientMAC, ApMac: g.APMAC,
		Method: gen.PortalMethod(g.Method), PlanName: g.Plan.Name,
		GrantedAt: g.GrantedAt, ExpiresAt: g.ExpiresAt, VoucherId: g.VoucherID,
		DeviceName: g.DeviceName, CanDisconnect: canDisconnect,
	}
	return p
}

// grantCursor is "<granted_at unix nanos>/<id>", inside the adapter's cursor.
func grantCursor(g store.GrantTarget) string {
	return strconv.FormatInt(g.GrantedAt.UnixNano(), 10) + "/" + g.ID
}

func parseGrantCursor(c string) (*time.Time, string) {
	ns, id, ok := strings.Cut(c, "/")
	if !ok {
		return nil, ""
	}
	n, err := strconv.ParseInt(ns, 10, 64)
	if err != nil {
		return nil, ""
	}
	at := time.Unix(0, n).UTC()
	return &at, id
}

func (a *Adapter) OpenGrants(ctx context.Context, siteID string, w api.Window) (api.Page[gen.PortalAuthorization], error) {
	afterAt, afterID := parseGrantCursor(decodeCursor(w.Cursor).C)
	limit := w.Limit
	if limit <= 0 {
		limit = api.DefaultPageLimit
	}
	rows, err := a.st.Grants().OpenGrants(ctx, siteID, a.now(), afterAt, afterID, limit+1)
	if err != nil {
		return api.Page[gen.PortalAuthorization]{}, out(err)
	}
	var page api.Page[gen.PortalAuthorization]
	if len(rows) > limit {
		rows = rows[:limit]
		page.NextCursor = encodeCursor(pageCursor{C: grantCursor(rows[len(rows)-1])})
	}
	// One driver per controller for the whole page: building one unseals the
	// controller's credential, and a page of fifty guests on one controller is
	// one controller.
	can := map[string]bool{}
	for _, g := range rows {
		key := ""
		if g.ControllerID != nil {
			key = *g.ControllerID
		}
		ok, seen := can[key]
		if !seen {
			_, err := a.resolveGrantTarget(ctx, g)
			ok = err == nil
			can[key] = ok
		}
		page.Items = append(page.Items, a.authorizationOut(g, ok))
	}
	return page, nil
}

func (a *Adapter) Grant(ctx context.Context, id string) (gen.PortalAuthorization, bool, error) {
	g, err := a.st.Grants().GrantTarget(ctx, id)
	if err != nil {
		return gen.PortalAuthorization{}, false, out(err)
	}
	_, terr := a.resolveGrantTarget(ctx, *g)
	return a.authorizationOut(*g, terr == nil), g.Open(a.now()), nil
}

func (a *Adapter) CanEndGrant(ctx context.Context, id string) error {
	g, err := a.st.Grants().GrantTarget(ctx, id)
	if err != nil {
		return out(err)
	}
	_, err = a.resolveGrantTarget(ctx, *g)
	return err
}

func (a *Adapter) RevokeGrant(ctx context.Context, id string) error {
	return out(a.st.Grants().RevokeGrant(ctx, id, a.now()))
}

// EndGrant asks the controller to unauthorize the grant's device. The error is
// classified exactly as EndSession's is, so the console reads one set of causes.
func (a *Adapter) EndGrant(ctx context.Context, id string) error {
	g, err := a.st.Grants().GrantTarget(ctx, id)
	if err != nil {
		return out(err)
	}
	t, err := a.resolveGrantTarget(ctx, *g)
	if err != nil {
		return err
	}
	name := ""
	if g.DeviceName != nil {
		name = *g.DeviceName
	}
	return sessionEndError(store.Nas{Name: name}, t.driver.DeauthorizeGuest(ctx, t.req))
}
