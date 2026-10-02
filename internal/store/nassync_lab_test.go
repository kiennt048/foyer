package store_test

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/store"
)

// The behavioural proof for the NAS projection: a real FreeRADIUS 3.2.10, real
// packets, and a RADIUS client written by Sync rather than inserted by hand.
//
// A synchronisation between two tables is the classic place for a function that
// runs, returns nil and copies nothing — a check that looks like it is checking
// something and is not. So the claim here is deliberately not *"the row
// exists"* — the unit tests already say that,
// and they would say it just as confidently about a projection FreeRADIUS could
// not use. The claim is **the packet is answered**, and then **the packet is
// discarded after removal**.
//
// It imports `internal/raddb` for the classification only. That is a test-only
// edge: neither package imports the other in production, and neither should —
// `NasRow` and `raddb.ClientRow` are two types with the same five fields precisely
// so that the store does not know the generator exists. The mapping below is the
// five lines the composition root will own.
//
// Run it with `make lab-nas-sync`. Without FOYER_LAB_E2E it skips and says what is
// unproven, because a test that quietly does not run reads exactly like one that
// passed.

const (
	labNetwork    = "foyer-nas-sync-net"
	labPostgresID = "foyer-nas-sync-postgres"
	labRadiusID   = "foyer-nas-sync-freeradius"
	labClientID   = "foyer-nas-sync-nas"

	// The lab's PostgreSQL password, matching lab/docker-compose.yml and the
	// generated lab/raddb. Lab-only, published on a high port, and never a
	// default anywhere outside lab/.
	labPGPassword = "foyer_lab_secret"
	labPGPort     = "55432"

	// FreeRADIUS's own login in the lab, matching FOYER_POSTGRES_PASSWORD in the
	// Makefile and FOYER_RADIUS_DB_PASSWORD in lab/docker-compose.yml.
	labRadiusPassword = "foyer_lab_secret_radius"

	// The Status-Server case from the blue/green probe suite, which ships inside
	// the FreeRADIUS image. It exercises no policy at all: it needs a known client
	// and a correct shared secret and nothing else, which is exactly the surface
	// this test is about.
	labProbeRequest = "/opt/foyer/blue-green/probes/status-server.request"
)

