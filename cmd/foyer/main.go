// Command foyer is the control plane: three listeners, one database, one process.
//
// ── why three listeners ──────────────────────────────────────────────────────
//
// `docs/rules/security.md` §7 requires the admin UI and the Policy API to be on
// separate listeners from the guest portal. Mounting the admin API on the policy
// listener's mux with `policyMux.Handle("/", apiServer.Handler())` would put the
// console and the RADIUS authorization endpoint on one port.
//
// The reason it matters is not tidiness. The guest portal is reachable **before a
// device has authenticated to anything** — by anyone within radio range of the SSID.
// A console sharing that origin shares its cookie jar, so a cross-site scripting
// bug in a venue's terms of service is an administrator session, and the `__Host-`
// prefix on the session cookie stops meaning what it was chosen to mean. Three
// ports, three audiences:
//
//	guest portal   0.0.0.0:8081    every unauthenticated device on the SSID
//	admin console  0.0.0.0:8443   an operator's browser, over HTTPS on a
//	                              certificate this process generates
//	policy API     127.0.0.1:8080  FreeRADIUS, and nothing else
//
// Only the portal is bound wide by default, because only the portal has to be.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/kiennt048/foyer/internal/aaa"
	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/obs"
	"github.com/kiennt048/foyer/internal/portal"
	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/redact"
	"github.com/kiennt048/foyer/internal/store"
	"github.com/kiennt048/foyer/internal/tlsca"
	"github.com/kiennt048/foyer/internal/version"
	"github.com/kiennt048/foyer/internal/webui"
)

func main() {
	// `foyer maintenance` is the maintenance container (maintprocess.go); anything
	// else is the control plane.
	if len(os.Args) > 1 && os.Args[1] == maintenanceMode {
		if err := runMaintenance(); err != nil {
			log.Fatalf("foyer maintenance: %v", err)
		}
		return
	}
	if err := run(); err != nil {
		log.Fatalf("foyer: %v", err)
	}
}

