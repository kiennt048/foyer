package apistore_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/store"
)

// The `network` tag against real PostgreSQL.

// TestARadiusClientRegisteredInTheConsoleReachesFreeRADIUSsTable — end to end: a
// device registered in the console reaches the `nas` table FreeRADIUS reads without
// anybody running `foyerctl sync-nas`. The write itself syncs it, the console can see that it did, and a deletion takes the row
// back out.
//
// **In a tenant of its own**: the sync unseals every client in its tenant, and the
// worlds sharing the default one each seal under a KEK of their own — a condition
// no deployment has (one KEK per install), which the sync rightly refuses.
func TestARadiusClientRegisteredInTheConsoleReachesFreeRADIUSsTable(t *testing.T) {
	w := newFreshInstall(t)
	w.signIn()
	var site gen.Site
	mustJSON(t, w.do(http.MethodPost, "/sites", map[string]any{"name": uniq("Venue")}, http.StatusCreated), &site)
	addr := w.nextAddr()
	raw := w.do(http.MethodPost, "/nas", map[string]any{
		"siteId": site.Id, "name": "Hotspot router", "driver": "mikrotik", "ipAddress": addr,
	}, http.StatusCreated)
	var created struct {
		Nas          gen.Nas `json:"nas"`
		SharedSecret string  `json:"sharedSecret"`
	}
	mustJSON(t, raw, &created)

	var rs gen.RadiusServerSettings
	w.getJSON("/network/radius", &rs)
	var secret string
	if err := w.owner.QueryRow(context.Background(), `SELECT secret FROM nas WHERE nasname = $1`, addr).Scan(&secret); err != nil {
		t.Fatalf("the new client is not in the table FreeRADIUS reads: %v (last sync %+v)", err, rs.LastSync)
	}
	if secret != created.SharedSecret {
		t.Error("the projected secret is not the one the console showed once")
	}
	if rs.LastSync == nil || rs.LastSync.Trigger != gen.RadiusSyncTriggerNasCreated || rs.LastSync.Outcome != gen.RadiusClientSyncOutcomeSynced {
		t.Errorf("the sync state the console shows: %+v", rs.LastSync)
	}

	w.do(http.MethodDelete, "/nas/"+created.Nas.Id, nil, http.StatusNoContent)
	var n int
	_ = w.owner.QueryRow(context.Background(), `SELECT count(*) FROM nas WHERE nasname = $1`, addr).Scan(&n)
	if n != 0 {
		t.Error("a deleted client is still in the table FreeRADIUS reads")
	}
}

// TestSettingsSavedInTheConsoleAreStoredAndTheKeyIsSealed — the portal address and
// a throttle round-trip through PostgreSQL, and the certificate's private key is in
// the database only as ciphertext.
func TestSettingsSavedInTheConsoleAreStoredAndTheKeyIsSealed(t *testing.T) {
	w := newWorld(t)
	w.signIn()
	var s gen.NetworkSettings
	w.getJSON("/network/settings", &s)
	s.Throttles.PortalAddress = gen.SignInThrottle{Burst: 300, IntervalMs: 200}
	portalURL := "https://wifi.venue.test/en/"
	w.do(http.MethodPut, "/network/settings", gen.NetworkSettingsUpdate{
		PortalUrl: &portalURL, Throttles: s.Throttles, BodyCeilings: s.BodyCeilings,
	}, http.StatusOK)

	got, err := w.adapter.Ports().Network.StoredNetwork(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.PortalURL == nil || *got.PortalURL != portalURL || got.Throttles == nil || got.Throttles.PortalAddress.Burst != 300 {
		t.Fatalf("stored: %+v", got)
	}

	certPEM, keyPEM := pair(t, "wifi.venue.test")
	w.do(http.MethodPut, "/network/portal-certificate", map[string]any{
		"certificatePem": certPEM, "privateKeyPem": keyPEM,
	}, http.StatusOK)
	var sealed []byte
	if err := w.owner.QueryRow(context.Background(),
		`SELECT private_key_sealed FROM network_certificates WHERE purpose = 'portal-tls'`).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("PRIVATE KEY")) || bytes.Contains(sealed, []byte(keyPEM[40:80])) {
		t.Fatal("the portal's private key is stored in the clear")
	}
	stored, err := w.adapter.Ports().Network.PortalCertificate(context.Background())
	if err != nil || string(stored.KeyPEM) != keyPEM {
		t.Fatalf("the sealed key does not open to the uploaded one: %v", err)
	}
}

// timedDrivers records the timing the adapter hands the per-NAS seam.
type timedDrivers struct {
	mu     sync.Mutex
	timing *apistore.CoATiming
}

func (d *timedDrivers) ForNAS(store.RadiusClient, string, nas.SessionSource) (nas.NASDriver, error) {
	return nas.NewStrictMockDriver(nas.VendorMikroTik), nil
}

func (d *timedDrivers) ForNASTimed(_ store.RadiusClient, _ string, _ nas.SessionSource, t apistore.CoATiming) (nas.NASDriver, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.timing = &t
	return nas.NewStrictMockDriver(nas.VendorMikroTik), nil
}

