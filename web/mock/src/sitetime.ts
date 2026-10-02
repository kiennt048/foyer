/**
 * A site's calendar, in the mock — `internal/api/handlers_sitetime.go`'s twin.
 *
 * Every site has a zone; one nobody set is `DEFAULT_TIME_ZONE`, the product default,
 * which is also what migration 00033 gives every existing site. A name is valid when this runtime's
 * `Intl` knows it, the same question Go's `time.LoadLocation` answers against the
 * server's zone database. `Local` is refused on both: it names a server's clock,
 * which is exactly what the setting exists to avoid.
 */
import type { SiteTimeZone } from "@foyer/contract";

export const DEFAULT_TIME_ZONE = "Asia/Ho_Chi_Minh";

const zones = new Map<string, { zone: string; updatedAt: string }>();

export function validTimeZone(name: string): boolean {
  const n = name.trim();
  if (!n || n.toLowerCase() === "local" || n.length > 64) return false;
  try {
    new Intl.DateTimeFormat("en-CA", { timeZone: n });
    return true;
  } catch {
    return false;
  }
}

export function timeZoneOf(siteId: string): string {
  return zones.get(siteId)?.zone ?? DEFAULT_TIME_ZONE;
}

export function siteTimeZone(siteId: string): SiteTimeZone {
  const z = zones.get(siteId);
  return { siteId, timeZone: z?.zone ?? DEFAULT_TIME_ZONE, updatedAt: z?.updatedAt ?? null };
}

export function setSiteTimeZone(siteId: string, zone: string): SiteTimeZone {
  /* `updatedAt` is when the zone changed — storing the zone a site already has is not
     a change, and the Go store (`time_zone_changed_at`) does not move it either. */
  const next = zone.trim();
  const before = zones.get(siteId);
  if ((before?.zone ?? DEFAULT_TIME_ZONE) === next) {
    if (before) zones.set(siteId, { zone: next, updatedAt: before.updatedAt });
    return siteTimeZone(siteId);
  }
  zones.set(siteId, { zone: next, updatedAt: new Date().toISOString() });
  return siteTimeZone(siteId);
}

/** `YYYY-MM-DD` of instant `t` on the site's calendar — the day its ceilings count. */
export function siteDay(siteId: string, t: number): string {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: timeZoneOf(siteId),
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(new Date(t));
}

/** For tests: every site back on the default calendar. */
export function resetSiteTimeZones(): void {
  zones.clear();
}
