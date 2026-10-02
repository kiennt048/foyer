package api

import (
	"context"
	"errors"
	"strings"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// Accounts and operators — two objects, and never one table with a flag.
//
// An `Account` is a *guest's* identity: it carries `siteId` and it has an
// `argon2id+nt` branch, because a guest identity may be opted in to
// PEAP-MSCHAPv2. An `Operator` works across sites, never authenticates over
// RADIUS, and is `argon2id` unconditionally.
//
// A flag would put a password-equivalent NT hash one boolean away from every
// administrator in the deployment — an NT hash is the sole input to the
// MSCHAPv2 response, so a stolen one authenticates directly. That is why they
// are separate ports here as well as separate tables.

func (s *Server) ListAccounts(ctx context.Context, request gen.ListAccountsRequestObject) (gen.ListAccountsResponseObject, error) {
	if s.ports.Accounts == nil {
		return nil, errNotImplemented
	}
	p := request.Params
	page, err := s.ports.Accounts.Accounts(ctx, string(p.SiteId), text((*string)(p.Q)), windowFrom(p.Cursor, p.Limit))
	if err != nil {
		return nil, err
	}
	items := page.Items
	if items == nil {
		items = []Account{}
	}
	return gen.ListAccounts200JSONResponse{Items: items, NextCursor: page.NextCursor}, nil
}

func (s *Server) CreateAccount(ctx context.Context, request gen.CreateAccountRequestObject) (gen.CreateAccountResponseObject, error) {
	if s.ports.Accounts == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body

	errs := map[string]string{}
	faults := map[string]gen.FieldFault{}
	fieldParams := map[string]map[string]any{}
	if strings.TrimSpace(in.Username) == "" {
		errs["username"] = "Required."
		faults["username"] = gen.FieldFaultRequired
	}
	if strings.TrimSpace(in.SiteId) == "" {
		errs["siteId"] = "Required."
		faults["siteId"] = gen.FieldFaultRequired
	}
	switch {
	case in.Password == "":
		errs["password"] = "Required."
		faults["password"] = gen.FieldFaultRequired
	case len(in.Password) < passwordMinimum:
		errs["password"] = "Too short."
		faults["password"] = gen.FieldFaultBelowMinimum
		fieldParams["password"] = map[string]any{"minimumCount": passwordMinimum}
	case in.EapTtlsPap != nil && *in.EapTtlsPap && len(in.Password) > papPasswordMaxBytes:
		errs["password"], faults["password"] = "Too long for 802.1X.", gen.FieldFaultAboveMaximum
		fieldParams["password"] = map[string]any{"maximumCount": papPasswordMaxBytes}
	}
	if len(faults) > 0 {
		return nil, withFieldParams(validation(errs, faults), fieldParams)
	}

	a, err := s.ports.Accounts.CreateAccount(ctx, in)
	if err != nil {
		if fault := credentialFault(err); fault != nil {
			return nil, fault
		}
		if isConflict(err) {
			return nil, duplicateField("username")
		}
		return nil, err
	}
	// Creating a guest account is not audited: the log records what cannot be
	// undone or what is security-relevant to the *network*, and a portal-only guest
	// account is neither — see `listAudit`. **Opting it in to 802.1X is**, because
	// it makes the identity reachable over EAP-TTLS/PAP against a verifier that is
	// not argon2id, so an account created opted in writes the entry an opt-in later
	// would have.
	if in.EapTtlsPap != nil && *in.EapTtlsPap {
		s.recordAccountCredentials(ctx, a, false)
	}
	return gen.CreateAccount201JSONResponse(a), nil
}

// credentialFault maps the refusals of an account's credential change to the
// field the console marks.
func credentialFault(err error) *apiError {
	switch {
	case errors.Is(err, ErrPasswordRequiredFor8021X):
		return validation(map[string]string{"password": "Required to enable 802.1X."},
			map[string]gen.FieldFault{"password": gen.FieldFaultRequired})
	case errors.Is(err, ErrPasswordTooLongFor8021X):
		return withFieldParams(validation(map[string]string{"password": "Too long for 802.1X."},
			map[string]gen.FieldFault{"password": gen.FieldFaultAboveMaximum}),
			map[string]map[string]any{"password": {"maximumCount": papPasswordMaxBytes}})
	case errors.Is(err, ErrPAPUsernameTaken):
		return duplicateField("eapTtlsPap")
	}
	return nil
}

// recordAccountCredentials writes `account-credentials-changed`: whether the
// password changed and the 802.1X state now in force. Never a value.
func (s *Server) recordAccountCredentials(ctx context.Context, a Account, passwordChanged bool) {
	s.recordIn(ctx, callerFrom(ctx), a.SiteId, gen.AuditKindAccountCredentialsChanged, gen.AuditSubject{
		// Id and username are pseudonymised by an erasure of the account (migration 00052).
		Kind: gen.AuditSubjectKindAccount, Id: a.Id, Name: ptr(a.Username),
	}, map[string]any{
		"passwordChanged": passwordChanged,
		"eapTtlsPap":      strings.Contains(string(a.CredentialKind), "+bcrypt"),
	})
}

func (s *Server) UpdateAccount(ctx context.Context, request gen.UpdateAccountRequestObject) (gen.UpdateAccountResponseObject, error) {
	if s.ports.Accounts == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	body := *request.Body
	if pw := body.Password; pw != nil {
		// The same floor `createAccount` applies. The 72-byte ceiling depends on
		// whether 802.1X is in force after the change, which only the port knows.
		if len(*pw) < passwordMinimum {
			return nil, withFieldParams(validation(map[string]string{"password": "Too short."},
				map[string]gen.FieldFault{"password": gen.FieldFaultBelowMinimum}),
				map[string]map[string]any{"password": {"minimumCount": passwordMinimum}})
		}
	}
	a, err := s.ports.Accounts.AmendAccount(ctx, request.AccountId, AccountAmendment{
		DisplayName: body.DisplayName,
		Enabled:     body.Enabled,
		Password:    body.Password,
		EapTtlsPap:  body.EapTtlsPap,
	})
	if err != nil {
		if fault := credentialFault(err); fault != nil {
			return nil, fault
		}
		return nil, err
	}
	if body.Password != nil || body.EapTtlsPap != nil {
		s.recordAccountCredentials(ctx, a, body.Password != nil)
	}
	return gen.UpdateAccount200JSONResponse(a), nil
}

func (s *Server) DeleteAccount(ctx context.Context, request gen.DeleteAccountRequestObject) (gen.DeleteAccountResponseObject, error) {
	if s.ports.Accounts == nil {
		return nil, errNotImplemented
	}
	if err := s.ports.Accounts.DeleteAccount(ctx, request.AccountId); err != nil {
		return nil, err
	}
	return gen.DeleteAccount204Response{}, nil
}

// ── operators ────────────────────────────────────────────────────────────────

func (s *Server) ListOperators(ctx context.Context, request gen.ListOperatorsRequestObject) (gen.ListOperatorsResponseObject, error) {
	if s.ports.Operators == nil {
		return nil, errNotImplemented
	}
	p := request.Params
	page, err := s.ports.Operators.Operators(ctx, text((*string)(p.Q)), windowFrom(p.Cursor, p.Limit))
	if err != nil {
		return nil, err
	}
	items := page.Items
	if items == nil {
		items = []Operator{}
	}
	return gen.ListOperators200JSONResponse{Items: items, NextCursor: page.NextCursor}, nil
}

func (s *Server) CreateOperator(ctx context.Context, request gen.CreateOperatorRequestObject) (gen.CreateOperatorResponseObject, error) {
	if s.ports.Operators == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	in := *request.Body

	errs := map[string]string{}
	faults := map[string]gen.FieldFault{}
	fieldParams := map[string]map[string]any{}
	if strings.TrimSpace(in.Username) == "" {
		errs["username"] = "Required."
		faults["username"] = gen.FieldFaultRequired
	}
	switch {
	case in.Password == "":
		errs["password"] = "Required."
		faults["password"] = gen.FieldFaultRequired
	case len(in.Password) < passwordMinimum:
		errs["password"] = "Too short."
		faults["password"] = gen.FieldFaultBelowMinimum
		fieldParams["password"] = map[string]any{"minimumCount": passwordMinimum}
	}
	if len(faults) > 0 {
		return nil, withFieldParams(validation(errs, faults), fieldParams)
	}

	op, err := s.ports.Operators.CreateOperator(ctx, in)
	if err != nil {
		if isConflict(err) {
			return nil, duplicateField("username")
		}
		return nil, err
	}
	s.record(ctx, callerFrom(ctx), gen.AuditKindOperatorCreated, gen.AuditSubject{
		Kind: gen.AuditSubjectKindOperator, Id: op.Id, Name: ptr(op.Username),
	}, map[string]any{"role": string(op.Role)})
	return gen.CreateOperator201JSONResponse(op), nil
}

func (s *Server) UpdateOperator(ctx context.Context, request gen.UpdateOperatorRequestObject) (gen.UpdateOperatorResponseObject, error) {
	if s.ports.Operators == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	before, err := s.ports.Operators.OperatorByID(ctx, request.OperatorId)
	if err != nil {
		return nil, err
	}
	if err := s.refuseToOrphanTheDeployment(ctx, before, request.Body.Role, request.Body.Enabled); err != nil {
		return nil, err
	}

	op, err := s.ports.Operators.AmendOperator(ctx, request.OperatorId, *request.Body)
	if err != nil {
		return nil, err
	}
	s.record(ctx, callerFrom(ctx), gen.AuditKindOperatorUpdated, gen.AuditSubject{
		Kind: gen.AuditSubjectKindOperator, Id: op.Id, Name: ptr(op.Username),
	}, map[string]any{"role": string(op.Role), "enabled": op.Enabled})
	return gen.UpdateOperator200JSONResponse(op), nil
}

func (s *Server) DeleteOperator(ctx context.Context, request gen.DeleteOperatorRequestObject) (gen.DeleteOperatorResponseObject, error) {
	if s.ports.Operators == nil {
		return nil, errNotImplemented
	}
	op, err := s.ports.Operators.OperatorByID(ctx, request.OperatorId)
	if err != nil {
		return nil, err
	}
	if err := s.refuseToOrphanTheDeployment(ctx, op, nil, ptr(false)); err != nil {
		return nil, err
	}
	if err := s.ports.Operators.DeleteOperator(ctx, op.Id); err != nil {
		return nil, err
	}
	s.record(ctx, callerFrom(ctx), gen.AuditKindOperatorDeleted, gen.AuditSubject{
		Kind: gen.AuditSubjectKindOperator, Id: op.Id, Name: ptr(op.Username),
	}, nil)
	return gen.DeleteOperator204Response{}, nil
}

// refuseToOrphanTheDeployment is `/problems/last-administrator`.
//
// **There is no password-reset channel**, so a deployment left with nobody who
// can administer it has no recovery short of the Go binary's first-boot path.
// The check is on *enabled* administrators, because disabling is the reversible
// half of deletion and leaves exactly the same hole.
func (s *Server) refuseToOrphanTheDeployment(ctx context.Context, op Operator, nextRole *gen.OperatorRole, nextEnabled *bool) error {
	if op.Role != gen.Administrator || !op.Enabled {
		return nil
	}
	stillAdmin := nextRole == nil || *nextRole == gen.Administrator
	stillEnabled := nextEnabled == nil || *nextEnabled
	if stillAdmin && stillEnabled {
		return nil
	}
	count, err := s.ports.Operators.AdministratorCount(ctx)
	if err != nil {
		return err
	}
	if count <= 1 {
		e := lastAdministrator()
		e.Params = map[string]any{"username": op.Username}
		return e
	}
	return nil
}
