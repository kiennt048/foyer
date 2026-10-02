package apistore_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/portal"
	"github.com/kiennt048/foyer/internal/store"
)

// A voucher redeemed through the real HTTP surface, against a real PostgreSQL,
// producing a real grant row.
//
// None of these properties is provable by a unit test:
//
//   - **`303`, never `200`.** The iOS Captive Network Assistant re-checks
//     captivity only after a full document navigation. Without one its button
//     stays "Cancel", and Cancel disassociates the device — so a `fetch`-shaped
//     login authenticates the guest and then throws them off the network, and
//     Playwright reports a pass. The assertion is invisible in a browser and fatal
//     on hardware.
//   - **One grant per voucher, under a race.** Driven concurrently rather than
//     sequentially, because a sequential test passes against the read-then-write
//     `docs/rules/security.md` §9 forbids.
//   - **No grant for a MAC the driver did not confirm present**, and the same
//     test then makes the driver permissive and requires a grant to appear. A
//     refusal that has never been shown to be reachable from the other side is a
//     check that may be checking nothing.
//
// The driver is a fake and is labelled one. `docs/rules/testing.md`: a
// hand-written NAS mock encodes the author's assumptions, which are the same
// assumptions that produced the bug. Nothing here is evidence about UniFi. What it
// is evidence about is PostgreSQL, the schema, the adapter and the routes.

// ── the fake driver ──────────────────────────────────────────────────────────

// newPortalDriver is the shipped mock, configured the way this suite needs it.
//
// **Not a hand-rolled fake.** `MockDriver` produces all six `PresenceOutcome`
// values and can fail, so a local duplicate would be a second copy of a driver's
// behaviour one import away from the original — a fake encoding its author's
// assumptions, in the version that is hardest to see, because a fake nobody
// suspects is not read.
//
// Strict, and that is the whole configuration: no permissive default, because a
// fake answering "present" for a MAC nobody told it about would make the named
// security test pass against a portal that never called a driver at all —
// `TestNoGrantExistsForAMACTheDriverDidNotConfirm` flips exactly that switch on
// purpose and requires a grant to appear.
//
// Every capability comes from `ShippedCapabilities`, `HardQuota` included. A
// fake that wrote `hardQuota: true` as a literal and happened to agree with the
// shipped driver would be that same assumption-encoding shape, not an exception
// to it. `TestALimitWithNoAccountingIsStoredUnenforced` lowers it in
// one written line, which is the direction the asymmetry allows.
func newPortalDriver() *nas.MockDriver {
	return nas.NewStrictMockDriver(nas.VendorUniFi)
}

// allow tells the driver this MAC really is associated.
//
// The MAC is normalised on the way in because the portal normalises before it
// asks, and `MockDriver` keys on its own normalisation — two spellings of one
// address is a refusal nobody can read.
func allow(d *nas.MockDriver, mac string) {
	d.Presences[portal.NormaliseMAC(mac)] = true
}

// portalDrivers hands the same driver back for every controller, and records the
// vendor site id it was asked to build for.
type portalDrivers struct {
	mu sync.Mutex
	d  *nas.MockDriver
	// builtForSite records the vendor site id the driver was constructed with, so
	// a test can tell it apart from Foyer's own site id.
	builtForSite string
}

func (p *portalDrivers) ForController(_ store.Controller, _ []byte, vendorSiteID string) (nas.NASDriver, error) {
	p.mu.Lock()
	p.builtForSite = vendorSiteID
	p.mu.Unlock()
	return p.d, nil
}

func (p *portalDrivers) site() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.builtForSite
}

// ── the venue ────────────────────────────────────────────────────────────────

// venue is one site with one access point behind one controller, one voucher
// batch, one account, and the guest listener in front of all of it.
type venue struct {
	t       *testing.T
	st      *store.Store
	owner   *pgxpool.Pool
	adapter *apistore.Adapter
	server  *httptest.Server
	driver  *nas.MockDriver
	drivers *portalDrivers

	site       string
	clientID   string
	vendorSite string
	apMAC      string
	guestMAC   string

	batchID   string
	voucherID string
	code      string

	accountUser string
	accountPass string
	accountID   string

	jar []*http.Cookie
}

