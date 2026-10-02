package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DeviceCookieKeyRepo owns `device_cookie_keys` (migration 00053): the console's
// trusted-device cookie key ring, persisted so a restart does not untrust every
// device at once. `internal/api/devicecookie.go` has the survey and
// the policy; this is the storage and the transaction that keeps two processes in
// agreement.
type DeviceCookieKeyRepo struct{ s *Store }

// DeviceCookieKeys is constructed per call, so this aggregate adds a file rather
// than a field to the shared Store struct.
func (s *Store) DeviceCookieKeys() *DeviceCookieKeyRepo { return &DeviceCookieKeyRepo{s: s} }

// DeviceCookieKey is one unsealed key of the ring.
type DeviceCookieKey struct {
	ID        string
	Key       []byte
	CreatedAt time.Time
}

func deviceKeyContext(id string) []byte {
	return SecretContext("device_cookie_keys", "key_sealed", id)
}

// deviceKeyLock serialises the ring's read-mint-prune across every process on one
// database, so two control-plane processes starting together mint one key rather
// than two. A transaction-scoped advisory lock: released on commit or rollback.
const deviceKeyLock = `SELECT pg_advisory_xact_lock(hashtext('foyer.device_cookie_keys'))`

// Ring answers the usable keys, newest (the signing key) first. In one
// transaction it first mints a key when there is none usable or the newest is
// older than rotateAfter, and deletes every key whose successor has been active
// for keepFor or longer.
//
// A row that does not unseal — its KEK was removed from FOYER_KEK — is skipped,
// not deleted (the KEK may come back) and not fatal: if that leaves no usable key a
// new one is minted, so the worst case is that every trusted device becomes
// untrusted once, which is the fail-safe direction.
func (r *DeviceCookieKeyRepo) Ring(ctx context.Context, now time.Time, rotateAfter, keepFor time.Duration) ([]DeviceCookieKey, error) {
	if r.s.cfg.Keys == nil {
		return nil, ErrNoKeyring
	}
	var ring []DeviceCookieKey
	err := r.s.tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, deviceKeyLock); err != nil {
			return err
		}
		type row struct {
			id      string
			sealed  []byte
			created time.Time
		}
		rows, err := tx.Query(ctx,
			`SELECT id, key_sealed, created_at FROM device_cookie_keys ORDER BY created_at DESC, id DESC`)
		if err != nil {
			return err
		}
		var all []row
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.id, &x.sealed, &x.created); err != nil {
				rows.Close()
				return err
			}
			all = append(all, x)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// Retirement: a key is retired when the next newer key was created. The
		// newest is never retired.
		var drop []string
		ring = ring[:0]
		for i, x := range all {
			if i > 0 && now.Sub(all[i-1].created) >= keepFor {
				drop = append(drop, x.id)
				continue
			}
			key, err := r.s.cfg.Keys.Open(x.sealed, deviceKeyContext(x.id))
			if err != nil {
				continue
			}
			ring = append(ring, DeviceCookieKey{ID: x.id, Key: key, CreatedAt: x.created})
		}
		if len(drop) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM device_cookie_keys WHERE id = ANY($1)`, drop); err != nil {
				return err
			}
		}

		newestUsable := len(ring) > 0 && len(all) > 0 && ring[0].ID == all[0].id
		if newestUsable && now.Sub(ring[0].CreatedAt) < rotateAfter {
			return nil
		}
		k, err := r.mint(ctx, tx, now)
		if err != nil {
			return err
		}
		ring = append([]DeviceCookieKey{k}, ring...)
		return nil
	})
	if err != nil {
		return nil, mapErr(err)
	}
	return ring, nil
}

func (r *DeviceCookieKeyRepo) mint(ctx context.Context, tx pgx.Tx, now time.Time) (DeviceCookieKey, error) {
	key := make([]byte, 32)
	idRaw := make([]byte, 8)
	if _, err := rand.Read(key); err != nil {
		return DeviceCookieKey{}, fmt.Errorf("store: generating a device-cookie key: %w", err)
	}
	if _, err := rand.Read(idRaw); err != nil {
		return DeviceCookieKey{}, fmt.Errorf("store: generating a device-cookie key id: %w", err)
	}
	id := hex.EncodeToString(idRaw)
	sealed, err := r.s.cfg.Keys.Seal(key, deviceKeyContext(id))
	if err != nil {
		return DeviceCookieKey{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO device_cookie_keys (id, key_sealed, created_at) VALUES ($1, $2, $3)`,
		id, sealed, now); err != nil {
		return DeviceCookieKey{}, err
	}
	return DeviceCookieKey{ID: id, Key: key, CreatedAt: now}, nil
}
