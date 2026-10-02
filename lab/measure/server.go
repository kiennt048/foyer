package main

import (
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type server struct {
	log  *eventLog
	tlsm *tlsMaterial

	mu      sync.Mutex
	captive bool

	staticDir string
	proxy     *httputil.ReverseProxy

	// probeDir is the portal build's diagnostic output, `web/portal/dist-probe`.
	// It is served at `/{lang}/probe/` and nowhere else, and only when the flag
	// names it — see servePortalProbe.
	probeDir  string
	proxyName string

	// httpHost and httpsHost are what /switch redirects to. They carry the LAN
	// address rather than the Host header, because a client that reached this
	// host by an address the other listener does not answer on would otherwise
	// be sent somewhere that does not exist.
	httpHost  string
	httpsHost string

	ttf  []byte
	woff []byte
}

func (s *server) captiveState() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.captive
}

func (s *server) setCaptive(v bool) {
	s.mu.Lock()
	s.captive = v
	s.mu.Unlock()
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/probe", http.StatusSeeOther)
	})
	mux.HandleFunc("GET /probe", s.handleProbe)

	// A real vendor redirect does not land on /probe, and finding that out with a
	// phone in your hand costs the measurement.
	//
	// Measured against a UniFi access point: the access point intercepts and
	// answers
	//
	//   http://<portal_hostname>:8880/guest/s/<site>/?ap=…&id=…&t=…&url=…&ssid=…
	//
	// The host is `portal_hostname`, which the controller REFUSES to accept with a
	// port in it; the port is UniFi's own 8880, or 8843 when redirect_https is set;
	// and the path is fixed. So an external portal that serves only its own routes
	// answers 404 to every redirected guest, and a CNA shows a blank page that looks
	// exactly like a rejected certificate.
	mux.HandleFunc("GET /guest/", s.handleProbe)
	mux.HandleFunc("POST /captive", s.handleCaptiveToggle)
	mux.HandleFunc("POST /report", s.handleReport)
	mux.HandleFunc("GET /log", s.handleLog)
	mux.HandleFunc("GET /switch", s.handleSwitch)
	mux.HandleFunc("GET /captive-portal/api", s.handleCaptiveAPI)
	mux.HandleFunc("GET /ca.crt", s.handleCA)
	mux.HandleFunc("GET /probe-font.ttf", s.serveFont("font/ttf", s.ttf))
	mux.HandleFunc("GET /probe-font.woff", s.serveFont("font/woff", s.woff))

	// The *portal's* measurement page, in the portal's own languages and on the
	// portal's own stylesheet: storage, viewport and colour scheme, and the
	// HTTPS question.
	//
	// It is served here and not by the Go binary, for two reasons that are really
	// one. `POST /report` above is the only route in the repository that records
	// one of these digests, so a page served anywhere else would send its values
	// into a 404 and the measurement would go back to being a photograph. And the
	// guest listener answers every path anybody within radio range can reach: a
	// diagnostic is not a thing a venue's guests may read.
	//
	// Registered ahead of the catch-all proxy below, which is what makes "the same
	// origin as the portal" true with `-proxy` in play — the browser sees one
	// origin, and `/report` is this host's.
	//
	// **Both spellings, and both exact.** `"/en/probe"` is exact because it names
	// a file; `"/en/probe/"` would be a *prefix* pattern matching the whole
	// subtree, so it takes `{$}` to be exact too. A person types the trailing
	// slash and a CNA re-checks captivity only after a full navigation, so both
	// have to answer — and neither has to answer for `/en/probe/en/probe/`.
	for _, lang := range portalProbeLangs {
		for _, p := range []string{"/" + lang + "/probe", "/" + lang + "/probe/{$}"} {
			mux.HandleFunc("GET "+p, s.handlePortalProbe(lang))
		}
	}

	// The operating systems' own connectivity probes. Serving them is what makes
	// arrangement B — interception from the laptop — measurable at all: with the
	// phone's traffic pointed here, the log shows which probe each OS actually
	// reaches for, and "does iOS still use the legacy hotspot-detect probe on
	// first association" is answered from the log rather than from tcpdump.
	for _, p := range []string{
		"GET /hotspot-detect.html",       // iOS, macOS
		"GET /library/test/success.html", // iOS, macOS, older path
		"GET /generate_204",              // Android
		"GET /gen_204",                   // Android
		"GET /connecttest.txt",           // Windows
		"GET /ncsi.txt",                  // Windows NCSI
		"GET /success.txt",               // Firefox
		"GET /canonical.html",            // Ubuntu, GNOME
	} {
		mux.HandleFunc(p, s.handleConnectivityProbe)
	}

	if s.staticDir != "" {
		mux.Handle("GET /site/", http.StripPrefix("/site/", http.FileServer(http.Dir(s.staticDir))))
	}

	mux.HandleFunc("/", s.handleFallback)

	return s.withLogging(mux)
}

