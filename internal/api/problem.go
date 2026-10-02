package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// Problem documents, RFC 9457, as `web/contract/openapi.yaml` defines them.
//
// Three rules the contract states and this file is the only place that can break:
//
//  1. `type` is the discriminator and the client renders its own copy from it.
//     `title` is a short fallback label; `detail` is a composed sentence and is
//     the field a conforming server sends only for a `type` it expects clients
//     not to know. `web/mock` sends none at all, and neither does this.
//  2. `detail` is never a stack trace, an internal path, or a database error
//     (`docs/rules/security.md` §9). `internalProblem` exists so that an
//     unexpected error has exactly one way to reach a client, and that way
//     carries no text from the error at all.
//  3. Nothing in a problem document may echo a submitted credential. There is no
//     path here that copies a request body into a response.
//
// `problem_test.go` pins two things about this file: that no constructor emits a
// `detail`, and **which members of `ProblemType` this package can and cannot
// produce**. The second is the contract's own rule — "every value must be
// reachable by driving the server" — held to as far as it can be while five
// operations are unimplemented, with the shortfall named rather than assumed.

// apiError is a refusal a handler can return from anywhere and have rendered as
// the contract's problem document. It carries the status and the discriminator;
// it deliberately has no place to put an underlying error's text.
type apiError struct {
	Status int
	Type   gen.ProblemType
	Title  string

	// Fields is the `errors` member: field name → the server's own wording.
	// Fields is the `params.fields` member: field name → why it was refused.
	Errors map[string]string
	Faults map[string]gen.FieldFault

	// Params is `params`: structured values the client interpolates. Never a
	// sentence — the contract is explicit that a sentence belongs in `detail`
	// and `detail` is what a client must not render.
	Params map[string]any

	// FieldParams is `params.fieldParams`: the figures **one refused field's**
	// sentence needs, keyed by the same field names as `Errors` and `Faults`.
	//
	// Separate from `Params` because the two answer different questions and the
	// contract says so: a refusal can mark several fields at once, so a figure at
	// the top level belongs to no particular one and two fields refused the same
	// way collide there. `x-fault-params` names which faults need which figures,
	// and a fault that declares one and arrives without it renders as a sentence
	// with a hole in it, such as *"At least 0."*
	FieldParams map[string]map[string]any
}

func (e *apiError) Error() string {
	// Deliberately terse and free of any request-supplied text: this string
	// reaches logs, and a refusal that quotes what was submitted is how a
	// password ends up in a log file.
	return fmt.Sprintf("api: %d %s", e.Status, e.Type)
}

// Problem renders the error as the wire shape.
func (e *apiError) Problem() gen.Problem {
	p := gen.Problem{
		Type:   e.Type,
		Title:  e.Title,
		Status: e.Status,
	}
	if len(e.Errors) > 0 {
		errs := make(map[string]string, len(e.Errors))
		for k, v := range e.Errors {
			errs[k] = v
		}
		p.Errors = &errs
	}
	if len(e.Faults) > 0 || len(e.Params) > 0 || len(e.FieldParams) > 0 {
		params := &gen.Problem_Params{}
		if len(e.Faults) > 0 {
			faults := make(map[string]gen.FieldFault, len(e.Faults))
			for k, v := range e.Faults {
				faults[k] = v
			}
			params.Fields = &faults
		}
		if len(e.FieldParams) > 0 {
			// Omitted rather than sent empty, the same way `web/mock` omits it: an
			// empty object reads as "asked and answered nothing" on the refusals
			// where no fault names a figure, which is most of them.
			figures := make(map[string]map[string]any, len(e.FieldParams))
			for field, values := range e.FieldParams {
				copied := make(map[string]any, len(values))
				for k, v := range values {
					copied[k] = v
				}
				figures[field] = copied
			}
			params.FieldParams = &figures
		}
		for k, v := range e.Params {
			if params.AdditionalProperties == nil {
				params.AdditionalProperties = map[string]any{}
			}
			params.AdditionalProperties[k] = v
		}
		p.Params = params
	}
	return p
}

