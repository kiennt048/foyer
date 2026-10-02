package apistore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/identity"
	"github.com/kiennt048/foyer/internal/store"
)

// OperatorDirectory — the people who may sign in to the console.
//
// Operators are deployment-wide rather than site-scoped, which is why there is no
// site filter here and none in the contract either. `listOperators` is the
// operation that would reopen that if roles ever became per site.

func (a *Adapter) Operators(ctx context.Context, search string, w api.Window) (api.Page[gen.Operator], error) {
	return fill(w,
		func(cursor string, limit int) (store.Page[store.Operator], error) {
			return a.st.Operators().List(ctx, listOptions("", search, cursor, limit))
		},
		func(o store.Operator) (gen.Operator, bool) { return operatorOut(o), true },
	)
}

func (a *Adapter) OperatorByID(ctx context.Context, id string) (gen.Operator, error) {
	o, err := a.st.Operators().Get(ctx, id)
	if err != nil {
		return gen.Operator{}, out(err)
	}
	return operatorOut(*o), nil
}

func (a *Adapter) CreateOperator(ctx context.Context, in gen.OperatorRequest) (gen.Operator, error) {
	hash, err := identity.HashPassword(in.Password)
	if err != nil {
		return gen.Operator{}, err
	}
	o := &store.Operator{
		ID:          a.newID("op-"),
		Username:    strings.ToLower(strings.TrimSpace(in.Username)),
		DisplayName: in.DisplayName,
		// argon2id, and this package never holds the cleartext beyond this call.
		PasswordHash: hash,
		Role:         string(in.Role),
		Enabled:      in.Enabled == nil || *in.Enabled,
	}
	if err := a.st.Operators().Create(ctx, o); err != nil {
		return gen.Operator{}, out(err)
	}
	return operatorOut(*o), nil
}

func (a *Adapter) AmendOperator(ctx context.Context, id string, in gen.OperatorUpdate) (gen.Operator, error) {
	patch := store.OperatorPatch{
		Role:    (*string)(in.Role),
		Enabled: in.Enabled,
	}
	if in.DisplayName != nil {
		patch.DisplayName = &in.DisplayName
	}
	o, err := a.st.Operators().Update(ctx, id, patch)
	if err != nil {
		return gen.Operator{}, out(err)
	}
	return operatorOut(*o), nil
}

func (a *Adapter) DeleteOperator(ctx context.Context, id string) error {
	return out(a.st.Operators().Delete(ctx, id))
}

// AdministratorCount is what refuses the last one.
//
// The repository counts administrators **other than** a named operator, because the
// question it was written for is "would removing this one leave nobody". The port
// asks the wider question, so nothing is excluded — the caller compares against 1
// and the operator being changed is inside that 1.
func (a *Adapter) AdministratorCount(ctx context.Context) (int, error) {
	n, err := a.st.Operators().CountOtherEnabledAdministrators(ctx, "")
	if err != nil {
		return 0, out(err)
	}
	return n, nil
}

// Authenticate returns one error for every reason.
//
// Unknown username, wrong password and disabled operator are indistinguishable from
// the caller's side, which is what keeps the sign-in form from being a way to
// confirm whether a person has an account here.
//
// **The unknown-username branch still pays for a verification.** Returning
// immediately would make an unknown username measurably faster than a wrong
// password, and an attacker with a stopwatch would have a username oracle that the
// identical error message was there to remove. The comparison is run against a
// verifier of this deployment's own making so that the cost is the same argon2id
// cost, and its result is discarded.
func (a *Adapter) Authenticate(ctx context.Context, username, password string) (gen.Operator, error) {
	o, err := a.st.Operators().GetByUsername(ctx, strings.ToLower(strings.TrimSpace(username)))
	if err != nil {
		a.burnAVerification(password)
		return gen.Operator{}, api.ErrNotFound
	}
	ok, err := identity.VerifyPassword(password, o.PasswordHash)
	if err != nil || !ok {
		return gen.Operator{}, api.ErrNotFound
	}
	// Checked after the verification rather than before, for the same reason the
	// repository returns a disabled operator rather than hiding one.
	if !o.Enabled {
		return gen.Operator{}, api.ErrNotFound
	}
	return operatorOut(*o), nil
}

