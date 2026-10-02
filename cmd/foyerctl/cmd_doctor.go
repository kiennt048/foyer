package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/raddb"
	"github.com/kiennt048/foyer/internal/store"
)

func init() {
	Register(&Command{
		Name:    "doctor",
		Usage:   "[--db <url>] [--root <dir>]",
		Summary: "Check an installation and say what is wrong with it",
		Run:     runDoctor,
	})
}

// `foyerctl doctor` is the half of the install design (docs/architecture.md) that
// makes "a stranger installs it" survivable: the first-run wizard gets someone to a running system, and this
// says why the running system is not working.
//
// It is scoped to what can be checked rather than guessed, and every check says what
// the failure *does* rather than only that it happened. Three of them exist because
// the same failure has looked like a network problem before: an empty client table
// makes FreeRADIUS discard every packet as coming from an unknown NAS, a projection
// behind its system of record does the same for one access point, and a rotation on
// a startup-loaded client leaves the running server refusing the new secret while
// the table says otherwise.
//
// It reads and it dials. It writes nothing, and in particular it does not run a sync
// — a diagnostic that repairs the thing it is diagnosing cannot be run twice to see
// whether the repair held.
func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	dbDefault, err := envOrFileDefault("FOYER_DATABASE_URL")
	if err != nil {
		return err
	}
	dbURL := fs.String("db", dbDefault, "PostgreSQL connection URL")
	tenant := fs.String("tenant", "", "tenant to check; defaults to the single tenant v1 ships with")
	root := fs.String("root", "/var/lib/foyer", "directory holding the raddb symlink and the startup snapshot")
	radiusd := fs.String("radiusd", "radiusd", "path to the radiusd binary")
	timeout := fs.Duration("timeout", 30*time.Second, "overall timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	var checks []checkResult
	checks = append(checks, keyringCheck())

	st, dbCheck := openForDoctor(ctx, *dbURL, *tenant)
	checks = append(checks, dbCheck)
	if st != nil {
		defer st.Close()
		checks = append(checks, migrationCheck(ctx, *dbURL))
		checks = append(checks, projectionCheck(ctx, st))
		checks = append(checks, snapshotCheck(ctx, st, raddb.SnapshotPathFor(*root)))
	}

	checks = append(checks, raddbTreeCheck(ctx, *radiusd, filepath.Join(*root, "raddb")))
	checks = append(checks, listenerChecks()...)

	if failed := renderChecks(os.Stdout, checks); failed > 0 {
		return fmt.Errorf("%d check(s) failed", failed)
	}
	return nil
}

type checkStatus string

const (
	statusOK   checkStatus = "ok"
	statusWarn checkStatus = "warn"
	statusFail checkStatus = "FAIL"
	// statusSkip is for a check that could not run. It is deliberately not `ok`:
	// a check that did not run reads exactly like one that passed, which is how
	// a mechanism comes to look like a check without being one.
	statusSkip checkStatus = "skip"
)

type checkResult struct {
	name   string
	status checkStatus
	detail string
	// advice is what to do about it, printed under the line.
	advice []string
}

func renderChecks(w io.Writer, checks []checkResult) int {
	failed, warned, skipped := 0, 0, 0
	for _, c := range checks {
		fmt.Fprintf(w, "%-4s  %-12s  %s\n", c.status, c.name, c.detail)
		for _, a := range c.advice {
			fmt.Fprintf(w, "        %s\n", a)
		}
		switch c.status {
		case statusFail:
			failed++
		case statusWarn:
			warned++
		case statusSkip:
			skipped++
		}
	}
	fmt.Fprintf(w, "\n%d check(s): %d passed, %d warning(s), %d not checked, %d failed\n",
		len(checks), len(checks)-failed-warned-skipped, warned, skipped, failed)
	return failed
}

// keyringCheck proves the key can open what it sealed, rather than that the
// variable is set. A KEK that parses and cannot round-trip is a deployment where
// every shared secret, controller credential and NT hash is unreadable, and the
// first symptom is a sync that cannot project a client.
func keyringCheck() checkResult {
	keys, err := store.KeyringFromEnv()
	if err != nil {
		return checkResult{"keyring", statusFail, "FOYER_KEK could not be parsed: " + err.Error(), nil}
	}
	if keys == nil {
		return checkResult{"keyring", statusFail, "FOYER_KEK is not set", []string{
			"Registering a controller, storing an NT hash and rotating a shared secret are",
			"refused without it, and `foyerctl sync-nas` cannot unseal a single client.",
		}}
	}
	aad := []byte("foyerctl/doctor")
	blob, err := keys.Seal([]byte("round-trip"), aad)
	if err != nil {
		return checkResult{"keyring", statusFail, "the key could not seal: " + err.Error(), nil}
	}
	back, err := keys.Open(blob, aad)
	if err != nil {
		return checkResult{"keyring", statusFail, "the key could not open what it sealed: " + err.Error(), nil}
	}
	if string(back) != "round-trip" {
		return checkResult{"keyring", statusFail, "the key opened its own ciphertext into something else", nil}
	}
	return checkResult{"keyring", statusOK, "FOYER_KEK is usable; active key " + keys.ActiveKeyID(), nil}
}

