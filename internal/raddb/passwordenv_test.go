package raddb

import (
	"strings"
	"testing"
)

// TestATreeCanNameItsDatabasePasswordWithoutHoldingIt is the generator half of the
// named database password: a tree with FreeRADIUS's database password written
// into mods-available/sql means the control plane's container has to hold that
// password — and a compromised control plane could INSERT and UPDATE radacct as
// FreeRADIUS. With PostgresPasswordEnv the tree names the password as
// `$ENV{…}` (expanded by radiusd at parse, MEASURED on 3.2.10) and carries none.
func TestATreeCanNameItsDatabasePasswordWithoutHoldingIt(t *testing.T) {
	dir := generate(t, func(c *Config) {
		c.PostgresPassword = ""
		c.PostgresPasswordEnv = "FOYER_RADIUS_DB_PASSWORD"
	})
	sql := readGenerated(t, dir, "mods-available", "sql")
	if !strings.Contains(sql, "\n    password = $ENV{FOYER_RADIUS_DB_PASSWORD}\n") {
		t.Fatalf("mods-available/sql does not read its password from the environment:\n%s", sql)
	}
	if strings.Contains(sql, `password = "`) {
		t.Fatalf("mods-available/sql still carries a literal password:\n%s", sql)
	}

	// The literal form is unchanged for the deployments that use it.
	lit := readGenerated(t, generate(t, nil), "mods-available", "sql")
	if !strings.Contains(lit, `password = "pg-password-for-this-test-only"`) || strings.Contains(lit, "$ENV{") {
		t.Fatalf("the literal-password tree changed:\n%s", lit)
	}
}

func TestPasswordEnvIsRefusedWhenAmbiguousOrNotAName(t *testing.T) {
	for _, tc := range []struct {
		name     string
		password string
		env      string
	}{
		{"both a password and an env name", "pg-password-for-this-test-only", "FOYER_RADIUS_DB_PASSWORD"},
		{"a lower-case name", "", "foyer_radius_db_password"},
		{"a name that closes the expansion", "", "X}"},
		{"a name with a space", "", "A B"},
		{"a name led by a digit", "", "1X"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := DefaultConfig()
			c.PostgresPassword, c.PostgresPasswordEnv = tc.password, tc.env
			if err := NewGenerator(c).Validate(); err == nil {
				t.Fatalf("PostgresPassword=%q PostgresPasswordEnv=%q was accepted", tc.password, tc.env)
			}
		})
	}
}

// TestChangingHowThePasswordIsNamedIsARestart: FreeRADIUS reads rlm_sql's
// configuration once, so moving between the literal and the environment form is a
// change the running server does not see until it restarts.
func TestChangingHowThePasswordIsNamedIsARestart(t *testing.T) {
	a := DefaultConfig()
	a.PostgresPassword = "pg-password-for-this-test-only"
	b := DefaultConfig()
	b.PostgresPasswordEnv = "FOYER_RADIUS_DB_PASSWORD"
	found := false
	for _, d := range configDiffs(a, b) {
		if strings.HasPrefix(d, "PostgresPasswordEnv ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a move to the environment form is not reported as a change: %v", configDiffs(a, b))
	}
}