// ── the refusals this API can produce ────────────────────────────────────────
//
// One constructor per `ProblemType` this package sends, and no more. A
// constructor nothing calls is the same vacuous catalogue entry the enum exists
// to prevent, so a value that cannot yet be produced is named in
// `problem_test.go` with its reason rather than given a constructor nothing
// reaches.

func notSignedIn() *apiError {
	return &apiError{Status: http.StatusUnauthorized, Type: gen.ProblemsnotSignedIn, Title: "Not signed in"}
}

func badCredentials() *apiError {
	// Never says whether it was the username or the password, and a disabled
	// operator gets this one too. An answer that distinguishes them is a
	// username oracle — see `signIn` in the contract.
	return &apiError{Status: http.StatusUnauthorized, Type: gen.ProblemsbadCredentials, Title: "Sign-in failed"}
}

func forbidden() *apiError {
	return &apiError{Status: http.StatusForbidden, Type: gen.Problemsforbidden, Title: "Forbidden"}
}

// passwordChangeRequired is the refusal an operator gets while they still hold
// the credential the install shipped with.
//
// **403 and not 401.** The session is valid and the operator is who they say they
// are; signing in again changes nothing, which is exactly the distinction the
// contract draws between `forbidden` and `not-signed-in`. It is a separate type
// from `forbidden` because the client's answer is different again: `forbidden`
// means *this account may not do this*, and the console renders a refusal;
// this one means *no account may do anything yet*, and the console routes to the
// one screen that clears it.
func passwordChangeRequired() *apiError {
	return &apiError{
		Status: http.StatusForbidden,
		Type:   gen.ProblemspasswordChangeRequired,
		Title:  "Password change required",
	}
}

func notFound() *apiError {
	return &apiError{Status: http.StatusNotFound, Type: gen.ProblemsnotFound, Title: "Not found"}
}

// duplicate is `/problems/duplicate`, and it is narrower than it looks.
//
// It carries no `errors` and no `params.fields`, because the contract scopes
// both to `/problems/validation` and `/problems/too-large`. What it carries is
// the value that collided, which is what a client interpolates into its own
// copy.
//
// It is **not** the answer to a duplicate username on `createAccount` or
// `createOperator`: those operations declare only `201` and `422`, so a `409`
// there would be a status no client has a branch for. That refusal is
// `duplicateField` below.
func duplicate(value string) *apiError {
	return &apiError{
		Status: http.StatusConflict,
		Type:   gen.Problemsduplicate,
		Title:  "Already exists",
		Params: map[string]any{"value": value},
	}
}

// oneSitePerInstall is `createSite`'s refusal of the second call.
//
// Not `duplicate`, and the difference is what the operator does next. `duplicate`
// carries the value that collided and means *choose another*; here nothing
// collided and choosing another name cannot help. The answer is that this
// deployment holds one venue and a second one is a second installation.
//
// It carries no params. There is nothing to interpolate — the site that already
// exists is on `listSites`, which the console has already loaded to render the
// switcher, and naming it here would be a second copy of a row the client holds.
func oneSitePerInstall() *apiError {
	return &apiError{
		Status: http.StatusConflict,
		Type:   gen.ProblemsoneSitePerInstall,
		Title:  "This deployment already has a site",
	}
}

// duplicateField is a uniqueness refusal on an operation whose contract declares
// no `409`. It is a validation refusal naming the field, with the `duplicate`
// fault — which is exactly what `FieldFault` has that member for.
func duplicateField(field string) *apiError {
	return validation(
		map[string]string{field: "Already in use."},
		map[string]gen.FieldFault{field: gen.FieldFaultDuplicate},
	)
}

