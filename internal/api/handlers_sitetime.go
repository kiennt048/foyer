package api

import (
	"context"
	"strings"
	"time"

	"github.com/kiennt048/foyer/internal/api/gen"
)

// A site's calendar: where "today" starts.
//
// ── why it is a setting ──────────────────────────────────────────────────────
//
// Every daily figure in the product — phone sign-in's ceiling and `sentToday`,
// `vouchersRedeemedToday`, the day and week buckets of `getTrends`, a portal block's
// start and end dates — would otherwise count the **process's** local day, which is
// what `TZ` says, and `deploy/` does not set `TZ`. A product counting UTC days is
// seven hours off in Vietnam: a venue owner's "today's offer" goes live at 07:00,
// and the SMS ceiling resets at 07:00 too. UniFi (Settings → System → Time Zone),
// Meraki (Network-wide → General → Local time zone) and Omada all make it a
// per-site setting an operator picks from a list; so does this, and an operator
// setting does not live in an env var.
//
// ── what reads it ────────────────────────────────────────────────────────────
//
// Every consumer asks the port per request (`siteLocation`); the store-backed port
// caches for a few seconds and forgets on write, so a change applies without a
// restart. A port that cannot answer falls back to the server's clock — the
// behaviour before the setting existed — rather than failing a guest's page.

// SiteTimeZoneStore is a site's calendar. `internal/apistore` stores it on `sites`.
type SiteTimeZoneStore interface {
	// SiteTimeZone answers ErrNotFound for an unknown site.
	SiteTimeZone(ctx context.Context, siteID string) (gen.SiteTimeZone, error)
	// SetSiteTimeZone stores a zone the caller has already validated.
	SetSiteTimeZone(ctx context.Context, siteID, zone string) (gen.SiteTimeZone, error)
	// SiteLocation is the zone loaded, for arithmetic. A nil location with no
	// error means the site has no zone of its own and the server's clock applies.
	SiteLocation(ctx context.Context, siteID string) (*time.Location, error)
}

// DefaultTimeZone is what a site created before the setting existed was given by
// migration 00033.
const DefaultTimeZone = "Asia/Ho_Chi_Minh"

// LoadTimeZone validates an operator's zone against this process's zone database.
//
// `Local` is refused — it names the server's clock, which is the thing the setting
// replaces — and so is the empty string, which `time.LoadLocation` reads as UTC.
// `UTC` itself is a place-less but honest choice and is allowed.
func LoadTimeZone(name string) (*time.Location, bool) {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "local") || len(name) > 64 {
		return nil, false
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, false
	}
	return loc, true
}

// siteLocation is the calendar a site counts its days in, or the server's clock.
func (s *Server) siteLocation(ctx context.Context, siteID string) *time.Location {
	if s.ports.TimeZones != nil && siteID != "" {
		if loc, err := s.ports.TimeZones.SiteLocation(ctx, siteID); err == nil && loc != nil {
			return loc
		}
	}
	return s.now().Location()
}

// siteToday is the site's calendar date now, `YYYY-MM-DD` — a portal block's
// schedule is compared against it.
func (s *Server) siteToday(ctx context.Context, siteID string) string {
	return s.now().In(s.siteLocation(ctx, siteID)).Format("2006-01-02")
}

func (s *Server) GetSiteTimeZone(ctx context.Context, request gen.GetSiteTimeZoneRequestObject) (gen.GetSiteTimeZoneResponseObject, error) {
	if s.ports.TimeZones == nil {
		return nil, errNotImplemented
	}
	tz, err := s.ports.TimeZones.SiteTimeZone(ctx, request.SiteId)
	if err != nil {
		return nil, err
	}
	return gen.GetSiteTimeZone200JSONResponse(tz), nil
}

// UpdateSiteTimeZone validates the name against the zone database, stores it, and
// writes one `site-time-zone-changed` entry naming the old zone and the new.
func (s *Server) UpdateSiteTimeZone(ctx context.Context, request gen.UpdateSiteTimeZoneRequestObject) (gen.UpdateSiteTimeZoneResponseObject, error) {
	if s.ports.TimeZones == nil {
		return nil, errNotImplemented
	}
	if request.Body == nil {
		return nil, validation(map[string]string{"body": "Required."}, map[string]gen.FieldFault{"body": gen.FieldFaultRequired})
	}
	site, err := s.siteNamed(ctx, request.SiteId)
	if err != nil {
		return nil, err
	}
	zone := strings.TrimSpace(request.Body.TimeZone)
	if _, ok := LoadTimeZone(zone); !ok {
		return nil, validation(map[string]string{"timeZone": "Choose a time zone from the list."},
			map[string]gen.FieldFault{"timeZone": gen.FieldFaultNotATimeZone})
	}
	before, err := s.ports.TimeZones.SiteTimeZone(ctx, site.Id)
	if err != nil {
		return nil, err
	}
	after, err := s.ports.TimeZones.SetSiteTimeZone(ctx, site.Id, zone)
	if err != nil {
		return nil, err
	}
	s.recordIn(ctx, callerFrom(ctx), site.Id, gen.AuditKindSiteTimeZoneChanged,
		gen.AuditSubject{Kind: gen.AuditSubjectKindSite, Id: site.Id, Name: ptr(site.Name)},
		map[string]any{"from": before.TimeZone, "to": after.TimeZone})
	return gen.UpdateSiteTimeZone200JSONResponse(after), nil
}
