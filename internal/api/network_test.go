package api_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// The `network` tag at the HTTP boundary: one test per setting, each proving that
// an administrator's write **takes effect in the running server** — not merely
// that it was stored — and that no private key ever comes back. The store is a
// fake here; the rows are proven against PostgreSQL in internal/store and
// internal/apistore.

// ── the fake store ───────────────────────────────────────────────────────────

type fakeNetwork struct {
	mu       sync.Mutex
	stored   api.StoredNetwork
	portal   *api.StoredCertificate
	eap      *api.StoredCertificate
	syncs    []gen.RadiusSyncTrigger
	applied  int
	lastSync *gen.RadiusClientSync
	lastApp  *gen.RadiusApply
	coa      map[string]*gen.CoaTimingValues
	noKEK    bool
}

func newFakeNetwork() *fakeNetwork {
	return &fakeNetwork{coa: map[string]*gen.CoaTimingValues{"nas-coa": nil}}
}

func (f *fakeNetwork) StoredNetwork(context.Context) (api.StoredNetwork, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stored, nil
}

func (f *fakeNetwork) SaveTransport(_ context.Context, in api.StoredTransport, actor string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, c, now := in.Throttles, in.BodyCeilings, time.Now()
	f.stored.PortalURL, f.stored.VenueURL = in.PortalURL, in.VenueURL
	f.stored.Throttles, f.stored.BodyCeilings = &t, &c
	f.stored.UpdatedAt, f.stored.UpdatedBy = &now, &actor
	return nil
}

func (f *fakeNetwork) PortalCertificate(context.Context) (api.StoredCertificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.portal == nil {
		return api.StoredCertificate{}, api.ErrNotFound
	}
	return *f.portal, nil
}

func (f *fakeNetwork) PutPortalCertificate(_ context.Context, cert string, key []byte, u *string, actor string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.noKEK {
		return api.ErrKeyringAbsent
	}
	f.portal = &api.StoredCertificate{CertificatePEM: cert, KeyPEM: key, UpdatedAt: time.Now(), UpdatedBy: actor}
	if u != nil {
		f.stored.PortalURL = u
	}
	return nil
}

func (f *fakeNetwork) DeletePortalCertificate(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.portal = nil
	return nil
}

func (f *fakeNetwork) SaveRadius(_ context.Context, maxServers int, eap bool, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	f.stored.RadiusMaxServers, f.stored.EapTLSEnabled, f.stored.RadiusChangedAt = &maxServers, eap, &now
	return nil
}

func (f *fakeNetwork) EapCertificate(context.Context) (api.StoredCertificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.eap == nil {
		return api.StoredCertificate{}, api.ErrNotFound
	}
	return api.StoredCertificate{CertificatePEM: f.eap.CertificatePEM, CAPEM: f.eap.CAPEM, UpdatedAt: f.eap.UpdatedAt}, nil
}

func (f *fakeNetwork) PutEapCertificate(_ context.Context, cert string, key []byte, ca, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	f.eap = &api.StoredCertificate{CertificatePEM: cert, KeyPEM: key, CAPEM: ca, UpdatedAt: now}
	f.stored.RadiusChangedAt = &now
	return nil
}

func (f *fakeNetwork) SyncRadiusClients(_ context.Context, trigger gen.RadiusSyncTrigger, _ string) (gen.RadiusClientSync, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncs = append(f.syncs, trigger)
	rec := gen.RadiusClientSync{At: time.Now(), Trigger: trigger, Outcome: gen.RadiusClientSyncOutcomeSynced,
		Removed: []string{}, Skipped: []gen.RadiusClientSkip{}, Reasons: []string{}, Inserted: 1}
	f.lastSync = &rec
	return rec, nil
}

func (f *fakeNetwork) ApplyRadius(context.Context, string) (gen.RadiusApply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied++
	rec := gen.RadiusApply{At: time.Now().Add(time.Second), Outcome: gen.RadiusApplyOutcomeApplied, Restarted: ptrTo(true)}
	f.lastApp = &rec
	return rec, nil
}

func (f *fakeNetwork) ApplyAvailable() bool { return true }

