package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// The console's own authentication, and the two gates in front of every
// operation the contract does not mark `security: []`.
//
// The code this replaces called `next.ServeHTTP` when no session resolved, which
// left **every endpoint open**, had no CSRF at all, and sent
// `Access-Control-Allow-Origin: *` beside `Access-Control-Allow-Credentials:
// true`. Three separate decisions here exist because of that:
//
//   - **Deny by default.** `requirementFor` returns a requirement for every
//     operation or the server refuses to start. There is no path through this
//     file that continues to a handler without either a session or an operation
//     the contract itself marked public.
//   - **The requirement is read from the contract, not from a list here.** An
//     operation whose `x-permission` changes needs no edit in Go, and an
//     operation that declares none fails at construction. `web/mock` derives its
//     gate the same way from the same annotations.
//   - **No CORS.** The console is same-origin — the contract's `servers:` entry
//     says so in as many words — so there is no CORS middleware at all rather
//     than a narrowed one. A header that is not emitted cannot be widened by
//     somebody in a hurry.

const (
	// sessionCookieName is `foyer_admin_session` under the `__Host-` prefix.
	//
	// The prefix is not decoration: it is enforced by the user agent rather than
	// by us. A cookie named `__Host-…` is refused unless it is `Secure`, has
	// `Path=/` and carries **no `Domain`**, which is what makes it impossible for
	// a sibling host — `guest.example.com`, the portal, reachable
	// pre-authentication by anyone within radio range — to set a cookie the
	// console will read. `docs/rules/security.md` §7 already puts the console and
	// the portal on separate listeners; this is the same boundary asserted by the
	// browser.
	//
	// `securitySchemes.sessionCookie.name` in the contract carries the same
	// prefixed name, and `TestSessionCookieNameIsTheContractsName` reads it out of
	// the embedded document rather than restating it, so the two cannot part
	// company from either side.
	sessionCookieName = "__Host-foyer_admin_session"

	// csrfHeaderName is the header the token is echoed in.
	csrfHeaderName = "X-Foyer-CSRF"

	sessionLifetime = 12 * time.Hour
)

// requirement is what an operation demands of a caller. The two values that are
// not permissions are spelled the way the contract spells them.
type requirement string

const (
	reqPublic        requirement = "public"
	reqAuthenticated requirement = "authenticated"
)

// operationGate is everything this package needs to know about one operation,
// and all of it comes from the contract.
type operationGate struct {
	// Requirement is `x-permission`.
	Requirement requirement
	// NeedsCSRF is derived rather than annotated, exactly as the contract's
	// `csrfToken` scheme says it must be: "The requirement cannot vary by
	// operation: it follows from the method and from whether the operation is
	// guest-facing, both of which are already in this document."
	//
	// Deriving it is the difference between honouring that sentence and copying
	// a list out of it. A list is a second place for the rule to live, and the
	// copies are what drift.
	NeedsCSRF bool

	// Guest is the `portal` tag, and it decides which listener the operation is
	// reachable on — see `surface.go`. Read from the contract rather than
	// listed here, so that an operation joining or leaving the guest surface is
	// a change to `openapi.yaml` and not a change to Go that somebody forgets.
	Guest bool

	Method      string
	PathPattern string
}

// gates indexes every operation in the contract by operationId.
type gates map[string]operationGate

// readGates computes the gate table from the embedded contract.
//
// It returns an error rather than defaulting, for the same reason `web/mock`
// throws at import: an operation with no `x-permission` is an operation nobody
// decided the access rules for, and the safe-looking default — deny — would hide
// the omission behind a 403 somebody debugs for an hour.
func readGates() (gates, error) {
	spec, err := gen.GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("api: reading the embedded contract: %w", err)
	}

	out := gates{}
	var missing []string
	for pattern, item := range spec.Paths.Map() {
		for method, op := range item.Operations() {
			if op.OperationID == "" {
				missing = append(missing, method+" "+pattern+" (no operationId)")
				continue
			}
			raw, ok := op.Extensions["x-permission"]
			if !ok {
				missing = append(missing, fmt.Sprintf("%s %s (%s)", method, pattern, op.OperationID))
				continue
			}
			text, ok := raw.(string)
			if !ok || text == "" {
				missing = append(missing, fmt.Sprintf("%s %s (%s)", method, pattern, op.OperationID))
				continue
			}

			out[op.OperationID] = operationGate{
				Requirement: requirement(text),
				NeedsCSRF:   csrfApplies(method, op),
				Guest:       slices.Contains(op.Tags, "portal"),
				Method:      method,
				PathPattern: pattern,
			}
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, fmt.Errorf(
			"api: these operations declare no x-permission in the contract: %s",
			strings.Join(missing, ", "))
	}
	return out, nil
}

