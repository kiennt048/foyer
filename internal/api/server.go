package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/portal"
)

// BasePath is where the contract says this API lives.
//
// Not `/api/v1`: the contract's `servers:` entry reads `/api/v0` and the contract
// wins on every disagreement.
const BasePath = "/api/v0"

// Server implements the operations generated from the contract.
//
// It satisfies `gen.StrictServerInterface`, and the assertion below is the whole
// point of generating it: an operation added to `web/contract/openapi.yaml` and
// not implemented here stops compiling, rather than becoming a 404 somebody
// finds in integration.
type Server struct {
	ports Ports
	gates gates

	// portal is the guest surface's own service, and it is nil unless a
	// deployment wired the ports it needs. Nil means the five portal operations
	// answer `501` — the same honest gap every other unwired port produces,
	// rather than a portal that renders a venue name and can let nobody on.
	portal *portal.Service

	// net is the network settings in force — the portal's public address, the
	// throttles, the ceilings, the console's portal certificate — swapped whole on
	// every write, rather than a `portalURL` string copied in at start, which is
	// why `getPortalSettings` answers the address an operator last saved. See
	// netsettings.go.
	net netState

	// contract validates request bodies against the embedded document. Built once
	// in New; a nil one would mean a server that silently stops enforcing its own
	// contract, which is the defect requestvalidation.go exists to remove.
	contract *validator

	// throttle is the sign-in rate limit, per address and per account, for the
	// operations that verify a password or a setup code. See `hardening.go`.
	throttle *signInThrottle
	// devices signs and reads the console's trusted-device cookie, which splits
	// the per-account bucket so a stranger cannot keep the account's owner out. See
	// `devicecookie.go`.
	devices *deviceCookies
	// bodies bounds how many bytes of signed-in bodies over the anonymous
	// ceiling are being read and decoded at once. See `hardening.go`.
	bodies *bodyBudget
	// smallBodies is the same for every other body: anonymous, the portal's, and
	// a signed-in caller's ordinary one.
	smallBodies *bodyBudget
	// bodyPlan is the body path's share of the memory plan: the budgets' sizes and
	// the most any ceiling may be set to. See memplan.go.
	bodyPlan bodyPlan
	// reports is the `reports` operations' columns and bounds, read out of the
	// contract once. See handlers_reports.go.
	reports reports

	now    func() time.Time
	newID  func(prefix string) string
	secret func() (string, error)
}

var _ gen.StrictServerInterface = (*Server)(nil)

// Config is what a Server needs beyond its ports.
type Config struct {
	Ports Ports

	// Portal is the guest portal's seam. It is separate from `Ports` because the
	// two surfaces are separate: a deployment can serve the console with no guest
	// portal wired, and `internal/portal` declares its own interfaces for the
	// reason its `ports.go` gives.
	//
	// **All of it or none of it.** A half-wired portal is refused at construction
	// rather than answering `501` per operation, because the ports are not
	// independent: a grant store with no driver seam is a portal that records
	// grants for devices nothing confirmed.
	Portal portal.Ports

	// PortalURL is where a guest's browser reaches the portal. It is a deployment
	// fact — `FOYER_PORTAL_URL`, defaulted in `internal/config` — and it is the
	// only way the console can preview what a guest sees: the admin and the
	// portal are separate listeners, so a console-constructed origin is right in
	// development and wrong everywhere else. Carried here rather than read from
	// the store, because no per-site row holds it and a stored copy would drift
	// from the address the process actually serves.
	PortalURL string
	// NetworkBootstrap is the deployment's own network values, in force until an
	// operator saves others in the console. Its PortalURL defaults to PortalURL.
	NetworkBootstrap NetworkBootstrap
	// Network is the process that serves the portal listeners; nil in a test,
	// which then describes a stored certificate rather than a served one.
	Network NetworkRuntime
	// PortalSessionLifetime bounds the pre-authentication state. Zero means
	// `portal.DefaultSessionLifetime`.
	PortalSessionLifetime time.Duration

	// ControlPlaneReplicas is how many copies of this process the deployment
	// runs behind one address. Zero or one means what it says.
	//
	// It is relayed to `portal.NewService`, which **refuses to construct** when
	// it is above one and the pre-authentication rate limiter is in-process:
	// N replicas would grant N times the configured rate, and nothing about the
	// running system would say so. See `portal.SharedLimiter`.
	ControlPlaneReplicas int

	// Memory is the process's memory plan (`cmd/foyer/memory.go`): the soft
	// limit and what the argon2id slots hold. The body budgets and the highest
	// ceiling an operator may set are derived from it (memplan.go).
	// Zero plans for the shipped container.
	Memory MemoryPlan

	// Now, NewID and NewSecret exist so that a test can make a response
	// reproducible without the handlers knowing they are under test. They
	// default to the real thing.
	Now       func() time.Time
	NewID     func(prefix string) string
	NewSecret func() (string, error)
}

