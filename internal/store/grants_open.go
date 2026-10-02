package store

import (
	"context"
	"time"
)

// The portal's open authorizations, with what ends one.
//
// A session is a `radacct` row, and on a network the controller authorizes with no
// RADIUS accounting profile there is none — so the console had no row to hang a
// disconnect on while the guest was plainly online. The grant is that row: it
// names the device's MAC, the access point it was confirmed under and the venue,
// and through the access point the controller that authorized it. UniFi's Hotspot
// Manager and Omada's Portal Authorization list are the same idea on the vendor's
// side.

// GrantTarget is a grant and the device that would end it.
type GrantTarget struct {
	PortalGrant
	// ControllerID is the controller that authorized the guest: the access
	// point's, or else the RADIUS client's. Nil when neither is known, which is a
	// guest no controller can unauthorize.
	ControllerID *string
	// DeviceName is that controller's name, or else the RADIUS client's.
	DeviceName *string
	// ClientDriver and ClientName are the RADIUS client the grant's device
	// arrives through, when one is known — what a refusal names.
	ClientDriver *string
	ClientName   *string
}

const grantTargetSelect = `
	SELECT g.id, g.site_id, g.nas_id, g.client_mac::text, g.ap_mac::text, g.method,
	       g.voucher_id, g.account_id, g.plan_name, g.plan_name_lang, g.duration_minutes,
	       g.data_allowance_mb, g.up_kbps, g.down_kbps, g.enforced_by, g.granted_at,
	       g.expires_at, g.revoked_at, g.delivery, g.phone,
	       coalesce(ap.controller_id, rc.controller_id), coalesce(c.name, rc.name),
	       rc.driver, rc.name
	FROM portal_grants g
	LEFT JOIN access_points ap  ON ap.mac = g.ap_mac
	LEFT JOIN radius_clients rc ON rc.id = coalesce(g.nas_id, ap.radius_client_id)
	LEFT JOIN controllers c     ON c.id = coalesce(ap.controller_id, rc.controller_id)`

func scanGrantTarget(row interface{ Scan(...any) error }) (*GrantTarget, error) {
	var t GrantTarget
	g := &t.PortalGrant
	err := row.Scan(&g.ID, &g.SiteID, &g.NASID, &g.ClientMAC, &g.APMAC, &g.Method,
		&g.VoucherID, &g.AccountID, &g.Plan.Name, &g.Plan.NameLang,
		&g.Plan.DurationMinutes, &g.Plan.DataAllowanceMB, &g.Plan.UpKbps, &g.Plan.DownKbps,
		&g.Plan.EnforcedBy, &g.GrantedAt, &g.ExpiresAt, &g.RevokedAt, &g.Delivery, &g.Phone,
		&t.ControllerID, &t.DeviceName, &t.ClientDriver, &t.ClientName)
	if err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

// Open reports whether the grant is neither revoked nor past its expiry at at.
func (g PortalGrant) Open(at time.Time) bool {
	return g.RevokedAt == nil && (g.ExpiresAt == nil || g.ExpiresAt.After(at))
}

// GrantTarget is one grant and its device. ErrNotFound when unknown.
func (r *GrantRepo) GrantTarget(ctx context.Context, id string) (*GrantTarget, error) {
	return scanGrantTarget(r.s.pool.QueryRow(ctx, grantTargetSelect+` WHERE g.id = $1`, id))
}

// OpenGrants lists a site's open grants, newest first, starting after the
// (grantedAt, id) pair of the previous page's last row when one is given.
func (r *GrantRepo) OpenGrants(ctx context.Context, siteID string, at time.Time, afterAt *time.Time, afterID string, limit int) ([]GrantTarget, error) {
	rows, err := r.s.pool.Query(ctx, grantTargetSelect+`
		WHERE g.site_id = $1
		  AND g.revoked_at IS NULL
		  AND (g.expires_at IS NULL OR g.expires_at > $2)
		  AND ($3::timestamptz IS NULL OR (g.granted_at, g.id) < ($3, $4))
		ORDER BY g.granted_at DESC, g.id DESC
		LIMIT $5`, siteID, at, afterAt, afterID, limit)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []GrantTarget
	for rows.Next() {
		t, err := scanGrantTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, mapErr(rows.Err())
}