func openForDoctor(ctx context.Context, dbURL, tenant string) (*store.Store, checkResult) {
	if dbURL == "" {
		return nil, checkResult{"database", statusFail, "no connection URL", []string{
			"Set FOYER_DATABASE_URL or pass --db. There is no default: a default database",
			"URL is a deployment pointed at whatever happens to answer on 5432.",
		}}
	}
	start := time.Now()
	// The keyring is passed so the projection check can compare shared secrets,
	// which is the drift an operator actually calls about. The error is dropped on
	// purpose: keyringCheck has already run and reported it, and a doctor that
	// refuses to open the database because a key is malformed cannot tell the
	// operator anything else that is wrong. `Plan` reports which half it compared.
	keys, _ := store.KeyringFromEnv()
	st, err := store.Open(ctx, store.Config{URL: dbURL, TenantID: tenant, Keys: keys, MaxConns: 2})
	if err != nil {
		// Never the URL back: it carries a password.
		return nil, checkResult{"database", statusFail, "did not answer: " + err.Error(), nil}
	}
	return st, checkResult{"database", statusOK, fmt.Sprintf("answered in %s", time.Since(start).Round(time.Millisecond)), nil}
}

func migrationCheck(ctx context.Context, dbURL string) checkResult {
	states, err := store.MigrationStatus(ctx, dbURL)
	if err != nil {
		return checkResult{"migrations", statusFail, "could not be read: " + err.Error(), nil}
	}
	applied, pending := 0, 0
	for _, s := range states {
		if s.Applied {
			applied++
		} else {
			pending++
		}
	}
	if pending > 0 {
		return checkResult{"migrations", statusFail,
			fmt.Sprintf("%d applied, %d pending: the schema is behind this binary", applied, pending),
			[]string{"Run `foyerctl migrate` as a role that owns the schema."}}
	}
	return checkResult{"migrations", statusOK, fmt.Sprintf("%d applied, none pending", applied), nil}
}

// projectionCheck asks the store what a sync would do, and reports it.
//
// It does not compare **prefixes** — parse every `nas.nasname` and every
// `radius_clients.address` back into a `netip.Prefix` and look for one the other
// side does not have. The reasoning behind that is sound — how a name is spelled is
// a rule `internal/store` owns and warns is subtle, and a second copy of it here
// would be a second source of truth for a value that lands in a UNIQUE column — but
// such a comparison **cannot disagree about anything except the address**.
//
// So it would read as current for a projection carrying a shortname and a shared
// secret that nothing in the system of record produces, and that is the drift
// operators actually call about: one access point refused with "Shared secret is
// incorrect" while every check is green. It could not see a row that should be
// removed either, which is a decommissioned access point still holding a valid
// RADIUS client.
//
// `NasProjection().Plan` is the same classification `Sync` writes from, so this
// reports what a sync would do rather than an approximation of it, and the naming
// rule stays in exactly one place.
func projectionCheck(ctx context.Context, st *store.Store) checkResult {
	plan, err := st.NasProjection().Plan(ctx)
	if err != nil {
		return checkResult{"projection", statusFail, "could not be planned: " + err.Error(), nil}
	}
	return renderProjection(plan)
}