func run() error {
	// ── observability, first, before anything can log ───────────────────────
	//
	// It is the first statement because it has to be. `config.Load` can warn, the
	// keyring can fail, and the maintenance scheduler starts on a timer — all three
	// would otherwise log through whatever `slog.Default()` happened to be, which is
	// the unconfigured text handler on stderr carrying no request ID. Before
	// `internal/obs` existed this process used stdlib `log` throughout, so it emitted
	// two formats on two streams and an operator grepping for a request found nothing.
	//
	// The two checks are nil here and filled in below, once the pool and the network
	// runtime exist: they are closures, so `/readyz` reads whatever was installed by
	// the time a request arrives. `internal/obs` treats a nil check as "not
	// configured" and reports it ready rather than inventing a failure.
	ob := newObservability(nil, nil)
	defer ob.restore()

	// **Which build is this?** The first line of the log is the only place an
	// operator reads without being told to look, so it names the build, not only a
	// release — a release string is the same for every build ever made of it. `deploy/verify.sh` could
	// answer the question, but only from outside, only through the Docker socket,
	// and only about the image rather than the process. See `internal/version`.
	ob.log.Info("the Foyer control plane is starting", "version", version.String(), "revision", version.Revision())

	cfg := config.Load()
	// The assertions come before anything is opened or bound. A process that
	// refuses on the ninth line of `main` is a process an operator can fix; one
	// that refuses after it has already answered a request is one that was
	// briefly wrong in public.
	if err := cfg.Validate(); err != nil {
		return err
	}
	for _, w := range cfg.Warnings() {
		ob.log.Warn("the configuration has a fault the operator should know about", "warning", w)
	}
	// Before anything can hash a password: the argon2id concurrency is sized from
	// the container's memory, and a sign-in served before this ran would be
	// sized by the default instead. See memory.go.
	applyMemoryPlan(cfg.GoMemLimitSet, ob.logf)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A control plane without its database is not a degraded control plane. It
	// refuses rather than continuing, so every operation that needs a repository
	// has one — a server built with no store would answer 501 to every operation.
	//
	// The keyring is built from cfg.KEK, the value Load read, rather than by
	// store.NewStore reading FOYER_KEK a second time: one reader, one answer.
	keys, err := store.KeyringFromSpec(cfg.KEK)
	if err != nil {
		return err
	}
	dbStore, err := store.NewStoreWithKeyring(ctx, cfg.DatabaseURL, keys)
	if err != nil {
		return fmt.Errorf("database unavailable, refusing to start: %w", err)
	}
	defer dbStore.Close()
	ob.log.Info("connected to PostgreSQL")

	// The maintenance jobs and the two role pools they run on (internal/jobs). A pool
	// that will not open is a job that fails every cycle, never a refusal to start:
	// this process also answers every Wi-Fi sign-in. See maintenance.go.
	//
	// **Or in the maintenance container** (maintprocess.go): with
	// FOYER_MAINTENANCE_URL set this process holds neither role's login, schedules
	// nothing, and asks that container for erasure's retention half.
	maint, eraser, changed, err := maintenanceFor(ctx, cfg, dbStore)
	if err != nil {
		return err
	}
	defer maint.Stop()

	adapter, err := apistore.New(apistore.Config{Store: dbStore, Retention: eraser,
		MaintenanceChanged: changed})
	if err != nil {
		return err
	}

	// The `network` tag's store, with this deployment's FreeRADIUS tree writer
	// when it has one (radius.go). Replaces the adapter's own, which has none.
	var applier apistore.RadiusApplier
	var raddbHandoff *raddbApplier
	if cfg.RaddbTarget != "" {
		raddbHandoff = newRaddbApplier(cfg, dbStore)
		applier = raddbHandoff
	}
	network := apistore.NewNetwork(apistore.NetworkConfig{Store: dbStore, Applier: applier, SnapshotPath: raddbSnapshotPath(cfg)})
	ports := adapter.Ports()
	ports.Network = network

	// The guest seam, built once and handed to both things that hold a
	// `portal.Service`: the API server's five contract operations, and the front
	// door below. One value rather than two, so the rate limiter that bounds the
	// pre-authentication state is one budget per guest rather than two.
	//
	// `api.New` refuses a half-wired portal rather than answering `501` per
	// operation, because the ports are not independent — a grant store with no
	// driver seam is a portal recording grants for devices nothing confirmed.
	apiServer, portalSvc, err := portalServices(cfg, ports, adapter.PortalPorts())
	if err != nil {
		return err
	}

	// The portal listeners' changing state (portaltls.go). The deployment's
	// certificate files are refused here — before any listener binds — if the
	// pair does not load or does not cover the host guests are sent to.
	portalRT, err := newPortalRuntime(cfg, apiServer, portalSvc, ob)
	if err != nil {
		return fmt.Errorf("guest portal TLS: %w", err)
	}
	apiServer.AttachNetwork(portalRT)
	// What an operator saved in the console, in force before the first request.
	if err := apiServer.ReloadNetwork(ctx); err != nil {
		return fmt.Errorf("reading the network settings: %w", err)
	}

	// Both dependencies of `/readyz` now exist, so the probe can see them. It is
	// after `ReloadNetwork` deliberately: the network check asks whether the stored
	// settings are in force, and installing it before that would make a probe that
	// could never pass.
	ob.withChecks(dbStore.Pool(), portalRT.networkLoaded)

	aaaEngine := aaa.NewEngine(dbStore, 300)

	servers := []*namedServer{
		policyListener(cfg, aaaEngine, ob),
		adminListener(cfg, apiServer, ob),
		portalRT.plainListener(),
	}
	// The HTTPS portal, when there is a certificate to serve on it now. When
	// there is not, the first one uploaded in the console binds it.
	if portalTLS := portalRT.startTLSAtBoot(); portalTLS != nil {
		servers = append(servers, portalTLS)
	}
	defer func() {
		if late := portalRT.lateTLS(); late != nil && !slices.Contains(servers, late) {
			shutdown([]*namedServer{late})
		}
	}()

	// The client table FreeRADIUS reads, synced at start so that a change made
	// while this process was down reaches it without anybody running foyerctl.
	if res, err := network.SyncRadiusClients(ctx, gen.RadiusSyncTriggerStartup, ""); err != nil {
		ob.log.Warn("the start-up sync of the RADIUS client table could not be recorded", "error", err.Error())
	} else if res.Outcome != gen.RadiusClientSyncOutcomeSynced {
		ob.log.Warn("the start-up sync of the RADIUS client table did not sync", "outcome", string(res.Outcome), "reasons", strings.Join(res.Reasons, " "))
	}

	// The tree FreeRADIUS runs, generated from what the console stored and handed
	// to the supervisor in the FreeRADIUS image (radius.go). In the background and
	// never fatal: the control plane serves the console and the portal whether or
	// not FreeRADIUS is up, and at first start its container may not exist yet.
	if raddbHandoff != nil {
		go raddbHandoff.Startup(ctx, network)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	return serveUntilSignal(ctx, servers, maint, sigCh)
}

// starter is what serveUntilSignal starts once the listeners are going: the
// maintenance scheduler (maintenance.go).
type starter interface{ Start(ctx context.Context) }

// serveUntilSignal starts every listener, then the maintenance jobs, and serves
// until a listener fails or a signal arrives.
//
// **A function of its own so the start is a behaviour a test can watch**: a
// syntax-tree test of `run` would accept `if false { maint.Start(ctx) }`, because
// a call inside a dead branch is still a call in the tree. `TestServingStartsTheMaintenance` runs this and waits for the
// jobs to be started; `TestRunEndsByServing` pins that `run` returns through it.
func serveUntilSignal(ctx context.Context, servers []*namedServer, maint starter, signals <-chan os.Signal) error {
	errs := make(chan error, len(servers))
	for _, s := range servers {
		go func(s *namedServer) {
			slog.Default().Info("the listener is serving",
				"listener", s.name, "address", s.srv.Addr, "scheme", s.scheme(),
				"maxConnections", s.maxConns)
			err := s.serve()
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				errs <- fmt.Errorf("%s: %w", s.name, err)
			}
		}(s)
	}

	if maint != nil {
		maint.Start(ctx)
	}

	select {
	case err := <-errs:
		// One listener failing takes the process down rather than leaving two of
		// three answering: a control plane that serves the console and not the
		// portal looks healthy to an operator and is broken for every guest.
		shutdown(servers)
		return err
	case <-signals:
	}

	slog.Default().Info("shutting the Foyer control plane down")
	shutdown(servers)
	slog.Default().Info("the Foyer control plane stopped cleanly")
	return nil
}

