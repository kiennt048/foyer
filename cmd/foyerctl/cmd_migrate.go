package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/kiennt048/foyer/internal/config"
	"github.com/kiennt048/foyer/internal/store"
)

func init() {
	Register(&Command{
		Name:    "migrate",
		Usage:   "[--db <url>] [--status] [--to <version>]",
		Summary: "Apply the embedded database migrations",
		Run:     runMigrate,
	})
}

// `foyerctl migrate` applies the schema, and upgrades it.
//
// One SQL file mounted into the PostgreSQL image's entrypoint runs exactly once,
// on an empty data directory. That can install; it cannot upgrade anybody, and the upgrade path has to exist from the
// start precisely because the first person to install has to be able to upgrade.
//
// The connection must authenticate as a role that owns the schema. `foyer_app`
// deliberately cannot create a table, so pointing this at the application's own
// credentials fails with a permission error rather than half-applying anything.
func runMigrate(args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ExitOnError)
	dbDefault, err := envOrFileDefault("FOYER_DATABASE_URL")
	if err != nil {
		return err
	}
	dbURL := fs.String("db", dbDefault, "PostgreSQL connection URL")
	status := fs.Bool("status", false, "Report what is applied and what is pending, and change nothing")
	to := fs.Int64("to", -1, "Migrate to this version instead of the latest; may roll back")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dbURL == "" {
		return errors.New("--db or FOYER_DATABASE_URL is required")
	}

	ctx := context.Background()

	if *status {
		states, err := store.MigrationStatus(ctx, *dbURL)
		if err != nil {
			return err
		}
		pending := 0
		for _, s := range states {
			mark := "pending"
			if s.Applied {
				mark = "applied"
			} else {
				pending++
			}
			fmt.Printf("  %-8s  %s\n", mark, s.Name)
		}
		fmt.Printf("\n%d migration(s), %d pending\n", len(states), pending)
		return nil
	}

	before, err := store.MigrationStatus(ctx, *dbURL)
	if err != nil {
		return err
	}

	switch {
	case *to < 0:
		if err := store.Migrate(ctx, *dbURL); err != nil {
			return err
		}
	case *to < latestApplied(before):
		// Down and Up are separate calls in goose, and asking the wrong one is
		// not a no-op: `UpTo 0` finds no migration at or below version 0 and
		// fails with "no migration files found", which reads like a broken
		// build rather than a direction mistake. Decide the direction here.
		if err := store.MigrateDownTo(ctx, *dbURL, *to); err != nil {
			return err
		}
	case *to > latestApplied(before):
		if err := store.MigrateTo(ctx, *dbURL, *to); err != nil {
			return err
		}
	}

	after, err := store.MigrationStatus(ctx, *dbURL)
	if err != nil {
		return err
	}

	applied := appliedCount(after) - appliedCount(before)
	switch {
	case applied > 0:
		fmt.Printf("Applied %d migration(s); database is at version %d.\n", applied, latestApplied(after))
	case applied < 0:
		fmt.Printf("Rolled back %d migration(s); database is at version %d.\n", -applied, latestApplied(after))
	default:
		fmt.Printf("Database is already at version %d; nothing to do.\n", latestApplied(after))
	}

	if *to < 0 {
		return provisionLogins(ctx, *dbURL)
	}
	return nil
}

// provisionLogins sets the passwords the four login roles log in with:
// foyer_app, foyer_radius, foyer_reaper and foyer_retention, from
// FOYER_APP_DB_PASSWORD, FOYER_RADIUS_DB_PASSWORD, FOYER_REAPER_DB_PASSWORD and
// FOYER_RETENTION_DB_PASSWORD (or their _FILE forms). The last two since migration
// 00021: cmd/foyer's maintenance jobs run as those roles, and after 00019 the
// application's login may not SET ROLE into them.
//
// Here, and not in a separate command, because this is the one process that
// already holds the schema owner's credentials, already runs on every `up` and
// every upgrade, and already gates everything else in both compose files. A
// separate one-shot would be a second service holding a superuser password for
// no reason but tidiness. deploy/first-boot.sh generates every value; nothing in
// this repository supplies one.
//
// Set on every run rather than once, so that a password rotated in the secret
// file reaches the database by re-running what the deployment already runs. An
// unset variable is a note, not a failure: migrations are also run by hand, by
// harnesses and by `--status`, and none of those should need to know a secret.
func provisionLogins(ctx context.Context, ownerURL string) error {
	logins := []struct{ role, name, pw string }{
		{store.DefaultAppRole, "FOYER_APP_DB_PASSWORD", ""},
		{store.RadiusRole, "FOYER_RADIUS_DB_PASSWORD", ""},
		{store.ReaperRole, "FOYER_REAPER_DB_PASSWORD", ""},
		{store.RetentionRole, "FOYER_RETENTION_DB_PASSWORD", ""},
	}
	for i := range logins {
		pw, err := config.EnvOrFile(logins[i].name)
		if err != nil {
			return err
		}
		logins[i].pw = pw
	}
	for i := range logins {
		for j := i + 1; j < len(logins); j++ {
			if logins[i].pw != "" && logins[i].pw == logins[j].pw {
				// Two roles, one secret, is one credential with two names: whoever
				// reads FreeRADIUS's generated sql module could then log in as the
				// application, or as the one role that may DELETE accounting.
				return fmt.Errorf("%s and %s are the same value; they must be generated independently", logins[i].name, logins[j].name)
			}
		}
	}
	// The lab's literals are published in lab/docker-compose.yml. cmd/foyer refuses
	// a FOYER_DATABASE_URL carrying one, but FreeRADIUS has no such check, so
	// without this a deployment that copied the lab's migrate block would give
	// foyer_radius a password anyone can read on the internet — and the control
	// plane's refusal would be the only thing that looked wrong. Same list, same
	// acknowledgement, as config.Validate.
	lab := os.Getenv(config.LabInsecureVar) == config.LabAcknowledgement
	for _, l := range logins {
		if def, found := config.KnownDefault(l.pw); found && !lab {
			return fmt.Errorf("%s contains the known default %q; generate it at first boot (deploy/first-boot.sh does). "+
				"If this really is the lab, set %s=%s", l.name, def, config.LabInsecureVar, config.LabAcknowledgement)
		}
	}
	for _, l := range logins {
		if l.pw == "" {
			fmt.Printf("%s is not set: %s's password was left as it is. A role that never had one cannot log in.\n", l.name, l.role)
			continue
		}
		if err := store.SetLoginPassword(ctx, ownerURL, l.role, l.pw); err != nil {
			// Never the value: SetLoginPassword's errors do not carry it either.
			return fmt.Errorf("%s: %w", l.name, err)
		}
		fmt.Printf("Set the %s login's password from %s.\n", l.role, l.name)
	}
	return nil
}

func appliedCount(states []store.MigrationState) int {
	n := 0
	for _, s := range states {
		if s.Applied {
			n++
		}
	}
	return n
}

func latestApplied(states []store.MigrationState) int64 {
	var v int64
	for _, s := range states {
		if s.Applied && s.Version > v {
			v = s.Version
		}
	}
	return v
}
