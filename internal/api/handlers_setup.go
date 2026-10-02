package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The first-access setup — how a fresh install gets its first administrator
// without shipping a credential.
//
// ── what the field does ─────────────────────────────────────────────────────
//
//   - **authentik**: `akadmin` exists with no usable password; the
//     `initial-setup` flow sets one and only runs while none is set. Whoever
//     reaches it first wins.
//   - **UniFi Network / Omada controller**: a first-run wizard creates the
//     administrator; no default credential; also first-come.
//   - **Grafana / pfSense**: a default credential (`admin`/`admin`,
//     `admin`/`pfsense`) and a prompt to change it — a shipped credential, which
//     Foyer does not have.
//   - **Jenkins**: the setup wizard is locked by `initialAdminPassword`, a random
//     value the installer writes and logs; **GitLab** generates
//     `initial_root_password`; **Keycloak** creates a temporary bootstrap admin, or
//     allows the welcome page only from loopback.
//   - **OWASP ASVS 4.0** §2.3.1: an initial secret is random and cannot become the
//     long-term password; §2.5.4: no shared or default accounts.
//
// **Decision: the Jenkins shape.** The console is published on every interface
// (`deploy/compose.yaml`), so a first-come setup leaves a window open to
// anybody on the venue LAN who opens the page before the owner owns the
// RADIUS server. Loopback-only is Keycloak's answer and does not survive Docker's
// port publishing (the peer is the bridge gateway). So the installer opens the
// setup with an 80-bit random code (`foyerctl open-setup`, called by
// `deploy/first-boot.sh`, which prints it), the console shows the setup form while
// it is open, and the code plus a username and a password of the operator's own
// create the administrator and close the setup in one transaction. The operator
// never types a command: they read one line of the installer they already ran.
// The username is theirs to choose (ASVS §2.5.4) rather than a fixed `admin`.
//
// **No expiry**, where GitLab deletes its file after 24 hours. The only way to get
// a code is the installer, so an expired code on an unattended fresh install is
// a console nobody can open, and 80 bits under the
// address throttle does not get weaker with time. Re-running the installer before
// setup is complete retires the old code and prints a new one.

// **`getAdminSetup` tells a stranger whether the install is fresh, on purpose.**
// The field answers the same question the same way: a fresh Jenkins shows every visitor
// its "Unlock Jenkins" page, authentik's `/if/flow/initial-setup/` is reachable
// anonymously and answers differently once an admin exists, and Keycloak's welcome
// page offers the admin form only while there is none. None of them hides the
// state, because the page an installer is sent to has to render the form before
// anyone is signed in, and hiding it would buy nothing: `completeAdminSetup`
// answers `setup-complete` to anyone who tries, which is the same bit. What guards
// a fresh install is the 80-bit code the installer printed, spent once, behind the
// address throttle — not the secrecy of "this install is new". So it stays
// anonymous and stays a boolean, and carries nothing else (no version, no time
// the setup was opened).
// **`noAdministrator` exists because `required: false` was two different installs
// wearing one answer.** Reproduced on a fresh install: an install whose
// bootstrap never ran — an interrupted installer, a restore that lost
// `console_setup`, a container started without `foyerctl open-setup` — has no
// administrator *and* no code, and without this bit the console renders the
// sign-in form for it. Silently, and successfully: the request is not failing
// open, the server is truthfully reporting a setup it considers closed. That form
// can never succeed, and the copy below it promises a recovery ("an administrator will disable this
// account") that needs an administrator this install does not have.
//
// The state is one bit and it is not a secret: `completeAdminSetup` answers
// `setup-complete` to anyone who asks, which is the same bit, and `internal/api/
// handlers_setup.go`'s survey above is why the whole of this is already public.
// **It changes nothing about the guard.** No code is minted on demand, the
// screen in that state offers no form, and `completeAdminSetup` stays closed
// exactly as it is for a finished install — the screen tells the truth about a
// dead end instead of pretending it is a login.
func (s *Server) GetAdminSetup(ctx context.Context, _ gen.GetAdminSetupRequestObject) (gen.GetAdminSetupResponseObject, error) {
	if s.ports.Setup == nil {
		return gen.GetAdminSetup200JSONResponse{}, nil
	}
	open, err := s.ports.Setup.SetupOpen(ctx)
	if err != nil {
		return nil, internalProblem()
	}
	if open {
		return gen.GetAdminSetup200JSONResponse{Required: true}, nil
	}
	// Only asked when the setup is closed, so the common case — a finished
	// install — still costs the one query it always cost.
	admin, err := s.ports.Setup.HasAdministrator(ctx)
	if err != nil {
		return nil, internalProblem()
	}
	return gen.GetAdminSetup200JSONResponse{Required: false, NoAdministrator: !admin}, nil
}

