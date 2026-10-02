package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// The deployment's certificate files get the checks a console upload gets
// (internal/config: ParseCertificateChain, CoversHost, CheckValidAt).
//
// Loading them with `tls.LoadX509KeyPair` and a hostname check alone, with a
// `api.NotServedReason` that looks at NotAfter and never at NotBefore, would serve a
// certificate that is not valid yet — every guest's browser refusing it while the
// console says `served` — and would load a chain out of order too. Each test below
// is red against that.

// TestANotYetValidCertificateFileIsNotServed — at start it is loaded, reported
// `not-served` with the reason, and the plain-HTTP listener stays the portal
// rather than sending every guest to a certificate error. It is served from the
// moment it is valid, with no restart.
func TestANotYetValidCertificateFileIsNotServed(t *testing.T) {
	validFrom := time.Now().Add(time.Hour)
	certPath, keyPath, _ := writePairValid(t, t.TempDir(), 1, validFrom, validFrom.Add(24*time.Hour), portalHost)
	srv, svc, _ := portalFor(t)
	rt, err := newPortalRuntime(tlsConfig(t, certPath, keyPath), srv, svc, testObservability(t))
	if err != nil {
		t.Fatalf("a pair whose only fault is the clock refused the start, which would take RADIUS down with the portal: %v", err)
	}

	if rt.served() != nil {
		t.Fatal("a certificate that is not valid for another hour is served")
	}
	st := rt.PortalCertificateStatus()
	if st.State != gen.PortalCertificateStateNotServed || st.NotServedReason == nil ||
		*st.NotServedReason != gen.PortalCertificateStatusNotServedReasonNotYetValid {
		t.Errorf("status %s / %v, want not-served / not-yet-valid", st.State, st.NotServedReason)
	}
	rec := serve(t, rt.plainListener().srv.Handler, http.MethodGet, "/guest/s/default/?id=aa:bb:cc:00:11:22", "10.20.0.5:40000")
	if loc := rec.Header().Get("Location"); strings.HasPrefix(loc, "https://") {
		t.Errorf("the plain-HTTP listener sent a guest to %q, an HTTPS origin with no servable certificate", loc)
	}

	rt.now = func() time.Time { return validFrom.Add(time.Minute) }
	if rt.served() == nil {
		t.Error("once valid, the certificate is still not served")
	}
}

// TestAReplacementThatIsNotValidYetKeepsThePreviousCertificate — a renewal must
// not displace a certificate that works with one that does not work yet.
func TestAReplacementThatIsNotValidYetKeepsThePreviousCertificate(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, _ := writePair(t, dir, 1, portalHost)
	srv, svc, _ := portalFor(t)
	rt, err := newPortalRuntime(tlsConfig(t, certPath, keyPath), srv, svc, testObservability(t))
	if err != nil {
		t.Fatal(err)
	}
	validFrom := time.Now().Add(time.Hour)
	_, _, _ = writePairValid(t, dir, 2, validFrom, validFrom.Add(24*time.Hour), portalHost)
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(certPath, future, future)
	_ = os.Chtimes(keyPath, future, future)

	c, err := rt.GetCertificate(nil)
	if err != nil {
		t.Fatalf("no certificate after a not-yet-valid replacement: %v", err)
	}
	if got := c.Leaf.SerialNumber.Int64(); got != 1 {
		t.Errorf("serial %d served, want 1: the replacement is not valid yet", got)
	}
}

// TestACertificateFileWithABrokenChainIsRefusedAtStart — the chain rule an upload
// is held to. A leaf followed by a certificate that did not sign it is what a
// renewal script concatenating the wrong intermediate produces.
func TestACertificateFileWithABrokenChainIsRefusedAtStart(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, _ := writePair(t, dir, 1, portalHost)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "unrelated CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leafPEM, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatal(err)
	}
	chained := append(leafPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	if err := os.WriteFile(certPath, chained, 0o600); err != nil {
		t.Fatal(err)
	}

	srv, svc, _ := portalFor(t)
	_, err = newPortalRuntime(tlsConfig(t, certPath, keyPath), srv, svc, testObservability(t))
	if err == nil || !strings.Contains(err.Error(), "chain-broken") {
		t.Fatalf("a certificate file whose second certificate did not sign the first was accepted (err %v)", err)
	}
	if strings.Contains(err.Error(), "PRIVATE KEY") {
		t.Errorf("the refusal carries key material: %v", err)
	}
}

// TestAReplacementIsHeldToThePortalAddressInForce — after an operator moves the
// portal to another name in the console, a renewed file for that name is the
// right certificate. Checking the file against the host FOYER_PORTAL_URL named at
// start, forever, would refuse the right certificate and keep the wrong one.
func TestAReplacementIsHeldToThePortalAddressInForce(t *testing.T) {
	dir := t.TempDir()
	certPath, keyPath, _ := writePair(t, dir, 1, portalHost)
	srv, svc, _ := portalFor(t)
	rt, err := newPortalRuntime(tlsConfig(t, certPath, keyPath), srv, svc, testObservability(t))
	if err != nil {
		t.Fatal(err)
	}
	const moved = "wifi.moved.test"
	rt.ApplyNetwork(api.EffectiveNetwork{PortalURL: "https://" + moved + "/en/"})

	_, _, _ = writePair(t, dir, 2, moved)
	future := time.Now().Add(time.Minute)
	_ = os.Chtimes(certPath, future, future)
	_ = os.Chtimes(keyPath, future, future)
	c, err := rt.GetCertificate(nil)
	if err != nil {
		t.Fatalf("no certificate is served for %s after its renewal: %v", moved, err)
	}
	if got := c.Leaf.SerialNumber.Int64(); got != 2 {
		t.Errorf("serial %d served, want 2: the replacement names the address guests are sent to now", got)
	}
}
