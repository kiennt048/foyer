package session_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/session"
	"github.com/kiennt048/foyer/internal/store"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The reaper is tested against real PostgreSQL, as `foyer_reaper`, for the same
// reason `internal/store` is: the thing that makes this an audited
// exception rather than a hole is a column-level GRANT, and a fake would agree with
// whatever this code believes about it.
//
// `internal/store/readonly_test.go` proves the grant is narrow -- that the reaper
// cannot rewrite a byte counter, insert or delete. These prove the other half: that
// what the reaper actually does fits inside it, and that it does the right thing to
// the four columns it may touch. Neither half is sufficient. A grant nothing
// exercises is a grant that is right by luck.

var (
	ownerURL     string
	dockerAbsent string
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	pw := randomString()
	ctr, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithDatabase("foyer"),
		postgres.WithUsername("foyer"),
		postgres.WithPassword(pw),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(90*time.Second)),
	)
	if err != nil {
		// Docker genuinely absent. Every test below says why it could not run
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

func randomString() string {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func requireDocker(t *testing.T) {
	t.Helper()
	if dockerAbsent != "" {
		t.Skipf("PostgreSQL is unavailable, so this guarantee is UNPROVEN on this machine: %s", dockerAbsent)
	}
}

// reaperPool is the reaper's own connection: SET ROLE foyer_reaper for the life of
// it, exactly as the deployment does. If a statement in this package needs a
// privilege the role does not hold, it fails here rather than in production.
func reaperPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	requireDocker(t)
	return poolAsRole(t, "foyer_reaper")
}

// ownerPool stands in for FreeRADIUS, which is the only thing that legitimately
// inserts into radacct.
func ownerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	requireDocker(t)
	return poolAsRole(t, "")
}

func poolAsRole(t *testing.T, role string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 3
	if role != "" {
		cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, "SET ROLE "+role)
			return err
		}
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// insertSession writes one open accounting row as FreeRADIUS would, with its last
// update `silentFor` ago.
func insertSession(t *testing.T, pool *pgxpool.Pool, id string, silentFor time.Duration, interval *int64) string {
	t.Helper()
	uniq := fmt.Sprintf("%s-%s", id, randomString())
	_, err := pool.Exec(context.Background(), `
		INSERT INTO radacct (acctsessionid, acctuniqueid, username, nasipaddress,
		                     acctstarttime, acctupdatetime, acctinterval,
		                     callingstationid, acctinputoctets, acctoutputoctets)
		VALUES ($1, $2, 'guest', '10.99.0.7',
		        now() - interval '8 hours', now() - ($3::text || ' seconds')::interval, $4,
		        '02:11:22:33:44:55', 4294967296, 8589934592)`,
		id, uniq, fmt.Sprintf("%d", int64(silentFor.Seconds())), interval)
	if err != nil {
		t.Fatalf("inserting a session as the owner: %v", err)
	}
	return uniq
}

// TestTheReaperClosesOnlyWhatWentSilentLongEnough.
//
// Two thresholds are in play and they are deliberately different numbers: the
// console *marks* a session orphaned at twice its reporting interval, and the
// reaper *closes* it at four times. A reaper that closed at the display threshold
// would mean no operator ever saw a session orphaned -- and "sessions keep going
// orphaned on that access point" is the signal that a NAS is misbehaving.
func TestTheReaperClosesOnlyWhatWentSilentLongEnough(t *testing.T) {
	requireDocker(t)
	owner := ownerPool(t)
	rp := reaperPool(t)
	ctx := context.Background()

	clearRadacct(t, owner)

	fresh := insertSession(t, owner, "fresh", 60*time.Second, nil)
	orphaned := insertSession(t, owner, "orphaned", 11*time.Minute, nil) // past 2x, short of 4x
	stale := insertSession(t, owner, "stale", 45*time.Minute, nil)

	r := session.NewReaper(rp, session.ReaperConfig{}, discardLogger())
	closed, err := r.Reap(ctx)
	if err != nil {
		t.Fatalf("the reaper could not do its one job as foyer_reaper: %v", err)
	}

	closedIDs := map[string]bool{}
	for _, s := range closed {
		closedIDs[s.AcctUniqueID] = true
	}
	if !closedIDs[stale] {
		t.Error("a session silent for 45 minutes was left open; nothing else will ever close it")
	}
	if closedIDs[orphaned] {
		t.Error("a session silent for 11 minutes was closed: it is orphaned, which an operator needs to be able to see, not gone")
	}
	if closedIDs[fresh] {
		t.Error("a session that reported a minute ago was closed")
	}
}

