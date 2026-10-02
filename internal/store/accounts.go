package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// AccountRepo owns `accounts` and `account_nt_hashes`.
//
// The two tables are one aggregate and two very different risks, and the split is
// the single most consequential constraint in this codebase: `accounts` holds an
// argon2id verifier that nothing can reverse, `account_nt_hashes` holds
// password-equivalent material for the identities explicitly opted in to
// PEAP-MSCHAPv2. No method here returns both.
type AccountRepo struct{ s *Store }

const accountColumns = `
	a.id, a.site_id, a.username, a.display_name, a.password_hash, a.enabled,
	a.plan_name, a.plan_name_lang, a.duration_minutes, a.data_allowance_mb,
	a.down_kbps, a.up_kbps, a.enforced_by, a.last_login_at, a.created_at, a.updated_at,
	'argon2id'
		|| CASE WHEN n.account_id IS NULL THEN '' ELSE '+nt' END
		|| CASE WHEN p.account_id IS NULL THEN '' ELSE '+bcrypt' END`

const accountFrom = `FROM accounts a
	LEFT JOIN account_nt_hashes    n ON n.account_id = a.id
	LEFT JOIN account_pap_verifiers p ON p.account_id = a.id`

func scanAccount(row interface{ Scan(...any) error }) (*Account, error) {
	var a Account
	err := row.Scan(&a.ID, &a.SiteID, &a.Username, &a.DisplayName, &a.PasswordHash, &a.Enabled,
		&a.Plan.Name, &a.Plan.NameLang, &a.Plan.DurationMinutes, &a.Plan.DataAllowanceMB,
		&a.Plan.DownKbps, &a.Plan.UpKbps, &a.Plan.EnforcedBy,
		&a.LastLoginAt, &a.CreatedAt, &a.UpdatedAt, &a.CredentialKind)
	if err != nil {
		return nil, mapErr(err)
	}
	return &a, nil
}

// List returns accounts by username.
//
// CredentialKind comes from whether a row exists in the NT-hash table rather than
// from a flag on the account, so the console cannot show `argon2id` for an identity
// that is in fact reachable by MSCHAPv2. A flag can be wrong; a LEFT JOIN cannot.
func (r *AccountRepo) List(ctx context.Context, opts ListOptions) (Page[Account], error) {
	limit := clampLimit(opts.Limit)
	after, err := decodeCursor(opts.Cursor)
	if err != nil {
		return Page[Account]{}, err
	}

	rows, err := r.s.pool.Query(ctx, `
		SELECT `+accountColumns+` `+accountFrom+`
		WHERE ($1 = '' OR a.site_id = $1)
		  AND ($2 = '' OR a.id > $2)
		  AND ($3 = '' OR a.username ILIKE '%' || $3 || '%' OR coalesce(a.display_name, '') ILIKE '%' || $3 || '%')
		ORDER BY a.id
		LIMIT $4`, opts.SiteID, after, opts.Query, limit+1)
	if err != nil {
		return Page[Account]{}, mapErr(err)
	}
	defer rows.Close()

	var items []Account
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return Page[Account]{}, err
		}
		items = append(items, *a)
	}
	if err := rows.Err(); err != nil {
		return Page[Account]{}, mapErr(err)
	}
	return paginate(items, limit, func(a Account) string { return a.ID }), nil
}

// Get returns one account.
func (r *AccountRepo) Get(ctx context.Context, id string) (*Account, error) {
	return scanAccount(r.s.pool.QueryRow(ctx, `SELECT `+accountColumns+` `+accountFrom+` WHERE a.id = $1`, id))
}

// GetByUsername resolves a guest login within a site.
func (r *AccountRepo) GetByUsername(ctx context.Context, siteID, username string) (*Account, error) {
	return scanAccount(r.s.pool.QueryRow(ctx,
		`SELECT `+accountColumns+` `+accountFrom+` WHERE a.site_id = $1 AND a.username = $2`, siteID, username))
}

// Create adds an account. The caller supplies an argon2id hash; a cleartext
// password never reaches this package.
func (r *AccountRepo) Create(ctx context.Context, a *Account) error {
	if a.Plan.EnforcedBy == "" {
		a.Plan.EnforcedBy = "unenforced"
	}
	err := r.s.pool.QueryRow(ctx, `
		INSERT INTO accounts
			(id, site_id, username, display_name, password_hash, enabled,
			 plan_name, plan_name_lang, duration_minutes, data_allowance_mb,
			 down_kbps, up_kbps, enforced_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING created_at, updated_at`,
		a.ID, a.SiteID, a.Username, a.DisplayName, a.PasswordHash, a.Enabled,
		a.Plan.Name, a.Plan.NameLang, a.Plan.DurationMinutes, a.Plan.DataAllowanceMB,
		a.Plan.DownKbps, a.Plan.UpKbps, a.Plan.EnforcedBy).
		Scan(&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return mapErr(err)
	}
	a.CredentialKind = "argon2id"
	return nil
}

