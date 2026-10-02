package apistore_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/apistore"
	"github.com/kiennt048/foyer/internal/nas"
	"github.com/kiennt048/foyer/internal/store"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The adapter is tested against a real PostgreSQL and a real HTTP server, and it is
// worth saying why neither half is optional.
//
// **Not against a fake store**, because the fake is `internal/api/memory` and it
// already passes: the whole risk of an adapter is that it type-checks and does
// nothing, and a fake would agree with whatever this code believes about SQL. Row
// scoping, the cursor, the LEFT JOIN behind the `Nas` read model and the
// SELECT-only grant on `radacct` are properties of PostgreSQL.
//
// **Not against the ports directly**, because the ports are half the seam. Driving
// the contract over HTTP is what exercises the gates, the CSRF requirement, the
// envelopes and the problem documents at the same time — and an operation wired to
// the wrong port answers `501`, which a direct call would never reveal.

var (
	ownerURL     string
	dockerAbsent string
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("foyer"),
		postgres.WithUsername("foyer"),
		postgres.WithPassword(randomString()),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		// Docker genuinely absent. Every test below reports why it could not run
		// rather than passing silently: an unproven guarantee has to look
		// different from a proven one.
		dockerAbsent = err.Error()
		os.Exit(m.Run())
	}
	ownerURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}
	if err := store.Migrate(ctx, ownerURL); err != nil {
		panic(err)
	}

	code := m.Run()
	_ = testcontainers.TerminateContainer(ctr)
	os.Exit(code)
}

func requireDocker(t *testing.T) {
	t.Helper()
	if dockerAbsent != "" {
		t.Skipf("PostgreSQL is unavailable, so this guarantee is UNPROVEN on this machine: %s", dockerAbsent)
	}
}

// randomString is hex, not base64url.
//
// **base64url makes every test building a hostname out of it a 27% flake** — red
// in one run of `make check-all` and green three runs earlier, which is the
// signature of a fixture rather than a defect. `base64.RawURLEncoding`'s alphabet
// carries `_`, which is legal in a base64 string and illegal in a hostname — so
// `uniq("scope") + ".example"` would be a valid FQDN about three times in four and
// `422 not-a-hostname` the rest of the time. Ten characters from a 64-symbol
// alphabet containing two illegal ones: 1 − (62/64)¹⁰ ≈ 0.27.
//
// Hex has no illegal character and no leading-hyphen case, so every caller — ids,
// labels, hostnames — gets a value that is valid everywhere rather than valid in
// the places somebody happened to test. A fixture generator that is *usually* legal
// is worse than one that is never legal: the second fails immediately and the first
// fails on somebody else's branch, weeks later, looking like their change.
func randomString() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func uniq(prefix string) string { return prefix + "-" + strings.ToLower(randomString()[:10]) }

// worldSeq gives each world its own address and MAC space.
//
// The container and its schema are shared, which is what keeps the suite under a
// minute — and `radius_clients` is `UNIQUE (tenant_id, address)` while
// `access_points` is `UNIQUE (tenant_id, mac)`, so two worlds sharing one address is
// two tests fighting over one row rather than two tests.
var worldSeq atomic.Int64

// ── the world ────────────────────────────────────────────────────────────────

// world is what one test seeded, named so assertions read as sentences.
type world struct {
	t *testing.T

	st      *store.Store
	owner   *pgxpool.Pool
	adapter *apistore.Adapter
	server  *httptest.Server

	siteA, siteB string
	// The device that answers, the one that refuses Message-Authenticator, and
	// the one nothing can reach. Ugly on purpose: a tidy fixture validates a world
	// that does not exist.
	nasOK, nasRefusing, nasUnreachable string
	// The controller behind `nasOK`. One device in this world has one, because
	// without it every driver-gated path in the adapter returns before it
	// reaches a driver — `probeNas` takes the no-controller path and
	// `SecretRotation` short-circuits, so the capability values the fake reports
	// would have no route into the product code at all.
	controllerID string
	// The same three devices, by the id the console knows them by: the access
	// point's, not the clients.conf row's. See `ListNas` in
	// internal/store/radiusclients.go.
	deviceOK, deviceRefusing, deviceUnreachable string
	batchA                                      string
	voucherInUse                                string
	voucherCode                                 string
	sessionOnline                               string
	sessionOrphaned                             string
	sessionWrapped                              string
	adminID, adminUser, adminPassword           string
	csrf                                        string
	jar                                         map[string]string
	// The second server's cookies, when a test drives the oracle beside the
	// adapter. See oracle_test.go.
	oracleJar map[string]string

	// The address block this world owns, and how many devices it has taken from
	// it. See worldSeq.
	block string
	hosts int
}