// namedServer is one listener, so that starting and stopping three of them is one
// loop rather than three copies.
type namedServer struct {
	name string
	srv  *http.Server
	tls  *tls.Config
	// maxConns bounds the connections this listener holds at once (connlimit.go).
	// Zero is refused by serve rather than read as "unbounded".
	maxConns int
}

// serve listens on the server's address, bounded, and serves until shut down.
func (s *namedServer) serve() error {
	if s.maxConns <= 0 {
		return fmt.Errorf("%s: no connection bound set; refusing to serve an unbounded listener", s.name)
	}
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return err
	}
	return s.serveOn(ln)
}

// serveOn serves on a listener somebody already bound, through the same bound.
func (s *namedServer) serveOn(ln net.Listener) error {
	ln = limitListener(ln, s.maxConns)
	if s.tls != nil {
		return s.srv.ServeTLS(ln, "", "")
	}
	return s.srv.Serve(ln)
}

func (s *namedServer) scheme() string {
	if s.tls != nil {
		return "https://"
	}
	return "http://"
}

func shutdown(servers []*namedServer) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, s := range servers {
		_ = s.srv.Shutdown(ctx)
	}
}

// policyListener answers FreeRADIUS's `rlm_rest` calls, and nothing else.
//
// The timeouts are short on purpose: `rlm_rest` blocks a FreeRADIUS worker thread
// for the duration of the call, the pool is fixed at `max_servers`, and when it is
// exhausted Status-Server is blocked too — so the health check dies with the thing
// it checks. §2 puts the ceiling below the NAS retransmit interval.
// **`/metrics` is mounted here and on no other listener**, and the reason is who
// can reach this one. `deploy/compose.yaml` does not publish 8080 — "the Policy API
// answers FreeRADIUS and nothing else" — it sits on the internal backplane, and
// `policyTLS` requires a client certificate on any non-loopback address. The guest
// portal is bound wide to every unauthenticated device on the SSID and the console
// is published on the venue's LAN, so a metrics endpoint on either of them
// publishes this deployment's request counts, error rates and uptime to a party
// that has no business knowing them. See `internal/obs` and `mountMetrics`.
func policyListener(cfg config.Config, engine *aaa.Engine, ob *observability) *namedServer {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/aaa/authorize", engine.HTTPHandler())
	mux.HandleFunc("POST /api/v1/aaa/authenticate", engine.HTTPHandler())
	ob.healthHandlers(mux)
	ob.mountMetrics(mux)

	s := &namedServer{
		name:     "Policy API",
		maxConns: policyMaxConns,
		srv: &http.Server{
			Addr: cfg.PolicyAddr,
			// The middleware is outside the mux, so it sees the request on the way in
			// and the status on the way out, and so `r.Pattern` is filled in by the
			// time it reads the route for the metric.
			Handler: ob.instrument(obs.NewPolicyListener(ob.log), mux),
			// A failed handshake is logged with the client's address; masked.
			ErrorLog:     redact.ErrorLog(),
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		},
	}
	if tlsCfg := policyTLS(cfg); tlsCfg != nil {
		s.tls = tlsCfg
		s.srv.TLSConfig = tlsCfg
	}
	return s
}

