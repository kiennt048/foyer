package apistore

import (
	"context"
	"errors"
	"strings"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
	"github.com/kiennt048/foyer/internal/store"
)

// SiteDirectory.
//
// Not paginated, and the repository is not either: a site is a venue, an install
// has a handful, and the console's site switcher needs all of them to render at
// all.
func (a *Adapter) Sites(ctx context.Context) ([]gen.Site, error) {
	rows, err := a.st.Sites().List(ctx)
	if err != nil {
		return nil, out(err)
	}
	sites := make([]gen.Site, 0, len(rows))
	for _, s := range rows {
		sites = append(sites, siteOut(s))
	}
	return sites, nil
}

// CreateSite writes the first row a deployment cannot start without.
//
// Without a contract operation for it, a store has no way to create its root
// aggregate: the only caller of `SiteRepo.Create` would be a test seeder, so a real
// install would have no path to a site and therefore no path to an access point.
//
// **The id is minted here, not taken from the operator.** It reaches
// `radius_clients`, `access_points`, `portal_settings`, `walled_garden_entries`,
// `vouchers` and every `audit_log` row, and `sites.id` is the primary key — a
// value typed once into a first-run form would be permanent and unfixable.
//
// `environment` is left to the column default (`live`). The contract does not
// accept one; see `SiteCreate` for why that is the narrow reading rather than an
// omission.
func (a *Adapter) CreateSite(ctx context.Context, in gen.SiteCreate) (gen.Site, error) {
	name := strings.TrimSpace(in.Name)
	s := &store.Site{ID: a.newID("site-"), Name: name}
	// `CreateFirst`, not `Create`: one venue per install. The refusal lands on the
	// SECOND call and this method's shape is the one the contract promises either
	// way.
	//
	// `Create` still exists and is still what test seeding uses. The schema permits
	// many sites deliberately — the tenant seam is kept as insurance, and a store
	// test proving a site scope needs two populated sites to prove anything.
	if err := a.st.Sites().CreateFirst(ctx, s); err != nil {
		if errors.Is(err, store.ErrSiteExists) {
			return gen.Site{}, api.ErrSiteExists
		}
		return gen.Site{}, out(err)
	}
	// A site with no portal settings is a venue the console cannot configure and
	// the guest cannot reach: `getPortalSettings` answers 404 and the Portal
	// screen reads "Not found". `portal_settings` has no migration seed — a site
	// is a venue and its name is the operator's — so the row is born here, with
	// the name the operator just gave and the repository's own defaults for
	// everything else. Upsert, not insert: re-running against a site whose row
	// already exists keeps the operator's edits rather than blanking them.
	if err := a.st.Portal().Upsert(ctx, &store.PortalSettings{
		SiteID: s.ID, VenueName: name,
	}); err != nil {
		return gen.Site{}, out(err)
	}
	return siteOut(*s), nil
}
