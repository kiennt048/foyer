package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/obs"
)

// observability is the three things this process can be asked about itself —
// is it alive, is it ready, what has it done — wired to the listeners it serves.
//
// **One value, built once at the top of `run`, handed to every listener.** A
// package-level registry would be one fewer parameter and one more thing that can
// be read before it is written: `serveUntilSignal` starts four listeners in a loop
// from a slice this package filled, and a global reached from that loop is a value
// whose initialisation order nothing states.
type observability struct {
	metrics   *obs.Metrics
	readiness obs.Readiness
	log       *slog.Logger
	// restore returns the process's logging defaults. A test that installs this
	// needs it so the installation does not leak into the next test; production
	// never calls it, because the process is exiting.
	restore func()
}

// newObservability builds it and installs the process logger.
//
// **The logger is installed first, before anything that can log**, which is why
// this is a function called at the top of `run` rather than a field assigned into
// a struct afterwards. `config.Load` can warn, the keyring can fail, and the
// maintenance scheduler starts on a timer — every one of those would otherwise go
// through whatever `slog.Default()` happened to be, which is the unconfigured text
// handler on stderr carrying no request ID.
//
// The context is `context.Background()` rather than the cancelled one: this runs
// before the signal handler exists, and the deadline here is for the two
// `PingContext` calls a readiness probe makes, not for start-up.
func newObservability(appPool *pgxpool.Pool, networkLoaded func() bool) *observability {
	logger := obs.NewLogger(nil, obs.LevelFromEnv())
	return &observability{
		log:       logger,
		restore:   obs.Install(logger),
		metrics:   obs.NewMetrics(),
		readiness: obs.Readiness{Postgres: pingCheck(appPool), Network: networkCheck(networkLoaded)},
	}
}

// pingCheck is the readiness check for PostgreSQL: a ping on the pool this process
// already holds, bounded by the handler's context.
//
// **A ping and not a `SELECT 1`**, deliberately. Both answer "is PostgreSQL
// reachable", and the ping is the one that does not depend on a table, a schema or
// a role's grants — so this probe cannot go false because a migration is half
// applied, or because `foyer_app` lost a grant on `radacct`. Those are real
// failures and none of them is this probe's business: readiness reports whether a
// request arriving now can be served, and a `SELECT 1` on a table a migration is
// rewriting answers a different question.
//
// A nil pool is `ready`, not a failure — see [obs.Readiness.Postgres]. The control
// plane refuses to start without a database, so the nil case is a test or the
// maintenance container, and a probe that invented a failure there would be one
// nobody could act on.
func pingCheck(pool *pgxpool.Pool) obs.Check {
	if pool == nil {
		return nil
	}
	return func(ctx context.Context) error { return pool.Ping(ctx) }
}

// errNetworkNotLoaded is the reason readiness gives when the stored network
// settings are not in force. A named error rather than a formatted string so the
// text is one string in the repository rather than one per call site.
var errNetworkNotLoaded = errors.New("the stored network settings have not been read into this process")

// networkCheck is the driver-path half of readiness, and it is a **local** check.
//
// It asks whether the stored network settings have been read into this process.
// That is the thing whose absence means a guest cannot be served: without it the
// API server answers from its bootstrap defaults, the portal does not know its own
// address, and `portal.EntryHandler` has nothing to bind a guest's browser to.
//
// **It does not call a controller.** The guest portal's front-door comment
// records that the
// Captive Network Assistant probes the portal repeatedly while a guest reads a
// page, and that a controller round trip per probe is a denial of service the
// venue pays for. A readiness probe runs on a timer for the life of the process,
// which is the same shape with worse manners. A controller being unreachable is a
// *degraded deployment*, not an unready process: the console keeps serving, the
// reaper keeps running, retention keeps sweeping, and taking the control plane out
// of rotation because a UniFi controller rebooted is the wrong remedy for that.
//
// The predicate is a closure rather than a direct read of `api.Server`'s state
// because `api.Server.network()` is unexported, and exporting it for a probe
// would widen a change to a package that has other owners. Stating the check here,
// where its reasoning is, and supplying the closure at the composition root keeps
// the decision and the reason in one place.
func networkCheck(loaded func() bool) obs.Check {
	if loaded == nil {
		return nil
	}
	return func(context.Context) error {
		if !loaded() {
			return errNetworkNotLoaded
		}
		return nil
	}
}