// TestCoATimingSetInTheConsoleReachesTheNextDriver — no restart and no cache: the
// next driver built for the device carries the timing just saved.
func TestCoATimingSetInTheConsoleReachesTheNextDriver(t *testing.T) {
	rec := &timedDrivers{}
	w := newWorldWith(t, func(c *apistore.Config) { c.NASDrivers = rec })
	w.signIn()
	raw := w.do(http.MethodPost, "/nas", map[string]any{
		"siteId": w.siteA, "name": "Router", "driver": "mikrotik", "ipAddress": w.nextAddr(),
	}, http.StatusCreated)
	var created struct {
		Nas gen.Nas `json:"nas"`
	}
	mustJSON(t, raw, &created)

	if _, _, err := w.adapter.NASDriverFor(context.Background(), created.Nas.Id); err != nil || rec.timing != nil {
		t.Fatalf("precondition: with nothing stored the defaults apply (%v, %+v)", err, rec.timing)
	}
	w.do(http.MethodPut, "/nas/"+created.Nas.Id+"/coa-timing", map[string]any{"timeoutMs": 750, "retries": 3}, http.StatusOK)
	if _, _, err := w.adapter.NASDriverFor(context.Background(), created.Nas.Id); err != nil {
		t.Fatal(err)
	}
	if rec.timing == nil || rec.timing.Timeout != 750*time.Millisecond || rec.timing.Retries != 3 {
		t.Errorf("the driver was built with %+v", rec.timing)
	}
	var list struct {
		Items []gen.NasCoaTiming `json:"items"`
	}
	w.getJSON("/network/coa-timing", &list)
	found := false
	for _, it := range list.Items {
		found = found || (it.NasId == created.Nas.Id && it.TimeoutMs == 750 && it.Source == gen.NasCoaTimingSourceConsole)
	}
	if !found {
		t.Errorf("the device is not listed with its timing: %+v", list.Items)
	}
}

// TestTheRadiusDefaultsAreTheGenerators — `internal/api` restates the worker count
// it reports as the default, because it does not import the generator; this holds
// the two equal.
// TestAStoredWorkerCountAbovePostgresBudgetIsNotGenerated — a row saved while the
// console allowed 256 workers is generated at the ceiling, not at 256: each worker
// is an rlm_sql connection against max_connections = 80.
func TestAStoredWorkerCountAbovePostgresBudgetIsNotGenerated(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	network := w.adapter.Ports().Network.(*apistore.Network)
	if err := network.SaveRadius(ctx, 30, false, "test"); err != nil {
		t.Fatal(err)
	}
	// What an earlier console accepted, written as that console wrote it -- which
	// since 00054 only a row older than that migration can be, so the
	// ceiling's CHECK is lifted to reproduce one. This suite shares one database
	// across tests, so the row and the CHECK are put back as 00054 leaves them.
	if _, err := w.owner.Exec(ctx, `ALTER TABLE network_settings DROP CONSTRAINT network_settings_radius_max_servers_ceiling`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := w.owner.Exec(context.Background(), `UPDATE network_settings SET radius_max_servers = 44 WHERE radius_max_servers > 44`); err != nil {
			t.Error(err)
		}
		if _, err := w.owner.Exec(context.Background(), `ALTER TABLE network_settings ADD CONSTRAINT network_settings_radius_max_servers_ceiling
			CHECK (radius_max_servers > 0 AND radius_max_servers <= 44)`); err != nil {
			t.Errorf("restoring 00054's CHECK: %v", err)
		}
	})
	if tag, err := w.owner.Exec(ctx, `UPDATE network_settings SET radius_max_servers = 256`); err != nil || tag.RowsAffected() != 1 {
		t.Fatalf("raising the stored worker count: %v (%d rows)", err, tag.RowsAffected())
	}
	s, err := network.RadiusSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if s.MaxServers != 44 {
		t.Fatalf("a stored 256 generates %d workers, want the PostgreSQL ceiling 44", s.MaxServers)
	}
}

// TestTheDatabaseHoldsTheWorkerCeiling: the application's own role can store
// api.RadiusMaxServersCeiling workers and cannot store one more (migration 00054).
// A ceiling held only in Go is one that SQL injection as foyer_app walks past.
func TestTheDatabaseHoldsTheWorkerCeiling(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	network := w.adapter.Ports().Network.(*apistore.Network)
	if err := network.SaveRadius(ctx, api.RadiusMaxServersCeiling, false, "test"); err != nil {
		t.Fatalf("the ceiling itself (%d) was refused: %v", api.RadiusMaxServersCeiling, err)
	}
	if err := network.SaveRadius(ctx, api.RadiusMaxServersCeiling+1, false, "test"); err == nil {
		t.Fatalf("the database stored %d workers, above the PostgreSQL ceiling %d", api.RadiusMaxServersCeiling+1, api.RadiusMaxServersCeiling)
	}
}

func TestTheRadiusDefaultsAreTheGenerators(t *testing.T) {
	if api.DefaultRadiusMaxServers != raddb.DefaultConfig().MaxServers {
		t.Fatalf("api.DefaultRadiusMaxServers %d, generator %d", api.DefaultRadiusMaxServers, raddb.DefaultConfig().MaxServers)
	}
}

func pair(t *testing.T, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, DNSNames: []string{name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalECPrivateKey(key)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd}))
}
