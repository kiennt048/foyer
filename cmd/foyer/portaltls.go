package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/obs"
	"github.com/kiennt048/foyer/internal/portal"
	"github.com/kiennt048/foyer/internal/redact"
)

// The optional HTTPS portal.
//
// ── what the field does, and what this copies ────────────────────────────────
//
// Every product surveyed keeps **plain HTTP for the interception** and moves the
// portal itself to HTTPS when the operator supplies a certificate:
//
//   - UniFi redirects to `http://<portal>:8880/guest/s/<site>/`, or to
//     `https://<portal>:8843/…` when the controller's HTTPS redirect is on. The
//     interception of the guest's first plaintext request is always HTTP; only
//     the portal URL it lands on changes scheme.
//   - PacketFence's guidance, repeated on its list for a decade, is the same: the
//     redirect cannot be HTTPS without a certificate error, so the portal is served
//     over HTTPS on a certificate the guest's device trusts and the plaintext side
//     exists to send people there.
//   - Fortinet, Aruba, pfSense and the UniFi community all document the pitfall
//     that decides the other half: a browser that has HSTS for the site the guest
//     *asked for* will never follow an interception to anything. That is why the
//     interception must be HTTP, and why the portal itself **never sends
//     `Strict-Transport-Security`** — HSTS on the portal's own name would, the
//     day the operator removes or lets lapse the certificate, turn a bypassable
//     warning into a hard failure on every phone that had visited, with no way
//     for the venue to undo it. Browsers ignore HSTS on an IP literal anyway.
//
// So, with a certificate configured: the HTTP listener still answers — it is
// where a NAS redirect lands — and sends every GET to the same path and query on
// the HTTPS origin of FOYER_PORTAL_URL. The HTTPS listener serves the whole
// portal, including RFC 8908, which `docs/architecture.md` §7 requires over
// https.
//
// ── why nothing here generates a certificate ─────────────────────────────────
//
// The console generates its own (`internal/tlsca`) because an operator types
// nothing for it. The portal is the opposite case: the guest is never shown a
// warning nobody chose, and a generated self-signed certificate in front of every
// guest is precisely that. An operator who wants TLS supplies the pair — publicly
// trusted for a name, which is the only kind an operating system's RFC 8908 client
// accepts, or self-signed with the venue's address in it, at a measured cost of one
// tap per device. Nothing is shipped in an image (`docs/rules/security.md` §7).
//
// ── the certificate is set in the console, and the listener follows it ───────
//
// An operator sets the portal certificate in the console, it is stored enveloped in the database, and it takes effect without a
// restart. So the two portal listeners do not decide their shape at start.
// `portalRuntime` holds what they serve and is told every change by
// `api.Server.ReloadNetwork` (`ApplyNetwork`):
//
//   - the certificate a handshake is answered with is looked up **per handshake**
//     — the console's pair when it is servable at the portal address in force,
//     otherwise the deployment's FOYER_PORTAL_TLS_* files (still reloaded when they
//     change), otherwise none;
//   - the plain-HTTP listener decides **per request** whether it is the portal or
//     the redirect to it;
//   - the HTTPS listener is bound the first time there is something to serve on
//     it, which may be long after start: uploading the first certificate starts it.
//
// A pair that stops being servable — expired, or no longer naming the portal's
// address after the address changed — is reported as `not-served` with the reason,
// and the plain-HTTP listener goes back to serving the portal itself, rather than
// sending every guest to a certificate error.

// portalRuntime is the portal's changing state, and api.NetworkRuntime.
type portalRuntime struct {
	cfg       config.Config
	apiServer *api.Server
	svc       *portal.Service
	now       func() time.Time

	// env is the deployment's certificate files, nil when none are configured.
	env *portalCertificate

	// eff is the network set in force; nil until the first ApplyNetwork, when the
	// deployment's own values apply.
	eff atomic.Pointer[api.EffectiveNetwork]

	mu sync.Mutex
	// tls is the HTTPS listener once it exists. lazy says run() has started
	// serving, so a listener needed later is bound here rather than by run().
	tls       *namedServer
	lazy      bool
	listenErr string

	// ob is the process's observability, carried here rather than threaded through
	// the three functions that build this runtime's listeners.
	//
	// **`tlsListener` is built from three places** — at boot, lazily when an
	// operator uploads a certificate, and from a test — and a parameter would have
	// to be added to all of them and to the runtime's constructors. A field makes
	// the observability part of the runtime's identity, which it is: the runtime is
	// the thing that changes what the portal serves, and a listener it builds must
	// be instrumented the same way whichever path built it. It is set in
	// [newPortalRuntime], so no listener can be built without it.
	ob *observability
}

