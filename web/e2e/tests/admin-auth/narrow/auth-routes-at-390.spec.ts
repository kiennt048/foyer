import { expect, test, type Page } from "@playwright/test";
import { NARROW_COLUMNS_REVERTED } from "../../helpers/admin.js";
import { ADMINISTRATOR, shellSettled, signInAs, signOut } from "../../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  The four authentication routes at 390 × 844
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `responsive-shell.spec.ts` pins the nine `NAV` destinations and cannot pin these:
 * `/operators` and `/audit` are administrator-only and were built after it, `/password`
 * is reached from the sidebar foot rather than from `NAV`, and **`/sign-in` renders
 * outside `Shell` entirely.**
 *
 * That last one is the reason this is a separate file rather than four more rows in
 * that one's array. Its lock's sabotage is *"the four width declarations in the
 * `@media (max-width: 900px)` block, put back"* — `.app`'s grid track, and `min-width:
 * auto` on `.sidebar`, `.main` and `.page`. **None of those selectors exist on
 * `/sign-in`.** There is no grid, no sidebar, no `.main` and no `.page`; there is a
 * `.signin` centring a `.signin-card`. So `/sign-in` is a genuinely new case for that
 * lock rather than a thirteenth instance of the same one, and it needs its own
 * sabotage — which is the clearest possible evidence that it is a different layout
 * rather than a narrower one.
 *
 * ── AND THE TWO TRAPS IN THE DISCLOSURE HALF ────────────────────────────────
 *
 * `/operators` and `/audit` both drop columns below 900px into a per-row disclosure,
 * so both inherit the two mistakes `narrow-values-reachable` guards against:
 *
 *  1. **`toBeVisible()` is not reachability.** A `colSpan` list sized to a table that
 *     is wider than its scroll port starts off the right-hand edge, and Playwright
 *     calls it visible the whole way. Measured against `.table-scroll`'s
 *     `clientWidth` instead.
 *  2. **`tbody[aria-busy=false]` is not a settle for the narrow hook.** The disclosure
 *     column is rendered by a `useNarrow()` whose first client value can lag the
 *     table's own loading state, so a read at that moment finds `0` disclosures.
 *     The wait is on `[data-role="row-disclose"]` itself, and the expected count is computed from the row count rather than typed.
 */

interface AuthRoute {
  path: string;
  /** On screen before measuring, so nothing is measured against a skeleton. */
  ready: string;
  /** Signed-out routes exist and are two of the four; they must not be signed in. */
  signedIn: boolean;
}

const ROUTES: AuthRoute[] = [
  { path: "/sign-in", ready: "#signin-username", signedIn: false },
  { path: "/operators", ready: "tbody[aria-busy=false]", signedIn: true },
  { path: "/audit", ready: "tbody[aria-busy=false]", signedIn: true },
  { path: "/password", ready: "#current-password", signedIn: true },
];

/** The tables that drop columns into a disclosure. `/sign-in` and `/password` have none. */
const DISCLOSURE_ROUTES = ["/operators", "/audit"] as const;

const DISCLOSE = '[data-role="row-disclose"]';
const ROWS = "tbody tr:not(.empty-row):not(.row-detail)";

interface Measurement {
  scrollWidth: number;
  clientWidth: number;
  innerWidth: number;
}

async function measure(page: Page): Promise<Measurement> {
  return page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
    innerWidth: window.innerWidth,
  }));
}

async function reach(page: Page, route: AuthRoute): Promise<void> {
  await page.goto(route.path);
  if (route.signedIn) await shellSettled(page);
  await expect(page.locator(route.ready).first()).toBeVisible();
}

test.afterEach(async ({ context }) => {
  await signOut(context);
});

/**
 * Opens the first row's disclosure, with the settle the narrow hook actually needs.
 *
 * `tbody[aria-busy=false]` is **not** it: the disclosure column is rendered by the
 * narrow hook rather than by the query, so a read at that moment can find `0`
 * controls on a table that has finished loading. So the wait is on the control, and
 * the number of controls is then checked against the number of rows — derived, so a
 * table that discloses on only some of its rows fails here rather than passing against
 * whichever count was written down.
 */
async function openFirstDisclosure(page: Page, path: string): Promise<void> {
  await page.goto(path);
  await shellSettled(page);
  await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();
  await expect(page.locator(DISCLOSE).first()).toBeVisible();

  const rows = await page.locator(ROWS).count();
  expect(rows, `${path} rendered no rows, so there is nothing to disclose.`).toBeGreaterThan(0);
  await expect(
    page.locator(DISCLOSE),
    `${path} has ${rows} rows and a different number of disclosure controls.`,
  ).toHaveCount(rows);

  await page.locator(DISCLOSE).first().click();
  const list = page.locator("tr.row-detail .row-detail-list");
  await expect(list).toHaveCount(1);
  await expect(
    list.locator("dt"),
    "The disclosure opened with nothing in it. An empty list satisfies a `.row-detail` " +
      "selector and axe reports a clean result for a panel with no content — the same " +
      "trap the wizard's `ul.findings li` avoids.",
  ).not.toHaveCount(0);
}

interface Geometry {
  listX: number;
  listWidth: number;
  portX: number;
  portWidth: number;
  tableWidth: number;
  /** The furthest right any `dt` or `dd` in the open disclosure reaches. */
  worstFieldRight: number;
}

/** The same measurements `narrow-values-reachable` takes, over these two tables. */
async function geometry(page: Page): Promise<Geometry> {
  return page.evaluate(() => {
    const list = document.querySelector(".row-detail-list")!;
    const port = document.querySelector(".table-scroll")! as HTMLElement;
    const listBox = list.getBoundingClientRect();
    const portBox = port.getBoundingClientRect();
    return {
      listX: listBox.x,
      listWidth: listBox.width,
      portX: portBox.x,
      portWidth: port.clientWidth,
      tableWidth: port.querySelector("table")!.getBoundingClientRect().width,
      worstFieldRight: [...list.querySelectorAll("dt, dd")].reduce(
        (a, el) => Math.max(a, el.getBoundingClientRect().right),
        0,
      ),
    };
  });
}


test.describe("the four authentication routes fit a 390px viewport", () => {
  for (const route of ROUTES) {
    test(`${route.path} does not scroll sideways`, async ({ page, context }) => {
      if (route.signedIn) await signInAs(context, ADMINISTRATOR);
      await reach(page, route);
      const m = await measure(page);

      expect(m.innerWidth, "this project's 390px viewport did not apply").toBe(390);
      expect(
        m.scrollWidth,
        `${route.path} is ${m.scrollWidth}px wide against a ${m.clientWidth}px ` +
          `viewport — ${m.scrollWidth - m.clientWidth}px of horizontal scroll on a ` +
          "phone.\n" +
          "  If several of these went red together with the nine in " +
          "`responsive-shell.spec.ts`, look at the `@media (max-width: 900px)` block " +
          "in `admin.css` before you look at the screen. If only `/sign-in` went red, " +
          "look at `.signin` and `.signin-card` instead — that route renders outside " +
          "`Shell` and shares none of the shell's rules.\n" +
          "  A table wider than its port is *not* this failure: `.table-scroll` " +
          "absorbs that and the page stays 390.",
      ).toBe(m.clientWidth);
    });
  }

  /**
   * ── THE EVIDENCE, PART ONE: THE THREE ROUTES INSIDE THE SHELL ───────────────
   *
   * The same four width declarations `responsive-shell` reverts, injected after the
   * console's own stylesheet at equal specificity so they win on source order. If the
   * three assertions above can see this, they can see a regression.
   */
  test("the lock is not vacuous: reverting the shell widths overflows the three shell routes", async ({
    page,
    context,
  }) => {
    const REVERTED = `
      @media (max-width: 900px) {
        .sidebar { min-width: auto; }
        .app { grid-template-columns: var(--sidebar-w) 1fr; }
        .main { min-width: auto; }
        .page { min-width: auto; }
      }`;

    await signInAs(context, ADMINISTRATOR);
    const measured: { path: string; scrollWidth: number; clientWidth: number }[] = [];

    for (const route of ROUTES.filter((r) => r.signedIn)) {
      await reach(page, route);
      /* After the route has settled, so the injection is the only variable. */
      await page.addStyleTag({ content: REVERTED });
      await page.evaluate(
        () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
      );
      const m = await measure(page);
      measured.push({ path: route.path, scrollWidth: m.scrollWidth, clientWidth: m.clientWidth });
    }

    const unaffected = measured.filter((m) => m.scrollWidth <= m.clientWidth).map((m) => m.path);
    expect(
      unaffected,
      "The sabotage did not overflow these routes, so the assertions above cannot " +
        "distinguish the fix from its absence on them. Either the injected stylesheet " +
        "no longer names the declarations that hold the layout — check it against " +
        "`admin.css`'s `@media (max-width: 900px)` block — or something else is now " +
        "doing the holding.\n" +
        `  Measured: ${measured.map((m) => `${m.path}=${m.scrollWidth}`).join(" ")}`,
    ).toEqual([]);

    const worst = measured.reduce((a, b) => (b.scrollWidth > a.scrollWidth ? b : a));
    expect(
      worst.scrollWidth - worst.clientWidth,
      `The worst route under sabotage (${worst.path}) overflowed by only ` +
        `${worst.scrollWidth - worst.clientWidth}px, which is within rounding.`,
    ).toBeGreaterThan(20);
  });

  /**
   * ── THE EVIDENCE, PART TWO: `/sign-in`, WHICH NEEDS ITS OWN ────────────────
   *
   * `.signin-card { width: min(23rem, 100%) }` is what makes this route fit. The naive
   * version — the card claiming its design width regardless of the port — is the
   * revert, and it is the only shape of this failure the route can have: 368px of card
   * inside 1.5rem of padding a side needs 416px, against 390.
   *
   * Injected on its own, and asserted on its own, because the shell sabotage above is
   * **inert here**: none of `.app`, `.sidebar`, `.main` or `.page` exists on this
   * document. That is the whole argument for this route being a new case, and it is
   * driven rather than claimed — the shell revert is applied first and required *not*
   * to overflow.
   */
  test("the lock is not vacuous: /sign-in needs its own sabotage, and the shell's does nothing", async ({
    page,
  }) => {
    await reach(page, ROUTES[0]!);

    /* The shell revert, which is what `responsive-shell` uses on all nine of its
       routes. Here it must change nothing. */
    await page.addStyleTag({
      content: `
        @media (max-width: 900px) {
          .sidebar { min-width: auto; }
          .app { grid-template-columns: var(--sidebar-w) 1fr; }
          .main { min-width: auto; }
          .page { min-width: auto; }
        }`,
    });
    await page.evaluate(
      () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
    );
    const withShellRevert = await measure(page);
    expect(
      withShellRevert.scrollWidth,
      "The shell's own sabotage overflowed `/sign-in`, which would mean this route " +
        "does render inside `Shell` after all — and the argument for it being a " +
        "separate case, and for this file existing, needs re-making.",
    ).toBe(withShellRevert.clientWidth);

    /* And now the one that does apply to this document. */
    await page.addStyleTag({ content: `.signin-card { width: 23rem; }` });
    await page.evaluate(
      () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
    );
    const m = await measure(page);
    expect(
      m.scrollWidth - m.clientWidth,
      "Reverting `.signin-card`'s width clamp did not overflow the page, so the " +
        "`/sign-in` assertion above cannot tell a clamped card from an unclamped one. " +
        "Either the padding on `.signin` changed or something else is now holding the " +
        "card — re-measure before softening anything.\n" +
        `  Measured ${m.scrollWidth}px against a ${m.clientWidth}px viewport.`,
    ).toBeGreaterThan(0);
  });
});

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  The dropped columns are *readable*, not merely present
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `narrow-values-reachable.spec.ts` holds this for eight tables. **`/operators` and
 * `/audit` are not among them** — both were built after it — and they drop columns
 * exactly as the other eight do, so they inherit the same two mistakes:
 *
 *  1. **`toBeVisible()` is not reachability.** A `colSpan` list sized to a table that
 *     is wider than its scroll port starts off the right-hand edge, and Playwright
 *     calls it visible the whole way. The measurement is against `.table-scroll`'s
 *     `clientWidth`, which is the only thing that can tell the two apart. The
 *     geometry below is deliberately the same shape as that file's: list width against
 *     port width, and the **right** edge of the worst field against the port's right
 *     edge. A left-edge-only check is not enough.
 *  2. **`tbody[aria-busy=false]` is not a settle for the narrow hook:** a read at that
 *     moment can find `0` disclosure controls. The wait is on
 *     `[data-role="row-disclose"]` itself, and the expected count is computed from the
 *     row count rather than typed.
 */
test.describe("the columns these tables drop below 900px stay reachable", () => {
  for (const path of DISCLOSURE_ROUTES) {
    test(`${path} · every row discloses, and the values are inside the port`, async ({
      page,
      context,
    }) => {
      await signInAs(context, ADMINISTRATOR);
      await openFirstDisclosure(page, path);

      const g = await geometry(page);
      expect(
        Math.round(g.listWidth),
        `${path}: the disclosure list is ${Math.round(g.listWidth)}px wide inside a ` +
          `${g.portWidth}px scroll port, on a table that is ${Math.round(g.tableWidth)}px ` +
          "wide. It is sized to the table rather than to the port, which is exactly how " +
          "a dropped value ends up off the right-hand edge while still passing " +
          "`toBeVisible()`. See `.row-detail-list` in `admin.css` — `calc(100cqw - 2 * " +
          "var(--cell-px))`, and the container declared on `.table-wrap`.",
      ).toBeLessThanOrEqual(g.portWidth + 1);

      expect(
        Math.round(g.listX),
        `${path}: the disclosure list starts at ${Math.round(g.listX)}px, left of the ` +
          `scroll port at ${Math.round(g.portX)}px.`,
      ).toBeGreaterThanOrEqual(Math.round(g.portX) - 1);

      const portRight = g.portX + g.portWidth;
      expect(
        Math.round(g.worstFieldRight),
        `${path}: a field in the row disclosure reaches ` +
          `${Math.round(g.worstFieldRight)}px against a port ending at ` +
          `${Math.round(portRight)}px, so it is off the right-hand edge of a scroll ` +
          "container the operator has no reason to think scrolls.\n" +
          "  The columns were dropped so their values would be *readable*. A value " +
          "that has to be found before it can be read is the `.id-actions` mistake " +
          "again: the thing existed, and the device it was for could not reach it.",
      ).toBeLessThanOrEqual(Math.round(portRight) + 1);
    });
  }

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The geometry above is the only thing separating *"the value is one tap away"* from
   * *"the value is off the right-hand edge of a scroll container"*, so the reverted
   * state is injected and the measurement is required to fail.
   *
   * **Two stylesheets, and both are needed:**
   *
   *  - `NARROW_COLUMNS_REVERTED` (from `helpers/admin.ts`) makes the table wider than
   *    its port again. Measured here: `/operators` **650px** and `/audit` **636px**
   *    against a **356px** port. Without it these two tables fit, and reverting the
   *    list alone changes nothing — the `colSpan` cell it inherits from is already
   *    port-width.
   *  - `.row-detail-list { width: auto; position: static }` gives the `<dl>` that
   *    inherited width back, which is the version this test would otherwise certify.
   *
   * The list goes from 328px to the table's width, and the assertion below is on that
   * rather than on a remembered number.
   */
  test("the lock is not vacuous: sized to the table, the disclosure leaves the port", async ({
    page,
    context,
  }) => {
    await signInAs(context, ADMINISTRATOR);
    const measured: string[] = [];

    for (const path of DISCLOSURE_ROUTES) {
      await openFirstDisclosure(page, path);
      await page.addStyleTag({ content: NARROW_COLUMNS_REVERTED });
      await page.addStyleTag({
        content: `.row-detail-list { width: auto; position: static; }`,
      });
      await page.evaluate(
        () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
      );

      const g = await geometry(page);
      measured.push(
        `${path}: list ${Math.round(g.listWidth)}px, port ${g.portWidth}px, ` +
          `table ${Math.round(g.tableWidth)}px`,
      );
      expect(
        Math.round(g.listWidth),
        `${path}: reverting \`width: calc(100cqw …)\` and \`position: sticky\` left the ` +
          `disclosure list at ${Math.round(g.listWidth)}px in a ${g.portWidth}px port, ` +
          `on a ${Math.round(g.tableWidth)}px table. The sabotage no longer reproduces ` +
          "the defect, so the assertions above cannot distinguish the fix from its " +
          "absence — something else is now holding the list, and the numbers in the " +
          "comment need taking again.\n" +
          `  Measured: ${measured.join(" · ")}`,
      ).toBeGreaterThan(g.portWidth + 1);
    }
  });
});