// AccountPatch is the mutable part of an account. A nil field is unchanged.
type AccountPatch struct {
	DisplayName **string
	Enabled     *bool
	Plan        *Plan
	// PasswordHash is an argon2id verifier. Never a password.
	PasswordHash *string
}

// Update applies a patch.
func (r *AccountRepo) Update(ctx context.Context, id string, p AccountPatch) (*Account, error) {
	var displayName *string
	setName := p.DisplayName != nil
	if setName {
		displayName = *p.DisplayName
	}
	var plan Plan
	setPlan := p.Plan != nil
	if setPlan {
		plan = *p.Plan
	}

	if _, err := r.s.pool.Exec(ctx, `
		UPDATE accounts SET
			display_name      = CASE WHEN $2 THEN $3 ELSE display_name END,
			enabled           = coalesce($4, enabled),
			password_hash     = coalesce($5, password_hash),
			plan_name         = CASE WHEN $6 THEN $7  ELSE plan_name END,
			plan_name_lang    = CASE WHEN $6 THEN $8  ELSE plan_name_lang END,
			duration_minutes  = CASE WHEN $6 THEN $9  ELSE duration_minutes END,
			data_allowance_mb = CASE WHEN $6 THEN $10 ELSE data_allowance_mb END,
			down_kbps         = CASE WHEN $6 THEN $11 ELSE down_kbps END,
			up_kbps           = CASE WHEN $6 THEN $12 ELSE up_kbps END,
			enforced_by       = CASE WHEN $6 THEN $13 ELSE enforced_by END,
			updated_at        = now()
		WHERE id = $1`,
		id, setName, displayName, p.Enabled, p.PasswordHash,
		setPlan, plan.Name, plan.NameLang, plan.DurationMinutes, plan.DataAllowanceMB,
		plan.DownKbps, plan.UpKbps, nonEmpty(plan.EnforcedBy, "unenforced")); err != nil {
		return nil, mapErr(err)
	}
	return r.Get(ctx, id)
}

