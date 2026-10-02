package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// NetworkSettingsRepo owns the transport and RADIUS-plane settings an operator sets
// in the console (migration 00030): the portal's public address, the sign-in
// throttles and request ceilings, FreeRADIUS's generator settings, the two
// certificates with their sealed keys, the per-client CoA timing, and the record of
// the last client-table sync and configuration apply.
//
// **The store keeps the values; it does not judge them.** Every bound is the API's
// (`internal/api/netsettings.go`), which answers a refusal with the figure, and the
// JSON columns are the contract's own shapes, passed through as raw bytes so this
// package does not import the contract's types.
type NetworkSettingsRepo struct{ s *Store }

// NetworkSettings returns the repository. A method rather than a field on Store so
// that adding it touches no other file of this package.
func (s *Store) NetworkSettings() *NetworkSettingsRepo { return &NetworkSettingsRepo{s: s} }

// NetworkSettingsRow is the deployment's row. Nil is "not set here".
type NetworkSettingsRow struct {
	PortalURL        *string
	VenueURL         *string
	Throttles        json.RawMessage
	BodyCeilings     json.RawMessage
	RadiusMaxServers *int
	EAPTLSEnabled    bool
	RadiusChangedAt  *time.Time
	UpdatedAt        *time.Time
	UpdatedBy        *string
}

// Get answers the row, or a zero row when nothing was ever saved — never
// ErrNotFound, because "nothing saved" is the ordinary state of a new install.
func (r *NetworkSettingsRepo) Get(ctx context.Context) (NetworkSettingsRow, error) {
	var row NetworkSettingsRow
	var throttles, ceilings []byte
	err := r.s.pool.QueryRow(ctx, `
		SELECT portal_url, venue_url, throttles, body_ceilings, radius_max_servers,
		       eap_tls_enabled, radius_changed_at, updated_at, updated_by
		FROM network_settings`).Scan(&row.PortalURL, &row.VenueURL, &throttles, &ceilings,
		&row.RadiusMaxServers, &row.EAPTLSEnabled, &row.RadiusChangedAt, &row.UpdatedAt, &row.UpdatedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return NetworkSettingsRow{}, nil
	}
	if err != nil {
		return NetworkSettingsRow{}, mapErr(err)
	}
	row.Throttles, row.BodyCeilings = throttles, ceilings
	return row, nil
}

// SaveTransport writes the portal address, the venue address, the throttles and
// the ceilings. The generator settings are left as they are.
func (r *NetworkSettingsRepo) SaveTransport(ctx context.Context, portalURL, venueURL *string, throttles, ceilings json.RawMessage, actor string) error {
	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO network_settings (portal_url, venue_url, throttles, body_ceilings, updated_at, updated_by)
		VALUES ($1, $2, $3, $4, now(), $5)
		ON CONFLICT (tenant_id) DO UPDATE SET
		    portal_url = EXCLUDED.portal_url, venue_url = EXCLUDED.venue_url,
		    throttles = EXCLUDED.throttles, body_ceilings = EXCLUDED.body_ceilings,
		    updated_at = now(), updated_by = EXCLUDED.updated_by`,
		portalURL, venueURL, nullJSON(throttles), nullJSON(ceilings), actor)
	return mapErr(err)
}

// SaveRadius writes the generator settings and stamps radius_changed_at, which is
// what makes the running server's copy stale until the next apply.
func (r *NetworkSettingsRepo) SaveRadius(ctx context.Context, maxServers int, eapTLS bool, actor string) error {
	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO network_settings (radius_max_servers, eap_tls_enabled, radius_changed_at, updated_at, updated_by)
		VALUES ($1, $2, now(), now(), $3)
		ON CONFLICT (tenant_id) DO UPDATE SET
		    radius_max_servers = EXCLUDED.radius_max_servers, eap_tls_enabled = EXCLUDED.eap_tls_enabled,
		    radius_changed_at = now(), updated_at = now(), updated_by = EXCLUDED.updated_by`,
		maxServers, eapTLS, actor)
	return mapErr(err)
}

func nullJSON(b json.RawMessage) any {
	if len(b) == 0 {
		return nil
	}
	return []byte(b)
}