// policyTLS builds the TLS configuration §2 asks for, including the client
// certificate requirement that makes it mTLS rather than merely encrypted.
//
// Encryption alone would leave the Policy API answering authorization questions for
// anybody who can reach the port. `FOYER_POLICY_CLIENT_CA` is what makes the answer
// conditional on being FreeRADIUS.
func policyTLS(cfg config.Config) *tls.Config {
	if cfg.PolicyTLSCert == "" || cfg.PolicyTLSKey == "" {
		return nil
	}
	cert, err := tls.LoadX509KeyPair(cfg.PolicyTLSCert, cfg.PolicyTLSKey)
	if err != nil {
		// **The line is structured and the process still stops.** `log.Fatalf` writes
		// and exits; `slog` does not exit, so a conversion that only swapped the call
		// would have turned three refusals into three warnings and a Policy API serving
		// with no certificate at all. `TestThePolicyAPIStillRefusesRatherThanWarns`
		// holds it.
		slog.Default().Error("the Policy API certificate could not be read; refusing to serve authorization questions",
			"error", err.Error(), "cert", cfg.PolicyTLSCert)
		os.Exit(1)
	}
	out := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if cfg.PolicyClientCA != "" {
		pem, err := os.ReadFile(cfg.PolicyClientCA)
		if err != nil {
			slog.Default().Error("the Policy API client CA could not be read; refusing to serve authorization questions",
				"error", err.Error(), "ca", cfg.PolicyClientCA)
			os.Exit(1)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			slog.Default().Error("FOYER_POLICY_CLIENT_CA contains no certificate; refusing to serve authorization questions",
				"ca", cfg.PolicyClientCA)
			os.Exit(1)
		}
		out.ClientCAs = pool
		out.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		slog.Default().Warn("the Policy API has TLS but no FOYER_POLICY_CLIENT_CA, so any client that can reach it may ask it authorization questions. §2 asks for mTLS.")
	}
	return out
}

