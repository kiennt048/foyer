import { expect, test, type APIRequestContext, type Page, type Route } from "@playwright/test";
import { MOCK_URL } from "../helpers/env.js";
import { ADMINISTRATOR, shellSettled, signInAs, signOut } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `/problems/last-administrator` — the refusal no gesture in the product reaches
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * ── READ THIS BEFORE DELETING THE `page.route` BELOW ────────────────────────
 *
 * **The interception is the test, not scaffolding around it.**
 *
 * `OperatorsScreen` computes `wouldOrphanTheDeployment` from the rows it is holding
 * and *disables* the three controls on the row it identifies. On the fixture world
 * that computation is correct — one enabled administrator (`op-viet`), one disabled
 * (`op-ha`) — so the three controls are disabled and **there is no click that produces
 * a `409`**. Playwright will not click a `disabled` button, and forcing one would not
 * dispatch the handler either.
 *
 * Nor can the page be argued into staleness from the outside. The screen's own comment
 * calls its count *a courtesy, not the rule*: the list is one page, so a deployment
 * with more operators than fit it could disagree with the server. On **this** world it
 * cannot disagree in the permissive direction — five operators fit on one page, and
 * searching only ever *removes* rows, which can only make the client believe an orphan
 * is nearer than it is. The client over-restricts or agrees; it never under-restricts.
 *
 * So the only way to reach the server's refusal is to hand the page a list it would
 * have believed if the world were one operator different: `op-ha.enabled` rewritten to
 * `true` **in the `GET` response only**. Two enabled administrators, no row identified
 * as the last one, all three controls live — and the server, which is looking at the
 * real world, refuses.
 *
 * ── WHY IT IS SAFE TO RUN IN CI ─────────────────────────────────────────────
 *
 * **Neither path mutates.** Both handlers in `web/mock/src/server.ts` compute
 * `wouldOrphanTheDeployment` and `return` the `409` *before* touching `world` — the
 * `PATCH` before its first assignment, the `DELETE` before its `splice`. Each test
 * below re-reads `GET /operators` afterwards, straight at the mock and outside the
 * interception, and asserts the row is byte-for-byte the one it started with. That
 * re-read is not belt-and-braces: it is what turns *"the dialog stayed open"* into
 * evidence, because a dialog that stays open is byte-identical whether the refusal
 * arrived or `onError` simply did nothing.
 *
 * ── AND WHY BOTH PATHS ──────────────────────────────────────────────────────
 *
 * `PATCH` and `DELETE` are two operations, two server branches, and — more to the
 * point — **two entirely different renderings of the same refusal**. The `PATCH` lands
 * in `update`'s `onError` and becomes a toast in a polite live region; the `DELETE`
 * lands in `del.error` and is announced *inside* the open modal, because the page
 * behind it is inert and a toast out there is neither reachable nor announced. A lock
 * on one says nothing about the other.
 */

/** The username the fixtures give the single enabled administrator. */
const LAST_ADMINISTRATOR = ADMINISTRATOR;
/** The disabled administrator whose `enabled` flag the interception rewrites. */
const DISABLED_ADMINISTRATOR_ID = "op-ha";

interface MockOperator {
  id: string;
  username: string;
  role: "administrator" | "staff";
  enabled: boolean;
}

const listOperators = (url: URL) => url.pathname === "/api/v0/operators";

/**
 * Rewrites the list the page reads, and **only** the list.
 *
 * `route.fetch()` so the response is the mock's own — the fixture's ids, timestamps
 * and display names travel through untouched, and a fixture change does not need a
 * matching edit here. Anything that is not a `GET` on the collection is handed
 * straight back, which is what keeps the `PATCH` and the `DELETE` going to the real
 * server. That separation is the whole mechanism: **the client is stale, the server is
 * not.**
 */
async function serveStaleOperatorList(page: Page): Promise<void> {
  await page.route(listOperators, async (route: Route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const response = await route.fetch();
    const body = (await response.json()) as { items: MockOperator[]; nextCursor: string | null };
    const target = body.items.find((o) => o.id === DISABLED_ADMINISTRATOR_ID);
    expect(
      target,
      `The fixture world no longer contains \`${DISABLED_ADMINISTRATOR_ID}\`, so this ` +
        "interception rewrites nothing and the controls under test stay disabled. See " +
        "`buildOperators` in `web/fixtures/src/index.ts`.",
    ).toBeTruthy();
    expect(
      target!.enabled,
      `\`${DISABLED_ADMINISTRATOR_ID}\` is already enabled in the fixtures. There are ` +
        "now two enabled administrators in the real world, `wouldOrphanTheDeployment` " +
        "is false on the server as well as on the client, and the `409` this file " +
        "exists for can no longer be reached at all.",
    ).toBe(false);
    target!.enabled = true;
    await route.fulfill({
      response,
      contentType: "application/json",
      body: JSON.stringify(body),
    });
  });
}