// nextAddr allocates this world's next device address.
func (w *world) nextAddr() string {
	w.hosts++
	return w.block + strconv.Itoa(10+w.hosts)
}

// macFor is the access-point MAC for one of this world's devices, in the same
// per-world space as its addresses.
func (w *world) macFor(host int) string {
	parts := strings.Split(strings.TrimSuffix(w.block, "."), ".")
	b, _ := strconv.Atoi(parts[1])
	c, _ := strconv.Atoi(parts[2])
	return fmt.Sprintf("94:2a:6f:%02x:%02x:%02x", b%256, c%256, (10+host)%256)
}

// newWorld migrates nothing and seeds everything: the schema is already applied by
// TestMain, and every row here is written through the repositories the adapter uses,
// except the accounting rows — which are written as the schema owner, because
// `foyer_app` holds SELECT on `radacct` and nothing else and the database is what
// enforces that rather than this file.
func newWorld(t *testing.T) *world { return newWorldWith(t, nil) }

// newWorldWith is newWorld with one hand on the adapter's configuration.
//
// It exists for exactly one kind of assertion: *this path did not go through that
// seam*. A negative like that is only worth anything beside a positive taken from
// the same harness, so the test that asserts a UniFi device never reaches the
// per-NAS driver also asserts that a device with no controller does — and both need
// the same recording fake in the same world.
func newWorldWith(t *testing.T, tune func(*apistore.Config)) *world {
	t.Helper()
	requireDocker(t)

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	kr, err := store.NewKeyring("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), store.Config{URL: ownerURL, Keys: kr, MaxConns: 6})
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(st.Close)

	n := worldSeq.Add(1)
	w := &world{t: t, st: st, owner: ownerPool(t), block: fmt.Sprintf("10.%d.%d.", 100+n/250, n%250)}

	cfg := apistore.Config{
		Store: st,
		// A fake behind the driver seam, and it is labelled one: nothing in this
		// suite is evidence about what a UniFi controller does.
		Drivers: fakeDrivers{},
	}
	if tune != nil {
		tune(&cfg)
	}
	adapter, err := apistore.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	w.adapter = adapter

	srv, err := api.New(api.Config{Ports: adapter.Ports()})
	if err != nil {
		t.Fatal(err)
	}
	w.server = httptest.NewServer(srv.Handler())
	t.Cleanup(w.server.Close)

	w.seed()
	return w
}

// newFreshInstall is a world that has been migrated and first-booted and nothing
// more: an administrator who can sign in, and **no sites**.
//
// **It gets a tenant of its own, and that is the whole trick.** Every other world
// in this package shares one PostgreSQL schema, so "this deployment has no sites"
// is not expressible there — `w.seed()` creates two, and so does every sibling
// test that ran first. Row-level security is what makes it expressible: the store
// runs as `foyer_app`, every policy compares `tenant_id = foyer_current_tenant()`,
// and `createSite`'s `WHERE NOT EXISTS (SELECT 1 FROM sites)` therefore asks about
// THIS deployment rather than about the container.
//
// It exists because of two facts together: the first site has to be creatable and
// usable through the contract, and `createSite` refuses a second call (one venue
// per install) — which turns "a world that always has a site" from a blind spot
// into a world where the `201` cannot be reached at all.
func newFreshInstall(t *testing.T) *world {
	t.Helper()
	requireDocker(t)

	owner := ownerPool(t)
	var tenant string
	if err := owner.QueryRow(context.Background(), `
		INSERT INTO tenants (slug, name) VALUES ($1, $1) RETURNING id::text`,
		uniq("fresh")).Scan(&tenant); err != nil {
		t.Fatalf("creating the tenant for a fresh install: %v", err)
	}

	// A neighbouring deployment, so that *this install lists no sites* is a claim
	// about scoping rather than a claim about an empty container — true even when
	// this test runs alone. It is NOT what catches a lost `TenantID`; the assertion
	// below is, and the note there says why this was not enough.
	if _, err := owner.Exec(context.Background(), `
		WITH neighbour AS (
			INSERT INTO tenants (slug, name) VALUES ($1, $1) RETURNING id
		)
		INSERT INTO sites (id, tenant_id, name, environment)
		SELECT $2, neighbour.id, 'Another operator''s venue', 'live' FROM neighbour`,
		uniq("neighbour"), uniq("site")); err != nil {
		t.Fatalf("seeding the neighbouring deployment: %v", err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	kr, err := store.NewKeyring("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(context.Background(), store.Config{
		URL: ownerURL, Keys: kr, MaxConns: 4, TenantID: tenant,
	})
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(st.Close)

	// ── the world asserts its own isolation ──────────────────────────────────
	//
	// Asserting it indirectly — *a fresh install lists no sites* — is not enough: a
	// sabotage that deletes `TenantID` above leaves that **green** when run with
	// `-run TestASiteCreated`, because no sibling has seeded a site, so the default
	// tenant is empty too and the mis-scoped world sees the same empty list.
	// Isolated-green and in-suite-red, which `docs/rules/testing.md` names as a
	// timing signature and which here means the world cannot express the property
	// it exists for.
	//
	// Seeding a neighbouring deployment does not fix it either: an unscoped store
	// falls back to the DEFAULT tenant, so a neighbour in a tenant of its own is
	// exactly the row it still cannot see. Seeding into the default tenant would
	// work and would write a site every sibling test in this package can see, to
	// prove something about this one.
	//
	// So it asks the database what tenant this pool is acting for, using the very
	// function every row-level policy in the schema compares against. Derived from
	// the authority rather than inferred from a symptom — and a world that loses its
	// scoping is red on the first line, in any order, with nothing seeded anywhere.
	var acting string
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT coalesce(foyer_current_tenant()::text, '')`).Scan(&acting); err != nil {
		t.Fatalf("asking which tenant this world acts for: %v", err)
	}
	if acting != tenant {
		t.Fatalf("this world acts for tenant %q, not the fresh one (%q).\n"+
			"Row-level security is the only thing making \"this deployment has no sites\" true, so\n"+
			"every assertion built on it is now about whatever else is in this container.", acting, tenant)
	}

	n := worldSeq.Add(1)
	w := &world{t: t, st: st, owner: owner, block: fmt.Sprintf("10.%d.%d.", 100+n/250, n%250)}

	adapter, err := apistore.New(apistore.Config{Store: st, Drivers: fakeDrivers{}})
	if err != nil {
		t.Fatal(err)
	}
	w.adapter = adapter

	srv, err := api.New(api.Config{Ports: adapter.Ports()})
	if err != nil {
		t.Fatal(err)
	}
	w.server = httptest.NewServer(srv.Handler())
	t.Cleanup(w.server.Close)

	// The one thing a fresh install DOES have. `deploy/first-boot.sh` creates the
	// administrator before anything else, and a deployment nobody can sign in to
	// could not reach `createSite` at all.
	w.adminUser = uniq("admin")
	w.adminPassword = randomString() + "Aa1"
	op, err := adapter.CreateOperator(context.Background(), gen.OperatorRequest{
		Username: w.adminUser, Password: w.adminPassword, Role: gen.Administrator,
	})
	if err != nil {
		t.Fatalf("creating the operator: %v", err)
	}
	w.adminID = op.Id
	return w
}

func ownerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 3
	// Scoped, but never de-privileged: this pool stands in for FreeRADIUS, which
	// is the only thing that may write accounting.
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SELECT set_config('foyer.tenant_id', $1, false)`, store.DefaultTenantID)
		return err
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func (w *world) seed() {
	ctx := context.Background()
	t := w.t

	w.siteA = uniq("site")
	w.siteB = uniq("site")
	for _, id := range []string{w.siteA, w.siteB} {
		if err := w.st.Sites().Create(ctx, &store.Site{ID: id, Name: "Venue " + id, Environment: "lab"}); err != nil {
			t.Fatalf("creating site: %v", err)
		}
	}

	// An operator with a real argon2id verifier, created through the adapter so
	// that the sign-in path under test is the one the console uses.
	w.adminUser = uniq("admin")
	w.adminPassword = randomString() + "Aa1"
	op, err := w.adapter.CreateOperator(ctx, gen.OperatorRequest{
		Username: w.adminUser,
		Password: w.adminPassword,
		Role:     gen.Administrator,
	})
	if err != nil {
		t.Fatalf("creating the operator: %v", err)
	}
	w.adminID = op.Id

	w.controllerID = uniq("ctl")
	ctl := &store.Controller{
		ID: w.controllerID, Name: "Controller " + w.controllerID, Vendor: "unifi",
		BaseURL: "https://192.0.2.10:8443", AuthMode: "api-key", TLSMode: "trust-on-first-use",
	}
	if err := w.st.Controllers().Create(ctx, ctl, []byte(randomString())); err != nil {
		t.Fatalf("creating the controller: %v", err)
	}

	w.nasOK = w.seedNas(w.siteA, "Lobby AP", w.nextAddr(), store.Capabilities{
		WalledGarden: "fqdn-sni", HardQuota: true, BandwidthLimits: true,
	}, "ok", "enforced", ptr(true))
	// Attached in SQL because RadiusClientPatch carries no controller — the same
	// direct write this harness already uses for access points and radpostauth.
	if _, err := w.owner.Exec(ctx,
		`UPDATE radius_clients SET controller_id = $2 WHERE id = $1`, w.nasOK, w.controllerID); err != nil {
		t.Fatalf("attaching the controller: %v", err)
	}
	// Refuses Message-Authenticator: left open to BlastRADIUS, and the alarm is
	// keyed on the observed value rather than the configured one.
	w.nasRefusing = w.seedNas(w.siteA, "Back office AP", w.nextAddr(), store.Capabilities{
		WalledGarden: "none",
	}, "ok", "refused", ptr(false))
	// Unreachable and never measured: rotation is refused on it, and its
	// accounting figures are absent rather than zero.
	w.nasUnreachable = w.seedNas(w.siteB, "Warehouse AP", w.nextAddr(), store.Capabilities{
		WalledGarden: "ip-range",
	}, "unreachable", "unknown", nil)

	w.seedVouchers()
	w.seedAccounts()
	w.seedPortalAndGarden()
	w.seedAccounting()
}

func (w *world) seedNas(siteID, name, addr string, caps store.Capabilities, reach, msgAuth string, accounting *bool) string {
	ctx := context.Background()
	ss := &store.SharedSecret{ID: uniq("sec"), SiteID: siteID, Label: name}
	if err := w.st.SharedSecrets().Create(ctx, ss, []byte(randomString())); err != nil {
		w.t.Fatalf("creating a shared secret: %v", err)
	}
	prefix := netip.MustParsePrefix(addr + "/32")
	c := &store.RadiusClient{
		ID: uniq("rc"), SiteID: siteID, Name: name, Address: prefix,
		Source: "manual", Driver: "unifi", SharedSecretID: ptr(ss.ID),
		RequireMsgAuth: true, LimitProxyState: true,
		MessageAuthState: msgAuth, AccountingConfigured: accounting,
		Reachability: reach, Capabilities: caps, Enabled: true,
	}
	if err := w.st.RadiusClients().Create(ctx, c); err != nil {
		w.t.Fatalf("creating a radius client: %v", err)
	}
	return c.ID
}

func (w *world) seedVouchers() {
	ctx := context.Background()
	b, err := w.adapter.CreateBatch(ctx, gen.VoucherBatchCreate{
		SiteId: w.siteA, Label: "Reception " + uniq("b"), Count: 4,
		ExpiryMode: gen.FromFirstUse,
		Plan:       gen.PlanRequest{DurationMinutes: ptr(120), DataAllowanceMb: ptr(500)},
	}, w.adminUser)
	if err != nil {
		w.t.Fatalf("creating a voucher batch: %v", err)
	}
	w.batchA = b.Id

	page, err := w.adapter.Vouchers(ctx, api.VoucherQuery{SiteID: w.siteA, BatchID: b.Id, Window: api.Window{Limit: 10}})
	if err != nil || len(page.Items) == 0 {
		w.t.Fatalf("reading back the batch: %v", err)
	}
	w.voucherInUse = page.Items[0].Id
	w.voucherCode = page.Items[0].Code

	// Redeemed, so that revoking it has a session to end. The store's own Redeem
	// is what claims it, transactionally, the way the portal will.
	if _, err := w.st.Vouchers().Redeem(ctx, w.voucherCode, w.block+"online", time.Now(), nil); err != nil {
		w.t.Fatalf("redeeming: %v", err)
	}

	// One batch in the other site, so that every site-scoped assertion has
	// something it must not see.
	if _, err := w.adapter.CreateBatch(ctx, gen.VoucherBatchCreate{
		SiteId: w.siteB, Label: "Warehouse " + uniq("b"), Count: 2,
		ExpiryMode: gen.FromFirstUse, Plan: gen.PlanRequest{DurationMinutes: ptr(30)},
	}, w.adminUser); err != nil {
		w.t.Fatalf("creating the second batch: %v", err)
	}
}

func (w *world) seedAccounts() {
	ctx := context.Background()
	for _, s := range []string{w.siteA, w.siteB} {
		if _, err := w.adapter.CreateAccount(ctx, gen.AccountCreate{
			SiteId: s, Username: uniq("guest"), Password: randomString() + "Aa1",
		}); err != nil {
			w.t.Fatalf("creating an account: %v", err)
		}
	}
}

func (w *world) seedPortalAndGarden() {
	ctx := context.Background()
	for _, s := range []string{w.siteA, w.siteB} {
		if err := w.st.Portal().Upsert(ctx, &store.PortalSettings{
			SiteID: s, VenueName: "Venue " + s,
			Languages: []string{"en", "vi"}, Methods: []string{"voucher"},
			TermsSummary: map[string]string{"en": "Be nice."},
		}); err != nil {
			w.t.Fatalf("seeding portal settings: %v", err)
		}
		// A `required` entry, which the operator may not delete: without the
		// portal's own name the portal cannot serve itself.
		if err := w.st.Garden().Create(ctx, &store.GardenEntry{
			ID: uniq("garden"), SiteID: s, Kind: "fqdn", Value: "portal." + s + ".example", Origin: "required",
		}); err != nil {
			w.t.Fatalf("seeding a required garden entry: %v", err)
		}
	}
}

// seedAccounting writes `radacct` and `radpostauth` as the schema owner.
//
// It cannot be written any other way, and that is the point: `foyer_app` holds
// `SELECT` on both tables and nothing else, so an INSERT from the application role
// is refused by PostgreSQL rather than by a code review.
func (w *world) seedAccounting() {
	ctx := context.Background()
	now := time.Now()

	w.sessionOnline = w.block + "online"
	w.sessionOrphaned = w.block + "orphaned"
	w.sessionWrapped = w.block + "wrapped"

	lobby, backOffice, warehouse := w.macFor(1), w.macFor(2), w.macFor(3)
	rows := []struct {
		unique, session, username, nasIP, calling, called string
		start, update                                     time.Time
		stop                                              *time.Time
		in, out                                           int64
		interval                                          int64
	}{
		// Online, healthy, and the one the redeemed voucher is being used for.
		{w.sessionOnline, "a-1", w.voucherCode, w.block + "11", "aa:bb:cc:dd:ee:01", lobby + ":Guest",
			now.Add(-30 * time.Minute), now.Add(-2 * time.Minute), nil, 1 << 20, 2 << 20, 300},
		// **No `Acct-Stop`, forty days.** The classic RADIUS accounting failure:
		// the NAS rebooted and nothing reaped it, so it stays open forever.
		{w.sessionOrphaned, "a-2", "someone", w.block + "12", "02:11:22:33:44:55", backOffice + ":Guest",
			now.Add(-40 * 24 * time.Hour), now.Add(-40 * 24 * time.Hour), nil, 4096, 4096, 300},
		// **A wrapped counter.** The reconstructed total is past 2^32, so the
		// reported counter and the total disagree and `countersWrapped` says why.
		{w.sessionWrapped, "a-3", "someone-else", w.block + "11", "02:aa:bb:cc:dd:ee", lobby + ":Guest",
			now.Add(-3 * time.Hour), now.Add(-time.Minute), nil, (1 << 32) + 12345, 7 << 20, 300},
		// In the other site entirely.
		{w.block + "other-site", "b-1", "warehouse-guest", w.block + "13", "02:99:88:77:66:55", warehouse + ":Staff",
			now.Add(-time.Hour), now.Add(-5 * time.Minute), nil, 512, 512, 300},
	}
	for _, r := range rows {
		if _, err := w.owner.Exec(ctx, `
			INSERT INTO radacct (acctuniqueid, acctsessionid, username, nasipaddress,
			                     callingstationid, calledstationid, acctstarttime,
			                     acctupdatetime, acctstoptime, acctinputoctets,
			                     acctoutputoctets, acctinterval)
			VALUES ($1,$2,$3,$4::inet,$5,$6,$7,$8,$9,$10,$11,$12)`,
			r.unique, r.session, r.username, r.nasIP, r.calling, r.called,
			r.start, r.update, r.stop, r.in, r.out, r.interval); err != nil {
			w.t.Fatalf("seeding radacct: %v", err)
		}
	}

	// An Access-Request from an address in no `clients.conf` entry: FreeRADIUS
	// discards it without a reply, so the guest sees a network that does not work
	// and Foyer sees nothing — unless it counts the address.
	if _, err := w.owner.Exec(ctx, `
		INSERT INTO radacct (acctuniqueid, acctsessionid, username, nasipaddress, acctstarttime)
		VALUES ($1, 'x-1', 'nobody', '192.0.2.77'::inet, now())`, w.block+"unmatched"); err != nil {
		w.t.Fatalf("seeding an unmatched session: %v", err)
	}

	attempts := []struct {
		username, reply, calling, called string
	}{
		{w.voucherCode, "Access-Accept", "aa:bb:cc:dd:ee:01", lobby + ":Guest"},
		{"wrong-code", "Access-Reject", "aa:bb:cc:dd:ee:02", lobby + ":Guest"},
		// Site B's, and the row a site-scoped query must never return.
		{"warehouse-guest", "Access-Accept", "02:99:88:77:66:55", warehouse + ":Staff"},
	}
	for _, at := range attempts {
		if _, err := w.owner.Exec(ctx, `
			INSERT INTO radpostauth (username, reply, callingstationid, calledstationid)
			VALUES ($1,$2,$3,$4)`, at.username, at.reply, at.calling, at.called); err != nil {
			w.t.Fatalf("seeding radpostauth: %v", err)
		}
	}

	// The access points the called-station ids resolve to. Discovered rather than
	// typed, which is the only way an access point may exist.
	aps := []struct{ mac, name, site, client string }{
		{lobby, "Lobby AP radio", w.siteA, w.nasOK},
		{backOffice, "Back office radio", w.siteA, w.nasRefusing},
		{warehouse, "Warehouse radio", w.siteB, w.nasUnreachable},
	}
	for _, ap := range aps {
		id := "ap-" + strings.ReplaceAll(ap.mac, ":", "")
		if _, err := w.owner.Exec(ctx, `
			INSERT INTO access_points (id, site_id, mac, name, radius_client_id, state)
			VALUES ($1,$2,$3::macaddr,$4,$5,'online')`,
			id, ap.site, ap.mac, ap.name, ap.client); err != nil {
			w.t.Fatalf("seeding an access point: %v", err)
		}
		switch ap.client {
		case w.nasOK:
			w.deviceOK = id
		case w.nasRefusing:
			w.deviceRefusing = id
		case w.nasUnreachable:
			w.deviceUnreachable = id
		}
	}
}

// ── driving the contract over HTTP ───────────────────────────────────────────

// signIn drives the real sign-in and keeps the cookie and the CSRF token.
//
// Through the API rather than by minting a session row, because the point is that
// the sign-in path works end to end: an argon2id verifier the adapter wrote, a
// session row the adapter stored, a cookie the API set, and a CSRF token the
// console has to echo.
func (w *world) signIn() {
	w.t.Helper()
	body := w.do(http.MethodPost, "/auth/session", map[string]any{
		"username": w.adminUser, "password": w.adminPassword,
	}, http.StatusOK)
	var session gen.AuthSession
	if err := json.Unmarshal(body, &session); err != nil {
		w.t.Fatalf("reading the sign-in response: %v", err)
	}
	w.csrf = session.CsrfToken
	if w.csrf == "" {
		w.t.Fatal("signed in with no CSRF token, so no state-changing request can be made")
	}
}

func (w *world) url(path string) string { return w.server.URL + api.BasePath + path }

// do issues one request with the session cookie and the CSRF header, and asserts
// the status.
func (w *world) do(method, path string, body any, wantStatus int) []byte {
	w.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			w.t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	req, err := http.NewRequest(method, w.url(path), reader)
	if err != nil {
		w.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if w.csrf != "" {
		req.Header.Set("X-Foyer-CSRF", w.csrf)
	}
	if w.jar == nil {
		w.jar = map[string]string{}
	}
	for name, value := range w.jar {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}

	resp, err := w.server.Client().Do(req)
	if err != nil {
		w.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Value == "" {
			delete(w.jar, c.Name)
			continue
		}
		w.jar[c.Name] = c.Value
	}
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		w.t.Fatalf("%s %s: got %d, want %d: %s", method, path, resp.StatusCode, wantStatus, truncate(raw))
	}
	return raw
}

func (w *world) getJSON(path string, into any) {
	w.t.Helper()
	raw := w.do(http.MethodGet, path, nil, http.StatusOK)
	if err := json.Unmarshal(raw, into); err != nil {
		w.t.Fatalf("GET %s: %v: %s", path, err, truncate(raw))
	}
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func truncate(b []byte) string {
	if len(b) > 600 {
		return string(b[:600]) + "…"
	}
	return string(b)
}

// ── the fake behind the driver seam ──────────────────────────────────────────

// fakeDrivers is a fake and is labelled one.
//
// `docs/rules/nas-drivers.md` is explicit that a hand-written NAS mock encodes
// the author's assumptions — the same assumptions that produced the bug — so
// nothing in this suite is evidence about what a UniFi controller does. What it is
// evidence about is everything on this side of the seam.
type fakeDrivers struct{}

func (fakeDrivers) ForController(c store.Controller, _ []byte, _ string) (nas.NASDriver, error) {
	d := nas.NewMockDriver(nas.VendorUniFi)
	if c.Reachability == "unreachable" {
		// Mutated rather than replaced. The mock derives `Warnings` and
		// `Unverified` from the shipped driver, and a fresh `ProbeResult` literal
		// here would drop both — a fake quietly answering that an unreachable
		// controller has no caveats, which is a fake encoding its author's
		// assumption, one field along.
		d.ProbeOutcome.Reachable = false
		d.ProbeOutcome.Errors = []string{"dial: no route"}
	}
	return d, nil
}

func ptr[T any](v T) *T { return &v }