// adminListener answers the console's `/api/v0`, and nothing else.
//
// Its own mux with no catch-all: an unknown path is a 404 from this listener rather
// than something the portal or the policy engine handles, which is what keeps the
// three surfaces from quietly merging again the next time somebody adds a route.
func adminListener(cfg config.Config, apiServer *api.Server, ob *observability) *namedServer {
	mux := http.NewServeMux()
	mux.Handle(api.BasePath+"/", apiServer.Handler())
	// `/healthz` and `/readyz`, and **not** `/metrics` — see [policyListener] for why
	// the exposure decision follows the listener's reach and not its usefulness.
	ob.healthHandlers(mux)
	// The console itself, and the catch-all is narrower than a single-page
	// application usually needs — deliberately, because this listener has an
	// invariant that comes before the console.
	//
	// A SPA must answer a deep link after a reload, so it needs a fallback. But
	// `TestTheAdminAndPolicyListenersRouteWhatTheyMount` pins that **this listener
	// never answers a guest document path**, and a mount with a plain SPA
	// fallback does exactly that. The comment above this function says why:
	// an unknown path being a 404 here is what keeps the three surfaces from
	// quietly merging again the next time somebody adds a route. A console that
	// serves `/en/` has merged them, on a port a guest cannot reach — which makes
	// it invisible rather than harmless.
	//
	// So the fallback declines the portal's namespace, and the namespace is
	// **read from the frontend build** rather than listed here. A language added
	// to the portal is a language this listener stops claiming, with nobody
	// editing this file — which is the difference between a boundary and a copy
	// of one. `/api/v0/` needs no such care: it is a more specific pattern, so
	// `http.ServeMux` prefers it, and a mistyped API path is still a 404 from the
	// API rather than HTML with a 200.
	//
	// **The derivation has an empty case, and it was live.** See [mountConsole].
	mountConsole(mux, webui.Built(), webui.PortalLanguages(), webui.Admin())

	// The body ceiling is applied twice, deliberately: `internal/api` answers the
	// contract's 413 per operation, and this listener-wide cap covers every other
	// handler on the mux — the console's static files, anything added later — so
	// no route on this port reads an unbounded body because nobody thought to
	// limit it.
	//
	// Both ceilings are read per request: an operator sets them
	// in the console, and `net/http` fixes MaxHeaderBytes when the server is
	// built, so the listener is built with the most the setting may be and
	// `LimitHeaders` enforces the setting below it.
	srv := &http.Server{
		Addr: cfg.AdminAddr,
		// A failed handshake is logged with the client's address; masked.
		ErrorLog: redact.ErrorLog(),
		// The middleware is outside the body and header limiters as well as the mux, so
		// it measures the request as the client sent it. Inside them, a request refused
		// for a body over the ceiling would be recorded with no route, and the access
		// line would be the only evidence the ceiling exists.
		Handler:        ob.instrument(obs.NewAdminListener(ob.log), apiServer.LimitHeaders(api.LimitBody(mux, apiServer.AdminListenerBodyLimit))),
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   30 * time.Second,
		MaxHeaderBytes: api.MaxHeaderCeiling,
	}
	name := "Admin console"

	// HTTPS, on a certificate this process makes. `tlsca` writes it when a path is configured and keeps it in memory
	// when none is — see `internal/config`'s AdminTLSCert for why no default path
	// is baked in.
	//
	// **The certificate served is the one `EnsureSelfSigned` returned**, never a
	// re-read of the file. `deploy/verify.sh` compares the certificate on the wire
	// against the file's SHA-256 precisely because a re-read is where the two
	// could diverge — and if they diverge, the fingerprint `first-boot.sh` printed
	// is not the one the operator's browser shows, which is the single way that
	// advice could get somebody hurt.
	// `Rotating` rather than `EnsureSelfSigned` directly, and the difference is not
	// convenience: it renews **per handshake** instead of only at process start.
	//
	// `deploy/` schedules nothing — no timer, no cron entry, no unattended reboot,
	// no periodic pull, and `restart: unless-stopped` survives a reboot without
	// ever causing one. So a start-time-only check promises *"no process starts
	// with a certificate expiring within 60 days"*, which is a different sentence
	// from *"the console never serves an expired certificate"*: a box that simply
	// runs reaches expiry in service. No window value fixes that — a window is a
	// bet on a restart cadence, and against *never* every bet loses.
	rot, err := tlsca.NewRotating(cfg.AdminTLSCert, cfg.AdminTLSKey, cfg.AdminTLSNames)
	if err != nil {
		slog.Default().Error("the admin console certificate could not be made; refusing to serve the console", "error", err.Error())
		os.Exit(1)
	}
	cert, generated := rot.Initial()
	slog.Default().Info("the admin console certificate", "detail", tlsca.Describe(cert, cfg.AdminTLSCert, generated))

	// **`GetCertificate` alone, and `Certificates` deliberately absent.** Go prefers
	// `GetCertificate`, so a stale `Certificates` entry beside it is dormant rather
	// than wrong — until somebody reorders this config and the console silently
	// reverts to the certificate it held at startup, with nothing reporting it.
	// That is the whole reason renewal is unobservable when it breaks.
	//
	// It is also what `deploy/verify.sh` rests on: it compares the certificate the
	// console *presents* against the file on disk. Serving anything other than what
	// `tlsca` returned would mean the fingerprint `first-boot.sh` printed is not the
	// one the operator's browser shows, which is the single way that advice could
	// get somebody hurt.
	tlsCfg := &tls.Config{GetCertificate: rot.GetCertificate, MinVersion: tls.VersionTLS12}
	srv.TLSConfig = tlsCfg

	return &namedServer{name: name, srv: srv, tls: tlsCfg, maxConns: adminMaxConns}
}

