package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// VoucherRepo owns `voucher_batches` and `vouchers`.
type VoucherRepo struct{ s *Store }

const batchColumns = `
	b.id, b.site_id, b.label, b.count, b.created_by,
	b.plan_name, b.plan_name_lang, b.duration_minutes, b.data_allowance_mb,
	b.down_kbps, b.up_kbps, b.enforced_by,
	b.expiry_mode, b.valid_for_hours, b.expires_at, b.created_at,
	count(v.id) FILTER (WHERE v.state = 'unused'),
	count(v.id) FILTER (WHERE v.state = 'in-use'),
	count(v.id) FILTER (WHERE v.state = 'exhausted'),
	count(v.id) FILTER (WHERE v.state = 'expired'),
	count(v.id) FILTER (WHERE v.state = 'revoked')`

func scanBatch(row interface{ Scan(...any) error }) (*VoucherBatch, error) {
	var b VoucherBatch
	err := row.Scan(&b.ID, &b.SiteID, &b.Label, &b.Count, &b.CreatedBy,
		&b.Plan.Name, &b.Plan.NameLang, &b.Plan.DurationMinutes, &b.Plan.DataAllowanceMB,
		&b.Plan.DownKbps, &b.Plan.UpKbps, &b.Plan.EnforcedBy,
		&b.ExpiryMode, &b.ValidForHours, &b.ExpiresAt, &b.CreatedAt,
		&b.Counts.Unused, &b.Counts.InUse, &b.Counts.Exhausted, &b.Counts.Expired, &b.Counts.Revoked)
	if err != nil {
		return nil, mapErr(err)
	}
	return &b, nil
}