func newVenue(t *testing.T) *venue {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()

	st := openStore(t)
	n := worldSeq.Add(1)
	v := &venue{
		t:      t,
		st:     st,
		owner:  ownerPool(t),
		driver: newPortalDriver(),
		site:   uniq("site"),
		// Per-venue MAC and address space: `access_points` is UNIQUE (tenant_id,
		// mac) and `radius_clients` is UNIQUE (tenant_id, address), so two venues
		// sharing either is two tests fighting over one row.
		apMAC:      fmt.Sprintf("94:2a:6f:%02x:%02x:01", (n/256)%256, n%256),
		guestMAC:   fmt.Sprintf("02:2a:6f:%02x:%02x:02", (n/256)%256, n%256),
		vendorSite: uniq("vsite"),
	}
	allow(v.driver, v.guestMAC)
	v.drivers = &portalDrivers{d: v.driver}

	adapter, err := apistore.New(apistore.Config{Store: st, Drivers: v.drivers})
	if err != nil {
		t.Fatalf("building the adapter: %v", err)
	}
	v.adapter = adapter

	v.seed(ctx, n)
	// Every venue's guest arrives from loopback, and the RFC 8908 book is keyed on
	// the address: a previous test's device there would make this one's ambiguous.
	// A fresh book, as a fresh deployment has (the tests do not run in parallel).
	if _, err := v.owner.Exec(ctx, `DELETE FROM portal_addresses`); err != nil {
		t.Fatalf("clearing the address book: %v", err)
	}

	ports := adapter.PortalPorts()
	srv, err := api.New(api.Config{Ports: adapter.Ports(), Portal: ports})
	if err != nil {
		t.Fatalf("api.New: %v", err)
	}
	svc, err := portal.NewService(portal.ServiceConfig{Ports: ports})
	if err != nil {
		t.Fatalf("portal.NewService: %v", err)
	}

	// The same three mounts `cmd/foyer`'s portal listener has, in the same order.
	// A test server that mounted only the API would prove the operations and not
	// the front door, and the front door is the only place a MAC is ever learned.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/captive-portal", svc.CaptiveHandler("http://portal.example/en/", ""))
	mux.Handle(api.BasePath+"/", srv.PortalHandler())
	// A bare `/`, not `GET /`. See `cmd/foyer`'s portalListener: `GET /` beside
	// `/api/v0/` is a pattern conflict `http.ServeMux` panics on at registration,
	// so the process would not start at all.
	mux.Handle("/", svc.EntryHandler([]string{"en", "vi"}, "en"))

	v.server = httptest.NewServer(mux)
	t.Cleanup(v.server.Close)
	return v
}

// openStore connects with a keyring of its own.
//
// The controller credential is envelope-encrypted, and the driver seam unseals one
// before it builds anything — so a store with no keyring here would fail every
// presence check for a reason that has nothing to do with the thing under test.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	kr, err := store.NewKeyring("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatalf("building a keyring: %v", err)
	}
	st, err := store.Open(context.Background(), store.Config{URL: ownerURL, Keys: kr, MaxConns: 8})
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

func (v *venue) seed(ctx context.Context, n int64) {
	t := v.t

	if err := v.st.Sites().Create(ctx, &store.Site{ID: v.site, Name: "Portal venue", Environment: "lab"}); err != nil {
		t.Fatalf("creating the site: %v", err)
	}

	// A controller with a credential, because the driver seam unseals one before
	// it builds anything. A site whose access point has no controller has no
	// driver, and no driver is a refusal rather than a skip.
	controllerID := uniq("ctl")
	c := &store.Controller{
		ID: controllerID, Name: "Controller " + controllerID, Vendor: "unifi",
		BaseURL: "https://192.0.2.10:8443", AuthMode: "api-key", TLSMode: "trust-on-first-use",
	}
	if err := v.st.Controllers().Create(ctx, c, []byte(randomString())); err != nil {
		t.Fatalf("creating the controller: %v", err)
	}
	if err := v.st.Controllers().RecordProbe(ctx, controllerID, store.ControllerProbe{
		Reachability: "ok",
		VendorSites:  []store.VendorSite{{ID: v.vendorSite, Name: "Default"}},
	}, time.Now()); err != nil {
		t.Fatalf("recording the probe: %v", err)
	}
	if err := v.st.Controllers().BindVendorSite(ctx, controllerID, v.vendorSite, &v.site); err != nil {
		t.Fatalf("binding the vendor site: %v", err)
	}

	ss := &store.SharedSecret{ID: uniq("sec"), SiteID: v.site, Label: "Guest profile"}
	if err := v.st.SharedSecrets().Create(ctx, ss, []byte(randomString())); err != nil {
		t.Fatalf("creating the shared secret: %v", err)
	}
	v.clientID = uniq("rc")
	if err := v.st.RadiusClients().Create(ctx, &store.RadiusClient{
		ID: v.clientID, SiteID: v.site, Name: "Lobby AP",
		Address: netip.MustParsePrefix(fmt.Sprintf("10.%d.%d.10/32", 200+n/250, n%250)),
		Source:  "manual", Driver: "unifi", ControllerID: &controllerID, SharedSecretID: ptr(ss.ID),
		RequireMsgAuth: true, LimitProxyState: true, MessageAuthState: "enforced",
		Reachability: "ok", Enabled: true,
		Capabilities: store.Capabilities{WalledGarden: "fqdn-sni", HardQuota: true},
	}); err != nil {
		t.Fatalf("creating the radius client: %v", err)
	}

	// The access point, written as the schema owner because discovery is what
	// creates one and there is no controller here to discover from.
	if _, err := v.owner.Exec(ctx, `
		INSERT INTO access_points (id, site_id, controller_id, mac, name, radius_client_id, state)
		VALUES ($1, $2, $3, $4::macaddr, 'Lobby radio', $5, 'online')`,
		"ap-"+strings.ReplaceAll(v.apMAC, ":", ""), v.site, controllerID, v.apMAC, v.clientID); err != nil {
		t.Fatalf("seeding the access point: %v", err)
	}

	if err := v.st.Portal().Upsert(ctx, &store.PortalSettings{
		SiteID: v.site, VenueName: "The Portal Venue",
		Languages: []string{"en", "vi"}, Methods: []string{"voucher", "account"},
		TermsSummary: map[string]string{"en": "Be nice."},
	}); err != nil {
		t.Fatalf("seeding portal settings: %v", err)
	}

	batch, err := v.adapter.CreateBatch(ctx, gen.VoucherBatchCreate{
		SiteId: v.site, Label: "Front counter", Count: 1, ExpiryMode: gen.FromFirstUse,
		Plan: gen.PlanRequest{
			Name: ptr("One hour"), NameLang: ptr(gen.En),
			DurationMinutes: ptr(60), DataAllowanceMb: ptr(500),
			UpKbps: ptr(2000), DownKbps: ptr(8000),
		},
	}, "seed")
	if err != nil {
		t.Fatalf("creating the batch: %v", err)
	}
	v.batchID = batch.Id
	page, err := v.adapter.Vouchers(ctx, api.VoucherQuery{SiteID: v.site, BatchID: batch.Id, Window: api.Window{Limit: 5}})
	if err != nil || len(page.Items) != 1 {
		t.Fatalf("reading the batch back: %v (%d items)", err, len(page.Items))
	}
	v.voucherID, v.code = page.Items[0].Id, page.Items[0].Code

	v.accountUser, v.accountPass = uniq("guest"), randomString()+"Aa1"
	acc, err := v.adapter.CreateAccount(ctx, gen.AccountCreate{
		SiteId: v.site, Username: v.accountUser, Password: v.accountPass,
	})
	if err != nil {
		t.Fatalf("creating the account: %v", err)
	}
	v.accountID = acc.Id
}

