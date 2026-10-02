package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver, for goose
	"github.com/pressly/goose/v3"
)

// The schema, embedded.
//
// It is embedded rather than read from disk because the binary that applies a
// migration must be the binary that was built with it. A `migrations/` directory
// shipped alongside is a directory that can be a different version from the code
// reading it, and the first person to hit that discovers it at 2am during an
// upgrade.
//
// A schema file applied once by the PostgreSQL entrypoint to an empty data
// directory could never upgrade anybody. `docs/architecture.md` §12 puts the
// upgrade path in the first release for that reason: the first person to install
// has to be able to upgrade.
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// goose's dialect and base filesystem are process-global. Nothing else in this
// binary uses goose, but a test running Migrate concurrently would race on them.
var gooseMu sync.Mutex

const migrationDir = "migrations"

// MigrationState is one migration and whether it has been applied.
type MigrationState struct {
	Version int64
	Name    string
	Applied bool
}

// Migrate applies every pending migration, in order, to the database at url.
//
// It is idempotent: applying to an already-current database is a no-op that
// succeeds. The connection must authenticate as a role that owns the schema --
// `foyer_app` deliberately cannot create a table.
func Migrate(ctx context.Context, url string) error {
	return withGoose(ctx, url, func(db *sql.DB) error {
		return goose.UpContext(ctx, db, migrationDir)
	})
}

// MigrateTo applies migrations up to and including version.
func MigrateTo(ctx context.Context, url string, version int64) error {
	return withGoose(ctx, url, func(db *sql.DB) error {
		return goose.UpToContext(ctx, db, migrationDir, version)
	})
}

// MigrateDownTo rolls back to version, which may be 0 for "nothing applied".
//
// Down migrations exist so that the schema can be exercised in both directions in
// a test; a production rollback of a migration that dropped a column is a restore
// from backup, not a Down. Every migration in this package has a Down and
// migrate_test.go walks the whole set down and back up.
func MigrateDownTo(ctx context.Context, url string, version int64) error {
	return withGoose(ctx, url, func(db *sql.DB) error {
		return goose.DownToContext(ctx, db, migrationDir, version)
	})
}

// MigrationStatus reports every embedded migration and whether it is applied.
func MigrationStatus(ctx context.Context, url string) ([]MigrationState, error) {
	names, err := migrationNames()
	if err != nil {
		return nil, err
	}

	var applied map[int64]bool
	err = withGoose(ctx, url, func(db *sql.DB) error {
		// EnsureDBVersion creates goose's own table if the database has never
		// been migrated, so `status` on an empty database reports "none applied"
		// rather than failing on a missing relation.
		if _, err := goose.EnsureDBVersionContext(ctx, db); err != nil {
			return err
		}
		rows, err := db.QueryContext(ctx,
			fmt.Sprintf("SELECT version_id FROM %s WHERE is_applied", goose.TableName()))
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		applied = map[int64]bool{}
		for rows.Next() {
			var v int64
			if err := rows.Scan(&v); err != nil {
				return err
			}
			applied[v] = true
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}

	out := make([]MigrationState, 0, len(names))
	for _, n := range names {
		out = append(out, MigrationState{Version: n.version, Name: n.name, Applied: applied[n.version]})
	}
	return out, nil
}

func withGoose(ctx context.Context, url string, fn func(*sql.DB) error) error {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return fmt.Errorf("store: opening database for migration: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("store: connecting for migration: %w", err)
	}

	gooseMu.Lock()
	defer gooseMu.Unlock()

	goose.SetBaseFS(migrationFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("store: selecting migration dialect: %w", err)
	}
	return fn(db)
}

type migrationName struct {
	version int64
	name    string
}

func migrationNames() ([]migrationName, error) {
	entries, err := fs.ReadDir(migrationFS, migrationDir)
	if err != nil {
		return nil, err
	}
	out := make([]migrationName, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		var version int64
		base := e.Name()
		if i := strings.Index(base, "_"); i > 0 {
			if _, err := fmt.Sscanf(base[:i], "%d", &version); err != nil {
				return nil, fmt.Errorf("store: migration %q has no leading version number", base)
			}
		}
		out = append(out, migrationName{version: version, name: base})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
