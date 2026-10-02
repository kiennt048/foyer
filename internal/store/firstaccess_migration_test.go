package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/kiennt048/foyer/internal/store"
)

// TestFirstAccessMigrationDeletesOnlyTheShippedAdmin: migration 00040 closes the
// published password by deleting the operator `first-boot.sh` created with it —
// `admin`, still on its temporary password — and nobody else, not even another
// operator on a temporary password an administrator has just issued. Driven from
// 00039 up, on a database of its own.
func TestFirstAccessMigrationDeletesOnlyTheShippedAdmin(t *testing.T) {
	ctx := context.Background()
	url := freshDatabase(t)
	fatalf(t, store.MigrateTo(ctx, url, 39), "migrating to 00039")

	cfg, err := pgxpool.ParseConfig(url)
	fatalf(t, err, "parsing")
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, `SELECT set_config('foyer.tenant_id', $1, false)`, store.DefaultTenantID)
		return err
	}
	owner, err := pgxpool.NewWithConfig(ctx, cfg)
	fatalf(t, err, "owner pool")
	defer owner.Close()
	const hash = "$argon2id$v=19$m=65536,t=3,p=4$c2FsdA$aGFzaA"
	for _, op := range []struct {
		id, username string
		mustChange   bool
	}{
		{"op-shipped", "admin", true},         // the shipped credential
		{"op-temporary", "linh", true},        // a temporary password an administrator issued
		{"op-changed", "admin-2", false},      // an ordinary operator
		{"op-admin-changed", "adminx", false}, // not the name, not the flag
	} {
		_, err := owner.Exec(ctx, `INSERT INTO operators (id, username, password_hash, role, must_change_password)
			VALUES ($1, $2, $3, 'administrator', $4)`, op.id, op.username, hash, op.mustChange)
		fatalf(t, err, "seeding %s", op.id)
	}

	fatalf(t, store.MigrateTo(ctx, url, 40), "migrating to 00040")

	for id, want := range map[string]int{"op-shipped": 0, "op-temporary": 1, "op-changed": 1, "op-admin-changed": 1} {
		var n int
		fatalf(t, owner.QueryRow(ctx, `SELECT count(*) FROM operators WHERE id = $1`, id).Scan(&n), "counting")
		if n != want {
			t.Errorf("%s: %d row(s) after 00040, want %d", id, n, want)
		}
	}
}
