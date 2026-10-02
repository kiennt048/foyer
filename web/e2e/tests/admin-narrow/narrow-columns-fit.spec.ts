import { expect, test, type Page } from "@playwright/test";
import { NARROW_COLUMNS_REVERTED, SESSIONS_WIDEST_ROWS, admin } from "../helpers/admin.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `narrow-columns-fit` — the page fitted 390px and the table did not
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `responsive-shell` locks the *document* at 390px and stays green when a table does
 * not fit, because `.table-scroll` absorbs a table wider than its port. That is the
 * design working for the page and able to hide a defect inside it: the scroll port is
 * 356px, and without the shared-port rules four of the eight tables lay out wider than
 * that, so an operator scrolls sideways to read the column the narrow layout keeps
 * *because* it identifies the row. On `/sessions` the identity column would start at
 * x=295 in a 356px port — `a8:52:12:` and scroll for the rest, which is the whole
 * purpose of dropping the other seven columns, undone.
 *
 * The recorded figures with the shared-port rules reverted, port 356px:
 *
 *   route         fine pointer      coarse pointer    widest columns
 *   /sessions     537px (181 over)  545px (189 over)  MAC 267, Status 202
 *   /controllers  465px (109 over)  473px (117 over)  Status 158, actions 140, Name 126
 *   /accounts     407px  (51 over)  415px  (59 over)  actions 149, Username 131
 *   /garden       399px  (43 over)  407px  (51 over)  Destination 164, Enforced by 112
 *   the other four                  356px, 0 over
 *
 * `/sessions` is measured at `SESSIONS_WIDEST_ROWS` rather than at `/sessions`, and
 * the block above `TABLES` says why each route is measured at its landing view.
 *
 * **Neither of the two decisions behind those widths is traded away.** The touch rule makes
 * `.id-actions` permanently visible under a coarse pointer, which is what makes
 * copy-a-MAC exist at all on a phone and widens every identifier column by two 24px
 * buttons; the column rule never drops a column that renders a control, and those actions
 * columns are 84–149px. The answer is that the kept columns **share the port**, which
 * is what reaches the middle truncation `IdCell` carries.
 *
 * ── WHY BOTH POINTER MODELS ─────────────────────────────────────────────────
 *
 * The two columns of numbers above differ by 8px on every route, and the difference
 * is `.row-disclosure`: 28px under a fine pointer, 36px under a coarse one, because
 * the touch rule sizes it for the thing that presses it. This project's viewport is
 * narrow but its pointer is fine, so a lock written only here would measure a phone
 * width with a mouse's touch targets and be 8px optimistic about the phone the narrow
 * layout is for. `hasTouch: true` is the knob that makes
 * `(hover: none) and (pointer: coarse)` match — see `touch-actions-visible.spec.ts`,
 * which checks exactly that.
 *
 * ── WHAT IS ASSERTED ────────────────────────────────────────────────────────
 *
 * `.table-scroll`'s `scrollWidth` equals its `clientWidth`. Geometry rather than
 * visibility, and content that spills its *cell* counts too — an unbreakable mono
 * string leaving a `<td>` grows the scroll container exactly as a wide column does —
 * so this cannot be satisfied by pinning the table's width and letting the text
 * escape. **A width assertion that has never been red proves nothing**, so the last
 * case injects the reverted rules and requires the four numbers back.
 */

