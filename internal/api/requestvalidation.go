package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/legacy"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/portal"
)

// Request validation against the embedded contract.
//
// WHY THIS EXISTS, and it is a reproduced failure rather than a tidiness one.
//
// Without it, on a fresh install, two operations answer `500` from a
// PostgreSQL CHECK constraint where the contract declares `422`:
//
//	POST /voucher-batches  ->  voucher_batches_expiry_mode_check
//	POST /controllers      ->  controllers_tls_mode_check
//
// The first omitted `expiryMode`, which `VoucherBatchCreate` lists as REQUIRED.
// The second sent a `tlsMode` outside the `TlsMode` enum. Neither was refused,
// because oapi-codegen emits `type TlsMode string` and required fields as plain
// values — so a missing field is the zero value and a bad enum is just a string.
// Both reached the database, and the operator saw "Something went wrong".
//
// Hand-written checks for SOME fields of the same request — `label`, `siteId`,
// `count` — are the worst arrangement available: it looks like the boundary
// validates, so the next field added is assumed covered and is not. This is
// `docs/rules/frontend.md`'s *"the mock does not validate requests"* on the Go
// side, and `docs/rules/database.md`'s restated-constraint defect at the same
// time: the contract states a rule, the schema states it again, and the code
// between them enforces neither.
//
// The contract is the only authority, it is already embedded for the generated
// server, and `kin-openapi` is already a dependency. So the document validates its
// own requests, and a rule added to the contract is enforced without anybody
// writing a matching `if`.
//
// WHAT IT DELIBERATELY DOES NOT DO: it does not validate responses. That is a real
// gap; validating requests is the half that protects the database, and it is the
// half whose absence a fresh install reproduces.

// validator holds the routing table built once from the embedded spec.
type validator struct {
	router routers.Router
}

// newValidator compiles the embedded contract into a router.
//
// It returns an error rather than falling back to "validate nothing": a server that
// silently stops enforcing its own contract is the shape this file exists to
// remove, and it must not be reintroduced by the thing that removes it.
func newValidator() (*validator, error) {
	spec, err := gen.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("api: reading the embedded contract: %w", err)
	}
	// The document declares `servers: [{url: /api/v0}]`, and the router matches on
	// the full path including that prefix.
	// `legacy`, not `gorillamux`: the gorillamux router pulls github.com/gorilla/mux,
	// which is a module this project does not have, and go.mod is kept to the
	// dependencies it already has. The legacy router imports
	// only kin-openapi's own packages and matches the same paths.
	router, err := legacy.NewRouter(spec)
	if err != nil {
		return nil, fmt.Errorf("api: building a router from the contract: %w", err)
	}
	return &validator{router: router}, nil
}

// middleware refuses a request the contract does not permit, with the problem
// document the contract itself declares for that case.
func (v *validator) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only bodies are validated. Path and query parameters already go through
		// the generated binder, which answers 400 on its own, and running both
		// produces two different documents for one fault.
		if r.Body == nil || r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}

		route, pathParams, err := v.router.FindRoute(r)
		if err != nil {
			// Not an operation this contract describes. The surface router answers
			// that — 404 for the wrong surface, per `surface.go` — and inventing a
			// different answer here would hide it.
			next.ServeHTTP(w, r)
			return
		}

		// The body has to be read to be validated and read again to be handled.
		//
		// `limitBody` has already wrapped it in `http.MaxBytesReader`, so this read
		// stops at the listener's ceiling instead of at the end of whatever a
		// caller chose to send. Without it this would be an unbounded `io.ReadAll`,
		// on the portal listener too, before anything had looked at the request.
		body, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if problem, tooLarge := bodyTooLarge(err); tooLarge {
			writeProblem(w, problem)
			return
		}
		if err != nil {
			writeProblem(w, validation(
				map[string]string{"body": "Could not be read."},
				map[string]gen.FieldFault{"body": gen.FieldFaultRequired},
			))
			return
		}
		// Weighed at its real size before anything decodes it (memplan.go): the
		// reserve `limitBody` took for its length is settled to
		// what this body's shape costs, and a body no budget could hold is refused
		// here rather than decoded "alone".
		if res := reservationOf(r.Context()); res != nil {
			weight := bodyWeight(body, r.Header.Get("Content-Type"))
			if !res.settle(weight) {
				fits := res.budget.total * int64(len(body)) / weight
				writeProblem(w, payloadTooLarge(int64(len(body)), fits))
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))

		input := &openapi3filter.RequestValidationInput{
			Request:    r,
			PathParams: pathParams,
			Route:      route,
			Options: &openapi3filter.Options{
				// Authentication is this server's own, not the document's.
				AuthenticationFunc: func(_ context.Context, _ *openapi3filter.AuthenticationInput) error { return nil },
				// **Validate; never rewrite.** By default kin-openapi fills a
				// missing property that declares a `default` and then RE-ENCODES
				// the body — and it has an encoder for JSON only. `Language`
				// declares `default: en`, so every portal form posted without
				// `lang` failed the rewrite ("rewriting failed") and the guest was
				// answered `422` JSON inside the CNA instead of a page. The
				// rewritten body was never used either way: the original bytes are
				// put back below, and every handler applies its own default
				// (`languageOf`). Skipping it changes nothing a handler sees.
				SkipSettingDefaults: true,
			},
		}
		if err := openapi3filter.ValidateRequest(r.Context(), input); err != nil {
			// STASHED, not answered here.
			//
			// This middleware has to run at the HTTP level, because the raw body is
			// gone by the time a strict middleware sees the request. But the
			// authentication gate IS a strict middleware, so answering here would
			// put 422 in front of 401 — and an anonymous caller would learn the
			// body shape of an operation they may not reach.
			//
			// So the fault waits in the context and `answerContractFault`, placed
			// inside the gate, emits it once the caller has been let through.
			r = r.WithContext(context.WithValue(r.Context(), contractFaultKey{}, contractFault(err)))
		}

		r.Body = io.NopCloser(bytes.NewReader(body))
		next.ServeHTTP(w, r)
	})
}

