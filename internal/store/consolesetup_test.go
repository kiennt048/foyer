package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/kiennt048/foyer/internal/store"
)

// TestMigration00040RemovesTheShippedPassword: an operator still flagged
// `must_change_password` when 00040 runs holds the password this repository
// published, and the migration deletes it — across every tenant, which is the
// half that would silently do nothing under the forced row-level policy if the
// migration did not step outside it. An operator who changed theirs is untouched.
func TestMigration00040RemovesTheShippedPassword(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()
	url := freshDatabase(t)
	fatalf(t, store.Migrate(ctx, url), "migrating up")
	fatalf(t, store.MigrateDownTo(ctx, url, 39), "migrating down to before 00040")

	conn, err := pgx.Connect(ctx, url)
	fatalf(t, err, "connecting")
	defer conn.Close(ctx)
	var tenant string
	fatalf(t, conn.QueryRow(ctx, `INSERT INTO tenants (slug, name) VALUES ('t00040', 't00040') RETURNING id::text`).Scan(&tenant), "tenant")
	_, err = conn.Exec(ctx, `
		INSERT INTO operators (id, tenant_id, username, password_hash, role, enabled, must_change_password) VALUES
		  ('op-shipped', $1, 'admin', '$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA', 'administrator', true, true),
		  ('op-changed', $1, 'owner', '$argon2id$v=19$m=65536,t=3,p=4$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA', 'administrator', true, false)`, tenant)
	fatalf(t, err, "seeding operators")

	fatalf(t, store.Migrate(ctx, url), "applying 00040")

	var left []string
	rows, err := conn.Query(ctx, `SELECT id FROM operators WHERE tenant_id = $1 ORDER BY id`, tenant)
	fatalf(t, err, "reading operators")
	for rows.Next() {
		var id string
		fatalf(t, rows.Scan(&id), "scan")
		left = append(left, id)
	}
	if len(left) != 1 || left[0] != "op-changed" {
		t.Fatalf("after 00040 the operators are %v, want only op-changed: the shipped-password holder must go and nobody else", left)
	}
}