interface TableRoute {
  path: string;
  label: string;
  /**
   * The kept columns, in order, for the failure message — so a red run says whether
   * the column set changed or the sharing broke. Not asserted here:
   * `narrow-values-reachable` computes the drop set from the product itself.
   */
  keeps: string;
  /** What it measures over the port with the shared-port rules reverted, coarse pointer. 0 = it fits. */
  wasOver: number;
  /**
   * The rows whose presence the `wasOver` figure depends on, where any do.
   *
   * `wasOver` is the width of a *table*, and a table's width is the **sum of its
   * columns' maxima** — so these numbers are only true of the world the fixtures
   * author. Seven of the eight tables have a fixed row set and nothing can move them.
   * The eighth grows: every portal grant adds a live session.
   *
   * **A list, not one row.** "Sum of the columns' maxima" is not "the widest row":
   * the maxima can sit on *different* rows, and on `/sessions` they do. Measured at 390px with the revert injected, hiding rows
   * from the landing view one group at a time:
   *
   *   nothing hidden       over 176   columns [28, 48, 202, 254]
   *   orphan hidden        over 169   columns [28, 48, 195, 254]   −7
   *   duplicates hidden    over 124   columns [28, 48, 202, 202]   −52
   *   both hidden          over 117   columns [28, 48, 195, 202]
   *
   * `sess-orphaned-no-stop` carries the Status column (`Orphaned · 40 days ago`,
   * 202px) and is worth **7px** — `sess-stranded-unreachable` is 195px right behind
   * it. `sess-duplicate-a`/`b` carry the identity column, at 254px against 202, and
   * are worth **52px**. Naming only the Status row would guard 4% of the figure and
   * leave the rows carrying 30% of it unnamed. Both are named, and losing either one
   * is a red run that says which.
   *
   * It is asserted before the measurement rather than left to the number, because
   * "sessions overflowed by 72px" is a sentence about the CSS and a lost row is not a
   * CSS problem. See `LIVE_SESSION_WINDOW` in `web/mock/src/state.ts`.
   *
   * ── what the guard turns a lost row into ────────────────────────────────────
   *
   * Against a mock with the live-session cap removed and 140 grants pushed through it
   * — `first page rows: 100 | orphan on page: false` — the same world, one run each way:
   *
   *   with `restsOn`     `sess-orphaned-no-stop` is not on this page, and 189px is a
   *                      measurement of a table that contains it … **Nothing about the
   *                      layout is wrong when this fails.**
   *
   *   without it         `sessions overflowed by 72px under the revert, against a
   *                      recorded 189px. Measured: sessions=72(was 189)
   *                      controllers=117(was 117) accounts=59(was 59) garden=51(was 51)`
   *
   * **The guard converts the second message into a sentence naming the row, the cause
   * and the remedy.** One route adrift out of eight is a question about that route's data, not
   * about the rule the number came from.
   *
   * **The number in the failure text is the recorded figure, not the measured one** —
   * the guard fires before the injection, so no measurement happens. It reads oddly
   * cold: the number in the message is the one you should still be getting. That is
   * the right way round and it is deliberate.
   */
  restsOn?: string[];
}

/*
 * ── EACH ROUTE IS MEASURED AT ITS LANDING VIEW, NOT A FILTERED ONE ──────────
 *
 * Not at `SESSIONS_WIDEST_ROWS` (`?state=orphaned,unknown,ended`), because no filter
 * is wider than the page: **that selection measures 124px against the landing view's
 * 176px.** A table is the sum of its columns' maxima, and on `/sessions` those maxima
 * sit on rows in *different states*: `sess-orphaned-no-stop` holds the Status column
 * and is `orphaned`, `sess-duplicate-a`/`b` hold the identity column at 254px against
 * 202 and are `online`. `?state=` is the axis that separates exactly those two, so no
 * filter carries both.
 *
 * **No proper subset of the page is as wide as the page**, which makes the landing
 * view the widest view by construction — and "is this the widest view?" is not a
 * question you can ask about the whole table. Filtering can only remove rows, and
 * removing rows can only narrow columns.
 *
 * The cause of the 254/202 split is **not established and is deliberately not guessed
 * at here**. It is not `Mono`'s `<wbr>`s — `IdCell` builds its own `.mono` span, never
 * calls `Mono`, and `/sessions` contains no `<wbr>` at all. The measured fact is that
 * the per-row content is identical at both widths.
 *
 * `restsOn` above is what carries the protection, and it is a list for exactly this
 * reason: it names *both* rows the figure rests on. `SESSIONS_WIDEST_ROWS` stays exported — `narrow-values-reachable` still
 * needs a table wider than its port, and 480px in a 356px port still is one.
 */

/*
 * Every route that renders a table at 390px. **Eight**: a voucher batch's codes are a
 * table too, they are reached from `/vouchers`, and `narrow-values-reachable` counts
 * them as the eighth. They fit even without the shared-port rules, and they are here
 * so that stays true rather than being assumed — a table missing from a coverage list
 * reads as coverage.
 *
 * `/design` is the one exclusion, for the reason `responsive-shell` states at length
 * and asserts rather than claims: absent from `NAV`, lazily loaded, and its overflow
 * comes from `.gal-theme` panels rather than from a table. Its `COLUMNS` array feeds
 * five demo renders, so the gallery's own tables are still exercised — at the width
 * the gallery is reviewed at.
 */
