package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/portal"
	"github.com/kiennt048/foyer/internal/ratelimit"
)

// Two bounds on what a caller can make this process spend:
//
//   - **Bytes.** `requestvalidation.go` reads every POST body before validating
//     it, on both listeners — including the guest portal's, which every device in
//     radio range can reach before it has authenticated. Without a ceiling that
//     read is unbounded. The Policy API does the same with
//     `http.MaxBytesReader` (`internal/aaa/handler.go`).
//   - **Sign-in attempts.** Every `signIn` and every `loginWithAccount` is an
//     argon2id computation at 64 MiB. `internal/identity` bounds how many run
//     at once; this bounds how many a caller may ask for.
//
// The control plane is one process serving the console, the portal and the Policy
// API, and the Policy API is on the path of every Wi-Fi login at the venue. So a
// flood against either of the first two is not a console outage — it is the
// process that answers FreeRADIUS running out of memory. That is why these are
// here and not a nicety.

// Body ceilings, per surface, and one per operation.
//
// The portal's forms carry a voucher code, a username, a password and a language;
// 16 KiB is two orders of magnitude over any of them, on the listener every device
// in radio range can reach. The console's megabyte covers every console request
// but one, `updatePortalSettings` included (its largest field, `logoDataUri`, is
// capped at 500 kB decoded by the contract).
//
// The one is `putPortalHeroImage`: a photograph as base64 in JSON, capped by
// `HeroImageUpload.dataBase64`'s `x-max-decoded-bytes`. Its ceiling is **derived
// from that figure**, never restated — `contractfigures_test.go` exists because
// two copies of the image ceiling drift apart — as the base64 length of the
// largest photograph the contract accepts plus room for the JSON around it. A
// flat console-wide ceiling that large would let every other operation be sent
// that much, and a flat one smaller would refuse a photograph the contract says
// is acceptable with the wrong answer.
//
// Every figure above is for a **signed-in** caller. Anybody else is held to
// `AnonymousBodyLimit` on every operation; see `bodyLimitFor` for why.
const (
	AdminBodyLimit  int64 = 1 << 20
	PortalBodyLimit int64 = 16 << 10

	// AnonymousBodyLimit is the ceiling for a console request with no session,
	// and for every public console operation. The portal's figure, on purpose:
	// both are what a caller who has proved nothing may make this process read.
	AnonymousBodyLimit = PortalBodyLimit

	// envelopeAllowance is the JSON around a base64 field: the filename, the
	// content type and the punctuation. Generous, because a refusal of an honest
	// upload is the failure this derivation exists to prevent.
	envelopeAllowance int64 = 64 << 10
)

// heroImageBodyLimit is the ceiling for `putPortalHeroImage`, read from the
// contract through `portalBudgets`.
var heroImageBodyLimit = int64(base64.StdEncoding.EncodedLen(portalBudgets.image)) + envelopeAllowance

// MaxAdminBodyLimit is the largest body any console operation accepts, for a
// listener that wants one cap in front of everything it serves.
func MaxAdminBodyLimit() int64 {
	if heroImageBodyLimit > AdminBodyLimit {
		return heroImageBodyLimit
	}
	return AdminBodyLimit
}