// decoyVerifier is an argon2id verifier over a value nothing knows.
//
// Built once per process from `crypto/rand`, so it cannot be a credential: there is
// no password that produces it, and it is never stored or compared for a real
// answer. Its only job is to make the unknown-username path cost what the
// known-username path costs.
var decoyVerifier = func() string {
	secret, err := NewSecret()
	if err != nil {
		return ""
	}
	hash, err := identity.HashPassword(secret)
	if err != nil {
		return ""
	}
	return hash
}()

// burnAVerification is the unknown-username path's argon2id, for the console's
// `Authenticate` and the portal's `AuthenticateGuest` alike.
func (a *Adapter) burnAVerification(password string) { decoyVerify(password) }

// decoyVerify is a variable so a test can count it: a decoy nobody can observe is
// a decoy nobody can prove is still wired (`export_test.go`).
var decoyVerify = func(password string) {
	if decoyVerifier == "" {
		return
	}
	_, _ = identity.VerifyPassword(password, decoyVerifier)
}

// ChangePassword verifies `current` before setting `next`.
//
// The session proves the browser was signed in once; it does not prove who is at
// the keyboard, and an unattended console is the ordinary case.
//
// **It does not end the operator's other sessions, and that is a reported gap
// rather than a decision.** `OperatorSessionRepo.DeleteForOperator` exists and
// documents exactly this call — a changed password that leaves old sessions live
// has revoked nothing, which is the thing an operator changing a password after a
// laptop went missing is actually buying. But the port is
// `ChangePassword(operatorID, current, next)` and carries no token hash, so this
// package cannot tell the caller's own session from the ones it should end: calling
// it would sign the operator out of the console they are standing at, one request
// after a `204` that said the change succeeded. The contract declares no such
// behaviour either. **The port wants the caller's session, or a `KeepSession`
// argument.**
func (a *Adapter) ChangePassword(ctx context.Context, operatorID, current, next string) error {
	o, err := a.st.Operators().Get(ctx, operatorID)
	if err != nil {
		return out(err)
	}
	ok, err := identity.VerifyPassword(current, o.PasswordHash)
	if err != nil || !ok {
		// `ErrConflict` rather than `ErrNotFound`: the operator exists and the
		// caller distinguishes the two — a wrong current password names the
		// field, a missing operator signs the caller out.
		return api.ErrConflict
	}
	hash, err := identity.HashPassword(next)
	if err != nil {
		return err
	}
	return out(a.st.Operators().SetPassword(ctx, operatorID, hash))
}

func (a *Adapter) RecordSignIn(ctx context.Context, operatorID string, at time.Time) error {
	return out(a.st.Operators().RecordLogin(ctx, operatorID, at))
}

// MustChangePassword reads `operators.must_change_password`.
//
// A missing row answers `false` rather than an error: the operator was deleted
// under a live session, which `resolveCaller` already treats as signed out, and an
// error here would turn that into a refusal an operator cannot act on.
func (a *Adapter) MustChangePassword(ctx context.Context, operatorID string) (bool, error) {
	op, err := a.st.Operators().Get(ctx, operatorID)
	if err != nil {
		return false, nil
	}
	return op.MustChangePassword, nil
}

// ── the first-access setup (migration 00040) ────────────────────────────────

func (a *Adapter) SetupOpen(ctx context.Context) (bool, error) {
	return a.st.ConsoleSetup().IsOpen(ctx)
}

func (a *Adapter) HasAdministrator(ctx context.Context) (bool, error) {
	return a.st.ConsoleSetup().HasAdministrator(ctx)
}

// CompleteSetup hands the store two closures so that the order is the store's
// transaction's: the code is compared under the row lock, and the argon2id runs
// only after it matched — a wrong code costs a SHA-256, never 64 MiB.
func (a *Adapter) CompleteSetup(ctx context.Context, code, username, password string) (gen.Operator, error) {
	o := &store.Operator{
		ID:       a.newID("op-"),
		Username: strings.ToLower(strings.TrimSpace(username)),
	}
	err := a.st.ConsoleSetup().Complete(ctx,
		func(digest []byte) bool { return identity.SameSetupCode(code, digest) },
		func() (string, error) { return identity.HashPassword(password) },
		o)
	switch {
	case errors.Is(err, store.ErrSetupClosed):
		return gen.Operator{}, api.ErrSetupClosed
	case errors.Is(err, store.ErrSetupCodeWrong):
		return gen.Operator{}, api.ErrSetupCodeWrong
	case err != nil:
		return gen.Operator{}, out(err)
	}
	return operatorOut(*o), nil
}
