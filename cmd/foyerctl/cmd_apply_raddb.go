package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/store"
)

func init() {
	Register(&Command{
		Name:    "apply-raddb",
		Usage:   "--root <dir> [--restart-command <cmd>] [--probe-secret-env NAME] [--dry-run]",
		Summary: "Apply a generated FreeRADIUS tree through the four blue/green gates",
		Run:     runApplyRaddb,
	})
}

// applyPlan is everything the flags decided, separated from the flags so that the
// wiring below can be built and exercised without a FreeRADIUS install.
//
// It exists because an Applier built with no SnapshotPath and no StartupClients
// makes gate 4 report "no snapshot path configured … so it is a guess" on every
// run, and the startup snapshot is then never written outside a unit test — so
// the measured difference between `sql-only` and `restart` would never be used.
type applyPlan struct {
	cfg     raddb.Config
	root    string
	radiusd string

	altAuthPort int
	altAcctPort int
	probeHost   string
	secret      string

	probes []raddb.Probe
	// ready is the status-server case, when the probe directory has one. It
	// serves twice: gate 2's readiness poll and gate 4's live verification.
	// Without it gate 2 sleeps and says in its own report that the wait was a
	// guess, and gate 4 has nothing to verify a cutover against.
	ready *raddb.Probe

	noEAP  bool
	dryRun bool

	// snapshotPath is where gate 4 records what the restarted server will load.
	// Empty means no snapshot, and snapshotWhy says why in the words the report
	// prints — never silence, because the cost of not having one is a wrong
	// classification later rather than a missing file now.
	snapshotPath string
	snapshotWhy  string

	// restartCommand restarts the live server after the swap. Empty means the
	// symlink moves and the running server keeps the tree it started with.
	restartCommand string

	// dbURL reaches the client table for the snapshot. It carries a password and
	// is never printed.
	dbURL string

	timeout time.Duration
}

func runApplyRaddb(args []string) error {
	plan, generateOnly, err := parseApplyRaddb(args)
	if err != nil {
		return err
	}

	if generateOnly != "" {
		if err := raddb.NewGenerator(plan.cfg).Generate(generateOnly); err != nil {
			return err
		}
		fmt.Printf("wrote %s; no gate was run, so nothing about this tree has been checked\n", generateOnly)
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), plan.timeout)
	defer cancel()

	// The client table is opened here, before a single gate runs, and read at
	// cutover.
	//
	// The two halves are deliberately split. Reading has to happen at the moment
	// the server is restarted — an apply takes minutes and an access point can be
	// registered inside one — but *connecting* at that moment would mean an
	// unreachable database first showing up after gate 4 has already swapped the
	// symlink, which is the one point in the procedure with no rollback for it.
	// So the connection is proven at second zero and only the query is late.
	var startupClients func(context.Context) ([]raddb.ClientRow, error)
	if plan.snapshotPath != "" {
		// NoRole: by default this URL is the --pg-* coordinates, i.e. FreeRADIUS's
		// own login (foyer_radius), which holds SELECT on `nas` — all this reads —
		// and cannot SET ROLE foyer_app. The snapshot is "what FreeRADIUS loads", so
		// reading it with FreeRADIUS's privileges is the honest shape anyway.
		st, err := store.Open(ctx, store.Config{URL: plan.dbURL, Role: store.NoRole, MaxConns: 2})
		if err != nil {
			return fmt.Errorf("the startup snapshot needs the client table and the database did not answer: %w\n"+
				"Pass --db, or --no-snapshot to apply without one and accept that the next classification is a guess", err)
		}
		defer st.Close()
		startupClients = startupClientRows(st.NasProjection())
	}

	a := plan.applier(raddb.ExecRunner{}, startupClients)

	rep, applyErr := a.Apply(ctx, plan.cfg)
	printReport(os.Stdout, rep, plan.notes())

	if plan.dryRun && rep.Failed() != nil && rep.Failed().Gate.Number == 4 {
		fmt.Println("\ndry run: gates 0 to 3 passed and nothing was cut over")
		return nil
	}
	return applyErr
}