// bodyLimitFor answers the ceiling for one request: the operation's own when it
// has one, the surface's otherwise — **and the anonymous ceiling for a caller
// with no session**, whatever the operation.
//
// The last clause is the difference between bounding the bytes and bounding the
// memory. The request validator
// decodes a JSON body into a tree before the gate has looked at the caller, and
// the tree is far larger than the text. Measured in this package: one 1 MiB body
// of `[0,0,0,…]` posted anonymously to `createAccount` peaked at 34–40 MiB of
// heap, ten at once at 471 MiB and fifty at 1.3 GiB; even a 1 MiB string field
// peaked at about 9 MiB each, 234 MiB for fifty. The console listener is
// published on every interface (`deploy/compose.yaml`), so without it a guest on
// the venue Wi-Fi could exhaust the process that answers FreeRADIUS with no
// password and no argon2id at all — the outcome these bounds exist to prevent, by
// a route the sign-in throttle never sees.
//
// So the megabyte is for signed-in operators only. An anonymous caller, and every
// caller of a public operation (`signIn`, whose body is a username and a
// password), gets `AnonymousBodyLimit` — the figure the portal listener, which
// every device in radio range reaches, is held to. The session is
// resolved only when it could change the answer: a declared length within the
// anonymous ceiling costs no lookup, so an ordinary console request pays nothing
// for this. `TestAnAnonymousCallerGetsTheAnonymousCeiling` pins it.
//
// **The three figures are read per request** from the settings in force
// (netsettings.go): an operator sets them in the console, within
// bounds, and a change binds on the next request. The constants above are the
// defaults.
func (s *Server) bodyLimitFor(sf surface, r *http.Request) int64 {
	c := s.ceilings()
	if sf == surfacePortal {
		return int64(c.PortalBytes)
	}
	anonymous := int64(c.AnonymousBytes)
	limit := int64(c.ConsoleBytes)
	if s.contract != nil {
		if route, _, err := s.contract.router.FindRoute(r); err == nil && route.Operation != nil {
			if g, known := s.gates[route.Operation.OperationID]; known && g.Requirement == reqPublic {
				return anonymous
			}
			if route.Operation.OperationID == "PutPortalHeroImage" {
				limit = heroImageBodyLimit
			}
		}
	}
	if r.ContentLength >= 0 && r.ContentLength <= anonymous {
		// Within the ceiling every caller gets, who is asking cannot change the
		// answer, so nobody is looked up.
		return limit
	}
	if who, _ := s.resolveCaller(r.Context(), r); who == nil {
		return anonymous
	}
	return limit
}

// limitBody caps the request body **before anything reads it**.
//
// A declared `Content-Length` over the ceiling is refused without reading a byte.
// Anything else is wrapped in `http.MaxBytesReader`, so a streamed body stops at
// the ceiling, and the reader that hits it — the request validator, or the
// generated decoder for a route the validator does not know — maps the error to
// the same `413` through `bodyTooLarge`.
//
// It answers before the authentication gate, which is the one ordering this
// package otherwise avoids (`requestvalidation.go` stashes its 422 so that 401
// comes first). The difference is what the answer discloses: a 422 describes the
// body shape of an operation an anonymous caller may not reach, and a 413 says
// only that the listener does not read that many bytes, which is true of every
// operation on it.
func (s *Server) limitBody(sf surface) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				next.ServeHTTP(w, r)
				return
			}
			limit := s.bodyLimitFor(sf, r)
			if r.ContentLength > limit {
				// The body stays unread, so the connection cannot be reused.
				w.Header().Set("Connection", "close")
				writeProblem(w, payloadTooLarge(r.ContentLength, limit))
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			// A body that may be larger than the anonymous ceiling — only a
			// signed-in caller gets one — waits for room in the in-flight budget
			// (bodybudget.go). A streamed body of unknown length is weighed at its
			// ceiling, because that is what it may turn out to be.
			//
			// Every other body — the anonymous ones, the portal's, a signed-in
			// operator's ordinary form — draws on a second budget of its own, so
			// a synchronised flood of maximal anonymous bodies over every
			// connection a listener holds is bounded in decoded memory too, while
			// an honest form of a few hundred bytes never waits.
			length := r.ContentLength
			if length < 0 {
				length = limit
			}
			budget := s.smallBodies
			if sf == surfaceAdmin && limit > int64(s.ceilings().AnonymousBytes) && length > int64(s.ceilings().AnonymousBytes) {
				budget = s.bodies
			}
			if length > 0 {
				// The budget is in decoded bytes (memplan.go). Reserve the most this
				// length could cost, **capped at the whole budget** — which is not a
				// silent clamp: the validator weighs the body once it is read, hands
				// back the excess, and refuses one that does not fit at all.
				// Reserving the worst case up front is what keeps a request from ever
				// waiting while holding part of what it needs.
				reserve := min(length*decodeWorstPerByte, budget.total)
				// The reservation hands back what it still holds (`done`), so the
				// release acquire returns is not used.
				_, ok := budget.acquire(r.Context(), reserve)
				if !ok {
					// The client went away while it waited; nobody to answer.
					return
				}
				res := &bodyReservation{budget: budget, held: reserve}
				defer res.done()
				r = r.WithContext(context.WithValue(r.Context(), bodyReservationKey{}, res))
			}
			next.ServeHTTP(w, r)
		})
	}
}