// ── driving it ───────────────────────────────────────────────────────────────

// arrive is the NAS redirect: the request that carries the unsigned parameters and
// gets a session cookie back.
func (v *venue) arrive(mac string) {
	v.t.Helper()
	q := url.Values{
		"ap": {v.apMAC}, "id": {mac},
		"ssid": {"Venue Guest"}, "url": {"http://example.test/news"},
	}
	resp := v.send(http.MethodGet, "/?"+q.Encode(), nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		v.t.Fatalf("the front door answered %d, want 302", resp.StatusCode)
	}
	if len(v.jar) == 0 {
		v.t.Fatal("the front door set no session cookie, so nothing binds this browser to a MAC")
	}
}

// send makes one request carrying whatever cookies the portal has set.
//
// The client refuses to follow redirects, because the redirect *is* the assertion:
// following one would turn "the portal answered 303 to the connected page" into
// "the portal answered 404 for a document this server does not serve".
func (v *venue) send(method, path string, form url.Values) *http.Response {
	v.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequest(method, v.server.URL+path, body)
	if err != nil {
		v.t.Fatal(err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range v.jar {
		req.AddCookie(c)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(req)
	if err != nil {
		v.t.Fatalf("%s %s: %v", method, path, err)
	}
	for _, c := range resp.Cookies() {
		if c.Name == portal.SessionCookieName && c.Value != "" {
			v.jar = []*http.Cookie{c}
		}
	}
	return resp
}

func (v *venue) api(method, path string, form url.Values) *http.Response {
	v.t.Helper()
	return v.send(method, api.BasePath+path, form)
}

// grantRow is one row of portal_grants, read back as the database holds it.
//
// Read with SQL rather than through the adapter on purpose: the adapter is what is
// under test, and a read that went through the same conversion as the write could
// agree with itself about a column that is wrong.
type grantRow struct {
	ID         string
	SiteID     string
	NASID      *string
	ClientMAC  string
	APMAC      *string
	Method     string
	VoucherID  *string
	AccountID  *string
	PlanName   *string
	PlanLang   *string
	Duration   *int
	Allowance  *int
	UpKbps     *int
	DownKbps   *int
	EnforcedBy string
	GrantedAt  time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

func (v *venue) grants(ctx context.Context) []grantRow {
	v.t.Helper()
	rows, err := v.owner.Query(ctx, `
		SELECT id, site_id, nas_id, client_mac::text, ap_mac::text, method, voucher_id, account_id,
		       plan_name, plan_name_lang, duration_minutes, data_allowance_mb, up_kbps, down_kbps,
		       enforced_by, granted_at, expires_at, revoked_at
		FROM portal_grants WHERE site_id = $1 ORDER BY granted_at`, v.site)
	if err != nil {
		v.t.Fatalf("reading portal_grants: %v", err)
	}
	defer rows.Close()
	var out []grantRow
	for rows.Next() {
		var g grantRow
		if err := rows.Scan(&g.ID, &g.SiteID, &g.NASID, &g.ClientMAC, &g.APMAC, &g.Method,
			&g.VoucherID, &g.AccountID, &g.PlanName, &g.PlanLang, &g.Duration, &g.Allowance,
			&g.UpKbps, &g.DownKbps, &g.EnforcedBy, &g.GrantedAt, &g.ExpiresAt, &g.RevokedAt); err != nil {
			v.t.Fatalf("scanning portal_grants: %v", err)
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		v.t.Fatalf("reading portal_grants: %v", err)
	}
	return out
}

func (v *venue) voucherState(ctx context.Context) string {
	v.t.Helper()
	var state string
	if err := v.owner.QueryRow(ctx, `SELECT state FROM vouchers WHERE id = $1`, v.voucherID).Scan(&state); err != nil {
		v.t.Fatalf("reading the voucher: %v", err)
	}
	return state
}

// ── a voucher, redeemed, end to end ──────────────────────────────────────────

// TestAVoucherIsRedeemedAndAGrantRowExists is the vertical slice.
//
// Front door, session cookie, form POST, `303`, and a row in PostgreSQL — with
// every column checked against what the venue actually sold, because a grant that
// records the wrong plan is a guest who was sold an hour and given ten minutes.
func TestAVoucherIsRedeemedAndAGrantRowExists(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)
	v.arrive(v.guestMAC)

	before := time.Now()
	resp := v.api(http.MethodPost, "/portal/voucher", url.Values{"code": {v.code}, "lang": {"en"}})
	defer resp.Body.Close()

	// **`303`, not `200`.** The CNA re-checks captivity only after a document
	// navigation; without one its button stays "Cancel", and Cancel disassociates
	// the device. A `200` here is a guest authenticated and then thrown off the
	// network, which no screenshot and no `toBeVisible()` can see.
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status is %d, want 303 — a 200 authenticates the guest and then throws them off the network",
			resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/en/connected/" {
		t.Errorf("Location is %q, want /en/connected/", loc)
	}
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control is %q — the contract requires no-store on this response by name", got)
	}
	if raw, _ := io.ReadAll(resp.Body); len(raw) != 0 {
		t.Errorf("the redirect carried %d bytes of body", len(raw))
	}

	rows := v.grants(ctx)
	if len(rows) != 1 {
		t.Fatalf("portal_grants holds %d rows, want exactly 1", len(rows))
	}
	g := rows[0]

	if g.Method != "voucher" {
		t.Errorf("method is %q, want voucher", g.Method)
	}
	if g.VoucherID == nil || *g.VoucherID != v.voucherID {
		t.Errorf("voucher_id is %v, want %q", g.VoucherID, v.voucherID)
	}
	if g.AccountID != nil {
		t.Errorf("account_id is %v on a voucher grant", *g.AccountID)
	}
	if g.ClientMAC != portal.NormaliseMAC(v.guestMAC) {
		t.Errorf("client_mac is %q, want %q", g.ClientMAC, portal.NormaliseMAC(v.guestMAC))
	}
	if g.APMAC == nil || *g.APMAC != portal.NormaliseMAC(v.apMAC) {
		t.Errorf("ap_mac is %v, want %q", g.APMAC, portal.NormaliseMAC(v.apMAC))
	}
	if g.NASID == nil || *g.NASID != v.clientID {
		t.Errorf("nas_id is %v, want the clients.conf row %q", g.NASID, v.clientID)
	}

	// The plan, copied. There is no foreign key to the batch, so a batch edited
	// afterwards cannot change what this guest was already sold.
	if g.PlanName == nil || *g.PlanName != "One hour" {
		t.Errorf("plan_name is %v, want the batch's own name", g.PlanName)
	}
	if g.PlanLang == nil || *g.PlanLang != "en" {
		t.Errorf("plan_name_lang is %v, want en — venue text reaching a guest is marked, never translated", g.PlanLang)
	}
	for _, c := range []struct {
		name string
		got  *int
		want int
	}{
		{"duration_minutes", g.Duration, 60},
		{"data_allowance_mb", g.Allowance, 500},
		{"up_kbps", g.UpKbps, 2000},
		{"down_kbps", g.DownKbps, 8000},
	} {
		if c.got == nil || *c.got != c.want {
			t.Errorf("%s is %v, want %d", c.name, c.got, c.want)
		}
	}

	// `enforced_by` is stored, not derived, and it is the *driver's* HardQuota at
	// the moment of granting rather than the column the console reads. This
	// driver reports true, so the NAS is the thing enforcing the plan.
	if g.EnforcedBy != "nas" {
		t.Errorf("enforced_by is %q, want nas — the driver reported HardQuota", g.EnforcedBy)
	}
	if g.RevokedAt != nil {
		t.Errorf("revoked_at is set on a fresh grant")
	}
	if g.ExpiresAt == nil {
		t.Fatal("expires_at is NULL for a sixty-minute plan")
	}
	if want := before.Add(60 * time.Minute); g.ExpiresAt.Before(want.Add(-2*time.Minute)) ||
		g.ExpiresAt.After(want.Add(2*time.Minute)) {
		t.Errorf("expires_at is %s, want about %s", g.ExpiresAt, want)
	}

	if state := v.voucherState(ctx); state != "in-use" {
		t.Errorf("the voucher is %q after redemption, want in-use", state)
	}

	// The session now names the grant, which is the only way the connected page
	// can find it again: `Status` resolves through the session's grant id and
	// never through the MAC.
	var attached *string
	if err := v.owner.QueryRow(ctx, `SELECT grant_id FROM portal_sessions WHERE client_mac = $1::macaddr`,
		portal.NormaliseMAC(v.guestMAC)).Scan(&attached); err != nil {
		t.Fatalf("reading portal_sessions: %v", err)
	}
	if attached == nil || *attached != g.ID {
		t.Errorf("the session's grant_id is %v, want %q", attached, g.ID)
	}

	// The driver was asked to authorize, with the **vendor's** site id. Handing a
	// UniFi controller Foyer's own site id is the conflation `internal/portal`'s
	// ports.go warns about: the lookup lands in the wrong place on a controller
	// that manages more than one of the vendor's sites.
	calls := v.driver.AuthorizeLog()
	if len(calls) != 1 {
		t.Fatalf("the driver was asked to authorize %d times, want once", len(calls))
	}
	if calls[0].SiteID != v.vendorSite {
		t.Errorf("AuthorizeGuest got site %q, want the controller's own %q (Foyer's is %q)",
			calls[0].SiteID, v.vendorSite, v.site)
	}
	// The same conflation one step earlier, and it was recorded and never asserted:
	// the driver is *built* for a vendor site too, and a driver constructed against
	// Foyer's site id looks up the client in the wrong place before AuthorizeGuest
	// is reached at all.
	if got := v.drivers.site(); got != v.vendorSite {
		t.Errorf("the driver was built for site %q, want the controller's own %q (Foyer's is %q)",
			got, v.vendorSite, v.site)
	}
	if calls[0].TimeLimitMinutes != 60 || calls[0].DataUsageLimitMB != 500 {
		t.Errorf("the NAS was asked to enforce %d minutes / %d MB, want 60 / 500",
			calls[0].TimeLimitMinutes, calls[0].DataUsageLimitMB)
	}
}

// ── the same voucher, twice, at once ─────────────────────────────────────────

// TestOneVoucherYieldsOneGrantUnderARace.
//
// Driven concurrently rather than sequentially, and that is the whole point: a
// sequential test passes against the read-then-write §9 forbids, because the first
// redemption has already committed by the time the second reads. Two guests typing
// the same code in the same millisecond is the case the rule exists for.
//
// Two browsers with two MACs, because that is what two guests are — and because the
// rate limiter keys on the MAC, so one bucket would refuse the second request for
// the wrong reason entirely.
func TestOneVoucherYieldsOneGrantUnderARace(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)

	second := strings.TrimSuffix(v.guestMAC, "02") + "03"
	allow(v.driver, second)

	// Two sessions, minted before the race so that the race is only the claim.
	first := newVenue2(t, v, v.guestMAC)
	other := newVenue2(t, v, second)

	start := make(chan struct{})
	var wg sync.WaitGroup
	codes := make([]int, 2)
	locations := make([]string, 2)
	for i, browser := range []*venue{first, other} {
		wg.Add(1)
		go func(i int, b *venue) {
			defer wg.Done()
			<-start
			resp := b.api(http.MethodPost, "/portal/voucher", url.Values{"code": {v.code}, "lang": {"en"}})
			defer resp.Body.Close()
			codes[i] = resp.StatusCode
			locations[i] = resp.Header.Get("Location")
			_, _ = io.Copy(io.Discard, resp.Body)
		}(i, browser)
	}
	close(start)
	wg.Wait()

	for i, c := range codes {
		if c != http.StatusSeeOther {
			t.Errorf("browser %d got %d, want 303 — every outcome is a navigation", i, c)
		}
	}

	rows := v.grants(ctx)
	if len(rows) != 1 {
		t.Fatalf("portal_grants holds %d rows for one voucher, want exactly 1: %v", len(rows), locations)
	}

	var connected, refused int
	for _, loc := range locations {
		switch {
		case loc == "/en/connected/":
			connected++
		case strings.HasPrefix(loc, "/en/voucher/?"):
			refused++
			if !strings.Contains(loc, "e=already-used") {
				t.Errorf("the losing browser was told %q, want already-used", loc)
			}
		default:
			t.Errorf("unexpected Location %q", loc)
		}
	}
	if connected != 1 || refused != 1 {
		t.Errorf("%d connected and %d refused, want exactly one of each", connected, refused)
	}
}

// newVenue2 is a second browser against the same venue: the same server, the same
// database, its own cookie jar.
func newVenue2(t *testing.T, v *venue, mac string) *venue {
	t.Helper()
	b := *v
	b.jar = nil
	b.guestMAC = mac
	b.arrive(mac)
	return &b
}

// TestThePartialUniqueIndexRefusesASecondGrantForOneVoucher.
//
// The claim is one statement and it already serialises, so the race above cannot
// reach this index — which is exactly why it is worth exercising directly. §9's
// requirement is that single use survives *a future caller who writes the
// read-then-write anyway*, and a constraint nobody has watched refuse is a
// constraint nobody knows works.
func TestThePartialUniqueIndexRefusesASecondGrantForOneVoucher(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)

	mk := func(id string) *store.PortalGrant {
		return &store.PortalGrant{
			ID: id, SiteID: v.site, ClientMAC: v.guestMAC, Method: "voucher",
			VoucherID: ptr(v.voucherID), Plan: store.Plan{EnforcedBy: "unenforced"},
			GrantedAt: time.Now(),
		}
	}
	if err := v.st.Grants().CreateGrant(ctx, mk("grant-one")); err != nil {
		t.Fatalf("the first grant was refused: %v", err)
	}
	err := v.st.Grants().CreateGrant(ctx, mk("grant-two"))
	if err == nil {
		t.Fatal("a second grant for the same voucher was accepted — the partial unique index is not doing anything")
	}
	if !strings.Contains(err.Error(), "portal_grants_one_per_voucher") {
		t.Errorf("the refusal was %v, and it does not name the index that made it — "+
			"a negative test whose subject has more than one way to fail is a test of "+
			"whichever guard fired first", err)
	}

	// Three click-through grants carry no voucher and must not collide.
	//
	// **This does not prove the index is partial, and the difference was measured
	// rather than assumed.** Rewriting the migration's `CREATE UNIQUE INDEX … WHERE
	// voucher_id IS NOT NULL` as a plain total unique index leaves this half green:
	// PostgreSQL's unique indexes treat NULLs as distinct, so the `WHERE` clause
	// buys index size and nothing else here. What does turn it red is
	// `NULLS NOT DISTINCT`, which was tried and did. So what this asserts is the
	// behaviour a click-through venue depends on — every guest gets their own grant
	// — and not the shape of the index that provides it.
	for i := range 3 {
		if err := v.st.Grants().CreateGrant(ctx, &store.PortalGrant{
			ID: fmt.Sprintf("grant-click-%d", i), SiteID: v.site, ClientMAC: v.guestMAC,
			Method: "click-through", Plan: store.Plan{EnforcedBy: "unenforced"}, GrantedAt: time.Now(),
		}); err != nil {
			t.Fatalf("click-through grant %d was refused: %v", i, err)
		}
	}
}