// instrument wraps a listener's handler in the middleware for that listener.
//
// It is a method rather than a call at each of the mount sites because the sites
// differ in which `obs.Listener` they use, and getting one wrong is a security
// decision made by accident: `obs.NewPolicyListener` is the only one with
// `Trusted: true`, because it is the only listener whose peers authenticate with a
// client certificate. Routing every mount through here means a new listener is
// added by *naming* it, and the name is what decides.
func (o *observability) instrument(l obs.Listener, h http.Handler) http.Handler {
	return l.Middleware(h, o.metrics)
}

// healthHandlers are the two process-level endpoints every listener serves.
//
// **`/healthz` and `/readyz` are different handlers and must stay that way.** Not
// tidiness: it is the Kubernetes rule, and the two answer opposite questions with
// opposite consequences. `/healthz` is liveness — it consults nothing, and a
// failure means kill the process. `/readyz` is readiness — it consults PostgreSQL
// and the driver's configuration, and a failure means stop sending traffic here,
// which is the only correct action for a database that is briefly unreachable and
// emphatically not the correct action for the process itself.
//
// Both are mounted on every listener because all three are real places a monitor
// might be pointed, and because the portal's liveness answer is what
// `deploy/verify.sh` probes to tell "the plain listener is serving" from "the
// console is not reachable". `TestLivenessAndReadinessShareNoCode` in
// `internal/obs` is what holds the two apart.
func (o *observability) healthHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", obs.Liveness)
	mux.Handle("GET /readyz", o.readiness.Handler())
}

// logf is `applyMemoryPlan`'s `func(string, ...any)` seam, bound to this process's
// logger.
//
// The seam exists because `memory.go` reports what it did and takes a writer, which
// is the right shape for a package that must not choose a logging system. This
// adapter is where that choice is made, once, rather than at four call sites that
// each pass `log.Printf` and each emit a different format from the rest of the
// process.
func (o *observability) logf(format string, args ...any) {
	o.log.Info(fmt.Sprintf(format, args...))
}

// withChecks installs the readiness checks once the things they ask about exist.
//
// **It is a separate call because both dependencies are created partway through
// `run`**, after the observability has to exist. The checks are read per request
// by `/readyz` rather than captured at construction, so a probe that arrives in
// the window before this is called sees no configured checks — reports ready, and
// that window is before any listener is bound, so nothing can be asking.
func (o *observability) withChecks(pool *pgxpool.Pool, networkLoaded func() bool) {
	o.readiness.Postgres = pingCheck(pool)
	o.readiness.Network = networkCheck(networkLoaded)
}

// mountMetrics puts `/metrics` on the mux, and it is called from the policy
// listener and nowhere else.
//
// **The decision is who can reach the listener, and there are three answers.** The
// guest portal is bound wide because every unauthenticated device on the SSID must
// reach it; a metrics endpoint there publishes a venue's capacity, sign-in rate and
// error rate to anyone in radio range, which is a finding rather than a feature. The
// admin console is published on the venue's LAN and is an operator's browser origin;
// its request counts and error rates are the venue's business to an unauthenticated
// peer. The Policy API is the one `deploy/compose.yaml` does not publish — "NOT
// published 8080 the Policy API answers FreeRADIUS and nothing else" — sits on the
// internal backplane, and requires a client certificate. authentik reaches the same
// isolation with a dedicated metrics port; Foyer does not add a fourth listener,
// because a new port is a new published surface and a new thing for an operator to
// get wrong, and the policy listener's documented purpose is already the trust level
// a scrape needs.
//
// It is a separate function from [observability.healthHandlers] rather than a flag
// on it because the flag would be a boolean somebody sets to true, and the answer
// to "may this listener expose metrics" is not a boolean. This way a listener
// either has the endpoint or does not, and which one is visible at the mount site.
func (o *observability) mountMetrics(mux *http.ServeMux) {
	mux.Handle("GET /metrics", o.metrics.Handler())
}
