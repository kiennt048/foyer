import { expect, test, type Page } from "@playwright/test";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `status-is-not-only-hue` — the redundant coding the components claimed to have
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `ui/primitives.tsx` states the contract: *the label carries the meaning and colour
 * is redundant reinforcement rather than the only signal*. The eight status tints sit
 * at **deliberately equal luminance** — `tokens.css` records the reason and this file
 * does not reopen it — so when the redundant channel goes missing, hue is not merely
 * the loudest signal, it is the entire signal.
 *
 * Two measured failure modes:
 *
 *  - **Pills.** Across 143 pills on nine routes, a glyph on every `urgent` and
 *    `warning` pill and none on any `negative` one leaves `/attempts` rendering
 *    `Accepted` and `Rejected` on 21 of 21 rows at a background contrast of
 *    **1.003:1** — in greyscale, twenty-one identical chips.
 *  - **Notices.** Two glyphs for eight levels (`urgent || warning ? Alert : Info`)
 *    fail, because `Alert` and `Info` would be the same circle with a dot moved
 *    5.8px: between an urgent alarm and a warning there would be **no non-hue
 *    difference at all** — 1.001:1 light, 1.084:1 dark.
 *
 * ── WHAT IS ASSERTED, AND WHY IT IS SHAPES RATHER THAN COUNTS ───────────────
 *
 * A glyph *count* is satisfied by drawing the same circle eight times, which is
 * exactly the failure above. So the level→glyph relation is read out of the
 * rendered SVG — the `d` of every path, the geometry of every circle — and levels
 * are compared to each other. A tie between two levels is a finding whether or not
 * anybody remembered to write that pair down.
 *
 * The pill sweep walks the routes rather than the gallery, because 0% was a fact
 * about call sites and the gallery is one call site that was always right.
 */

/**
 * Every route in `NAV`, with what says it has finished arriving.
 *
 * **The readiness selector is load-bearing and easy to get wrong.** Five of these
 * screens are lazily loaded and their Suspense fallback renders a `.page-title` — so
 * waiting for the title is satisfied by the fallback, the sweep counts zero pills on
 * that route, and the whole file goes quietly weaker. Waiting for the *table* cannot
 * be satisfied by a fallback. `/` has no table and names its own.
 */
const ROUTES: { path: string; ready: string }[] = [
  { path: "/", ready: ".card-grid .card .metric-foot" },
  { path: "/sessions", ready: "tbody[aria-busy=false]" },
  { path: "/controllers", ready: "tbody[aria-busy=false]" },
  { path: "/access-points", ready: "tbody[aria-busy=false]" },
  { path: "/vouchers", ready: "tbody[aria-busy=false]" },
  { path: "/accounts", ready: "tbody[aria-busy=false]" },
  { path: "/attempts", ready: "tbody[aria-busy=false]" },
  { path: "/garden", ready: "tbody[aria-busy=false]" },
  { path: "/operators", ready: "tbody[aria-busy=false]" },
  { path: "/audit", ready: "tbody[aria-busy=false]" },
];

interface PillTally {
  total: number;
  marked: number;
  /** One label per level, for a failure message that names what is on screen. */
  sample: string;
}

/**
 * A pill's first segment, tallied by level.
 *
 * "Marked" is **any element child**, not `svg`: the online session's mark is
 * `<span class="heartbeat">`, a pulsing dot that says updates are arriving. Counting
 * only `svg` would report `/sessions` at 0% while every row on it carries the most
 * information-dense non-hue signal in the console.
 */
async function pills(page: Page): Promise<Record<string, PillTally>> {
  return page.evaluate(() => {
    const out: Record<string, PillTally> = {};
    for (const pill of document.querySelectorAll<HTMLElement>(".pill")) {
      const level = pill.dataset.level ?? "(none)";
      const seg = pill.querySelector(".pill-seg");
      out[level] ??= { total: 0, marked: 0, sample: seg?.textContent?.trim() ?? "" };
      out[level].total++;
      if (seg && seg.children.length > 0) out[level].marked++;
    }
    return out;
  });
}

/** The drawn shape of a glyph, as a string that differs when the outline differs. */
const SHAPE_OF = `(svg) => svg ? [...svg.children].map((c) =>
    c.tagName + "(" + (c.getAttribute("d") ??
      [c.getAttribute("cx"), c.getAttribute("cy"), c.getAttribute("r")].join(",")) + ")",
  ).join(" ") : null`;