// ── the presence check, and the sabotage that shows it can fail ──────────────

// TestNoGrantExistsForAMACTheDriverDidNotConfirm is the named security test from
// `docs/rules/testing.md`, driven against PostgreSQL.
//
// The second half is the sabotage, and it is in the test rather than in a commit
// message: the driver is made permissive and the *same* request must then produce
// a grant. Without it, this file would assert a refusal that might be coming from
// a typo in a voucher code, a missing session, or nothing at all — which is how a
// green suite ends up holding a defect in place.
func TestNoGrantExistsForAMACTheDriverDidNotConfirm(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)

	// A MAC the driver was never told about. It arrives the same way every MAC
	// arrives: as an unsigned query parameter anyone within radio range can set.
	stranger := strings.TrimSuffix(v.guestMAC, "02") + "ff"
	v.arrive(stranger)

	resp := v.api(http.MethodPost, "/portal/voucher", url.Values{"code": {v.code}, "lang": {"en"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status is %d, want 303 — a refusal is a navigation too", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); !strings.Contains(loc, "e=not-present") {
		t.Errorf("Location is %q, want the not-present reason", loc)
	}
	if rows := v.grants(ctx); len(rows) != 0 {
		t.Fatalf("portal_grants holds %d rows for a MAC nothing confirmed", len(rows))
	}
	// And no voucher was burned. Claiming a single-use code for a device that was
	// refused is a support call that ends with staff issuing a second one.
	if state := v.voucherState(ctx); state != "unused" {
		t.Errorf("the voucher is %q after a refused redemption, want unused", state)
	}

	// ── the sabotage ─────────────────────────────────────────────────────────
	//
	// Answer present unconditionally, submit the identical request, and require a
	// grant. This is the assertion above, made to fail on purpose, from inside the
	// test that makes it.
	v.driver.WithDefaultPresent(true)
	resp = v.api(http.MethodPost, "/portal/voucher", url.Values{"code": {v.code}, "lang": {"en"}})
	resp.Body.Close()
	if loc := resp.Header.Get("Location"); loc != "/en/connected/" {
		t.Fatalf("with a permissive driver the same request answered %q — the refusal above "+
			"was not the presence check", loc)
	}
	if rows := v.grants(ctx); len(rows) != 1 {
		t.Fatalf("with a permissive driver portal_grants holds %d rows, want 1 — the assertion "+
			"above can never have been red", len(rows))
	}
	v.driver.WithDefaultPresent(false)
}

// TestAGrantNeedsADriver.
//
// An access point behind no controller has no driver, and a driver that cannot be
// built is `PresenceUnavailable` **and** a non-nil error — never a yes. §4 makes
// confirmation mandatory, so a controller Foyer cannot reach is a controller that
// cannot confirm a MAC.
//
// The guest is told `generic` rather than `not-present`, and that single difference
// is the whole of the fix `internal/portal/presence.go` exists for: "this device is
// not on the network" sends somebody to toggle Wi-Fi for twenty minutes, and
// "something went wrong" sends them to the counter, where a person can see the
// controller is down.
func TestAGrantNeedsADriver(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)

	if _, err := v.owner.Exec(ctx,
		`UPDATE access_points SET controller_id = NULL WHERE mac = $1::macaddr`,
		portal.NormaliseMAC(v.apMAC)); err != nil {
		t.Fatalf("unbinding the controller: %v", err)
	}

	v.arrive(v.guestMAC)
	resp := v.api(http.MethodPost, "/portal/voucher", url.Values{"code": {v.code}, "lang": {"en"}})
	resp.Body.Close()

	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "e=generic") {
		t.Errorf("Location is %q, want the generic reason — an outage is the venue's, not the guest's", loc)
	}
	if rows := v.grants(ctx); len(rows) != 0 {
		t.Fatalf("portal_grants holds %d rows with no driver to confirm anything", len(rows))
	}
	if state := v.voucherState(ctx); state != "unused" {
		t.Errorf("the voucher is %q, want unused", state)
	}
}

