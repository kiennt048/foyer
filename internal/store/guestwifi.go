package store

import (
	"context"
	"errors"
	"time"
)

// GuestWifiKeyRepo owns `guest_wifi_keys` (migration 00037): the WPA passphrase of
// a site's guest SSID, which the voucher slip's QR code needs to join a network
// that is not open.
//
// Sealed like a controller credential. `Status` never selects the value, so the
// settings read cannot hold it even in memory; `Reveal` is the slip's read and has
// that one caller.
type GuestWifiKeyRepo struct{ s *Store }

// GuestWifiKeys is constructed per call, so this aggregate adds a file rather than
// a field to the shared Store struct.
func (s *Store) GuestWifiKeys() *GuestWifiKeyRepo { return &GuestWifiKeyRepo{s: s} }

// GuestWifiKeyStatus is whether one is stored. Never the value.
type GuestWifiKeyStatus struct {
	Set       bool
	UpdatedAt *time.Time
	UpdatedBy *string
}

// ErrNotAWifiPassphrase is a value WPA would refuse: not 8-63 printable ASCII
// characters (IEEE 802.11-2020 Annex J.4.1).
var ErrNotAWifiPassphrase = errors.New("store: not a WPA passphrase")

// ValidWifiPassphrase is IEEE 802.11 Annex J's passphrase: 8 to 63 characters, each
// printable ASCII (32-126). The 64-hex-digit raw PSK form is not accepted: a phone's
// QR join string carries a passphrase, and a raw key printed on a slip is a string
// nobody can type.
func ValidWifiPassphrase(p string) bool {
	if len(p) < 8 || len(p) > 63 {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 32 || p[i] > 126 {
			return false
		}
	}
	return true
}

func wifiKeyContext(siteID string) []byte {
	return SecretContext("guest_wifi_keys", "passphrase", siteID)
}

// Status answers whether the site has a passphrase. A site with none is `Set: false`,
// never ErrNotFound.
func (r *GuestWifiKeyRepo) Status(ctx context.Context, siteID string) (GuestWifiKeyStatus, error) {
	var st GuestWifiKeyStatus
	var at time.Time
	err := r.s.pool.QueryRow(ctx,
		`SELECT updated_at, updated_by FROM guest_wifi_keys WHERE site_id = $1`, siteID).Scan(&at, &st.UpdatedBy)
	if err = mapErr(err); err != nil {
		if errors.Is(err, ErrNotFound) {
			return GuestWifiKeyStatus{}, nil
		}
		return GuestWifiKeyStatus{}, err
	}
	st.Set, st.UpdatedAt = true, &at
	return st, nil
}

// Set seals and stores the passphrase, replacing any before it.
func (r *GuestWifiKeyRepo) Set(ctx context.Context, siteID, passphrase, actor string) error {
	if !ValidWifiPassphrase(passphrase) {
		return ErrNotAWifiPassphrase
	}
	sealed, err := r.s.cfg.Keys.Seal([]byte(passphrase), wifiKeyContext(siteID))
	if err != nil {
		return err
	}
	var by *string
	if actor != "" {
		by = &actor
	}
	_, err = r.s.pool.Exec(ctx, `
		INSERT INTO guest_wifi_keys (site_id, passphrase, updated_by, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (site_id) DO UPDATE SET
			passphrase = EXCLUDED.passphrase, updated_by = EXCLUDED.updated_by, updated_at = now()`,
		siteID, sealed, by)
	return mapErr(err)
}

// Clear removes the passphrase. Idempotent.
func (r *GuestWifiKeyRepo) Clear(ctx context.Context, siteID string) error {
	_, err := r.s.pool.Exec(ctx, `DELETE FROM guest_wifi_keys WHERE site_id = $1`, siteID)
	return mapErr(err)
}

// Reveal unseals the passphrase for the voucher slip, or answers "" for a site with
// none. One caller; never logged.
func (r *GuestWifiKeyRepo) Reveal(ctx context.Context, siteID string) (string, error) {
	var sealed []byte
	err := r.s.pool.QueryRow(ctx, `SELECT passphrase FROM guest_wifi_keys WHERE site_id = $1`, siteID).Scan(&sealed)
	if err = mapErr(err); err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	plain, err := r.s.cfg.Keys.Open(sealed, wifiKeyContext(siteID))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}