// csrfApplies is the `csrfToken` scheme's rule, derived from the document.
//
// Three clauses, and the third is an interpretation this package makes and
// reports rather than one the contract states:
//
//   - the method changes state — POST, PUT, PATCH, DELETE — **or the operation is
//     a download**: a GET whose answer is a `text/csv` document. An export writes a
//     `data-exported` audit entry and hands personal data out of the product, and
//     it is a GET the browser sends with the `SameSite=Lax` cookie on a cross-site
//     top-level navigation — so without the header a link on any page would make a
//     signed-in administrator download it and put an entry in the audit log in
//     their name. The attacker cannot read the file; the act and the record are
//     still the administrator's without their intent. The console fetches every
//     export, so the header costs it nothing;
//   - the operation is not guest-facing (the `portal` tag). A Captive Network
//     Assistant cannot be given a token to echo, and the portal's defence is
//     `VerifyPresence` and a MAC-bound grant instead;
//   - the operation requires a session at all. `signIn` is a POST outside the
//     `portal` tag, and a literal reading demands a token on it — but the only
//     channel that issues one is the response to `signIn` itself, so the literal
//     reading is unimplementable. Every operation marked `security: []` is
//     exempt on that ground, which today is exactly `signIn` and the five portal
//     operations.
func csrfApplies(method string, op *openapi3.Operation) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	case http.MethodGet:
		if !isDownload(op) {
			return false
		}
	default:
		return false
	}
	if slices.Contains(op.Tags, "portal") {
		return false
	}
	// `security: []` — present and empty — is how the contract marks an
	// operation that needs no session. Absent means it inherits the document's
	// `security:` and does need one.
	if op.Security != nil && len(*op.Security) == 0 {
		return false
	}
	return true
}

// isDownload is an operation whose success is a `text/csv` document rather than a
// JSON answer — the exports.
func isDownload(op *openapi3.Operation) bool {
	if op.Responses == nil {
		return false
	}
	ok := op.Responses.Status(http.StatusOK)
	return ok != nil && ok.Value != nil && ok.Value.Content.Get("text/csv") != nil
}

// fetchedCrossSite reports a request the browser says came from another site.
//
// `Sec-Fetch-Site` is set by the browser and cannot be set by a page (it is a
// forbidden header name), so it is the one statement about a request's origin a
// cross-site page cannot forge. OWASP's CSRF cheat sheet recommends exactly this
// resource-isolation check as defence in depth beside the token. `same-site` is
// refused as well as `cross-site`: the guest portal is the same site as the console
// (one host, another port) and anyone in radio range reaches it. An absent header is
// a client that is not a current browser — `curl`, the test harness — and the token
// is still required of it.
func fetchedCrossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	}
	return false
}

// roleGrants is the deployment-wide role → permission table.
//
// It is duplicated from `web/contract/src/index.ts` (`ROLE_PERMISSIONS`) rather
// than derived, because it is not in `openapi.yaml` — the contract says so
// explicitly and says why: it "follows from a product decision, is identical in
// every deployment, and is what the *server* implements". This is that server.
// `security_test.go` pins the table against the `OperatorPermission` enum, so a
// permission added to the contract and to no role fails here.
//
// The split between `:read` and `:manage` exists only where the two roles
// actually differ — with one exception both roles hold, `sessions:manage`, whose
// reason is written on `OperatorPermission` in the contract: a permission named
// *read* must never reach a control that throws a device off the network.
//
// That staff may *read* the network configuration is the narrow reading and is
// deliberate: a staff member who cannot see that shared secrets exist cannot
// tell an administrator that one needs rotating.
var roleGrants = map[gen.OperatorRole][]gen.OperatorPermission{
	gen.Administrator: {
		gen.SessionsRead, gen.SessionsManage, gen.VouchersManage, gen.AccountsManage,
		gen.NasRead, gen.NasManage, gen.GardenRead, gen.GardenManage,
		gen.PortalRead, gen.PortalManage, gen.OperatorsManage, gen.AuditRead,
		gen.PrivacyManage,
		// Administrators only: a bulk offline copy of personal data outlives the
		// session and cannot be recalled, which reading a page of it on screen
		// does not. Staff keep `sessions:read`; they do not take the table home.
		gen.ReportsExport,
	},
	gen.Staff: {
		gen.SessionsRead, gen.SessionsManage, gen.VouchersManage, gen.AccountsManage,
		gen.NasRead, gen.GardenRead, gen.PortalRead,
	},
}