// ── the account path, and the id the CHECK constraint requires ───────────────

// TestAnAccountLoginRecordsTheAccountId.
//
// `portal.Grant` carries a username and `portal_grants.account_id` is required by
// the table's own CHECK, so the adapter resolves the id again. This is what says
// that resolution is right rather than merely present.
func TestAnAccountLoginRecordsTheAccountId(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)
	v.arrive(v.guestMAC)

	resp := v.api(http.MethodPost, "/portal/account", url.Values{
		"username": {v.accountUser}, "password": {v.accountPass}, "lang": {"en"},
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status is %d, want 303", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/en/connected/" {
		t.Fatalf("Location is %q, want /en/connected/", loc)
	}

	rows := v.grants(ctx)
	if len(rows) != 1 {
		t.Fatalf("portal_grants holds %d rows, want 1", len(rows))
	}
	if rows[0].Method != "account" {
		t.Errorf("method is %q, want account", rows[0].Method)
	}
	if rows[0].AccountID == nil || *rows[0].AccountID != v.accountID {
		t.Errorf("account_id is %v, want %q", rows[0].AccountID, v.accountID)
	}
	if rows[0].VoucherID != nil {
		t.Errorf("voucher_id is set on an account grant")
	}

	// **Nothing submitted is echoed on this path.** A username in a redirect is a
	// username in every proxy log between the phone and the portal.
	bad := newVenue2(t, v, v.guestMAC)
	resp = bad.api(http.MethodPost, "/portal/account", url.Values{
		"username": {v.accountUser}, "password": {"wrong"}, "lang": {"en"},
	})
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "e=bad-credentials") {
		t.Errorf("Location is %q, want bad-credentials", loc)
	}
	if strings.Contains(loc, v.accountUser) {
		t.Errorf("Location %q carries the submitted username", loc)
	}
}