// withLogging records one event per completed request. The User-Agent is the
// field that matters most: the CNA's own probe identifies itself as
// CaptiveNetworkSupport, and its WebView does not — so one log tells you whether
// the operating system fetched something or whether a person tapped a link.
func (s *server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		transport := "http"
		if r.TLS != nil {
			transport = tlsVersionName(r.TLS.Version)
		}
		s.log.add(event{
			Kind:   "http",
			Peer:   r.RemoteAddr,
			Detail: fmt.Sprintf("%s %s -> %d  %s", r.Method, r.URL.Path, rec.status, transport),
			UA:     r.Header.Get("User-Agent"),
		})
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *server) handleProbe(w http.ResponseWriter, r *http.Request) {
	data := probeData{
		Host:          r.Host,
		UA:            r.Header.Get("User-Agent"),
		Captive:       s.captiveState(),
		ServerTime:    time.Now().Format("15:04:05"),
		WidthBuckets:  widthBuckets,
		HeightBuckets: heightBuckets,
		Loopback:      isLoopbackPeer(r.RemoteAddr),
	}

	if r.TLS != nil {
		data.Scheme = "HTTPS"
		data.SchemeClass = "tls"
		data.SchemeNote = "served over TLS with the self-signed certificate"
		data.TLSDetail = fmt.Sprintf("%s  %s", tlsVersionName(r.TLS.Version), tls.CipherSuiteName(r.TLS.CipherSuite))
	} else {
		data.Scheme = "HTTP"
		data.SchemeClass = "plain"
		data.SchemeNote = "served in the clear, no TLS at all"
		data.TLSDetail = "no TLS"
	}

	sum := sha256.Sum256(s.tlsm.leaf.Raw)
	data.CertFingerprint = hex.EncodeToString(sum[:4])
	data.CertNotAfter = s.tlsm.leaf.NotAfter.Format("2006-01-02")

	if data.Captive {
		data.CaptiveJSON = `captive: true`
	} else {
		data.CaptiveJSON = `captive: false`
	}

	// The cookie test is answered by the server so that it survives JavaScript
	// being off, and because the portal's own session cookie is the thing whose
	// survival inside the CNA actually matters.
	if c, err := r.Cookie("foyer_probe"); err == nil && c.Value != "" {
		data.CookieVerdict = "sent back a cookie set at " + c.Value
		data.CookieClass = "yes"
	} else {
		data.CookieVerdict = "no cookie came back; one is set now, reload to test"
		data.CookieClass = "info"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "foyer_probe",
		Value:    time.Now().Format("15:04:05"),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   3600,
	})

	writeHTML(w, http.StatusOK, probeTmpl, data)
}

func (s *server) handleCaptiveToggle(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeHTML(w, http.StatusBadRequest, notFoundTmpl, "that form could not be read")
		return
	}
	next := r.PostFormValue("state") == "true"
	s.setCaptive(next)
	s.log.add(event{Kind: "state", Peer: r.RemoteAddr, Detail: fmt.Sprintf("captive set to %t", next)})

	// 303 rather than a rendered page: a full document navigation is the only
	// event that makes the CNA re-check captivity, so the control that flips the
	// answer also produces the navigation that lets the phone notice.
	http.Redirect(w, r, "/probe", http.StatusSeeOther)
}

func (s *server) handleLog(w http.ResponseWriter, r *http.Request) {
	events := s.log.events()
	writeHTML(w, http.StatusOK, logTmpl, logData{
		Events:    events,
		Count:     len(events),
		Captive:   s.captiveState(),
		ServerNow: time.Now().Format("15:04:05"),
	})
}

func (s *server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	target := "https://" + s.httpsHost + "/probe"
	if r.TLS != nil {
		target = "http://" + s.httpHost + "/probe"
	}
	// The absolute address lives in a Location header rather than in a document,
	// which is what keeps every page free of absolute URLs while still letting a
	// reader move between the two listeners with one tap.
	http.Redirect(w, r, target, http.StatusFound)
}

// handleCaptiveAPI is the RFC 8908 endpoint. The media type is the part that
// matters: a client that fetches this and gets application/json treats it as a
// failure, and the resulting blank screen looks exactly like a TLS refusal.
func (s *server) handleCaptiveAPI(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	portal := scheme + "://" + r.Host + "/probe"

	body := map[string]any{
		"captive":            s.captiveState(),
		"user-portal-url":    portal,
		"venue-info-url":     portal,
		"can-extend-session": true,
	}
	if !s.captiveState() {
		body["seconds-remaining"] = 3600
	}

	w.Header().Set("Content-Type", "application/captive+json")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(w).Encode(body)
}

