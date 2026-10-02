package store

import (
	"context"
	"net/netip"
	"time"
)

// RadiusClientRepo owns `radius_clients`, and serves the contract's `Nas` surface.
//
// A `Nas` in the contract is a **device**: it has a MAC, and `radiusVia` says
// whether its Access-Requests arrive from its own address or from a controller
// proxying for the whole cluster. A `radius_clients` row is a **clients.conf entry**.
// The two are 1:1 for UniFi, 12:1 for Aruba with dynamic-radius-proxy on, and 0:1
// for a MikroTik router that is its own NAS.
//
// So the read model is one LEFT JOIN, and the three cardinalities fall out of it
// rather than needing three code paths.
type RadiusClientRepo struct{ s *Store }

const nasSelect = `
	SELECT
		coalesce(ap.id, rc.id)                     AS nas_id,
		rc.id, rc.site_id, rc.name, rc.description, rc.address, rc.source, rc.driver,
		rc.controller_id, rc.shared_secret_id, rc.nas_identifier, rc.coa_port, rc.coa_vendor,
		rc.require_message_authenticator, rc.limit_proxy_state, rc.message_authenticator,
		rc.accounting_configured, rc.reachability, rc.capabilities, rc.enabled,
		rc.last_request_at, rc.probed_at, rc.created_at, rc.updated_at,
		ap.mac::text, coalesce(ap.radius_via, 'self'), ap.model, ap.firmware,
		host(ap.ip_address), ap.last_seen_at,
		(rc.shared_secret_id IS NOT NULL) AS secret_set,
		ss.last_used_at,
		coalesce(ap.name, rc.name) AS display_name
` + nasFrom

// nasFrom is the device population, and it is a constant so that nothing can count
// a different one.
//
// A `Nas` is a device: one row per access point behind a clients.conf entry, and one
// row for an entry with no access point at all. Anything that answers *how many
// devices are here* has to be over exactly the rows `ListNas` returns, or the
// console and the answer disagree about what a device is — which is the model error
// the object model in `docs/architecture.md` was written to end.
const nasFrom = `
	FROM radius_clients rc
	LEFT JOIN access_points ap ON ap.radius_client_id = rc.id
	LEFT JOIN shared_secrets ss ON ss.id = rc.shared_secret_id`

func scanNas(row interface{ Scan(...any) error }) (*Nas, error) {
	var n Nas
	err := row.Scan(&n.ID,
		&n.RadiusClient.ID, &n.SiteID, &n.RadiusClient.Name, &n.Description, &n.Address, &n.Source, &n.Driver,
		&n.ControllerID, &n.SharedSecretID, &n.NASIdentifier, &n.CoAPort, &n.CoAVendor,
		&n.RequireMsgAuth, &n.LimitProxyState, &n.MessageAuthState,
		&n.AccountingConfigured, &n.Reachability, &n.Capabilities, &n.Enabled,
		&n.LastRequestAt, &n.ProbedAt, &n.CreatedAt, &n.UpdatedAt,
		&n.MAC, &n.RadiusVia, &n.Model, &n.Firmware,
		&n.IPAddress, &n.LastSeenAt,
		&n.SecretSet, &n.SecretUsed, &n.Name)
	if err != nil {
		return nil, mapErr(err)
	}
	return &n, nil
}

// ListNas returns the device view, one page at a time.
func (r *RadiusClientRepo) ListNas(ctx context.Context, opts ListOptions) (Page[Nas], error) {
	limit := clampLimit(opts.Limit)
	after, err := decodeCursor(opts.Cursor)
	if err != nil {
		return Page[Nas]{}, err
	}

	rows, err := r.s.pool.Query(ctx, nasSelect+`
		WHERE ($1 = '' OR rc.site_id = $1)
		  AND ($2 = '' OR coalesce(ap.id, rc.id) > $2)
		  AND ($3 = '' OR coalesce(ap.name, rc.name) ILIKE '%' || $3 || '%'
		               OR host(rc.address) ILIKE '%' || $3 || '%'
		               OR coalesce(ap.mac::text, '') ILIKE '%' || $3 || '%')
		ORDER BY coalesce(ap.id, rc.id)
		LIMIT $4`, opts.SiteID, after, opts.Query, limit+1)
	if err != nil {
		return Page[Nas]{}, mapErr(err)
	}
	defer rows.Close()

	var items []Nas
	for rows.Next() {
		n, err := scanNas(rows)
		if err != nil {
			return Page[Nas]{}, err
		}
		items = append(items, *n)
	}
	if err := rows.Err(); err != nil {
		return Page[Nas]{}, mapErr(err)
	}
	return paginate(items, limit, func(n Nas) string { return n.ID }), nil
}