func validation(errs map[string]string, faults map[string]gen.FieldFault) *apiError {
	return &apiError{
		Status: http.StatusUnprocessableEntity,
		Type:   gen.Problemsvalidation,
		Title:  "That cannot be saved",
		Errors: errs,
		Faults: faults,
	}
}

// tooLarge sends the two figures **twice, and that is deliberate.**
//
// The contract's `x-fault-params` declares that `too-large` needs `bytes` and
// `limitBytes`, and it says where they go: `params.fieldParams[field]`, *"read off
// `params.fieldParams[field]` rather than the top level on purpose — a refusal marks
// several fields at once, so a figure at the top level belongs to no particular one
// and two fields refused the same way would collide there."*
//
// This sent only the top-level copy, which is the `below-minimum` mistake one field
// along: a client reading the declared location finds nothing and renders *"0 against
// a 0 limit"*. `web/mock` has sent both since the field existed; the top level is what
// a page-level sentence quotes and `fieldParams` is what the sentence under the input
// quotes, and with exactly one field refused they are the same two numbers.
//
// The figures are on the field, so the caller passes them per field rather than as a
// bag: a signature that took one map would make a second refused field impossible to
// express and would look, from the call site, as though it were already handled.
func tooLarge(field string, fault gen.FieldFault, params map[string]any) *apiError {
	e := &apiError{
		Status: http.StatusRequestEntityTooLarge,
		Type:   gen.ProblemstooLarge,
		Title:  "Too large",
		Errors: map[string]string{field: "Too large."},
		Faults: map[string]gen.FieldFault{field: fault},
		Params: params,
	}
	if len(params) > 0 {
		figures := make(map[string]any, len(params))
		for k, v := range params {
			figures[k] = v
		}
		e.FieldParams = map[string]map[string]any{field: figures}
	}
	return e
}

func controllerInUse(count int) *apiError {
	return &apiError{
		Status: http.StatusConflict,
		Type:   gen.ProblemscontrollerInUse,
		Title:  "Access points still reference this controller",
		Params: map[string]any{"count": count},
	}
}

func controllerUnreachable() *apiError {
	return &apiError{Status: http.StatusConflict, Type: gen.ProblemscontrollerUnreachable, Title: "Controller unreachable"}
}

// needsController is `422`, not `409`, because `createNas` declares only `201`
// and `422`. A status the contract does not declare is a status no generated
// client has a branch for, whatever the problem type says.
func needsController() *apiError {
	return &apiError{Status: http.StatusUnprocessableEntity, Type: gen.ProblemsneedsController, Title: "This driver needs a controller"}
}

func rotationRefused() *apiError {
	// The driver cannot push a secret and the device is unreachable — the one
	// combination with no route back. Neither condition refuses on its own.
	return &apiError{Status: http.StatusConflict, Type: gen.ProblemsrotationRefused, Title: "Rotation would take this device off the air"}
}

func gardenEntryRequired() *apiError {
	return &apiError{Status: http.StatusConflict, Type: gen.ProblemsgardenEntryRequired, Title: "Required entry"}
}

func lastAdministrator() *apiError {
	return &apiError{Status: http.StatusConflict, Type: gen.ProblemslastAdministrator, Title: "This is the last administrator"}
}

// ── the guest portal's three ────────────────────────────────────────────────
//
// These had no constructor while the five portal operations answered `501`, and
// the note that stood here said so. They exist now because the operations do, and
// the rule the contract states about `ProblemType` is what made the absence worth
// recording rather than shrugging at: "every value must be reachable by driving
// the server", because a value nothing can emit is a catalogue entry that can
// never be shown to be right.