// ListBatches returns batches newest first, each with its state histogram.
//
// The counts are aggregated here rather than stored on the batch because a stored
// `used_count` is a number that drifts: a voucher expiring changes the histogram
// and nothing updates a counter for it.
func (r *VoucherRepo) ListBatches(ctx context.Context, opts ListOptions) (Page[VoucherBatch], error) {
	limit := clampLimit(opts.Limit)
	after, err := decodeCursor(opts.Cursor)
	if err != nil {
		return Page[VoucherBatch]{}, err
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT `+batchColumns+`
		FROM voucher_batches b
		LEFT JOIN vouchers v ON v.batch_id = b.id
		WHERE ($1 = '' OR b.site_id = $1)
		  AND ($2 = '' OR b.id < $2)
		  AND ($3 = '' OR b.label ILIKE '%' || $3 || '%')
		GROUP BY b.id
		ORDER BY b.created_at DESC, b.id DESC
		LIMIT $4`, opts.SiteID, after, opts.Query, limit+1)
	if err != nil {
		return Page[VoucherBatch]{}, mapErr(err)
	}
	defer rows.Close()

	var items []VoucherBatch
	for rows.Next() {
		b, err := scanBatch(rows)
		if err != nil {
			return Page[VoucherBatch]{}, err
		}
		items = append(items, *b)
	}
	if err := rows.Err(); err != nil {
		return Page[VoucherBatch]{}, mapErr(err)
	}
	return paginate(items, limit, func(b VoucherBatch) string { return b.ID }), nil
}

// GetBatch returns one batch with its histogram.
func (r *VoucherRepo) GetBatch(ctx context.Context, id string) (*VoucherBatch, error) {
	return scanBatch(r.s.pool.QueryRow(ctx, `
		SELECT `+batchColumns+`
		FROM voucher_batches b
		LEFT JOIN vouchers v ON v.batch_id = b.id
		WHERE b.id = $1
		GROUP BY b.id`, id))
}

// NewVoucher is one code to be issued, with the id it will carry.
type NewVoucher struct {
	ID   string
	Code string
}

// CreateBatch writes the batch and every voucher in it, in one transaction.
//
// One transaction because a half-created batch is a printed sheet of codes that do
// not all work, and the operator has no way to tell which. The codes are generated
// by the caller with crypto/rand -- never here, and never with math/rand.
func (r *VoucherRepo) CreateBatch(ctx context.Context, b *VoucherBatch, vouchers []NewVoucher) error {
	if b.Plan.EnforcedBy == "" {
		b.Plan.EnforcedBy = "unenforced"
	}

	return r.s.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO voucher_batches
				(id, site_id, label, count, created_by, plan_name, plan_name_lang,
				 duration_minutes, data_allowance_mb, down_kbps, up_kbps, enforced_by,
				 expiry_mode, valid_for_hours, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
			RETURNING created_at`,
			b.ID, b.SiteID, b.Label, b.Count, b.CreatedBy, b.Plan.Name, b.Plan.NameLang,
			b.Plan.DurationMinutes, b.Plan.DataAllowanceMB, b.Plan.DownKbps, b.Plan.UpKbps,
			b.Plan.EnforcedBy, b.ExpiryMode, b.ValidForHours, b.ExpiresAt).
			Scan(&b.CreatedAt); err != nil {
			return mapErr(err)
		}

		// from-creation stamps every code now; from-first-use leaves expires_at
		// NULL until redemption, which is why a batch printed in January is still
		// worth selling in March.
		var expiresAt *time.Time
		if b.ExpiryMode == "from-creation" {
			expiresAt = b.ExpiresAt
		}

		// One statement rather than one per code, and `unnest` rather than COPY:
		// PostgreSQL refuses COPY FROM on a table with row-level security, and the
		// tenant seam is not negotiable for a speed-up on a 2000-row maximum.
		ids := make([]string, 0, len(vouchers))
		codes := make([]string, 0, len(vouchers))
		for _, v := range vouchers {
			ids = append(ids, v.ID)
			codes = append(codes, v.Code)
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO vouchers (id, batch_id, site_id, code, state, expires_at)
			SELECT u.id, $3, $4, u.code, 'unused', $5
			FROM unnest($1::text[], $2::text[]) AS u(id, code)`,
			ids, codes, b.ID, b.SiteID, expiresAt)
		return mapErr(err)
	})
}

const voucherColumns = `id, batch_id, site_id, code, state, first_used_at, expires_at, revoked_at, current_session_id, created_at`

func scanVoucher(row interface{ Scan(...any) error }) (*Voucher, error) {
	var v Voucher
	err := row.Scan(&v.ID, &v.BatchID, &v.SiteID, &v.Code, &v.State,
		&v.FirstUsedAt, &v.ExpiresAt, &v.RevokedAt, &v.CurrentSessionID, &v.CreatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &v, nil
}

// VoucherFilter narrows a voucher listing.
type VoucherFilter struct {
	ListOptions
	BatchID string
	State   string
}

// ListVouchers returns codes in issue order.
func (r *VoucherRepo) ListVouchers(ctx context.Context, f VoucherFilter) (Page[Voucher], error) {
	limit := clampLimit(f.Limit)
	after, err := decodeCursor(f.Cursor)
	if err != nil {
		return Page[Voucher]{}, err
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT `+voucherColumns+`
		FROM vouchers
		WHERE ($1 = '' OR site_id = $1)
		  AND ($2 = '' OR batch_id = $2)
		  AND ($3 = '' OR state = $3)
		  AND ($4 = '' OR id > $4)
		  AND ($5 = '' OR code ILIKE '%' || $5 || '%')
		ORDER BY id
		LIMIT $6`, f.SiteID, f.BatchID, f.State, after, f.Query, limit+1)
	if err != nil {
		return Page[Voucher]{}, mapErr(err)
	}
	defer rows.Close()

	var items []Voucher
	for rows.Next() {
		v, err := scanVoucher(rows)
		if err != nil {
			return Page[Voucher]{}, err
		}
		items = append(items, *v)
	}
	if err := rows.Err(); err != nil {
		return Page[Voucher]{}, mapErr(err)
	}
	return paginate(items, limit, func(v Voucher) string { return v.ID }), nil
}