// ── the two media types, which are not the same endpoint ─────────────────────

// TestTheStatusEndpointAndTheCaptivePortalAPIAnswerDifferentMediaTypes.
//
// `internal/portal/CONTRACT.md` invariant 11 puts `application/captive+json` on
// **the captive-portal API** (RFC 8908, errata EID 6620), and that is the endpoint
// this asserts it on. The contract's `getPortalStatus` declares `application/json`
// and is a different thing: it is what the connected page renders, not what an
// operating system polls.
//
// Asserted together, because "the portal answers captive+json" is true of one of
// these routes and false of the other, and a wrong media type is a check that
// passes without checking anything.
func TestTheStatusEndpointAndTheCaptivePortalAPIAnswerDifferentMediaTypes(t *testing.T) {
	v := newVenue(t)
	v.arrive(v.guestMAC)

	resp := v.api(http.MethodPost, "/portal/voucher", url.Values{"code": {v.code}, "lang": {"en"}})
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("redeeming: %d", resp.StatusCode)
	}

	status := v.api(http.MethodGet, "/portal/status", nil)
	defer status.Body.Close()
	if status.StatusCode != http.StatusOK {
		t.Fatalf("GET /portal/status answered %d, want 200", status.StatusCode)
	}
	if ct := status.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type is %q, want application/json — that is what the contract declares "+
			"for getPortalStatus", ct)
	}
	var out gen.PortalStatus
	if err := json.NewDecoder(status.Body).Decode(&out); err != nil {
		t.Fatalf("decoding the status: %v", err)
	}
	if out.State != gen.PortalStatusStateConnected {
		t.Errorf("state is %q, want connected", out.State)
	}
	if out.Plan.EnforcedBy != gen.PlanEnforcedByNas {
		t.Errorf("enforcedBy is %q, want nas — it is read from the grant, not re-derived",
			out.Plan.EnforcedBy)
	}
	if out.VenueName == nil || *out.VenueName != "The Portal Venue" {
		t.Errorf("venueName is %v — the success page must welcome the guest to the business "+
			"they consented to", out.VenueName)
	}
	if out.OriginalUrl == nil || *out.OriginalUrl != "http://example.test/news" {
		t.Errorf("originalUrl is %v, want the allowlisted destination", out.OriginalUrl)
	}

	capport := v.send(http.MethodGet, "/api/v1/captive-portal", nil)
	defer capport.Body.Close()
	if ct := capport.Header.Get("Content-Type"); ct != portal.MediaTypeCaptiveJSON {
		t.Errorf("the RFC 8908 endpoint answered %q, want %q", ct, portal.MediaTypeCaptiveJSON)
	}
}