func TestNasProjectionAgainstRealFreeRADIUS(t *testing.T) {
	if os.Getenv("FOYER_LAB_E2E") != "1" {
		t.Skip("UNRUN: needs Docker, a built FreeRADIUS image and about a minute. " +
			"Nothing about a real packet is proven on this machine. Run `make lab-nas-sync`.")
	}

	image := os.Getenv("FOYER_LAB_FR_IMAGE")
	if image == "" {
		image = "foyer-nas-sync-freeradius:test"
	}

	ctx := context.Background()
	lab := startLab(t, image)

	// The address the NAS sends from. It is inside 172.16/12, which DefaultConfig
	// lists as a dynamic-client network, so a row added for it after startup is
	// discoverable — the `sql-only` path.
	nasAddr := lab.clientIP
	t.Logf("the NAS speaks from %s; FreeRADIUS is %s", nasAddr, labRadiusID)

	// ---------------------------------------------------------------- premise
	//
	// The premise, stated as an observation rather than a belief: a freshly
	// migrated database has an empty client table, and every packet is discarded.
	since := time.Now()
	out, err := lab.radclient(t, "a-secret-no-client-has-yet")
	if err == nil && strings.Contains(out, "Access-Accept") {
		t.Fatalf("a packet was answered before any client existed:\n%s", out)
	}
	logs := lab.radiusLogs(t, since)
	if !strings.Contains(logs, "unknown client") {
		t.Errorf("the server did not report an unknown client; the premise is not what this lab is doing:\n%s", tailOf(logs, 20))
	}
	t.Logf("MEASURED — before the sync: no reply, and the server logs %q", grepLine(logs, "unknown client"))

	// ---------------------------------------------------------------- the sync
	st := lab.store(t)
	site := &store.Site{ID: "lab-site", Name: "Lab Venue", Environment: "lab"}
	fatalf(t, st.Sites().Create(ctx, site), "creating the lab site")

	secretValue := []byte("lab-shared-secret-" + randomString())
	secret := &store.SharedSecret{ID: "lab-secret", SiteID: site.ID, Label: "Lab RADIUS profile"}
	fatalf(t, st.SharedSecrets().Create(ctx, secret, secretValue), "creating the shared secret")

	client := &store.RadiusClient{
		ID: "lab-client", SiteID: site.ID, Name: "lab-ap",
		Address: netip.MustParsePrefix(nasAddr + "/32"),
		Source:  "manual", Driver: "unifi", SharedSecretID: &secret.ID,
		RequireMsgAuth: true, LimitProxyState: true, Enabled: true,
	}
	fatalf(t, st.RadiusClients().Create(ctx, client), "creating the RADIUS client")

	res, err := st.NasProjection().Sync(ctx)
	fatalf(t, err, "syncing the projection")
	if res.Inserted != 1 {
		t.Fatalf("Inserted = %d, want 1: %+v", res.Inserted, res)
	}

	// ------------------------------------------------- the packet is answered
	since = time.Now()
	out, err = lab.radclient(t, string(secretValue))
	if err != nil || !strings.Contains(out, "Access-Accept") {
		t.Fatalf("a client written by Sync was not answered (err %v):\n%s\nserver:\n%s",
			err, tailOf(out, 25), tailOf(lab.radiusLogs(t, since), 25))
	}
	logs = lab.radiusLogs(t, since)
	t.Logf("MEASURED — after Sync: Access-Accept, and the server logs %q", grepLine(logs, "Adding client"))

	// ------------------------------------------------ what removal costs, measured
	//
	// The client has now been discovered, which means FreeRADIUS holds a cached
	// definition of it for `lifetime` seconds — 300 by DefaultConfig. Deleting the
	// row does not reach into that cache, and this is the interesting part: a
	// decommissioned access point keeps working until the cache expires or the
	// server restarts. Measuring it is worth more than asserting it.
	fatalf(t, st.RadiusClients().Delete(ctx, client.ID), "deleting the client")
	res, err = st.NasProjection().Sync(ctx)
	fatalf(t, err, "syncing after the delete")

	out, cachedErr := lab.radclient(t, string(secretValue))
	stillAnswered := cachedErr == nil && strings.Contains(out, "Access-Accept")
	t.Logf("MEASURED — immediately after removal, with the dynamic-client definition still cached (lifetime %ds): answered = %v",
		raddb.DefaultConfig().DynamicClientLifetimeSeconds, stillAnswered)

	// ------------------------------------------------ the packet is discarded
	//
	// Restarting clears the cache and reloads the client table, so this is the
	// state a decommissioned NAS ends up in. It is also the honest statement of
	// what the removal bought: the row is gone, and the running server stops
	// honouring it at the next restart or at cache expiry, whichever comes first.
	lab.restartRadius(t)
	since = time.Now()
	out, err = lab.radclient(t, string(secretValue))
	if err == nil && strings.Contains(out, "Access-Accept") {
		t.Fatalf("a removed client was still answered after a restart; the projection did not lose the row:\n%s", tailOf(out, 25))
	}
	logs = lab.radiusLogs(t, since)
	if !strings.Contains(logs, "unknown client") {
		t.Errorf("the server did not report an unknown client after removal:\n%s", tailOf(logs, 20))
	}
	t.Logf("MEASURED — after removal and restart: no reply, and the server logs %q", grepLine(logs, "unknown client"))

	// The report is corroboration, asserted after the wire rather than before it.
	// The order matters when this goes red: a sync that never removes fails on the
	// packet above — the observable an operator would meet — rather than on
	// something this function reports about itself.
	//
	// It names the row rather than counting it, which is what lets the classifier
	// be told about a revocation it could not otherwise see.
	if len(res.Removed) != 1 || res.Removed[0] != nasAddr {
		t.Fatalf("Removed = %v, want [%s]; the sync accumulates rather than synchronises: %+v",
			res.Removed, nasAddr, res)
	}

	// ------------------------------------- what `restart` means, on the wire
	//
	// Now the other half of the classification. Put the client back, restart so
	// that the server loads it at *startup* rather than discovering it, and then
	// rotate the secret. The running server keeps the copy it loaded: the new
	// secret is refused and the old one still works. That pair is the whole reason
	// `Classify` needs a startup snapshot, and it is the thing a `sql-only` answer
	// here would be lying about.
	client.ID = "lab-client-2"
	fatalf(t, st.RadiusClients().Create(ctx, client), "recreating the RADIUS client")
	_, err = st.NasProjection().Sync(ctx)
	fatalf(t, err, "syncing the recreated client")
	lab.restartRadius(t)

	if out, err := lab.radclient(t, string(secretValue)); err != nil || !strings.Contains(out, "Access-Accept") {
		t.Fatalf("the recreated client was not answered after a restart (err %v):\n%s", err, tailOf(out, 25))
	}

	// The snapshot of what that server loaded, taken the way a composition root
	// would take it: the rows the projection holds, mapped into the generator's
	// ClientRow.
	snapshotPath := filepath.Join(t.TempDir(), raddb.SnapshotName)
	// AllRows, not Rows: the snapshot has to be what this server loaded, and
	// `read_clients = yes` runs over a connection with no tenant GUC, so it loads
	// the whole table. See TestAllRowsSeesWhatFreeRADIUSLoadsAndRowsDoesNot.
	rows, err := st.NasProjection().AllRows(ctx)
	fatalf(t, err, "reading the projection for the snapshot")
	loaded := raddb.Snapshot{Config: raddb.DefaultConfig(), Clients: toClientRows(rows)}
	fatalf(t, raddb.WriteStartupSnapshot(snapshotPath, loaded), "writing the startup snapshot")

	rotated := []byte("rotated-lab-secret-" + randomString())
	fatalf(t, st.SharedSecrets().Rotate(ctx, secret.ID, rotated), "rotating the shared secret")
	_, err = st.NasProjection().Sync(ctx)
	fatalf(t, err, "syncing after the rotation")

	// The wire first, and the classification judged against it.
	//
	// The order is the point. A test that asserted `Classify` on its own could
	// only tell you that the function returns what the test expects; measuring the
	// server's behaviour and *then* requiring the classification to agree with it
	// is what makes this evidence rather than a restatement. When the two disagree
	// the failure below quotes both, because two halves of one test that
	// contradict each other are worth more than either half.
	since = time.Now()
	out, err = lab.radclient(t, string(rotated))
	if err == nil && strings.Contains(out, "Access-Accept") {
		t.Fatalf("the running server accepted the *new* secret; then the change was sql-only after all and the snapshot is wrong:\n%s", tailOf(out, 25))
	}
	logs = lab.radiusLogs(t, since)
	if !strings.Contains(logs, "invalid Message-Authenticator") {
		t.Errorf("the server did not report an invalid Message-Authenticator for the rotated secret:\n%s", tailOf(logs, 25))
	}
	t.Logf("MEASURED — after the rotation, the running server refuses the new secret: %q", grepLine(logs, "invalid Message-Authenticator"))

	// And the other half of the same instant, which is what makes it a
	// measurement rather than a failure: the *old* secret still works, because the
	// running server never re-read the table.
	if out, err := lab.radclient(t, string(secretValue)); err != nil || !strings.Contains(out, "Access-Accept") {
		t.Fatalf("the old secret stopped working without a restart; then the running server did re-read the table, and `restart` is the wrong class (err %v):\n%s",
			err, tailOf(out, 25))
	}
	t.Log("MEASURED — and the OLD secret is still accepted by the same running server. That pair is what `restart` means.")

	// Now the classification, against what the wire just did.
	snap, err := raddb.ReadStartupSnapshot(snapshotPath)
	fatalf(t, err, "reading the startup snapshot back")
	rows, err = st.NasProjection().AllRows(ctx)
	fatalf(t, err, "reading the projection after the rotation")
	change := snap.Classify(raddb.Snapshot{Config: raddb.DefaultConfig(), Clients: toClientRows(rows)})
	if change.Class != raddb.ClassRestart {
		t.Fatalf(`Classify says %q for a secret rotation on a startup-loaded client, and the server it is describing has just said otherwise on the wire:
    the new secret was refused  -- %s
    the old secret still works  -- Access-Accept from the same running server
An operator shown %q would believe the rotation had taken effect while every access point on that secret was locked out.
Classify's reasons: %v`,
			change.Class, grepLine(logs, "invalid Message-Authenticator"), change.Class, change.Reasons)
	}
	t.Logf("Classify agrees with the wire: %s — %v", change.Class, change.Reasons)
}