// TestTheReaperBelievesTheTimestampsRatherThanTheClock.
//
// `acctstoptime` is the last moment anything was heard, not now(). The difference
// is a NAS that rebooted on Friday: now() invents the whole weekend as connected
// time, on a row a quota or an invoice reads.
//
// `acctsessiontime` follows from the same two timestamps and never from a byte
// counter -- `wrap.go` exists because subtracting raw RADIUS counters is how
// "remaining" hands a guest 4 GiB.
func TestTheReaperBelievesTheTimestampsRatherThanTheClock(t *testing.T) {
	requireDocker(t)
	owner := ownerPool(t)
	rp := reaperPool(t)
	ctx := context.Background()

	clearRadacct(t, owner)
	uniq := insertSession(t, owner, "silent", 2*time.Hour, nil)

	r := session.NewReaper(rp, session.ReaperConfig{}, discardLogger())
	if _, err := r.Reap(ctx); err != nil {
		t.Fatal(err)
	}

	var stop, update, start time.Time
	var seconds int64
	var cause string
	err := owner.QueryRow(ctx, `
		SELECT acctstarttime, acctstoptime, acctupdatetime, acctsessiontime, acctterminatecause
		FROM radacct WHERE acctuniqueid = $1`, uniq).
		Scan(&start, &stop, &update, &seconds, &cause)
	if err != nil {
		t.Fatal(err)
	}

	// Started 8 h ago, last heard from 2 h ago: six hours of session, not eight.
	const sixHours = 6 * 3600
	if seconds < sixHours-120 || seconds > sixHours+120 {
		t.Errorf("acctsessiontime is %d s; the session ran from its start to the last thing heard from it, which is about %d s. now() would have billed the silence", seconds, sixHours)
	}
	if time.Since(stop) < 90*time.Minute {
		t.Errorf("acctstoptime is %s, which is close to now(): the session ended when the NAS went quiet, not when the reaper noticed", stop)
	}
	// acctupdatetime is the one column that means "we touched it", and it is the
	// only way a reader can tell a reaped session from one a NAS closed.
	if !update.After(stop) {
		t.Errorf("acctupdatetime %s is not after acctstoptime %s, so nothing distinguishes this row from one a NAS closed properly", update, stop)
	}
	if cause != "Foyer-Stale-Reaped" {
		t.Errorf("terminate cause %q; it must be one no NAS would send, because the reaper knows the NAS went quiet and not why", cause)
	}
}

// TestTheReaperTouchesNothingElse is the assertion the column-level grant exists
// for, made from this side of it.
//
// `internal/store/readonly_test.go` proves PostgreSQL refuses a wider write. This
// proves the reaper does not attempt one -- and specifically that the byte counters
// come out of a reaped row bit-identical, including the one already past 2^32 that
// a careless "reset the counters on close" would have zeroed.
func TestTheReaperTouchesNothingElse(t *testing.T) {
	requireDocker(t)
	owner := ownerPool(t)
	rp := reaperPool(t)
	ctx := context.Background()

	clearRadacct(t, owner)
	uniq := insertSession(t, owner, "silent", 2*time.Hour, nil)

	var beforeIn, beforeOut int64
	var beforeUser, beforeCalling, beforeNAS string
	if err := owner.QueryRow(ctx, `
		SELECT acctinputoctets, acctoutputoctets, username, callingstationid, host(nasipaddress)
		FROM radacct WHERE acctuniqueid = $1`, uniq).
		Scan(&beforeIn, &beforeOut, &beforeUser, &beforeCalling, &beforeNAS); err != nil {
		t.Fatal(err)
	}

	r := session.NewReaper(rp, session.ReaperConfig{}, discardLogger())
	if _, err := r.Reap(ctx); err != nil {
		t.Fatal(err)
	}

	var afterIn, afterOut int64
	var afterUser, afterCalling, afterNAS string
	if err := owner.QueryRow(ctx, `
		SELECT acctinputoctets, acctoutputoctets, username, callingstationid, host(nasipaddress)
		FROM radacct WHERE acctuniqueid = $1`, uniq).
		Scan(&afterIn, &afterOut, &afterUser, &afterCalling, &afterNAS); err != nil {
		t.Fatal(err)
	}

	if beforeIn != afterIn || beforeOut != afterOut {
		t.Errorf("byte counters moved: in %d->%d, out %d->%d. Accounting is FreeRADIUS's, and a counter this reaper rewrote is a figure nobody can reconcile",
			beforeIn, afterIn, beforeOut, afterOut)
	}
	if beforeUser != afterUser || beforeCalling != afterCalling || beforeNAS != afterNAS {
		t.Error("the reaper rewrote an identity column")
	}
}

