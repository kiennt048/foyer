package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/store"
)

// TestMigrateIsIdempotent runs `foyerctl migrate` twice against a database that is
// already current.
//
// This test's world is a clone of the template TestMain migrated, so this is the
// second and third application. An install script, a compose file and a human all
// run migrate more than once, and the run that is not the first is the one that
// has to be a no-op rather than an error.
func TestMigrateIsIdempotent(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	before, err := store.MigrationStatus(ctx, ownerURL(t))
	fatalf(t, err, "reading migration status")
	if len(before) == 0 {
		t.Fatal("no migrations are embedded")
	}
	for _, s := range before {
		if !s.Applied {
			t.Fatalf("migration %s is pending after TestMain applied everything", s.Name)
		}
	}

	fatalf(t, store.Migrate(ctx, ownerURL(t)), "second migrate")
	fatalf(t, store.Migrate(ctx, ownerURL(t)), "third migrate")

	after, err := store.MigrationStatus(ctx, ownerURL(t))
	fatalf(t, err, "reading migration status again")
	if len(after) != len(before) {
		t.Fatalf("migration count changed from %d to %d", len(before), len(after))
	}
}

// TestMigrationsAreOrderedAndReversible walks the whole set down to nothing and
// back up, on a database of its own.
//
// A Down is not a production rollback -- undoing a migration that dropped a column
// is a restore from backup. It exists so the schema can be exercised in both
// directions here, which is what catches a migration that drops a table another one
// still references, and an ordering that only works because the database happened
// to already have the object.
func TestMigrationsAreOrderedAndReversible(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	url := freshDatabase(t)

	fatalf(t, store.Migrate(ctx, url), "migrating the fresh database up")

	states, err := store.MigrationStatus(ctx, url)
	fatalf(t, err, "reading status")
	for _, s := range states {
		if !s.Applied {
			t.Fatalf("migration %s did not apply", s.Name)
		}
	}

	fatalf(t, store.MigrateDownTo(ctx, url, 0), "migrating all the way down")

	states, err = store.MigrationStatus(ctx, url)
	fatalf(t, err, "reading status after down")
	for _, s := range states {
		if s.Applied {
			t.Fatalf("migration %s is still applied after migrating down to 0", s.Name)
		}
	}

	fatalf(t, store.Migrate(ctx, url), "migrating back up")
}

// freshDatabase creates an empty, unmigrated database inside the shared container
// and returns its URL. Unlike world, it is not cloned from the template: it is for
// the tests that are about applying migrations at all.
func freshDatabase(t *testing.T) string {
	t.Helper()
	requireDocker(t)
	name := strings.ReplaceAll(uniq("db"), "-", "_")

	// CREATE DATABASE cannot run inside a transaction and cannot take a parameter,
	// so the name is interpolated -- from randomString, which is base64 of
	// crypto/rand and cannot contain a quote.
	_, err := adminPool.Exec(context.Background(), `CREATE DATABASE "`+name+`"`)
	fatalf(t, err, "creating a database for this test")

	t.Cleanup(func() {
		_, _ = adminPool.Exec(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
	})
	return dbURL(name)
}
