import { expect, test, type Page } from "@playwright/test";
import { admin } from "../helpers/admin.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `wide-table-says-what-is-hidden` — the columns that were not there, and the
 *  freshness that fell off first
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `.table-scroll { overflow-x: auto }` was the whole rule. At 1440 × 1000
 * `/sessions` needed **1376px inside a 1150px scroller**; `document.scrollWidth`
 * stayed 1440 because that element absorbs the overflow, and macOS overlay
 * scrollbars are invisible until a gesture — so the card's right border read as the
 * end of the row and two of nine columns were simply not there. At 1280, 386px was
 * hidden.
 *
 * **The column that fell off first was the freshness column.** `docs/rules/frontend.md`
 * makes that one load-bearing — *any figure derived from accounting travels with its
 * freshness* — and at 1280 `Downloaded` was on screen while the thing saying how old
 * it is was not.
 *
 * ── AND THE SHARPEST PART, WHICH IS WHAT THE SECOND HALF LOCKS ──────────────
 *
 * **With the backend down the same table fits all nine columns at 1440.** A table's
 * column widths are its content's widths, so `MAC address` at 267px and `Status` at
 * 202px are sized by the ugly fixtures: the layout only fails once it has the data
 * it exists to show, and anyone looking at an empty console sees none of it.
 * That is why the cue is computed at run time from the rendered geometry rather than
 * declared per screen, and why this file measures both worlds in one run.
 *
 * ── HOW ────────────────────────────────────────────────────────────────────
 *
 * Nothing here is a list of column names. The expected set is derived from each
 * header's own geometry against the port, and compared against what the console
 * printed; `data-col` carries the column's key so the ordering rule can be stated
 * without reading a word of the copy.
 */

interface TableGeometry {
  port: number;
  content: number;
  /** Keys of columns any part of which is past the right-hand edge. */
  clippedKeys: string[];
  /** Their headers, in order — what the cue must name. */
  clippedHeaders: string[];
  /** Column keys in render order. */
  order: string[];
  /** The cue's text, or null when the console printed none. */
  cue: string | null;
}

async function geometry(page: Page): Promise<TableGeometry> {
  return page.evaluate(() => {
    const port = document.querySelector<HTMLElement>(".table-scroll")!;
    const table = port.querySelector("table")!;
    const base = table.getBoundingClientRect().left;
    const limit = port.clientWidth;
    const ths = [...table.querySelectorAll<HTMLElement>("thead tr:first-child th")];
    const clipped = ths.filter((th) => {
      const box = th.getBoundingClientRect();
      return box.left - base + box.width > limit + 0.5 && Boolean(th.textContent?.trim());
    });
    const cue = document.querySelector('[data-role="hidden-columns"]');
    return {
      port: limit,
      content: port.scrollWidth,
      clippedKeys: clipped.map((th) => th.dataset.col ?? ""),
      clippedHeaders: clipped.map((th) => th.textContent!.trim()),
      order: ths.map((th) => th.dataset.col ?? ""),
      cue: cue ? (cue.textContent ?? "").trim() : null,
    };
  });
}

