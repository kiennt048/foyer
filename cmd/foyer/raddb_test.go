package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/store"
)

// What `FOYER_RADDB_TARGET` produces, and why a naive version produces nothing
// usable.
//
// A `generateRaddb` that calls `raddb.DefaultConfig()` and overrides exactly one
// field, `PolicyAPIURL`, leaves `PostgresPassword` empty, and the generator
// refuses — correctly, because `read_clients = yes` reads the client table before
// the first packet and a FreeRADIUS that cannot reach PostgreSQL does not start.
// The control-plane path is then broken and warns once at startup.
//
// The answer is not "add FOYER_POSTGRES_PASSWORD to the control plane's
// environment". The connection string is already here, and it is the one this
// process is itself connected with — so the tree is derived from it rather than
// from a second variable that can disagree with it. A generated `rlm_sql` block
// pointing at a different database than the control plane uses is a failure with
// no symptom until an operator wonders why a new access point never appears.
//
// **The location is derived and the credential is not.** The
// control plane logs in as foyer_app (migration 00019), and a
// tree carrying that login would start, load its clients, and then fail every
// Accounting-Request: foyer_app may not write radacct. So WHERE comes from
// FOYER_DATABASE_URL and WHO is FreeRADIUS's own role, with its own password.
func TestTheGeneratedTreeIsDerivedFromTheDatabaseThisProcessUses(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		DatabaseURL:           "postgres://foyer_app:the-applications-password@db.internal:5433/foyer_db?sslmode=disable",
		RaddbPostgresPassword: "freeradius-own-generated-password",
		PolicyAddr:            "127.0.0.1:8080",
		AdminAddr:             "127.0.0.1:8443",
		PortalAddr:            "0.0.0.0:8081",
		RaddbTarget:           dir,
	}

	if err := generateRaddb(cfg); err != nil {
		t.Fatalf("generating into FOYER_RADDB_TARGET: %v", err)
	}

	sql := readTree(t, dir, "mods-available", "sql")
	for _, want := range []string{
		`server = "db.internal"`,
		`port = 5433`,
		`radius_db = "foyer_db"`,
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("the generated sql module does not carry %s — it is not the database this process is connected to", want)
		}
	}
}

// The tree never carries the control plane's own login.
//
// Red against the code this replaced, which copied user and password out of
// FOYER_DATABASE_URL: the data plane then held the application's credential —
// until 00019, the superuser's — in cleartext in mods-available/sql, which
// `radiusd -X` prints and every FreeRADIUS container can read.
func TestTheGeneratedTreeLogsInAsFreeRADIUSAndNeverAsTheApplication(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		DatabaseURL:           "postgres://foyer_app:the-applications-password@db.internal:5432/foyer",
		RaddbPostgresPassword: "freeradius-own-generated-password",
		PolicyAddr:            "127.0.0.1:8080",
		RaddbTarget:           dir,
	}
	if err := generateRaddb(cfg); err != nil {
		t.Fatalf("generating into FOYER_RADDB_TARGET: %v", err)
	}
	sql := readTree(t, dir, "mods-available", "sql")
	if !strings.Contains(sql, `login = "`+store.RadiusRole+`"`) {
		t.Errorf("the generated sql module does not log in as %s", store.RadiusRole)
	}
	if !strings.Contains(sql, `password = "freeradius-own-generated-password"`) {
		t.Error("the generated sql module does not carry FreeRADIUS's own password")
	}
	for _, leaked := range []string{"foyer_app", "the-applications-password"} {
		if strings.Contains(sql, leaked) {
			t.Errorf("the generated sql module carries %q, which is the control plane's credential", leaked)
		}
	}
}

// config spells the role again because internal/store imports it. This holds the
// two spellings equal, so a rename cannot leave the generator logging in as a role
// that no longer exists.
func TestTheRadiusUserIsTheStoresRadiusRole(t *testing.T) {
	if config.RadiusDatabaseUser != store.RadiusRole {
		t.Fatalf("config.RadiusDatabaseUser = %q, store.RadiusRole = %q", config.RadiusDatabaseUser, store.RadiusRole)
	}
}

// A configuration this process could not use is a refusal, not a tree generated
// against the defaults. The defaults are `postgres:5432/foyer`, which on a
// deployment is a plausible-looking wrong answer.
func TestAnUnusableConnectionStringDoesNotFallBackToTheDefaults(t *testing.T) {
	for _, c := range []struct {
		name, url, radiusPassword, names string
	}{
		{"an unparseable URL", "postgres://foyer_app:pw@db.internal:notaport/foyer", "a-password", "FOYER_DATABASE_URL"},
		{"no database named", "postgres://foyer_app:pw@db.internal:5432", "a-password", "FOYER_DATABASE_URL"},
		{"no FreeRADIUS password", "postgres://foyer_app:pw@db.internal:5432/foyer", "", "FOYER_POSTGRES_PASSWORD"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := config.Config{
				DatabaseURL:           c.url,
				RaddbPostgresPassword: c.radiusPassword,
				RaddbTarget:           dir,
				PolicyAddr:            "127.0.0.1:8080",
			}
			err := generateRaddb(cfg)
			if err == nil {
				t.Fatal("an unusable configuration generated a tree anyway")
			}
			if !strings.Contains(err.Error(), c.names) {
				t.Errorf("the refusal does not name the variable to change (%s): %v", c.names, err)
			}
			if entries, _ := os.ReadDir(dir); len(entries) != 0 {
				t.Errorf("the refused generation left %d entries behind", len(entries))
			}
		})
	}
}

// The mTLS material follows the policy listener's own configuration, so the tree
// cannot say https while the rest module presents nothing — an ordering hazard,
// on the path `deploy/` does not use.
func TestTheGeneratedTreeCarriesTheSameBoundaryTheListenerServes(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		DatabaseURL:           "postgres://foyer_app:pw@db:5432/foyer",
		RaddbPostgresPassword: "freeradius-own-generated-password",
		PolicyAddr:            "0.0.0.0:8080",
		PolicyTLSCert:         "/run/foyer/tls/policy.crt",
		PolicyTLSKey:          "/run/foyer/tls/policy.key",
		PolicyClientCA:        "/run/foyer/tls/ca.crt",
		RaddbPolicyClientCert: "/run/foyer/tls/radius.crt",
		RaddbPolicyClientKey:  "/run/foyer/tls/radius.key",
		RaddbPolicyServerCA:   "/run/foyer/tls/ca.crt",
		RaddbTarget:           dir,
		PolicyHost:            "foyer:8080",
	}

	if err := generateRaddb(cfg); err != nil {
		t.Fatalf("generating a TLS tree: %v", err)
	}
	rest := readTree(t, dir, "mods-available", "rest")
	for _, want := range []string{"https://foyer:8080/", "ca_info_file", "certificate_file", "private_key_file"} {
		if !strings.Contains(rest, want) {
			t.Errorf("the generated rest module does not carry %s", want)
		}
	}

	// And the other half of the interlock: TLS on the listener with no client
	// material for the tree must refuse rather than emit an https URL the rest
	// module cannot authenticate to.
	half := cfg
	half.RaddbPolicyClientCert = ""
	half.RaddbTarget = t.TempDir()
	if err := generateRaddb(half); err == nil {
		t.Error("a tree pointing at https with no client certificate was generated")
	}
}

func readTree(t *testing.T, dir string, parts ...string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(append([]string{dir}, parts...)...))
	if err != nil {
		t.Fatalf("reading %v: %v", parts, err)
	}
	return string(b)
}