func parseApplyRaddb(args []string) (*applyPlan, string, error) {
	fs := flag.NewFlagSet("apply-raddb", flag.ExitOnError)
	root := fs.String("root", "/var/lib/foyer", "directory holding raddb.blue, raddb.green and the raddb symlink")
	radiusd := fs.String("radiusd", "radiusd", "path to the radiusd binary")
	radclient := fs.String("radclient", "radclient", "path to radclient")
	eapolTest := fs.String("eapol-test", "eapol_test", "path to eapol_test")
	probeDir := fs.String("probe-dir", "/opt/foyer/blue-green/probes", "directory of radclient .request cases")
	eapConf := fs.String("eap-conf", "", "eapol_test configuration; empty runs no EAP case, and the report says so")
	probeHost := fs.String("probe-host", "127.0.0.1", "address the candidate server listens on for gates 2 and 3, and where gate 4 verifies the live server")
	altAuthPort := fs.Int("alt-auth-port", 11812, "authentication port for the candidate server")
	altAcctPort := fs.Int("alt-acct-port", 11813, "accounting port for the candidate server")
	secretEnv := fs.String("probe-secret-env", "FOYER_PROBE_SECRET", "environment variable holding the probe client's shared secret")
	policyURL := fs.String("policy-url", "http://foyer-backend:8080/api/v1/aaa/authorize", "Policy API URL")
	pgHost := fs.String("pg-host", "postgres", "PostgreSQL host")
	pgPort := fs.Int("pg-port", 5432, "PostgreSQL port")
	pgDB := fs.String("pg-db", "foyer", "PostgreSQL database")
	pgUser := fs.String("pg-user", store.RadiusRole, "PostgreSQL user FreeRADIUS logs in as; its own least-privileged role, never the schema owner")
	clientTable := fs.String("client-table", "nas", "SQL table holding RADIUS clients")
	eapCertDir := fs.String("eap-cert-dir", "", "enable EAP-TLS and read the certificate, key and CA from this directory")
	generateOnly := fs.String("generate-only", "", "write the tree to this directory and run no gates; for a harness that drives radiusd itself")
	// No environment default, on purpose: a shell command stored in a deployment's
	// environment is not how any deployment
	// restarts FreeRADIUS — the supervisor inside the FreeRADIUS image does
	// (lab/blue-green/supervise.sh) — so the only restart command left is one a
	// developer types here, for a host install they are driving by hand.
	restartCommand := fs.String("restart-command", "", "shell command that restarts the live FreeRADIUS after the swap, for example \"systemctl restart freeradius\"; a developer's host-install flag, never read from the environment")
	// Both secrets in the _FILE form as well as the variable, through the helper
	// cmd/foyer and every other foyerctl command reads them with
	// (TestEveryCommandThatTakesADatabaseURLAcceptsTheFileForm).
	dbFromEnv, dbErr := config.EnvOrFile("FOYER_DATABASE_URL")
	pgFromEnv, pgErr := config.EnvOrFile("FOYER_POSTGRES_PASSWORD")
	dbURL := fs.String("db", dbFromEnv, "PostgreSQL connection URL for the startup snapshot; defaults to the --pg-* coordinates")
	snapshot := fs.String("snapshot", "", "where to record what the restarted server loads; defaults to "+raddb.SnapshotName+" inside --root")
	noSnapshot := fs.Bool("no-snapshot", false, "apply without recording a startup snapshot, and accept that the next classification cannot tell a loaded client from a discoverable one")
	dryRun := fs.Bool("dry-run", false, "run gates 0 to 3 and stop before the cutover")
	timeout := fs.Duration("timeout", 2*time.Minute, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return nil, "", err
	}
	for _, err := range []error{dbErr, pgErr} {
		if err != nil {
			return nil, "", err
		}
	}

	secret := os.Getenv(*secretEnv)
	if secret == "" && *generateOnly == "" {
		return nil, "", fmt.Errorf("%s is empty; the probe client's shared secret has no default", *secretEnv)
	}

	cfg := raddb.DefaultConfig()
	cfg.PolicyAPIURL = *policyURL
	cfg.PostgresHost = *pgHost
	cfg.PostgresPort = *pgPort
	cfg.PostgresDB = *pgDB
	cfg.PostgresUser = *pgUser
	cfg.PostgresPassword = pgFromEnv
	cfg.ClientTable = *clientTable
	if *eapCertDir != "" {
		// Paths, never contents, and nothing here creates them. A certificate
		// baked into an image is one private key shared by every deployment on
		// earth.
		cfg.EAP = raddb.EAPConfig{
			Enabled:        true,
			DefaultEAPType: "tls",
			CertDir:        *eapCertDir,
			ServerCertFile: *eapCertDir + "/server.pem",
			PrivateKeyFile: *eapCertDir + "/server.key",
			CAFile:         *eapCertDir + "/ca.pem",
		}
	}

	p := &applyPlan{
		cfg:            cfg,
		root:           *root,
		radiusd:        *radiusd,
		altAuthPort:    *altAuthPort,
		altAcctPort:    *altAcctPort,
		probeHost:      *probeHost,
		secret:         secret,
		noEAP:          *eapConf == "",
		dryRun:         *dryRun,
		restartCommand: *restartCommand,
		dbURL:          *dbURL,
		timeout:        *timeout,
	}

	if *generateOnly != "" {
		return p, *generateOnly, nil
	}

	probes, err := radclientProbes(*probeDir, *radclient)
	if err != nil {
		return nil, "", err
	}
	if *eapConf != "" {
		probes = append(probes, raddb.EapolTestProbe("eap-tls", *eapolTest, *eapConf))
	}
	p.probes = probes
	for i := range probes {
		if probes[i].Name == statusServerProbe {
			p.ready = &probes[i]
			break
		}
	}

	if p.dbURL == "" {
		p.dbURL = postgresURL(*pgHost, *pgPort, *pgDB, *pgUser, cfg.PostgresPassword)
	}
	p.snapshotPath, p.snapshotWhy = snapshotDecision(*snapshot, *root, *noSnapshot, *restartCommand, *dryRun)

	return p, "", nil
}

