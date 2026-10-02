package apistore

import (
	"context"
	"sync"
	"time"

	"github.com/kiennt048/foyer/internal/api"
	"github.com/kiennt048/foyer/internal/api/gen"
)

// A site's calendar over `sites.time_zone` (migration 00033) — `api.SiteTimeZoneStore`.
//
// **Read per request, cached for zoneTTL, forgotten on write.** Every guest page
// asks for the site's day (a portal block's schedule), so an uncached read is one
// more query on the hottest path the portal has. The cache is on the Adapter, and
// `cmd/foyer` hands one Adapter to both listeners, so a write through the console
// is seen by the guest portal on its very next page — the TTL bounds only a second
// process against the same database, which the deployment does not run.

var _ api.SiteTimeZoneStore = (*Adapter)(nil)

// zoneTTL bounds how stale another process's view of a changed zone can be.
const zoneTTL = 30 * time.Second

type zoneCache struct {
	mu      sync.Mutex
	entries map[string]zoneEntry
}

type zoneEntry struct {
	loc *time.Location
	at  time.Time
}

func (c *zoneCache) get(siteID string, now time.Time) (*time.Location, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[siteID]
	if !ok || now.Sub(e.at) >= zoneTTL {
		return nil, false
	}
	return e.loc, true
}

func (c *zoneCache) put(siteID string, loc *time.Location, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]zoneEntry{}
	}
	c.entries[siteID] = zoneEntry{loc: loc, at: now}
}

func (c *zoneCache) forget(siteID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, siteID)
}

func (a *Adapter) SiteTimeZone(ctx context.Context, siteID string) (gen.SiteTimeZone, error) {
	zone, changed, err := a.st.Sites().TimeZone(ctx, siteID)
	if err != nil {
		return gen.SiteTimeZone{}, out(err)
	}
	// `updatedAt` is when the zone last changed (migration 00044), not the site
	// row's `updated_at`: a rename is not a change of calendar.
	return gen.SiteTimeZone{SiteId: siteID, TimeZone: zone, UpdatedAt: changed}, nil
}

func (a *Adapter) SetSiteTimeZone(ctx context.Context, siteID, zone string) (gen.SiteTimeZone, error) {
	if err := a.st.Sites().SetTimeZone(ctx, siteID, zone); err != nil {
		return gen.SiteTimeZone{}, out(err)
	}
	a.zones.forget(siteID)
	return a.SiteTimeZone(ctx, siteID)
}

// SiteLocation loads the stored zone. A name this process's zone database cannot
// load — stored by a build with a newer tzdata — is an error, and every caller
// falls back to the server's clock rather than failing a guest.
func (a *Adapter) SiteLocation(ctx context.Context, siteID string) (*time.Location, error) {
	now := time.Now()
	if loc, ok := a.zones.get(siteID, now); ok {
		return loc, nil
	}
	zone, _, err := a.st.Sites().TimeZone(ctx, siteID)
	if err != nil {
		return nil, out(err)
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		return nil, err
	}
	a.zones.put(siteID, loc, now)
	return loc, nil
}

// location is SiteLocation with the fallback every figure in this package uses: the
// process's own clock, which is what the figures counted before the setting.
func (a *Adapter) location(ctx context.Context, siteID string) *time.Location {
	if loc, err := a.SiteLocation(ctx, siteID); err == nil && loc != nil {
		return loc
	}
	return a.now().Location()
}