func (s *server) handleCA(w http.ResponseWriter, r *http.Request) {
	// This media type is what makes iOS offer to install the certificate rather
	// than display it as text. Installing it is the fallback branch of the HTTPS question and is
	// worth measuring in the same sitting as the refusal.
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="foyer-measure-root.crt"`)
	_, _ = w.Write(s.tlsm.caPEM)
}

func (s *server) serveFont(mediaType string, body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", mediaType)
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(body)
	}
}

// handleConnectivityProbe answers an operating system's own captivity check.
// While captive it redirects, which is what an intercepting portal does; once
// released it returns the exact payload each OS treats as "you are online".
func (s *server) handleConnectivityProbe(w http.ResponseWriter, r *http.Request) {
	if s.captiveState() {
		http.Redirect(w, r, "/probe", http.StatusFound)
		return
	}
	switch r.URL.Path {
	case "/generate_204", "/gen_204":
		w.WriteHeader(http.StatusNoContent)
	case "/connecttest.txt":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Microsoft Connect Test"))
	case "/ncsi.txt":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("Microsoft NCSI"))
	case "/success.txt":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("success\n"))
	default:
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<HTML><HEAD><TITLE>Success</TITLE></HEAD><BODY>Success</BODY></HTML>"))
	}
}

func (s *server) handleFallback(w http.ResponseWriter, r *http.Request) {
	if s.proxy != nil {
		s.proxy.ServeHTTP(w, r)
		return
	}
	note := ""
	if s.staticDir == "" {
		note = "No directory is being served: start this with -dir to add one under /site/."
	}
	writeHTML(w, http.StatusNotFound, notFoundTmpl, note)
}

func writeHTML(w http.ResponseWriter, status int, t *template.Template, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = t.Execute(w, data)
}

func newProxy(target string, log *eventLog) (*httputil.ReverseProxy, error) {
	u, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	p := httputil.NewSingleHostReverseProxy(u)
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.add(event{Kind: "proxy", Peer: r.RemoteAddr, Detail: "upstream failed: " + err.Error()})
		writeHTML(w, http.StatusBadGateway, notFoundTmpl,
			"The upstream this host proxies to did not answer. Nothing about the phone is wrong.")
	}
	return p, nil
}

func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS10:
		return "TLS 1.0"
	case tls.VersionTLS11:
		return "TLS 1.1"
	case tls.VersionTLS12:
		return "TLS 1.2"
	case tls.VersionTLS13:
		return "TLS 1.3"
	default:
		return fmt.Sprintf("TLS 0x%04x", v)
	}
}

func isLoopbackPeer(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// describeHello turns a ClientHello into one legible line. It is logged before
// any certificate is chosen, so it fires even when the client abandons the
// handshake a moment later — which is the whole reason it exists.
func describeHello(h *tls.ClientHelloInfo) string {
	versions := make([]string, 0, len(h.SupportedVersions))
	for _, v := range h.SupportedVersions {
		versions = append(versions, tlsVersionName(v))
	}
	sni := h.ServerName
	if sni == "" {
		sni = "(none, connected by IP)"
	}
	return fmt.Sprintf("sni %s  offers %s  alpn [%s]", sni, strings.Join(versions, " "), strings.Join(h.SupportedProtos, " "))
}

// handleReport records the digest the probe page computed, so the environment
// values land in the log rather than only on a screen.
//
// Without it every figure from a device walk — viewport, colour scheme, insets,
// storage, fonts — rests on a photograph of a phone, and cropped photographs are
// not an evidence chain.
//
// The body is capped and the request is never allowed to fail the page.
func (s *server) handleReport(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	line := strings.TrimSpace(string(body))
	if line == "" {
		line = "(empty report)"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "TLS"
	}
	s.log.add(event{Kind: "report", Peer: r.RemoteAddr, Detail: scheme + "  " + line})
	w.WriteHeader(http.StatusNoContent)
}

// portalProbeLangs are the languages the portal's probe is built in, and the
// whole of what this handler will read off disk.
//
// It is a fixed list on purpose. The path carries a language prefix because the
// URL has to be one a person can type on a phone keyboard and look like every
// other portal URL — but the prefix is a *lookup key*, never a path segment, so
// there is nothing in a request that reaches outside the directory this was
// pointed at. `../../` is not a language.
var portalProbeLangs = []string{"en", "vi"}

// handlePortalProbe serves one built probe document, at `/{lang}/probe` and
// `/{lang}/probe/`.
//
// **A static file, on purpose, and never a template.** The document is
// self-contained by construction — the portal build inlines its CSS and its
// script — and re-rendering it here would mean a second implementation of a
// measurement page, which is the thing that made the first set of figures
// unusable in the first place. What this host contributes is the origin: it owns
// `/report`, so the digest the page sends lands in the log above with the peer
// address and the User-Agent beside it.
//
// A missing directory answers 404 naming the build step, not a blank page. A
// blank page in a measurement that is already about a blank page is the one
// outcome nobody can diagnose.
func (s *server) handlePortalProbe(lang string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.probeDir == "" {
			writeHTML(w, http.StatusNotFound, notFoundTmpl,
				"The portal probe is not being served. Start this with -probe web/portal/dist-probe.")
			return
		}
		body, err := os.ReadFile(filepath.Join(s.probeDir, lang, "probe", "index.html"))
		if err != nil {
			writeHTML(w, http.StatusNotFound, notFoundTmpl,
				"No probe document in "+s.probeDir+" for "+lang+
					". Build it with: pnpm -F @foyer/portal build")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// A cached probe document is a probe of whatever was measured last time.
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}
}