// portalListener answers guests.
//
// Longer timeouts than the policy listener, because the client is a phone on a
// captive network doing a TLS handshake over Wi-Fi it has not finished joining.
//
// ── what this listener does not mount ────────────────────────────────────────
//
// No grant route that **takes the client MAC from the request body**, such as a
// `POST /api/v1/portal/grant`. There is no browser-to-MAC binding behind such a
// route and no session it could be checked against, so `docs/rules/security.md`
// §4 cannot be satisfied by it at all: the value the driver is asked to confirm is
// the same value the caller typed. Grants go through the contract's portal
// operations instead.
//
// `EntryHandler` negotiates a language, relays the query string, **and** binds the
// browser to the MAC the NAS named, which is the one thing a portal has to do before anything
// else can be checked.
//
// The RFC 8908 endpoint stays. It is a different endpoint from the contract's
// `/portal/status`: this one carries `application/captive+json` for the operating
// system's captive-portal API, and the contract types `/portal/status` as
// `application/json` because it is what the connected page renders. It answers
// **for the device that asked** — `portal.Service.CaptiveHandler` — rather than
// `captive: true` to everybody.
//
// ── two listeners when the operator supplies a certificate ───────────────────
//
// With FOYER_PORTAL_TLS_CERT/KEY set, `portalTLSListener` serves this same mux
// over HTTPS and this listener becomes `toHTTPS`: still bound, because it is where
// a NAS redirect lands, and sending every GET to the HTTPS origin. See
// portaltls.go for what the field does and why HSTS is never sent.
// mountConsole installs the console's fallback, and it has two states because the
// boundary above is **derived from the frontend build** — which is right, and which
// is why it has to be written down that the derivation has an empty case.
//
// In a tree where `make web-embed` has not been run, which is every fresh clone,
// `PortalLanguages()` is empty, so a loop that declines `/en/` iterates zero times,
// `/en/` reaches the console's absent-handler, and the listener answers **503 for a
// guest document path**. `TestTheAdminAndPolicyListenersRouteWhatTheyMount` would
// then be red for anyone who had not built the frontend, and green for everyone who
// had.
//
// **A boundary read from an artifact disappears with the artifact.** The repair is
// not to hardcode the language list — that would be the copy this comment's
// predecessor correctly refused. It is to tie the catch-all to the thing it falls
// back *to*: with no console embedded there is nothing to fall back to, so this
// listener keeps the older invariant it has always had — no catch-all, an unknown
// path is a 404 — and the 503 that names the missing build step stays reachable at
// the root, where an operator who opened the console will meet it.
//
// Both states are driven on every run by `TestTheConsoleNeverClaimsAGuestPath`,
// because a tree can only be in one of them at a time.
func mountConsole(mux *http.ServeMux, built bool, portalLanguages []string, console http.Handler) {
	if !built {
		mux.Handle("/{$}", console)
		return
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		for _, lang := range portalLanguages {
			if r.URL.Path == "/"+lang || strings.HasPrefix(r.URL.Path, "/"+lang+"/") {
				http.NotFound(w, r)
				return
			}
		}
		console.ServeHTTP(w, r)
	})
}

// portalServices builds the two things that hold a `portal.Service` — the API
// server's (the five contract operations) and the front door's — over one
// `portal.Ports`.
//
// **One address book for both**, for the same reason there is one `portalPorts`:
// the API server's service observes the browsers that call `/api/v0/portal/*`,
// the front door's observes the ones it redirects, and the RFC 8908 endpoint
// (the front door's `CaptiveHandler`) has to read what either saw. Left to
// `NewService`'s default, each would get its own book, and a grant issued through
// the API would reach the endpoint only through the thirty-minute session row.
// A function rather than three lines in `run()` so that
// `TestTheRFC8908EndpointReadsWhatTheAPIObserved` can hold the wiring in place.
func portalServices(cfg config.Config, adminPorts api.Ports, portalPorts portal.Ports) (*api.Server, *portal.Service, error) {
	if portalPorts.Addresses == nil {
		portalPorts.Addresses = portal.NewAddressBook()
	}
	source := gen.NetworkSettingSourceEnvironment
	if cfg.PortalURLDerived {
		source = gen.NetworkSettingSourceDerived
	}
	apiServer, err := api.New(api.Config{
		Ports: adminPorts, Portal: portalPorts, PortalURL: cfg.PortalURL,
		// The deployment's own values, in force until the console saves others.
		NetworkBootstrap: api.NetworkBootstrap{PortalURL: cfg.PortalURL, PortalURLSource: source, VenueURL: cfg.VenueURL},
		// The body budgets and ceiling bounds are the memory plan's (memory.go).
		Memory: processMemory,
	})
	if err != nil {
		return nil, nil, err
	}
	portalSvc, err := portal.NewService(portal.ServiceConfig{Ports: portalPorts})
	if err != nil {
		return nil, nil, err
	}
	return apiServer, portalSvc, nil
}

// It is `portalRuntime.plainListener` (portaltls.go); this builds a runtime over
// the deployment's own values for a caller with no console, which is every test
// of the listener's routing. A certificate file that does not load leaves it plain.
func portalListener(cfg config.Config, apiServer *api.Server, svc *portal.Service, ob *observability) *namedServer {
	rt, err := newPortalRuntime(cfg, apiServer, svc, ob)
	if err != nil {
		// A certificate file that does not load leaves the runtime plain rather than
		// failing the call: this helper serves a caller with no console, which is
		// every test of the listener's routing. `ob` is carried on the struct, so this
		// fallback still builds an instrumented listener.
		rt = &portalRuntime{cfg: cfg, apiServer: apiServer, svc: svc, now: time.Now, ob: ob}
	}
	return rt.plainListener()
}