// payloadTooLarge is the listener's refusal, on the field `body`, with the two
// figures `x-fault-params` requires for `too-large`.
func payloadTooLarge(bytes, limit int64) *apiError {
	return tooLarge("body", gen.FieldFaultTooLarge, map[string]any{"bytes": bytes, "limitBytes": limit})
}

// bodyTooLarge reports whether a read failed at the ceiling, and if so the
// refusal to send.
func bodyTooLarge(err error) (*apiError, bool) {
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return payloadTooLarge(mbe.Limit+1, mbe.Limit), true
	}
	return nil, false
}

// ── sign-in throttling ───────────────────────────────────────────────────────

// Sign-in policies. See `internal/ratelimit` for what the field does and why two
// independent buckets.
//
// The console: ten attempts back to back from one address, then one every six
// seconds; five against one account, then one every thirty. An operator who
// mistypes five times waits half a minute; a guesser gets 120 tries an hour
// against an account, each costing them the same argon2id it costs us.
//
// The portal's per-address budget is wider on purpose. Guests reach the portal
// directly on the venue LAN, but a venue that NATs its guest VLAN in front of the
// control plane puts every guest behind one address, and a limit tuned for one
// person would then be a limit for the whole café. The per-account bucket is the
// one that bounds guessing there, and the portal's existing per-MAC limiter
// (`portal.TokenBucket`) still sits behind this.
//
// **These are the defaults**, not the figures in force: an operator sets all four in the console (`updateNetworkSettings`), within bounds,
// and `signInThrottle.set` reshapes the live buckets without emptying them.
var (
	adminAddressPolicy  = ratelimit.Policy{Burst: 10, Every: 6 * time.Second}
	adminAccountPolicy  = ratelimit.Policy{Burst: 5, Every: 30 * time.Second}
	portalAddressPolicy = ratelimit.Policy{Burst: 30, Every: time.Second}
	portalAccountPolicy = ratelimit.Policy{Burst: 5, Every: 30 * time.Second}
)

// signInThrottle holds the buckets. One per Server, so a test server starts with
// a clean budget and the two surfaces never share one.
//
// `adminAccount` is the console's **untrusted** per-account bucket: every attempt
// that does not carry a trusted-device cookie for the username spends from it.
// `adminDevice` holds one bucket per (account, trusted device), under the same
// policy — see `devicecookie.go` for why the split, and OWASP's device-cookie
// defence for where it comes from.
type signInThrottle struct {
	adminAddress, adminAccount, adminDevice *ratelimit.Limiter
	portalAddress, portalAccount            *ratelimit.Limiter
}

func newSignInThrottle() *signInThrottle {
	return &signInThrottle{
		adminAddress:  ratelimit.New(adminAddressPolicy),
		adminAccount:  ratelimit.New(adminAccountPolicy),
		adminDevice:   ratelimit.New(adminAccountPolicy),
		portalAddress: ratelimit.New(portalAddressPolicy),
		portalAccount: ratelimit.New(portalAccountPolicy),
	}
}

// allowBoth spends from the address's bucket and then the account's, and the
// request must fit in both.
//
// An attempt the account's bucket refuses has still spent the address's token,
// and that is deliberate rather than an accident of ordering: the address did
// make an attempt, and a guesser rotating accounts is exactly the caller the
// address bucket exists to slow.
func allowBoth(addr, acct *ratelimit.Limiter, addrKey, acctKey string, now time.Time) (bool, time.Duration) {
	if ok, wait := addr.Allow(addrKey, now); !ok {
		return false, wait
	}
	if ok, wait := acct.Allow(acctKey, now); !ok {
		return false, wait
	}
	return true, 0
}

