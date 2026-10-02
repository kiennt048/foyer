package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/config"
)

// The mTLS boundary `docs/rules/security.md` §2 requires, exercised in the
// direction that matters: a client that cannot prove it is FreeRADIUS must be
// **refused**, and the refusal must name the certificate.
//
// ── why "refused" is not enough on its own ───────────────────────────────────
//
// `docs/rules/database.md` records what a negative test is worth when its
// subject has more than one way to fail: a test asserting only that an INSERT
// was denied could not tell "the table is protected" from "the sequence saved
// us". The same trap is here and it is easier to fall into, because a TLS dial
// fails for a dozen reasons that have nothing to do with authentication — the
// listener is not up, the port moved, the handshake timed out. So:
//
//   - every refusal is matched against the alert the *server* sent, which names
//     the certificate, and a connection-level failure is reported as a different
//     thing rather than counted as a pass;
//   - the genuine client runs in the same test and must be **served**, so a
//     server that refuses everybody cannot make the refusals look right.
//
// ── and it re-derives its own evidence ───────────────────────────────────────
//
// The last subtest removes FOYER_POLICY_CLIENT_CA and requires the same two
// clients to be **served**. That is the state of a deployment with TLS and no
// client authentication, so the evidence that these assertions can fail is
// produced on every run instead of being merely claimed.
func TestThePolicyAPIRefusesAClientThatIsNotFreeRADIUS(t *testing.T) {
	pki := newTestPKI(t)

	cfg := config.Config{
		PolicyAddr:     "127.0.0.1:0",
		AdminAddr:      "127.0.0.1:0",
		PortalAddr:     "127.0.0.1:0",
		PolicyTLSCert:  pki.serverCert,
		PolicyTLSKey:   pki.serverKey,
		PolicyClientCA: pki.caCert,
	}
	addr := servePolicy(t, cfg)

	t.Run("a client presenting no certificate is refused", func(t *testing.T) {
		_, err := policyGet(addr, pki.rootPool(t), nil)
		assertRefusedForItsCertificate(t, err, "no client certificate")
	})

	t.Run("a client presenting a certificate from another CA is refused", func(t *testing.T) {
		_, err := policyGet(addr, pki.rootPool(t), pki.rogueClientPair(t))
		assertRefusedForItsCertificate(t, err, "a certificate signed by a CA this install did not issue")
	})

	// The guard that stops the two above from being satisfied by a broken
	// listener. Without this, "everything is refused" reads identically to
	// "only the wrong clients are refused".
	t.Run("the certificate this install issued is served", func(t *testing.T) {
		status, err := policyGet(addr, pki.rootPool(t), pki.clientPair(t))
		if err != nil {
			t.Fatalf("the genuine client was refused: %v — the two refusals above prove nothing", err)
		}
		if status != http.StatusOK {
			t.Errorf("the genuine client got %d, want 200", status)
		}
	})

	// ── the sabotage, re-run every time rather than recorded once ────────────
	t.Run("without a client CA the same two clients are served, which is the defect", func(t *testing.T) {
		open := cfg
		open.PolicyClientCA = ""
		openAddr := servePolicy(t, open)

		if _, err := policyGet(openAddr, pki.rootPool(t), nil); err != nil {
			t.Fatalf("a client with no certificate was refused by a listener with no client CA: %v. "+
				"That means the refusals above are coming from something other than the client CA, "+
				"and this file is not testing what it says it tests", err)
		}
		if _, err := policyGet(openAddr, pki.rootPool(t), pki.rogueClientPair(t)); err != nil {
			t.Fatalf("a foreign certificate was refused by a listener with no client CA: %v", err)
		}
	})
}

// assertRefusedForItsCertificate insists the refusal names the certificate.
//
// `tls: certificate required` and `tls: bad certificate` are alerts the *server*
// sent after looking at what the client presented. `connection refused`,
// `i/o timeout` and `EOF` are the listener being absent or unhappy, and a test
// that accepted those would stay green with mTLS removed and the port closed.
func assertRefusedForItsCertificate(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("the Policy API served a client presenting %s", what)
	}
	msg := err.Error()
	for _, alert := range []string{
		"tls: certificate required",
		"tls: bad certificate",
		"tls: unknown certificate authority",
		"tls: unknown certificate",
	} {
		if strings.Contains(msg, alert) {
			t.Logf("refused %s: %v", what, err)
			return
		}
	}
	t.Fatalf("the dial for %s failed with %q, which does not name the certificate. "+
		"A refusal that could equally be a closed port is not evidence about mTLS", what, err)
}