// portalMux is the whole guest portal, whichever scheme it is served on.
//
// The RFC 8908 answer reads the portal and venue addresses per request, from the
// runtime, because an operator can change either in the console while it serves.
func portalMux(rt *portalRuntime) *http.ServeMux {
	apiServer, svc := rt.apiServer, rt.svc
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/captive-portal", func(w http.ResponseWriter, r *http.Request) {
		svc.CaptiveHandler(rt.portalURL(), rt.venueURL())(w, r)
	})
	mux.Handle(api.BasePath+"/", apiServer.PortalHandler())
	// Venue photographs, as raw bytes. Mounted before the document catch-all:
	// `webui.Portal()` answers 404 for anything it does not carry, and a
	// photograph is not a document. Public — a guest's `<img>` loads it before
	// authentication — on the listener bound wide, never on the console's.
	mux.Handle("/venue/", apiServer.VenueAssetHandler())
	// `/healthz` and `/readyz` — the two process-level answers, and **not**
	// `/metrics`, which is on the policy listener alone. See [policyListener].
	rt.ob.healthHandlers(mux)
	// The front door: the URL the access point sends a guest's browser to, and the
	// only place a MAC is ever learned. It does not call the driver — the Captive
	// Network Assistant probes this address repeatedly while a guest reads a page,
	// and a controller round trip per probe is a denial of service the venue pays
	// for.
	//
	// **No method on this pattern, and it is not a shortcut.** `GET /` beside
	// `/api/v0/` is a conflict `http.ServeMux` panics on at registration — it
	// matches fewer methods and a more general path, so neither pattern is more
	// specific and the mux refuses both. The process would not start. A bare `/`
	// *is* less specific than `/api/v0/`, so the API keeps its routes and this
	// catches everything else.
	//
	// **The documents go in front of it, and the order is the whole fix.** If
	// nothing serves `/en/`, it falls to this catch-all, which redirects to `/en/`
	// — an infinite loop, observed on a deployment. The Go binary serves these
	// documents in production; a test that asserts only the redirect never asks
	// what happens next.
	//
	// `EntryHandler` stays exactly what it was — it binds the browser to the MAC
	// the NAS named and negotiates a language — and now runs only for paths the
	// frontend does not have, which is what `/` is.
	// **`EntryHandler` answers `/` and nothing else, and that is the invariant
	// rather than a tidy-up.** The loop it prevents is not "a document is
	// missing" — it is the handler which *redirects to* `/{lang}/` also being
	// the handler that *receives* `/{lang}/`. Any composition where those are the
	// same handler can loop, and the obvious repair does: falling through to
	// `entry` when the frontend has no document puts the cycle straight back for
	// a binary built without one.
	//
	// So the split is by path, not by whether a document happens to exist. `/` is
	// the front door and redirects; everything else is the frontend's, and its
	// answer for something it does not have is a 404 — or a 503 naming the missing
	// build step when the binary carries no frontend at all. Both are answers. A
	// redirect to yourself is not.
	//
	// `PortalLanguages()` is what keeps the redirect inside that rule: it
	// negotiates only among languages the embedded build actually carries, so `/`
	// cannot send a guest to a document that was never built.
	//
	// The UniFi controller's External Portal field takes a bare IP and appends
	// `:8880/guest/s/<site>/` itself, so a redirected guest never lands on `/`.
	// That path is mounted on the same `EntryHandler`: it reads only the query
	// string, so it is path-agnostic, and serving it anywhere else would drop
	// the `id`/`ap` the browser-to-MAC binding is built from. A second handler
	// that bounced it to `/` would cost the CNA an extra navigation for nothing.
	entry := svc.EntryHandler(webui.PortalLanguages(), "en")
	docs := apiServer.GuestDocumentsHandler()
	mux.Handle("/{$}", entry)
	mux.Handle("/guest/", entry)
	mux.Handle("/en/", docs)
	mux.Handle("/vi/", docs)
	mountPortalFallback(mux, webui.Built(), webui.Portal())
	return mux
}

// mountPortalFallback is the portal listener's catch-all, and it has the same two
// states [mountConsole] has, for the same reason.
//
// With a frontend embedded, `webui.Portal()` answers the documents it carries and
// `404` for everything else, which is what makes it safe as a catch-all. **With
// none embedded it answers `503` on every path** — the diagnostic that names the
// missing build step — and as a catch-all that would put a `503` on `/de/`, on
// `/en/../../etc/passwd`, on every scan and typo a guest-reachable listener
// receives. `TestGuestUnknownPathsAreNotDocuments` would then fail on every fresh
// clone and pass in every tree that had run `make web-embed`.
//
// Nothing a guest needs is behind the catch-all: `/en/` and `/vi/` are
// rendered by `GuestDocumentsHandler` from Go whether or not a build is
// embedded, and `/` is the front door. So the unbuilt listener keeps the plain
// invariant — no catch-all, an unknown path is a `404` — and the `503` stays
// reachable where an operator meets it, at the console's root. The test drives
// both states, because a tree can only be in one.
func mountPortalFallback(mux *http.ServeMux, built bool, fallback http.Handler) {
	if !built {
		return
	}
	mux.Handle("/", fallback)
}