// retryAfterSeconds rounds up and never answers zero: `Retry-After: 0` is an
// invitation to retry immediately, which is the opposite of the refusal.
func retryAfterSeconds(d time.Duration) int {
	s := int(math.Ceil(d.Seconds()))
	if s < 1 {
		s = 1
	}
	return s
}

func rateLimited(wait time.Duration) *apiError {
	return &apiError{
		Status: http.StatusTooManyRequests,
		Type:   gen.ProblemsrateLimited,
		Title:  "Too many attempts",
		Params: map[string]any{"retryAfterSeconds": retryAfterSeconds(wait)},
	}
}

// accountKey is the bucket key for a username: normalised, then **hashed to a
// fixed size**.
//
// The username is whatever the caller typed — the contract gives it no
// `maxLength` — so a key built from the text itself is a map entry as long as the
// body allows, held for the bucket's whole refill period. That is 16 KiB per key
// on the portal and a megabyte on the console, and the limiter's
// `DefaultMaxKeys` of 100 000 is a bound in keys, not in bytes: on the
// portal alone, 180 live keys per address × 16 KiB × a /22 of guest addresses is
// well past the container. A digest makes an entry cost the same whatever was
// sent. SHA-256 truncated to 128 bits: the key only has to keep two accounts'
// budgets apart, and it is never compared against anything a caller chose.
func accountKey(username string) string {
	return "acct:" + accountDigest(username)
}

// accountDigest is accountKey without its prefix: what the trusted-device cookie
// names, so the cookie never carries the username itself.
func accountDigest(username string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(username))))
	return hex.EncodeToString(sum[:16])
}

