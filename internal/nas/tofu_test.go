package nas

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"
)

// trust-on-first-use is the only TLS mode a real controller can be registered
// with, so the driver must implement it.
//
// `web/contract/openapi.yaml` and migration `00004` both permit exactly `verify`
// and `trust-on-first-use`. Controllers ship self-signed certificates, so `verify`
// cannot succeed. A driver implementing only `verify`, `pinned-sha256` and
// `insecure-skip-verify` answers a controller registered the documented way with
// `500 unifi: unknown TLS mode "trust-on-first-use"` — every UniFi registration
// dead on arrival.
//
// The two halves below are the whole of the mode, and the second is the one that
// makes it worth having.
func TestTrustOnFirstUseLearnsThePinAndThenEnforcesIt(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	want := sha256.Sum256(srv.Certificate().Raw)
	wantHex := hex.EncodeToString(want[:])

	t.Run("first contact accepts and reports what it pinned", func(t *testing.T) {
		d, err := NewUniFiDriverWithError(UniFiConfig{
			BaseURL: srv.URL,
			TLSMode: TLSTrustOnFirstUse,
		})
		if err != nil {
			t.Fatalf("a trust-on-first-use driver must build: %v", err)
		}
		resp, err := d.httpClient.Get(srv.URL)
		if err != nil {
			t.Fatalf("first contact must succeed against a self-signed controller: %v", err)
		}
		resp.Body.Close()

		if got := d.ObservedFingerprint(); got != wantHex {
			t.Fatalf("ObservedFingerprint = %q, want %q.\n"+
				"  A caller that cannot read back what it pinned has built "+
				"insecure-skip-verify with extra steps: every contact stays a first contact.", got, wantHex)
		}
	})

	t.Run("a pinned driver refuses a certificate that is not the pin", func(t *testing.T) {
		// Pinned to a fingerprint this server cannot present.
		//
		// Not a SECOND httptest server pinned to the first:
		// `httptest.NewTLSServer` reuses ONE certificate for every server it
		// starts, so both fingerprints would be identical, the harness could not
		// express "a different certificate" at all, and the assertion could not
		// fail for the right reason. That is `testing.md`'s
		// input-that-cannot-be-wrong, in a test written to prove a security
		// property.
		var notThisOne [sha256.Size]byte
		for i := range notThisOne {
			notThisOne[i] = 0xA5
		}
		d, err := NewUniFiDriverWithError(UniFiConfig{
			BaseURL:      srv.URL,
			TLSMode:      TLSTrustOnFirstUse,
			PinnedSHA256: hex.EncodeToString(notThisOne[:]),
		})
		if err != nil {
			t.Fatalf("a pinned trust-on-first-use driver must build: %v", err)
		}
		if _, err := d.httpClient.Get(srv.URL); err == nil {
			t.Fatal("a pinned driver accepted a certificate that is not the pin — " +
				"trust-on-first-use that never enforces the pin is insecure-skip-verify " +
				"under a reassuring name")
		}
	})

	t.Run("a pinned driver still accepts the certificate it pinned", func(t *testing.T) {
		d, err := NewUniFiDriverWithError(UniFiConfig{
			BaseURL:      srv.URL,
			TLSMode:      TLSTrustOnFirstUse,
			PinnedSHA256: wantHex,
		})
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		resp, err := d.httpClient.Get(srv.URL)
		if err != nil {
			t.Fatalf("the pinned certificate must still be accepted: %v", err)
		}
		resp.Body.Close()
	})
}

// The contract's two modes must both be constructible, or a controller registered
// through the API cannot be driven at all — which is exactly what happened.
func TestEveryTLSModeTheContractPermitsIsImplemented(t *testing.T) {
	// The values `web/contract/openapi.yaml`'s TlsMode enum and migration 00004's
	// CHECK constraint both carry. Kept as literals on purpose: this test exists
	// because the driver and the contract disagreed, so deriving one from the
	// other here would remove the disagreement it is meant to detect.
	for _, mode := range []TLSMode{"verify", "trust-on-first-use"} {
		t.Run(string(mode), func(t *testing.T) {
			if _, err := buildTLSConfig(mode, "", &observedCert{}); err != nil {
				t.Fatalf("the contract permits %q and the driver refuses it: %v", mode, err)
			}
		})
	}
}

var _ = tls.VersionTLS12
var _ = x509.NewCertPool
