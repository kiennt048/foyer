package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every foyerctl command that takes a secret from the environment must accept the
// `_FILE` form, because `deploy/compose.yaml` supplies it that way and no other.
//
// A command that reads os.Getenv directly never sees it, and the migrate one-shot
// dies with
//
//	foyerctl migrate: --db or FOYER_DATABASE_URL is required
//
// while compose is passing FOYER_DATABASE_URL_FILE=/run/secrets/database_url.
// `internal/config` implements the convention and `cmd/foyer` uses it. The whole
// stack cannot start, and nothing in a test suite that never runs the shipped
// compose file can tell.
//
// This is `docs/rules/database.md`'s restated-constraint defect in a second language: the
// deployment file states a contract that the binary does not implement, and the
// two drifted because neither reads the other.
func TestEveryCommandThatTakesADatabaseURLAcceptsTheFileForm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "database_url")
	const want = "postgres://someone@example/db"
	if err := os.WriteFile(path, []byte(want+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The commands compose runs with a _FILE secret and no plain variable.
	for _, name := range []string{"migrate", "create-operator", "doctor", "sync-nas", "open-setup"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("FOYER_DATABASE_URL", "")
			os.Unsetenv("FOYER_DATABASE_URL")
			t.Setenv("FOYER_DATABASE_URL_FILE", path)

			cmd := commands[name]
			if cmd == nil {
				t.Fatalf("no command registered as %q", name)
			}
			// Run with no arguments. The command must NOT complain that the
			// database URL is missing: it is present, in the file form.
			err := cmd.Run(nil)
			if err == nil {
				return // it got far enough to try something else
			}
			if strings.Contains(err.Error(), "FOYER_DATABASE_URL is required") ||
				strings.Contains(err.Error(), "--db or") {
				t.Fatalf("%s did not read FOYER_DATABASE_URL_FILE: %v", name, err)
			}
		})
	}
}

// apply-raddb takes two secrets from the environment — FreeRADIUS's own database
// password, which it writes into mods-available/sql, and the database URL its
// startup snapshot reads the client table through. Read with os.Getenv, neither
// `_FILE` form reaches it. Red against such code: the plan carries an empty
// password and an empty URL.
func TestApplyRaddbReadsBothSecretsInTheFileForm(t *testing.T) {
	dir := t.TempDir()
	pwPath := filepath.Join(dir, "radius_db_password")
	urlPath := filepath.Join(dir, "database_url")
	if err := os.WriteFile(pwPath, []byte("from-the-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(urlPath, []byte("postgres://someone@example/db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"FOYER_POSTGRES_PASSWORD", "FOYER_DATABASE_URL"} {
		t.Setenv(v, "")
		os.Unsetenv(v)
	}
	t.Setenv("FOYER_POSTGRES_PASSWORD_FILE", pwPath)
	t.Setenv("FOYER_DATABASE_URL_FILE", urlPath)
	t.Setenv("FOYER_PROBE_SECRET", "a-probe-secret-for-this-test")

	plan, _, err := parseApplyRaddb([]string{"--root", t.TempDir(), "--probe-dir", probeDir(t)})
	if err != nil {
		t.Fatalf("parseApplyRaddb: %v", err)
	}
	if plan.cfg.PostgresPassword != "from-the-file" {
		t.Errorf("FOYER_POSTGRES_PASSWORD_FILE was not read: the generated tree would carry %q", plan.cfg.PostgresPassword)
	}
	if plan.dbURL != "postgres://someone@example/db" {
		t.Errorf("FOYER_DATABASE_URL_FILE was not read: the snapshot would use %q", plan.dbURL)
	}
}

// The restart command is a flag a developer types and nothing else.
// FOYER_RADIUSD_RESTART_COMMAND must not be read by anything, or a stale
// environment that still sets it would run it.
func TestApplyRaddbReadsNoRestartCommandFromTheEnvironment(t *testing.T) {
	t.Setenv("FOYER_RADIUSD_RESTART_COMMAND", "touch /tmp/should-never-run")
	t.Setenv("FOYER_PROBE_SECRET", "a-probe-secret-for-this-test")
	t.Setenv("FOYER_POSTGRES_PASSWORD", "x")
	plan, _, err := parseApplyRaddb([]string{"--root", t.TempDir(), "--probe-dir", probeDir(t)})
	if err != nil {
		t.Fatalf("parseApplyRaddb: %v", err)
	}
	if plan.restartCommand != "" {
		t.Fatalf("apply-raddb took %q from the environment", plan.restartCommand)
	}
}