/*
 * ── A FIGURE THAT MEASURES DIFFERENTLY IS NOT RE-RECORDED ────────────────────
 *
 * The reflex — the one the long comment below `Fit` forbids — is to answer a figure
 * that measures differently by re-recording it. Usually the repair belongs in
 * `NARROW_COLUMNS_REVERTED`, not in this table: `Mono` puts a `<wbr>` after every
 * separator in a mono value, and `overflow-wrap: normal` does not undo a break
 * opportunity that is in the markup, so the revert carries `wbr { display: none }`.
 * Without it `/attempts` measures 45 rather than its recorded 80, and `/garden` 25 —
 * half a pixel under its `/2` floor of 25.5. The helper carries the neutralisation run
 * that proves only the two `<wbr>`-bearing tables move; `IdCell` never calls `Mono`
 * and `/sessions` contains no `<wbr>` at all.
 *
 * Three routes measure differently from their recorded figure — `/sessions` 176
 * against 189, `/controllers` 133 against 117, `/accounts` 98 against 59 — for a reason
 * that has **not been established**. All three clear their floor comfortably, so they
 * are left exactly as recorded: a baseline changed on an untested cause is worth less
 * than one that disagrees honestly. The measured starting point is that `/sessions`'s
 * identity column is 254px on the landing view and 202.4px on a filtered one, **with
 * identical per-row content**.
 */
const TABLES: TableRoute[] = [
  {
    path: "/sessions",
    label: "sessions",
    keeps: "state, mac",
    wasOver: 189,
    restsOn: ["sess-orphaned-no-stop", "sess-duplicate-a"],
  },
  { path: "/controllers", label: "controllers", keeps: "name, status, actions", wasOver: 117 },
  { path: "/access-points", label: "access points", keeps: "name, state", wasOver: 0 },
  { path: "/vouchers", label: "voucher batches", keeps: "label, usage, expiry", wasOver: 0 },
  { path: "/vouchers/batch-daily", label: "voucher codes", keeps: "code, state, actions", wasOver: 0 },
  { path: "/accounts", label: "accounts", keeps: "username, state, actions", wasOver: 59 },
  /*
   * Three kept columns. Dropping `what` on a phone would leave `OUTCOME` +
   * `MAC ADDRESS`, and seven consecutive rows would read
   * `Rejected / a4:3d:0d:da:72:bb`, byte-identical. Keeping the column that says *why*
   * means this table depends on the shared-port rules: 80 over under the revert. That
   * is a stronger claim than a 0 would be — a `wasOver` of 0 asserts a table is
   * *unaffected* by the sabotage, and this one is not.
   *
   * **80 is also the figure that checks the `<wbr>` neutralisation.** This table holds
   * 105 of them, more than any other, and it measures 80 on the nose with the revert
   * suppressing them. A number that lands exactly is evidence; a number
   * that lands nearby is a coincidence you talked yourself into.
   */
  { path: "/attempts", label: "attempts", keeps: "outcome, what, mac", wasOver: 80 },
  { path: "/garden", label: "garden", keeps: "value, honoured, actions", wasOver: 51 },
];

interface Fit {
  /** `.table-scroll`: what the table has to live inside. */
  portWidth: number;
  /** Its `scrollWidth` — the table *plus* anything spilling out of a cell. */
  contentWidth: number;
  tableWidth: number;
  /**
   * The table's own `min-content` width: the narrowest it can be made without
   * anything overflowing, and therefore the width **no data value can push past**.
   * Reported on failure because it says which of two failures this is — a column
   * that stopped shrinking, or a table that merely has less slack than it had.
   */
  minContent: number;
  rows: number;
  columns: { header: string; width: number }[];
  spills: { tag: string; cls: string; text: string; over: number }[];
}

