package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// ControllerRepo owns `controllers` and `controller_vendor_sites`.
//
// The controller is the object an operator actually possesses: they have a URL and
// a credential for it, and everything else about their network is discovered
// through it. Registering one is the step the product could not perform at all
// before this schema existed.
type ControllerRepo struct{ s *Store }

const controllerColumns = `
	id, name, vendor, base_url, vendor_controller_id, auth_mode, tls_mode,
	certificate_fingerprint, username, (credential IS NOT NULL) AS credential_set,
	reachability, version, version_supported, flavour, last_contact_at, created_at, updated_at`

func scanController(row interface{ Scan(...any) error }) (*Controller, error) {
	var c Controller
	err := row.Scan(&c.ID, &c.Name, &c.Vendor, &c.BaseURL, &c.VendorControllerID,
		&c.AuthMode, &c.TLSMode, &c.CertificateFingerprint, &c.Username, &c.CredentialSet,
		&c.Reachability, &c.Version, &c.VersionSupported, &c.Flavour,
		&c.LastContactAt, &c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &c, nil
}

// List returns every controller with its vendor sites attached.
func (r *ControllerRepo) List(ctx context.Context) ([]Controller, error) {
	rows, err := r.s.pool.Query(ctx, `SELECT `+controllerColumns+` FROM controllers ORDER BY name`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []Controller
	ids := make([]string, 0)
	for rows.Next() {
		c, err := scanController(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
		ids = append(ids, c.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}

	sites, err := r.vendorSitesFor(ctx, ids)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].VendorSites = sites[out[i].ID]
		if out[i].VendorSites == nil {
			out[i].VendorSites = []VendorSite{}
		}
	}
	return out, nil
}

// Get returns one controller with its vendor sites.
func (r *ControllerRepo) Get(ctx context.Context, id string) (*Controller, error) {
	c, err := scanController(r.s.pool.QueryRow(ctx,
		`SELECT `+controllerColumns+` FROM controllers WHERE id = $1`, id))
	if err != nil {
		return nil, err
	}
	sites, err := r.vendorSitesFor(ctx, []string{id})
	if err != nil {
		return nil, err
	}
	c.VendorSites = sites[id]
	if c.VendorSites == nil {
		c.VendorSites = []VendorSite{}
	}
	return c, nil
}

func (r *ControllerRepo) vendorSitesFor(ctx context.Context, ids []string) (map[string][]VendorSite, error) {
	out := map[string][]VendorSite{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.s.pool.Query(ctx, `
		SELECT controller_id, vendor_site_id, name, bound_site_id
		FROM controller_vendor_sites
		WHERE controller_id = ANY($1)
		ORDER BY name`, ids)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid string
		var vs VendorSite
		if err := rows.Scan(&cid, &vs.ID, &vs.Name, &vs.BoundSiteID); err != nil {
			return nil, mapErr(err)
		}
		out[cid] = append(out[cid], vs)
	}
	return out, mapErr(rows.Err())
}

// Create registers a controller. secret is the API key or the password, and is
// sealed here rather than by the caller so that no code path can store one
// unsealed. An empty secret leaves the column NULL and `credentialSet` false.
func (r *ControllerRepo) Create(ctx context.Context, c *Controller, secret []byte) error {
	sealed, err := r.sealCredential(c.ID, secret)
	if err != nil {
		return err
	}
	err = r.s.pool.QueryRow(ctx, `
		INSERT INTO controllers
			(id, name, vendor, base_url, vendor_controller_id, auth_mode, tls_mode, username, credential)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING created_at, updated_at, (credential IS NOT NULL)`,
		c.ID, c.Name, c.Vendor, c.BaseURL, c.VendorControllerID, c.AuthMode, c.TLSMode,
		c.Username, sealed).
		Scan(&c.CreatedAt, &c.UpdatedAt, &c.CredentialSet)
	if err != nil {
		return mapErr(err)
	}
	c.Reachability = "unknown"
	c.VendorSites = []VendorSite{}
	return nil
}

// ControllerPatch is the mutable part of a controller. A nil field is unchanged.
type ControllerPatch struct {
	Name               *string
	BaseURL            *string
	VendorControllerID **string
	TLSMode            *string
	Username           *string
	// Secret replaces the sealed credential. A nil pointer leaves it alone; a
	// pointer to an empty slice clears it.
	Secret *[]byte
}

// Update applies a patch.
func (r *ControllerRepo) Update(ctx context.Context, id string, p ControllerPatch) (*Controller, error) {
	var vendorControllerID *string
	setVendorControllerID := p.VendorControllerID != nil
	if setVendorControllerID {
		vendorControllerID = *p.VendorControllerID
	}

	var sealed []byte
	setSecret := p.Secret != nil
	if setSecret && len(*p.Secret) > 0 {
		var err error
		sealed, err = r.sealCredential(id, *p.Secret)
		if err != nil {
			return nil, err
		}
	}

	if _, err := r.s.pool.Exec(ctx, `
		UPDATE controllers SET
			name       = coalesce($2, name),
			base_url   = coalesce($3, base_url),
			vendor_controller_id = CASE WHEN $4 THEN $5 ELSE vendor_controller_id END,
			tls_mode   = coalesce($6, tls_mode),
			username   = coalesce($7, username),
			credential = CASE WHEN $8 THEN $9 ELSE credential END,
			updated_at = now()
		WHERE id = $1`,
		id, p.Name, p.BaseURL, setVendorControllerID, vendorControllerID,
		p.TLSMode, p.Username, setSecret, sealed); err != nil {
		return nil, mapErr(err)
	}
	return r.Get(ctx, id)
}

// Delete removes a controller.
//
// Access points discovered from it cascade away with it; RADIUS clients do not,
// because a clients.conf row whose address is still receiving Access-Requests must
// not silently vanish. The caller checks InUse first and refuses with
// /problems/controller-in-use.
func (r *ControllerRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM controllers WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// InUse reports how many RADIUS clients still reference this controller.
func (r *ControllerRepo) InUse(ctx context.Context, id string) (int, error) {
	var n int
	err := r.s.pool.QueryRow(ctx,
		`SELECT count(*) FROM radius_clients WHERE controller_id = $1`, id).Scan(&n)
	return n, mapErr(err)
}

// Credential unseals the stored API key or password.
//
// It returns ErrSecretUnset when none is stored and ErrNoKeyring when the process
// has no KEK -- never an empty byte slice, because a driver that authenticates with
// an empty credential produces a 401 that reads like a wrong password.
func (r *ControllerRepo) Credential(ctx context.Context, id string) ([]byte, error) {
	var sealed []byte
	err := r.s.pool.QueryRow(ctx, `SELECT credential FROM controllers WHERE id = $1`, id).Scan(&sealed)
	if err != nil {
		return nil, mapErr(err)
	}
	if len(sealed) == 0 {
		return nil, ErrSecretUnset
	}
	return r.s.cfg.Keys.Open(sealed, SecretContext("controllers", "credential", id))
}

func (r *ControllerRepo) sealCredential(id string, secret []byte) ([]byte, error) {
	if len(secret) == 0 {
		return nil, nil
	}
	return r.s.cfg.Keys.Seal(secret, SecretContext("controllers", "credential", id))
}

// RecordProbe writes what a probe observed, including the vendor site list.
//
// The site list is replaced rather than merged, and bindings survive by
// vendor_site_id: a site the controller no longer reports has genuinely gone, and
// keeping a stale row would let a Foyer site stay bound to something that no longer
// exists.
func (r *ControllerRepo) RecordProbe(ctx context.Context, id string, p ControllerProbe, at time.Time) error {
	return r.s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE controllers SET
				reachability            = $2,
				version                 = $3,
				version_supported       = $4,
				flavour                 = $5,
				certificate_fingerprint = coalesce($6, certificate_fingerprint),
				last_contact_at         = $7,
				updated_at              = now()
			WHERE id = $1`,
			id, p.Reachability, p.Version, p.VersionSupported, p.Flavour, p.CertificateFingerprint, at)
		if err != nil {
			return mapErr(err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}

		if p.VendorSites == nil {
			return nil
		}
		keep := make([]string, 0, len(p.VendorSites))
		for _, vs := range p.VendorSites {
			keep = append(keep, vs.ID)
			if _, err := tx.Exec(ctx, `
				INSERT INTO controller_vendor_sites (controller_id, vendor_site_id, name)
				VALUES ($1, $2, $3)
				ON CONFLICT (controller_id, vendor_site_id) DO UPDATE SET name = EXCLUDED.name`,
				id, vs.ID, vs.Name); err != nil {
				return mapErr(err)
			}
		}
		_, err = tx.Exec(ctx,
			`DELETE FROM controller_vendor_sites WHERE controller_id = $1 AND NOT (vendor_site_id = ANY($2))`,
			id, keep)
		return mapErr(err)
	})
}

// BindVendorSite binds one of the controller's own sites to a Foyer site, or
// unbinds it when siteID is nil.
//
// At most one controller site may bind to a Foyer site; the unique index enforces
// it, and the reason is that a site whose grants two controllers can authorise has
// no answer to which one revokes them.
func (r *ControllerRepo) BindVendorSite(ctx context.Context, controllerID, vendorSiteID string, siteID *string) error {
	tag, err := r.s.pool.Exec(ctx, `
		UPDATE controller_vendor_sites SET bound_site_id = $3
		WHERE controller_id = $1 AND vendor_site_id = $2`,
		controllerID, vendorSiteID, siteID)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
