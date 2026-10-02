package nas

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// selfSigned makes a certificate and its key, the shape a UniFi controller ships.
func selfSigned(t *testing.T, cn string) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der, key
}

// TestUniFiPinIsCheckedAgainstTheLeafOnly: a pin matches the leaf and nothing else.
//
// The on-path attacker holds their own key and a copy of the real controller's
// certificate — which is public: every client that ever connected received it.
// They present `[attacker-leaf, real-controller-cert]`. The handshake completes,
// because the attacker really does hold the leaf's key and nothing with the
// platform verifier off checks that the second certificate signs the first. A
// pin matched against *any* certificate in that list accepts it, and the
// driver's next request carries the API key to the attacker.
//
// Driven through a real TLS handshake rather than by calling the callback, so
// the test is about what the driver does on the wire. Both the pinned mode and
// the strict half of TOFU (a fingerprint already stored) are exercised, because
// they share one implementation and the claim is about both.
func TestUniFiPinIsCheckedAgainstTheLeafOnly(t *testing.T) {
	realDER, _ := selfSigned(t, "unifi")
	attackerDER, attackerKey := selfSigned(t, "attacker")

	sum := sha256.Sum256(realDER)
	realPin := hex.EncodeToString(sum[:])
	attackerSum := sha256.Sum256(attackerDER)

	var sawKey bool
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "" {
			sawKey = true
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{{
		// The forged chain: the attacker's own leaf first, the real controller's
		// certificate second.
		Certificate: [][]byte{attackerDER, realDER},
		PrivateKey:  attackerKey,
	}}}
	ts.StartTLS()
	defer ts.Close()

	for _, mode := range []TLSMode{TLSPinnedSHA256, TLSTrustOnFirstUse} {
		t.Run(string(mode), func(t *testing.T) {
			sawKey = false
			d, err := NewUniFiDriverWithError(UniFiConfig{
				BaseURL: ts.URL, APIKey: testAPIKey, TLSMode: mode, PinnedSHA256: realPin,
				Flavour: FlavourStandalone, Timeout: 3 * time.Second,
			})
			if err != nil {
				t.Fatalf("construction: %v", err)
			}
			if _, err := d.lookupClient(context.Background(), "default", testMAC); err == nil {
				t.Fatal("a chain whose leaf is the attacker's and whose second certificate is the pinned one was accepted")
			}
			if sawKey {
				t.Fatal("the API key reached a server whose leaf does not match the pin")
			}
			// What the driver reports having seen must be the leaf — the
			// certificate an operator would be asked to trust — and never the
			// pinned one it found further down the list.
			if got, want := d.ObservedFingerprint(), hex.EncodeToString(attackerSum[:]); got != want {
				t.Fatalf("observed fingerprint = %s, want the leaf's %s", got, want)
			}
		})
	}

	// And the control: the same server presenting the real certificate as its
	// leaf (with its own key) is accepted, so the refusal above is about the
	// position of the match and not a pin that can never match.
	realOnly, realKey := selfSigned(t, "unifi")
	good := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	good.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{realOnly, attackerDER}, PrivateKey: realKey}}}
	good.StartTLS()
	defer good.Close()
	goodSum := sha256.Sum256(realOnly)
	d, err := NewUniFiDriverWithError(UniFiConfig{
		BaseURL: good.URL, APIKey: testAPIKey, TLSMode: TLSPinnedSHA256, PinnedSHA256: hex.EncodeToString(goodSum[:]),
		Flavour: FlavourStandalone, Timeout: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.lookupClient(context.Background(), "default", testMAC); err != nil {
		t.Fatalf("the pinned certificate as the leaf was refused: %v", err)
	}
}