// TestTheReaperRefusesAPassThatCouldBeAnOutage.
//
// A site-wide outage and a bug in the silence arithmetic produce exactly the same
// query result, and the reaper cannot tell them apart. When the number is big
// enough to be either, the safe direction is to write nothing and alarm: those
// sessions are already over, so closing them an hour later with somebody watching
// costs nothing, and closing four hundred live ones costs a venue.
func TestTheReaperRefusesAPassThatCouldBeAnOutage(t *testing.T) {
	requireDocker(t)
	owner := ownerPool(t)
	rp := reaperPool(t)
	ctx := context.Background()

	clearRadacct(t, owner)
	for i := 0; i < 6; i++ {
		insertSession(t, owner, fmt.Sprintf("bulk-%d", i), 2*time.Hour, nil)
	}

	r := session.NewReaper(rp, session.ReaperConfig{MaxPerPass: 3}, discardLogger())
	closed, err := r.Reap(ctx)
	if err == nil {
		t.Fatal("the reaper closed a pass six times the size of a normal one without objecting")
	}
	if len(closed) != 0 {
		t.Errorf("the reaper refused and still closed %d sessions", len(closed))
	}

	var open int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM radacct WHERE acctstoptime IS NULL`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 6 {
		t.Errorf("%d sessions are still open after a refused pass, want 6: a refusal that half-wrote is worse than either outcome", open)
	}
}

// TestDryRunWritesNothing: the mode an operator runs first, on a venue they are not
// sure about.
func TestDryRunWritesNothing(t *testing.T) {
	requireDocker(t)
	owner := ownerPool(t)
	rp := reaperPool(t)
	ctx := context.Background()

	clearRadacct(t, owner)
	insertSession(t, owner, "silent", 2*time.Hour, nil)

	r := session.NewReaper(rp, session.ReaperConfig{DryRun: true}, discardLogger())
	would, err := r.Reap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(would) != 1 {
		t.Fatalf("dry run reported %d sessions, want 1", len(would))
	}

	var open int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM radacct WHERE acctstoptime IS NULL`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 1 {
		t.Error("a dry run closed a session")
	}
}

// TestTheNASsOwnIntervalIsBelievedButFloored.
//
// Acct-Interim-Interval has a widely-implemented 300-second floor
// (`docs/rules/radius.md`), so a NAS claiming ten seconds is not evidence that
// forty seconds of silence means anything. A reaper that took the claim at face
// value would close every session on that NAS within a minute of a single lost UDP
// packet.
func TestTheNASsOwnIntervalIsBelievedButFloored(t *testing.T) {
	requireDocker(t)
	owner := ownerPool(t)
	rp := reaperPool(t)
	ctx := context.Background()

	clearRadacct(t, owner)
	tiny := int64(10)
	honest := int64(900)
	liar := insertSession(t, owner, "claims-10s", 5*time.Minute, &tiny)
	slow := insertSession(t, owner, "reports-15m", 45*time.Minute, &honest)

	r := session.NewReaper(rp, session.ReaperConfig{}, discardLogger())
	closed, err := r.Reap(ctx)
	if err != nil {
		t.Fatal(err)
	}

	ids := map[string]bool{}
	for _, s := range closed {
		ids[s.AcctUniqueID] = true
	}
	if ids[liar] {
		t.Error("a session on a NAS claiming a ten-second interval was closed after five minutes of silence; the interval floor is 300 s and the claim is not believed below it")
	}
	// 45 min of silence against a claimed 15 min interval is 3x -- short of 4x.
	if ids[slow] {
		t.Error("a session on a NAS that genuinely reports every fifteen minutes was closed at three intervals of silence, not four")
	}

	if got := r.Config().SilenceThreshold(10); got != 20*time.Minute {
		t.Errorf("SilenceThreshold(10s) = %s, want 20m: the floor applies before the multiplier", got)
	}
	if got := r.Config().SilenceThreshold(900); got != 60*time.Minute {
		t.Errorf("SilenceThreshold(900s) = %s, want 60m", got)
	}
}

func clearRadacct(t *testing.T, owner *pgxpool.Pool) {
	t.Helper()
	if _, err := owner.Exec(context.Background(), `DELETE FROM radacct`); err != nil {
		t.Fatal(err)
	}
}