// ── the binding, which is never re-pointed ───────────────────────────────────

// TestASessionIsNeverRePointedAtADifferentMAC.
//
// A request carrying a session for one address and NAS parameters naming another
// does not update the row: the old session is forgotten and a new one is minted,
// which then has to pass the presence check on its own.
//
// The narrow claim is the true one. Nothing stops somebody deleting their own
// cookie and starting again, so this is not a barrier to *trying* a second MAC.
// What it prevents is a grant issued after a presence check on one address being
// carried over to another.
func TestASessionIsNeverRePointedAtADifferentMAC(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)
	v.arrive(v.guestMAC)

	var firstHash string
	if err := v.owner.QueryRow(ctx,
		`SELECT token_hash FROM portal_sessions WHERE client_mac = $1::macaddr`,
		portal.NormaliseMAC(v.guestMAC)).Scan(&firstHash); err != nil {
		t.Fatalf("reading the first session: %v", err)
	}

	// The same browser, the same cookie, a different MAC in the query string.
	stranger := strings.TrimSuffix(v.guestMAC, "02") + "ee"
	v.arrive(stranger)

	var stillThere int
	if err := v.owner.QueryRow(ctx,
		`SELECT count(*) FROM portal_sessions WHERE token_hash = $1`, firstHash).Scan(&stillThere); err != nil {
		t.Fatalf("re-reading the first session: %v", err)
	}
	if stillThere != 0 {
		t.Error("the original binding survived a request naming a different MAC — it must be forgotten, not re-pointed")
	}

	var boundTo string
	if err := v.owner.QueryRow(ctx,
		`SELECT client_mac::text FROM portal_sessions WHERE site_id = $1`, v.site).Scan(&boundTo); err != nil {
		t.Fatalf("reading the new session: %v", err)
	}
	if boundTo != portal.NormaliseMAC(stranger) {
		t.Errorf("the new session is bound to %q, want %q", boundTo, portal.NormaliseMAC(stranger))
	}

	// The new session has not been confirmed by any driver, so it grants nothing.
	resp := v.api(http.MethodPost, "/portal/voucher", url.Values{"code": {v.code}, "lang": {"en"}})
	resp.Body.Close()
	if rows := v.grants(ctx); len(rows) != 0 {
		t.Fatalf("portal_grants holds %d rows for a MAC that inherited a binding", len(rows))
	}
}

