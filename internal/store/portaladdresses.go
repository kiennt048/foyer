package store

import (
	"context"
	"errors"
	"net/netip"
	"time"

	"github.com/jackc/pgx/v5"
)

// PortalAddressRepo is the RFC 8908 address book (migration 00045): which portal
// session, device and grant were last seen from a client address.
//
// It holds exactly the rules of `portal.MemoryAddressBook`, restated as one
// statement each so that two processes writing the same address agree:
//
//   - an entry is live for `window` after it was last seen, and a dead entry is
//     replaced whole rather than amended;
//   - a second MAC from a live entry's address marks it ambiguous until a window
//     after that sighting, and forgets the grant it held — the other device's grant
//     is not this one's;
//   - the same MAC keeps its grant across a new session, because the CNA destroys
//     its cookies and meets the portal again grantless.
type PortalAddressRepo struct{ s *Store }

// PortalAddresses returns the address-book repository.
func (s *Store) PortalAddresses() *PortalAddressRepo { return &PortalAddressRepo{s: s} }

// PortalAddress is one entry.
type PortalAddress struct {
	TokenHash string
	ClientMAC string
	GrantID   string
	Ambiguous bool
}

// Observe records that a browser at addr resolved this session. grantID may be
// empty. `window` is the book's liveness bound.
func (r *PortalAddressRepo) Observe(ctx context.Context, addr netip.Addr, tokenHash, clientMAC, grantID string, at time.Time, window time.Duration) error {
	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO portal_addresses AS p (address, token_hash, client_mac, grant_id, seen_at)
		VALUES ($1::inet, $2, $3::macaddr, nullif($4, ''), $5)
		ON CONFLICT (tenant_id, address) DO UPDATE SET
			ambiguous_until = CASE
				WHEN p.seen_at + make_interval(secs => $6) <= EXCLUDED.seen_at THEN NULL
				WHEN p.client_mac <> EXCLUDED.client_mac THEN EXCLUDED.seen_at + make_interval(secs => $6)
				ELSE p.ambiguous_until END,
			grant_id = CASE
				WHEN p.seen_at + make_interval(secs => $6) <= EXCLUDED.seen_at THEN EXCLUDED.grant_id
				WHEN p.client_mac <> EXCLUDED.client_mac THEN EXCLUDED.grant_id
				ELSE coalesce(EXCLUDED.grant_id, p.grant_id) END,
			token_hash = EXCLUDED.token_hash,
			client_mac = EXCLUDED.client_mac,
			seen_at    = EXCLUDED.seen_at`,
		addr.Unmap().WithZone("").String(), tokenHash, NormaliseMAC(clientMAC), grantID, at, window.Seconds())
	return mapErr(err)
}

// AttachGrant records a grant against every live address that session was seen
// from.
func (r *PortalAddressRepo) AttachGrant(ctx context.Context, tokenHash, grantID string, at time.Time, window time.Duration) error {
	_, err := r.s.pool.Exec(ctx, `
		UPDATE portal_addresses SET grant_id = $2
		 WHERE token_hash = $1 AND seen_at + make_interval(secs => $4) > $3`,
		tokenHash, grantID, at, window.Seconds())
	return mapErr(err)
}

// Lookup answers the live entry for addr. ErrNotFound when there is none.
func (r *PortalAddressRepo) Lookup(ctx context.Context, addr netip.Addr, at time.Time, window time.Duration) (PortalAddress, error) {
	var e PortalAddress
	var until *time.Time
	err := r.s.pool.QueryRow(ctx, `
		SELECT token_hash, client_mac::text, coalesce(grant_id, ''), ambiguous_until
		  FROM portal_addresses
		 WHERE address = $1::inet AND seen_at + make_interval(secs => $3) > $2`,
		addr.Unmap().WithZone("").String(), at, window.Seconds()).
		Scan(&e.TokenHash, &e.ClientMAC, &e.GrantID, &until)
	if errors.Is(err, pgx.ErrNoRows) {
		return PortalAddress{}, ErrNotFound
	}
	if err != nil {
		return PortalAddress{}, mapErr(err)
	}
	e.Ambiguous = until != nil && at.Before(*until)
	return e, nil
}

// Reap deletes the entries last seen before `before` — a window ago. The read path
// already ignores them; this is what keeps the address and the MAC from outliving
// the only question they answer.
func (r *PortalAddressRepo) Reap(ctx context.Context, before time.Time) (int64, error) {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM portal_addresses WHERE seen_at < $1`, before)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}

// The address book's personal data: the device, and the address it asked from.
// Erased by MAC here, and with an account's grants by the foreign key's cascade.
func init() {
	for _, c := range []PersonalColumn{
		{Table: "portal_addresses", Column: "client_mac", Holds: "client MAC last seen from an address (RFC 8908)", Identifies: []SubjectKind{SubjectMAC}},
		{Table: "portal_addresses", Column: "address", Holds: "IP address a device reached the portal from", WithRow: true},
	} {
		RegisterPersonalColumn(c)
	}
	// Before the grants (Order 20), whose deletion would otherwise cascade here
	// uncounted.
	RegisterEraser(Eraser{Table: "portal_addresses", Kinds: []SubjectKind{SubjectMAC, SubjectPhone}, Order: 15,
		Erase: func(ctx context.Context, tx pgx.Tx, s Subject) (int64, error) {
			if s.Kind == SubjectPhone {
				return execCount(ctx, tx, `DELETE FROM portal_addresses WHERE grant_id IN (`+phoneGrants+`)`, s.PhoneDigits)
			}
			return execCount(ctx, tx, `DELETE FROM portal_addresses WHERE client_mac = $1::macaddr`, s.MAC)
		}})
}