// statusServerProbe is the case that proves two things and nothing else: this
// source address is a known client, and this shared secret is the right one. It
// runs no policy, which is what makes it safe to send at a live server.
const statusServerProbe = "status-server"

// snapshotDecision answers where the startup snapshot goes, or why there is none.
//
// The second return is the load-bearing one. A snapshot is a claim about what the
// running server loaded at startup, and it is only true if a start actually
// follows it. Where nothing restarts the live server, writing one records a client
// list the running server never read — and the next `Classify` then answers
// `sql-only` for a rotation on a loaded client, which is the direction that tells
// an operator no restart is needed while the server refuses every packet on the
// new secret. Measured on 3.2.10: the new secret produces "invalid
// Message-Authenticator! (Shared secret is incorrect.)" while the old one is still
// answered `Access-Accept`, on the same running server, in the same minute.
//
// So the absence of a restart is a reason to write nothing, and the reason is
// printed rather than left to be noticed.
func snapshotDecision(path, root string, noSnapshot bool, restartCommand string, dryRun bool) (string, string) {
	switch {
	case noSnapshot:
		return "", "--no-snapshot was given: nothing recorded what this server loads, so the next classification cannot tell a startup-loaded client from a discoverable one and every answer it gives is a guess"
	case restartCommand == "" && !dryRun:
		return "", "no --restart-command: this run swaps the symlink and nothing restarts the live server, so it goes on running the tree it started with. No startup snapshot was written, because one written before a restart that never happens claims a client list the running server did not load, and the next classification would answer sql-only for a rotation that needs a restart"
	case path != "":
		return path, ""
	default:
		return raddb.SnapshotPathFor(root), ""
	}
}

// applier builds the Applier the four gates run on.
//
// The Runner and the client-table read are parameters rather than fixed, so that
// the wiring itself — the snapshot written before the restart, above all — is
// something a test can watch happen without a FreeRADIUS install or a database.
func (p *applyPlan) applier(runner raddb.Runner, startupClients func(context.Context) ([]raddb.ClientRow, error)) *raddb.Applier {
	a := &raddb.Applier{
		Runner:      runner,
		Root:        p.root,
		RadiusdPath: p.radiusd,
		AltAuthPort: p.altAuthPort,
		AltAcctPort: p.altAcctPort,
		ProbeTarget: raddb.Target{Host: p.probeHost, Secret: p.secret},
		Probes:      p.probes,
	}

	if p.snapshotPath != "" {
		a.SnapshotPath = p.snapshotPath
		a.StartupClients = startupClients
	}

	if p.ready != nil {
		a.Ready = pollProbe(runner, *p.ready, 40, 250*time.Millisecond)
	}

	switch {
	case p.dryRun:
		// Stopping before the cutover is a real thing to want, and it must be
		// stated in the report rather than looking like a full run that passed.
		// A snapshot written on this path is discarded by gate 4's rollback,
		// which is the whole reason a dry run may write one at all.
		a.Restart = func(context.Context) error {
			return errors.New("dry run: stopping before the live server is restarted")
		}
	case p.restartCommand != "":
		a.Restart = func(ctx context.Context) error {
			out, err := runner.Run(ctx, "sh", "-c", p.restartCommand)
			if err != nil {
				return fmt.Errorf("%s: %w: %s", p.restartCommand, err, lastLines(string(out), 500))
			}
			return nil
		}
		if p.ready != nil {
			// Verification is what makes the rollback reachable. Without it gate
			// 4 reports a successful cutover for a server that died on startup,
			// and blue/green degrades into swap-and-hope.
			live := raddb.Target{Host: p.probeHost, AuthPort: p.cfg.AuthPort, AcctPort: p.cfg.AcctPort, Secret: p.secret}
			verify := pollProbe(runner, *p.ready, 20, 250*time.Millisecond)
			a.VerifyLive = func(ctx context.Context) error { return verify(ctx, live) }
		}
	}

	return a
}

