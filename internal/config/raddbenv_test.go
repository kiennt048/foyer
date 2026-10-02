package config

import (
	"strings"
	"testing"
)

// TestTheFreeRADIUSPasswordCanBeNamedInsteadOfHeld: a control plane
// given FOYER_RADDB_POSTGRES_PASSWORD_ENV generates a tree that reads FreeRADIUS's
// password from FreeRADIUS's own environment, and so needs no FOYER_POSTGRES_PASSWORD;
// given both, it refuses rather than choosing.
func TestTheFreeRADIUSPasswordCanBeNamedInsteadOfHeld(t *testing.T) {
	t.Setenv("FOYER_DATABASE_URL", "postgres://foyer_app:pw@db:5432/foyer")
	t.Setenv("FOYER_RADDB_TARGET", t.TempDir())
	t.Setenv("FOYER_POSTGRES_PASSWORD", "")
	t.Setenv("FOYER_POSTGRES_PASSWORD_FILE", "")
	t.Setenv("FOYER_RADDB_POSTGRES_PASSWORD_ENV", "FOYER_RADIUS_DB_PASSWORD")

	c := Load()
	p, err := c.Postgres()
	if err != nil {
		t.Fatalf("with the password named and not held, Postgres() refused: %v", err)
	}
	if p.Password != "" || p.PasswordEnv != "FOYER_RADIUS_DB_PASSWORD" || p.User != RadiusDatabaseUser {
		t.Fatalf("Postgres() = %+v, want no password, the env name, and FreeRADIUS's own login", p)
	}

	t.Setenv("FOYER_POSTGRES_PASSWORD", "also-a-password")
	if _, err := Load().Postgres(); err == nil || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("a held password beside a named one was accepted: %v", err)
	}
}
