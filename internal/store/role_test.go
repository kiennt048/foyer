package store_test

import (
	"context"
	"testing"

	"github.com/kiennt048/foyer/internal/store"
)

// The escape hatch is selectable, and the default is still the default.
//
// `Config.Role` documented the empty string as the way to disable the `SET ROLE`.
// `withDefaults` substituted `foyer_app` before `Open` ever tested it, so the branch
// that honours a disabled SET had **never executed**: a store opened as the schema
// owner still ran as the application role, and taking the documentation at its word
// produced `permission denied for table tenants` -- which reads as a broken grant
// rather than as an option that does not exist.
//
// Three cases, and all three are load-bearing:
//
//   - `NoRole` reaches the privileges the connection authenticated with. Without
//     this the option is documentation again.
//   - the default still de-privileges. A fix that made the hatch reachable by
//     making the empty string mean it would have silently un-dropped privilege for
//     every caller in the repository, none of which sets `Role` at all.
//   - a role name that is neither is still refused before it reaches `SET ROLE`,
//     which is an identifier and cannot be bound as a parameter.
//
// `tenants` is the subject because it has exactly one way to refuse a write:
// `foyer_app` holds SELECT and nothing more, and the primary key defaults to
// `gen_random_uuid()` rather than to a sequence. `radacct` would not do -- see
// requireRefused, where a missing sequence grant can stand in for a missing table
// grant and nothing goes red.
func TestTheSetRoleEscapeHatchIsSelectable(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	insertTenant := func(st *store.Store) error {
		_, err := st.Pool().Exec(ctx,
			`INSERT INTO tenants (slug, name) VALUES ($1, 'Second')`, uniq("tenant"))
		return err
	}

	t.Run("NoRole keeps the connection's own privileges", func(t *testing.T) {
		st, err := store.Open(ctx, store.Config{URL: ownerURL(t), Role: store.NoRole, MaxConns: 2})
		fatalf(t, err, "opening a store with no SET ROLE")
		defer st.Close()

		if err := insertTenant(st); err != nil {
			t.Fatalf("the escape hatch did not reach the schema owner's privileges: %v", err)
		}
		if _, err := st.Pool().Exec(ctx, `DELETE FROM tenants WHERE slug LIKE 'tenant-%'`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("an unset role still de-privileges", func(t *testing.T) {
		st, err := store.Open(ctx, store.Config{URL: ownerURL(t), MaxConns: 2})
		fatalf(t, err, "opening a store with the role left unset")
		defer st.Close()

		requireRefused(t, insertTenant(st), "INSERT INTO tenants with Config.Role unset", "tenants")
	})

	t.Run("a role that is neither is refused before SET ROLE", func(t *testing.T) {
		_, err := store.Open(ctx, store.Config{URL: ownerURL(t), Role: "foyer_app; DROP TABLE tenants", MaxConns: 2})
		if err == nil {
			t.Fatal("a role name that cannot be an identifier was accepted")
		}
	})
}
