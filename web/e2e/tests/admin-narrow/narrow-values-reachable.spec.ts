import { expect, test, type Page } from "@playwright/test";
import { NARROW_COLUMNS_REVERTED, SESSIONS_WIDEST_ROWS, admin } from "../helpers/admin.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `narrow-values-reachable` — without this, `narrow: "drop"` deletes data
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Below the breakpoint each table keeps two or three columns and drops the rest into
 * a per-row disclosure. **Five of the eight tables open nothing** — no detail route,
 * no dock — so a dropped column there is not deferred, it is gone. That is what makes
 * this a data-integrity assertion rather than a layout one.
 *
 * Two things this file refuses to do, both learned from tests already in this suite:
 *
 * **It does not hardcode the drop counts.** The design lists them — sessions 7,
 * controllers 5, access points 5, voucher batches 3, voucher codes 2, accounts 3,
 * attempts 4, garden 3 — and a test that typed those numbers in would pass forever
 * against a column that quietly stopped being declared. So each expectation is
 * *computed*: the same table is counted at 1280 and at 390, and the difference is
 * what the disclosure has to contain. `capability-gating.spec.ts` learned exactly
 * this ("derived from the capability model, not a number somebody typed") and its
 * header says why. The numbers above are recorded here only so a failure message can
 * say whether the shape changed or the mechanism broke.
 *
 * **It does not settle for `toBeVisible()`.** The `<dl>` sits in a `colSpan` cell, so
 * before it was sized to the scroll port it inherited the *table's* width and started
 * three quarters of the way off the right-hand edge — present in the DOM, `visible`
 * to Playwright, and unreadable on the device the whole item is for. `.table-scroll`
 * absorbing a wide table is why the page does not scroll, and it is the same
 * mechanism that hides this. So geometry is measured against `.table-scroll`'s
 * `clientWidth`, which is the only thing that can tell the two apart.
 */

interface TableRoute {
  path: string;
  label: string;
  /** What the design says this table drops at 390px — for failure messages only. */
  recorded: number;
}

const TABLES: TableRoute[] = [
  { path: "/sessions", label: "sessions", recorded: 7 },
  { path: "/controllers", label: "controllers", recorded: 5 },
  { path: "/access-points", label: "access points", recorded: 5 },
  { path: "/vouchers", label: "voucher batches", recorded: 3 },
  { path: "/vouchers/batch-daily", label: "voucher codes", recorded: 2 },
  { path: "/accounts", label: "accounts", recorded: 3 },
  { path: "/attempts", label: "attempts", recorded: 4 },
  { path: "/garden", label: "garden", recorded: 3 },
];

const NARROW = { width: 390, height: 844 };
const WIDE = { width: 1280, height: 800 };

interface Census {
  headers: number;
  /** Real columns: not the selection checkbox, not the disclosure. */
  data: number;
  disclose: number;
  rows: number;
}

/** Loads the route at whatever the viewport currently is and counts its header. */
async function census(page: Page, path: string): Promise<Census> {
  await page.goto(path);
  await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
  await expect(page.locator(`${admin.rows}`).first()).toBeVisible();
  return page.evaluate(() => {
    const table = document.querySelector(".table-wrap table")!;
    const ths = [...table.querySelectorAll("thead tr:first-child th")];
    const is = (th: Element, cls: string) => th.classList.contains(cls);
    return {
      headers: ths.length,
      data: ths.filter((th) => !is(th, "check-col") && !is(th, "disclose-col")).length,
      disclose: ths.filter((th) => is(th, "disclose-col")).length,
      rows: table.querySelectorAll("tbody tr[data-row-key]").length,
    };
  });
}