// ── certificates ─────────────────────────────────────────────────────────────

// Certificate purposes.
const (
	CertificatePortalTLS = "portal-tls"
	CertificateEAPTLS    = "eap-tls"
)

// NetworkCertificate is one stored certificate. The key is never on this type: it
// is unsealed only by CertificateKey, for the one caller that serves it.
type NetworkCertificate struct {
	Purpose        string
	CertificatePEM string
	CAPEM          *string
	UpdatedAt      time.Time
	UpdatedBy      string
}

func certificateAAD(purpose string) []byte {
	return SecretContext("network_certificates", "private_key_sealed", purpose)
}

// PutEAPCertificate seals the key and stores the EAP-TLS pair with its CA, and
// stamps radius_changed_at in the same transaction: the running server does not
// have the new certificate until the next apply, and the console says so.
//
// ErrNoKeyring when this deployment has no key-encryption key — for both puts: a
// private key is never stored in the clear.
func (r *NetworkSettingsRepo) PutEAPCertificate(ctx context.Context, certPEM string, keyPEM []byte, caPEM, actor string) error {
	sealed, err := r.s.cfg.Keys.Seal(keyPEM, certificateAAD(CertificateEAPTLS))
	if err != nil {
		return err
	}
	return mapErr(r.s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO network_certificates (purpose, certificate_pem, private_key_sealed, ca_pem, updated_at, updated_by)
			VALUES ($1, $2, $3, $4, now(), $5)
			ON CONFLICT (tenant_id, purpose) DO UPDATE SET
			    certificate_pem = EXCLUDED.certificate_pem, private_key_sealed = EXCLUDED.private_key_sealed,
			    ca_pem = EXCLUDED.ca_pem, updated_at = now(), updated_by = EXCLUDED.updated_by`,
			CertificateEAPTLS, certPEM, sealed, caPEM, actor); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO network_settings (radius_changed_at, updated_at, updated_by) VALUES (now(), now(), $1)
			ON CONFLICT (tenant_id) DO UPDATE SET radius_changed_at = now(), updated_at = now(), updated_by = EXCLUDED.updated_by`,
			actor)
		return err
	}))
}

// PutPortalCertificate stores the portal pair and, when portalURL is not nil, the
// portal address with it, in one transaction: the two must agree, so a write that
// changed one and not the other would leave a portal serving a name it is not
// reached by.
func (r *NetworkSettingsRepo) PutPortalCertificate(ctx context.Context, certPEM string, keyPEM []byte, portalURL *string, actor string) error {
	sealed, err := r.s.cfg.Keys.Seal(keyPEM, certificateAAD(CertificatePortalTLS))
	if err != nil {
		return err
	}
	return mapErr(r.s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO network_certificates (purpose, certificate_pem, private_key_sealed, updated_at, updated_by)
			VALUES ($1, $2, $3, now(), $4)
			ON CONFLICT (tenant_id, purpose) DO UPDATE SET
			    certificate_pem = EXCLUDED.certificate_pem, private_key_sealed = EXCLUDED.private_key_sealed,
			    updated_at = now(), updated_by = EXCLUDED.updated_by`,
			CertificatePortalTLS, certPEM, sealed, actor); err != nil {
			return err
		}
		if portalURL == nil {
			return nil
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO network_settings (portal_url, updated_at, updated_by) VALUES ($1, now(), $2)
			ON CONFLICT (tenant_id) DO UPDATE SET
			    portal_url = EXCLUDED.portal_url, updated_at = now(), updated_by = EXCLUDED.updated_by`,
			*portalURL, actor)
		return err
	}))
}

// Certificate answers the stored certificate for a purpose, or ErrNotFound.
func (r *NetworkSettingsRepo) Certificate(ctx context.Context, purpose string) (NetworkCertificate, error) {
	c := NetworkCertificate{Purpose: purpose}
	err := r.s.pool.QueryRow(ctx, `
		SELECT certificate_pem, ca_pem, updated_at, updated_by FROM network_certificates WHERE purpose = $1`,
		purpose).Scan(&c.CertificatePEM, &c.CAPEM, &c.UpdatedAt, &c.UpdatedBy)
	if err != nil {
		return NetworkCertificate{}, mapErr(err)
	}
	return c, nil
}