// notes are the things the report has to say that are not a gate result.
func (p *applyPlan) notes() []string {
	var out []string
	if p.noEAP {
		out = append(out, "no eapol_test case ran. radclient does not speak EAP, so gate 3\nproved nothing about the EAP surface in this run.")
	}
	if p.snapshotWhy != "" {
		out = append(out, p.snapshotWhy+".")
	}
	if p.ready == nil {
		out = append(out, "no "+statusServerProbe+" case in the probe directory: gate 2 waited rather than\nasked, and the cutover was not verified against the live server.")
	}
	return out
}

// pollProbe turns one probe into a bounded readiness check.
//
// Bounded and reported rather than slept: the Applier's own fallback waits a fixed
// two seconds and says in its report that the wait was a guess, which is honest and
// is still a guess. This asks the server instead.
func pollProbe(runner raddb.Runner, probe raddb.Probe, attempts int, every time.Duration) func(context.Context, raddb.Target) error {
	return func(ctx context.Context, t raddb.Target) error {
		name, args := probe.Build(t)
		var last error
		for i := 0; i < attempts; i++ {
			out, err := runner.Run(ctx, name, args...)
			if err == nil {
				return nil
			}
			last = fmt.Errorf("%w: %s", err, lastLines(string(out), 300))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(every):
			}
		}
		return fmt.Errorf("%s did not answer after %d attempts: %w", probe.Name, attempts, last)
	}
}

// startupClientRows reads the whole client table and maps it into the generator's
// shape.
//
// **The whole table, with no tenant scope**, and that is the point rather than an
// oversight. FreeRADIUS reads `nas` with `read_clients = yes` over a connection
// that never sets the tenant GUC, so it loads every row — another tenant's, and
// the hand-seeded ones with no tenant at all. A snapshot missing a row the server
// loaded classifies that client as new, and a new client inside a dynamic network
// classifies `sql-only`.
//
// The two row types stay separate types on purpose: making them one would mean
// `internal/store` importing the generator or the generator importing the store,
// and neither should know the other exists. This is the composition root, so the
// mapping lives here.
func startupClientRows(repo *store.NasProjectionRepo) func(context.Context) ([]raddb.ClientRow, error) {
	return func(ctx context.Context) ([]raddb.ClientRow, error) {
		rows, err := repo.AllRows(ctx)
		if err != nil {
			return nil, err
		}
		return clientRows(rows), nil
	}
}

// clientRows is the mapping itself, kept apart from the read so that the
// classification path can reuse it.
func clientRows(rows []store.NasRow) []raddb.ClientRow {
	out := make([]raddb.ClientRow, len(rows))
	for i, r := range rows {
		out[i] = raddb.ClientRow{
			NASName:   r.NASName,
			Shortname: r.Shortname,
			Type:      r.Type,
			Secret:    r.Secret,
			Server:    r.Server,
		}
	}
	return out
}

// postgresURL builds a connection string from the coordinates the generated tree
// already uses, so that a deployment which configured FreeRADIUS's database has
// not also had to configure a second one for this command to disagree with.
//
// It carries a password. Nothing prints it.
func postgresURL(host string, port int, db, user, password string) string {
	u := &url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(host, strconv.Itoa(port)),
		Path:   "/" + db,
	}
	if password != "" {
		u.User = url.UserPassword(user, password)
	} else {
		u.User = url.User(user)
	}
	return u.String()
}

func radclientProbes(dir, radclientPath string) ([]raddb.Probe, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("probe directory %s: %w", dir, err)
	}
	var probes []raddb.Probe
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".request") {
			continue
		}
		path := dir + "/" + e.Name()
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		packetType := "auth"
		for _, line := range strings.Split(string(body), "\n") {
			if rest, ok := strings.CutPrefix(line, "#packet-type:"); ok {
				packetType = strings.TrimSpace(rest)
				break
			}
		}
		name := strings.TrimSuffix(e.Name(), ".request")
		probes = append(probes, raddb.RadclientProbe(name, radclientPath, path, packetType))
	}
	if len(probes) == 0 {
		// An empty suite passes every assertion in it.
		return nil, fmt.Errorf("no .request cases in %s", dir)
	}
	return probes, nil
}

func printReport(w io.Writer, rep raddb.Report, notes []string) {
	for _, r := range rep.Results {
		status := "ok  "
		if !r.OK {
			status = "FAIL"
		}
		fmt.Fprintf(w, "%s  gate %d %-14s %-8s %s\n", status, r.Gate.Number, r.Gate.Name, r.Elapsed.Round(time.Millisecond), r.Detail)
		if r.Err != nil {
			fmt.Fprintf(w, "      %v\n", r.Err)
		}
	}
	if rep.RolledBack {
		fmt.Fprintln(w, "\nthe cutover was rolled back; the previous tree is live")
	}
	for _, n := range notes {
		fmt.Fprintf(w, "\n%s\n", n)
	}
}

func lastLines(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
