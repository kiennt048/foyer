package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// ConsoleSetupRepo owns `console_setup` (migration 00040): the console's
// first-access setup, which replaced the shipped administrator password.
// `internal/api/handlers_setup.go` has the survey and the decision.
type ConsoleSetupRepo struct{ s *Store }

// ConsoleSetup is constructed per call, so this aggregate adds a file rather than
// a field to the shared Store struct.
func (s *Store) ConsoleSetup() *ConsoleSetupRepo { return &ConsoleSetupRepo{s: s} }

var (
	// ErrSetupNotNeeded: an enabled administrator exists, so there is nothing for
	// a setup to create and `open-setup` refuses to open one.
	ErrSetupNotNeeded = errors.New("store: an enabled administrator exists; the console needs no setup")
	// ErrSetupClosed: no setup is open — completed, or never opened.
	ErrSetupClosed = errors.New("store: the console setup is not open")
	// ErrSetupCodeWrong: the setup is open and the code does not match.
	ErrSetupCodeWrong = errors.New("store: not the setup code")
)

// Open opens the setup with this code digest, or replaces the digest of one
// already open — the installer run twice prints a new code and retires the old.
//
// **Only when no enabled administrator exists**, checked in the same transaction.
// Through the API the setup closes permanently the first time it succeeds; the
// installer can reopen it only on an install where nobody can administer anything,
// which is the one state in which reopening creates no second way in.
func (r *ConsoleSetupRepo) Open(ctx context.Context, digest []byte) error {
	if len(digest) != 32 {
		return errors.New("store: a setup code digest is 32 bytes")
	}
	return mapErr(r.s.tx(ctx, func(tx pgx.Tx) error {
		// Serialise with a concurrent completeAdminSetup: whoever holds the row
		// decides, and the administrator count is read after the lock.
		if _, err := tx.Exec(ctx, `SELECT 1 FROM console_setup FOR UPDATE`); err != nil {
			return err
		}
		var admins int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM operators WHERE role = 'administrator' AND enabled`).Scan(&admins); err != nil {
			return err
		}
		if admins > 0 {
			return ErrSetupNotNeeded
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO console_setup (code_digest, opened_at, completed_at, completed_by)
			VALUES ($1, now(), NULL, NULL)
			ON CONFLICT (tenant_id) DO UPDATE
			   SET code_digest = EXCLUDED.code_digest, opened_at = now(),
			       completed_at = NULL, completed_by = NULL`, digest)
		return err
	}))
}

// IsOpen reports whether a setup is open. No row is "never opened", which is
// closed.
func (r *ConsoleSetupRepo) IsOpen(ctx context.Context) (bool, error) {
	var open bool
	err := r.s.pool.QueryRow(ctx, `SELECT code_digest IS NOT NULL FROM console_setup`).Scan(&open)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return open, mapErr(err)
}

// HasAdministrator reports whether an enabled administrator exists — **the same
// population `Open` gates on**, asked outside a transaction by the console's
// first screen. `enabled` and not merely `role`, for the reason
// `OperatorRepo.CountOtherEnabledAdministrators` gives: a deployment whose only
// administrator is disabled has nobody who can create another, and there is no
// password-reset channel to get one back.
func (r *ConsoleSetupRepo) HasAdministrator(ctx context.Context) (bool, error) {
	var n int
	err := r.s.pool.QueryRow(ctx,
		`SELECT count(*) FROM operators WHERE role = 'administrator' AND enabled`).Scan(&n)
	return n > 0, mapErr(err)
}

// Complete is the whole of `completeAdminSetup`'s write, in one transaction: lock
// the row, check the code with `matches`, hash the password with `hash` — only
// after the code matched, so a wrong code never costs an argon2id — insert the
// administrator, and close the setup. Two callers racing serialise on the lock,
// and the second finds it closed.
func (r *ConsoleSetupRepo) Complete(ctx context.Context, matches func(digest []byte) bool, hash func() (string, error), op *Operator) error {
	return mapErr(r.s.tx(ctx, func(tx pgx.Tx) error {
		var digest []byte
		err := tx.QueryRow(ctx, `SELECT code_digest FROM console_setup FOR UPDATE`).Scan(&digest)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && digest == nil) {
			return ErrSetupClosed
		}
		if err != nil {
			return err
		}
		if !matches(digest) {
			return ErrSetupCodeWrong
		}
		h, err := hash()
		if err != nil {
			return err
		}
		op.PasswordHash = h
		op.Role = "administrator"
		op.Enabled = true
		op.MustChangePassword = false
		if err := tx.QueryRow(ctx, `
			INSERT INTO operators (id, username, display_name, password_hash, role, enabled, must_change_password)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			RETURNING created_at, updated_at`,
			op.ID, op.Username, op.DisplayName, op.PasswordHash, op.Role, op.Enabled, op.MustChangePassword).
			Scan(&op.CreatedAt, &op.UpdatedAt); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			UPDATE console_setup
			   SET code_digest = NULL, completed_at = now(), completed_by = $1`, op.Username)
		return err
	}))
}
