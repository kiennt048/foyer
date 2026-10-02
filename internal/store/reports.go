package store

import (
	"context"
	"errors"
	"time"
)

// The `reports` reads: three streamed scans and the trend aggregates.
//
// They are methods on the repositories that own the tables rather than a fourth
// repository, because the column lists, the scanners and — for `radacct` — the
// longest-prefix client join and the session-state rule are those repositories'
// and must be known in exactly one place. A report that re-derived `sessionState`
// would be the second copy `docs/rules/database.md` names.
//
// **Every scan is a visitor over a server-side cursor.** pgx reads rows off the
// wire as `rows.Next()` asks for them, so a quarter of `radacct` is never in this
// process at once. The visitor's error stops the scan and closes the cursor.
//
// **Read-only, like the rest of this package's view of `radacct`.** The
// application's role holds SELECT on it and nothing else; nothing here could write
// it if it tried.

// ErrStopScan is what a visitor may return to end a scan early without it being
// reported as a failure. Nothing in this repository returns it today; it exists so
// a caller that wants the first N rows does not have to invent an error.
var ErrStopScan = errors.New("store: scan stopped by the visitor")

// SessionsStartedBetween visits the site's sessions with `acctstarttime` in
// `[from, to)`, oldest first, ties by `acctuniqueid`.
//
// `acctstarttime` is indexed since migration 00002 (`radacct_starttime_idx`), so
// the range is an index scan and not a walk of the table. The two tables Foyer owns
// gained their range indexes in 00024.
func (r *AccountingRepo) SessionsStartedBetween(ctx context.Context, siteID string, from, to time.Time, visit func(Session) error) error {
	rows, err := r.s.pool.Query(ctx, `
		SELECT `+sessionColumns+`
		FROM radacct ra`+clientForNAS+calledStationParts("ra")+soleAccessPoint+`
		WHERE rc.site_id = $1
		  AND ra.acctstarttime >= $2 AND ra.acctstarttime < $3
		ORDER BY ra.acctstarttime, ra.acctuniqueid`, siteID, from, to)
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		s, err := r.scanSession(rows)
		if err != nil {
			return err
		}
		if err := visit(*s); err != nil {
			return stopped(err)
		}
	}
	return mapErr(rows.Err())
}

// VouchersActiveBetween visits the site's vouchers created **or** first used in
// `[from, to)`, by creation, oldest first.
func (r *VoucherRepo) VouchersActiveBetween(ctx context.Context, siteID string, from, to time.Time, visit func(Voucher) error) error {
	rows, err := r.s.pool.Query(ctx, `
		SELECT `+voucherColumns+`
		FROM vouchers
		WHERE site_id = $1
		  AND ((created_at >= $2 AND created_at < $3)
		    OR (first_used_at >= $2 AND first_used_at < $3))
		ORDER BY created_at, id`, siteID, from, to)
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		v, err := scanVoucher(rows)
		if err != nil {
			return err
		}
		if err := visit(*v); err != nil {
			return stopped(err)
		}
	}
	return mapErr(rows.Err())
}

// GrantWithAccount is a grant and, for an account grant, the account's username.
//
// The username is joined at read time rather than stored on the grant, and an
// account deleted since reads as nil: the export says *an account*, and not whose.
type GrantWithAccount struct {
	PortalGrant
	AccountUsername *string
}

// GrantsBetween visits the site's grants made in `[from, to)`, oldest first.
func (r *GrantRepo) GrantsBetween(ctx context.Context, siteID string, from, to time.Time, visit func(GrantWithAccount) error) error {
	rows, err := r.s.pool.Query(ctx, `
		SELECT g.id, g.site_id, g.nas_id, g.client_mac::text, g.ap_mac::text, g.method,
		       g.voucher_id, g.account_id, g.plan_name, g.plan_name_lang, g.duration_minutes,
		       g.data_allowance_mb, g.up_kbps, g.down_kbps, g.enforced_by, g.granted_at,
		       g.expires_at, g.revoked_at, g.delivery, a.username
		FROM portal_grants g
		LEFT JOIN accounts a ON a.id = g.account_id
		WHERE g.site_id = $1 AND g.granted_at >= $2 AND g.granted_at < $3
		ORDER BY g.granted_at, g.id`, siteID, from, to)
	if err != nil {
		return mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var g GrantWithAccount
		err := rows.Scan(&g.ID, &g.SiteID, &g.NASID, &g.ClientMAC, &g.APMAC, &g.Method,
			&g.VoucherID, &g.AccountID, &g.Plan.Name, &g.Plan.NameLang,
			&g.Plan.DurationMinutes, &g.Plan.DataAllowanceMB, &g.Plan.UpKbps, &g.Plan.DownKbps,
			&g.Plan.EnforcedBy, &g.GrantedAt, &g.ExpiresAt, &g.RevokedAt, &g.Delivery,
			&g.AccountUsername)
		if err != nil {
			return mapErr(err)
		}
		if err := visit(g); err != nil {
			return stopped(err)
		}
	}
	return mapErr(rows.Err())
}

func stopped(err error) error {
	if errors.Is(err, ErrStopScan) {
		return nil
	}
	return err
}

// ── trends ───────────────────────────────────────────────────────────────────

// TrendBucket is one bucket's counts, in the order the boundaries were given.
type TrendBucket struct {
	GuestDevices     int
	VouchersRedeemed int
	// Bytes is the sum of the reconstructed totals — the generated accounting
	// query already folds Gigawords into the octet columns — over the sessions
	// that started in the bucket and reported a counter.
	Bytes int64
	// Wrapped is true when a counted total exceeded 2^32, i.e. the same test
	// `scanSession` applies to set `CountersWrapped`.
	Wrapped bool
	// OldestOpenUpdate is the least recent `coalesce(acctupdatetime,
	// acctstarttime)` over the **open** counted sessions, and nil when there is
	// none. A closed session's counter is its final report and has no sample age.
	OldestOpenUpdate *time.Time
}