func permissionsFor(role gen.OperatorRole) []gen.OperatorPermission {
	granted, ok := roleGrants[role]
	if !ok {
		// An unknown role gets nothing. A role the server does not recognise is
		// not a reason to guess generously.
		return []gen.OperatorPermission{}
	}
	return slices.Clone(granted)
}

// ── the signed-in caller ─────────────────────────────────────────────────────

type callerKey struct{}

// caller is who is making this request, once a session has resolved.
type caller struct {
	Operator    Operator
	Permissions []gen.OperatorPermission
	TokenHash   string
	CSRFToken   string

	// MustChangePassword: this operator is signed in on a temporary password
	// somebody else set (`foyerctl create-operator --must-change-password`). The
	// gate reads it; handlers read it only to answer
	// `getAuthSession`, because a handler that had to remember to check it would
	// be a gate with as many copies as there are handlers.
	MustChangePassword bool
}

func (c *caller) may(p gen.OperatorPermission) bool { return slices.Contains(c.Permissions, p) }

func callerFrom(ctx context.Context) *caller {
	c, _ := ctx.Value(callerKey{}).(*caller)
	return c
}

// withCaller is how the gate hands the operator to a handler. A handler reads it
// and never re-reads the cookie: one place resolves a session, so there is one
// place to get it wrong.
func withCaller(ctx context.Context, c *caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

// ── tokens ───────────────────────────────────────────────────────────────────

// newToken returns a bearer value and the hash that is what gets stored.
//
// crypto/rand, never math/rand. The hash is what the store holds so that a
// database dump does not contain live sessions — the same reason a password
// verifier is not a password.
func newToken() (value, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", err
	}
	value = base64.RawURLEncoding.EncodeToString(buf)
	return value, hashToken(value), nil
}

func hashToken(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// sameToken is constant-time.
//
// A timing-variable comparison on a CSRF token is a slow oracle for it, and §9
// asks for constant time on vouchers and OTPs for the same reason. The empty
// case is refused explicitly rather than left to the comparison, because
// `ConstantTimeCompare` on two empty strings returns 1 — so a session with no
// token would accept a request with no header.
func sameToken(submitted, stored string) bool {
	if submitted == "" || stored == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(submitted), []byte(stored)) == 1
}

// setSessionCookie writes the session cookie.
//
// `Secure` unconditionally, which `__Host-` requires anyway. Browsers treat
// `http://localhost` as a secure context, so this still works against a
// development listener without a second, weaker code path — and a second code
// path is how a deployment ends up shipping the weaker one.
func setSessionCookie(w http.ResponseWriter, value string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func sessionTokenFrom(r *http.Request) string {
	c, err := r.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// passwordChangeExempt is what an operator on a temporary password may still
// reach. See the gate in `server.go` for why each one is on the list.
//
// **Keyed on operationId, not on a path.** Every other decision this package makes
// about an operation is read from the contract by operationId, and a path list
// beside them would be a second addressing scheme that drifts the first time an
// operation moves.
var passwordChangeExempt = map[string]bool{
	"GetAuthSession":    true,
	"SignOut":           true,
	"ChangeOwnPassword": true,
	// Public and says nothing about the caller: the sign-in screen asks it, and a
	// temporary-password session that lands there must not be answered with a
	// refusal that routes it somewhere else mid-render.
	"GetAdminSetup": true,
}
