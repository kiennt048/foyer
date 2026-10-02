package obs

import (
	"log/slog"
	"net/http"
	"time"
)

// Listener is the middleware a mount point installs. It is one value per listener
// rather than one package-level global, because two of the three properties it
// carries are per-listener facts and a global would have to be told which listener
// it was currently on — which is the bug that would put a guest-supplied
// `X-Request-Id` into the policy listener's logs the first time somebody reused
// the portal's middleware on it.
type Listener struct {
	// Name is the `listener` label value, and it is a *constant per listener*.
	// Every request metric line carries one of exactly three values, which is
	// what makes the metric's cardinality a number rather than a function of how
	// many operators have ever used the console.
	Name string
	// Trusted decides whether an inbound `X-Request-Id` is adopted. True for the
	// policy listener only; see [Lookup] for why.
	Trusted bool
	// Log is where the access line goes. Nil is `slog.Default()`.
	Log *slog.Logger
}

// The three listeners, named. They are constructors rather than exported struct
// literals so that `Trusted` cannot be set to true on the wrong one by accident,
// and so the `listener` label value and the trust decision cannot drift apart —
// they are the same decision about the same listener, made once.
const (
	listenerPortal = "portal"
	listenerAdmin  = "admin"
	listenerPolicy = "policy"
)

// NewPortalListener is the guest portal's: every unauthenticated device on the
// SSID reaches it, so it does not adopt an inbound ID and it does not log a line
// per request above DEBUG.
func NewPortalListener(l *slog.Logger) Listener {
	return Listener{Name: listenerPortal, Trusted: false, Log: l}
}

// NewPolicyListener is the Policy API's: the one listener whose peers prove
// themselves with a client certificate, and the one `/metrics` is mounted on.
func NewPolicyListener(l *slog.Logger) Listener {
	return Listener{Name: listenerPolicy, Trusted: true, Log: l}
}

// NewAdminListener is the console's: an operator's browser, over TLS, on the
// venue's LAN.
func NewAdminListener(l *slog.Logger) Listener {
	return Listener{Name: listenerAdmin, Trusted: false, Log: l}
}

// accessLogLevel is the level this listener records its request line at, and the
// three differ deliberately.
//
// The portal is the widest surface in the product: every unauthenticated device on
// the SSID reaches it, and the Captive Network Assistant on iOS and Android probes
// it repeatedly while a guest reads a page. A line per probe at INFO is a log that
// costs money to keep and tells an operator nothing — `deploy/compose.yaml` bounds
// it at 50 MB per container and that bound is reached by noise. The console is a
// human clicking, and is bounded by that. The Policy API is the opposite: an
// authorization decision *is* the product, a rejection is the one thing an operator
// needs to find, and it is a handful of lines a minute.
func (l Listener) accessLogLevel() slog.Level {
	if l.Name == listenerPolicy {
		return slog.LevelInfo
	}
	return slog.LevelDebug
}

// Middleware returns the handler every mount on this listener wraps its mux in.
//
// The order of the three things it does is the order that matters:
//
//  1. the ID is resolved and put in the context, so that *every* line written
//     while this request runs — including from `internal/api`, `internal/store`
//     and `internal/session`, several layers down and with no knowledge of HTTP —
//     carries it. That is the correlation guarantee, and it is why the ID travels
//     in the context rather than as a logger argument;
//  2. the ID is echoed on the response, so an operator with a browser open in front
//     of a failing console can quote the exact ID to search for instead of
//     guessing at a timestamp;
//  3. the request is served, and then one line is written and one metric
//     recorded.
//
// Step 3 happens after `next.ServeHTTP` because the status code is not known
// before it, and it happens exactly once because a handler that writes a header
// twice, or a client that disconnects mid-body, must still produce one line rather
// than two — a duplicated access line makes a count in the log disagree with the
// count in the metric, and the disagreement is unresolvable after the fact.
//
// m may be nil, in which case nothing is recorded. That is for a caller wiring a
// listener in a test that does not care; a production mount passes the registry.
func (l Listener) Middleware(next http.Handler, m *Metrics) http.Handler {
	logger := l.Log
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := Lookup(r, l.Trusted)
		if id == "" {
			id = NewRequestID()
		}
		ctx := WithRequestID(r.Context(), id)
		w.Header().Set(HeaderRequestID, id)

		rec := &responseRecorder{ResponseWriter: w}
		start := time.Now()
		// The route is the *pattern*, read after ServeHTTP because Go's ServeMux
		// fills `Request.Pattern` in as it matches. A pattern is one of a fixed set
		// of strings compiled into this binary; a path is whatever a guest typed.
		// That distinction is the whole privacy argument for the metric — see
		// [RouteLabel].
		next.ServeHTTP(rec, r.WithContext(ctx))
		dur := time.Since(start)

		route := RouteLabel(r.Pattern)
		if m != nil {
			m.Observe(l.Name, r.Method, route, rec.code())
		}
		logger.LogAttrs(ctx, l.accessLogLevel(), "request",
			slog.String(LabelListener, l.Name),
			slog.String("method", r.Method),
			slog.String(LabelRoute, route),
			slog.Int("status", rec.code()),
			slog.Int64("bytes", rec.bytes),
			slog.Duration("duration", dur),
		)
	})
}

// Liveness is the liveness answer, and it consults nothing.
//
// **It is not [Readiness] and shares no function with it**, on purpose. The
// Kubernetes documentation's warning is the reason: a liveness probe that fails on
// a dependency restarts the container every time that dependency restarts, and
// this process is the one answering every Wi-Fi sign-in on site. The two answer
// opposite questions and take opposite actions when they fail — kill versus
// remove-from-rotation — so merging them is not a simplification, it is an outage
// waiting for a Postgres restart.
// `TestLivenessAndReadinessShareNoCode` is what holds them apart, because the way
// this goes wrong is somebody making `/healthz` "more useful" by giving it a
// database check, and every test staying green while a database
// restart takes a venue's network down with it.
//
// It answers 200 with the body "ok", which is what it has always answered: three
// mount sites and two deployment probes (`deploy/compose.yaml`'s healthcheck and
// `deploy/first-boot.sh`'s wait) depend on that body, and changing it would break
// the console probe for no gain.
func Liveness(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}