func (f *fakeNetwork) LastRadiusOperations(context.Context) (*gen.RadiusClientSync, *gen.RadiusApply, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastSync, f.lastApp, nil
}

func (f *fakeNetwork) CoaTimings(context.Context) ([]api.CoaTimingRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []api.CoaTimingRow{{NasID: "nas-coa", SiteID: testSite, Name: "Core", Driver: gen.NasDriverKindMikrotik, Stored: f.coa["nas-coa"]}}, nil
}

func (f *fakeNetwork) SetCoaTiming(_ context.Context, nasID string, t *gen.CoaTimingValues) (api.CoaTimingRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if nasID == "nas-1" {
		return api.CoaTimingRow{}, api.ErrNoCoA
	}
	if _, ok := f.coa[nasID]; !ok {
		return api.CoaTimingRow{}, api.ErrNotFound
	}
	f.coa[nasID] = t
	return api.CoaTimingRow{NasID: nasID, SiteID: testSite, Name: "Core", Driver: gen.NasDriverKindMikrotik, Stored: t}, nil
}

// recordingRuntime is the portal listeners' side, as far as this package can see.
type recordingRuntime struct {
	mu   sync.Mutex
	last api.EffectiveNetwork
	n    int
}

func (r *recordingRuntime) ApplyNetwork(e api.EffectiveNetwork) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last, r.n = e, r.n+1
}

func (r *recordingRuntime) PortalCertificateStatus() gen.PortalCertificateStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return api.DescribePortalPair(r.last.PortalCertificate, r.last.PortalURL, time.Now())
}

type netRig struct {
	*rig
	srv *api.Server
	net *fakeNetwork
	rt  *recordingRuntime
}