var _ api.NetworkRuntime = (*portalRuntime)(nil)

// newPortalRuntime loads the deployment's certificate files, if any. A pair that
// does not load or does not cover the portal's host is refused, as it was before
// the console could supply one: this is the start-up assertion.
func newPortalRuntime(cfg config.Config, apiServer *api.Server, svc *portal.Service, ob *observability) (*portalRuntime, error) {
	rt := &portalRuntime{cfg: cfg, apiServer: apiServer, svc: svc, now: time.Now, ob: ob}
	if cfg.PortalTLSConfigured() {
		// The host is the portal address **in force**, asked each time a file is
		// checked: FOYER_PORTAL_URL at start, and whatever the console saved after
		// that. A replacement file is held to the address guests are sent to now.
		env, err := newPortalCertificate(cfg.PortalTLSCert, cfg.PortalTLSKey, rt.portalHost, func() time.Time { return rt.now() })
		if err != nil {
			return nil, err
		}
		slog.Default().Info("the deployment portal certificate", "detail", env.describe())
		rt.env = env
	}
	return rt, nil
}

func (rt *portalRuntime) portalURL() string {
	if e := rt.eff.Load(); e != nil {
		return e.PortalURL
	}
	return rt.cfg.PortalURL
}

// portalHost is the host of the portal address in force, "" when it has none.
func (rt *portalRuntime) portalHost() string {
	host, _, err := config.PortalURLHost(rt.portalURL())
	if err != nil {
		return ""
	}
	return host
}

func (rt *portalRuntime) venueURL() string {
	if e := rt.eff.Load(); e != nil {
		return e.VenueURL
	}
	return rt.cfg.VenueURL
}

func (rt *portalRuntime) consolePair() *api.PortalPair {
	if e := rt.eff.Load(); e != nil {
		return e.PortalCertificate
	}
	return nil
}

// served is the certificate a handshake gets now, and nil when there is none.
func (rt *portalRuntime) served() *tls.Certificate {
	now, u := rt.now(), rt.portalURL()
	if p := rt.consolePair(); p != nil {
		if _, ok := api.NotServedReason(p.Certificate.Leaf, u, now); ok {
			return &p.Certificate
		}
	}
	if rt.env != nil {
		if c := rt.env.currentCert(); c != nil {
			if _, ok := api.NotServedReason(c.Leaf, u, now); ok {
				return c
			}
		}
	}
	return nil
}

// GetCertificate is the HTTPS listener's tls.Config hook.
func (rt *portalRuntime) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	if rt.env != nil {
		// Picks up a renewed file before the choice below reads it.
		_, _ = rt.env.GetCertificate(hello)
	}
	if c := rt.served(); c != nil {
		return c, nil
	}
	return nil, errors.New("no portal certificate is servable at the portal's address")
}

// httpsOrigin is where the plain-HTTP listener sends guests, or "" when it is the
// portal itself: nothing servable, or an HTTPS listener that could not bind.
func (rt *portalRuntime) httpsOrigin() string {
	if rt.served() == nil {
		return ""
	}
	rt.mu.Lock()
	failed := rt.listenErr != ""
	rt.mu.Unlock()
	if failed {
		return ""
	}
	u, err := url.Parse(rt.portalURL())
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return ""
	}
	return "https://" + u.Host
}

// ApplyNetwork is told every new set. It binds the HTTPS listener the first time
// there is a certificate to serve on it.
func (rt *portalRuntime) ApplyNetwork(eff api.EffectiveNetwork) {
	rt.eff.Store(&eff)
	if rt.served() == nil {
		return
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.tls != nil || !rt.lazy {
		return
	}
	s := rt.tlsListener()
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		rt.listenErr = err.Error()
		slog.Default().Error("the HTTPS guest portal could not listen, so guests stay on plain HTTP",
			"address", s.srv.Addr, "error", err.Error())
		return
	}
	s.srv.Addr = ln.Addr().String()
	rt.tls, rt.listenErr = s, ""
	slog.Default().Info("the listener is serving, started by a console change", "listener", s.name, "address", s.srv.Addr, "scheme", s.scheme())
	go func() {
		if err := s.serveOn(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			rt.mu.Lock()
			rt.listenErr = err.Error()
			rt.mu.Unlock()
			slog.Default().Error("the HTTPS guest portal stopped", "error", err.Error())
		}
	}()
}

