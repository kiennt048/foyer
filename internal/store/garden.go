package store

import "context"

// GardenRepo owns `walled_garden_entries`.
//
// The walled garden is unauthenticated egress. Every row here is a destination any
// device that associates can reach without identifying itself, which is why the
// list is short, audited, and why `required` entries are not the operator's to
// delete: without the portal's own name, its certificate chain's OCSP and CRL
// endpoints and the RFC 8908 endpoint, the portal cannot serve itself.
type GardenRepo struct{ s *Store }

const gardenColumns = `id, site_id, kind, value, note, origin, created_at`

// ListForSite returns a site's garden, required entries first.
func (r *GardenRepo) ListForSite(ctx context.Context, siteID string) ([]GardenEntry, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT `+gardenColumns+`
		FROM walled_garden_entries
		WHERE site_id = $1
		ORDER BY (origin = 'required') DESC, kind, value`, siteID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	out := []GardenEntry{}
	for rows.Next() {
		var e GardenEntry
		if err := rows.Scan(&e.ID, &e.SiteID, &e.Kind, &e.Value, &e.Note, &e.Origin, &e.CreatedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, e)
	}
	return out, mapErr(rows.Err())
}

// Create adds an operator entry.
func (r *GardenRepo) Create(ctx context.Context, e *GardenEntry) error {
	if e.Origin == "" {
		e.Origin = "operator"
	}
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO walled_garden_entries (id, site_id, kind, value, note, origin)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at`,
		e.ID, e.SiteID, e.Kind, e.Value, e.Note, e.Origin).Scan(&e.CreatedAt)
	return mapErr(err)
}

// Delete removes an entry.
//
// It refuses a `required` one at the database level rather than trusting the caller
// to check: the WHERE clause excludes them, so a required entry reports ErrNotFound
// and the API answers with a refusal rather than a 500 from a foreign key.
func (r *GardenRepo) Delete(ctx context.Context, siteID, id string) error {
	tag, err := r.s.pool.Exec(ctx,
		`DELETE FROM walled_garden_entries WHERE id = $1 AND site_id = $2 AND origin <> 'required'`,
		id, siteID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// EffectiveKind reports the weakest matching the site's drivers can do.
//
// It is the minimum rather than the maximum on purpose: a garden is only as strong
// as the device that enforces it least well, and a site with one FQDN/SNI driver
// and one IP-range driver has an IP-range garden. `none` beats both -- a driver that
// cannot do a garden at all means unauthenticated devices reach whatever the network
// reaches.
func (r *GardenRepo) EffectiveKind(ctx context.Context, siteID string) (string, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT DISTINCT coalesce(capabilities ->> 'walledGarden', 'none')
		FROM radius_clients WHERE site_id = $1 AND enabled`, siteID)
	if err != nil {
		return "", mapErr(err)
	}
	defer rows.Close()

	rank := map[string]int{"none": 0, "ip-range": 1, "fqdn-sni": 2}
	weakest := ""
	for rows.Next() {
		var kind string
		if err := rows.Scan(&kind); err != nil {
			return "", mapErr(err)
		}
		if weakest == "" || rank[kind] < rank[weakest] {
			weakest = kind
		}
	}
	if err := rows.Err(); err != nil {
		return "", mapErr(err)
	}
	if weakest == "" {
		// No devices at all. Nothing enforces anything, and saying "fqdn-sni"
		// here would be the console promising a garden that has no enforcer.
		return "none", nil
	}
	return weakest, nil
}