// CertificateKey unseals the private key. Two callers: the portal's TLS listener
// and the FreeRADIUS tree writer. It is never part of an API answer.
func (r *NetworkSettingsRepo) CertificateKey(ctx context.Context, purpose string) ([]byte, error) {
	var sealed []byte
	if err := r.s.pool.QueryRow(ctx,
		`SELECT private_key_sealed FROM network_certificates WHERE purpose = $1`, purpose).Scan(&sealed); err != nil {
		return nil, mapErr(err)
	}
	return r.s.cfg.Keys.Open(sealed, certificateAAD(purpose))
}

// DeleteCertificate removes one. Removing an absent one is not an error.
func (r *NetworkSettingsRepo) DeleteCertificate(ctx context.Context, purpose string) error {
	_, err := r.s.pool.Exec(ctx, `DELETE FROM network_certificates WHERE purpose = $1`, purpose)
	return mapErr(err)
}

// ── CoA timing ──────────────────────────────────────────────────────────────

// CoATiming is one client's stored timing.
type CoATiming struct {
	TimeoutMs int
	Retries   int
}

// CoATimings answers every stored timing, by radius_clients id.
func (r *NetworkSettingsRepo) CoATimings(ctx context.Context) (map[string]CoATiming, error) {
	rows, err := r.s.pool.Query(ctx, `SELECT radius_client_id, timeout_ms, retries FROM radius_client_coa_timing`)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := map[string]CoATiming{}
	for rows.Next() {
		var id string
		var t CoATiming
		if err := rows.Scan(&id, &t.TimeoutMs, &t.Retries); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

// CoATimingFor answers one client's timing; ok is false when none is stored.
func (r *NetworkSettingsRepo) CoATimingFor(ctx context.Context, radiusClientID string) (CoATiming, bool, error) {
	var t CoATiming
	err := r.s.pool.QueryRow(ctx,
		`SELECT timeout_ms, retries FROM radius_client_coa_timing WHERE radius_client_id = $1`,
		radiusClientID).Scan(&t.TimeoutMs, &t.Retries)
	if errors.Is(err, pgx.ErrNoRows) {
		return CoATiming{}, false, nil
	}
	if err != nil {
		return CoATiming{}, false, mapErr(err)
	}
	return t, true, nil
}

// SetCoATiming stores a client's timing; nil clears it back to the defaults.
func (r *NetworkSettingsRepo) SetCoATiming(ctx context.Context, radiusClientID string, t *CoATiming) error {
	if t == nil {
		_, err := r.s.pool.Exec(ctx, `DELETE FROM radius_client_coa_timing WHERE radius_client_id = $1`, radiusClientID)
		return mapErr(err)
	}
	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO radius_client_coa_timing (radius_client_id, timeout_ms, retries) VALUES ($1, $2, $3)
		ON CONFLICT (radius_client_id) DO UPDATE SET timeout_ms = EXCLUDED.timeout_ms, retries = EXCLUDED.retries`,
		radiusClientID, t.TimeoutMs, t.Retries)
	return mapErr(err)
}

// ── the last sync and the last apply ─────────────────────────────────────────

// Radius operation kinds.
const (
	RadiusOperationSync  = "sync"
	RadiusOperationApply = "apply"
)

// RecordRadiusOperation keeps the latest record of one kind, replacing the last.
func (r *NetworkSettingsRepo) RecordRadiusOperation(ctx context.Context, kind string, at time.Time, record json.RawMessage) error {
	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO radius_operations (kind, at, record) VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, kind) DO UPDATE SET at = EXCLUDED.at, record = EXCLUDED.record`,
		kind, at, []byte(record))
	return mapErr(err)
}

// RadiusOperation answers the latest record of one kind; nil when there is none.
func (r *NetworkSettingsRepo) RadiusOperation(ctx context.Context, kind string) (json.RawMessage, *time.Time, error) {
	var rec []byte
	var at time.Time
	err := r.s.pool.QueryRow(ctx, `SELECT at, record FROM radius_operations WHERE kind = $1`, kind).Scan(&at, &rec)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, mapErr(err)
	}
	return rec, &at, nil
}