// GetNas resolves a device id, which is an access point's id where there is one and
// the client's own where there is not.
func (r *RadiusClientRepo) GetNas(ctx context.Context, id string) (*Nas, error) {
	return scanNas(r.s.pool.QueryRow(ctx, nasSelect+` WHERE coalesce(ap.id, rc.id) = $1`, id))
}

// RadiusReadiness answers two questions about a site in one statement: how many
// devices are registered here, and how many of them can speak RADIUS at all.
//
// ── why both numbers, and why they are counted here ──────────────────────────
//
// `listAuthAttempts` has to say something true on an empty stream, and *no device
// here has a shared secret* is a sentence covering two different afternoons. **No
// device is registered at all** is the first-run state and the answer is to register
// one. **Devices are registered and none has a secret** is either a deliberate
// controller-API deployment or the single most common real fault in the product —
// a wrong or missing shared secret, which FreeRADIUS answers by discarding the
// packet, leaving no `radpostauth` row for anything to report. One count separates
// them; without it a console has to pick one sentence and be wrong half the time.
//
// Counted over `nasFrom`, so the population is exactly the one `ListNas` returns and
// the number cannot mean something different from the list beside it.
//
// **Registered, not enabled**, unlike `ListClients`. The question is what an
// operator has set up, not what is currently answering: a disabled device is still
// registered, and telling somebody no access point exists here while one sits
// disabled in the list is the same wrong-sentence defect one level down.
func (r *RadiusClientRepo) RadiusReadiness(ctx context.Context, siteID string) (devices, withSecret int, err error) {
	err = r.s.pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE rc.shared_secret_id IS NOT NULL)`+nasFrom+`
		WHERE ($1 = '' OR rc.site_id = $1)`, siteID).Scan(&devices, &withSecret)
	if err != nil {
		return 0, 0, mapErr(err)
	}
	return devices, withSecret, nil
}

// ListClients returns the clients.conf rows for a site, or for every site when
// siteID is empty.
//
// This is the generator's view rather than the console's: one row per address, with
// the two BlastRADIUS switches, and no access points attached. `internal/raddb`
// renders it; the secret is fetched separately through SharedSecretRepo.Reveal so
// that a list of clients is not a list of secrets.
func (r *RadiusClientRepo) ListClients(ctx context.Context, siteID string) ([]RadiusClient, error) {
	rows, err := r.s.pool.Query(ctx, `
		SELECT id, site_id, name, description, address, source, driver, controller_id,
		       shared_secret_id, nas_identifier, coa_port, coa_vendor,
		       require_message_authenticator, limit_proxy_state, message_authenticator,
		       accounting_configured, reachability, capabilities, enabled,
		       last_request_at, probed_at, created_at, updated_at
		FROM radius_clients
		WHERE ($1 = '' OR site_id = $1) AND enabled
		ORDER BY address`, siteID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()

	var out []RadiusClient
	for rows.Next() {
		var c RadiusClient
		if err := rows.Scan(&c.ID, &c.SiteID, &c.Name, &c.Description, &c.Address, &c.Source,
			&c.Driver, &c.ControllerID, &c.SharedSecretID, &c.NASIdentifier, &c.CoAPort, &c.CoAVendor,
			&c.RequireMsgAuth, &c.LimitProxyState, &c.MessageAuthState,
			&c.AccountingConfigured, &c.Reachability, &c.Capabilities, &c.Enabled,
			&c.LastRequestAt, &c.ProbedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, c)
	}
	return out, mapErr(rows.Err())
}

// Create adds a clients.conf row.
func (r *RadiusClientRepo) Create(ctx context.Context, c *RadiusClient) error {
	if c.Source == "" {
		c.Source = "manual"
	}
	if c.MessageAuthState == "" {
		c.MessageAuthState = "unknown"
	}
	if c.Reachability == "" {
		c.Reachability = "unknown"
	}
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO radius_clients
			(id, site_id, name, description, address, source, driver, controller_id,
			 shared_secret_id, nas_identifier, coa_port, coa_vendor,
			 require_message_authenticator, limit_proxy_state, message_authenticator,
			 accounting_configured, reachability, capabilities, enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19)
		RETURNING created_at, updated_at`,
		c.ID, c.SiteID, c.Name, c.Description, c.Address, c.Source, c.Driver, c.ControllerID,
		c.SharedSecretID, c.NASIdentifier, c.CoAPort, c.CoAVendor,
		c.RequireMsgAuth, c.LimitProxyState, c.MessageAuthState,
		c.AccountingConfigured, c.Reachability, c.Capabilities, c.Enabled).
		Scan(&c.CreatedAt, &c.UpdatedAt)
	return mapErr(err)
}