// networkLoaded reports whether the stored network settings have been read into
// this process, which is the local half of the `/readyz` driver-path check
// (`observability.go`).
//
// **`eff` is the mirror of `api.Server`'s own copy, and that is deliberate.**
// `api.Server.network()` is unexported — `PortalURL()` and `VenueURL()` are its
// only accessors — and exporting it for a probe would widen a change to a package
// with other owners for the sake of one boolean. `eff` is written by
// `ApplyNetwork`, which `api.Server.putNetwork` calls at the end of every
// successful `ReloadNetwork`, so the two are set together and this reads the same
// fact.
//
// `PortalURL()` could not answer the question instead: it falls back to the
// bootstrap values when nothing has been loaded, so it returns a non-empty string
// either way and would report ready for a process that had never read the
// console's settings. That fallback is right for serving and wrong for a probe,
// which is why this is a separate question with a separate answer.
func (rt *portalRuntime) networkLoaded() bool { return rt.eff.Load() != nil }

// PortalCertificateStatus is what the portal presents now.
func (rt *portalRuntime) PortalCertificateStatus() gen.PortalCertificateStatus {
	now, u := rt.now(), rt.portalURL()
	var st gen.PortalCertificateStatus
	switch {
	case rt.consolePair() != nil:
		st = api.DescribePortalPair(rt.consolePair(), u, now)
	case rt.env != nil && rt.env.currentCert() != nil:
		c := rt.env.currentCert()
		st = api.DescribeCertificateInfo(config.DescribeCertificate(c.Leaf, len(c.Certificate)), gen.PortalCertificateStatusSourceEnvironment)
		if reason, ok := api.NotServedReason(c.Leaf, u, now); !ok {
			st.State, st.NotServedReason = gen.PortalCertificateStateNotServed, &reason
		}
	default:
		return gen.PortalCertificateStatus{State: gen.PortalCertificateStateNone, Source: gen.PortalCertificateStatusSourceNone}
	}
	rt.mu.Lock()
	failed := rt.listenErr != ""
	rt.mu.Unlock()
	if st.State == gen.PortalCertificateStateServed && failed {
		reason := gen.PortalCertificateStatusNotServedReasonListenerFailed
		st.State, st.NotServedReason = gen.PortalCertificateStateNotServed, &reason
	}
	return st
}

// startTLSAtBoot answers the HTTPS listener run() starts with the others, or nil
// when there is nothing to serve yet — and from then on a listener needed later is
// bound by ApplyNetwork.
func (rt *portalRuntime) startTLSAtBoot() *namedServer {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.lazy = true
	if rt.served() == nil {
		return nil
	}
	rt.tls = rt.tlsListener()
	return rt.tls
}

// lateTLS is the HTTPS listener, for run() to shut down with the others: one that
// ApplyNetwork bound after start is in no other list.
func (rt *portalRuntime) lateTLS() *namedServer {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.tls
}

func (rt *portalRuntime) tlsListener() *namedServer {
	tlsCfg := &tls.Config{GetCertificate: rt.GetCertificate, MinVersion: tls.VersionTLS12}
	return &namedServer{
		name:     "Guest portal (HTTPS)",
		maxConns: portalMaxConns,
		// The same listener-wide body and header ceilings as the plain-HTTP portal
		// listener, read per request: it serves the same mux to the same
		// unauthenticated guests.
		srv: &http.Server{
			Addr: rt.cfg.PortalTLSAddr,
			// A failed handshake is logged with the client's address; masked.
			ErrorLog: redact.ErrorLog(),
			// The same `portal` label as the plain listener, deliberately: these are
			// one audience on two schemes, and a `portal-tls` label would make
			// "how many guests are hitting the portal" two sums nobody remembers to
			// add. The scheme is in the access line's `listener` field's sibling
			// `scheme`, which the log carries and the metric does not need.
			Handler:        rt.ob.instrument(obs.NewPortalListener(rt.ob.log), rt.limits(portalMux(rt))),
			TLSConfig:      tlsCfg,
			ReadTimeout:    10 * time.Second,
			WriteTimeout:   10 * time.Second,
			MaxHeaderBytes: api.MaxHeaderCeiling,
		},
		tls: tlsCfg,
	}
}

