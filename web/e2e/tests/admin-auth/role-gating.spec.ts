import { expect, test, type Page } from "@playwright/test";
import { ADMINISTRATOR, STAFF, shellSettled, signInAs, signOut } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `/operators` and `/audit` — the role gate, driven **both ways**
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The mock's role gate is live whenever a session exists (`REQUIRE_SESSION` is about
 * *authentication*, not authorization — see `web/mock/src/server.ts`). Sign in as
 * `le.minh.tuan` and `GET /operators` answers a real `403`. So both halves of this are
 * drivable today, and the refusal has copy that a person has actually looked at.
 *
 * ── THE TEST THIS FILE DELIBERATELY DOES NOT CONTAIN ────────────────────────
 *
 * *"A staff member sees no row actions on `/operators`."*
 *
 * It is **vacuous by construction**, and this note exists so nobody writes it. `listOperators` requires `operators:manage`, so a staff session renders **zero
 * rows** — and whether the actions column was filtered is therefore unobservable. The
 * assertion is green today, green if `columns.filter(…)` is deleted, and green if
 * `useCan` is stubbed to return `true`.
 *
 * The observable half is the **Add operator** button in the page header, which
 * `OperatorsScreen` renders from `canManage` *independently of the query*. That is
 * what is asserted below, in both directions.
 *
 * ── AND THE COUNT THAT IS OFF BY TWO ────────────────────────────────────────
 *
 * `/operators` mounts its `ConfirmDialog` **always** and toggles it with `open` — a
 * conditionally mounted `<dialog>` leaves the document while still open, so `close()`
 * never runs and focus never returns, which is the defect `confirm-dialog-focus-return`
 * exists for. The consequence here is that a closed, invisible dialog contributes one
 * `input` and one `button` to `main` for **both** roles. Any control count that does
 * not exclude dialog descendants is silently off by two, and drifts the moment a
 * second always-mounted overlay appears. `:not(dialog *)` is the scope, and the last
 * test in this file proves the exclusion is doing work rather than being decoration.
 */

const ADD_OPERATOR = "main .page-head .page-actions button";
const NAV_OPERATORS = 'nav.nav a[href="/operators"]';
const NAV_AUDIT = 'nav.nav a[href="/audit"]';
const NAV_PRIVACY = 'nav.nav a[href="/privacy"]';

test.afterEach(async ({ context }) => {
  await signOut(context);
});

/**
 * Every control `main` offers, with the always-mounted confirmation excluded.
 *
 * `:not(dialog *)` and not `:not(dialog)`: the dialog element itself is not a control,
 * its *descendants* are. The unscoped figure is returned alongside so a failure can
 * say which of the two moved.
 */
async function controlCounts(page: Page): Promise<{ scoped: number; unscoped: number }> {
  return page.evaluate(() => {
    const main = document.querySelector("main");
    if (!main) return { scoped: -1, unscoped: -1 };
    return {
      scoped: main.querySelectorAll("button:not(dialog *), input:not(dialog *)").length,
      unscoped: main.querySelectorAll("button, input").length,
    };
  });
}