/** The real world, read outside the interception. */
async function realOperator(
  request: APIRequestContext,
  username: string,
): Promise<MockOperator | undefined> {
  const res = await request.get(`${MOCK_URL}/api/v0/operators`);
  expect(res.status(), "Re-reading the operator list failed.").toBe(200);
  const body = (await res.json()) as { items: MockOperator[] };
  return body.items.find((o) => o.username === username);
}

/**
 * The toast region — and **not** the table's row-count region.
 *
 * Both are `aria-live="polite"`, and a bare `[aria-live="polite"]` resolves to two
 * elements on this screen. `Table` renders its own, present and empty, on every route
 * in the console. The `:not()` names the one that is not the toasts by its `data-role`
 * handle rather than by position, for the same reason the refused-route lock in
 * `role-gating.spec.ts` asserts text rather than presence: a live region that exists is not a live region that said
 * anything.
 */
const TOASTS = '[aria-live="polite"]:not([data-role="row-count-live"])';

/** The row the refusal is about, once the stale list has enabled its three controls. */
function rowFor(page: Page, username: string) {
  return page.locator("tbody tr:not(.empty-row)").filter({ hasText: username });
}

test.afterEach(async ({ context }) => {
  await signOut(context);
});

test.describe("the last administrator cannot be demoted or deleted", () => {
  /**
   * The `PATCH` path — *Make staff* on the only enabled administrator.
   *
   * The refusal arrives in `update`'s `onError`, which raises a toast. `Toasts` is a
   * `aria-live="polite"` region, so the assertion is on the **text** of what it
   * carries: a region that exists is not a region that said anything, and this console
   * carries one on every route.
   */
  test("PATCH · demoting them is refused, announced, and changes nothing", async ({
    page,
    context,
    request,
  }) => {
    await signInAs(context, ADMINISTRATOR);
    const before = await realOperator(request, LAST_ADMINISTRATOR);
    expect(before, "The fixture administrator is missing.").toBeTruthy();

    await serveStaleOperatorList(page);
    await page.goto("/operators");
    await shellSettled(page);
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

    const row = rowFor(page, LAST_ADMINISTRATOR);
    await expect(row).toHaveCount(1);

    /*
     * The interception did its work. Without this the click below would fail with
     * "element is not enabled" and the failure would read as a product change rather
     * than as a broken harness.
     */
    const actions = row.locator(".row-actions button");
    await expect(
      actions,
      "The row still offers no live controls, so the stale list did not reach the " +
        "page. The interception is the mechanism of this test — see the header.",
    ).toHaveCount(3);
    for (let i = 0; i < 3; i++) {
      await expect(actions.nth(i), `row action ${i} is still disabled`).toBeEnabled();
    }

    /* The middle control is the role change: disable/enable, role, delete. */
    await actions.nth(1).click();

    const toasts = page.locator(TOASTS);
    await expect(toasts, "The toast region is not on the page.").toHaveCount(1);
    await expect(
      toasts,
      "The `409` produced no announcement. A refusal on a low-friction action still " +
        "has to be reported: without it the button silently re-enables and the pill " +
        "snaps back, which reads as *it did not take* rather than as *the server " +
        "refused*.",
    ).toContainText(/last administrator/i, { timeout: 10_000 });

    /* And the world is where it was. This is what makes the assertion above evidence
       rather than a description of a page that never changed. */
    const after = await realOperator(request, LAST_ADMINISTRATOR);
    expect(
      after,
      "The refused `PATCH` changed the operator anyway. The mock returns the `409` " +
        "before its first assignment, so this is a server change, not a test one.",
    ).toEqual(before);
  });

  /**
   * The `DELETE` path — the typed-name confirmation, and the refusal *inside* it.
   *
   * `ConfirmDialog`'s `type-name` tier renders no cancel button by design, so the
   * dialog is dismissed by `Escape`. It also renders the error through `ErrorNotice`,
   * which is `role="alert"` — and asserting on that node's **text** is the point:
   * *"the dialog stays open on a `409`"* is byte-identical to *"`onError` did
   * nothing"*, and only the text and the re-read below tell them apart.
   */
  test("DELETE · deleting them is refused inside the dialog, and changes nothing", async ({
    page,
    context,
    request,
  }) => {
    await signInAs(context, ADMINISTRATOR);
    const before = await realOperator(request, LAST_ADMINISTRATOR);
    expect(before, "The fixture administrator is missing.").toBeTruthy();

    await serveStaleOperatorList(page);
    await page.goto("/operators");
    await shellSettled(page);
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

    const row = rowFor(page, LAST_ADMINISTRATOR);
    const del = row.locator(".row-actions button").nth(2);
    await expect(
      del,
      "The delete control is still disabled, so the stale list did not reach the page.",
    ).toBeEnabled();
    await del.click();

    const dialog = page.locator("dialog[open]");
    await expect(dialog).toHaveCount(1);
    /* It really is the typed-name tier, so the field below exists for a reason. */
    await expect(
      dialog.locator(".dialog-foot .btn-danger"),
      "The dialog that opened has no destructive button, so this is not the deletion " +
        "confirmation.",
    ).toHaveCount(1);

    await dialog.locator("input[type=text]").fill(LAST_ADMINISTRATOR);
    await dialog.locator(".dialog-foot .btn-danger").click();

    await expect(
      dialog.locator("[role=alert]"),
      "The refused deletion said nothing inside the dialog. The page behind a modal " +
        "is inert, so a notice or a toast out there is neither reachable nor " +
        "announced — without `ConfirmDialog`'s `error`, a `500` leaves the dialog " +
        "byte-for-byte unchanged with focus on `<body>`, which reads as " +
        "*nothing happened* on an action that has no undo.",
    ).toContainText(/last administrator/i, { timeout: 10_000 });

    await expect(
      page.locator("dialog[open]"),
      "The dialog closed on a refusal, so the operator was returned to a list that " +
        "still contains the row they believe they deleted.",
    ).toHaveCount(1);

    const after = await realOperator(request, LAST_ADMINISTRATOR);
    expect(
      after,
      "The refused `DELETE` removed the operator anyway. The mock returns the `409` " +
        "before its `splice`, so this is a server change, not a test one.",
    ).toEqual(before);

    await dialog.press("Escape");
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The two tests above are worth exactly as much as this one, which proves the two
   * things they rest on and which nothing else in the suite states:
   *
   *  1. **without the interception the controls are disabled** — so the drives above
   *     really are reaching a state the product does not otherwise offer, and a reader
   *     who deletes the `page.route` as scaffolding gets a red test rather than a
   *     quietly weaker one;
   *  2. **the server refuses on the real world regardless of what the page believes** —
   *     asked directly, with no browser in the loop.
   *
   * The second half is what makes the interception legitimate rather than a
   * fabrication: it is not inventing the refusal, it is removing the client-side
   * courtesy that stops the operator ever seeing it.
   */
  test("the lock is not vacuous: without the stale list the controls are disabled, and the server still refuses", async ({
    page,
    context,
    request,
  }) => {
    await signInAs(context, ADMINISTRATOR);
    await page.goto("/operators");
    await shellSettled(page);
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

    const actions = rowFor(page, LAST_ADMINISTRATOR).locator(".row-actions button");
    await expect(actions).toHaveCount(3);
    for (let i = 0; i < 3; i++) {
      await expect(
        actions.nth(i),
        `Row action ${i} on the last administrator is clickable without the ` +
          "interception. Either the fixture world gained a second enabled " +
          "administrator — in which case the two tests above are now measuring a row " +
          "that is not the last one — or `wouldOrphanTheDeployment` in " +
          "`OperatorsScreen` stopped disabling it, which is a product regression: the " +
          "operator is being offered three controls the server answers `409` to.",
      ).toBeDisabled();
    }

    /* And the refusal is real, asked directly. `role` rather than `enabled`, so this
       probe cannot disable anybody even if the server stopped refusing. */
    const operator = await realOperator(request, LAST_ADMINISTRATOR);
    const refused = await request.patch(`${MOCK_URL}/api/v0/operators/${operator!.id}`, {
      data: { role: "staff" },
    });
    expect(
      refused.status(),
      "The server accepted demoting the only enabled administrator. That is the rule " +
        "`/problems/last-administrator` exists for, and there is no password-reset " +
        "channel and no recovery short of the Go binary's first-boot path.",
    ).toBe(409);
    expect((await refused.json()).type).toBe("/problems/last-administrator");

    /* Belt and braces on a probe that is meant not to mutate. */
    expect(
      await realOperator(request, LAST_ADMINISTRATOR),
      "The refused probe changed the world.",
    ).toEqual(operator);
  });
});