test.describe("a status level is never carried by hue alone", () => {
  /**
   * ── THE PILLS, ACROSS THE CONSOLE ───────────────────────────────────────────
   *
   * `neutral` is the one level with no glyph, and that is a decision rather than an
   * omission: a neutral pill is the *absence* of a verdict, and a symbol for "no
   * opinion" is how a status vocabulary stops meaning anything. It is asserted as an
   * exception here so that it stays deliberate.
   */
  test("every pill that makes a claim carries a mark, on every route", async ({ page }) => {
    const totals: Record<string, PillTally> = {};

    for (const route of ROUTES) {
      await page.goto(route.path);
      await expect(page.locator(route.ready).first()).toBeVisible();

      for (const [level, tally] of Object.entries(await pills(page))) {
        totals[level] ??= { total: 0, marked: 0, sample: tally.sample };
        totals[level].total += tally.total;
        totals[level].marked += tally.marked;
      }
    }

    /* Vacuity guard. A sweep that found four pills would pass every assertion below
       and measure nothing; the fixture world renders 143. */
    const seen = Object.values(totals).reduce((n, t) => n + t.total, 0);
    expect(seen, "the pill sweep found almost nothing — the fixtures are not loaded").
      toBeGreaterThan(80);

    for (const [level, tally] of Object.entries(totals)) {
      if (level === "neutral") {
        continue;
      }
      expect(
        tally.marked,
        `${tally.marked} of ${tally.total} \`${level}\` pills carry a mark (e.g. ` +
          `"${tally.sample}"). \`negative\` was 0 of 15 before the glyph was derived ` +
          "from the level inside `Pill` — if this is short again, a call site is " +
          "passing `icon={null}` where it means `icon` omitted.",
      ).toBe(tally.total);
    }

    expect(
      totals.neutral?.total ?? 0,
      "no neutral pills were found at all, so the exception below is not being exercised",
    ).toBeGreaterThan(0);
  });

  /**
   * ── THE SHAPES, LEVEL AGAINST LEVEL ─────────────────────────────────────────
   *
   * The gallery renders all eight levels as a pill and as a notice in both themes,
   * which makes it the one place the relation can be read as a whole.
   *
   * The pairs named below are the two measured above. The general assertion
   * underneath them — five distinct outlines — is what stops a future level being
   * given a ninth circle.
   */
  test("the glyph set is wider than two, and urgent is not warning", async ({ page }) => {
    await page.goto("/design");
    await expect(page.locator('.gal-theme[data-scheme="dark"] .pill').first()).toBeVisible();

    const shapes = await page.evaluate((shapeOf) => {
      const shapeFn = eval(shapeOf) as (svg: SVGElement | null) => string | null;
      const read = (selector: string): Record<string, string | null> => {
        const out: Record<string, string | null> = {};
        for (const el of document.querySelectorAll<HTMLElement>(selector)) {
          const level = el.dataset.level;
          if (!level || level in out) continue;
          const scope = el.classList.contains("pill") ? el.querySelector(".pill-seg") : el;
          out[level] = shapeFn(scope?.querySelector("svg") ?? null);
        }
        return out;
      };
      return { notice: read(".notice[data-level]"), pill: read(".pill[data-level]") };
    }, SHAPE_OF);

    /* Guards everything below: an empty gallery satisfies "no two are equal". */
    expect(Object.keys(shapes.notice).length, "the gallery is not rendering eight notices").toBe(8);
    expect(Object.keys(shapes.pill).length, "the gallery is not rendering eight pills").toBe(8);

    for (const surface of ["notice", "pill"] as const) {
      const s = shapes[surface];
      expect(
        s.urgent,
        `An ${surface} at level \`urgent\` draws the same outline as one at \`warning\`. ` +
          "Their tints differ by 1.001:1 in the light theme, so this is the whole of " +
          "the difference between an alarm and a caution.",
      ).not.toBe(s.warning);
      expect(
        s.negative,
        `A ${surface} at level \`negative\` draws the same outline as one at ` +
          "`positive` — measured 1.003:1 apart, which is what /attempts shows in greyscale.",
      ).not.toBe(s.positive);
    }

    /* Every notice has a glyph — it has no tinted chip around a word to fall back
       on — and the set of outlines is five, not two. */
    expect(Object.values(shapes.notice).filter((v) => v === null)).toEqual([]);
    const outlines = new Set(Object.values(shapes.notice));
    expect(
      outlines.size,
      `The eight notice levels draw ${outlines.size} distinct outlines. It was 2 — ` +
        "`Alert` for urgent and warning, `Info` for the other six — and `Alert` and " +
        "`Info` are the same circle with a dot moved 5.8px.",
    ).toBeGreaterThanOrEqual(5);
  });

  /**
   * ── THE BORDER SLOT ─────────────────────────────────────────────────────────
   *
   * `.notice { border: 1px solid transparent }` reserved the space and no level used
   * it. It now separates *act now* from *read this* — a difference visible in
   * greyscale without reading the glyph — and the six calm levels keep it empty so
   * the edge means something when it is there.
   *
   * Deliberately **not** a per-level border: that would be severity by hue a second
   * time, and per-level luminance is the settled decision this file explicitly
   * does not reopen (it would make a `neutral` notice shout).
   */
  test("the two levels that ask for action are the two with a drawn edge", async ({ page }) => {
    await page.goto("/design");
    await expect(page.locator(".notice").first()).toBeVisible();

    const borders = await page.evaluate(() => {
      const out: Record<string, { colour: string; opaque: boolean }> = {};
      for (const el of document.querySelectorAll<HTMLElement>(".gal-theme .notice[data-level]")) {
        const level = el.dataset.level!;
        if (level in out) continue;
        const colour = getComputedStyle(el).borderTopColor;
        /* Transparent in any notation — `rgba(…, 0)` or `color(srgb … / 0)`. */
        const alpha = /\/\s*0\s*\)|,\s*0\s*\)/.test(colour) ? 0 : 1;
        out[level] = { colour, opaque: alpha > 0 };
      }
      return out;
    });

    expect(Object.keys(borders).length, "the gallery is not rendering eight notices").toBe(8);

    const drawn = Object.entries(borders)
      .filter(([, b]) => b.opaque)
      .map(([level]) => level)
      .sort();
    expect(
      drawn,
      "The notice border slot no longer says *act now*. It carried nothing at all " +
        "before the notice glyph fix — every level transparent — and a border on more levels than these " +
        "two makes it a second hue channel rather than a second signal.\n" +
        `  Measured: ${JSON.stringify(borders)}`,
    ).toEqual(["urgent", "warning"]);
  });
});