// toClientRows is the mapping the composition root owns: two types with the same
// five fields, kept separate so that neither package imports the other.
func toClientRows(rows []store.NasRow) []raddb.ClientRow {
	out := make([]raddb.ClientRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, raddb.ClientRow{
			NASName:   r.NASName,
			Shortname: r.Shortname,
			Type:      r.Type,
			Secret:    r.Secret,
			Server:    r.Server,
		})
	}
	return out
}

// ---------------------------------------------------------------- the lab itself

type labEnv struct {
	image    string
	clientIP string
	dsn      string
}

func startLab(t *testing.T, image string) *labEnv {
	t.Helper()

	raddbDir, err := filepath.Abs(filepath.Join("..", "..", "lab", "raddb"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(raddbDir, "radiusd.conf")); err != nil {
		t.Fatalf("no generated tree at %s; run `make lab-raddb` first: %v", raddbDir, err)
	}

	lab := &labEnv{image: image}
	lab.tearDown()
	t.Cleanup(lab.tearDown)

	mustDocker(t, "network", "create", labNetwork)

	// PostgreSQL, aliased `postgres` on the network because that is the host the
	// generated sql module names, and published so this test can reach it.
	mustDocker(t, "run", "-d", "--name", labPostgresID,
		"--network", labNetwork, "--network-alias", "postgres",
		"-e", "POSTGRES_DB=foyer", "-e", "POSTGRES_USER=foyer",
		"-e", "POSTGRES_PASSWORD="+labPGPassword,
		"-p", labPGPort+":5432", "postgres:16-alpine")

	lab.dsn = fmt.Sprintf("postgres://foyer:%s@127.0.0.1:%s/foyer?sslmode=disable", labPGPassword, labPGPort)

	// Readiness is a connection through the published port, not `pg_isready`
	// inside the container. The two disagree: the PostgreSQL image runs a
	// temporary server on a unix socket while initdb applies the entrypoint
	// scripts, and `pg_isready` answers yes to that one — so a migration started
	// on its word is reset by a peer that is about to restart. Measured here as an
	// intermittent "connection reset by peer" on the first migration.
	waitFor(t, 120, "PostgreSQL", func() bool {
		conn, err := pgx.Connect(context.Background(), lab.dsn)
		if err != nil {
			return false
		}
		defer conn.Close(context.Background())
		return conn.Ping(context.Background()) == nil
	})

	// The schema arrives the way lab/docker-compose.yml's one-shot service brings
	// it: applied before anything reads it. Here that is a direct call, because
	// this test is the thing holding the ordering.
	fatalf(t, store.Migrate(context.Background(), lab.dsn), "migrating the lab database")
	// And, as that one-shot now also does, the password FreeRADIUS logs in with.
	// The generated tree names foyer_radius (Makefile, lab-raddb), not the owner,
	// so this is what proves the data plane's own grants are enough to load the
	// client list — a tree that still logged in as the superuser would pass here
	// whatever those grants said.
	fatalf(t, store.SetLoginPassword(context.Background(), lab.dsn, store.RadiusRole, labRadiusPassword),
		"setting the FreeRADIUS login's password")

	// FreeRADIUS. `read_clients = yes` means it reads the client table before the
	// first packet, so the database above is a start-time dependency and the
	// ordering here is load-bearing rather than tidy.
	mustDocker(t, "run", "-d", "--name", labRadiusID, "--network", labNetwork,
		"-v", raddbDir+":/etc/raddb:ro", image, "radiusd", "-f", "-l", "stdout")

	// The NAS: a long-lived container with its own address on the same bridge, so
	// that packets arrive from an address that is not loopback. Loopback would be
	// answered by a different code path in the client lookup and would prove less.
	mustDocker(t, "run", "-d", "--name", labClientID, "--network", labNetwork,
		"--entrypoint", "sleep", image, "infinity")

	ip, err := docker("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", labClientID)
	if err != nil {
		t.Fatalf("reading the NAS address: %v", err)
	}
	lab.clientIP = strings.TrimSpace(ip)
	if lab.clientIP == "" {
		t.Fatal("the NAS container has no address")
	}

	lab.waitForRadius(t)
	return lab
}

func (l *labEnv) tearDown() {
	for _, name := range []string{labClientID, labRadiusID, labPostgresID} {
		_, _ = docker("rm", "-f", name)
	}
	_, _ = docker("network", "rm", labNetwork)
}

func (l *labEnv) store(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), store.Config{
		URL:      l.dsn,
		Keys:     testKeyring(t),
		MaxConns: 4,
	})
	fatalf(t, err, "opening the lab store")
	t.Cleanup(st.Close)
	return st
}