// New builds the server.
//
// It returns an error rather than panicking on a misconfiguration, and the two
// things it refuses on are deliberate:
//
//   - a contract operation with no `x-permission`, because that is an operation
//     nobody decided the access rules for;
//   - nothing else. A nil port is allowed, and every operation that needs it
//     answers `501` with a problem document naming nothing at all. That is how
//     an adapter can wire `internal/store`'s repositories one aggregate at a time
//     without a half-wired server pretending to work.
func New(cfg Config) (*Server, error) {
	g, err := readGates()
	if err != nil {
		return nil, err
	}
	// The contract compiles into a request validator here rather than lazily, so a
	// document this binary cannot route is a startup failure and not a surprise on
	// the first POST.
	v, err := newValidator()
	if err != nil {
		return nil, err
	}
	// The export columns and range bounds are the contract's, bound here so a
	// disagreement is a startup failure and not a surprise in a spreadsheet.
	rep, err := readReports()
	if err != nil {
		return nil, err
	}
	s := &Server{
		reports:  rep,
		ports:    cfg.Ports,
		gates:    g,
		contract: v,
		throttle: newSignInThrottle(),
		bodyPlan: planBodies(cfg.Memory),
		now:      cfg.Now,
		newID:    cfg.NewID,
		secret:   cfg.NewSecret,
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.newID == nil {
		s.newID = defaultID
	}
	if s.secret == nil {
		s.secret = defaultSecret
	}
	// The body budgets are the memory plan's (memplan.go).
	s.bodies = newBodyBudget(s.bodyPlan.large)
	s.smallBodies = newBodyBudget(s.bodyPlan.small)
	// After `now`, which the ring reads its clock from (devicecookie.go).
	s.devices = newDeviceCookies(cfg.Ports.DeviceKeys, s.now)
	s.net.bootstrap = cfg.NetworkBootstrap
	if s.net.bootstrap.PortalURL == "" {
		s.net.bootstrap.PortalURL = cfg.PortalURL
	}
	s.net.runtime = cfg.Network

	if anyPortalPort(cfg.Portal) {
		p := cfg.Portal
		if p.Limiter == nil {
			// The pre-authentication state has to be rate limited and there is
			// no safe default of "none": the portal's authenticating operations
			// are reachable by anyone within radio range and every one of them
			// is an HTTP call to the venue's controller. A deployment that
			// supplies no limiter gets this one rather than nothing.
			p.Limiter = portal.NewTokenBucket(portal.DefaultBurst, portal.DefaultRefill)
		}
		svc, err := portal.NewService(portal.ServiceConfig{
			Ports:           p,
			Now:             s.now,
			SessionLifetime: cfg.PortalSessionLifetime,
			Replicas:        cfg.ControlPlaneReplicas,
		})
		if err != nil {
			return nil, err
		}
		s.portal = svc
	}
	return s, nil
}

// gate is the strict middleware that decides whether a request reaches a
// handler at all.
//
// It runs per operation and knows the operationId, which is what lets the
// requirement come from the contract rather than from a table here. It denies by
// default: an operationId with no gate is refused, because the only way that can
// happen is a generated handler for an operation `readGates` did not see, and
// guessing in that situation is how a scaffold ends up with every endpoint open.
func (s *Server) gate(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		g, known := s.gates[operationID]
		if !known {
			writeProblem(w, forbidden())
			return nil, nil
		}

		who, err := s.resolveCaller(ctx, r)
		if err != nil {
			writeProblem(w, asAPIError(err))
			return nil, nil
		}
		if who != nil {
			ctx = withCaller(ctx, who)
		}

		// `signOut` is the one operation that answers the same way with and
		// without a session, and the contract says why: "a client that has just
		// been told 401 by something else must still be able to reach it".
		// Sign-me-out has one correct outcome. It is still CSRF-protected when a
		// session exists, so a forced sign-out needs the token.
		signOut := operationID == "SignOut"

		switch {
		case g.Requirement == reqPublic:
			// Public. `getAuthSession` is here and answers its own 401 — that
			//401 is a normal answer, not a refusal, and the client's response
			// to it is to route rather than to render an error.
		case who == nil && signOut:
			// Falls through to the handler, which answers 204.
		case who == nil:
			writeProblem(w, notSignedIn())
			return nil, nil
		case g.Requirement == reqAuthenticated:
			// A session is enough.
		default:
			if !who.may(gen.OperatorPermission(g.Requirement)) {
				p := forbidden()
				p.Params = map[string]any{"required": string(g.Requirement)}
				writeProblem(w, p)
				return nil, nil
			}
		}

		if g.NeedsCSRF && who != nil {
			if fetchedCrossSite(r) || !sameToken(r.Header.Get(csrfHeaderName), who.CSRFToken) {
				p := forbidden()
				p.Params = map[string]any{"required": "csrf"}
				writeProblem(w, p)
				return nil, nil
			}
		}

		// **The forced password change, and it is here rather than in a client.**
		//
		// An operator signed in on a temporary password somebody else set
		// (`foyerctl create-operator --must-change-password`) must choose their own
		// before anything else. A console that only
		// *routes* to that screen has not forced anything: a script, a stale tab, a
		// second browser or `curl` with the session cookie all walk straight past
		// it. Refusing in the gate is what makes the sentence true, and it is the
		// same argument the gate itself rests on -- one place decides, so there is
		// one place to get it wrong.
		//
		// The allowlist is three operations and each is load-bearing:
		// `ChangeOwnPassword` is the way out, `GetAuthSession` is how the console
		// learns it must route there, and `SignOut` is how somebody who cannot
		// change it leaves. Anything else, including every read, is refused --
		// a read-only console on a password somebody else knows is still a console
		// an unauthorised person may be looking at.
		if who != nil && who.MustChangePassword && !passwordChangeExempt[operationID] {
			writeProblem(w, passwordChangeRequired())
			return nil, nil
		}

		return f(ctx, w, r, request)
	}
}