async function fit(page: Page, path: string): Promise<Fit> {
  await page.goto(path);
  await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
  await expect(page.locator(admin.rows).first()).toBeVisible();

  return page.evaluate(() => {
    const port = document.querySelector<HTMLElement>(".table-scroll")!;
    const table = port.querySelector<HTMLTableElement>("table")!;
    const edge = port.getBoundingClientRect().x + port.clientWidth;

    const columns = [...table.querySelectorAll<HTMLTableCellElement>("thead tr:first-child th")]
      .map((th) => ({
        header: th.textContent!.trim() || `(${th.className || "unnamed"})`,
        width: Math.round(th.getBoundingClientRect().width),
      }))
      .sort((a, b) => b.width - a.width);

    const spills: Fit["spills"] = [];
    for (const el of table.querySelectorAll<HTMLElement>("*")) {
      const box = el.getBoundingClientRect();
      if (box.right <= edge + 1) continue;
      /* Leaves only: a `<td>` reported as spilling says nothing about what inside
         it is doing the spilling. */
      if (el.querySelector("*")) continue;
      spills.push({
        tag: el.tagName.toLowerCase(),
        cls: typeof el.className === "string" ? (el.className.split(" ")[0] ?? "") : "",
        text: (el.textContent ?? "").trim().slice(0, 24),
        over: Math.round(box.right - edge),
      });
    }
    spills.sort((a, b) => b.over - a.over);

    const restore = table.style.width;
    table.style.width = "min-content";
    const minContent = Math.round(table.getBoundingClientRect().width);
    table.style.width = restore;

    return {
      portWidth: port.clientWidth,
      contentWidth: port.scrollWidth,
      tableWidth: Math.round(table.getBoundingClientRect().width),
      minContent,
      rows: table.querySelectorAll("tbody tr[data-row-key]").length,
      columns,
      spills: spills.slice(0, 4),
    };
  });
}

function report(label: string, m: Fit): string {
  const spills = m.spills.length
    ? m.spills.map((s) => `${s.tag}.${s.cls} "${s.text}" +${s.over}px`).join("\n    ")
    : "(nothing past the edge — the table box itself is what is wide)";
  return (
    `${label}: the table needs ${m.contentWidth}px inside a ${m.portWidth}px scroll ` +
    `port — ${m.contentWidth - m.portWidth}px of sideways scroll to read a row.\n` +
    `  table ${m.tableWidth}px, min-content ${m.minContent}px, ${m.rows} rows\n` +
    `  columns:  ${m.columns.map((c) => `${c.header}=${c.width}`).join("  ")}\n` +
    `  spilling: ${spills}\n` +
    "  The kept columns share the port rather than claim their content width — see " +
    "the narrow block at the end of admin.css. If `min-content` above is over the " +
    "port, some cell stopped being able to shrink: a new `white-space: nowrap`, a " +
    "value that is not `.mono`, or a third button in an actions column. Widening the " +
    "port is not available — at 390px there is no more of it."
  );
}

/**
 * Every view this file measures for a route: the one an operator lands on, and — where
 * the two differ — the one holding the rows the fit rule is *for*.
 *
 * Both, not one. The landing view is what the console actually opens with and has to
 * fit whatever the world has put in it; the selected view is where the orphan, the
 * gaps and the ended sessions are, and it is the one that cannot be quietly emptied of
 * them.
 */
function viewsOf(t: TableRoute): { url: string; label: string }[] {
  return [{ url: t.path, label: t.label }];
}

/**
 * The same eight tables under both pointer models, because the disclosure control is
 * 8px wider under a coarse one and a phone is the coarse one.
 */
for (const pointer of ["fine", "coarse"] as const) {
  test.describe(`the kept columns fit the scroll port at 390px, ${pointer} pointer`, () => {
    test.use({ hasTouch: pointer === "coarse" });

    test(`the ${pointer} pointer is the one being measured`, async ({ page }) => {
      await page.goto("/sessions");
      const coarse = await page.evaluate(
        () => matchMedia("(hover: none) and (pointer: coarse)").matches,
      );
      expect(
        coarse,
        "The pointer model is not what this describe block says it is, so one of these " +
          "two runs is measuring the other one's layout. `hasTouch` is the knob — " +
          "`isMobile` changes the viewport meta and the user agent and leaves the " +
          "pointer fine.",
      ).toBe(pointer === "coarse");
    });

    for (const table of TABLES) {
      for (const view of viewsOf(table)) {
        test(`${view.label}: no sideways scroll to read a row`, async ({ page }) => {
          const m = await fit(page, view.url);

          /* The port is the thing being fitted into. A run where it is absurd would
             make the assertion below meaningless in the passing direction. */
          expect(
            m.portWidth,
            `the scroll port is ${m.portWidth}px at a 390px viewport, which is not the ` +
              "layout this file measures",
          ).toBeGreaterThan(300);
          expect(m.portWidth).toBeLessThanOrEqual(390);
          expect(m.rows, "no rows, so nothing is claiming any width").toBeGreaterThan(0);

          expect(m.contentWidth, report(`${view.label} (${table.keeps})`, m)).toBe(m.portWidth);
        });
      }
    }
  });
}

