import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { TrendBucket } from "@foyer/contract";
import { Bars } from "./Trends.jsx";

/**
 * The trends chart's geometry and its text, rendered without a browser.
 *
 * `@foyer/admin` has no jsdom, so what is asserted is the markup: one bar per bucket
 * including the empty ones, the tallest bar full height, a zero drawn as nothing
 * rather than a sliver, a wrapped byte total said as "at least", and a table carrying
 * the same figures for anybody who does not point at bars. The download and the role
 * gate are in `web/e2e/tests/admin-auth/reports.spec.ts`, which drives them.
 */

const live = { state: "live", sampledAt: "2026-09-23T04:00:00Z", intervalSeconds: null } as const;

function bucket(day: number, guests: number, bytes: number, lowerBound = false): TrendBucket {
  return {
    start: `2026-09-${String(day).padStart(2, "0")}T00:00:00Z`,
    end: `2026-09-${String(day + 1).padStart(2, "0")}T00:00:00Z`,
    guestDevices: guests,
    vouchersRedeemed: 0,
    bytes,
    bytesLowerBound: lowerBound,
    bytesFreshness: live,
  };
}

const buckets = [bucket(20, 4, 1_000_000), bucket(21, 0, 0), bucket(22, 8, 5_000_000_000, true)];

const heights = (markup: string) =>
  [...markup.matchAll(/data-testid="trends-bar"[^>]*height="([\d.]+)"/g)].map((m) => Number(m[1]));

describe("the trends chart", () => {
  it("draws one bar per bucket, a zero as nothing, and the peak at full height", () => {
    const markup = renderToStaticMarkup(
      <Bars buckets={buckets} figure="guestDevices" granularity="day" name="Guest devices" />,
    );
    const h = heights(markup);
    expect(h).toHaveLength(3);
    expect(h[1], "an empty day drawn as a bar reads as a day with guests").toBe(0);
    expect(h[2]).toBeGreaterThan(h[0]!);
    expect(h[2]).toBe(160 - 18);
    expect(markup).toContain('role="img"');
    expect(markup).toContain('aria-label="Guest devices"');
  });

  it("says a wrapped byte total is a floor, in the tooltip and in the table", () => {
    const markup = renderToStaticMarkup(
      <Bars buckets={buckets} figure="bytes" granularity="day" name="Data" />,
    );
    expect(markup).toContain("at least 5.0 GB");
    expect(markup.match(/at least 5\.0 GB/g)!.length, "tooltip, table and peak label").toBeGreaterThanOrEqual(2);
    expect(markup).toContain("<table");
  });

  it("renders an empty answer as a baseline and no bars", () => {
    const markup = renderToStaticMarkup(
      <Bars buckets={[]} figure="vouchersRedeemed" granularity="week" name="Vouchers redeemed" />,
    );
    expect(heights(markup)).toHaveLength(0);
    expect(markup).toContain("<line");
  });
});