func (s *Server) CompleteAdminSetup(ctx context.Context, request gen.CompleteAdminSetupRequestObject) (gen.CompleteAdminSetupResponseObject, error) {
	// Closed answers the same whatever the body says: a finished install tells a
	// caller nothing more than that it is finished.
	if s.ports.Setup == nil || s.ports.Operators == nil || s.ports.AdminSessions == nil {
		return nil, setupComplete()
	}
	open, err := s.ports.Setup.SetupOpen(ctx)
	if err != nil {
		return nil, internalProblem()
	}
	if !open {
		return nil, setupComplete()
	}

	var in gen.AdminSetupRequest
	if request.Body != nil {
		in = *request.Body
	}
	username := strings.ToLower(strings.TrimSpace(in.Username))

	faults := map[string]gen.FieldFault{}
	errs := map[string]string{}
	params := map[string]map[string]any{}
	if strings.TrimSpace(in.SetupCode) == "" {
		faults["setupCode"], errs["setupCode"] = gen.FieldFaultRequired, "Required."
	}
	if username == "" {
		faults["username"], errs["username"] = gen.FieldFaultRequired, "Required."
	}
	switch {
	case in.Password == "":
		faults["password"], errs["password"] = gen.FieldFaultRequired, "Required."
	case len(in.Password) < passwordMinimum:
		faults["password"], errs["password"] = gen.FieldFaultBelowMinimum, "Too short."
		params["password"] = map[string]any{"minimumCount": passwordMinimum}
	}
	if len(faults) > 0 {
		return nil, withFieldParams(validation(errs, faults), params)
	}

	op, err := s.ports.Setup.CompleteSetup(ctx, in.SetupCode, username, in.Password)
	switch {
	case errors.Is(err, ErrSetupClosed):
		// The other browser won the race.
		return nil, setupComplete()
	case errors.Is(err, ErrSetupCodeWrong):
		// Logged without the code: an operator reading the journal learns that
		// somebody is trying, not what they tried.
		slog.Warn("console setup: a wrong setup code was submitted")
		return nil, validation(
			map[string]string{"setupCode": "Not the code the installer printed."},
			map[string]gen.FieldFault{"setupCode": gen.FieldFaultIncorrect},
		)
	case isConflict(err):
		return nil, duplicateField("username")
	case err != nil:
		return nil, internalProblem()
	}

	// Audited by the administrator it made: there is nobody else to attribute it
	// to, and `operator-created` is what happened.
	s.record(ctx, &caller{Operator: op}, gen.AuditKindOperatorCreated, gen.AuditSubject{
		Kind: gen.AuditSubjectKindOperator, Id: op.Id, Name: ptr(op.Username),
	}, map[string]any{"role": string(op.Role), "setup": true})
	slog.Info("console setup: the first administrator was created; the setup is closed", "operator", op.Id)

	sess, err := s.startSession(ctx, op, username)
	if err != nil {
		return nil, err
	}
	return sess, nil
}

func setupComplete() *apiError {
	return &apiError{Status: http.StatusConflict, Type: gen.ProblemssetupComplete, Title: "Setup is complete"}
}