func newNetRig(t *testing.T) *netRig {
	t.Helper()
	store := world(t)
	ports := store.Ports()
	fake := newFakeNetwork()
	ports.Network = fake
	rt := &recordingRuntime{}
	srv, err := api.New(api.Config{
		Ports:            ports,
		NetworkBootstrap: api.NetworkBootstrap{PortalURL: "http://10.0.0.2:8081/en/", PortalURLSource: gen.NetworkSettingSourceDerived},
		Network:          rt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.ReloadNetwork(context.Background()); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return &netRig{rig: &rig{t: t, server: ts, store: store}, srv: srv, net: fake, rt: rt}
}

func (r *netRig) settings() gen.NetworkSettings {
	r.t.Helper()
	resp := r.do(http.MethodGet, "/network/settings", nil)
	body := readAll(r.t, resp)
	if resp.StatusCode != http.StatusOK {
		r.t.Fatalf("getNetworkSettings: %d %s", resp.StatusCode, body)
	}
	var s gen.NetworkSettings
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		r.t.Fatal(err)
	}
	return s
}

// put sends an updateNetworkSettings built from the settings in force, changed.
func (r *netRig) put(change func(*gen.NetworkSettingsUpdate)) (int, string) {
	r.t.Helper()
	s := r.settings()
	in := gen.NetworkSettingsUpdate{Throttles: s.Throttles, BodyCeilings: s.BodyCeilings}
	if s.PortalUrl.Source == gen.NetworkSettingSourceConsole {
		in.PortalUrl = s.PortalUrl.Value
	}
	change(&in)
	resp := r.do(http.MethodPut, "/network/settings", in)
	return resp.StatusCode, readAll(r.t, resp)
}

func faultOf(t *testing.T, body, field string) (gen.FieldFault, map[string]any) {
	t.Helper()
	var p struct {
		Params struct {
			Fields      map[string]gen.FieldFault `json:"fields"`
			FieldParams map[string]map[string]any `json:"fieldParams"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return p.Params.Fields[field], p.Params.FieldParams[field]
}

// ── the throttles ────────────────────────────────────────────────────────────

// TestAThrottleSetInTheConsoleBindsOnTheNextSignIn — a venue raising or lowering a
// sign-in bucket must not need a restart. Lowered to one attempt, the second
// failed sign-in from the same address is refused at once; at the shipped ten it
// would not be. The admin's own sign-in spent a token from the same address first,
// which the reshape keeps rather than refunding.
func TestAThrottleSetInTheConsoleBindsOnTheNextSignIn(t *testing.T) {
	r := newNetRig(t)
	r.signIn(adminUser)
	code, body := r.put(func(in *gen.NetworkSettingsUpdate) {
		in.Throttles.ConsoleAddress = gen.SignInThrottle{Burst: 1, IntervalMs: 600_000}
	})
	if code != http.StatusOK {
		t.Fatalf("updateNetworkSettings: %d %s", code, body)
	}
	if got := r.settings().Throttles.ConsoleAddress; got.Burst != 1 || got.IntervalMs != 600_000 {
		t.Fatalf("the throttle in force is %+v", got)
	}

	anon := &rig{t: t, server: r.server}
	statuses := []int{}
	for range 2 {
		resp := anon.do(http.MethodPost, "/auth/session", map[string]string{"username": "nobody", "password": "wrong-password"})
		_ = readAll(t, resp)
		statuses = append(statuses, resp.StatusCode)
	}
	if statuses[1] != http.StatusTooManyRequests {
		t.Fatalf("second attempt under a one-attempt bucket answered %v, want 429 second", statuses)
	}

	// Bounded: no value opens the form to unlimited guessing.
	code, body = r.put(func(in *gen.NetworkSettingsUpdate) { in.Throttles.PortalAddress.Burst = 0 })
	if f, p := faultOf(t, body, "throttles.portalAddress.burst"); code != http.StatusUnprocessableEntity || f != gen.FieldFaultBelowMinimum || p["minimumCount"] != float64(1) {
		t.Errorf("a zero burst: %d %s", code, body)
	}
	if e := auditOf(t, r.rig, gen.AuditKindNetworkSettingsChanged); len(e) != 1 {
		t.Errorf("%d network-settings-changed entries, want the one accepted write", len(e))
	}
}

// ── the ceilings ─────────────────────────────────────────────────────────────

// TestABodyCeilingSetInTheConsoleBindsOnTheNextRequest — the anonymous ceiling,
// lowered, refuses a body the shipped 16 KiB admitted, on the very next request.
func TestABodyCeilingSetInTheConsoleBindsOnTheNextRequest(t *testing.T) {
	r := newNetRig(t)
	r.signIn(adminUser)
	body := bytes.Repeat([]byte("a"), 12<<10)
	send := func() int {
		req, _ := http.NewRequest(http.MethodPost, r.server.URL+api.BasePath+"/auth/session", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := r.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = readAll(t, resp)
		return resp.StatusCode
	}
	if got := send(); got == http.StatusRequestEntityTooLarge {
		t.Fatalf("precondition: 12 KiB was already refused at the shipped ceiling")
	}
	code, out := r.put(func(in *gen.NetworkSettingsUpdate) { in.BodyCeilings.AnonymousBytes = 8 << 10 })
	if code != http.StatusOK {
		t.Fatalf("updateNetworkSettings: %d %s", code, out)
	}
	if got := send(); got != http.StatusRequestEntityTooLarge {
		t.Errorf("after lowering the anonymous ceiling to 8 KiB a 12 KiB body answered %d, want 413", got)
	}
	if got := r.srv.PortalListenerBodyLimit(); got != api.PortalBodyLimit {
		t.Errorf("the portal ceiling moved with the anonymous one: %d", got)
	}
}

// TestTheHeaderCeilingSetInTheConsoleBinds — enforced per request, below the
// listener's fixed outer bound.
func TestTheHeaderCeilingSetInTheConsoleBinds(t *testing.T) {
	r := newNetRig(t)
	r.signIn(adminUser)
	h := r.srv.LimitHeaders(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	send := func() int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Big", strings.Repeat("b", 20<<10))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if got := send(); got != http.StatusNoContent {
		t.Fatalf("precondition: 20 KiB of headers under the shipped 64 KiB answered %d", got)
	}
	if code, out := r.put(func(in *gen.NetworkSettingsUpdate) { in.BodyCeilings.HeaderBytes = 16 << 10 }); code != http.StatusOK {
		t.Fatalf("updateNetworkSettings: %d %s", code, out)
	}
	if got := send(); got != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("after lowering the header ceiling to 16 KiB, 20 KiB of headers answered %d, want 431", got)
	}
	if code, out := r.put(func(in *gen.NetworkSettingsUpdate) { in.BodyCeilings.HeaderBytes = api.MaxHeaderCeiling + 1 }); code != http.StatusUnprocessableEntity {
		t.Errorf("a header ceiling above what the listener is built with: %d %s", code, out)
	}
}

// ── the portal's address ─────────────────────────────────────────────────────

// TestThePortalAddressSetInTheConsoleIsInForce — the value the RFC 8908 answer and
// the plain-HTTP redirect read is the one just saved, and the deployment's own
// comes back when it is cleared.
func TestThePortalAddressSetInTheConsoleIsInForce(t *testing.T) {
	r := newNetRig(t)
	r.signIn(adminUser)
	if s := r.settings(); s.PortalUrl.Source != gen.NetworkSettingSourceDerived {
		t.Fatalf("before any write the portal address is %+v", s.PortalUrl)
	}
	code, body := r.put(func(in *gen.NetworkSettingsUpdate) {
		in.PortalUrl = ptrTo("http://wifi.venue.test/en/")
		in.VenueUrl = ptrTo("http://venue.test/about")
	})
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if got := r.srv.PortalURL(); got != "http://wifi.venue.test/en" {
		t.Errorf("the server's portal address is %q", got)
	}
	if r.rt.last.PortalURL != "http://wifi.venue.test/en/" || r.rt.last.VenueURL != "http://venue.test/about" {
		t.Errorf("the portal listeners were told %+v", r.rt.last)
	}
	if s := r.settings(); s.PortalUrl.Source != gen.NetworkSettingSourceConsole || s.VenueUrl.Source != gen.NetworkSettingSourceConsole {
		t.Errorf("sources after the write: %s / %s", s.PortalUrl.Source, s.VenueUrl.Source)
	}

	code, body = r.put(func(in *gen.NetworkSettingsUpdate) { in.PortalUrl = ptrTo("javascript:alert(1)") })
	if f, _ := faultOf(t, body, "portalUrl"); code != http.StatusUnprocessableEntity || f != gen.FieldFaultNotAWebAddress {
		t.Errorf("a javascript: portal address: %d %s", code, body)
	}

	if code, body := r.put(func(in *gen.NetworkSettingsUpdate) { in.PortalUrl = nil }); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	if got := r.srv.PortalURL(); got != "http://10.0.0.2:8081/en" {
		t.Errorf("clearing the console's value left %q, want the deployment's own", got)
	}
}

// ── the portal certificate ───────────────────────────────────────────────────

type pemPair struct{ cert, key string }

func selfSigned(t *testing.T, notAfter time.Time, names ...string) pemPair {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    notAfter.Add(-100 * 24 * time.Hour),
		NotAfter:     notAfter,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	return pemPair{
		cert: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		key:  string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})),
	}
}

// TestAPortalCertificateUploadIsCheckedServedAndNeverReturned — the upload is
// refused for each reason with the reason, stored and handed to the listeners when
// it is sound, and no answer anywhere carries the key.
func TestAPortalCertificateUploadIsCheckedServedAndNeverReturned(t *testing.T) {
	r := newNetRig(t)
	r.signIn(adminUser)
	good := selfSigned(t, time.Now().Add(90*24*time.Hour), "wifi.venue.test")
	other := selfSigned(t, time.Now().Add(90*24*time.Hour), "wifi.venue.test")
	expired := selfSigned(t, time.Now().Add(-time.Hour), "wifi.venue.test")
	url := "https://wifi.venue.test/en/"

	for _, c := range []struct {
		name, field, reason string
		body                map[string]any
	}{
		{"not PEM", "certificatePem", "not-pem", map[string]any{"certificatePem": "hello", "privateKeyPem": good.key, "portalUrl": url}},
		{"another key", "privateKeyPem", "key-mismatch", map[string]any{"certificatePem": good.cert, "privateKeyPem": other.key, "portalUrl": url}},
		{"expired", "certificatePem", "expired", map[string]any{"certificatePem": expired.cert, "privateKeyPem": expired.key, "portalUrl": url}},
		{"another name", "portalUrl", "name-not-covered", map[string]any{"certificatePem": good.cert, "privateKeyPem": good.key, "portalUrl": "https://other.test/en/"}},
		{"plain http", "portalUrl", "portal-url-not-https", map[string]any{"certificatePem": good.cert, "privateKeyPem": good.key, "portalUrl": "http://wifi.venue.test/en/"}},
	} {
		resp := r.do(http.MethodPut, "/network/portal-certificate", c.body)
		body := readAll(t, resp)
		f, p := faultOf(t, body, c.field)
		if resp.StatusCode != http.StatusUnprocessableEntity || f != gen.FieldFaultCertificateRefused || p["reason"] != c.reason {
			t.Errorf("%s: %d, %s %v on %s: %s", c.name, resp.StatusCode, f, p, c.field, body)
		}
	}
	if r.net.portal != nil {
		t.Fatal("a refused upload was stored")
	}

	resp := r.do(http.MethodPut, "/network/portal-certificate", map[string]any{
		"certificatePem": good.cert, "privateKeyPem": good.key, "portalUrl": url})
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a sound pair: %d %s", resp.StatusCode, body)
	}
	var st gen.PortalCertificateStatus
	_ = json.Unmarshal([]byte(body), &st)
	if st.State != gen.PortalCertificateStateServed || !st.KeySet || st.Source != gen.PortalCertificateStatusSourceConsole {
		t.Errorf("status after upload: %+v", st)
	}
	if p := r.rt.last.PortalCertificate; p == nil || p.Info.FingerprintSHA256 != *st.FingerprintSha256 {
		t.Errorf("the portal listeners were not handed the uploaded pair: %+v", r.rt.last.PortalCertificate)
	}
	if r.srv.PortalURL() != "https://wifi.venue.test/en" {
		t.Errorf("the address sent with the certificate is not in force: %q", r.srv.PortalURL())
	}

	// Never returned: not by the upload's answer, not by the settings read.
	for _, text := range []string{body, readAll(t, r.do(http.MethodGet, "/network/settings", nil))} {
		if strings.Contains(text, "PRIVATE KEY") || strings.Contains(text, strings.Split(good.key, "\n")[1]) {
			t.Fatalf("an answer carries the private key: %s", text)
		}
	}
	if e := auditOf(t, r.rig, gen.AuditKindPortalCertificateChanged); len(e) != 1 || strings.Contains(string(mustJSON(t, e)), "PRIVATE") {
		t.Errorf("audit: %+v", e)
	}

	// With a certificate served, the address has to stay one it covers.
	code, out := r.put(func(in *gen.NetworkSettingsUpdate) { in.PortalUrl = ptrTo("https://elsewhere.test/en/") })
	if f, p := faultOf(t, out, "portalUrl"); code != http.StatusUnprocessableEntity || f != gen.FieldFaultCertificateRefused || p["reason"] != "name-not-covered" {
		t.Errorf("moving the address off the certificate: %d %s", code, out)
	}

	// Removed: the listeners are told there is none.
	if resp := r.do(http.MethodDelete, "/network/portal-certificate", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d %s", resp.StatusCode, readAll(t, resp))
	}
	if r.rt.last.PortalCertificate != nil {
		t.Error("the listeners still hold the removed certificate")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestAPortalCertificateNeedsAKeyEncryptionKey — a key is never stored in the
// clear, so a deployment with nothing to seal it under refuses the upload.
func TestAPortalCertificateNeedsAKeyEncryptionKey(t *testing.T) {
	r := newNetRig(t)
	r.net.noKEK = true
	r.signIn(adminUser)
	good := selfSigned(t, time.Now().Add(24*time.Hour), "wifi.venue.test")
	resp := r.do(http.MethodPut, "/network/portal-certificate", map[string]any{
		"certificatePem": good.cert, "privateKeyPem": good.key, "portalUrl": "https://wifi.venue.test/en/"})
	if body := readAll(t, resp); resp.StatusCode != http.StatusNotImplemented {
		t.Errorf("no KEK: %d %s", resp.StatusCode, body)
	}
}

// ── FreeRADIUS ───────────────────────────────────────────────────────────────

// TestARadiusClientWriteSyncsTheClientTableByItself — a client changed in the
// console must reach FreeRADIUS without anybody running `foyerctl sync-nas`.
// Every write triggers the sync, and it is audited.
func TestARadiusClientWriteSyncsTheClientTableByItself(t *testing.T) {
	r := newNetRig(t)
	r.signIn(adminUser)
	if resp := r.do(http.MethodPatch, "/nas/nas-1", map[string]any{"enabled": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("updateNas: %d %s", resp.StatusCode, readAll(t, resp))
	}
	if resp := r.do(http.MethodDelete, "/nas/nas-1", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("deleteNas: %d %s", resp.StatusCode, readAll(t, resp))
	}
	if got := r.net.syncs; len(got) != 2 || got[0] != gen.RadiusSyncTriggerNasUpdated || got[1] != gen.RadiusSyncTriggerNasDeleted {
		t.Fatalf("syncs after an update and a delete: %v", got)
	}
	if e := auditOf(t, r.rig, gen.AuditKindRadiusClientsSynced); len(e) != 2 {
		t.Errorf("%d radius-clients-synced entries, want 2", len(e))
	}
	var rs gen.RadiusServerSettings
	_ = json.Unmarshal([]byte(readAll(t, r.do(http.MethodGet, "/network/radius", nil))), &rs)
	if rs.LastSync == nil || rs.LastSync.Trigger != gen.RadiusSyncTriggerNasDeleted {
		t.Errorf("the sync state the console shows: %+v", rs.LastSync)
	}
}

// TestGeneratorSettingsAreBoundedAndPendingUntilApplied — saved, the running server
// does not have them, and the console says so until an apply reaches it.
func TestGeneratorSettingsAreBoundedAndPendingUntilApplied(t *testing.T) {
	r := newNetRig(t)
	r.signIn(adminUser)
	radius := func() gen.RadiusServerSettings {
		var rs gen.RadiusServerSettings
		_ = json.Unmarshal([]byte(readAll(t, r.do(http.MethodGet, "/network/radius", nil))), &rs)
		return rs
	}
	if rs := radius(); rs.MaxServers != api.DefaultRadiusMaxServers || rs.PendingApply {
		t.Fatalf("a fresh deployment: %+v", rs)
	}
	resp := r.do(http.MethodPut, "/network/radius", map[string]any{"maxServers": 2, "eapTlsEnabled": false})
	if f, _ := faultOf(t, readAll(t, resp), "maxServers"); f != gen.FieldFaultBelowMinimum {
		t.Errorf("two workers: %d %s", resp.StatusCode, f)
	}
	resp = r.do(http.MethodPut, "/network/radius", map[string]any{"maxServers": 32, "eapTlsEnabled": true})
	if f, _ := faultOf(t, readAll(t, resp), "eapTlsEnabled"); f != gen.FieldFaultRequired {
		t.Errorf("EAP-TLS on with no certificate: %d %s", resp.StatusCode, f)
	}
	if resp := r.do(http.MethodPut, "/network/radius", map[string]any{"maxServers": 40, "eapTlsEnabled": false}); resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, readAll(t, resp))
	}
	if rs := radius(); rs.MaxServers != 40 || !rs.PendingApply {
		t.Fatalf("after saving: %+v", rs)
	}
	if resp := r.do(http.MethodPost, "/network/radius/apply", nil); resp.StatusCode != http.StatusOK || r.net.applied != 1 {
		t.Fatalf("apply: %d, applied %d", resp.StatusCode, r.net.applied)
	}
	if rs := radius(); rs.PendingApply {
		t.Errorf("still pending after an apply that restarted the server: %+v", rs)
	}
	if e := auditOf(t, r.rig, gen.AuditKindRadiusConfigurationApplied); len(e) != 1 {
		t.Errorf("%d radius-configuration-applied entries", len(e))
	}
}

// TestCoaTimingIsBoundedPerDeviceAndDefaults — stored per device, refused outside
// its bounds, refused for a device that does not speak CoA itself.
func TestCoaTimingIsBoundedPerDeviceAndDefaults(t *testing.T) {
	r := newNetRig(t)
	r.signIn(adminUser)
	resp := r.do(http.MethodPut, "/nas/nas-coa/coa-timing", map[string]any{"timeoutMs": 50, "retries": 9})
	body := readAll(t, resp)
	if f, _ := faultOf(t, body, "timeoutMs"); f != gen.FieldFaultBelowMinimum {
		t.Errorf("50 ms: %s", body)
	}
	if f, _ := faultOf(t, body, "retries"); f != gen.FieldFaultAboveMaximum {
		t.Errorf("nine retries: %s", body)
	}
	resp = r.do(http.MethodPut, "/nas/nas-coa/coa-timing", map[string]any{"timeoutMs": 800, "retries": 2})
	var got gen.NasCoaTiming
	_ = json.Unmarshal([]byte(readAll(t, resp)), &got)
	if resp.StatusCode != http.StatusOK || got.TimeoutMs != 800 || got.Retries != 2 || got.Source != gen.NasCoaTimingSourceConsole {
		t.Errorf("a sound timing: %d %+v", resp.StatusCode, got)
	}
	if v := r.net.coa["nas-coa"]; v == nil || v.TimeoutMs != 800 {
		t.Errorf("stored: %+v", v)
	}
	resp = r.do(http.MethodPut, "/nas/nas-1/coa-timing", map[string]any{"timeoutMs": 800, "retries": 2})
	if f, _ := faultOf(t, readAll(t, resp), "nasId"); f != gen.FieldFaultIncorrect {
		t.Errorf("a UniFi access point: %d %s", resp.StatusCode, f)
	}
	resp = r.do(http.MethodPut, "/nas/nas-coa/coa-timing", map[string]any{"timeoutMs": nil, "retries": nil})
	_ = json.Unmarshal([]byte(readAll(t, resp)), &got)
	if got.Source != gen.NasCoaTimingSourceDefault || r.net.coa["nas-coa"] != nil {
		t.Errorf("both null did not go back to the defaults: %+v", got)
	}
}

// TestNetworkWritesAreAdministratorsOnly — staff read the network configuration
// (`nas:read`) and change none of it.
func TestNetworkWritesAreAdministratorsOnly(t *testing.T) {
	r := newNetRig(t)
	r.signIn(staffUser)
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "/network/settings"}, {http.MethodPut, "/network/portal-certificate"},
		{http.MethodDelete, "/network/portal-certificate"}, {http.MethodPut, "/network/radius"},
		{http.MethodPut, "/network/radius/eap-certificate"}, {http.MethodPost, "/network/radius/sync"},
		{http.MethodPost, "/network/radius/apply"}, {http.MethodPut, "/nas/nas-coa/coa-timing"},
	} {
		if resp := r.do(c.method, c.path, map[string]any{}); resp.StatusCode != http.StatusForbidden {
			t.Errorf("staff %s %s: %d", c.method, c.path, resp.StatusCode)
		}
	}
	if resp := r.do(http.MethodGet, "/network/settings", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("staff read: %d", resp.StatusCode)
	}
}

// TestNotServedReasonHoldsTheLeafToBothEndsOfItsValidity — the clock rule is
// config.CheckValidAt's. Looking only at NotAfter would serve a certificate that
// is not valid yet, and every browser would refuse it.
func TestNotServedReasonHoldsTheLeafToBothEndsOfItsValidity(t *testing.T) {
	now := time.Now()
	leaf := &x509.Certificate{
		DNSNames:  []string{"wifi.example"},
		NotBefore: now.Add(time.Hour),
		NotAfter:  now.Add(48 * time.Hour),
	}
	const at = "https://wifi.example/en/"
	if r, ok := api.NotServedReason(leaf, at, now); ok || r != gen.PortalCertificateStatusNotServedReasonNotYetValid {
		t.Errorf("an hour before NotBefore: %q served=%v, want not-yet-valid", r, ok)
	}
	if r, ok := api.NotServedReason(leaf, at, now.Add(2*time.Hour)); !ok {
		t.Errorf("inside the window: %q, want served", r)
	}
	if r, ok := api.NotServedReason(leaf, at, now.Add(72*time.Hour)); ok || r != gen.PortalCertificateStatusNotServedReasonExpired {
		t.Errorf("after NotAfter: %q served=%v, want expired", r, ok)
	}
}