const ROUTES = [
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

test.describe("a table wider than its port says which columns are past the edge", () => {
  /**
   * Every table, at the two desk widths the console is designed for. The claim is an
   * equivalence in both directions: a table that overflows names exactly the columns
   * that are clipped, and a table that fits says nothing at all.
   */
  for (const width of [1440, 1280]) {
    test(`at ${width}px, every table's cue matches its own geometry`, async ({ page }) => {
      await page.setViewportSize({ width, height: 1000 });
      let overflowing = 0;

      for (const route of ROUTES) {
        await page.goto(route.path);
        await expect(page.locator(route.ready).first()).toBeVisible();
        /* The cue is measured after layout by a ResizeObserver, so give React the
           frame it needs to render the line before reading it. */
        await page.waitForTimeout(400);

        const g = await geometry(page);
        const at = `${route.path} @${width}: ${g.content}px in a ${g.port}px port`;

        if (g.clippedHeaders.length === 0) {
          expect(g.cue, `${at} fits, and the console still claims columns are hidden`).toBeNull();
          continue;
        }

        overflowing++;
        expect(g.cue, `${at} hides ${g.clippedHeaders.join(", ")} and says nothing`).not.toBeNull();
        for (const header of g.clippedHeaders) {
          expect(g.cue, `${at}: the cue does not name "${header}"`).toContain(header);
        }
        /* And it does not name a column that is on screen — a cue that lists every
           column is not a cue. */
        const onScreen = await page.evaluate(
          () =>
            [...document.querySelectorAll<HTMLElement>("thead tr:first-child th")]
              .map((th) => th.textContent?.trim() ?? "")
              .filter(Boolean),
        );
        for (const header of onScreen.filter((h) => !g.clippedHeaders.includes(h))) {
          expect(g.cue, `${at}: the cue names "${header}", which is on screen`).not.toContain(
            header,
          );
        }
      }

      /* Vacuity. If nothing overflows at either width, every assertion above is the
         `toBeNull` branch and this file is measuring an absence. */
      expect(
        overflowing,
        `No table overflowed its port at ${width}px, so the cue was never exercised. ` +
          "/sessions is recorded at 1376px in a 1150px scroller — if the fixtures " +
          "have shrunk, the world has healed and `frontend.md` says what that costs.",
      ).toBeGreaterThan(0);
    });
  }

  /**
   * ── THE RULE THE REORDER MADE STRUCTURAL ────────────────────────────────────
   *
   * *Any figure derived from accounting travels with its freshness.* The freshness
   * column was declared last on `/sessions`, and columns fall off the right-hand edge
   * in declaration order — so it was the first thing hidden, and at 1280 `Downloaded`
   * was readable while nothing said how old it was.
   *
   * Asserted as the ordering rather than as a width, because the ordering is what
   * makes it true at *every* width, including the ones nobody has measured.
   */
  test("/sessions never shows a byte figure without the column that dates it", async ({ page }) => {
    for (const width of [1440, 1280, 1024]) {
      await page.setViewportSize({ width, height: 1000 });
      await page.goto("/sessions");
      await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
      await page.waitForTimeout(300);

      const g = await geometry(page);
      const index = (key: string) => g.order.indexOf(key);

      expect(index("lastUpdate"), "the freshness column is not on this table").toBeGreaterThan(-1);
      for (const bytes of ["down", "up"]) {
        expect(index(bytes), `the ${bytes} column is not on this table`).toBeGreaterThan(-1);
        expect(
          index("lastUpdate"),
          `At ${width}px, \`${bytes}\` is declared before \`lastUpdate\`, so it survives a ` +
            "narrower window than the column that says how old it is — a byte figure " +
            "with no date beside it at 1280.",
        ).toBeLessThan(index(bytes));
      }

      /* And the consequence, at this width: no byte column is fully visible unless
         the freshness column is too. */
      const clipped = new Set(g.clippedKeys);
      if (!clipped.has("down") || !clipped.has("up")) {
        expect(
          clipped.has("lastUpdate"),
          `At ${width}px a byte column is on screen and the freshness column is clipped.`,
        ).toBe(false);
      }
    }
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * Two halves, because the cue has two ways of being worthless.
   *
   * **It could be static.** So a column is widened from the test until a table that
   * fitted stops fitting, and the cue must appear naming that column — proving it is
   * computed from what is on screen rather than declared once per screen.
   *
   * **It could be permanent.** So the same table is loaded with the backend down,
   * where it fits all its columns because there is no content to widen them, and the
   * cue must be absent. That is the original trap stated as a test: the table only fails
   * once it has the data it exists to show.
   */
  test("the cue is computed, not declared: widening a column makes it appear", async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.goto("/accounts");
    await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
    await page.waitForTimeout(300);

    const before = await geometry(page);
    expect(
      before.cue,
      `/accounts already overflows at 1440 (${before.content}px in ${before.port}px), so ` +
        "this case cannot tell an appearing cue from a permanent one. Pick a table " +
        "that fits.",
    ).toBeNull();

    const victim = before.order.find((k) => k) ?? "";
    await page.addStyleTag({
      content: `td:nth-child(${before.order.indexOf(victim) + 1}) { min-width: 900px; }`,
    });
    await page.evaluate(
      () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
    );
    await page.waitForTimeout(300);

    const after = await geometry(page);
    expect(
      after.content,
      "widening a column did not make the table wider than its port",
    ).toBeGreaterThan(after.port);
    expect(
      after.cue,
      `/accounts is now ${after.content}px in a ${after.port}px port and the console ` +
        "still says nothing. The cue is not tracking the layout.",
    ).not.toBeNull();
    for (const header of after.clippedHeaders) {
      expect(after.cue).toContain(header);
    }
  });

  test("with the backend down /sessions fits, and claims nothing is hidden", async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 1000 });
    await page.route("**/api/v0/**", (route) => route.abort("failed"));
    await page.goto("/sessions");
    await expect(page.locator(".empty-row").first()).toBeVisible();
    await page.waitForTimeout(400);

    const g = await geometry(page);
    expect(
      g.content,
      `With no rows, /sessions measured ${g.content}px in a ${g.port}px port. All nine ` +
        "columns fit an empty table — only the data makes it overflow.",
    ).toBeLessThanOrEqual(g.port);
    expect(
      g.cue,
      "the console names hidden columns on a table that is not hiding any",
    ).toBeNull();
  });
});
