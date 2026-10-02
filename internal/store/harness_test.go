package store_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/store"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// Tier-1 store tests run against a real PostgreSQL 16.
//
// Not against a fake, and the reason is the point of this whole package: row-level
// security and a SELECT-only role are properties of PostgreSQL. A fake would agree
// with whatever this code believes about them, which is exactly the belief under
// test.

// Every test gets a world of its own: a database cloned from a migrated template.
//
// The container is shared, because starting PostgreSQL is what costs; the database
// is not. With a shared database, `go test ./internal/store -count=2` fails on the
// second iteration with `radius_clients_tenant_id_address_key` and its siblings,
// because tests seed fixed addresses, names and codes on purpose -- a
// longest-prefix test is about 10.0.0.0/8, not about a random one -- and the
// first iteration's rows are still there. The same collision is what any two
// tests picking the same fixture would hit in one run, in whichever order the
// files happened to sort. `uniq` hides it for identifiers; it cannot for a value the
// test is *about*.
//
// So TestMain migrates `foyer_template` once, closes it to connections, and the first
// helper a test calls -- appStore, storeAsRole, ownerPool, ownerURL, appURL,
// radiusURL, productStore, radiusConn -- clones it for that test with
// `CREATE DATABASE … TEMPLATE`, which copies files rather than replaying migrations.
// The clone is dropped in t.Cleanup.
//
// What a clone does **not** copy, and why that is fine: roles and their passwords
// are cluster-wide (foyer_app, foyer_radius, foyer_reaper, foyer_retention,
// foyer_credential_view), so the logins TestMain provisions work in every world,
// and a test that sets a role's password changes it for all of them, exactly as it
// did before. Database-level settings (`ALTER DATABASE … SET`) and database ACLs are
// not copied; no migration uses either, and a migration that starts to would show
// up here as a test that passes in production and fails in its clone.
//
// The world belongs to the top-level test, found by the first segment of t.Name(),
// so a test and its subtests see one database. Its lifetime is tied to whichever
// *testing.T asked first: open it in the parent when sibling subtests must share
// rows.

const templateDB = "foyer_template"

var (
	// clusterURL is the container's maintenance database, `postgres`. Nothing is
	// migrated there; it is where CREATE and DROP DATABASE run from.
	clusterURL   string
	adminPool    *pgxpool.Pool
	dockerAbsent string

	// The login roles' passwords, generated for this run and set the way
	// `foyerctl migrate` sets them. Roles are cluster-wide, so one password serves
	// every world. They are how the product connects since migration 00019.
	appPassword    string
	radiusPassword string

	worldsMu sync.Mutex
	worlds   = map[string]string{}
	worldSeq atomic.Int64
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	pw := randomString()
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("foyer"),
		postgres.WithPassword(pw),
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

	clusterURL, err = ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}
	adminPool, err = pgxpool.New(ctx, clusterURL)
	if err != nil {
		panic(err)
	}
	if _, err := adminPool.Exec(ctx, `CREATE DATABASE `+templateDB); err != nil {
		panic(err)
	}
	tmpl := dbURL(templateDB)
	if err := store.Migrate(ctx, tmpl); err != nil {
		panic(err)
	}
	appPassword = provisionLogin(ctx, tmpl, store.DefaultAppRole)
	radiusPassword = provisionLogin(ctx, tmpl, store.RadiusRole)
	// Closed to connections, so a stray one can never make a clone fail with
	// "source database is being accessed by other users" -- or, worse, leave a
	// row in the template that every later world inherits.
	if _, err := adminPool.Exec(ctx, `ALTER DATABASE `+templateDB+` WITH IS_TEMPLATE true ALLOW_CONNECTIONS false`); err != nil {
		panic(err)
	}

	code := m.Run()
	adminPool.Close()
	_ = testcontainers.TerminateContainer(ctr)
	os.Exit(code)
}

// world returns the name of the database this test's top-level test owns, cloning
// it from the template on first use.
func world(t *testing.T) string {
	t.Helper()
	requireDocker(t)
	root, _, _ := strings.Cut(t.Name(), "/")

	worldsMu.Lock()
	defer worldsMu.Unlock()
	if name, ok := worlds[root]; ok {
		return name
	}
	// The name is a counter, so it cannot contain a quote; CREATE DATABASE takes
	// no bind parameter.
	name := fmt.Sprintf("t_%d", worldSeq.Add(1))
	if _, err := adminPool.Exec(context.Background(), `CREATE DATABASE `+name+` TEMPLATE `+templateDB); err != nil {
		t.Fatalf("cloning a world for this test: %v", err)
	}
	worlds[root] = name
	// Registered before any pool the test opens, so it runs after all of them
	// have closed. FORCE covers a connection the test leaked.
	t.Cleanup(func() {
		worldsMu.Lock()
		delete(worlds, root)
		worldsMu.Unlock()
		_, _ = adminPool.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	})
	return name
}

