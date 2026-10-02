package jobs_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/jobs"
	"github.com/kiennt048/foyer/internal/session"
	"github.com/kiennt048/foyer/internal/store"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The standard jobs, against real PostgreSQL 16 and the real scheduler on the real
// clock: the acceptance criterion is "create expired data, wait one cycle, the
// data is gone", and a fake would agree with whatever the jobs believe.

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
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// voucherCode is twelve characters of the contract's alphabet, fresh each call so
// the suite can run more than once against one database.
func voucherCode() string {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func requireDocker(t *testing.T) {
	t.Helper()
	if dockerAbsent != "" {
		t.Skipf("PostgreSQL is unavailable, so this guarantee is UNPROVEN on this machine: %s", dockerAbsent)
	}
}

func owner(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SELECT set_config('foyer.tenant_id', $1, false)`, store.DefaultTenantID)
		return err
	}
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

func must(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

type world struct {
	app       *store.Store
	reaper    *session.Reaper
	retention *store.Retention
	owner     *pgxpool.Pool
}

func newWorld(t *testing.T) world {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	app, err := store.Open(ctx, store.Config{URL: ownerURL, MaxConns: 4})
	must(t, err, "opening the application store")
	t.Cleanup(app.Close)
	rp, err := store.Open(ctx, store.Config{URL: ownerURL, Role: "foyer_reaper", MaxConns: 2})
	must(t, err, "opening the reaper pool")
	t.Cleanup(rp.Close)
	ret, err := store.OpenRetention(ctx, store.Config{URL: ownerURL})
	must(t, err, "opening the retention connection")
	t.Cleanup(ret.Close)
	return world{app: app, reaper: session.NewReaper(rp.Pool(), session.ReaperConfig{}, quiet()),
		retention: ret, owner: owner(t)}
}

func (w world) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.owner.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("seeding: %v\n%s", err, sql)
	}
}

func (w world) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := w.owner.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatalf("counting: %v\n%s", err, sql)
	}
	return n
}

// TestTheStandardJobsCleanExpiredDataWithinOneCycle seeds one expired and one live
// row for every job, runs the real scheduler for real, and waits for the expired
// rows to go and the live ones to stay.
func TestTheStandardJobsCleanExpiredDataWithinOneCycle(t *testing.T) {
	w := newWorld(t)
	site := "site-" + randomString()[:8]
	w.exec(t, `INSERT INTO sites (id, name, environment) VALUES ($1, 'Test Venue', 'lab')`, site)

	// portal-sessions
	w.exec(t, `INSERT INTO portal_sessions (token_hash, site_id, client_mac, expires_at) VALUES
		('expired-'||$2, $1, '02:00:00:00:00:01', now() - interval '1 minute'),
		('live-'||$2,    $1, '02:00:00:00:00:02', now() + interval '1 hour')`, site, randomString())
	// operator-sessions
	op := "op-" + randomString()[:8]
	w.exec(t, `INSERT INTO operators (id, username, password_hash, role) VALUES ($1, $1, '$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA', 'staff')`, op)
	w.exec(t, `INSERT INTO operator_sessions (token_hash, operator_id, csrf_token, expires_at) VALUES
		('expired-'||$1, $1, 'c', now() - interval '1 minute'),
		('live-'||$1,    $1, 'c', now() + interval '1 hour')`, op)
	// voucher-expiry
	batch := "vb-" + randomString()[:8]
	w.exec(t, `INSERT INTO voucher_batches (id, site_id, label, count, expiry_mode) VALUES ($1, $2, 'x', 2, 'from-creation')`, batch, site)
	w.exec(t, `INSERT INTO vouchers (id, batch_id, site_id, code, state, expires_at) VALUES
		($1||'-old', $1, $2, $3, 'unused', now() - interval '1 minute'),
		($1||'-new', $1, $2, $4, 'unused', now() + interval '1 hour')`, batch, site, voucherCode(), voucherCode())
	stale, ancient := "stale-"+randomString(), "ancient-"+randomString()
	// stale-sessions: silent for an hour, far past the reaper's 20-minute threshold
	w.exec(t, `INSERT INTO radacct (acctsessionid, acctuniqueid, nasipaddress, acctstarttime, acctupdatetime, callingstationid)
		VALUES ($1, $1, '10.99.0.1', now() - interval '3 hours', now() - interval '1 hour', '02:00:00:00:00:03')`, stale)
	// retention: a session that ended 400 days ago, past the 90-day default
	w.exec(t, `INSERT INTO radacct (acctsessionid, acctuniqueid, nasipaddress, acctstarttime, acctstoptime, callingstationid)
		VALUES ($1, $1, '10.99.0.1', now() - interval '401 days', now() - interval '400 days', '02:00:00:00:00:04')`, ancient)
	// phone-codes: a code sent two days ago, past PhoneCodeRetention (a day), and one
	// sent a minute ago. Personal data -- the number and the device -- and without
	// this case no test would notice if the job did nothing.
	codeOld, codeNew := "pc-old-"+randomString()[:8], "pc-new-"+randomString()[:8]
	w.exec(t, `INSERT INTO phone_codes (id, site_id, session_token_hash, client_mac, phone, code_mac, key_id,
		created_at, expires_at, max_attempts) VALUES
		($1, $3, 'tok-old', '02:00:00:00:00:05', '+84912345678', $4, 'k1', now() - interval '2 days', now() - interval '2 days' + interval '5 minutes', 5),
		($2, $3, 'tok-new', '02:00:00:00:00:06', '+84912345679', $4, 'k1', now() - interval '1 minute', now() + interval '4 minutes', 5)`,
		codeOld, codeNew, site, make([]byte, 32))
	// portal-sessions' second half, the RFC 8908 address book: an address last seen
	// two days ago, past DefaultAddressWindow (a day), and one seen now.
	addrOld, addrNew := "10.98."+fmt.Sprint(time.Now().UnixNano()%250)+".1", "10.98."+fmt.Sprint(time.Now().UnixNano()%250)+".2"
	w.exec(t, `INSERT INTO portal_addresses (address, token_hash, client_mac, seen_at) VALUES
		($1::inet, 'tok-a-old', '02:00:00:00:00:07', now() - interval '2 days'),
		($2::inet, 'tok-a-new', '02:00:00:00:00:08', now())`, addrOld, addrNew)

	s := jobs.New(jobs.Options{Logger: quiet(), InitialDelay: 10 * time.Millisecond})
	for _, j := range jobs.Standard(jobs.Deps{Store: w.app, Reaper: w.reaper, Retention: w.retention,
		Logger: quiet(), Every: 250 * time.Millisecond}) {
		must(t, s.Add(j), "registering "+j.Name)
	}
	s.Start(context.Background())
	stopped := false
	defer func() {
		if !stopped {
			s.Stop()
		}
	}()

	deadline := time.Now().Add(30 * time.Second)
	for {
		st := s.Stats()
		ran := true
		for _, name := range []string{jobs.JobPortalSessions, jobs.JobOperatorSessions, jobs.JobVoucherExpiry,
			jobs.JobStaleSessions, jobs.JobRetention, jobs.JobPhoneCodes} {
			if st[name].Runs == 0 {
				ran = false
			}
		}
		if ran {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not every job ran within 30s: %+v", st)
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.Stop()
	stopped = true

	for name, st := range s.Stats() {
		if st.Failures != 0 {
			t.Errorf("job %s failed %d time(s): %s", name, st.Failures, st.LastError)
		}
	}
	check := func(what string, want int, sql string, args ...any) {
		t.Helper()
		if got := w.count(t, sql, args...); got != want {
			t.Errorf("%s: %d, want %d", what, got, want)
		}
	}
	check("expired portal sessions", 0, `SELECT count(*) FROM portal_sessions WHERE site_id = $1 AND expires_at <= now()`, site)
	check("live portal sessions", 1, `SELECT count(*) FROM portal_sessions WHERE site_id = $1 AND expires_at > now()`, site)
	check("expired console sessions", 0, `SELECT count(*) FROM operator_sessions WHERE operator_id = $1 AND expires_at <= now()`, op)
	check("live console sessions", 1, `SELECT count(*) FROM operator_sessions WHERE operator_id = $1`, op)
	check("the past-expiry voucher still marked unused", 0, `SELECT count(*) FROM vouchers WHERE id = $1 AND state <> 'expired'`, batch+"-old")
	check("the live voucher", 1, `SELECT count(*) FROM vouchers WHERE id = $1 AND state = 'unused'`, batch+"-new")
	check("the stale session still open", 0, `SELECT count(*) FROM radacct WHERE acctsessionid = $1 AND acctstoptime IS NULL`, stale)
	check("the stale session closed by the reaper", 1, `SELECT count(*) FROM radacct WHERE acctsessionid = $1 AND acctterminatecause = 'Foyer-Stale-Reaped'`, stale)
	check("the 400-day-old session", 0, `SELECT count(*) FROM radacct WHERE acctsessionid = $1`, ancient)
	check("the two-day-old phone code", 0, `SELECT count(*) FROM phone_codes WHERE id = $1`, codeOld)
	check("the phone code sent a minute ago", 1, `SELECT count(*) FROM phone_codes WHERE id = $1`, codeNew)
	check("the address last seen two days ago", 0, `SELECT count(*) FROM portal_addresses WHERE address = $1::inet`, addrOld)
	check("the address seen now", 1, `SELECT count(*) FROM portal_addresses WHERE address = $1::inet`, addrNew)
	check("the retention sweep's record of accounting", 1, `SELECT count(*) FROM retention_sweeps WHERE category = 'accounting' AND NOT last_failed`)
}

// TestTheStandardJobsAreTheSixNamed pins the list: removing a job from Standard is
// a change somebody has to make on purpose.
func TestTheStandardJobsAreTheSixNamed(t *testing.T) {
	var names []string
	for _, j := range jobs.Standard(jobs.Deps{}) {
		names = append(names, j.Name)
		if j.Every != jobs.DefaultInterval {
			t.Errorf("%s runs every %s by default, want %s", j.Name, j.Every, jobs.DefaultInterval)
		}
		if j.Jitter <= 0 {
			t.Errorf("%s has no jitter", j.Name)
		}
	}
	want := []string{jobs.JobPortalSessions, jobs.JobOperatorSessions, jobs.JobVoucherExpiry, jobs.JobStaleSessions, jobs.JobRetention, jobs.JobPhoneCodes}
	if !slices.Equal(names, want) {
		t.Fatalf("standard jobs %v, want %v", names, want)
	}
}

// TestAMissingPoolIsAFailureEveryCycleNotASilence: a control plane that could not
// open the reaper's or the retention role's pool still registers those jobs, and
// they fail loudly with the reason.
func TestAMissingPoolIsAFailureEveryCycleNotASilence(t *testing.T) {
	why := errors.New("permission denied to set role \"foyer_retention\"")
	for _, j := range jobs.Standard(jobs.Deps{RetentionErr: why}) {
		if j.Name != jobs.JobRetention && j.Name != jobs.JobStaleSessions {
			continue
		}
		_, err := j.Run(context.Background())
		if !errors.Is(err, jobs.ErrUnavailable) {
			t.Fatalf("%s with no pool returned %v, want ErrUnavailable", j.Name, err)
		}
		if j.Name == jobs.JobRetention && !strings.Contains(err.Error(), why.Error()) {
			t.Fatalf("the reason was lost: %v", err)
		}
	}
}