test.describe("the role gate on /operators", () => {
  test("an administrator is offered the control that adds one", async ({ page, context }) => {
    await signInAs(context, ADMINISTRATOR);
    await page.goto("/operators");
    await shellSettled(page);
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

    await expect(
      page.locator(ADD_OPERATOR),
      "An administrator was not offered *Add operator*. This control renders from " +
        "`canManage` and is independent of the query, which is what makes it the one " +
        "observable half of the gate — see the header of this file.",
    ).toHaveCount(1);
    await expect(page.locator("tbody tr:not(.empty-row)")).not.toHaveCount(0);
  });

  test("a staff member is not, and the refusal is the server's", async ({ page, context }) => {
    const session = await signInAs(context, STAFF);
    expect(
      session.permissions,
      "The fixture staff operator has gained `operators:manage`, so this file no " +
        "longer measures a gate.",
    ).not.toContain("operators:manage");

    await page.goto("/operators");
    await shellSettled(page);
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

    await expect(
      page.locator(ADD_OPERATOR),
      "A staff session was offered *Add operator*. The console gates on " +
        "`AuthSession.permissions` through `useCan`; a control that renders here is " +
        "one that leads to a `403`.",
    ).toHaveCount(0);

    /* The refusal really came from the server rather than from an empty fixture: the
       table is showing the error state, not the empty state. */
    await expect(
      page.locator("tbody tr:not(.empty-row)"),
      "A staff session rendered operator rows, so `GET /operators` was not refused " +
        "and the mock's role gate is no longer live.",
    ).toHaveCount(0);
  });

  /**
   * The two destinations leave the sidebar together.
   *
   * `App.tsx` makes this one rule rather than four places to remember — sidebar,
   * drawer, command palette and shortcut sheet are filtered from one list — so the
   * sidebar is a faithful proxy for all four.
   */
  test("the two administrator destinations leave the navigation for a staff session", async ({
    page,
    context,
  }) => {
    await signInAs(context, ADMINISTRATOR);
    await page.goto("/");
    await shellSettled(page);
    await expect(page.locator(NAV_OPERATORS)).toHaveCount(1);
    await expect(page.locator(NAV_AUDIT)).toHaveCount(1);
    await expect(page.locator(NAV_PRIVACY)).toHaveCount(1);
    const asAdministrator = await page.locator("nav.nav a.nav-item").count();

    await signOut(context);
    await signInAs(context, STAFF);
    await page.goto("/");
    await shellSettled(page);

    await expect(
      page.locator(NAV_OPERATORS),
      "A staff session was offered `/operators` in the navigation.",
    ).toHaveCount(0);
    await expect(
      page.locator(NAV_AUDIT),
      "A staff session was offered `/audit` in the navigation.",
    ).toHaveCount(0);
    await expect(
      page.locator(NAV_PRIVACY),
      "A staff session was offered `/privacy` in the navigation.",
    ).toHaveCount(0);

    /* The difference is exactly the permissioned rows in `NAV` — operators, the audit
       log and privacy. */
    expect(
      await page.locator("nav.nav a.nav-item").count(),
      "The staff navigation is not the administrator's minus the three permissioned " +
        "destinations, so something other than the permission filter changed it.",
    ).toBe(asAdministrator - 3);
  });

  /**
   * The control count, scoped — and the proof the scope is load-bearing.
   *
   * Two roles, one page, and the closed `ConfirmDialog` contributes to `main` in
   * both. The assertion is on the *difference* between scoped and unscoped, which is
   * the part that is invariant across roles and is exactly what a naive count would
   * silently absorb.
   */
  test("counting controls excludes the always-mounted confirmation, for both roles", async ({
    page,
    context,
  }) => {
    await signInAs(context, ADMINISTRATOR);
    await page.goto("/operators");
    await shellSettled(page);
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();
    const administrator = await controlCounts(page);

    await signOut(context);
    await signInAs(context, STAFF);
    await page.goto("/operators");
    await shellSettled(page);
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();
    const staff = await controlCounts(page);

    /* The dialog is closed and invisible, and it is still in the document. */
    await expect(
      page.locator("main dialog"),
      "`/operators` no longer mounts its `ConfirmDialog` unconditionally. That would " +
        "be a *product* change with its own defect attached — a conditionally mounted " +
        "`<dialog>` leaves the document while still open, so `close()` never runs and " +
        "focus never returns. See `confirm-dialog-focus-return`.",
    ).toHaveCount(1);
    await expect(page.locator("main dialog[open]")).toHaveCount(0);

    for (const [role, counts] of [
      ["administrator", administrator],
      ["staff", staff],
    ] as const) {
      expect(
        counts.unscoped - counts.scoped,
        `As ${role}, the closed confirmation contributed ` +
          `${counts.unscoped - counts.scoped} controls to an unscoped count of ` +
          `\`main\` rather than the 2 it renders (one text input, one danger button).\n` +
          "  This is the assertion that keeps a control count from drifting: an " +
          "unscoped count is off by exactly this much for *both* roles, so it looks " +
          "stable while measuring the wrong thing. If the number moved, an overlay " +
          "was added or removed — re-derive the scope, do not adjust this number.",
      ).toBe(2);
    }

    /* And the gate really does remove something. Not a fixed number: the staff table
       has no rows, so most of the difference has nothing to do with the gate. */
    expect(
      staff.scoped,
      "A staff session offers `main` at least as many controls as an administrator " +
        "does, so nothing on this screen is gated at all.",
    ).toBeLessThan(administrator.scoped);
  });
});

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  A REFUSED ROUTE — what happens to a staff member who arrives anyway
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * A staff member reaches these routes by typing the URL, by a stale bookmark, or by
 * being demoted while the console is open — which is not exotic: `/operators` is where
 * the demotion happens.
 *
 * ── WHAT HOLDS EACH ──────────────────────────────────────────────────────────
 *
 * The retry is `worthAskingAgain` in `admin/src/main.tsx` — never retry a `4xx`, once
 * for `5xx` and for the synthesised `status === 0`. The announcement is
 * `admin/src/ui/table.tsx`: the empty row's refusal mounts carrying `role="alert"` and
 * its own text.
 *
 * The announcement case asserts *something announced the refusal*, not that a live
 * region **exists** (see below), because a selector that had quietly stopped matching
 * would read exactly like a pass. Stripping the announcement from the running page —
 * `[role=alert]` removed and the row-count region emptied, between `shellSettled` and
 * the read — turns both routes red on `{ alerts: 0, liveText: "" }`.
 *
 * That is a manual check rather than a case here on purpose: as a permanent test it
 * would assert only that `page.evaluate` can remove a node.
 */