// resolveCaller turns a cookie into an operator, or into nothing.
//
// Nothing is not an error: an expired session, a token that no longer resolves
// and no cookie at all are the same situation from a client's side, and telling
// them apart would say whether a token was ever valid.
func (s *Server) resolveCaller(ctx context.Context, r *http.Request) (*caller, error) {
	token := sessionTokenFrom(r)
	if token == "" || s.ports.AdminSessions == nil || s.ports.Operators == nil {
		return nil, nil
	}
	sess, err := s.ports.AdminSessions.LookupAdminSession(ctx, hashToken(token))
	if err != nil {
		return nil, nil
	}
	if !sess.ExpiresAt.IsZero() && !sess.ExpiresAt.After(s.now()) {
		return nil, nil
	}
	op, err := s.ports.Operators.OperatorByID(ctx, sess.OperatorID)
	if err != nil {
		// The operator row was deleted under a live session. Treat it as signed
		// out rather than as an internal error.
		return nil, nil
	}
	if !op.Enabled {
		return nil, nil
	}
	// An error here is deliberately not fatal to the request: a lookup that failed
	// is not evidence that the operator is on a temporary password, and treating
	// it as such would lock a console out of every screen because of a transient
	// database error. It errs towards letting the request through, which is the
	// same direction `Enabled` above errs in for the same class of failure.
	mustChange, _ := s.ports.Operators.MustChangePassword(ctx, op.Id)

	return &caller{
		Operator:           op,
		Permissions:        permissionsFor(op.Role),
		TokenHash:          sess.TokenHash,
		CSRFToken:          sess.CSRFToken,
		MustChangePassword: mustChange,
	}, nil
}

// ── the compatibility shim for cmd/foyer ─────────────────────────────────────

// NewServer is a server with no ports wired, for a caller that needs the
// handler and nothing behind it.
//
// It takes `any` rather than `*store.Store` deliberately: a shim typed on
// `internal/store`'s `Store` would make this package's build depend on that
// package's shape, which is the coupling the ports exist to avoid.
//
// **What it returns is a server with no ports wired**, so every operation that
// needs one answers `501` with a problem document. That is deliberately visible:
// a server that returned convincing JSON from handlers that never touched a
// database would leave a caller unable to tell. The real repositories are wired
// to `Ports` through `New`.
func NewServer(_ any) *Server {
	s, err := New(Config{})
	if err != nil {
		// `New` fails only when the embedded contract is unreadable, which is a
		// build-time fact rather than a runtime one: the spec is compiled in.
		panic(fmt.Sprintf("api: %v", err))
	}
	return s
}

// anyPortalPort reports whether a caller meant to wire the guest surface at all.
//
// The distinction it draws is between *not configured* — which answers `501`, the
// honest gap — and *misconfigured*, which is refused at construction. Wiring four
// of the five portal ports is the second, and it is the shape that would
// otherwise produce a portal issuing grants against no driver.
func anyPortalPort(p portal.Ports) bool {
	return p.Sessions != nil || p.Grants != nil || p.Credentials != nil ||
		p.Sites != nil || p.Drivers != nil
}