// contractFault turns a kin-openapi complaint into the project's own problem
// document, naming the field wherever the library named it.
//
// The field name matters more than the sentence: `web/admin` renders a fault per
// field, and a validation answer with no field is one the console cannot put beside
// the input that caused it.
func contractFault(err error) *apiError {
	errs := map[string]string{}
	faults := map[string]gen.FieldFault{}

	var reqErr *openapi3filter.RequestError
	if errors.As(err, &reqErr) {
		var schemaErr *openapi3.SchemaError
		if errors.As(reqErr, &schemaErr) {
			field := strings.Join(schemaErr.JSONPointer(), ".")
			if field == "" {
				field = "body"
			}
			switch schemaErr.SchemaField {
			case "required":
				// kin-openapi reports the OWNER of the missing property, and names
				// the property in its reason. The console needs the property.
				if name := missingPropertyName(schemaErr.Reason); name != "" {
					field = name
				}
				errs[field] = "Required."
				faults[field] = gen.FieldFaultRequired
			// The contract's FieldFault vocabulary is deliberately smaller than the
			// library's error set — the console renders a message per fault, so a
			// fault it has no copy for is a blank field label. Everything the
			// contract does not name maps to `incorrect`, which it does.
			case "enum":
				errs[field] = "Not one of the permitted values."
				faults[field] = gen.FieldFaultIncorrect
			case "minimum", "exclusiveMinimum", "minLength", "minItems":
				errs[field] = "Below the permitted minimum."
				faults[field] = gen.FieldFaultBelowMinimum
			case "maximum", "exclusiveMaximum", "maxLength", "maxItems":
				errs[field] = "Above the permitted maximum."
				faults[field] = gen.FieldFaultTooLarge
			default:
				errs[field] = "Not valid for this field."
				faults[field] = gen.FieldFaultIncorrect
			}
			return validation(errs, faults)
		}
	}

	// Anything the library reports that is not a schema fault — a malformed body,
	// an unsupported media type. One document, named on the body.
	return validation(
		map[string]string{"body": "Could not be read."},
		map[string]gen.FieldFault{"body": gen.FieldFaultRequired},
	)
}

// missingPropertyName pulls the property out of kin-openapi's own phrasing,
// which is `property "expiryMode" is missing`.
//
// Parsing a library's message is fragile and is done anyway, with a fallback that
// is correct rather than wrong: when the shape changes, the fault is reported
// against the object instead of the field, which is less useful and not incorrect.
func missingPropertyName(reason string) string {
	const marker = `property "`
	i := strings.Index(reason, marker)
	if i < 0 {
		return ""
	}
	rest := reason[i+len(marker):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// contractFaultKey carries a stashed validation fault from the HTTP-level
// validator to the strict middleware that answers it.
type contractFaultKey struct{}

// answerContractFault emits a stashed contract fault, and is placed INSIDE the
// authentication gate so that 401 always precedes 422.
//
// **A guest's form is answered with a page, never with a problem document.** The
// six portal operations are document navigations from a `<form>` inside the
// Captive Network Assistant (`PortalRedirect`): a `422 application/problem+json`
// there renders raw JSON in a mini-browser with no back button, and it is not a
// navigation the CNA treats as a captivity re-check. So a request the contract
// refuses — `lang=xx`, a missing `code` — goes back to the form it came from with
// `generic`, the same answer a venue-side failure gets, in the language the form
// named if that is one the portal speaks.
func (s *Server) answerContractFault(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, args any) (any, error) {
		if fault, ok := ctx.Value(contractFaultKey{}).(*apiError); ok && fault != nil {
			if page, form := guestFormPage[operationID]; form {
				return portalRedirect{location: portal.Failure(page,
					portal.Language(r.PostFormValue("lang")), portal.ReasonGeneric, "")}, nil
			}
			return nil, fault
		}
		return f(ctx, w, r, args)
	}
}

// guestFormPage is where each guest form operation sends a guest back to. Every
// operation whose request body is `application/x-www-form-urlencoded` must be in
// it — `TestEveryGuestFormHasAPageToReturnTo` derives that set from the contract.
var guestFormPage = map[string]portal.Page{
	"RedeemVoucher":               portal.PageVoucher,
	"AcceptTerms":                 portal.PageWelcome,
	"LoginWithAccount":            portal.PageAccount,
	"RequestPhoneCode":            portal.PagePhone,
	"VerifyPhoneCode":             portal.PagePhone,
	"WithdrawOwnMarketingConsent": portal.PageConnected,
}