// ownerURL logs in as the schema owner, in this test's world. It is how the
// migrator and this harness's seeding connect.
func ownerURL(t *testing.T) string {
	t.Helper()
	return dbURL(world(t))
}

// appURL and radiusURL log in as foyer_app and foyer_radius themselves, in this
// test's world.
func appURL(t *testing.T) string {
	t.Helper()
	requireLogins(t)
	return withLogin(ownerURL(t), store.DefaultAppRole, appPassword)
}

func radiusURL(t *testing.T) string {
	t.Helper()
	requireLogins(t)
	return withLogin(ownerURL(t), store.RadiusRole, radiusPassword)
}

// dbURL is clusterURL pointed at another database.
func dbURL(name string) string {
	u, err := url.Parse(clusterURL)
	if err != nil {
		panic(err)
	}
	u.Path = "/" + name
	return u.String()
}

// provisionLogin gives role a fresh password and returns it.
//
// A failure is recorded rather than panicked, so that the tests about logins fail
// by name and every other test in the package still runs. A panic here would make
// "the migration that grants LOGIN was reverted" look like "the harness is broken".
func provisionLogin(ctx context.Context, dsn, role string) string {
	pw := randomString()
	if err := store.SetLoginPassword(ctx, dsn, role, pw); err != nil {
		loginErrs = append(loginErrs, err)
		return ""
	}
	return pw
}

var loginErrs []error

// requireLogins fails the test if TestMain could not give the login roles a
// password.
func requireLogins(t *testing.T) {
	t.Helper()
	requireDocker(t)
	for _, err := range loginErrs {
		t.Fatalf("the login roles could not be provisioned: %v", err)
	}
}

func withLogin(raw, user, password string) string {
	u, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	u.User = url.UserPassword(user, password)
	return u.String()
}

func requireDocker(t *testing.T) {
	t.Helper()
	if dockerAbsent != "" {
		t.Skipf("PostgreSQL is unavailable, so this guarantee is UNPROVEN on this machine: %s", dockerAbsent)
	}
}

func randomString() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func testKeyring(t *testing.T) *store.Keyring {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	kr, err := store.NewKeyring("k1:" + base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

// appStore opens a Store as the application does: same connection string, then
// SET ROLE foyer_app for the life of every connection.
func appStore(t *testing.T) *store.Store {
	t.Helper()
	requireDocker(t)
	st, err := store.Open(context.Background(), store.Config{
		URL:      ownerURL(t),
		Keys:     testKeyring(t),
		MaxConns: 4,
	})
	if err != nil {
		t.Fatalf("opening store: %v", err)
	}
	t.Cleanup(st.Close)
	return st
}

// storeAsRole opens a Store under a named role and tenant, for the tests that are
// about privilege and isolation rather than about behaviour.
func storeAsRole(t *testing.T, role, tenantID string) *store.Store {
	t.Helper()
	requireDocker(t)
	st, err := store.Open(context.Background(), store.Config{
		URL:      ownerURL(t),
		Role:     role,
		TenantID: tenantID,
		Keys:     testKeyring(t),
		MaxConns: 2,
	})
	if err != nil {
		t.Fatalf("opening store as %s: %v", role, err)
	}
	t.Cleanup(st.Close)
	return st
}

// ownerPool is a connection with no SET ROLE at all: the schema owner.
//
// It stands in for the two things that are not the application -- FreeRADIUS, which
// writes radacct, and the migration runner. Tests that need accounting rows to exist
// insert them here, because the application role cannot and must not.
func ownerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	requireDocker(t)
	cfg, err := pgxpool.ParseConfig(ownerURL(t))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 3
	// Scoped, but never de-privileged. That is the whole difference between this
	// pool and the application's.
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

// uniq returns an identifier no other test in this package will collide with. Each
// test has its own world now, so this matters within a test -- two sites seeded by
// one fixture -- rather than across tests.
func uniq(prefix string) string {
	return prefix + "-" + strings.ToLower(randomString()[:10])
}

func seedSite(t *testing.T, st *store.Store) *store.Site {
	t.Helper()
	s := &store.Site{ID: uniq("site"), Name: "Test Venue", Environment: "lab"}
	if err := st.Sites().Create(context.Background(), s); err != nil {
		t.Fatalf("creating site: %v", err)
	}
	return s
}

func mustSecret(t *testing.T, st *store.Store, siteID string) *store.SharedSecret {
	t.Helper()
	ss := &store.SharedSecret{ID: uniq("sec"), SiteID: siteID, Label: "Guest profile"}
	if err := st.SharedSecrets().Create(context.Background(), ss, []byte(randomString())); err != nil {
		t.Fatalf("creating shared secret: %v", err)
	}
	return ss
}

func fatalf(t *testing.T, err error, format string, args ...any) {
	t.Helper()
	if err != nil {
		t.Fatalf(fmt.Sprintf(format, args...)+": %v", err)
	}
}
