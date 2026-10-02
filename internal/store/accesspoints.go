package store

import (
	"context"
	"net/netip"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// AccessPointRepo owns `access_points`.
//
// An access point is discovered, never typed. The twelfth one costs nothing, and
// the MAC in the NAS redirect finally resolves to something -- which is the whole
// reason this is a separate noun.
type AccessPointRepo struct{ s *Store }

const accessPointColumns = `
	id, site_id, controller_id, mac::text, name, model, firmware, host(ip_address),
	radius_client_id, radius_via, state, missing_since, first_seen_at, last_seen_at,
	created_at, updated_at`

func scanAccessPoint(row interface{ Scan(...any) error }) (*AccessPoint, error) {
	var ap AccessPoint
	err := row.Scan(&ap.ID, &ap.SiteID, &ap.ControllerID, &ap.MAC, &ap.Name, &ap.Model,
		&ap.Firmware, &ap.IPAddress, &ap.RadiusClientID, &ap.RadiusVia, &ap.State,
		&ap.MissingSince, &ap.FirstSeenAt, &ap.LastSeenAt, &ap.CreatedAt, &ap.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &ap, nil
}

// ListForSite returns the access points of one site, or of every site when siteID
// is empty.
func (r *AccessPointRepo) ListForSite(ctx context.Context, siteID string) ([]AccessPoint, error) {
	rows, err := r.s.pool.Query(ctx,
		`SELECT `+accessPointColumns+` FROM access_points WHERE ($1 = '' OR site_id = $1) ORDER BY name`, siteID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []AccessPoint
	for rows.Next() {
		ap, err := scanAccessPoint(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ap)
	}
	return out, mapErr(rows.Err())
}

// Get returns one access point.
func (r *AccessPointRepo) Get(ctx context.Context, id string) (*AccessPoint, error) {
	return scanAccessPoint(r.s.pool.QueryRow(ctx,
		`SELECT `+accessPointColumns+` FROM access_points WHERE id = $1`, id))
}

// GetByMAC resolves the identifier the NAS redirect actually carries.
//
// This is the lookup the previous model had no way to perform: `ap=94:2a:6f:d0:30:57`
// arrives in a query string and had to resolve to a device before anything could
// verify presence on it.
func (r *AccessPointRepo) GetByMAC(ctx context.Context, mac string) (*AccessPoint, error) {
	return scanAccessPoint(r.s.pool.QueryRow(ctx,
		`SELECT `+accessPointColumns+` FROM access_points WHERE mac = $1::macaddr`, NormaliseMAC(mac)))
}

// NormaliseMAC reduces the spellings a vendor might send to one.
//
// UniFi sends `94:2a:6f:d0:30:57`; PacketFence's disconnect path sets NAS-Identifier
// to the same value with the colons stripped; a Called-Station-Id uses dashes. The
// column is `macaddr` so PostgreSQL is the one that decides what the canonical form
// is -- this only has to get the value into something it will accept.
func NormaliseMAC(mac string) string {
	var hex strings.Builder
	for _, c := range strings.ToLower(mac) {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			hex.WriteRune(c)
		}
	}
	s := hex.String()
	if len(s) != 12 {
		return mac // let PostgreSQL refuse it, with its own message
	}
	var out strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			out.WriteByte(':')
		}
		out.WriteString(s[i : i+2])
	}
	return out.String()
}

func derivedAPID(mac string) string {
	return "ap-" + strings.ReplaceAll(NormaliseMAC(mac), ":", "")
}

func derivedClientID(mac string) string {
	return "rc-" + strings.ReplaceAll(NormaliseMAC(mac), ":", "")
}

