package obs

import (
	"context"
	"encoding/json"
	"log"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// A real PostgreSQL, once, for the readiness tests.
//
// **Tier-1 against a real database, for the same reason `internal/store`'s harness
// gives: connection behaviour is a property of PostgreSQL.** A fake pool would
// agree with whatever this file believes "the database is unreachable" means, which
// is precisely the belief under test — and what is wanted is a proof that readiness
// answers false when PostgreSQL is unreachable, which a fake cannot give without
// being written to fail on demand, at which point it is asserting that the test's
// own stub works.
//
// Started once and shared, because starting PostgreSQL is the expensive part and
// these tests only ping and close. Docker absent is reported as a skip with the
// reason and never as a pass: an unproven guarantee has to look different from a
// proven one.
var (
	dbOnce sync.Once
	dbURL  string
	dbErr  error
)

// testDatabaseURL is "" when there is no database to test against.
func testDatabaseURL(t *testing.T) string {
	t.Helper()
	dbOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		dbURL, dbErr = startPostgres(ctx)
	})
	if dbErr != nil {
		t.Logf("no PostgreSQL: %v", dbErr)
		return ""
	}
	return dbURL
}

func startPostgres(ctx context.Context) (string, error) {
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("postgres"),
		postgres.WithUsername("foyer"),
		postgres.WithPassword("obs-test-only-not-a-deployment-credential"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second),
		),
	)
	if err != nil {
		return "", err
	}
	url, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		return "", err
	}
	// The container is deliberately not terminated: the pool is closed by each
	// test's own cleanup, and a `go test` that ran twice in one process would
	// otherwise find a second database to start for no gain.
	return url, nil
}

func openPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("opening a pool against the test database: %v", err)
	}
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		t.Fatalf("the test database did not answer a ping: %v", err)
	}
	return pool
}

// pingCheck is the readiness check `cmd/foyer` installs: a ping on the pool it
// already holds, bounded by the handler's context.
//
// It is a `Ping` and not a `SELECT 1` deliberately. Both answer "is PostgreSQL
// reachable", and the ping is the one that does not depend on a table, a schema or
// a role's grants — so a readiness probe cannot go false because a migration is
// half-applied or because the role lost a privilege, which are failures the probe
// has no business reporting as "this process cannot serve".
func pingCheck(pool *pgxpool.Pool) Check {
	return func(ctx context.Context) error { return pool.Ping(ctx) }
}

// serveReadiness runs the handler and returns the decoded body.
func serveReadiness(t *testing.T, r Readiness) Result {
	t.Helper()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	return decodeResult(t, rec)
}

func readinessStatus(t *testing.T, r Readiness) int {
	t.Helper()
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	return rec.Code
}

func decodeResult(t *testing.T, rec *httptest.ResponseRecorder) Result {
	t.Helper()
	var res Result
	if err := json.NewDecoder(strings.NewReader(rec.Body.String())).Decode(&res); err != nil {
		t.Fatalf("the readiness body is not JSON (status %d): %v\n%s", rec.Code, err, rec.Body.String())
	}
	return res
}

// stdlibLogger is the process's stdlib logger, which `Install` has re-pointed at
// the structured handler. Reached through `log.Default()` rather than held, so
// the test exercises the same object `net/http` writes a failed handshake to.
func stdlibLogger() *log.Logger { return log.Default() }