// waitForRadius waits for the server to be listening at all. It cannot use a
// Status-Server probe, because at this point there is no client to send one from
// — which is the whole condition the first assertion is about.
//
// **Only the current start's output counts.** `docker restart` keeps the
// container's log, so the previous start's "Ready to process requests" is still in
// it: searching the whole log returns at once after every restart, and the next
// probe races a server that has not bound its socket yet. So this reads the
// container's own StartedAt after the restart and looks only at lines docker
// timestamped at or after it (linesOfThisStart).
func (l *labEnv) waitForRadius(t *testing.T) {
	t.Helper()
	startedAt := containerStartedAt(t, labRadiusID)
	waitFor(t, 80, "FreeRADIUS", func() bool {
		all, err := docker("logs", "--timestamps", labRadiusID)
		if err != nil {
			return false
		}
		out := linesOfThisStart(all, startedAt)
		if strings.Contains(out, "Ready to process requests") {
			return true
		}
		if strings.Contains(out, "Failed") || strings.Contains(out, "Errors reading") {
			t.Fatalf("FreeRADIUS failed to start:\n%s", tailOf(out, 30))
		}
		return false
	})
}

// containerStartedAt is when docker last started the container: after `docker
// restart`, the restart.
func containerStartedAt(t *testing.T, container string) time.Time {
	t.Helper()
	out, err := docker("inspect", "-f", "{{.State.StartedAt}}", container)
	if err != nil {
		t.Fatalf("reading %s's start time: %v\n%s", container, err, out)
	}
	at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(out))
	if err != nil {
		t.Fatalf("reading %s's start time %q: %v", container, out, err)
	}
	return at
}

