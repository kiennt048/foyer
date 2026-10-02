package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/portal"
)

// The optional HTTPS portal.
//
// **No certificate is committed.** Every pair here is generated at run time into
// `t.TempDir()` and gone when the test ends — `docs/rules/security.md` §7 is
// about images, and a test fixture key in git is a key in every clone forever.

const portalHost = "portal.test"

// writePair writes a fresh self-signed certificate for the given names, valid
// from an hour ago for a day.
func writePair(t *testing.T, dir string, serial int64, names ...string) (certPath, keyPath string, cert *x509.Certificate) {
	t.Helper()
	return writePairValid(t, dir, serial, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour), names...)
}

// writePairValid is writePair with the validity window given.
func writePairValid(t *testing.T, dir string, serial int64, notBefore, notAfter time.Time, names ...string) (certPath, keyPath string, cert *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(serial),
		Subject:      pkix.Name{CommonName: names[0]},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPath, keyPath = filepath.Join(dir, "portal.crt"), filepath.Join(dir, "portal.key")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	parsed, _ := x509.ParseCertificate(der)
	return certPath, keyPath, parsed
}

func tlsConfig(t *testing.T, certPath, keyPath string) config.Config {
	t.Helper()
	cfg := testConfig()
	cfg.PortalURL = "https://" + portalHost + "/en/"
	cfg.PortalTLSAddr = "127.0.0.1:0"
	cfg.PortalTLSCert = certPath
	cfg.PortalTLSKey = keyPath
	return cfg
}

// startTLS serves the listener on loopback and returns a client that trusts only
// the given certificate and dials it whatever host a URL names. A nil trust skips
// verification, for the one test that asks *which* certificate was presented
// rather than whether it verifies — `VerifyConnection` is not reached after a
// failed verification.
func startTLS(t *testing.T, l *namedServer, trust *x509.Certificate) (*http.Client, func() *x509.Certificate) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = l.srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = l.srv.Close() })

	pool := x509.NewCertPool()
	if trust != nil {
		pool.AddCert(trust)
	}
	var seen atomic.Pointer[x509.Certificate]
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				RootCAs:            pool,
				InsecureSkipVerify: trust == nil, //nolint:gosec // see above: this client observes, it does not trust
				VerifyConnection: func(cs tls.ConnectionState) error {
					seen.Store(cs.PeerCertificates[0])
					return nil
				},
			},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
			},
			DisableKeepAlives: true,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return client, seen.Load
}

func portalFor(t *testing.T) (*api.Server, *portal.Service, *portal.MemoryStore) {
	t.Helper()
	ports, mem := captivePorts()
	srv, err := api.New(api.Config{Portal: ports})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	svc, err := portal.NewService(portal.ServiceConfig{Ports: ports})
	if err != nil {
		t.Fatalf("portal.NewService: %v", err)
	}
	return srv, svc, mem
}