/**
 * ── THE EVIDENCE ────────────────────────────────────────────────────────────
 *
 * The fit assertions above are worth exactly as much as this one case.
 *
 * The product fix cannot be reverted from inside the browser, so the revert is
 * injected: `NARROW_COLUMNS_REVERTED` in `helpers/admin.ts`, the same string
 * `narrow-values-reachable` uses to get a table wider than its port. With it in
 * place the four routes measure what the responsive design recorded, to the pixel —
 * 545, 473, 415 and 407 against a 356px port — which is both what makes this lock
 * meaningful and a second opinion on the design's numbers having been taken with
 * touch emulation.
 *
 * The four that already fitted are asserted **not** to move. A sabotage that broke
 * everything would prove far less: it would mean the injection is simply a wide
 * stylesheet rather than the precise inverse of the fix.
 *
 * ── AND THE NUMBERS ARE ABOUT A WORLD, NOT ONLY ABOUT A STYLESHEET ──────────
 *
 * `wasOver` is a table's width, and a table is as wide as its widest cell — so each
 * of these four is a claim about the fixtures as much as about the CSS. Seven of the
 * eight tables have a fixed row set. `/sessions` does not: every portal grant adds a
 * live session, and this file read **72 instead of 189** on a `pnpm dev` mock that
 * had served a few hundred of them. The console asks for a hundred rows, the mock
 * returned the newest hundred, and `sess-orphaned-no-stop` — `Orphaned · 40 days
 * ago`, the widest Status cell in the console at 202px — had been pushed below the
 * page. The Status column fell to 85px and nothing about the layout had changed.
 *
 * `restsOn` is why that now fails as itself. The mock stopped letting live sessions
 * bury the authored ones — `LIVE_SESSION_WINDOW` in `web/mock/src/state.ts`, pinned
 * against `DEFAULT_PAGE_LIMIT` by `state.test.ts`, so raising either number alone is
 * a red test rather than an orphan quietly on page two — and this case names the row
 * it depends on, so that if it ever leaves again the failure says which row and why
 * instead of reading as a CSS regression.
 *
 * **Do not answer a drifted number here by re-recording it.** 72 passes its own
 * `/2` floor and would pass on a fresh world too, so the suite goes green and the
 * floor has quietly halved — a `wasOver` taken from a world with none of its
 * anomalies on screen is a smaller floor bought by measuring a healthier product.
 *
 * ── AND EACH ROUTE IS MEASURED AT THE VIEW IT WAS RECORDED AT ───────────────
 *
 * Every route is measured at its landing view, which is the widest view it has:
 * a table is the sum of its columns' maxima, so removing rows can only narrow it.
 * `restsOn` is what makes that mean something — it names the rows the figure is
 * made of, and it is a list because on `/sessions` they are two rows in two
 * different columns. The block above `TABLES` has the measurement, and the reason no
 * filtered view is used.
 *
 * The half-band below — "over by more than half the recorded figure" — is deliberate
 * slack. `restsOn` catches a lost row by name, so the band need not stand in for that;
 * tightening it to equality would be a claim about pixel stability across machines,
 * and nothing here has measured that. It is not slack to spare, though: `/garden`
 * without its `<wbr>`s neutralised measures 25 against a floor of 25.5.
 */

interface Reverted {
  /** `.table-scroll`'s sideways scroll, with the fix injected back out. */
  over: number;
  rows: number;
}

/**
 * One route: settle it, check the rows the figure rests on, inject the revert, measure.
 *
 * Every row in `restsOn` is checked, not just the first — the figure is a sum of
 * column maxima and each named row carries one of them, so losing any one of them
 * makes the number describe a different table.
 */