// servePolicy binds the real policy listener on an ephemeral port and returns
// its address. It goes through policyListener and policyTLS rather than building
// a tls.Config here: the thing under test is what cmd/foyer configures, and a
// second copy of it in a test is a second source of truth for the guarantee.
func servePolicy(t *testing.T, cfg config.Config) string {
	t.Helper()
	s := policyListener(cfg, nil, testObservability(t))
	if s.tls == nil {
		t.Fatal("policyListener produced no TLS configuration for a certificate and key")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.srv.ServeTLS(ln, "", "") }()
	t.Cleanup(func() { _ = s.srv.Close() })
	return ln.Addr().String()
}

// policyGet dials the Policy API as a client with, or without, a certificate.
//
// ── the harness defect this signature exists to avoid ────────────────────────
//
// With `Certificates` set, the foreign-CA case passes with `tls: certificate
// required` — the same alert as the no-certificate case, and the server logs
// "client didn't provide a certificate". **Go's client filters its own
// certificate out**, because a CertificateRequest carries the server's acceptable
// CA list and `Certificates` is matched against it. So the test would measure the
// Go client's politeness, not the server's refusal, and would stay green with
// `ClientAuth` set to `RequestClientCert`, which asks for a certificate and
// accepts any answer including none.
//
// `GetClientCertificate` is not filtered. The certificate goes on the wire, the
// server verifies it against its own CA, and the alert that comes back is about
// what it found. That is the difference between "the client declined to try" and
// "the server said no".
func policyGet(addr string, roots *x509.CertPool, cert *tls.Certificate) (int, error) {
	tlsCfg := &tls.Config{RootCAs: roots, ServerName: "foyer", MinVersion: tls.VersionTLS12}
	if cert != nil {
		tlsCfg.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
			return cert, nil
		}
	}
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   10 * time.Second,
	}
	resp, err := client.Get("https://" + addr + "/healthz")
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// ── a two-CA PKI, in files, because that is what the process reads ───────────
//
// `deploy/first-boot.sh` generates a per-install CA and hands paths to the
// process. The second CA here stands for every certificate in the world that is
// not this install's — an attacker's, another deployment's, a public one.

type testPKI struct {
	dir        string
	caCert     string
	serverCert string
	serverKey  string
	clientCert string
	clientKey  string
	rogueCert  string
	rogueKey   string
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	dir := t.TempDir()
	p := &testPKI{
		dir:        dir,
		caCert:     filepath.Join(dir, "ca.crt"),
		serverCert: filepath.Join(dir, "policy.crt"),
		serverKey:  filepath.Join(dir, "policy.key"),
		clientCert: filepath.Join(dir, "radius.crt"),
		clientKey:  filepath.Join(dir, "radius.key"),
		rogueCert:  filepath.Join(dir, "rogue.crt"),
		rogueKey:   filepath.Join(dir, "rogue.key"),
	}

	caCert, caKey := mustCA(t, "Foyer test policy CA")
	writePEM(t, p.caCert, "CERTIFICATE", caCert.Raw)

	issue(t, caCert, caKey, "foyer", []string{"foyer"}, x509.ExtKeyUsageServerAuth, p.serverCert, p.serverKey)
	issue(t, caCert, caKey, "freeradius", nil, x509.ExtKeyUsageClientAuth, p.clientCert, p.clientKey)

	rogueCert, rogueKey := mustCA(t, "Somebody else's CA")
	issue(t, rogueCert, rogueKey, "freeradius", nil, x509.ExtKeyUsageClientAuth, p.rogueCert, p.rogueKey)

	return p
}

func (p *testPKI) rootPool(t *testing.T) *x509.CertPool {
	t.Helper()
	pemBytes, err := os.ReadFile(p.caCert)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("the test CA is not a certificate")
	}
	return pool
}

func (p *testPKI) clientPair(t *testing.T) *tls.Certificate {
	return loadPair(t, p.clientCert, p.clientKey)
}

func (p *testPKI) rogueClientPair(t *testing.T) *tls.Certificate {
	return loadPair(t, p.rogueCert, p.rogueKey)
}

func loadPair(t *testing.T, certPath, keyPath string) *tls.Certificate {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	return &pair
}

func mustCA(t *testing.T, cn string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial(t),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key
}

func issue(t *testing.T, ca *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, dnsNames []string, eku x509.ExtKeyUsage, certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(t),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, certPath, "CERTIFICATE", der)

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	writePEM(t, keyPath, "EC PRIVATE KEY", keyDER)
}

func writePEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: der}); err != nil {
		t.Fatal(err)
	}
}

func serial(t *testing.T) *big.Int {
	t.Helper()
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatal(err)
	}
	return n
}
