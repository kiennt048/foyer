package deploycheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// sandbox is a FOYER_ROOT with real deploy scripts under src/deploy and stubs for
// what they call. Every `docker` invocation is logged with the database login the
// control plane would read at that moment, which is the fact the upgrade's order
// exists to protect.
type sandbox struct {
	root, log string
	env       []string
}

const dockerStub = `#!/bin/sh
printf 'docker %s | db=%s\n' "$*" "$(cat "$FOYER_ROOT/secrets/database_url" 2>/dev/null)" >> "$LOG"
case "$*" in
  *"migrate foyerctl migrate --status"*) exit 0 ;;
  *"run --rm migrate"*) [ -n "$FAIL_MIGRATE" ] && exit 1; exit 0 ;;
  *pg_dump*) echo "-- dump"; exit 0 ;;
esac
exit 0
`

// first-boot.sh's stand-in. `--secrets-only` does what the real one now does to an
// install from before 00019 — stages the foyer_app login — and `--promote-logins`
// runs the REAL script, which needs neither root nor openssl for that one step.
const firstBootStub = `#!/bin/bash
set -euo pipefail
echo "first-boot $*" >> "$LOG"
case "${1:-}" in
  --secrets-only)
    printf '%s\n' "$NEW_LOGIN" > "$FOYER_ROOT/secrets/database_url.next" ;;
  --promote-logins)
    exec bash "$FOYER_ROOT/src/deploy/first-boot.real.sh" "$@" ;;
esac
`

func newSandbox(t *testing.T, real ...string) *sandbox {
	t.Helper()
	root := t.TempDir()
	deploy := filepath.Join(root, "src", "deploy")
	for _, f := range real {
		b, err := os.ReadFile(filepath.Join(repoRoot, "deploy", f))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(deploy, f), string(b), 0o755)
	}
	// A stub never replaces a script the test asked for the real copy of.
	stub := func(name, body string) {
		for _, f := range real {
			if f == name {
				return
			}
		}
		write(t, filepath.Join(deploy, name), body, 0o755)
	}
	stub("consolenames.sh", "FOYER_ADMIN_TLS_NAMES=sandbox.test\nexport FOYER_ADMIN_TLS_NAMES\n")
	stub("secretdirs.sh", "FOYER_UID=$(id -u)\n")
	stub("verify.sh", "#!/bin/sh\necho verify >> \"$LOG\"\n")
	write(t, filepath.Join(root, "src", ".shipped-rev"), "0123abc\n", 0o644)
	write(t, filepath.Join(root, "secrets", "foyer.env"), "FOYER_PORTAL_URL=http://sandbox.test/\n", 0o600)
	write(t, filepath.Join(root, "secrets", "database_url"), oldLogin+"\n", 0o400)

	bin := filepath.Join(root, "bin")
	write(t, filepath.Join(bin, "docker"), dockerStub, 0o755)
	write(t, filepath.Join(bin, "sudo"), "#!/bin/sh\nexec \"$@\"\n", 0o755)

	sb := &sandbox{root: root, log: filepath.Join(root, "log")}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "FOYER_") || strings.HasPrefix(kv, "PATH=") {
			continue
		}
		sb.env = append(sb.env, kv)
	}
	sb.env = append(sb.env,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FOYER_ROOT="+root, "LOG="+sb.log, "NEW_LOGIN="+newLogin)
	return sb
}

// run executes a deploy script with the sandbox's environment and **no
// arguments**, plus any extra environment the caller names.
//
// The variadic is environment, not argv, and that is worth saying out loud
// because the mistake is easy: `sb.run(t, "restore.sh", "--dry-run", dump)`
// compiles, sets two environment variables called `--dry-run` and the dump
// path, runs the script with no arguments at all, and then fails on a script
// that is correct. Use runArgs when the script takes options.
func (sb *sandbox) run(t *testing.T, script string, extraEnv ...string) (string, error) {
	t.Helper()
	return sb.runArgs(t, script, nil, extraEnv...)
}