async function overUnderRevert(page: Page, table: TableRoute): Promise<Reverted> {
  await page.goto(table.path);
  await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
  await expect(page.locator(admin.rows).first()).toBeVisible();

  /* The rows `wasOver` rests on, before anything is injected — so a world that has
     lost one says which row and why, rather than arriving as a pixel count. */
  for (const key of table.restsOn ?? []) {
    await expect(
      page.locator(`${admin.rows}[data-row-key="${key}"]`),
      `${table.label} at ${table.path}: \`${key}\` is not on this page, and ` +
        `${table.wasOver}px is a measurement of a table that contains it. Either the ` +
        "fixtures no longer author that row, or the mock has served enough portal " +
        "grants to push the authored anomalies below the hundred rows the console " +
        "asks for: restart it, and see `LIVE_SESSION_WINDOW` in " +
        "`web/mock/src/state.ts`, which is supposed to make that impossible. " +
        "**Nothing about the layout is wrong when this fails.**",
    ).toHaveCount(1);
  }

  /* After the route has settled, so the injection is the only variable. */
  await page.addStyleTag({ content: NARROW_COLUMNS_REVERTED });
  await page.evaluate(
    () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
  );
  return page.evaluate(() => {
    const port = document.querySelector<HTMLElement>(".table-scroll")!;
    return {
      over: port.scrollWidth - port.clientWidth,
      rows: port.querySelectorAll("tbody tr[data-row-key]").length,
    };
  });
}

interface Recorded extends Reverted {
  label: string;
  wasOver: number;
}

/**
 * Everything a red run needs to be diagnosed without opening a browser: the figures,
 * and the row sets behind them.
 */
function evidence(measured: Recorded[]): string {
  return (
    `  Measured: ${measured.map((m) => `${m.label}=${m.over}(was ${m.wasOver})`).join(" ")}\n` +
    `  Rows: ${measured.map((m) => `${m.label} ${m.rows}`).join(", ")}`
  );
}

test.describe("the fit lock is not vacuous", () => {
  test.use({ hasTouch: true });

  test("reverting the shared-port rules puts the four routes back over the port", async ({
    page,
  }) => {
    const measured: Recorded[] = [];

    for (const table of TABLES) {
      measured.push({
        label: table.label,
        wasOver: table.wasOver,
        ...(await overUnderRevert(page, table)),
      });
    }

    const summary = evidence(measured);
    const shouldOverflow = measured.filter((m) => m.wasOver > 0);

    expect(
      shouldOverflow.filter((m) => m.over <= 0).map((m) => m.label),
      "The revert did not put these routes back over the port, so the assertions above " +
        "cannot tell the fix from its absence on them. Either " +
        "`NARROW_COLUMNS_REVERTED` no longer names the declarations that hold the " +
        "columns — check it against the narrow block in admin.css — or something else " +
        "is now doing the holding, in which case the numbers in this file need taking " +
        `again.\n${summary}`,
    ).toEqual([]);

    /*
     * And by roughly the recorded figure, rather than by a pixel of rounding.
     *
     * **Every route, then one assertion — not an `expect` per route inside a loop.**
     * A red run names *all* the routes below their floor instead of throwing on the
     * first. An ordered `expect` inside a loop hides its own siblings: a route half a
     * pixel under its floor stays invisible until the failure in front of it is fixed.
     */
    const belowFloor = shouldOverflow.filter((m) => m.over <= m.wasOver / 2);
    expect(
      belowFloor.map((m) => `${m.label} ${m.over}px, floor ${m.wasOver / 2}px`),
      "These routes overflowed by less than half what they did when the figures were " +
        "recorded, so the revert is no longer restoring the state they were measured " +
        "in. Check whether the *product* changed — a column dropped, a value that now " +
        "wraps where it did not — before touching the numbers, and read the two kinds " +
        `of re-record above \`TABLES\` before re-recording either way.\n${summary}`,
    ).toEqual([]);

    /* The precision of the sabotage, which is what makes it evidence rather than a
       wide stylesheet: the four that fitted before still fit under it. */
    expect(
      measured.filter((m) => m.wasOver === 0 && m.over > 0).map((m) => m.label),
      "The revert also broke tables that were never over the port. It is supposed to " +
        "be the exact inverse of the narrow column rules, so either it has grown a rule " +
        "the fix does not have, or one of these tables has quietly come to depend on " +
        `the fix that the design says it does not need.\n${summary}`,
    ).toEqual([]);
  });
});