// Trends answers one TrendBucket per `[starts[i], ends[i])`.
//
// Three statements, each a `LEFT JOIN` from the bucket list, so a bucket nothing
// happened in is a row of zeros rather than a missing row — a missing bar reads as
// a day nobody measured.
//
// `guestDevices` is the **union** of accounting MACs and grant MACs, deduplicated
// per bucket on the MAC's twelve hex digits: Calling-Station-Id has no canonical
// spelling, and one phone counted twice because two tables spelled it differently
// is two guests who are one. A value that does not normalise to twelve digits is
// not a MAC and is not counted.
func (r *AccountingRepo) Trends(ctx context.Context, siteID string, starts, ends []time.Time) ([]TrendBucket, error) {
	if len(starts) != len(ends) {
		return nil, errors.New("store: trend bucket bounds are not paired")
	}
	out := make([]TrendBucket, len(starts))
	if len(starts) == 0 {
		return out, nil
	}
	lo, hi := starts[0], ends[len(ends)-1]

	const buckets = `
		WITH b AS (
			SELECT i, s, e
			FROM unnest($2::timestamptz[], $3::timestamptz[]) WITH ORDINALITY AS t(s, e, i)
		)`

	// Devices.
	rows, err := r.s.pool.Query(ctx, buckets+`,
		macs AS (
			SELECT ra.acctstarttime AS at,
			       lower(regexp_replace(coalesce(ra.callingstationid, ''), '[^0-9A-Fa-f]', '', 'g')) AS mac
			FROM radacct ra`+clientForNAS+`
			WHERE rc.site_id = $1 AND ra.acctstarttime >= $4 AND ra.acctstarttime < $5
			UNION ALL
			SELECT g.granted_at, replace(g.client_mac::text, ':', '')
			FROM portal_grants g
			WHERE g.site_id = $1 AND g.granted_at >= $4 AND g.granted_at < $5
		)
		SELECT b.i, count(DISTINCT m.mac)
		FROM b LEFT JOIN macs m ON m.at >= b.s AND m.at < b.e AND length(m.mac) = 12
		GROUP BY b.i ORDER BY b.i`, siteID, starts, ends, lo, hi)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := fill(rows, out, func(t *TrendBucket, n int64) { t.GuestDevices = int(n) }); err != nil {
		return nil, err
	}

	// Bytes, the wrap, and the oldest open sample.
	rows, err = r.s.pool.Query(ctx, buckets+`,
		counted AS (
			SELECT ra.acctstarttime AS at, ra.acctstoptime IS NULL AS open,
			       coalesce(ra.acctupdatetime, ra.acctstarttime) AS heard,
			       coalesce(ra.acctinputoctets, 0) + coalesce(ra.acctoutputoctets, 0) AS octets,
			       coalesce(ra.acctinputoctets, 0) >= 4294967296
			         OR coalesce(ra.acctoutputoctets, 0) >= 4294967296 AS wrapped
			FROM radacct ra`+clientForNAS+`
			WHERE rc.site_id = $1 AND ra.acctstarttime >= $4 AND ra.acctstarttime < $5
			  AND (ra.acctinputoctets IS NOT NULL OR ra.acctoutputoctets IS NOT NULL)
		)
		SELECT b.i, coalesce(sum(c.octets), 0)::bigint, coalesce(bool_or(c.wrapped), false),
		       min(c.heard) FILTER (WHERE c.open)
		FROM b LEFT JOIN counted c ON c.at >= b.s AND c.at < b.e
		GROUP BY b.i ORDER BY b.i`, siteID, starts, ends, lo, hi)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		var i int64
		var t TrendBucket
		if err := rows.Scan(&i, &t.Bytes, &t.Wrapped, &t.OldestOpenUpdate); err != nil {
			return nil, mapErr(err)
		}
		if i < 1 || int(i) > len(out) {
			return nil, errors.New("store: a trend row for a bucket that was not asked for")
		}
		out[i-1].Bytes, out[i-1].Wrapped, out[i-1].OldestOpenUpdate = t.Bytes, t.Wrapped, t.OldestOpenUpdate
	}
	if err := rows.Err(); err != nil {
		return nil, mapErr(err)
	}

	// Vouchers redeemed: first use in the bucket, `CountRedeemedSince`'s rule.
	rows, err = r.s.pool.Query(ctx, buckets+`
		SELECT b.i, count(v.id)
		FROM b LEFT JOIN vouchers v
		  ON v.site_id = $1 AND v.first_used_at >= b.s AND v.first_used_at < b.e
		GROUP BY b.i ORDER BY b.i`, siteID, starts, ends)
	if err != nil {
		return nil, mapErr(err)
	}
	if err := fill(rows, out, func(t *TrendBucket, n int64) { t.VouchersRedeemed = int(n) }); err != nil {
		return nil, err
	}
	return out, nil
}

// fill reads `(bucket ordinal, count)` rows into out.
func fill(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
	Close()
}, out []TrendBucket, set func(*TrendBucket, int64)) error {
	defer rows.Close()
	for rows.Next() {
		var i, n int64
		if err := rows.Scan(&i, &n); err != nil {
			return mapErr(err)
		}
		if i < 1 || int(i) > len(out) {
			return errors.New("store: a trend row for a bucket that was not asked for")
		}
		set(&out[i-1], n)
	}
	return mapErr(rows.Err())
}
