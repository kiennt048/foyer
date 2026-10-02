package deploycheck

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const repoRoot = "../.."

// The two logins every test here moves between: an install from before migration
// 00019 logged the control plane in as the superuser, and the new tree logs it in
// as foyer_app, which only `foyerctl migrate` grants.
const (
	oldLogin = "postgres://foyer:old@postgres:5432/foyer?sslmode=disable"
	newLogin = "postgres://foyer_app:new@postgres:5432/foyer?sslmode=disable"
)

func owner() string { return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()) }

// runBash runs script with deploy/logins.sh sourced, in dir, and returns stdout.
func runBash(t *testing.T, dir, script string) string {
	t.Helper()
	logins, err := filepath.Abs(filepath.Join(repoRoot, "deploy", "logins.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", "-c", "set -euo pipefail; . \"$LOGINS\"; "+script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "LOGINS="+logins, "OWNER="+owner())
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	return string(out)
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(b), "\n")
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

// A changed login is written beside the live one and the live one is left alone:
// the process that is running keeps the credential the database accepts.
func TestAChangedDatabaseLoginIsStagedNotSwitched(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "database_url")
	write(t, live, oldLogin+"\n", 0o400)

	got := runBash(t, dir, `foyer_stage_login_url database_url "$OWNER" '`+newLogin+`'`)
	if strings.TrimSpace(got) != "staged" {
		t.Fatalf("stage reported %q, want staged", got)
	}
	if read(t, live) != oldLogin {
		t.Fatalf("the live login was rewritten before anything granted the new one: %q", read(t, live))
	}
	if read(t, live+".next") != newLogin {
		t.Fatalf("database_url.next = %q, want the new login", read(t, live+".next"))
	}
	if fi, _ := os.Stat(live + ".next"); fi.Mode().Perm() != 0o400 {
		t.Fatalf("database_url.next is %v; it must already be 0400, so promotion is a bare rename", fi.Mode().Perm())
	}

	runBash(t, dir, `foyer_promote_logins .`)
	if read(t, live) != newLogin {
		t.Fatalf("after promotion database_url = %q, want the new login", read(t, live))
	}
	if _, err := os.Stat(live + ".next"); !os.IsNotExist(err) {
		t.Fatalf("database_url.next survived its promotion: %v", err)
	}
}

// A fresh install has nothing running to protect, so the file is written directly;
// an unchanged one is left as it is and a stale staged copy is dropped.
func TestAFreshOrUnchangedLoginIsNotStaged(t *testing.T) {
	dir := t.TempDir()
	if got := strings.TrimSpace(runBash(t, dir, `foyer_stage_login_url database_url "$OWNER" '`+newLogin+`'`)); got != "written" {
		t.Fatalf("fresh install: %q, want written", got)
	}
	if read(t, filepath.Join(dir, "database_url")) != newLogin {
		t.Fatal("fresh install: database_url not written")
	}
	write(t, filepath.Join(dir, "database_url.next"), "stale\n", 0o400)
	if got := strings.TrimSpace(runBash(t, dir, `foyer_stage_login_url database_url "$OWNER" '`+newLogin+`'`)); got != "unchanged" {
		t.Fatalf("second run: %q, want unchanged", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "database_url.next")); !os.IsNotExist(err) {
		t.Fatal("an unchanged login left a stale database_url.next to be promoted later")
	}
	if got := runBash(t, dir, `foyer_promote_logins .`); !strings.Contains(got, "no staged") {
		t.Fatalf("promotion with nothing staged said %q", got)
	}
}

// first-boot.sh must go through the stager for the one file a running control
// plane reads, and must never write it directly again.
func TestFirstBootStagesTheControlPlaneLogin(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot, "deploy", "first-boot.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, `foyer_stage_login_url "${SECRETS}/database_url"`) {
		t.Error("first-boot.sh does not stage database_url through foyer_stage_login_url")
	}
	for _, direct := range []string{`> "${SECRETS}/database_url"`, `foyer_write_secret_file "${SECRETS}/database_url"`} {
		if strings.Contains(s, direct) {
			t.Errorf("first-boot.sh writes database_url directly (%s): a running control plane re-reads it on restart", direct)
		}
	}
}
