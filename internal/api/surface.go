package api

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// One contract, two listeners.
//
// `docs/rules/security.md` §7 puts the admin console and the guest portal on
// **separate listeners**, and the reason is not tidiness: the portal is reachable
// by every unauthenticated device within radio range, before any of them has a
// route to anywhere, and the console is reachable by nobody who is not already
// signed in. `cmd/foyer` binds them on different addresses and only the portal is
// bound wide.
//
// The contract is one document covering both, so the split has to happen here.
// It happens by **operation tag, read out of the contract**, not from a list in
// Go: an operation carrying the `portal` tag belongs to the guest surface and
// every other operation belongs to the console's. That is the same derivation
// `csrfApplies` uses for the same reason — a list is a second place for the rule
// to live and the copies are what drift.
//
// An operation asked for on the wrong surface answers `404`, which is what it
// is: on that listener there is no such route. It is deliberately not `403`,
// which would say the route exists and you may not have it.

type surface int

const (
	surfaceAdmin surface = iota
	surfacePortal
)

// Handler is the admin console's surface, mounted at the contract's base path.
//
// The middleware chain is short on purpose, and one thing is absent on purpose:
// there is **no CORS middleware**. The console is same-origin — the contract's
// `servers:` entry says so. `Access-Control-Allow-Origin: *` beside
// `Access-Control-Allow-Credentials: true` is a pair browsers reject, which is the
// only thing that would keep it from being a vulnerability. Reintroducing CORS
// here would be a decision, not a fix.
func (s *Server) Handler() http.Handler { return s.handler(surfaceAdmin) }

// PortalHandler is the guest surface: the five operations tagged `portal`, and
// nothing else.
//
// It carries one middleware the console's does not — `guestSession`, which
// resolves the browser-to-MAC binding — and that is the second reason the split
// is real rather than presentational. The console's operations must never see a
// guest session and the portal's operations have no other way to learn a MAC.
func (s *Server) PortalHandler() http.Handler { return s.handler(surfacePortal) }

func (s *Server) handler(sf surface) http.Handler {
	// **Innermost first.** The generated handler wraps in slice order — `handler
	// = middleware(handler, id)` — so the last entry ends up outermost. Writing
	// it in the reading order of the chain would put the surface check *inside*
	// the gate, and a console operation asked for on the guest listener would
	// then answer `401` instead of `404`: a refusal that says the route exists,
	// on the listener bound to `0.0.0.0`. That is the wrong answer.
	//
	// Outermost to innermost: onSurface → gate → guestSession → handler. So the
	// slice reads the other way.
	var middleware []gen.StrictMiddlewareFunc
	if sf == surfacePortal {
		middleware = append(middleware, s.guestSession)
	}
	// `throttleSignIn` sits inside the stashed contract fault and outside the
	// guest session: a refused sign-in costs neither a token nor a session lookup,
	// and the decision is made before any password is hashed (`hardening.go`).
	middleware = append(middleware, s.throttleSignIn, s.answerContractFault, s.gate, s.onSurface(sf))

	strict := gen.NewStrictHandlerWithOptions(
		s,
		middleware,
		gen.StrictHTTPServerOptions{
			// A handler that returns an error never reaches a client with the
			// error's text. `asAPIError` funnels everything unrecognised into
			// `/problems/internal` with no detail — §9: no stack traces, no
			// internal paths, no database errors.
			ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				problem := asAPIError(err)
				// §9 keeps the cause away from the CLIENT. It must not keep it
				// away from the OPERATOR as well: without this, a failure
				// while registering a controller answers
				// `{"status":500,"title":"Something went wrong"}` and the
				// container log has nothing in it at all — not the operation,
				// not the error, not a line. A 500 nobody can diagnose is worse
				// than a stack trace nobody should see.
				//
				// Only the internal case is logged. A 404 or a 422 is the API
				// working, and logging those turns the log into a request log
				// that an operator stops reading.
				if problem.Status >= 500 {
					slog.Error("api: request failed",
						"method", r.Method,
						"path", r.URL.Path,
						"status", problem.Status,
						"err", err)
				}
				writeProblem(w, problem)
			},
			RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				// A body that stopped at the listener's ceiling is not a malformed
				// one. The validator answers this for every route it knows; this is
				// the same answer for a body read by the generated decoder instead.
				if problem, ok := bodyTooLarge(err); ok {
					writeProblem(w, problem)
					return
				}
				// A body that will not decode. The error names the JSON offset
				// and sometimes the field, so it is not carried across.
				writeProblem(w, validation(
					map[string]string{"body": "Could not be read."},
					map[string]gen.FieldFault{"body": gen.FieldFaultRequired},
				))
			},
		},
	)

	return gen.HandlerWithOptions(strict, gen.StdHTTPServerOptions{
		BaseURL:    BasePath,
		BaseRouter: http.NewServeMux(),
		// The contract validates its own request bodies before a handler sees one.
		//
		// **Innermost first**, the same as the strict list above. The generated
		// wrapper runs `handler = middleware(handler)` in slice order, so the LAST
		// entry is outermost. Written the other way round, as
		// `{securityHeaders, limitBody, validator}`, it would put the validator
		// outside the security headers — its own direct refusals would go out
		// without them — and run `limitBody` *after* the validator, which would
		// read a 64 MiB streamed body to the end before the ceiling was ever
		// consulted. `TestAStreamedBodyStopsAtTheCeiling` pins it; a
		// Content-Length test alone cannot see it.
		//
		// So, outermost to innermost: security headers, then `limitBody`, then
		// the validator — which is the first thing that reads a body.
		Middlewares: []gen.MiddlewareFunc{s.contract.middleware, s.limitBody(sf), securityHeaders},
		// A missing required query parameter, or one that will not parse. The
		// contract does not declare a response for it on most operations, so
		// this is the API's own answer and it uses a `ProblemType` the contract
		// does declare.
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeProblem(w, validation(
				map[string]string{"query": "Missing or unreadable."},
				map[string]gen.FieldFault{"query": gen.FieldFaultRequired},
			))
		},
	})
}

// onSurface refuses an operation that does not belong to this listener.
//
// It denies by default in the same way `gate` does: an operationId `readGates`
// did not see is not on any surface, and the only way that happens is a generated
// handler for an operation the contract does not describe.
func (s *Server) onSurface(sf surface) gen.StrictMiddlewareFunc {
	return func(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
		return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
			g, known := s.gates[operationID]
			if !known || surfaceFor(g) != sf {
				writeProblem(w, notFound())
				return nil, nil
			}
			return f(ctx, w, r, request)
		}
	}
}

func surfaceFor(g operationGate) surface {
	if g.Guest {
		return surfacePortal
	}
	return surfaceAdmin
}

// securityHeaders is the hygiene §9 asks for on an API that answers JSON to a
// browser. Nothing here is a substitute for the console's own CSP; these are the
// ones that matter for a JSON endpoint carrying an authenticated session, and for
// a portal endpoint answering a client whose whole data store is ephemeral.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		// Nothing this API answers may be cached: every response is scoped to a
		// session and a site, and the portal's are scoped to one device's MAC.
		// The contract requires `Cache-Control: no-store` on the portal's `303`
		// by name, so this is also the header that satisfies `PortalRedirect`.
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