// plainListener is the plain-HTTP portal: the portal itself, or the redirect to
// the HTTPS origin while a certificate is served — decided per request.
func (rt *portalRuntime) plainListener() *namedServer {
	mux := portalMux(rt)
	redirect := toHTTPS(rt.httpsOrigin)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rt.httpsOrigin() != "" {
			redirect.ServeHTTP(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	return &namedServer{
		name:     "Guest portal",
		maxConns: portalMaxConns,
		// Listener-wide body and header ceilings, for the same reason as the
		// console's and with more at stake: this is the port every device in radio
		// range reaches before it has authenticated. `internal/api` answers the
		// contract's 413 on its own operations; this covers the document handlers,
		// the front door, the HTTPS redirector and whatever is mounted here next.
		// Both ceilings are read per request, so a console change binds at once.
		srv: &http.Server{
			Addr: rt.cfg.PortalAddr,
			// A failed handshake is logged with the client's address; masked.
			ErrorLog: redact.ErrorLog(),
			// The middleware is the outermost layer, so it wraps the ceilings and the
			// HTTPS redirector as well as the mux — a redirect to the https origin is
			// a request an operator needs counted, and a request refused for its body
			// is one they need to see refused.
			Handler:        rt.ob.instrument(obs.NewPortalListener(rt.ob.log), rt.limits(handler)),
			ReadTimeout:    10 * time.Second,
			WriteTimeout:   10 * time.Second,
			MaxHeaderBytes: api.MaxHeaderCeiling,
		},
	}
}

func (rt *portalRuntime) limits(h http.Handler) http.Handler {
	return rt.apiServer.LimitHeaders(api.LimitBody(h, rt.apiServer.PortalListenerBodyLimit))
}

// portalTLSListener is the HTTPS portal on the deployment's certificate files, or
// nil when none are configured. Kept for the tests that drive the file path; run()
// goes through the runtime.
func portalTLSListener(cfg config.Config, apiServer *api.Server, svc *portal.Service, ob *observability) (*namedServer, error) {
	rt, err := newPortalRuntime(cfg, apiServer, svc, ob)
	if err != nil || rt.env == nil {
		return nil, err
	}
	return rt.tlsListener(), nil
}

// toHTTPS is the plain-HTTP portal listener while a certificate is served.
//
// `302`, never `301` or `308`: a permanent redirect is cached by the user agent,
// and a captive portal is the one place a client holding on to last week's
// routing decision is a device that cannot get online — the same reason
// `EntryHandler` uses `302`. The query string is relayed verbatim, because the NAS
// parameters in it are the only evidence `VerifyPresence` has (docs/rules/security.md §4);
// the session and its cookie are minted on the HTTPS side, where the cookie is
// `Secure` and belongs to the origin the guest will actually use.
//
// A request that is not a GET or HEAD is **refused, not redirected**. Following a
// `307` would resend the body — a voucher or a password — which has already
// crossed the air in the clear once; the refusal at least guarantees nothing is
// granted on the strength of a plaintext submission while the operator has asked
// for HTTPS.
//
// The origin is read per request, because an operator can change it.
func toHTTPS(origin func() string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", obs.Liveness)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "This portal accepts submissions over HTTPS only.", http.StatusForbidden)
			return
		}
		path := r.URL.EscapedPath()
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		target := origin() + path
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound)
	})
	return mux
}

// portalCertificate serves the operator's pair and picks up a replacement.
//
// **Reloaded when the files change, checked per handshake.** An operator's
// certificate is renewed by something outside this process — ACME every sixty to
// ninety days — and the console's lesson (`deploy/` schedules no restart, ever)
// applies unchanged: a pair read once at start is a portal that reaches expiry in
// service. Two `stat` calls per handshake is the price.
//
// **The same checks as a pair uploaded in the console**, from `internal/config`:
// PEM of the right kind, the key the leaf's, the chain in order
// (`ParseCertificateChain`), the leaf naming the portal's host (`CoversHost`), and
// the clock (`CheckValidAt`). At start a pair whose only fault is the clock is
// loaded, logged, and not served — `api.NotServedReason` says `expired` or
// `not-yet-valid` — because refusing to start would take RADIUS down with the
// portal. Any other fault refuses the start, as it always has.
//
// A replacement that fails **any** of them, the clock included, is **refused and
// the previous pair kept**, loudly. A renewal script that writes the certificate
// before the key must not take the portal down for the second between, and a
// replacement that is not valid yet must not displace one that is.
type portalCertificate struct {
	certPath, keyPath string
	host              func() string
	now               func() time.Time

	mu       sync.Mutex
	current  *tls.Certificate
	certStat fileStamp
	keyStat  fileStamp
	lastErr  string
}

type fileStamp struct {
	mod  time.Time
	size int64
}

func stamp(path string) (fileStamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}, err
	}
	return fileStamp{mod: fi.ModTime(), size: fi.Size()}, nil
}

func newPortalCertificate(certPath, keyPath string, host func() string, now func() time.Time) (*portalCertificate, error) {
	p := &portalCertificate{certPath: certPath, keyPath: keyPath, host: host, now: now}
	cert, cs, ks, err := p.load(false)
	if err != nil {
		return nil, err
	}
	p.current, p.certStat, p.keyStat = cert, cs, ks
	return p, nil
}