// ── an expired session is a session that never existed ───────────────────────

// TestAnExpiredSessionIsIndistinguishableFromAnUnknownToken.
//
// The expiry is in the repository's WHERE clause, so the two are one answer by
// construction rather than by a comparison somebody remembered to write. A caller
// that could tell them apart could tell whether a token was ever valid.
func TestAnExpiredSessionIsIndistinguishableFromAnUnknownToken(t *testing.T) {
	ctx := context.Background()
	v := newVenue(t)
	v.arrive(v.guestMAC)

	if _, err := v.owner.Exec(ctx,
		`UPDATE portal_sessions SET expires_at = now() - interval '1 minute' WHERE client_mac = $1::macaddr`,
		portal.NormaliseMAC(v.guestMAC)); err != nil {
		t.Fatalf("ageing the session: %v", err)
	}

	// The cookie still resolves to a row that exists. The repository must answer
	// not-found anyway.
	if _, err := v.st.Grants().Session(ctx, portal.HashToken(v.jar[0].Value), time.Now()); err == nil {
		t.Fatal("an expired session resolved")
	} else if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an expired session answered %v, want the same not-found an unknown token gets", err)
	}

	// And through the route: no session means no grant, and the guest is told the
	// same thing a browser that never arrived is told.
	status := v.api(http.MethodGet, "/portal/status", nil)
	status.Body.Close()
	if status.StatusCode != http.StatusNotFound {
		t.Errorf("GET /portal/status answered %d for an expired session, want 404", status.StatusCode)
	}
}

// A limited plan on a NAS a probe found no accounting on is stored `unenforced`.
//
// Over PostgreSQL rather than over a fake, because the fact that makes the third
// value sayable is a column: `radius_clients.accounting_configured`, written by the
// NAS probe, three-valued so that *not measured* is not *not configured*. Without
// it, `enforcement` would have two inputs and a limited plan behind a soft-quota
// driver would always be `foyer-sampled` — the portal telling a guest their allowance was being sampled every five minutes on a device
// from which no byte counter will ever arrive.
func TestALimitWithNoAccountingIsStoredUnenforced(t *testing.T) {
	ctx := context.Background()

	for _, tc := range []struct {
		name       string
		configured *bool
		want       string
	}{
		{"a probe found accounting configured", ptr(true), "foyer-sampled"},
		{"a probe found none", ptr(false), "unenforced"},
		{"nobody has probed it", nil, "foyer-sampled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := newVenue(t)
			// Soft quota, so accounting is the only thing that could be applying
			// the plan's limits.
			v.driver.WithCapabilities(func(c *nas.Capabilities) { c.HardQuota = false })

			if _, err := v.owner.Exec(ctx,
				`UPDATE radius_clients SET accounting_configured = $2 WHERE id = $1`,
				v.clientID, tc.configured); err != nil {
				t.Fatalf("writing the probe's measurement: %v", err)
			}

			v.arrive(v.guestMAC)
			resp := v.api(http.MethodPost, "/portal/voucher", url.Values{"code": {v.code}, "lang": {"en"}})
			resp.Body.Close()

			rows := v.grants(ctx)
			if len(rows) != 1 {
				t.Fatalf("portal_grants holds %d rows, want 1", len(rows))
			}
			if rows[0].Duration == nil && rows[0].Allowance == nil {
				t.Fatal("the granted plan states no limit, so this case cannot distinguish anything")
			}
			if rows[0].EnforcedBy != tc.want {
				t.Errorf("enforced_by is %q, want %q", rows[0].EnforcedBy, tc.want)
			}
		})
	}
}
