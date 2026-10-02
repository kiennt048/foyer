package store

import (
	"context"
	"time"
)

// OperatorRepo owns `operators`.
type OperatorRepo struct{ s *Store }

const operatorColumns = `id, username, display_name, password_hash, role, enabled, must_change_password, last_login_at, created_at, updated_at`

func scanOperator(row interface{ Scan(...any) error }) (*Operator, error) {
	var o Operator
	err := row.Scan(&o.ID, &o.Username, &o.DisplayName, &o.PasswordHash, &o.Role,
		&o.Enabled, &o.MustChangePassword, &o.LastLoginAt, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		return nil, mapErr(err)
	}
	return &o, nil
}

// List returns operators newest first. Operators are deployment-wide rather than
// site-scoped, so there is no site filter here and the contract has none either.
func (r *OperatorRepo) List(ctx context.Context, opts ListOptions) (Page[Operator], error) {
	limit := clampLimit(opts.Limit)
	after, err := decodeCursor(opts.Cursor)
	if err != nil {
		return Page[Operator]{}, err
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT `+operatorColumns+`
		FROM operators
		WHERE ($1 = '' OR username > $1)
		  AND ($2 = '' OR username ILIKE '%' || $2 || '%' OR coalesce(display_name, '') ILIKE '%' || $2 || '%')
		ORDER BY username
		LIMIT $3`, after, opts.Query, limit+1)
	if err != nil {
		return Page[Operator]{}, mapErr(err)
	}
	defer rows.Close()

	var items []Operator
	for rows.Next() {
		o, err := scanOperator(rows)
		if err != nil {
			return Page[Operator]{}, err
		}
		items = append(items, *o)
	}
	if err := rows.Err(); err != nil {
		return Page[Operator]{}, mapErr(err)
	}
	return paginate(items, limit, func(o Operator) string { return o.Username }), nil
}

// Get returns one operator by id.
func (r *OperatorRepo) Get(ctx context.Context, id string) (*Operator, error) {
	return scanOperator(r.s.pool.QueryRow(ctx,
		`SELECT `+operatorColumns+` FROM operators WHERE id = $1`, id))
}

// GetByUsername is the sign-in lookup.
//
// It returns a disabled operator rather than hiding one, because the caller must
// still verify the password before answering: an endpoint that answers faster for a
// disabled account than a wrong one has told an attacker which usernames exist.
func (r *OperatorRepo) GetByUsername(ctx context.Context, username string) (*Operator, error) {
	return scanOperator(r.s.pool.QueryRow(ctx,
		`SELECT `+operatorColumns+` FROM operators WHERE username = $1`, username))
}

// Create adds an operator. The caller hashes the password -- argon2id, in
// internal/identity -- because this package must never be a place a cleartext
// password passes through.
func (r *OperatorRepo) Create(ctx context.Context, o *Operator) error {
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO operators (id, username, display_name, password_hash, role, enabled, must_change_password)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at, updated_at`,
		o.ID, o.Username, o.DisplayName, o.PasswordHash, o.Role, o.Enabled, o.MustChangePassword).
		Scan(&o.CreatedAt, &o.UpdatedAt)
	return mapErr(err)
}

// OperatorPatch is the mutable part of an operator. A nil field is unchanged.
type OperatorPatch struct {
	DisplayName **string
	Role        *string
	Enabled     *bool
}

// Update applies a patch.
func (r *OperatorRepo) Update(ctx context.Context, id string, p OperatorPatch) (*Operator, error) {
	var displayName *string
	setName := p.DisplayName != nil
	if setName {
		displayName = *p.DisplayName
	}

	return scanOperator(r.s.pool.QueryRow(ctx, `
		UPDATE operators SET
			display_name = CASE WHEN $2 THEN $3 ELSE display_name END,
			role         = coalesce($4, role),
			enabled      = coalesce($5, enabled),
			updated_at   = now()
		WHERE id = $1
		RETURNING `+operatorColumns,
		id, setName, displayName, p.Role, p.Enabled))
}