// Delete removes an account and, by cascade, any NT hash it had.
func (r *AccountRepo) Delete(ctx context.Context, id string) error {
	tag, err := r.s.pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, id)
	if err != nil {
		return mapErr(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RecordLogin stamps a successful guest authentication.
func (r *AccountRepo) RecordLogin(ctx context.Context, id string, at time.Time) error {
	_, err := r.s.pool.Exec(ctx, `UPDATE accounts SET last_login_at = $2 WHERE id = $1`, id, at)
	return mapErr(err)
}

// ---------------------------------------------------------------------------
// NT hashes -- their own methods, because they are their own risk.
// ---------------------------------------------------------------------------

// OptInToMSCHAPv2 stores an NT hash for an account.
//
// The name is the API. There is no SetNTHash, because the caller is not merely
// storing a value: an NT hash is unsalted MD4 and is the sole input to the MSCHAPv2
// response, so a stolen one authenticates directly. Writing it is an explicit
// decision to make this identity reachable by a protocol that cannot use argon2,
// and the method says so.
//
// optedInBy names the operator who made that decision, because "who turned this on"
// is the first question after an incident.
func (r *AccountRepo) OptInToMSCHAPv2(ctx context.Context, accountID string, ntHash []byte, optedInBy *string) error {
	if len(ntHash) == 0 {
		return ErrSecretUnset
	}
	sealed, err := r.s.cfg.Keys.Seal(ntHash, SecretContext("account_nt_hashes", "nt_hash", accountID))
	if err != nil {
		return err
	}
	_, err = r.s.pool.Exec(ctx, `
		INSERT INTO account_nt_hashes (account_id, nt_hash, opted_in_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (account_id) DO UPDATE SET
			nt_hash = EXCLUDED.nt_hash,
			opted_in_at = now(),
			opted_in_by = EXCLUDED.opted_in_by`,
		accountID, sealed, optedInBy)
	return mapErr(err)
}

// OptOutOfMSCHAPv2 removes the NT hash, which is what takes the identity back out
// of MSCHAPv2's reach.
func (r *AccountRepo) OptOutOfMSCHAPv2(ctx context.Context, accountID string) error {
	_, err := r.s.pool.Exec(ctx, `DELETE FROM account_nt_hashes WHERE account_id = $1`, accountID)
	return mapErr(err)
}

// NTHash unseals the stored hash.
//
// It exists for the credential view FreeRADIUS reads via rlm_sql, and for nothing
// else. The Policy API must never return this over rlm_rest: that would turn an
// authorization endpoint into one that yields the password verifier for any
// username supplied, which is a credential-store dump without touching PostgreSQL.
func (r *AccountRepo) NTHash(ctx context.Context, accountID string) ([]byte, error) {
	var sealed []byte
	err := r.s.pool.QueryRow(ctx,
		`SELECT nt_hash FROM account_nt_hashes WHERE account_id = $1`, accountID).Scan(&sealed)
	if err != nil {
		return nil, mapErr(err)
	}
	return r.s.cfg.Keys.Open(sealed, SecretContext("account_nt_hashes", "nt_hash", accountID))
}

// ---------------------------------------------------------------------------
// The 802.1X verifier -- its own methods, because it is its own decision.
// ---------------------------------------------------------------------------

// ErrPAPUsernameTaken is the refusal that keeps one site's password from
// authenticating another site's identity.
//
// `account_pap_verifiers.username` is globally unique because FreeRADIUS's
// identity namespace is flat: an 802.1X supplicant sends a username and there is
// no site in the packet. The refusal is the honest cost of that, and — the one
// thing a flat namespace buys — it can name what it collided with.
var ErrPAPUsernameTaken = errors.New("store: another opted-in identity already uses this 802.1X username")

// OptInTo8021X stores the bcrypt verifier that makes an identity reachable by
// EAP-TTLS/PAP.
//
// The name is the API, exactly as OptInToMSCHAPv2's is. The caller is not storing
// a value; it is deciding that this identity may authenticate to the network with
// a verifier that is NOT argon2id, and `docs/rules/security.md` §1 asks that
// such a decision be explicit rather than implied by a column.
//
// The verifier arrives already hashed. `internal/store` never sees a cleartext
// password — its CONTRACT.md says so — and `identity.HashPAPVerifier` is where the
// bcrypt cost is decided once instead of per call site.
func (r *AccountRepo) OptInTo8021X(ctx context.Context, accountID, verifier string, optedInBy *string) error {
	if verifier == "" {
		return ErrSecretUnset
	}
	// The username is not a parameter: it is read from the account inside the same
	// statement, so this method cannot be handed a username belonging to somebody
	// else. The composite foreign key would refuse that anyway; this makes the
	// refusal unreachable rather than merely certain.
	_, err := r.s.pool.Exec(ctx, `
		INSERT INTO account_pap_verifiers (account_id, username, verifier, opted_in_by)
		SELECT a.id, a.username, $2, $3 FROM accounts a WHERE a.id = $1
		ON CONFLICT (account_id) DO UPDATE SET
			verifier    = EXCLUDED.verifier,
			opted_in_at = now(),
			opted_in_by = EXCLUDED.opted_in_by`,
		accountID, verifier, optedInBy)
	if err == nil {
		return nil
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "account_pap_verifiers_username_is_global" {
		return fmt.Errorf("%w: %s", ErrPAPUsernameTaken, r.whoHolds8021XUsername(ctx, accountID))
	}
	return mapErr(err)
}

// whoHolds8021XUsername answers the only question an operator has when the
// refusal above fires, and answers it best-effort: a description of the conflict
// is not worth failing the refusal over.
func (r *AccountRepo) whoHolds8021XUsername(ctx context.Context, accountID string) string {
	var site, username string
	err := r.s.pool.QueryRow(ctx, `
		SELECT other.site_id, other.username
		FROM accounts a
		JOIN account_pap_verifiers v ON v.username = a.username
		JOIN accounts other ON other.id = v.account_id
		WHERE a.id = $1`, accountID).Scan(&site, &username)
	if err != nil {
		return "held by another account"
	}
	return fmt.Sprintf("%q is already enabled for 802.1X in site %s", username, site)
}

// OptOutOf8021X removes the verifier, which is what takes the identity back out
// of EAP-TTLS/PAP's reach. The argon2id verifier is untouched, so the portal and
// the console go on authenticating this identity exactly as before.
func (r *AccountRepo) OptOutOf8021X(ctx context.Context, accountID string) error {
	_, err := r.s.pool.Exec(ctx, `DELETE FROM account_pap_verifiers WHERE account_id = $1`, accountID)
	return mapErr(err)
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