// throttleSignIn is the strict middleware that spends the budget, for the
// operations that verify a password or a setup code and for nothing else.
//
// It runs **inside** the gate and the stashed contract fault, so a request the
// contract refuses is answered 422 without costing a token, and **before** the
// handler, so the refusal is decided before any argon2id runs. An attempt with an
// empty username or password is not charged either: both handlers refuse it
// before verifying anything, so it costs this process nothing to allow.
func (s *Server) throttleSignIn(f gen.StrictHandlerFunc, operationID string) gen.StrictHandlerFunc {
	switch operationID {
	case "SignIn", "LoginWithAccount", "CompleteAdminSetup":
	default:
		return f
	}
	return func(ctx context.Context, w http.ResponseWriter, r *http.Request, request any) (any, error) {
		now := s.now()
		addrKey := ratelimit.ClientKey(r.RemoteAddr)

		if operationID == "CompleteAdminSetup" {
			// **Per address only.** There is no account yet, and one bucket
			// shared by every caller would let a single device keep the owner out
			// of their own install — the lockout the device cookie exists to
			// prevent on `signIn`. An 80-bit code at the address budget is out of
			// reach of guessing; the budget is what bounds the argon2id a correct
			// code then spends. Charged only for a request that names a code.
			req, _ := request.(gen.CompleteAdminSetupRequestObject)
			if req.Body == nil || strings.TrimSpace(req.Body.SetupCode) == "" {
				return f(ctx, w, r, request)
			}
			if ok, wait := s.throttle.adminAddress.Allow(addrKey, now); !ok {
				p := rateLimited(wait)
				return gen.CompleteAdminSetup429ApplicationProblemPlusJSONResponse{
					SignInRateLimitedApplicationProblemPlusJSONResponse: gen.SignInRateLimitedApplicationProblemPlusJSONResponse{
						Body:    p.Problem(),
						Headers: gen.SignInRateLimitedResponseHeaders{RetryAfter: retryAfterSeconds(wait)},
					},
				}, nil
			}
			return f(ctx, w, r, request)
		}

		username, password := signInCredentials(request)
		if username == "" || password == "" {
			return f(ctx, w, r, request)
		}
		acctKey := accountKey(username)

		if operationID == "SignIn" {
			// The trusted-device split (devicecookie.go): a browser that signed in
			// as this account before spends from its own bucket, everybody else
			// from the account's shared one.
			accounts, spentKey := s.throttle.adminAccount, acctKey
			if device := s.devices.deviceFor(r, accountDigest(username)); device != "" {
				accounts, spentKey = s.throttle.adminDevice, acctKey+"|dev:"+device
			}
			ok, wait := allowBoth(s.throttle.adminAddress, accounts, addrKey, spentKey, now)
			if !ok {
				p := rateLimited(wait)
				return gen.SignIn429ApplicationProblemPlusJSONResponse{
					SignInRateLimitedApplicationProblemPlusJSONResponse: gen.SignInRateLimitedApplicationProblemPlusJSONResponse{
						Body:    p.Problem(),
						Headers: gen.SignInRateLimitedResponseHeaders{RetryAfter: retryAfterSeconds(wait)},
					},
				}, nil
			}
			resp, err := f(ctx, w, r, request)
			if _, signedIn := resp.(signedIn); signedIn && err == nil {
				// Reset-on-success, **of the bucket this attempt spent and no
				// other**. Clearing the shared bucket when the owner signs in from
				// a trusted device would hand the attacker emptying it a fresh
				// burst every time the owner came back.
				accounts.Forget(spentKey)
			}
			return resp, err
		}

		// The portal. Its answer is `303` on every outcome by rule — the CNA
		// re-checks captivity only after a navigation, and a `429` rendered in a
		// 900 × 550 mini-browser is raw text with no back button — so a refusal
		// here is the same navigation to the form with the generic reason that the
		// portal's own per-MAC limiter already sends. No `Retry-After`: the page a
		// guest lands on is a document, not a client that could honour one.
		//
		// **The account bucket is per account *and* per source address.** Keyed by
		// username alone, a stranger in radio range spending five guesses and two a
		// minute would hold a guest's account shut for as long as they cared to —
		// the console's lockout, on the portal. The
		// console's answer (a trusted-device cookie) does not carry over: Apple's
		// CNA runs on an ephemeral `WKWebsiteDataStore`, so its cookies are
		// destroyed when the sheet closes (docs/protocol-reality.md, MEASURED on
		// macOS; iOS not inspected) — every Apple guest's next sign-in would arrive
		// "untrusted" and meet the stranger's empty bucket. Android's login app
		// does keep cookies, but a defence that works for half the phones is only
		// half a defence. The signal no CNA can drop is
		// the transport source address: the socket's peer, never a header, and on
		// the guest network a DHCP lease a stranger cannot answer TCP for.
		// authentik's reputation is keyed the same way (per IP and username).
		// What it gives up: guessing one account is bounded per address rather
		// than overall, so an attacker with many leases gets more tries — each
		// lease costs association and DHCP inside radio range, and each address
		// is still held to `portalAddress`. A venue that NATs its guests behind one
		// address degenerates to a plain per-account bucket, and is no worse off.
		ok, _ := allowBoth(s.throttle.portalAddress, s.throttle.portalAccount, addrKey, acctKey+"|"+addrKey, now)
		if !ok {
			var lang string
			if req, isLogin := request.(gen.LoginWithAccountRequestObject); isLogin && req.Body != nil {
				lang = languageOf(req.Body.Lang)
			}
			return portalRedirect{location: portal.Failure(portal.PageAccount, lang, portal.ReasonGeneric, "")}, nil
		}
		return f(ctx, w, r, request)
	}
}

func signInCredentials(request any) (username, password string) {
	switch req := request.(type) {
	case gen.SignInRequestObject:
		if req.Body != nil {
			return strings.TrimSpace(req.Body.Username), req.Body.Password
		}
	case gen.LoginWithAccountRequestObject:
		if req.Body != nil {
			return strings.TrimSpace(req.Body.Username), req.Body.Password
		}
	}
	return "", ""
}