// RadiusClientPatch is the mutable part of a client. A nil field is unchanged.
type RadiusClientPatch struct {
	Name           *string
	Description    **string
	Address        *netip.Prefix
	NASIdentifier  **string
	CoAPort        **int
	SharedSecretID **string
	Enabled        *bool
}

// Update applies a patch.
func (r *RadiusClientRepo) Update(ctx context.Context, id string, p RadiusClientPatch) error {
	var description, nasIdentifier, sharedSecretID *string
	var coaPort *int
	setDescription, setNASIdentifier, setCoAPort, setSecret :=
		p.Description != nil, p.NASIdentifier != nil, p.CoAPort != nil, p.SharedSecretID != nil
	if setDescription {
		description = *p.Description
	}
	if setNASIdentifier {
		nasIdentifier = *p.NASIdentifier
	}
	if setCoAPort {
		coaPort = *p.CoAPort
	}
	if setSecret {
		sharedSecretID = *p.SharedSecretID
	}

	tag, err := r.s.pool.Exec(ctx, `
		UPDATE radius_clients SET
			name             = coalesce($2, name),
			description      = CASE WHEN $3 THEN $4 ELSE description END,
			address          = coalesce($5, address),
			nas_identifier   = CASE WHEN $6 THEN $7 ELSE nas_identifier END,
			coa_port         = CASE WHEN $8 THEN $9 ELSE coa_port END,
			shared_secret_id = CASE WHEN $10 THEN $11 ELSE shared_secret_id END,
			enabled          = coalesce($12, enabled),
			updated_at       = now()
		WHERE id = $1`,
		id, p.Name, setDescription, description, p.Address,
		setNASIdentifier, nasIdentifier, setCoAPort, coaPort,
		setSecret, sharedSecretID, p.Enabled)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes a clients.conf row. Access points that pointed at it keep their
// row with radius_client_id NULL: the device still exists, it simply has no
// resolved address any more, and saying so is the failure state a fused
// device/client model could not express.
func (r *RadiusClientRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM radius_clients WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClientProbe is what a probe of one client observed.
type ClientProbe struct {
	Reachability string
	Capabilities Capabilities
	// MessageAuthenticator is what the wire showed: enforced, refused, or unknown
	// where nothing was measured. It is a different claim from the configured
	// require_message_authenticator, and the console alarms on `refused`.
	MessageAuthenticator string
	// AccountingConfigured is three-valued. Nil means not measured.
	AccountingConfigured *bool
}

// RecordProbe writes what a probe observed.
func (r *RadiusClientRepo) RecordProbe(ctx context.Context, id string, p ClientProbe, at time.Time) error {
	tag, err := r.s.pool.Exec(ctx, `
		UPDATE radius_clients SET
			reachability          = $2,
			capabilities          = $3,
			message_authenticator = $4,
			accounting_configured = coalesce($5, accounting_configured),
			probed_at             = $6,
			updated_at            = now()
		WHERE id = $1`,
		id, p.Reachability, p.Capabilities, p.MessageAuthenticator, p.AccountingConfigured, at)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// NasCounts is the overview's view of the estate.
type NasCounts struct {
	Total int
	// Unhealthy counts devices with something an operator has to act on: the
	// client is unreachable, its credentials have expired, or it refuses
	// Message-Authenticator -- which is a device left open to BlastRADIUS.
	Unhealthy int
}

// CountNas answers the overview's two device figures in one pass, over the same
// device view the console lists.
func (r *RadiusClientRepo) CountNas(ctx context.Context, siteID string) (NasCounts, error) {
	var c NasCounts
	err := r.s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE rc.reachability IN ('unreachable', 'credentials-expired')
		                           OR rc.message_authenticator = 'refused')
		FROM radius_clients rc
		LEFT JOIN access_points ap ON ap.radius_client_id = rc.id
		WHERE ($1 = '' OR rc.site_id = $1)`, siteID).Scan(&c.Total, &c.Unhealthy)
	return c, mapErr(err)
}

// MarkRequestSeen stamps the last time an Access-Request arrived from this address.
//
// It is what separates "the controller says the AP is offline" from "we have not
// heard from this RADIUS client" -- two of the three failure states the fused model
// collapsed into one word.
func (r *RadiusClientRepo) MarkRequestSeen(ctx context.Context, id string, at time.Time) error {
	_, err := r.s.pool.Exec(ctx, `UPDATE radius_clients SET last_request_at = $2 WHERE id = $1`, id, at)
	return mapErr(err)
}
