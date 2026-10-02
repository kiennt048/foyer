package store

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// SharedSecretRepo owns `shared_secrets`.
//
// The secret is one per controller-level RADIUS profile and is presented by every
// device using that profile, so it is a record in its own right rather than a
// column on the client. What that buys is a rotation that can tell the operator the
// truth: "this changes the secret for these eleven devices, and you paste the new
// one into your controller once."
type SharedSecretRepo struct{ s *Store }

const sharedSecretColumns = `id, site_id, label, created_at, last_rotated_at, last_used_at`

// ErrSecretNotText is returned when a shared secret carries a byte that cannot
// survive the projection into `nas.secret`.
//
// It is a distinct error rather than a generic write failure because the operator
// can act on it: the usual cause is a paste that brought a trailing newline or a
// non-breaking space with it.
var ErrSecretNotText = errors.New("store: shared secret must be printable ASCII")

// checkSecretBytes refuses a secret that `text` cannot hold or FreeRADIUS cannot
// read back.
//
// **The constraint belongs here, at the moment the operator is still holding the
// thing they pasted.** Without it the bytes are sealed happily, and the failure
// arrives much later inside `NasProjection().Sync` as
// `invalid byte sequence for encoding "UTF8": 0xff` -- a message that names neither
// the client nor the secret, on an operation the operator did not start.
//
// Printable ASCII, 0x21 to 0x7e:
//
//   - `nas.secret` is `text`, so any byte that is not valid UTF-8 is refused by
//     PostgreSQL, and a NUL is refused even though it is.
//   - FreeRADIUS's client definition is a text value in a config grammar; a
//     control character in it is at best unquotable and at worst silently truncates
//     the secret, which is an outage that authenticates nobody and explains nothing.
//   - **Space is excluded on purpose.** A leading or trailing space in a pasted
//     secret is invisible in every UI that will ever display it, and produces a
//     secret that differs from the one in the controller by a character nobody can
//     see.
//
// The error names the position and the class, never the byte's value: this reaches
// logs, and `docs/rules/security.md` §3 requires secrets to be redacted from them.
func checkSecretBytes(secret []byte) error {
	if len(secret) == 0 {
		return ErrSecretUnset
	}
	for i, b := range secret {
		switch {
		case b == ' ':
			return fmt.Errorf("%w: a space at byte %d", ErrSecretNotText, i)
		case b < 0x21 || b == 0x7f:
			return fmt.Errorf("%w: a control character at byte %d", ErrSecretNotText, i)
		case b > 0x7f:
			return fmt.Errorf("%w: a non-ASCII byte at byte %d", ErrSecretNotText, i)
		}
	}
	return nil
}

func scanSharedSecret(row interface{ Scan(...any) error }) (*SharedSecret, error) {
	var ss SharedSecret
	if err := row.Scan(&ss.ID, &ss.SiteID, &ss.Label, &ss.CreatedAt, &ss.LastRotatedAt, &ss.LastUsedAt); err != nil {
		return nil, mapErr(err)
	}
	return &ss, nil
}

// ListForSite returns the secrets defined for a site.
func (r *SharedSecretRepo) ListForSite(ctx context.Context, siteID string) ([]SharedSecret, error) {
	rows, err := r.s.pool.Query(ctx,
		`SELECT `+sharedSecretColumns+` FROM shared_secrets WHERE site_id = $1 ORDER BY created_at`, siteID)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	var out []SharedSecret
	for rows.Next() {
		ss, err := scanSharedSecret(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *ss)
	}
	return out, mapErr(rows.Err())
}

// Get returns one secret's metadata. Never its value -- see Reveal.
func (r *SharedSecretRepo) Get(ctx context.Context, id string) (*SharedSecret, error) {
	return scanSharedSecret(r.s.pool.QueryRow(ctx,
		`SELECT `+sharedSecretColumns+` FROM shared_secrets WHERE id = $1`, id))
}

// Create seals and stores a new secret.
//
// The caller generates the value with crypto/rand. There is no default and there
// never may be: FreeRADIUS ships an example secret, and a shipped default is the
// most common way appliance software gets owned at scale.
func (r *SharedSecretRepo) Create(ctx context.Context, ss *SharedSecret, secret []byte) error {
	if err := checkSecretBytes(secret); err != nil {
		return err
	}
	sealed, err := r.s.cfg.Keys.Seal(secret, SecretContext("shared_secrets", "secret", ss.ID))
	if err != nil {
		return err
	}
	err = r.s.pool.QueryRow(ctx, `
		INSERT INTO shared_secrets (id, site_id, label, secret)
		VALUES ($1, $2, $3, $4)
		RETURNING created_at`, ss.ID, ss.SiteID, ss.Label, sealed).Scan(&ss.CreatedAt)
	return mapErr(err)
}

// Rotate replaces the value and stamps last_rotated_at.
//
// Rotation is available whether or not the driver can push the new secret to the
// device: `Capabilities.RotateSecret` gates only the **push**. Gating generation on
// it would leave a site whose driver cannot push with no way to rotate at all,
// which is the same over-broad reading as "UniFi cannot do CoA".
func (r *SharedSecretRepo) Rotate(ctx context.Context, id string, secret []byte) error {
	if err := checkSecretBytes(secret); err != nil {
		return err
	}
	sealed, err := r.s.cfg.Keys.Seal(secret, SecretContext("shared_secrets", "secret", id))
	if err != nil {
		return err
	}
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE shared_secrets SET secret = $2, last_rotated_at = now() WHERE id = $1`, id, sealed)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Reveal unseals the secret.
//
// Two callers only: the raddb generator, which projects it into `nas.secret` where
// FreeRADIUS can read it, and the admin API's show-once response immediately after
// a rotation. It is never part of a list, never logged, and never in a support
// bundle -- `radiusd -X` output already carries secrets in cleartext and there will
// be a "send us your logs" button.
func (r *SharedSecretRepo) Reveal(ctx context.Context, id string) ([]byte, error) {
	var sealed []byte
	if err := r.s.pool.QueryRow(ctx, `SELECT secret FROM shared_secrets WHERE id = $1`, id).Scan(&sealed); err != nil {
		return nil, mapErr(err)
	}
	if len(sealed) == 0 {
		return nil, ErrSecretUnset
	}
	return r.s.cfg.Keys.Open(sealed, SecretContext("shared_secrets", "secret", id))
}

// AppliesTo names the RADIUS clients presenting this secret.
//
// It exists so that a rotation dialog can state the blast radius before the
// operator commits, rather than after.
func (r *SharedSecretRepo) AppliesTo(ctx context.Context, id string) ([]string, error) {
	rows, err := r.s.pool.Query(ctx,
		`SELECT id FROM radius_clients WHERE shared_secret_id = $1 ORDER BY id`, id)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var clientID string
		if err := rows.Scan(&clientID); err != nil {
			return nil, mapErr(err)
		}
		out = append(out, clientID)
	}
	return out, mapErr(rows.Err())
}

// MarkUsed stamps the last time a request authenticated against this secret.
func (r *SharedSecretRepo) MarkUsed(ctx context.Context, id string, at time.Time) error {
	_, err := r.s.pool.Exec(ctx, `UPDATE shared_secrets SET last_used_at = $2 WHERE id = $1`, id, at)
	return mapErr(err)
}

// Delete removes a secret. The foreign key from radius_clients is ON DELETE
// RESTRICT, so a secret still in use refuses to go rather than leaving clients with
// no way to authenticate.
func (r *SharedSecretRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM shared_secrets WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