test.describe("dropped values stay reachable at 390px", () => {
  for (const table of TABLES) {
    test(`${table.label}: every dropped column is one tap away, and readable`, async ({ page }) => {
      /* The expectation, computed from the product rather than from the design doc. */
      await page.setViewportSize(WIDE);
      const wide = await census(page, table.path);
      await page.setViewportSize(NARROW);
      const narrow = await census(page, table.path);

      const dropped = wide.data - narrow.data;

      expect(wide.disclose, "the disclosure column renders above the breakpoint").toBe(0);
      expect(narrow.disclose, "no disclosure column at 390px").toBe(1);
      expect(
        dropped,
        `${table.label} drops no columns at 390px — it showed ${wide.data} at 1280 and ` +
          `${narrow.data} at 390. The design says ${table.recorded}. Either every column ` +
          `is now narrow: "keep", or the narrow branch is not rendering at all.`,
      ).toBeGreaterThan(0);
      expect(
        narrow.rows,
        "the row count changed between the two widths, so the two censuses are not " +
          "describing the same table",
      ).toBe(wide.rows);

      /* Every row offers it — a disclosure on some rows is worse than none, because
         the rows without one look like rows with nothing more to say. */
      await expect(page.locator('[data-role="row-disclose"]')).toHaveCount(narrow.rows);

      /*
       * Open all of them at once.
       *
       * Not thoroughness for its own sake: an empty `<dd>` is a term with no
       * definition, and the one column known to have returned bare `null` is on a
       * seven-row table. Checking the first row only would sample past it. It is also
       * the cheapest possible `stopPropagation` check — forty-eight disclosure clicks
       * on `/sessions`, none of which may open the detail dock.
       */
      await page
        .locator('[data-role="row-disclose"]')
        .evaluateAll((els) => els.forEach((el) => (el as HTMLElement).click()));

      await expect(page.locator("tr.row-detail")).toHaveCount(narrow.rows);
      await expect(page.locator('[data-role="row-disclose"][aria-expanded="true"]')).toHaveCount(
        narrow.rows,
      );

      const audit = await page.evaluate(() => {
        const rowKeys = new Set(
          [...document.querySelectorAll("tbody tr[data-row-key]")].map(
            (tr) => tr.getAttribute("data-row-key")!,
          ),
        );
        const details = [...document.querySelectorAll("tr.row-detail")];
        return {
          /* Pairs per row, as a set: they must all be the same number. */
          pairCounts: [
            ...new Set(
              details.map(
                (tr) => tr.querySelectorAll(".row-detail-list > div > dt + dd").length,
              ),
            ),
          ],
          dtCounts: [
            ...new Set(details.map((tr) => tr.querySelectorAll(".row-detail-list dt").length)),
          ],
          /* A `<dl>` may contain only dt, dd, div, script and template — a `<p>`
             straight in one is a `serious` axe violation. */
          strayChildren: details.flatMap((tr) =>
            [...(tr.querySelector(".row-detail-list")?.children ?? [])]
              .filter((el) => el.tagName !== "DIV")
              .map((el) => el.tagName.toLowerCase()),
          ),
          unlabelled: details.flatMap((tr) =>
            [...tr.querySelectorAll("dt")]
              .filter((dt) => dt.textContent!.trim() === "")
              .map(() => tr.getAttribute("data-row-detail")!),
          ),
          blank: details.flatMap((tr) =>
            [...tr.querySelectorAll("dd")]
              .map((dd, i) => ({ i, text: dd.textContent!.trim() }))
              .filter((d) => d.text === "")
              .map((d) => `${tr.getAttribute("data-row-detail")} field ${d.i}`),
          ),
          orphans: details
            .map((tr) => tr.getAttribute("data-row-detail")!)
            .filter((key) => !rowKeys.has(key)),
        };
      });

      expect(
        audit.pairCounts,
        `${table.label}: the disclosure does not contain the same number of fields on ` +
          "every row, so what a row shows depends on the row rather than on the column set.",
      ).toEqual([dropped]);
      expect(audit.dtCounts, "a term without its definition, or the other way round").toEqual([
        dropped,
      ]);
      expect(
        audit.strayChildren,
        "Something other than a wrapper <div> is a direct child of the definition list. " +
          "A <dl> may contain only dt, dd, div, script and template — anything else " +
          "is a `serious` axe violation.",
      ).toEqual([]);
      expect(
        audit.unlabelled,
        `${table.label}: a dropped column produced an empty <dt>. A column with no ` +
          "header is a column of controls, and `Table` is supposed to refuse that " +
          "pairing in development before it can reach a browser.",
      ).toEqual([]);
      expect(
        audit.blank,
        `${table.label}: a dropped value rendered as an empty <dd> — a term with no ` +
          "definition. In a <td> a blank cell is harmless; here it is the row saying " +
          "nothing where it should say <Absent>. `Garden`'s optional `note` is the " +
          "column most likely to do this.",
      ).toEqual([]);
      expect(audit.orphans, "a detail row that belongs to no row").toEqual([]);

      /*
       * ── AND IT IS ON SCREEN ───────────────────────────────────────────────
       *
       * `toBeVisible()` passes for all of this already. The measurement is the test.
       *
       * Recorded at 390px, identical on all eight tables: the scroll port is 356px
       * wide. A 356px list starting 14px into it — `.row-detail > td`'s own
       * `--cell-px` padding — would put its right edge 14px past the port, and a
       * tolerance equal to that padding would let it pass. Nothing would be clipped,
       * but the box would be wrong, and a test whose tolerance is somebody else's bug
       * is a test documenting it.
       *
       * `.row-detail-list` is `calc(100cqw - 2 * var(--cell-px))`: 328px in a 356px
       * port, 14px of margin at each end, and **no tolerance** — the list's right edge
       * is the port's, and the assertions say so. Both ends are checked against the
       * padding read from the DOM rather than against 14, so this stays true if the
       * token moves.
       *
       * Sized to the *table* instead, the list is 537px against that same 356px port
       * on `/sessions`.
       */
      const geometry = await page.evaluate(() => {
        const list = document.querySelector(".row-detail-list")!;
        const port = document.querySelector(".table-scroll")! as HTMLElement;
        const listBox = list.getBoundingClientRect();
        const portBox = port.getBoundingClientRect();
        const cell = list.closest("td")!;
        const padLeft = Number.parseFloat(getComputedStyle(cell).paddingLeft) || 0;
        const worst = [...list.querySelectorAll("dt, dd")].reduce(
          (a, el) => Math.max(a, el.getBoundingClientRect().right),
          0,
        );
        return {
          listX: listBox.x,
          listWidth: listBox.width,
          portX: portBox.x,
          portWidth: port.clientWidth,
          tableWidth: port.querySelector("table")!.getBoundingClientRect().width,
          worstFieldRight: worst,
          padLeft,
        };
      });

      expect(
        Math.round(geometry.listWidth),
        `${table.label}: the disclosure list is ${Math.round(geometry.listWidth)}px wide ` +
          `inside a ${geometry.portWidth}px scroll port, on a table that is ` +
          `${Math.round(geometry.tableWidth)}px wide. It is sized to the table rather ` +
          "than to the port, which is exactly how a dropped value ends up off the " +
          "right-hand edge while still passing toBeVisible(). See `.row-detail-list` " +
          "in admin.css — `100cqw`, and the container declared on `.table-wrap`.",
      ).toBeLessThanOrEqual(geometry.portWidth + 1);

      /* And it is the port *minus the cell's padding on both sides*, which is the
         box the list actually occupies. Asserted against the padding read from the
         DOM, so it follows `--cell-px` rather than restating 14. */
      expect(
        Math.round(geometry.listWidth),
        `${table.label}: the disclosure list is ${Math.round(geometry.listWidth)}px in a ` +
          `${geometry.portWidth}px port whose cell padding is ${geometry.padLeft}px a ` +
          "side. It should be the port less both paddings — `calc(100cqw - 2 * " +
          "var(--cell-px))`. A bare `100cqw` is 14px too wide and puts the list's right " +
          "edge outside the port; anything narrower is losing width a dropped value " +
          "could be using.",
      ).toBeGreaterThanOrEqual(Math.round(geometry.portWidth - 2 * geometry.padLeft) - 1);

      expect(
        Math.round(geometry.listX),
        `${table.label}: the disclosure list starts at ${Math.round(geometry.listX)}px, ` +
          `left of the scroll port at ${Math.round(geometry.portX)}px.`,
      ).toBeGreaterThanOrEqual(Math.round(geometry.portX) - 1);

      const portRight = geometry.portX + geometry.portWidth;
      expect(
        Math.round(geometry.worstFieldRight),
        `${table.label}: a field in the disclosure reaches ` +
          `${Math.round(geometry.worstFieldRight)}px against a port ending at ` +
          `${Math.round(portRight)}px. **No tolerance** — not even the cell's own left ` +
          `padding (${geometry.padLeft}px): the list is the port less both paddings, ` +
          "so a field past this edge is a field off screen.",
      ).toBeLessThanOrEqual(Math.round(portRight) + 1);
    });
  }

  /**
   * ── THE EVIDENCE FOR THE GEOMETRY ───────────────────────────────────────────
   *
   * The three measurements above are the only thing separating "the value is one tap
   * away" from "the value is off the right-hand edge of a scroll container". They are
   * worth what this case says they are worth, so the reverted state is injected and the
   * check is required to fail.
   *
   * `.row-detail-list` is `width: calc(100cqw - 2 * var(--cell-px)); position: sticky;
   * left: 0` — the port's own width less the cell padding, from the container declared
   * on `.table-wrap`. Reverting the width and the stickiness gives the `<dl>` the
   * `colSpan` cell's inherited width, which is the table's — the version a test of
   * visibility alone would certify.
   *
   * Recorded on `/sessions`: the list goes from **356px to 509px** inside a 356px
   * port. Every visibility assertion still passes on it —
   * `toBeVisible()`, the pair count, the non-empty definitions — which is the whole
   * point.
   *
   * ── AND IT NEEDS A SECOND SABOTAGE ──────────────────────────────────────────
   *
   * **This case has a precondition the product does not meet**, which its own failure
   * message names: "the sessions table now fits the 390px port … pick a table that is
   * still wider than the port, or delete this test and say why". `narrow-columns-fit`
   * makes every table fit its port, so there is no wide one to pick — and *that is
   * why* `NARROW_COLUMNS_REVERTED` is injected first. It restores a table wider than
   * the port (sessions 545px in a 356px port) rather than picking a different table,
   * which keeps the case measuring the same thing.
   *
   * ── AND AT THE VIEW THAT NUMBER BELONGS TO ──────────────────────────────────
   *
   * `SESSIONS_WIDEST_ROWS` rather than `/sessions`. The 545px is a property of the
   * authored rows, and page one of the default view is not guaranteed to be holding
   * them: `LIVE_SESSION_WINDOW` stops portal arrivals burying them, but a fixture
   * edit, a default filter or a stray `q=` empties page one just as well. The check
   * would survive that — a table of healthy rows still measures over a 356px port —
   * while demonstrating the geometry against a table that had lost every row this
   * file's `colSpan` reasoning is about. See the header of `narrow-columns-fit`.
   *
   * Deleting it would be the wrong trade: the
   * `100cqw` rule is what stops a dropped value being off screen the day a table stops
   * fitting again, and this is the only case that can tell whether the geometry
   * assertions above can see that.
   */
  test("the readability check is not vacuous: sized to the table, it fails", async ({ page }) => {
    await page.goto(SESSIONS_WIDEST_ROWS);
    await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");

    /* First sabotage: the kept columns claim their content width again, so the table
       is wider than its port and the `colSpan` cell has something wrong to inherit. */
    await page.addStyleTag({ content: NARROW_COLUMNS_REVERTED });
    await page.evaluate(
      () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
    );

    await page.locator('[data-role="row-disclose"]').first().click();
    await expect(page.locator(".row-detail-list")).toHaveCount(1);

    const measure = () =>
      page.evaluate(() => {
        const list = document.querySelector(".row-detail-list")!;
        const port = document.querySelector(".table-scroll")! as HTMLElement;
        return {
          listWidth: Math.round(list.getBoundingClientRect().width),
          portWidth: port.clientWidth,
          tableWidth: Math.round(port.querySelector("table")!.getBoundingClientRect().width),
        };
      });

    const before = await measure();
    expect(before.listWidth).toBeLessThanOrEqual(before.portWidth + 1);
    expect(
      before.tableWidth,
      "The sessions table fits the 390px port even with the shared-port rules reverted, " +
        "so nothing here is off screen when the list is sized to it and this case can no " +
        "longer demonstrate anything. `NARROW_COLUMNS_REVERTED` is supposed to put it " +
        "back to 545px — check that string against the narrow block in admin.css, and " +
        "check that `SESSIONS_WIDEST_ROWS` still returns the authored rows, before " +
        "changing anything here.",
    ).toBeGreaterThan(before.portWidth);

    /* Second sabotage: and now the list inherits that width, which is the failure the
       geometry assertions exist to catch. */
    await page.addStyleTag({
      content: ".row-detail-list { width: auto; position: static; }",
    });
    await page.evaluate(
      () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
    );

    const after = await measure();

    /* Still `visible` to Playwright, which is the reason the assertions are geometric. */
    await expect(page.locator(".row-detail-list")).toBeVisible();

    expect(
      after.listWidth,
      `Reverting \`width: 100cqw\` and \`position: sticky\` left the disclosure list at ` +
        `${after.listWidth}px inside a ${after.portWidth}px port — it did not grow, so ` +
        "the geometry assertions above cannot tell the fixed layout from the broken one. " +
        "Something else is now constraining the list; re-measure before trusting them.",
    ).toBeGreaterThan(after.portWidth + 1);
  });

  /**
   * The disclosure is a control on a row that is itself a control, on the one screen
   * where that collides. Without `stopPropagation` — which `TriCheckbox` and the
   * `IdCell` actions already carry — reading a dropped value also opens the detail
   * dock over the thing being read, on a viewport where the dock takes 55vh.
   */
  test("opening a disclosure on /sessions does not open the detail dock", async ({ page }) => {
    await page.goto("/sessions");
    await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
    await expect(page.locator(".page[data-dock]")).toHaveAttribute("data-dock", "false");

    await page.locator('[data-role="row-disclose"]').first().click();
    await expect(page.locator("tr.row-detail")).toHaveCount(1);

    await expect(
      page.locator(".page[data-dock]"),
      "The disclosure opened the detail dock as well. `onRowClick` fired because the " +
        "button did not stop the event — the same fix `TriCheckbox` and the IdCell " +
        "actions already carry.",
    ).toHaveAttribute("data-dock", "false");
    await expect(page.locator(admin.detailDock)).toHaveCount(0);

    /* And it is genuinely a live dock on this screen, so the assertion above is not
       passing because nothing could ever have opened. */
    await page.locator(admin.rows).first().click();
    await expect(
      page.locator(".page[data-dock]"),
      "Clicking a session row no longer opens the dock, so the assertion above proves " +
        "nothing — it would pass against a disclosure that did not stop propagation.",
    ).toHaveAttribute("data-dock", "true");
  });

  /**
   * Column-span bookkeeping.
   *
   * The selection checkbox is not a `Column` and neither is the disclosure, and both
   * have to be counted into the `colSpan` of the empty, error and skeleton rows or
   * they span short and the table's shape breaks exactly where an operator is being
   * told there is nothing to see.
   *
   * Asserted as an identity — the empty row spans as many columns as the header has —
   * rather than against the number 4, so it stays true when a column changes side.
   * The measured shape at 390 on `/sessions` is 4: checkbox, disclosure, state, MAC.
   */
  test("the empty row spans the disclosure column too", async ({ page }) => {
    await page.goto("/sessions?q=zz-no-such-device");
    await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");

    const shape = await page.evaluate(() => {
      const table = document.querySelector(".table-wrap table")!;
      const emptyCells = [...table.querySelectorAll<HTMLTableCellElement>("tr.empty-row td")];
      return {
        headers: table.querySelectorAll("thead tr:first-child th").length,
        disclose: table.querySelectorAll("thead th.disclose-col").length,
        check: table.querySelectorAll("thead th.check-col").length,
        emptyCells: emptyCells.length,
        colSpan: emptyCells[0]?.colSpan ?? 0,
        dataRows: table.querySelectorAll("tbody tr[data-row-key]").length,
      };
    });

    expect(shape.dataRows, "the query matched something, so there is no empty row").toBe(0);
    expect(shape.emptyCells, "no empty row rendered").toBe(1);
    expect(shape.check, "the selection column is gone, so the span arithmetic changed").toBe(1);
    expect(shape.disclose, "no disclosure column, so this is not the narrow rendering").toBe(1);
    expect(
      shape.colSpan,
      `The empty row spans ${shape.colSpan} of ${shape.headers} columns. The disclosure ` +
        "is the second non-`Column` cell after the selection checkbox and has to be " +
        "counted in `span` at table.tsx, or the empty, error and skeleton rows come up " +
        "short by one.",
    ).toBe(shape.headers);
    /* The measured shape, so a change of column set reads as a change rather than
       as this test having always been about the number four. */
    expect(shape.headers, "sessions no longer keeps exactly two columns at 390px").toBe(4);
  });
});

/**
 * Above the breakpoint the disclosure does not render at all.
 *
 * It is not a disabled control and not a hidden one — `Table` computes the dropped
 * set rather than hiding it in CSS, precisely so a dropped cell is not in the DOM
 * twice, announced once in the row and again inside the list. `display: none` would
 * have looked identical on screen and been wrong for a screen reader.
 */
test.describe("above the breakpoint there is no disclosure", () => {
  test.use({ viewport: WIDE });

  for (const table of TABLES) {
    test(`${table.label} renders no disclosure at 1280px`, async ({ page }) => {
      await page.goto(table.path);
      await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
      await expect(page.locator(admin.rows).first()).toBeVisible();

      await expect(page.locator('[data-role="row-disclose"]')).toHaveCount(0);
      await expect(page.locator("tr.row-detail")).toHaveCount(0);
      await expect(page.locator("th.disclose-col")).toHaveCount(0);
      await expect(
        page.locator("tbody dl"),
        "A definition list inside the table body at desk width means the dropped " +
          "columns are in the DOM twice — once as cells and once as terms.",
      ).toHaveCount(0);
    });
  }
});
