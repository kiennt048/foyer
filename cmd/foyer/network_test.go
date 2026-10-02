package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/portal"
)

// The portal listeners following the console: the certificate and the portal's
// address are set in the console and take effect without a restart, rather than
// the listeners being built once from the environment, with the HTTPS one existing
// only if a file did.

// consolePair loads a generated pair the way ReloadNetwork does.
func consolePair(t *testing.T, names ...string) (*api.PortalPair, *x509.Certificate) {
	t.Helper()
	certPath, keyPath, leaf := writePair(t, t.TempDir(), time.Now().UnixNano(), names...)
	c, _ := os.ReadFile(certPath)
	k, _ := os.ReadFile(keyPath)
	pair, info, err := config.ParseCertificatePair(c, k, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return &api.PortalPair{Certificate: pair, Info: info, UpdatedAt: time.Now(), UpdatedBy: "ada"}, leaf
}

// TestACertificateUploadedInTheConsoleStartsTheHTTPSPortalWithoutARestart — a
// portal that booted on plain HTTP with no certificate file: the first pair the
// console hands it binds the HTTPS listener, the plain listener starts sending
// guests there, and the RFC 8908 answer names the new address. Removing it puts
// the plain listener back to being the portal.
func TestACertificateUploadedInTheConsoleStartsTheHTTPSPortalWithoutARestart(t *testing.T) {
	srv, svc, _ := portalFor(t)
	cfg := testConfig()
	cfg.PortalTLSAddr = "127.0.0.1:0"
	rt, err := newPortalRuntime(cfg, srv, svc, testObservability(t))
	if err != nil {
		t.Fatal(err)
	}
	plain := rt.plainListener().srv.Handler
	if rt.startTLSAtBoot() != nil {
		t.Fatal("an HTTPS listener was built with nothing to serve on it")
	}
	redirect := "/guest/s/default/?id=aa:bb:cc:00:11:22&ap=11:22:33:44:55:66"
	if rec := serve(t, plain, http.MethodGet, redirect, "10.20.0.5:40000"); rec.Code != http.StatusFound || rec.Header().Get("Location")[:4] != "/en/" {
		t.Fatalf("precondition: the plain listener is the portal, got %d %q", rec.Code, rec.Header().Get("Location"))
	}

	pair, leaf := consolePair(t, portalHost)
	rt.ApplyNetwork(api.EffectiveNetwork{PortalURL: "https://" + portalHost + "/en", VenueURL: "https://" + portalHost + "/about", PortalCertificate: pair})
	t.Cleanup(func() {
		if l := rt.lateTLS(); l != nil {
			_ = l.srv.Close()
		}
	})

	if rec := serve(t, plain, http.MethodGet, redirect, "10.20.0.5:40001"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://"+portalHost+redirect {
		t.Errorf("with the console's certificate the plain listener answered %d %q, want the HTTPS origin", rec.Code, rec.Header().Get("Location"))
	}
	late := rt.lateTLS()
	if late == nil {
		t.Fatal("no HTTPS listener was bound after the console supplied a certificate")
	}

	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, late.srv.Addr)
		},
		DisableKeepAlives: true,
	}}
	var res *http.Response
	for range 50 { // the listener is serving in a goroutine
		if res, err = client.Get("https://" + portalHost + "/api/v1/captive-portal"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the HTTPS portal did not answer on the console's certificate: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	var data portal.CapportData
	if err := json.Unmarshal(body, &data); err != nil || data.UserPortalURL != "https://"+portalHost+"/en" || data.VenueInfoURL != "https://"+portalHost+"/about" {
		t.Errorf("RFC 8908 over the new listener: %v %s", err, body)
	}
	if st := rt.PortalCertificateStatus(); st.State != gen.PortalCertificateStateServed || st.Source != gen.PortalCertificateStatusSourceConsole {
		t.Errorf("status: %+v", st)
	}

	// The address moved off the certificate: not served, and the plain listener is
	// the portal again rather than a redirect to a certificate error.
	rt.ApplyNetwork(api.EffectiveNetwork{PortalURL: "https://elsewhere.test/en", PortalCertificate: pair})
	if st := rt.PortalCertificateStatus(); st.State != gen.PortalCertificateStateNotServed ||
		st.NotServedReason == nil || *st.NotServedReason != gen.PortalCertificateStatusNotServedReasonNameNotCovered {
		t.Errorf("a certificate that no longer names the portal: %+v", st)
	}
	if rec := serve(t, plain, http.MethodGet, redirect, "10.20.0.5:40002"); rec.Code != http.StatusFound || rec.Header().Get("Location")[:4] != "/en/" {
		t.Errorf("with nothing servable the plain listener answered %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// TestTheGeneratorSettingsSavedInTheConsoleReachTheTree — the worker count and
// EAP-TLS with its material, which were `foyerctl generate-raddb --max-servers` and
// `apply-raddb --eap-cert-dir`, written from what the console stored; and turning
// EAP-TLS off takes the key back out of the tree.
func TestTheGeneratorSettingsSavedInTheConsoleReachTheTree(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		DatabaseURL:           "postgres://foyer_app:pw@db.internal:5432/foyer",
		RaddbPostgresPassword: "freeradius-own-generated-password",
		PolicyAddr:            "127.0.0.1:8080",
		RaddbTarget:           dir,
	}
	certPath, keyPath, _ := writePair(t, t.TempDir(), 7, "radius.venue.test")
	c, _ := os.ReadFile(certPath)
	k, _ := os.ReadFile(keyPath)
	on := apistore.RadiusGeneratorSettings{MaxServers: 64, EAP: &apistore.EAPMaterial{CertificatePEM: c, PrivateKeyPEM: k, CAPEM: c}}
	if _, err := generateRaddbInto(cfg, on, dir); err != nil {
		t.Fatal(err)
	}
	if conf := readTree(t, dir, "radiusd.conf"); !strings.Contains(conf, "max_servers = 64") {
		t.Errorf("radiusd.conf does not carry the console's worker count")
	}
	if eap := readTree(t, dir, "mods-available", "eap"); !strings.Contains(eap, "${certdir}/foyer-eap/server.key") {
		t.Errorf("the eap module does not point at the written key: %s", eap)
	}
	if got, _ := os.ReadFile(dir + "/certs/foyer-eap/server.key"); string(got) != string(k) {
		t.Error("the EAP key in the tree is not the stored one")
	}

	if _, err := generateRaddbInto(cfg, apistore.RadiusGeneratorSettings{MaxServers: 4}, dir); err != nil {
		t.Fatalf("four workers, EAP off: %v", err)
	}
	if _, err := os.Stat(dir + "/certs/foyer-eap"); !os.IsNotExist(err) {
		t.Errorf("EAP-TLS off left the key directory in the tree: %v", err)
	}
}