// SetPassword replaces the argon2id verifier, and clears must_change_password in
// the same statement.
//
// **One statement rather than two, and it is the same argument `docs/rules/security.md` §9
// makes about single-use vouchers.** A change that succeeded and a flag that did not
// clear is a console that refuses every screen after the operator has done exactly
// what it asked -- with no way out, because the change operation would then be asked
// for a `currentPassword` that is no longer current. Two statements can produce that
// state; one cannot.
//
// It clears unconditionally rather than only when set. There is no operation that
// turns the flag on, so "clear a flag that was already false" is a no-op on every
// row it can reach, and a conditional would be a branch nothing exercises.
func (r *OperatorRepo) SetPassword(ctx context.Context, id, passwordHash string) error {
	tag, err := r.s.pool.Exec(ctx,
		`UPDATE operators SET password_hash = $2, must_change_password = false, updated_at = now() WHERE id = $1`, id, passwordHash)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Delete removes an operator and, by cascade, their console sessions.
func (r *OperatorRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM operators WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CountOtherEnabledAdministrators answers the one question that gates deleting or
// demoting an operator: would this leave the deployment with nobody who can
// administer it.
//
// It has to be asked, because there is no password-reset channel and no recovery
// short of the Go binary's first-boot path. The contract has a refusal type for
// exactly this -- /problems/last-administrator -- and it is the only refusal in the
// set that protects the deployment from its own operator rather than the other way
// round.
func (r *OperatorRepo) CountOtherEnabledAdministrators(ctx context.Context, excludeID string) (int, error) {
	var n int
	err := r.s.pool.QueryRow(ctx, `
		SELECT count(*) FROM operators
		WHERE role = 'administrator' AND enabled AND id <> $1`, excludeID).Scan(&n)
	return n, mapErr(err)
}

// ---------------------------------------------------------------------------

// OperatorSessionRepo owns `operator_sessions` -- the console's own sign-in state.
type OperatorSessionRepo struct{ s *Store }

// Create persists a session. tokenHash is a hash of the cookie value; the value
// itself never reaches this package.
func (r *OperatorSessionRepo) Create(ctx context.Context, sess *OperatorSession) error {
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO operator_sessions (token_hash, operator_id, csrf_token, user_agent, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at`,
		sess.TokenHash, sess.OperatorID, sess.CSRFToken, sess.UserAgent, sess.ExpiresAt).
		Scan(&sess.CreatedAt)
	return mapErr(err)
}

// Resolve returns the session and its operator, or ErrNotFound.
//
// Expiry and the operator's enabled flag are both in the WHERE clause rather than
// checked afterwards: a disabled operator's live session stops working on the next
// request, which is what an administrator disabling an account expects to have
// bought.
func (r *OperatorSessionRepo) Resolve(ctx context.Context, tokenHash string) (*OperatorSession, *Operator, error) {
	var sess OperatorSession
	var op Operator
	err := r.s.pool.QueryRow(ctx, `
		SELECT s.token_hash, s.operator_id, s.csrf_token, s.user_agent, s.created_at, s.expires_at,
		       o.id, o.username, o.display_name, o.password_hash, o.role, o.enabled,
		       o.last_login_at, o.created_at, o.updated_at
		FROM operator_sessions s
		JOIN operators o ON o.id = s.operator_id
		WHERE s.token_hash = $1 AND s.expires_at > now() AND o.enabled`, tokenHash).
		Scan(&sess.TokenHash, &sess.OperatorID, &sess.CSRFToken, &sess.UserAgent, &sess.CreatedAt, &sess.ExpiresAt,
			&op.ID, &op.Username, &op.DisplayName, &op.PasswordHash, &op.Role, &op.Enabled,
			&op.LastLoginAt, &op.CreatedAt, &op.UpdatedAt)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	return &sess, &op, nil
}

// Delete signs one session out.
func (r *OperatorSessionRepo) Delete(ctx context.Context, tokenHash string) error {
	_, err := r.s.pool.Exec(ctx, `DELETE FROM operator_sessions WHERE token_hash = $1`, tokenHash)
	return mapErr(err)
}

// DeleteForOperator signs every session of one operator out. A password change
// calls it: leaving old sessions live would mean a changed password revoked
// nothing.
func (r *OperatorSessionRepo) DeleteForOperator(ctx context.Context, operatorID string) error {
	_, err := r.s.pool.Exec(ctx, `DELETE FROM operator_sessions WHERE operator_id = $1`, operatorID)
	return mapErr(err)
}

// DeleteExpired removes sessions past their expiry.
func (r *OperatorSessionRepo) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM operator_sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, mapErr(err)
	}
	return tag.RowsAffected(), nil
}

// RecordLogin stamps a successful sign-in.
func (r *OperatorRepo) RecordLogin(ctx context.Context, id string, at time.Time) error {
	_, err := r.s.pool.Exec(ctx,
		`UPDATE operators SET last_login_at = $2, updated_at = now() WHERE id = $1`, id, at)
	return mapErr(err)
}

// paginate trims an over-fetched slice to the page size and derives the cursor from
// whatever the caller sorted by.
//
// The over-fetch of one row is what makes nextCursor honest: without it, a page
// that happens to end exactly on the limit reports a cursor pointing at nothing,
// and the console renders a "load more" that loads nothing.
func paginate[T any](items []T, limit int, key func(T) string) Page[T] {
	if len(items) > limit {
		items = items[:limit]
		return Page[T]{Items: items, NextCursor: encodeCursor(key(items[len(items)-1]))}
	}
	return Page[T]{Items: items}
}