// runArgs executes a deploy script with arguments. Split from run because the
// two are not interchangeable, and the difference is invisible at the call site
// of the one that is wrong.
func (sb *sandbox) runArgs(t *testing.T, script string, args []string, extraEnv ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(sb.root, "src", "deploy", script)}, args...)...)
	cmd.Env = append(append([]string{}, sb.env...), extraEnv...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (sb *sandbox) lines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(sb.log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// index returns the first log line containing every one of parts, or -1.
func index(lines []string, parts ...string) int {
	for i, l := range lines {
		ok := true
		for _, p := range parts {
			if !strings.Contains(l, p) {
				ok = false
				break
			}
		}
		if ok {
			return i
		}
	}
	return -1
}

func upgradeSandbox(t *testing.T) *sandbox {
	// `backup.sh` and `backups.sh` are the real ones, not a stub. upgrade.sh's
	// step 1 calls backup.sh rather than taking its own dump, so a stub here
	// would leave the test asserting an order over a script the upgrade no
	// longer runs — which is how the tests passed while `upgrade.sh` stopped
	// backing anything up at all. The real backup.sh reaches compose.sh, which
	// the docker stub answers, and it needs a postgres_password to read.
	sb := newSandbox(t, "upgrade.sh", "images.sh", "images.conf", "logins.sh", "consolesetup.sh",
		"compose.yaml", "Dockerfile.foyer", "backup.sh", "backups.sh", "compose.sh")
	real, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "first-boot.sh"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(sb.root, "src", "deploy", "first-boot.real.sh"), string(real), 0o755)
	write(t, filepath.Join(sb.root, "src", "deploy", "first-boot.sh"), firstBootStub, 0o755)
	// What the real backup.sh reads: the database password for the dump, and a
	// KEK in the exact `id:base64key` shape first-boot.sh generates, because the
	// manifest's KEK fingerprint is taken from it.
	write(t, filepath.Join(sb.root, "secrets", "postgres_password"), "sandbox-pg-password\n", 0o600)
	write(t, filepath.Join(sb.root, "secrets", "kek"), "sandbox:kZWtleQ==\n", 0o400)
	return sb
}

// The upgrade's safety argument is its order, so the order is what is asserted:
// the old control plane keeps the login it was started with through the build, the
// stop and the migration, and the new login is in place before anything starts.
func TestUpgradeSwitchesTheLoginOnlyAfterMigrateGrantsIt(t *testing.T) {
	sb := upgradeSandbox(t)
	out, err := sb.run(t, "upgrade.sh")
	if err != nil {
		t.Fatalf("upgrade.sh failed in the sandbox: %v\n%s", err, out)
	}
	l := sb.lines(t)

	secrets := index(l, "first-boot --secrets-only")
	images := index(l, "docker compose", " build")
	stop := index(l, "docker compose", "stop freeradius foyer")
	migrate := index(l, "docker compose", "run --rm migrate |")
	promote := index(l, "first-boot --promote-logins")
	start := index(l, "docker compose", "up -d")
	for name, i := range map[string]int{"secrets": secrets, "images": images, "stop": stop, "migrate": migrate, "promote": promote, "start": start} {
		if i < 0 {
			t.Fatalf("no %s step in the log:\n%s\n\noutput:\n%s", name, strings.Join(l, "\n"), out)
		}
	}
	if !(secrets < images && images < stop && stop < migrate && migrate < promote && promote < start) {
		t.Fatalf("steps out of order (secrets %d, images %d, stop %d, migrate %d, promote %d, start %d):\n%s",
			secrets, images, stop, migrate, promote, start, strings.Join(l, "\n"))
	}
	for _, i := range []int{images, stop, migrate} {
		if !strings.HasSuffix(l[i], "db="+oldLogin) {
			t.Errorf("the control plane's login changed before migrate granted the new one:\n  %s", l[i])
		}
	}
	if !strings.HasSuffix(l[start], "db="+newLogin) {
		t.Errorf("the stack started without the new login in place:\n  %s", l[start])
	}
	if !strings.Contains(out, "promoted database_url") {
		t.Errorf("the real first-boot.sh --promote-logins did not say it promoted anything:\n%s", out)
	}
}

// A failed migration starts nothing and switches nothing: the previous image, if
// somebody starts it by hand, comes up on the login it had.
func TestAFailedMigrationLeavesTheLoginAlone(t *testing.T) {
	sb := upgradeSandbox(t)
	out, err := sb.run(t, "upgrade.sh", "FAIL_MIGRATE=1")
	if err == nil {
		t.Fatalf("upgrade.sh succeeded with a failing migration:\n%s", out)
	}
	l := sb.lines(t)
	if i := index(l, "first-boot --promote-logins"); i >= 0 {
		t.Errorf("the login was promoted after a failed migration:\n%s", strings.Join(l, "\n"))
	}
	if i := index(l, "docker compose", "up -d"); i >= 0 {
		t.Errorf("something was started after a failed migration:\n%s", strings.Join(l, "\n"))
	}
	if got := read(t, filepath.Join(sb.root, "secrets", "database_url")); got != oldLogin {
		t.Errorf("database_url = %q after a failed migration, want the old login", got)
	}
	if !strings.Contains(out, "NOTHING HAS BEEN STARTED") {
		t.Errorf("the failure did not say nothing was started:\n%s", out)
	}
}

// up.sh starts the stack in one `docker compose up`, which runs migrate before it
// recreates the control plane; the staged login must be in place by then.
func TestUpPromotesAStagedLoginBeforeStarting(t *testing.T) {
	sb := newSandbox(t, "up.sh", "images.sh", "images.conf", "logins.sh", "compose.yaml")
	for _, f := range []string{"postgres_password", "owner_database_url", "app_db_password", "radius_db_password",
		"reaper_db_password", "retention_db_password", "reaper_database_url", "retention_database_url", "maintenance_token"} {
		write(t, filepath.Join(sb.root, "secrets", f), "x\n", 0o400)
	}
	write(t, filepath.Join(sb.root, "secrets", "database_url.next"), newLogin+"\n", 0o400)
	out, err := sb.run(t, "up.sh")
	if err != nil {
		t.Fatalf("up.sh failed in the sandbox: %v\n%s", err, out)
	}
	l := sb.lines(t)
	start := index(l, "docker compose", "up -d")
	if start < 0 {
		t.Fatalf("up.sh never ran docker compose up:\n%s", strings.Join(l, "\n"))
	}
	if !strings.HasSuffix(l[start], "db="+newLogin) {
		t.Errorf("up.sh started the stack before promoting the staged login:\n  %s", l[start])
	}
}
