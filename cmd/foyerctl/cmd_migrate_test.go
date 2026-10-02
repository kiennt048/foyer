package main

import (
	"context"
	"strings"
	"testing"
)

// The two login passwords are two secrets, or the split buys nothing.
//
// FreeRADIUS's password is in cleartext in the generated mods-available/sql —
// rlm_sql has no other way to authenticate — and `radiusd -X` prints the module.
// If it were also the application's password, whoever can read the data plane's
// configuration could log in as the control plane. Refused before any connection,
// which is why this needs no database.
func TestTheTwoLoginPasswordsMustDiffer(t *testing.T) {
	same := "0123456789abcdef0123456789abcdef"
	t.Setenv("FOYER_APP_DB_PASSWORD", same)
	t.Setenv("FOYER_RADIUS_DB_PASSWORD", same)

	err := provisionLogins(context.Background(), "postgres://nobody:unused@127.0.0.1:1/none")
	if err == nil || !strings.Contains(err.Error(), "same value") {
		t.Fatalf("identical login passwords were accepted: %v", err)
	}
	if strings.Contains(err.Error(), same) {
		t.Fatal("the refusal printed the password")
	}
}

// Unset is a note rather than a failure: `foyerctl migrate` is also run by hand
// and by harnesses that have no business holding a login secret.
func TestUnsetLoginPasswordsAreNotAFailure(t *testing.T) {
	t.Setenv("FOYER_APP_DB_PASSWORD", "")
	t.Setenv("FOYER_RADIUS_DB_PASSWORD", "")
	if err := provisionLogins(context.Background(), "postgres://nobody:unused@127.0.0.1:1/none"); err != nil {
		t.Fatalf("migrate with no login passwords set failed: %v", err)
	}
}

// The lab's published login passwords are refused outside the lab.
//
// cmd/foyer refuses a FOYER_DATABASE_URL carrying `foyer_lab_secret`, but
// FreeRADIUS checks nothing, so a deployment that copied lab/docker-compose.yml's
// migrate block would have given foyer_radius a password printed in this
// repository. Red before the check existed: the only error was a connection error
// to the unreachable URL below, not a refusal of the value. Refused before any
// connection, which is why this needs no database.
func TestTheLabsLoginPasswordsAreRefusedOutsideTheLab(t *testing.T) {
	generated := "0123456789abcdef0123456789abcdef"
	for _, c := range []struct{ name, app, radius string }{
		{"FOYER_APP_DB_PASSWORD", "foyer_lab_secret_app", generated},
		{"FOYER_RADIUS_DB_PASSWORD", generated, "foyer_lab_secret_radius"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("FOYER_LAB_INSECURE", "")
			t.Setenv("FOYER_APP_DB_PASSWORD", c.app)
			t.Setenv("FOYER_RADIUS_DB_PASSWORD", c.radius)
			err := provisionLogins(context.Background(), "postgres://nobody:unused@127.0.0.1:1/none")
			if err == nil || !strings.Contains(err.Error(), "known default") || !strings.Contains(err.Error(), c.name) {
				t.Fatalf("a lab password in %s was not refused as a known default: %v", c.name, err)
			}
		})
	}

	// And the lab, which says so in the exact sentence config.Validate wants, gets
	// past the check — to the connection, which fails here because nothing listens.
	t.Run("the lab, acknowledged", func(t *testing.T) {
		t.Setenv("FOYER_LAB_INSECURE", "i-am-a-lab-and-not-a-deployment")
		t.Setenv("FOYER_APP_DB_PASSWORD", "foyer_lab_secret_app")
		t.Setenv("FOYER_RADIUS_DB_PASSWORD", "foyer_lab_secret_radius")
		err := provisionLogins(context.Background(), "postgres://nobody:unused@127.0.0.1:1/none")
		if err == nil || strings.Contains(err.Error(), "known default") {
			t.Fatalf("the acknowledged lab was refused its own passwords, or nothing was attempted: %v", err)
		}
	})
}
