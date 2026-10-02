package main

import (
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/store"
)

// TestTheControlPlaneCanGenerateATreeWithoutHoldingFreeRADIUSsPassword: a control
// plane container holding FreeRADIUS's database password only so it can write it
// into mods-available/sql lets a compromise of the control plane INSERT and UPDATE
// radacct as FreeRADIUS. With
// FOYER_RADDB_POSTGRES_PASSWORD_ENV the tree names the password as `$ENV{…}` and
// this process is given none; setting both is refused.
func TestTheControlPlaneCanGenerateATreeWithoutHoldingFreeRADIUSsPassword(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		DatabaseURL:              "postgres://foyer_app:the-applications-password@db.internal:5432/foyer",
		RaddbPostgresPasswordEnv: "FOYER_RADIUS_DB_PASSWORD",
		PolicyAddr:               "127.0.0.1:8080",
		RaddbTarget:              dir,
	}
	if err := generateRaddb(cfg); err != nil {
		t.Fatalf("generating with no FreeRADIUS password in this process: %v", err)
	}
	sql := readTree(t, dir, "mods-available", "sql")
	if !strings.Contains(sql, `login = "`+store.RadiusRole+`"`) || !strings.Contains(sql, "password = $ENV{FOYER_RADIUS_DB_PASSWORD}") {
		t.Fatalf("the generated sql module does not read FreeRADIUS's password from its environment:\n%s", sql)
	}

	cfg.RaddbPostgresPassword = "freeradius-own-generated-password"
	cfg.RaddbTarget = t.TempDir()
	if err := generateRaddb(cfg); err == nil || !strings.Contains(err.Error(), "FOYER_RADDB_POSTGRES_PASSWORD_ENV") {
		t.Fatalf("both a password and an environment name were accepted: %v", err)
	}
}