// The header ceiling on the two listeners people reach replaces net/http's 1 MiB
// default. A request line carries the NAS redirect's query string — the guest's
// original URL among it — and a cookie or two; the 64 KiB default is far past
// both. It is `api.DefaultHeaderBytes`, set in the console and
// enforced per request by `api.Server.LimitHeaders` below `api.MaxHeaderCeiling`,
// which is what each listener is built with.

// generateRaddb writes a FreeRADIUS tree beside the control plane.
//
// ── why it derives everything ────────────────────────────────────────────────
//
// `raddb.DefaultConfig()` with **one** field overridden leaves `PostgresPassword`
// empty, and the generator refuses — correctly, because `read_clients = yes`
// reads the client table before the first packet and a FreeRADIUS that cannot
// reach PostgreSQL does not start.
//
// So everything is derived, from what this process is itself using:
//
//   - the database's location, from `FOYER_DATABASE_URL`. Not from a second
//     variable: a generated `rlm_sql` block pointing somewhere other than the
//     control plane's own database is a failure with no symptom until an
//     operator wonders why a new access point never appears.
//   - the login, NOT from there. FreeRADIUS logs in as its own role,
//     `foyer_radius`, with FOYER_POSTGRES_PASSWORD; this process logs in as
//     `foyer_app`, which may not write radacct (migration 00019).
//     `config.Postgres` makes that split and says why.
//   - the policy URL and its scheme, from the listener this process bound, so the
//     generated module cannot point at a port nothing answers or claim https
//     against a plaintext listener.
//   - the mTLS material, from FOYER_RADDB_POLICY_*. Those are paths inside the
//     FreeRADIUS container and cannot be derived from this process's own
//     certificate paths — on a deployment the two directories are deliberately
//     different, because the FreeRADIUS one holds no CA key.
//
// It returns an error rather than writing a partial tree, and `run` logs it
// without exiting: the control plane serves the console and the portal whether or
// not a FreeRADIUS tree was written beside it.
func generateRaddb(cfg config.Config) error {
	genCfg, err := raddbBaseConfig(cfg)
	if err != nil {
		return err
	}
	return raddb.NewGenerator(genCfg).Generate(cfg.RaddbTarget)
}

// raddbBaseConfig is everything above that is derived from this process, before
// the operator's settings (radius.go) are laid over it.
func raddbBaseConfig(cfg config.Config) (raddb.Config, error) {
	pg, err := cfg.Postgres()
	if err != nil {
		return raddb.Config{}, err
	}

	genCfg := raddb.DefaultConfig()
	genCfg.PostgresHost = pg.Host
	genCfg.PostgresPort = pg.Port
	genCfg.PostgresDB = pg.Database
	genCfg.PostgresUser = pg.User
	genCfg.PostgresPassword = pg.Password
	genCfg.PostgresPasswordEnv = pg.PasswordEnv
	genCfg.PolicyAPIURL = fmt.Sprintf("%s://%s/api/v1/aaa/authorize", policyScheme(cfg), policyReachable(cfg.PolicyAddr, cfg.PolicyHost))
	genCfg.PolicyClientCertFile = cfg.RaddbPolicyClientCert
	genCfg.PolicyClientKeyFile = cfg.RaddbPolicyClientKey
	genCfg.PolicyServerCAFile = cfg.RaddbPolicyServerCA
	return genCfg, nil
}

func policyScheme(cfg config.Config) string {
	if cfg.PolicyTLSCert != "" && cfg.PolicyTLSKey != "" {
		return "https"
	}
	return "http"
}

// policyReachable is the host:port FreeRADIUS should call.
//
// `FOYER_POLICY_HOST` (config.PolicyHost) overrides it, because in the lab and on
// a deployment FreeRADIUS is a different container and has to reach this process
// by service name rather than by whatever address it bound inside its own
// namespace.
func policyReachable(addr, override string) string {
	if override != "" {
		return override
	}
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host := strings.Trim(addr[:i], "[]")
		if host == "" || host == "0.0.0.0" || host == "::" {
			return "127.0.0.1" + addr[i:]
		}
	}
	return addr
}