// load reads and checks the pair; clock says whether a pair not valid now is
// refused (a replacement) or loaded to be reported (the start). The error names
// paths, hosts and the fault, never contents: it reaches the process log.
func (p *portalCertificate) load(clock bool) (*tls.Certificate, fileStamp, fileStamp, error) {
	cs, err := stamp(p.certPath)
	if err != nil {
		return nil, cs, fileStamp{}, fmt.Errorf("FOYER_PORTAL_TLS_CERT %q: %w", p.certPath, err)
	}
	ks, err := stamp(p.keyPath)
	if err != nil {
		return nil, cs, ks, fmt.Errorf("FOYER_PORTAL_TLS_KEY %q: %w", p.keyPath, err)
	}
	certPEM, err := os.ReadFile(p.certPath)
	if err != nil {
		return nil, cs, ks, fmt.Errorf("FOYER_PORTAL_TLS_CERT %q: %w", p.certPath, err)
	}
	keyPEM, err := os.ReadFile(p.keyPath)
	if err != nil {
		return nil, cs, ks, fmt.Errorf("FOYER_PORTAL_TLS_KEY %q: %w", p.keyPath, err)
	}
	cert, chain, err := config.ParseCertificateChain(certPEM, keyPEM)
	if err != nil {
		return nil, cs, ks, fmt.Errorf("the portal certificate pair %q / %q: %w", p.certPath, p.keyPath, err)
	}
	// **The name check is the one that matters and nothing else would make it.**
	// A certificate that is valid, correctly chained and issued for another name
	// is refused by every guest's browser on the name, and nothing in this process
	// would otherwise know — the console's FOYER_ADMIN_TLS_NAMES history is this
	// exact failure. VerifyHostname handles an IP literal against IP SANs too.
	if host := p.host(); host == "" {
		return nil, cs, ks, fmt.Errorf("the portal certificate %q cannot be checked: the portal address in force names no host", p.certPath)
	} else if err := config.CoversHost(chain[0], host); err != nil {
		return nil, cs, ks, fmt.Errorf("the portal certificate %q does not cover %q, the host the portal address sends guests to: %w",
			p.certPath, host, err)
	}
	if clock {
		if err := config.CheckValidAt(chain, p.now()); err != nil {
			return nil, cs, ks, fmt.Errorf("the portal certificate %q: %w", p.certPath, err)
		}
	}
	return &cert, cs, ks, nil
}

// GetCertificate is the tls.Config hook.
func (p *portalCertificate) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cs, errC := stamp(p.certPath)
	ks, errK := stamp(p.keyPath)
	if errC == nil && errK == nil && (cs != p.certStat || ks != p.keyStat) {
		cert, cs2, ks2, err := p.load(true)
		switch {
		case err == nil:
			p.current, p.certStat, p.keyStat, p.lastErr = cert, cs2, ks2, ""
			slog.Default().Info("the portal certificate", "detail", p.describeLocked())
		case err.Error() != p.lastErr:
			// Said once per distinct failure, not once per handshake.
			p.lastErr = err.Error()
			slog.Default().Warn("the replacement portal certificate was refused and the previous one is still served", "error", err.Error())
		}
	}
	if p.current == nil {
		return nil, errors.New("no portal certificate")
	}
	return p.current, nil
}

func (p *portalCertificate) describe() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.describeLocked()
}

func (p *portalCertificate) describeLocked() string {
	leaf := p.current.Leaf
	sum := sha256.Sum256(leaf.Raw)
	msg := fmt.Sprintf("Guest portal certificate %s: covers %s, valid until %s, SHA-256 %s",
		p.certPath, p.host(), leaf.NotAfter.UTC().Format(time.RFC3339), hex.EncodeToString(sum[:]))
	now := p.now()
	switch left := leaf.NotAfter.Sub(now); {
	case now.Before(leaf.NotBefore):
		msg += fmt.Sprintf(". WARNING: it is NOT VALID until %s and is not served before then", leaf.NotBefore.UTC().Format(time.RFC3339))
	case left <= 0:
		msg += ". WARNING: it has EXPIRED; every guest is shown a certificate error"
	case left < 14*24*time.Hour:
		msg += fmt.Sprintf(". WARNING: it expires in %d hours and nothing in Foyer renews it", int(left.Hours()))
	}
	return msg
}

// currentCert is the pair loaded from the files, without looking at them again.
func (p *portalCertificate) currentCert() *tls.Certificate {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}