// deviceNotVerified is `getPortalContext`'s refusal.
//
// **It deliberately does not say which check failed.** The contract is explicit:
// the client is not associated, is on a different access point or site, or is
// already authorized — one answer for all of them, because an answer that
// distinguished them would be an oracle for what the portal believes about a MAC
// anybody can name in a query string. There is no `detail` here at all, so there
// is no sentence for a future reader to helpfully make more specific.
func deviceNotVerified() *apiError {
	return &apiError{Status: http.StatusForbidden, Type: gen.ProblemsdeviceNotVerified, Title: "Could not confirm this device"}
}

// methodNotOffered is `acceptTerms` at a venue that has not enabled
// click-through, or has enabled it without a duration.
//
// `404` rather than a silent grant. Click-through is the one method with no
// credential in front of it, so an endpoint that keeps working while its button
// is hidden is unmetered public internet for anybody who reads a URL.
func methodNotOffered() *apiError {
	return &apiError{Status: http.StatusNotFound, Type: gen.ProblemsmethodNotOffered, Title: "Not offered here"}
}

// noGrant is `getPortalStatus` with nothing to report.
//
// It means **no grant answers to this browser**, and nothing else. A grant that
// has ended is answered `200` with its real state: making this cover both is how
// `PortalStatus.state` ends up with two members nothing can produce, and how a
// guest whose voucher was just revoked gets shown a green tick.
func noGrant() *apiError {
	return &apiError{Status: http.StatusNotFound, Type: gen.ProblemsnoGrant, Title: "Not connected"}
}

// `/problems/session-end-failed` has no constructor either, and for a different
// reason: it is never an HTTP refusal. It travels inside `revokeVoucher`'s `200`
// as a `Failure`, because the operation did what it was asked and something it
// attempted afterwards did not — writing `200` into a problem document would be
// false, and `502` would be a status the operation never returns.

// internalProblem is the only way an unexpected error reaches a client, and it
// carries nothing from the error. §9: no stack traces, no internal paths.
func internalProblem() *apiError {
	return &apiError{Status: http.StatusInternalServerError, Type: gen.Problemsinternal, Title: "Something went wrong"}
}

// errNotImplemented marks an operation this package has not built yet.
//
// It answers `501` rather than a plausible body, on purpose. The whole reason
// `internal/api` was replaced rather than repaired is that eight of its handlers
// returned convincing JSON that had never touched a store, so a gap has to be
// louder than a success. `notimplemented.go` pins the exact set.
var errNotImplemented = errors.New("api: operation not implemented")

func notImplementedProblem() *apiError {
	return &apiError{Status: http.StatusNotImplemented, Type: gen.Problemsinternal, Title: "Not implemented"}
}

// writeProblem renders a refusal. `application/problem+json` per RFC 9457 and
// per the contract's `responses:` blocks — the media type is part of the
// contract, and a plain `application/json` refusal is one a client's error
// branch may never recognise.
func writeProblem(w http.ResponseWriter, e *apiError) {
	body, err := json.Marshal(e.Problem())
	if err != nil {
		// Marshalling a problem cannot realistically fail, and if it does the
		// answer still may not carry the error's text.
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"type":"/problems/internal","title":"Something went wrong","status":500}`))
		return
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(e.Status)
	_, _ = w.Write(body)
}

// asAPIError maps any error a handler or port returns onto a refusal.
//
// The default is `internalProblem`, which carries no text — so a port that
// returns a raw `pq: relation "nas" does not exist` cannot leak it. That is the
// point of having exactly one funnel.
func asAPIError(err error) *apiError {
	var e *apiError
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, errNotImplemented):
		return notImplementedProblem()
	case errors.Is(err, ErrNotFound):
		return notFound()
	case errors.Is(err, ErrSiteExists):
		return oneSitePerInstall()
	case errors.Is(err, ErrNeedsSite):
		return validation(
			map[string]string{"siteId": "Required: this controller is not bound to a site yet, so there is no default."},
			map[string]gen.FieldFault{"siteId": gen.FieldFaultRequired},
		)
	case errors.Is(err, ErrConflict):
		return duplicateField("name")
	default:
		return internalProblem()
	}
}