func renderProjection(plan store.NasPlan) checkResult {
	detail := fmt.Sprintf("%d row(s) already current, %d to add, %d to update, %d to remove, %d client(s) not projectable",
		plan.Unchanged, len(plan.Insert), len(plan.Update), len(plan.Remove), len(plan.Skipped))
	res := checkResult{"projection", statusOK, detail, nil}
	warn := func() {
		if res.status == statusOK {
			res.status = statusWarn
		}
	}

	// A check that could not run reads exactly like one that passed, so the half
	// this process was unable to compare is stated rather than left out.
	if !plan.SecretsCompared {
		warn()
		res.advice = append(res.advice,
			"FOYER_KEK is unset, so shared-secret drift was NOT compared: a row whose secret",
			"no longer matches the system of record reads as current here.")
	}

	for _, s := range plan.Skipped {
		warn()
		res.advice = append(res.advice, "not projectable: "+s.Address+" — "+s.Reason)
	}

	if len(plan.Remove) > 0 {
		warn()
		res.advice = append(res.advice,
			"in the client table and produced by nothing, so they are RADIUS clients no",
			"operator can revoke: "+join(plan.Remove),
			"Run `foyerctl sync-nas`.")
	}

	var stale, cosmetic []string
	for _, u := range plan.Update {
		if slices.Contains(u.Differs, "shared secret") {
			stale = append(stale, u.NASName)
			continue
		}
		cosmetic = append(cosmetic, u.NASName+" ("+join(u.Differs)+")")
	}
	sort.Strings(stale)
	sort.Strings(cosmetic)

	if len(cosmetic) > 0 {
		warn()
		res.advice = append(res.advice,
			"projected with columns the system of record no longer produces: "+join(cosmetic),
			"Run `foyerctl sync-nas`.")
	}
	if len(stale) > 0 {
		res.status = statusFail
		res.advice = append(res.advice,
			"projected with a shared secret the system of record no longer produces, so",
			"FreeRADIUS refuses every packet from them with \"Shared secret is incorrect\":",
			join(stale),
			"Run `foyerctl sync-nas`.")
	}

	if len(plan.Insert) > 0 {
		missing := make([]string, 0, len(plan.Insert))
		for _, i := range plan.Insert {
			missing = append(missing, i.NASName)
		}
		sort.Strings(missing)
		res.status = statusFail
		res.advice = append(res.advice,
			"enabled and not in the client table, so FreeRADIUS discards every packet from",
			"them as coming from an unknown NAS: "+join(missing),
			"Run `foyerctl sync-nas`.")
	}
	return res
}

// snapshotCheck answers whether a restart is owed.
//
// A `restart` here is not an error: it is a change that has been written to the
// table and is not in the running server, which is a normal state to be in for the
// minutes between a sync and a maintenance window. It is a warning because nothing
// else in the product will ever mention it, and the symptom — one access point
// whose every packet is refused with "Shared secret is incorrect" — reads as a
// network fault.
func snapshotCheck(ctx context.Context, st *store.Store, path string) checkResult {
	f, err := raddb.ReadStartupSnapshot(path)
	if errors.Is(err, os.ErrNotExist) {
		return checkResult{"snapshot", statusWarn, "none at " + path, []string{
			"Nothing recorded what the running server loaded at startup, so whether a change",
			"needs a restart cannot be answered. `foyerctl apply-raddb --restart-command …`",
			"writes one at cutover.",
		}}
	}
	if err != nil {
		return checkResult{"snapshot", statusFail, "could not be read: " + err.Error(), nil}
	}
	rows, err := st.NasProjection().AllRows(ctx)
	if err != nil {
		return checkResult{"snapshot", statusFail, "the nas table could not be read: " + err.Error(), nil}
	}

	redacted := raddb.RedactSnapshot(raddb.Snapshot{Clients: clientRows(rows)}, f.Salt)
	change := raddb.ClassifyRedacted(f.Redacted, raddb.Snapshot{Config: f.Redacted.Config, Clients: redacted.Clients})
	switch change.Class {
	case raddb.ClassNone:
		return checkResult{"snapshot", statusOK, "the running server's client list is the table's", nil}
	case raddb.ClassSQLOnly:
		return checkResult{"snapshot", statusOK, change.String() + "; no restart is owed", change.Reasons}
	default:
		return checkResult{"snapshot", statusWarn, "the table has changes the running server cannot see: " + change.String(),
			append(append([]string{}, change.Reasons...),
				"Restarting drops every in-flight EAP conversation and the TLS session cache",
				"with it. SIGHUP does not reliably reload clients in 3.2.x.")}
	}
}