// Get returns one voucher by id.
func (r *VoucherRepo) Get(ctx context.Context, id string) (*Voucher, error) {
	return scanVoucher(r.s.pool.QueryRow(ctx, `SELECT `+voucherColumns+` FROM vouchers WHERE id = $1`, id))
}

// GetByCode is the redemption lookup, and the one the policy engine uses.
//
// The comparison is by equality on an indexed column, which is not constant-time --
// timing here reveals only whether a code exists, and a twelve-character code from a
// 32-symbol alphabet is not brute-forceable through that channel. The constant-time
// requirement in the security rules is about comparing a *submitted secret against a
// stored one*, which is what redemption does after this lookup, not this lookup.
func (r *VoucherRepo) GetByCode(ctx context.Context, code string) (*Voucher, error) {
	return scanVoucher(r.s.pool.QueryRow(ctx, `SELECT `+voucherColumns+` FROM vouchers WHERE code = $1`, code))
}

// Revoke ends a voucher's life.
//
// It refuses to revoke one that is already revoked rather than silently succeeding,
// so that the audit log records one revocation rather than however many times an
// operator pressed the button.
func (r *VoucherRepo) Revoke(ctx context.Context, id string, at time.Time) (*Voucher, error) {
	return scanVoucher(r.s.pool.QueryRow(ctx, `
		UPDATE vouchers
		SET state = 'revoked', revoked_at = $2
		WHERE id = $1 AND state <> 'revoked'
		RETURNING `+voucherColumns, id, at))
}

// Redeem claims a code for a session, transactionally.
//
// Single-use is enforced by the UPDATE's own WHERE clause rather than by reading the
// state and then writing it: two portal requests arriving together both pass a
// read-then-write check, and exactly one wins here.
//
// `from-first-use` expiry is stamped now, which is the whole point of that mode.
func (r *VoucherRepo) Redeem(ctx context.Context, code, sessionID string, at time.Time, validFor *time.Duration) (*Voucher, error) {
	var expiresAt *time.Time
	if validFor != nil {
		t := at.Add(*validFor)
		expiresAt = &t
	}
	return scanVoucher(r.s.pool.QueryRow(ctx, `
		UPDATE vouchers SET
			state              = 'in-use',
			first_used_at      = coalesce(first_used_at, $3),
			expires_at         = coalesce(expires_at, $4),
			current_session_id = $2
		WHERE code = $1
		  AND state IN ('unused', 'in-use')
		  AND (expires_at IS NULL OR expires_at > $3)
		RETURNING `+voucherColumns, code, sessionID, at, expiresAt))
}

// CountRedeemedSince answers the overview's "vouchers redeemed today".
//
// It counts first use rather than current state, because a voucher redeemed this
// morning and exhausted this afternoon was still redeemed today.
func (r *VoucherRepo) CountRedeemedSince(ctx context.Context, siteID string, since time.Time) (int, error) {
	var n int
	err := r.s.pool.QueryRow(ctx, `
		SELECT count(*) FROM vouchers
		WHERE ($1 = '' OR site_id = $1) AND first_used_at >= $2`, siteID, since).Scan(&n)
	return n, mapErr(err)
}

// ExpirePast moves vouchers past their expiry into the expired state.
//
// A voucher is expired because the clock says so, not because something ran, so the
// state column trails reality between sweeps. Everything that authorises checks the
// timestamp as well; this exists so the console's histogram is not a lie.
func (r *VoucherRepo) ExpirePast(ctx context.Context, at time.Time) (int64, error) {
	tag, err := r.s.pool.Exec(ctx, `
		UPDATE vouchers SET state = 'expired'
		WHERE expires_at IS NOT NULL AND expires_at <= $1
		  AND state IN ('unused', 'in-use')`, at)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}
