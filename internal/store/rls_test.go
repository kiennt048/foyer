package store_test

import (
	"context"
	"testing"

	"github.com/kiennt048/foyer/internal/store"
)

// The tenant seam, exercised rather than described.
//
// v1 ships single-tenant and nothing in the product offers to create a second
// tenant, so these tests create one directly. That is the point: the seam has to be
// proven to work now, while it costs nothing, rather than discovered not to work in
// the release where somebody needs it.
//
// Every policy is FORCED since migration 00019, so a non-superuser owner is subject
// to it too; the owner here is a superuser, which skips row security regardless,
// and that is what migrations, backups and restore run as. The *application* is
// `foyer_app`, which is not the owner, and that is who these tests drive.

const secondTenant = "00000000-0000-0000-0000-0000000000ff"

func TestRowLevelSecurityIsolatesTenants(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	owner := ownerPool(t)
	_, err := owner.Exec(ctx, `
		INSERT INTO tenants (id, slug, name) VALUES ($1, 'second', 'Second')
		ON CONFLICT (id) DO NOTHING`, secondTenant)
	fatalf(t, err, "creating a second tenant")

	// One site in the default tenant, one in the second, written through the
	// application role each time so that the INSERT policy is exercised too.
	first := storeAsRole(t, store.DefaultAppRole, store.DefaultTenantID)
	second := storeAsRole(t, store.DefaultAppRole, secondTenant)

	a := &store.Site{ID: uniq("rls-a"), Name: "Tenant A venue", Environment: "lab"}
	fatalf(t, first.Sites().Create(ctx, a), "creating a site in tenant A")

	b := &store.Site{ID: uniq("rls-b"), Name: "Tenant B venue", Environment: "lab"}
	fatalf(t, second.Sites().Create(ctx, b), "creating a site in tenant B")

	t.Run("a tenant cannot read another's row by id", func(t *testing.T) {
		if _, err := first.Sites().Get(ctx, b.ID); err != store.ErrNotFound {
			t.Fatalf("tenant A read tenant B's site: got %v, want ErrNotFound", err)
		}
		if _, err := second.Sites().Get(ctx, a.ID); err != store.ErrNotFound {
			t.Fatalf("tenant B read tenant A's site: got %v, want ErrNotFound", err)
		}
	})

	t.Run("a tenant cannot see another's row in a list", func(t *testing.T) {
		sites, err := first.Sites().List(ctx)
		fatalf(t, err, "listing sites in tenant A")
		for _, s := range sites {
			if s.ID == b.ID {
				t.Fatalf("tenant A's site list contains tenant B's %s", b.ID)
			}
		}
	})

	t.Run("a tenant cannot delete another's row", func(t *testing.T) {
		// Rename rather than delete: the failure mode of a write escaping the
		// policy is the same, and the row is still needed by the assertions
		// above if this test is run more than once.
		if err := first.Sites().Rename(ctx, b.ID, "renamed by the wrong tenant"); err != store.ErrNotFound {
			t.Fatalf("tenant A wrote tenant B's site: got %v, want ErrNotFound", err)
		}
		got, err := second.Sites().Get(ctx, b.ID)
		fatalf(t, err, "re-reading tenant B's site")
		if got.Name != "Tenant B venue" {
			t.Fatalf("tenant B's site name is now %q; the write escaped the policy", got.Name)
		}
	})
}

// An unset tenant matches no row, which is the failure direction that matters.
//
// A policy comparing against NULL yields NULL, which is not true, so every row is
// filtered out. The alternative -- a policy that falls back to "all rows" when the
// scope is missing -- fails open, and a forgotten SET would leak every tenant's data
// silently instead of returning an empty list loudly.
func TestUnsetTenantScopeSeesNothing(t *testing.T) {
	requireDocker(t)
	ctx := context.Background()

	scoped := storeAsRole(t, store.DefaultAppRole, store.DefaultTenantID)
	site := seedSite(t, scoped)

	// TenantID cannot be set to empty through Config -- withDefaults fills it in,
	// deliberately -- so the GUC is cleared on one connection directly. That is
	// the shape of the bug being guarded against: something reset the scope. The
	// connection is held for the whole test, because the GUC lives on it and
	// returning it to the pool would hand the next caller a different one.
	unscoped := storeAsRole(t, store.DefaultAppRole, store.DefaultTenantID)
	conn, err := unscoped.Pool().Acquire(ctx)
	fatalf(t, err, "acquiring a connection")
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT set_config('foyer.tenant_id', '', false)`); err != nil {
		t.Fatalf("clearing the tenant scope: %v", err)
	}
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM sites WHERE id = $1`, site.ID).Scan(&n); err != nil {
		t.Fatalf("counting sites with no tenant scope: %v", err)
	}
	if n != 0 {
		t.Fatalf("a connection with no tenant scope saw %d row(s); it must see none", n)
	}

	// And a write with no scope fails rather than landing in a NULL tenant.
	_, err = conn.Exec(ctx, `INSERT INTO sites (id, name, environment) VALUES ($1, 'x', 'lab')`, uniq("nos"))
	if err == nil {
		t.Fatal("a connection with no tenant scope inserted a site; it must not be able to")
	}
}
