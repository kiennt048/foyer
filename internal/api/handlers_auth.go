package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The console's own sign-in.
//
// `docs/rules/security.md` §9 and the contract's `signIn` description both
// land on the same rule and it is the one thing in this file that is easy to
// undo by accident: **the refusal never says whether it was the username or the
// password**, and a disabled operator receives exactly the same refusal as an
// unknown one. Distinguishing them turns the sign-in form into a way for anyone
// to confirm whether a person has an account here.
//
// Nothing in this file writes a submitted credential anywhere — not into a
// response, not into an audit entry, not into an error.
// `TestNothingEchoesASubmittedCredential` in `server_test.go` drives sign-in,
// the password change and both create-with-a-password operations with a known
// string and fails on any response that contains it.

// passwordMinimum is the floor `createOperator`, `changeOwnPassword` and
// `completeAdminSetup` share.
const passwordMinimum = 12

// **Foyer ships no credential** (`docs/rules/security.md` §7). A new install has
// no known administrator password: the first administrator is made by
// `completeAdminSetup` (handlers_setup.go) with a password of their own. There is
// no shipped value, so there is no refusal of a shipped value as a new password.

func (s *Server) SignIn(ctx context.Context, request gen.SignInRequestObject) (gen.SignInResponseObject, error) {
	if s.ports.Operators == nil || s.ports.AdminSessions == nil {
		return nil, errNotImplemented
	}

	var username, password string
	if request.Body != nil {
		username = strings.ToLower(strings.TrimSpace(request.Body.Username))
		password = request.Body.Password
	}

	faults := map[string]gen.FieldFault{}
	errs := map[string]string{}
	if username == "" {
		faults["username"] = gen.FieldFaultRequired
		errs["username"] = "Required."
	}
	if password == "" {
		faults["password"] = gen.FieldFaultRequired
		errs["password"] = "Required."
	}
	if len(faults) > 0 {
		return nil, validation(errs, faults)
	}

	op, err := s.ports.Operators.Authenticate(ctx, username, password)
	if err != nil {
		// Every reason lands here: unknown username, wrong password, disabled
		// operator. That is the point.
		return nil, badCredentials()
	}

	sess, err := s.startSession(ctx, op, username)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

// startSession mints a console session for an operator who has just proved who
// they are — by password on `signIn`, or by the setup code on
// `completeAdminSetup` — and a fresh trusted-device cookie for the username.
func (s *Server) startSession(ctx context.Context, op Operator, username string) (signedIn, error) {
	token, tokenHash, err := newToken()
	if err != nil {
		return signedIn{}, internalProblem()
	}
	csrf, _, err := newToken()
	if err != nil {
		return signedIn{}, internalProblem()
	}

	expires := s.now().Add(sessionLifetime)
	if err := s.ports.AdminSessions.CreateAdminSession(ctx, AdminSession{
		TokenHash:  tokenHash,
		CSRFToken:  csrf,
		OperatorID: op.Id,
		ExpiresAt:  expires,
	}); err != nil {
		return signedIn{}, internalProblem()
	}
	_ = s.ports.Operators.RecordSignIn(ctx, op.Id, s.now())
	op.LastLoginAt = ptr(s.now())

	// Read rather than remembered: the sign-in path and every later request must
	// agree about this, and the way they agree is by asking the same question of
	// the same row.
	mustChange, _ := s.ports.Operators.MustChangePassword(ctx, op.Id)

	return signedIn{
		body: gen.AuthSession{
			Operator:           op,
			Permissions:        permissionsFor(op.Role),
			CsrfToken:          csrf,
			MustChangePassword: mustChange,
		},
		token:   token,
		expires: expires,
		device:  s.devices.issue(ctx, accountDigest(username)),
		now:     s.now(),
	}, nil
}

// signedIn is `SignIn200JSONResponse` plus the `Set-Cookie` the contract
// describes on the `sessionCookie` scheme.
//
// The generated response type carries only the body, because a `Set-Cookie` on a
// `200` is not something an OpenAPI document expresses well and this one
// describes it in prose on the scheme instead. Writing it here rather than in a
// middleware keeps the one place that mints a session and the one place that
// hands it to a browser adjacent.
type signedIn struct {
	body    gen.AuthSession
	token   string
	expires time.Time
	// device is the trusted-device cookie's value (devicecookie.go).
	device string
	now    time.Time
}

func (r signedIn) VisitSignInResponse(w http.ResponseWriter) error {
	setSessionCookie(w, r.token, r.expires)
	setDeviceCookie(w, r.device, r.now)
	return gen.SignIn200JSONResponse(r.body).VisitSignInResponse(w)
}

// VisitCompleteAdminSetupResponse: the setup signs its browser in exactly as
// `signIn` does.
func (r signedIn) VisitCompleteAdminSetupResponse(w http.ResponseWriter) error {
	setSessionCookie(w, r.token, r.expires)
	setDeviceCookie(w, r.device, r.now)
	return gen.CompleteAdminSetup200JSONResponse(r.body).VisitCompleteAdminSetupResponse(w)
}

// GetAuthSession is the console's first call.
//
// It answers with the operator, the permissions the *server* says that operator
// has, and the CSRF token — all three, because a console that reloaded would
// otherwise hold a session cookie it cannot use for anything that changes state.
// `401` here is a normal answer rather than an error: it means nobody is signed
// in, which is the state every console starts in.
func (s *Server) GetAuthSession(ctx context.Context, _ gen.GetAuthSessionRequestObject) (gen.GetAuthSessionResponseObject, error) {
	who := callerFrom(ctx)
	if who == nil {
		return nil, notSignedIn()
	}
	return gen.GetAuthSession200JSONResponse(gen.AuthSession{
		Operator:           who.Operator,
		Permissions:        who.Permissions,
		CsrfToken:          who.CSRFToken,
		MustChangePassword: who.MustChangePassword,
	}), nil
}

func (s *Server) SignOut(ctx context.Context, _ gen.SignOutRequestObject) (gen.SignOutResponseObject, error) {
	if who := callerFrom(ctx); who != nil && s.ports.AdminSessions != nil {
		_ = s.ports.AdminSessions.DeleteAdminSession(ctx, who.TokenHash)
	}
	// 204 whether or not there was a session to end. "Sign me out" has one
	// correct outcome, and a client that has just been told 401 by something
	// else must still be able to reach it.
	return signedOut{}, nil
}

type signedOut struct{}

func (signedOut) VisitSignOutResponse(w http.ResponseWriter) error {
	clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (s *Server) ChangeOwnPassword(ctx context.Context, request gen.ChangeOwnPasswordRequestObject) (gen.ChangeOwnPasswordResponseObject, error) {
	who := callerFrom(ctx)
	if who == nil {
		return nil, notSignedIn()
	}
	if s.ports.Operators == nil {
		return nil, errNotImplemented
	}

	var current, next string
	if request.Body != nil {
		current = request.Body.CurrentPassword
		next = request.Body.NewPassword
	}

	faults := map[string]gen.FieldFault{}
	errs := map[string]string{}
	params := map[string]map[string]any{}
	switch {
	case current == "":
		faults["currentPassword"] = gen.FieldFaultRequired
		errs["currentPassword"] = "Required."
	}
	switch {
	case next == "":
		faults["newPassword"] = gen.FieldFaultRequired
		errs["newPassword"] = "Required."
	case len(next) < passwordMinimum:
		faults["newPassword"] = gen.FieldFaultBelowMinimum
		errs["newPassword"] = "Too short."
		params["newPassword"] = map[string]any{"minimumCount": passwordMinimum}
	case next == current:
		faults["newPassword"] = gen.FieldFaultUnchanged
		errs["newPassword"] = "Same as the current one."
	}
	if len(faults) > 0 {
		return nil, withFieldParams(validation(errs, faults), params)
	}

	if err := s.ports.Operators.ChangePassword(ctx, who.Operator.Id, current, next); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, notSignedIn()
		}
		// The only other way this fails is a wrong `currentPassword`, and the
		// answer names the field without repeating what was submitted.
		return nil, validation(
			map[string]string{"currentPassword": "Not correct."},
			map[string]gen.FieldFault{"currentPassword": gen.FieldFaultIncorrect},
		)
	}
	return gen.ChangeOwnPassword204Response{}, nil
}

// withFieldParams attaches `params.fieldParams` — the figures in one field's
// sentence, keyed by the same field names as `fields`.
//
// It is separate from `params` because a refusal marks more than one field at a
// time, so a figure belonging to one field has no unambiguous home at the top
// level: two fields refused against different limits would collide there
// silently, and the collision would look exactly like a correct answer.
func withFieldParams(e *apiError, byField map[string]map[string]any) *apiError {
	if len(byField) == 0 {
		return e
	}
	if e.Params == nil {
		e.Params = map[string]any{}
	}
	e.Params["fieldParams"] = byField
	return e
}