// linesOfThisStart keeps the lines of `docker logs --timestamps` output that
// docker stamped at or after startedAt, without their stamps. A line whose stamp
// does not parse is dropped rather than kept: kept, it could be the previous
// start's "Ready" line, which is the whole defect.
func linesOfThisStart(logs string, startedAt time.Time) string {
	var b strings.Builder
	for _, line := range strings.Split(logs, "\n") {
		stamp, rest, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || at.Before(startedAt) {
			continue
		}
		b.WriteString(rest)
		b.WriteByte('\n')
	}
	return b.String()
}

func (l *labEnv) restartRadius(t *testing.T) {
	t.Helper()
	mustDocker(t, "restart", labRadiusID)
	l.waitForRadius(t)
}

// radclient sends the Status-Server probe from the NAS container.
//
// Status-Server on the authentication port is answered by the server itself with
// no policy involved, so a reply means exactly one thing: this source address is a
// known client and this shared secret is the right one.
func (l *labEnv) radclient(t *testing.T, secret string) (string, error) {
	t.Helper()
	return docker("exec", labClientID, "radclient",
		"-x", "-t", "3", "-r", "1", "-f", labProbeRequest,
		labRadiusID+":1812", "status", secret)
}

func (l *labEnv) radiusLogs(t *testing.T, since time.Time) string {
	t.Helper()
	out, err := docker("logs", "--since", since.Format(time.RFC3339), labRadiusID)
	if err != nil {
		t.Logf("reading FreeRADIUS logs: %v", err)
	}
	return out
}

// ---------------------------------------------------------------- small helpers

func docker(args ...string) (string, error) {
	cmd := exec.Command("docker", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustDocker(t *testing.T, args ...string) {
	t.Helper()
	if out, err := docker(args...); err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func waitFor(t *testing.T, tries int, what string, ready func() bool) {
	t.Helper()
	for i := 0; i < tries; i++ {
		if ready() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatalf("%s never became ready", what)
}

func tailOf(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// grepLine returns the first line containing needle, trimmed, so that a log
// observation can be quoted in the test output rather than described.
func grepLine(s, needle string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// TestAReadyLineFromThePreviousStartDoesNotCount is the harness race: after
// `docker restart` the log still holds the previous start's "Ready to process
// requests", and a wait that searched the whole log returned before the new server had bound its socket. Pure: no
// FreeRADIUS, so it runs in every `go test ./internal/store`, not only in
// `make lab-test`.
func TestAReadyLineFromThePreviousStartDoesNotCount(t *testing.T) {
	restart := time.Date(2026, 9, 24, 3, 0, 5, 500_000_000, time.UTC)
	logs := strings.Join([]string{
		"2026-09-24T03:00:01.100000000Z Info: Starting - reading configuration files ...",
		"2026-09-24T03:00:02.200000000Z Info: Ready to process requests",
		"2026-09-24T03:00:05.400000000Z Info: Signalled to terminate",
		"not a stamped line: Ready to process requests",
		"2026-09-24T03:00:05.600000000Z Info: Starting - reading configuration files ...",
		"",
	}, "\n")
	got := linesOfThisStart(logs, restart)
	if strings.Contains(got, "Ready to process requests") {
		t.Fatalf("the previous start's Ready line counted as this start's:\n%s", got)
	}
	if !strings.Contains(got, "Starting - reading configuration files") {
		t.Fatalf("this start's own line was dropped:\n%q", got)
	}
	logs += "2026-09-24T03:00:06.700000000Z Info: Ready to process requests\n"
	if !strings.Contains(linesOfThisStart(logs, restart), "Ready to process requests") {
		t.Fatal("this start's Ready line was not seen")
	}
}