// TestThePortalServesHTTPSWhenACertificateIsConfigured — the whole portal, over
// TLS, on the operator's certificate: the RFC 8908 endpoint RFC 8908 §4 says must
// be https, and the front door, whose session cookie is then `Secure`. And never
// `Strict-Transport-Security` (see portaltls.go for the field's reason).
func TestThePortalServesHTTPSWhenACertificateIsConfigured(t *testing.T) {
	certPath, keyPath, cert := writePair(t, t.TempDir(), 1, portalHost)
	cfg := tlsConfig(t, certPath, keyPath)
	srv, svc, _ := portalFor(t)

	l, err := portalTLSListener(cfg, srv, svc, testObservability(t))
	if err != nil {
		t.Fatalf("portalTLSListener: %v", err)
	}
	if l == nil {
		t.Fatal("no HTTPS portal listener was built although a certificate is configured")
	}
	client, _ := startTLS(t, l, cert)

	res, err := client.Get("https://" + portalHost + "/api/v1/captive-portal")
	if err != nil {
		t.Fatalf("GET the RFC 8908 endpoint over TLS: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != portal.MediaTypeCaptiveJSON {
		t.Fatalf("RFC 8908 over TLS answered %d %q", res.StatusCode, res.Header.Get("Content-Type"))
	}
	var data portal.CapportData
	if err := json.Unmarshal(body, &data); err != nil || data.UserPortalURL != cfg.PortalURL {
		t.Errorf("RFC 8908 over TLS: %v, user-portal-url %q, want %q", err, data.UserPortalURL, cfg.PortalURL)
	}
	if !strings.HasPrefix(data.UserPortalURL, "https://") {
		t.Errorf("user-portal-url %q is not https; RFC 8908 requires it to be", data.UserPortalURL)
	}

	front, err := client.Get("https://" + portalHost + "/guest/s/default/?id=aa:bb:cc:00:11:22&ap=11:22:33:44:55:66")
	if err != nil {
		t.Fatalf("GET the front door over TLS: %v", err)
	}
	_ = front.Body.Close()
	if front.StatusCode != http.StatusFound {
		t.Errorf("the front door over TLS answered %d, want 302", front.StatusCode)
	}
	var secure bool
	for _, c := range front.Cookies() {
		if c.Name == portal.SessionCookieName {
			secure = c.Secure
		}
	}
	if !secure {
		t.Errorf("the portal session cookie set over TLS is not Secure")
	}

	for _, r := range []*http.Response{res, front} {
		if h := r.Header.Get("Strict-Transport-Security"); h != "" {
			t.Errorf("%s sent Strict-Transport-Security %q: on a captive portal it turns a lapsed or removed certificate into a hard failure on every phone that visited", r.Request.URL.Path, h)
		}
	}
}

// TestTheHTTPPortalSendsGuestsToHTTPSOnceACertificateIsConfigured — the plain-HTTP
// listener stays up, because it is where a NAS redirect lands, and relays every
// GET to the HTTPS origin with the NAS parameters intact. It mints nothing
// itself, and it grants nothing on a plaintext submission.
func TestTheHTTPPortalSendsGuestsToHTTPSOnceACertificateIsConfigured(t *testing.T) {
	certPath, keyPath, _ := writePair(t, t.TempDir(), 1, portalHost)
	cfg := tlsConfig(t, certPath, keyPath)
	srv, svc, mem := portalFor(t)
	h := portalListener(cfg, srv, svc, testObservability(t)).srv.Handler

	redirect := "/guest/s/default/?ap=11:22:33:44:55:66&id=aa:bb:cc:00:11:22&t=1&ssid=Cafe"
	rec := serve(t, h, http.MethodGet, redirect, "10.20.0.5:40000")
	if rec.Code != http.StatusFound {
		t.Fatalf("GET the UniFi redirect over HTTP answered %d, want 302 to HTTPS", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://"+portalHost+redirect {
		t.Errorf("redirected to %q, want the same path and query on https://%s", loc, portalHost)
	}
	if n := len(mem.Sessions()); n != 0 {
		t.Errorf("the plain-HTTP listener minted %d sessions; the cookie belongs to the HTTPS origin", n)
	}

	post := serve(t, h, http.MethodPost, api.BasePath+"/portal/voucher", "10.20.0.5:40001")
	if post.Code != http.StatusForbidden {
		t.Errorf("a plaintext POST answered %d, want 403 while the portal is configured for HTTPS", post.Code)
	}
	if n := len(mem.Grants()); n != 0 {
		t.Errorf("%d grants from a plaintext submission", n)
	}

	if hz := serve(t, h, http.MethodGet, "/healthz", "127.0.0.1:1"); hz.Code != http.StatusOK {
		t.Errorf("/healthz over HTTP answered %d", hz.Code)
	}

	// Without a certificate the HTTP listener is the portal itself, as before.
	plain := serve(t, portalListener(testConfig(), srv, svc, testObservability(t)).srv.Handler, http.MethodGet, redirect, "10.20.0.6:40000")
	if plain.Code != http.StatusFound || !strings.HasPrefix(plain.Header().Get("Location"), "/en/") {
		t.Errorf("with no certificate, the HTTP front door answered %d %q, want its own 302 to /en/", plain.Code, plain.Header().Get("Location"))
	}
}

// TestAPortalCertificateForAnotherNameIsRefused — the failure the console's
// FOYER_ADMIN_TLS_NAMES history is about: valid, correctly chained, and refused
// by every guest's browser on the name, with nothing in the process knowing.
func TestAPortalCertificateForAnotherNameIsRefused(t *testing.T) {
	certPath, keyPath, _ := writePair(t, t.TempDir(), 1, "other.test")
	srv, svc, _ := portalFor(t)
	_, err := portalTLSListener(tlsConfig(t, certPath, keyPath), srv, svc, testObservability(t))
	if err == nil || !strings.Contains(err.Error(), portalHost) {
		t.Fatalf("a certificate for other.test was accepted for %s (err %v)", portalHost, err)
	}

	// An address, which is what a portal reached by IP uses: the CNA connected by IP with
	// no SNI, and an IP SAN is what works there.
	ipCert, ipKey, _ := writePair(t, t.TempDir(), 2, "192.0.2.1")
	cfg := tlsConfig(t, ipCert, ipKey)
	cfg.PortalURL = "https://192.0.2.1:8843/en/"
	if _, err := portalTLSListener(cfg, srv, svc, testObservability(t)); err != nil {
		t.Errorf("a certificate with the portal's IP SAN was refused: %v", err)
	}
}

// TestAReplacedPortalCertificateIsServedWithoutARestart — ACME renews outside the
// process and nothing in deploy/ ever restarts it. A broken replacement keeps the
// previous certificate rather than taking the portal down.
func TestAReplacedPortalCertificateIsServedWithoutARestart(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, _ := writePair(t, dir, 1, portalHost)
	srv, svc, _ := portalFor(t)
	l, err := portalTLSListener(tlsConfig(t, certPath, keyPath), srv, svc, testObservability(t))
	if err != nil {
		t.Fatal(err)
	}
	client, seen := startTLS(t, l, nil)
	get := func() {
		t.Helper()
		res, err := client.Get("https://" + portalHost + "/healthz")
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
	}
	get()
	if seen() == nil || seen().SerialNumber.Int64() != 1 {
		t.Fatalf("precondition: serial %v served, want 1", seen())
	}

	_, _, _ = writePair(t, dir, 2, portalHost)
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(certPath, future, future)
	_ = os.Chtimes(keyPath, future, future)
	get()
	if got := seen().SerialNumber.Int64(); got != 2 {
		t.Errorf("after replacing the files, serial %d is served, want 2 without a restart", got)
	}

	if err := os.WriteFile(certPath, []byte("half a renewal"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := future.Add(time.Minute)
	_ = os.Chtimes(certPath, later, later)
	get()
	if got := seen().SerialNumber.Int64(); got != 2 {
		t.Errorf("a replacement that does not load took the certificate from serial 2 to %d", got)
	}
}