// raddbTreeCheck runs gate 1 against the live tree.
//
// Gate 1 is necessary and not sufficient, per FreeRADIUS's own man page: the server
// can return zero here and still fail when run normally, and some modules are
// skipped. This is a diagnostic, so an unchecked module is a smaller cost than in
// the applier — but the limit is stated rather than implied.
func raddbTreeCheck(ctx context.Context, radiusdPath, tree string) checkResult {
	if _, err := exec.LookPath(radiusdPath); err != nil {
		return checkResult{"raddb", statusSkip, radiusdPath + " is not on PATH, so the generated tree was not checked", nil}
	}
	if _, err := os.Stat(tree); err != nil {
		return checkResult{"raddb", statusWarn, "no tree at " + tree, []string{
			"`foyerctl apply-raddb` creates it and points the raddb symlink at it.",
		}}
	}

	runner := raddb.ExecRunner{}
	out, err := runner.Run(ctx, radiusdPath, "-v")
	if err != nil {
		return checkResult{"raddb", statusFail, "radiusd -v failed: " + err.Error(), nil}
	}
	v, err := raddb.CheckVersionOutput(string(out))
	if err != nil {
		return checkResult{"raddb", statusFail, err.Error(), nil}
	}
	if _, err := runner.Run(ctx, radiusdPath, "-XC", "-d", tree); err != nil {
		return checkResult{"raddb", statusFail, "radiusd -XC rejected " + tree, []string{"Run it by hand to see which line."}}
	}
	return checkResult{"raddb", statusOK, "FreeRADIUS " + v.String() + "; " + tree + " parses",
		[]string{"-XC is incomplete by design: a module that fails to instantiate is invisible to it."}}
}

// listenerChecks dials the three listeners.
//
// The addresses come from `internal/config`, which is what `cmd/foyer` starts from,
// never from a second copy of the variable names and their defaults.
//
// **A copy drifts.** `cmd/foyer` admits to being a lab only for the exact sentence
// `config.LabInsecureVar` names; a copy that accepted any non-empty value would let
// `FOYER_LAB_INSECURE=yes` produce a doctor reporting a plaintext Policy API on a
// routable address as a permitted warning while the control plane refuses to start
// on it. A diagnostic that disagrees with the thing it diagnoses, about a
// §2 boundary, is worse than no diagnostic.
func listenerChecks() []checkResult {
	cfg := config.Load()
	return []checkResult{
		dialCheck("portal", cfg.PortalAddr),
		dialCheck("admin", cfg.AdminAddr),
		dialCheck("policy", cfg.PolicyAddr),
		policyExposureCheck(cfg),
	}
}

func dialCheck(name, addr string) checkResult {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return checkResult{name, statusFail, fmt.Sprintf("%q is not host:port", addr), nil}
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 500*time.Millisecond)
	if err != nil {
		return checkResult{name, statusWarn, "nothing is listening on " + addr, []string{
			"The control plane may simply not be running; doctor does not start it.",
		}}
	}
	_ = conn.Close()
	return checkResult{name, statusOK, "listening on " + addr, nil}
}

// policyExposureCheck does not agree with the startup assertion -- it **is** the
// startup assertion, called.
//
// A restated rule drifts. A copy can accept any non-empty FOYER_LAB_INSECURE and
// call a configuration `cmd/foyer` refuses to start on "a permitted warning", or
// answer **OK** for a non-loopback Policy API with a certificate and no client CA
// -- TLS with no client authentication -- and offer setting a client CA as advice.
// A test that asserted such an OK would hold the exposure in place.
//
// `config.PolicyBoundaryError` is the one authority. What is left here is
// rendering, which is what a diagnostic should be.
func policyExposureCheck(cfg config.Config) checkResult {
	const name = "policy-tls"

	if err := cfg.PolicyBoundaryError(); err != nil {
		return checkResult{name, statusFail, err.Error(), []string{
			"The control plane refuses to start in this configuration.",
		}}
	}

	host, _, _ := net.SplitHostPort(cfg.PolicyAddr)
	switch {
	case isLoopbackHost(host) && !cfg.PolicyTLSConfigured():
		return checkResult{name, statusOK, "the Policy API is bound to loopback", nil}
	case isLoopbackHost(host) && cfg.PolicyClientCA == "":
		return checkResult{name, statusWarn, "the Policy API has TLS on loopback but does not check who is calling", []string{
			"docs/rules/security.md §2 asks for mTLS. On loopback the exposure is a local process rather than",
			"the network, which is why this is a warning and not the refusal it is on any other address.",
		}}
	case !cfg.PolicyTLSConfigured():
		// Reachable only with the lab acknowledgement set, which is the one thing
		// that makes PolicyBoundaryError permit plaintext off loopback.
		return checkResult{name, statusWarn, "plaintext on a non-loopback address, permitted only by " + config.LabInsecureVar, []string{
			"This must never be set on a deployment.",
		}}
	}
	return checkResult{name, statusOK, "mTLS is configured on the Policy API", nil}
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return a.IsLoopback()
}

func join(v []string) string {
	out := ""
	for i, s := range v {
		if i > 0 {
			out += ", "
		}
		out += s
	}
	return out
}
