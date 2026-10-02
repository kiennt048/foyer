package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `FOYER_DATABASE_URL` and `FOYER_KEK` had no `_FILE` form, so both were readable
// through `docker inspect` and `/proc/<pid>/environ`.
//
// `FOYER_KEK` is the one that matters most: it unseals every RADIUS shared
// secret, every controller credential and every NT hash in the database, and an
// NT hash is password-equivalent (`docs/rules/security.md` §1). PostgreSQL avoids exactly
// this with `POSTGRES_PASSWORD_FILE`, and the shape is copied rather than
// invented — including trimming only the trailing newline, because that is what
// `printf '%s\n' > file` leaves behind and a password may legitimately end in a
// space.
func TestASecretCanArriveInAFileInsteadOfTheEnvironment(t *testing.T) {
	dir := t.TempDir()
	kekPath := filepath.Join(dir, "kek")
	urlPath := filepath.Join(dir, "database_url")
	writeFile(t, kekPath, "2026-08:c2VjcmV0\n")
	writeFile(t, urlPath, "postgres://foyer:generated@db:5432/foyer?sslmode=require\n")

	t.Setenv("FOYER_KEK_FILE", kekPath)
	t.Setenv("FOYER_DATABASE_URL_FILE", urlPath)

	c := Load()
	if err := c.Validate(); err != nil {
		t.Fatalf("a configuration whose secrets came from files was refused: %v", err)
	}
	if c.KEK != "2026-08:c2VjcmV0" {
		t.Errorf("FOYER_KEK_FILE produced %q; the trailing newline should be gone and nothing else", c.KEK)
	}
	if !strings.HasPrefix(c.DatabaseURL, "postgres://foyer:generated@") {
		t.Errorf("FOYER_DATABASE_URL_FILE produced %q", c.DatabaseURL)
	}
}

// Only the trailing newline. A secret is bytes somebody generated, and helpfully
// trimming a trailing space turns "the key does not work" into a mystery.
func TestOnlyTheTrailingNewlineIsTrimmed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "kek")
	writeFile(t, p, "  2026-08:key with spaces  \r\n")
	t.Setenv("FOYER_KEK_FILE", p)

	if got := Load().KEK; got != "  2026-08:key with spaces  " {
		t.Errorf("got %q, want the value with its interior and leading whitespace intact", got)
	}
}

// Two sources for one secret is a configuration nobody meant, and picking a
// winner silently is how a rotated key goes unnoticed for a week. It is refused,
// and the refusal names both variables.
func TestASecretGivenTwiceIsRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "kek")
	writeFile(t, p, "2026-08:from-the-file\n")
	t.Setenv("FOYER_KEK_FILE", p)
	t.Setenv("FOYER_KEK", "2026-08:from-the-environment")
	t.Setenv("FOYER_DATABASE_URL", "postgres://foyer:generated@db:5432/foyer")

	err := Load().Validate()
	if err == nil {
		t.Fatal("a secret supplied twice was accepted, and one of the two was silently ignored")
	}
	for _, want := range []string{"FOYER_KEK", "FOYER_KEK_FILE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s: %v", want, err)
		}
	}
	// And it must not leak either value while complaining about them.
	for _, secret := range []string{"from-the-file", "from-the-environment"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the refusal printed the secret itself: %v", err)
		}
	}
}

// A `_FILE` pointing at nothing is a refusal that names the path, not a silent
// fallback to an empty key. An empty KEK is permitted on purpose — `internal/store`
// returns ErrNoKeyring where it is needed — so a misconfigured mount would
// otherwise look exactly like a deliberate absence.
func TestAMissingSecretFileIsRefusedByPath(t *testing.T) {
	t.Setenv("FOYER_DATABASE_URL", "postgres://foyer:generated@db:5432/foyer")
	t.Setenv("FOYER_KEK_FILE", "/nonexistent/kek")

	err := Load().Validate()
	if err == nil {
		t.Fatal("a FOYER_KEK_FILE pointing at nothing was accepted as an absent key")
	}
	if !strings.Contains(err.Error(), "/nonexistent/kek") {
		t.Errorf("the refusal does not name the path: %v", err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// FreeRADIUS's own database password reaches the control plane only when the
// control plane writes FreeRADIUS's tree, and then in the _FILE form too.
//
// The tree does not take its login from FOYER_DATABASE_URL, which is foyer_app's
// (migration 00019) and must not be the data plane's.
func TestTheFreeRADIUSPasswordIsReadOnlyForAGeneratedTree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "radius_db_password")
	writeFile(t, path, "from-the-file\n")
	t.Setenv("FOYER_POSTGRES_PASSWORD", "")
	t.Setenv("FOYER_POSTGRES_PASSWORD_FILE", path)

	t.Setenv("FOYER_RADDB_TARGET", "")
	if got := Load().RaddbPostgresPassword; got != "" {
		t.Fatalf("with no FOYER_RADDB_TARGET the control plane read FreeRADIUS's password anyway (%d bytes)", len(got))
	}

	t.Setenv("FOYER_RADDB_TARGET", t.TempDir())
	if got := Load().RaddbPostgresPassword; got != "from-the-file" {
		t.Fatalf("FOYER_POSTGRES_PASSWORD_FILE was not read for a generated tree: got %d bytes", len(got))
	}
}
