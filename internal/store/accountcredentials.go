package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// An account's two verifiers, changed together.
//
// `OptInTo8021X` and `Update{PasswordHash}` already exist and each is one
// statement. The console's operation changes a password and the 802.1X opt-in in
// one request (`updateAccount`), and doing that as two statements leaves a window
// -- and a failure mode -- in which the portal answers to the new password and the
// network to the old one, or a refusal of the second half (the 802.1X username is
// taken at another venue) is reported after the first half already happened. So
// these run in one transaction, and a refusal changes nothing.

// CredentialChange is what one write does to an account's verifiers. Every field
// is optional; the zero value changes nothing.
type CredentialChange struct {
	// PasswordHash is a new argon2id verifier. Never a password.
	PasswordHash *string
	// PAPVerifier is a new bcrypt verifier: the identity is opted in to 802.1X
	// (EAP-TTLS/PAP), or its 802.1X verifier is replaced.
	PAPVerifier *string
	// OptOut8021X removes the bcrypt verifier. Exclusive with PAPVerifier.
	OptOut8021X bool
	// By names the operator who made the change.
	By *string
}

// CreateWithCredentials inserts the account and, when papVerifier is set, its
// 802.1X verifier, in one transaction: an account whose opt-in was refused is not
// created.
func (r *AccountRepo) CreateWithCredentials(ctx context.Context, a *Account, papVerifier string, by *string) error {
	if papVerifier == "" {
		return r.Create(ctx, a)
	}
	if a.Plan.EnforcedBy == "" {
		a.Plan.EnforcedBy = "unenforced"
	}
	err := r.s.tx(ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `
			INSERT INTO accounts
				(id, site_id, username, display_name, password_hash, enabled,
				 plan_name, plan_name_lang, duration_minutes, data_allowance_mb,
				 down_kbps, up_kbps, enforced_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			RETURNING created_at, updated_at`,
			a.ID, a.SiteID, a.Username, a.DisplayName, a.PasswordHash, a.Enabled,
			a.Plan.Name, a.Plan.NameLang, a.Plan.DurationMinutes, a.Plan.DataAllowanceMB,
			a.Plan.DownKbps, a.Plan.UpKbps, a.Plan.EnforcedBy).
			Scan(&a.CreatedAt, &a.UpdatedAt); err != nil {
			return err
		}
		return optIn8021XTx(ctx, tx, a.ID, papVerifier, by)
	})
	if err != nil {
		return r.papRefusal(ctx, err, "", a.Username)
	}
	a.CredentialKind = "argon2id+bcrypt"
	return nil
}

// SetCredentials applies a CredentialChange in one transaction and returns the
// account as it now stands. ErrNotFound for an unknown account; ErrPAPUsernameTaken
// when another venue's identity holds this 802.1X username.
func (r *AccountRepo) SetCredentials(ctx context.Context, id string, c CredentialChange) (*Account, error) {
	if c.PAPVerifier != nil && c.OptOut8021X {
		return nil, errors.New("store: an 802.1X opt-in and opt-out in one change")
	}
	if c.PAPVerifier != nil && *c.PAPVerifier == "" {
		return nil, ErrSecretUnset
	}
	err := r.s.tx(ctx, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE accounts SET password_hash = coalesce($2, password_hash), updated_at = now()
			 WHERE id = $1`, id, c.PasswordHash)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		switch {
		case c.PAPVerifier != nil:
			return optIn8021XTx(ctx, tx, id, *c.PAPVerifier, c.By)
		case c.OptOut8021X:
			_, err := tx.Exec(ctx, `DELETE FROM account_pap_verifiers WHERE account_id = $1`, id)
			return err
		}
		return nil
	})
	if err != nil {
		return nil, r.papRefusal(ctx, err, id, "")
	}
	return r.Get(ctx, id)
}

// Has8021X reports whether the account holds a bcrypt verifier. ErrNotFound for an
// unknown account.
func (r *AccountRepo) Has8021X(ctx context.Context, id string) (bool, error) {
	var has bool
	err := r.s.pool.QueryRow(ctx, `
		SELECT p.account_id IS NOT NULL FROM accounts a
		LEFT JOIN account_pap_verifiers p ON p.account_id = a.id WHERE a.id = $1`, id).Scan(&has)
	return has, mapErr(err)
}

func optIn8021XTx(ctx context.Context, tx pgx.Tx, accountID, verifier string, by *string) error {
	// The username is read from the account in the same statement, as OptInTo8021X
	// does, so this cannot be handed somebody else's.
	_, err := tx.Exec(ctx, `
		INSERT INTO account_pap_verifiers (account_id, username, verifier, opted_in_by)
		SELECT a.id, a.username, $2, $3 FROM accounts a WHERE a.id = $1
		ON CONFLICT (account_id) DO UPDATE SET
			verifier    = EXCLUDED.verifier,
			opted_in_at = now(),
			opted_in_by = EXCLUDED.opted_in_by`,
		accountID, verifier, by)
	return err
}

// papRefusal names the global-username collision after the transaction has rolled
// back (an aborted transaction answers no further query), and maps the rest.
func (r *AccountRepo) papRefusal(ctx context.Context, err error, accountID, username string) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "account_pap_verifiers_username_is_global" {
		who := "held by another account"
		if accountID != "" {
			who = r.whoHolds8021XUsername(ctx, accountID)
		} else if username != "" {
			var site string
			if r.s.pool.QueryRow(ctx, `
				SELECT a.site_id FROM account_pap_verifiers v JOIN accounts a ON a.id = v.account_id
				 WHERE v.username = $1`, username).Scan(&site) == nil {
				who = fmt.Sprintf("%q is already enabled for 802.1X in site %s", username, site)
			}
		}
		return fmt.Errorf("%w: %s", ErrPAPUsernameTaken, who)
	}
	return mapErr(err)
}