test.describe("a refused administrator route", () => {
  const ROUTES = ["/operators", "/audit"] as const;

  for (const route of ROUTES) {
    /**
     * Do not retry a refusal — a bare `retry: 1` in `main.tsx` would.
     *
     * A `403` is a permanent refusal: the operator's role is not going to change
     * between two requests one tick apart. Asking again cannot turn it into a `200`,
     * so the only thing a retry buys is a second entry in the server's log and a
     * slower refusal on screen. The `worthAskingAgain` predicate is what prevents it.
     */
    test(`${route} does not retry a 403`, async ({ page, context }) => {
      await signInAs(context, STAFF);

      const path = `/api/v0${route}`;
      const attempts: number[] = [];
      page.on("response", (res) => {
        if (new URL(res.url()).pathname === path) attempts.push(res.status());
      });

      await page.goto(route);
      await shellSettled(page);
      await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();
      /* Long enough for a retry to have happened if one is going to. */
      await page.waitForTimeout(2_000);

      expect(
        attempts,
        `${route} was requested ${attempts.length} times for one staff visit — ` +
          `statuses ${JSON.stringify(attempts)}.\n` +
          "  A `403` is a decision about who this operator " +
          "is; it cannot become a `200` by being asked again. The fix is a `retry` " +
          "predicate in `admin/src/main.tsx` that does not retry 4xx, not a change " +
          "here.",
      ).toHaveLength(1);
    });

    /**
     * Announce the refusal — a silent one is announced to nobody, and axe calls the page clean.
     *
     * A refusal rendered inside a `<tr class="empty-row">` with no live region and no
     * role, *after* `aria-busy` has already flipped to `false`, with focus on `<body>`,
     * tells a screen-reader operator that the table finished loading and nothing else.
     *
     * **axe reports zero violations on exactly this state** — it is in both sweeps and
     * both are green on it. That is the clearest instance in this
     * repository of what the accessibility gate cannot see, and the reason these ten
     * keyboard-and-focus locks exist beside it.
     *
     * ── THE VERSION OF THIS TEST THAT IS GREEN AGAINST THE DEFECT ─────────────
     *
     * Asking for
     * `main [role=alert], main [role=status], main [aria-live]:not(:empty)` passes, because
     * `Table` renders a `role="status" aria-live="polite"` region **on every route,
     * present and empty**, so the second and third clauses matched a node that says
     * nothing. A console can carry a live region on every screen and be silent, which
     * is the whole reason `Notice` mounts with its text rather than being filled later.
     *
     * So the question is *what was announced*, not *what exists*: either an `alert`
     * appeared for the refusal, or the region that is already there carries text about
     * it. Both are read, and both are reported on failure.
     */
    test(`${route} announces the refusal`, async ({ page, context }) => {
      await signInAs(context, STAFF);
      await page.goto(route);
      await shellSettled(page);
      await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

      /* The refusal is on screen — otherwise the assertion below would be about a
         page that has not finished rather than about a page that is silent. */
      await expect(
        page.locator("tbody tr.empty-row"),
        "No empty row rendered, so the refusal never reached the table.",
      ).toHaveCount(1);

      const alerts = await page.locator("main [role=alert]").count();
      const liveText = (
        (await page.locator('main [data-role="row-count-live"]').textContent()) ?? ""
      ).trim();

      expect(
        { alerts, liveText },
        `${route} refused a staff session and nothing on the page announced it.\n` +
          "  `aria-busy` had already flipped to `false`, so " +
          "the only thing a screen reader was told is that the table finished " +
          "loading, and focus was on `<body>`. Compounding it, the headline says " +
          "*Reload the page to try again*, which is wrong advice for a permanent " +
          "refusal — only the second sentence corrects it.\n" +
          "  Note what is *not* evidence here: axe reports zero violations on this " +
          "exact state, in both schemes and at both widths. It is in both sweeps " +
          "and both are green on it.\n" +
          "  Note also what this assertion is *not*: the presence of a live region. " +
          "`Table` renders a `role=\"status\"` one on every route, present and empty, " +
          "and a test that merely looks for it is green against the defect.",
      ).not.toEqual({ alerts: 0, liveText: "" });
    });
  }
});
