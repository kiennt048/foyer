package api

import (
	"context"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// PortalGrantStore is the portal's open authorizations and how one is ended on the
// controller that authorized it.
//
// A session is a `radacct` row. On a network the controller authorizes with no
// RADIUS accounting profile there is none, so `disconnectSession` had nothing to
// act on while the guest was plainly online. UniFi's Hotspot Manager and Omada's
// Portal Authorization list put *unauthorize* on the authorization record; this is
// that record.
type PortalGrantStore interface {
	// OpenGrants lists the site's grants that are neither revoked nor expired,
	// newest first, with canDisconnect read from the device's driver.
	OpenGrants(ctx context.Context, siteID string, w Window) (Page[gen.PortalAuthorization], error)
	// Grant is one grant, and whether it is still open. ErrNotFound when unknown.
	Grant(ctx context.Context, id string) (grant gen.PortalAuthorization, open bool, err error)
	// CanEndGrant answers from the driver before anything is changed: a
	// *DisconnectRefusal when the grant's device is not driven through a
	// controller that can unauthorize it.
	CanEndGrant(ctx context.Context, id string) error
	// RevokeGrant withdraws the grant. ErrNotFound when it was already revoked.
	RevokeGrant(ctx context.Context, id string) error
	// EndGrant asks the controller to unauthorize the grant's device, answering a
	// *SessionEndError the way EndSession does.
	EndGrant(ctx context.Context, id string) error
}
