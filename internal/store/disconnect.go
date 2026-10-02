package store

import (
	"context"
	"time"
)

// The three reads and one write `disconnectSession` needs.
//
// None of them touches `radacct` or `radpostauth`. A disconnect is an act on the
// device; the device's own Acct-Stop is what closes the accounting row, so there is
// nothing for Foyer to write there and this file must never gain a statement that
// does.

// RevokeOpenGrantsForMAC revokes every grant for this device at this venue that is
// still open — not revoked, and not past its expiry — and answers how many.
//
// One statement, so two operators pressing the control together revoke each grant
// once between them rather than each reporting the same grants.
//
// A MAC that is not twelve hex digits identifies no grant and answers zero rather
// than letting PostgreSQL refuse the cast: a session whose Calling-Station-Id is
// not a MAC is a session no portal grant could have been issued for.
func (r *GrantRepo) RevokeOpenGrantsForMAC(ctx context.Context, siteID, mac string, at time.Time) (int64, error) {
	if len(MACOnly(mac)) != 12 {
		return 0, nil
	}
	tag, err := r.s.pool.Exec(ctx, `
		UPDATE portal_grants
		   SET revoked_at = $3
		 WHERE site_id = $1
		   AND client_mac = $2::macaddr
		   AND revoked_at IS NULL
		   AND (expires_at IS NULL OR expires_at > $3)`,
		siteID, NormaliseMAC(mac), at)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}

// OpenGrantVoucherForMAC names the voucher behind the newest open grant for this
// device at this venue, whose voucher is not already revoked. `ErrNotFound` when
// there is none, which is an answer and not a failure.
func (r *GrantRepo) OpenGrantVoucherForMAC(ctx context.Context, siteID, mac string, at time.Time) (string, error) {
	if len(MACOnly(mac)) != 12 {
		return "", ErrNotFound
	}
	var id string
	err := r.s.pool.QueryRow(ctx, `
		SELECT g.voucher_id
		  FROM portal_grants g
		  JOIN vouchers v ON v.id = g.voucher_id
		 WHERE g.site_id = $1
		   AND g.client_mac = $2::macaddr
		   AND g.revoked_at IS NULL
		   AND (g.expires_at IS NULL OR g.expires_at > $3)
		   AND v.state <> 'revoked'
		 ORDER BY g.granted_at DESC
		 LIMIT 1`, siteID, NormaliseMAC(mac), at).Scan(&id)
	if err != nil {
		return "", mapErr(err)
	}
	return id, nil
}

// VoucherForSession names the voucher whose `current_session_id` is this session,
// at this venue, and not already revoked. `ErrNotFound` when there is none.
func (r *VoucherRepo) VoucherForSession(ctx context.Context, siteID, sessionID string) (string, error) {
	var id string
	err := r.s.pool.QueryRow(ctx, `
		SELECT id FROM vouchers
		 WHERE site_id = $1 AND current_session_id = $2 AND state <> 'revoked'
		 LIMIT 1`, siteID, sessionID).Scan(&id)
	if err != nil {
		return "", mapErr(err)
	}
	return id, nil
}