// Reconcile applies one discovery run against a controller.
//
// Ids are derived from the MAC rather than generated, which is what makes running
// discovery twice a no-op instead of a second copy of every device.
//
// Three outcomes, and the third is the one the fused model could not express:
//
//   - **adopted** -- a device Foyer had never seen.
//   - **updated** -- a device it had, with fresh name, firmware or address.
//   - **missing** -- a device the controller stopped reporting. Its row survives,
//     marked, because an AP that disappeared is not an AP that was deleted, and
//     deleting it would drop the MAC that resolves last week's session rows.
//
// A device speaking RADIUS from its own address gets a clients.conf row derived from
// that address, sharing the site's one secret. A device behind a proxying controller
// gets none: Aruba's dynamic-radius-proxy is an operator toggle Foyer cannot see, so
// the honest move is to leave the binding unset and let an unmatched source address
// be a first-class finding rather than a guess that silently drops packets.
func (r *AccessPointRepo) Reconcile(
	ctx context.Context,
	controllerID, siteID, sharedSecretID, driver string,
	devices []DiscoveredAP,
	at time.Time,
) (Discovery, error) {
	result := Discovery{SharedSecretID: sharedSecretID, AccessPointIDs: []string{}}

	err := r.s.tx(ctx, func(tx pgx.Tx) error {
		seen := make([]string, 0, len(devices))

		for _, d := range devices {
			mac := NormaliseMAC(d.MAC)
			apID := derivedAPID(mac)
			seen = append(seen, apID)
			result.AccessPointIDs = append(result.AccessPointIDs, apID)

			via := d.RadiusVia
			if via == "" {
				via = "self"
			}

			var clientID *string
			if via == "self" && d.IPAddress != nil {
				id := derivedClientID(mac)
				if _, err := tx.Exec(ctx, `
					INSERT INTO radius_clients
						(id, site_id, name, address, source, driver, controller_id, shared_secret_id)
					VALUES ($1, $2, $3, ($4::inet), 'discovered', $5, $6, $7)
					ON CONFLICT (id) DO UPDATE SET
						address          = EXCLUDED.address,
						name             = EXCLUDED.name,
						shared_secret_id = EXCLUDED.shared_secret_id,
						updated_at       = now()`,
					id, siteID, d.Name, d.IPAddress.String(), driver, controllerID, sharedSecretID); err != nil {
					return mapErr(err)
				}
				clientID = &id
			}

			var inserted bool
			if err := tx.QueryRow(ctx, `
				INSERT INTO access_points
					(id, site_id, controller_id, mac, name, model, firmware, ip_address,
					 radius_client_id, radius_via, state, last_seen_at)
				VALUES ($1, $2, $3, $4::macaddr, $5, $6, $7, $8::inet, $9, $10, $11, $12)
				ON CONFLICT (id) DO UPDATE SET
					site_id          = EXCLUDED.site_id,
					controller_id    = EXCLUDED.controller_id,
					name             = EXCLUDED.name,
					model            = EXCLUDED.model,
					firmware         = EXCLUDED.firmware,
					ip_address       = EXCLUDED.ip_address,
					radius_client_id = coalesce(EXCLUDED.radius_client_id, access_points.radius_client_id),
					radius_via       = EXCLUDED.radius_via,
					state            = EXCLUDED.state,
					missing_since    = NULL,
					last_seen_at     = EXCLUDED.last_seen_at,
					updated_at       = now()
				RETURNING (xmax = 0)`,
				apID, siteID, controllerID, mac, d.Name, d.Model, d.Firmware,
				addrText(d.IPAddress), clientID, via, orUnknown(d.State), at).Scan(&inserted); err != nil {
				return mapErr(err)
			}
			if inserted {
				result.Adopted++
			} else {
				result.Updated++
			}
		}

		tag, err := tx.Exec(ctx, `
			UPDATE access_points
			SET state = 'missing',
			    missing_since = coalesce(missing_since, $2),
			    updated_at = now()
			WHERE controller_id = $1 AND NOT (id = ANY($3)) AND state <> 'missing'`,
			controllerID, at, seen)
		if err != nil {
			return mapErr(err)
		}
		result.Missing = int(tag.RowsAffected())
		return nil
	})
	if err != nil {
		return Discovery{}, err
	}
	return result, nil
}

// AttachRadiusClient binds a device to the clients.conf row its Access-Requests
// arrive from. It is the operator's answer for a proxying controller, where
// discovery deliberately leaves the binding unset.
func (r *AccessPointRepo) AttachRadiusClient(ctx context.Context, apID string, clientID *string) error {
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE access_points SET radius_client_id = $2, updated_at = now() WHERE id = $1`, apID, clientID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a device the operator has decided is gone for good.
func (r *AccessPointRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM access_points WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func orUnknown(state string) string {
	if state == "" {
		return "unknown"
	}
	return state
}

func addrText(a *netip.Addr) *string {
	if a == nil {
		return nil
	}
	s := a.String()
	return &s
}
