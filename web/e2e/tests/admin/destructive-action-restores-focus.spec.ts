import { expect, test, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { tableReady } from "../helpers/admin.js";
import { MOCK_URL } from "../helpers/env.js";
import { focusReport, openDialog } from "../helpers/overlays.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `destructive-action-restores-focus` — after the row is gone
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Measured: **after a confirmed delete, focus was `<body>` on all five destructive
 * surfaces.** This one has no platform answer to fall back on — the trigger's row has
 * gone, so "return focus to the opener" is not available and something deliberate has
 * to be chosen.
 *
 * `admin/src/screens/refocus.ts` chose it, and the rule is one rule rather than four
 * answers:
 *
 * > Focus goes to the first action on the row that took the removed row's place;
 * > failing that, the nearest row before it; failing that, the control that adds to
 * > the list.
 *
 * **So the rule is what is asserted, not a list of expected elements.** The expected
 * target is recomputed from the DOM *after* the removal, from the index the removed
 * row held — which means a fixture moving, a screen gaining a column, or a delete
 * that empties the table all keep this test meaningful, and a screen that quietly
 * stops following the rule fails it. A list of five element selectors would have gone
 * stale the first time a row order changed and would have been "fixed" by updating
 * the list.
 *
 * Two consequences of the rule are visible in what the cases below actually catch:
 *
 * - **The first control in the row, not the one in the same column.** On `/accounts`
 *   that is *Disable* rather than *Delete* — landing a keyboard operator on a Delete
 *   button one keystroke after they confirmed a deletion is a mis-press waiting to
 *   happen.
 * - **Forward first, then backward.** Deleting a middle row lands on the row that
 *   moved up; deleting the last row lands on the one before it. Both branches are
 *   driven, because a rule tested in one direction is half a rule.
 *
 * Every row these tests destroy is one they created. The mock keeps its world across
 * runs and has no way to put a deleted fixture back, so deleting a seeded row would
 * quietly shrink the ugly world every later test is measured against.
 *
 * **`/vouchers` · revoke is the fifth surface and is not driven here.** Revocation
 * needs a batch, and the mock has no `DELETE /voucher-batches/{id}` — a case for it
 * would leave a batch behind on every run, an accumulation somebody would have to
 * clean up by hand. `refocus.ts` records what it does
 * (the revoked row keeps its place and loses its only control, so the scan walks on),
 * and that is the same rule with no special case in it.
 */

interface Seeded {
  /** The row this surface's deletion is measured on. */
  rowKey: string;
}

/*
 * Everything seeded, taken back out afterwards whether or not the test got as far as
 * deleting it — and the cleanup is *asserted*, the same rule as `expectNasRemoved`.
 * A `404` is the normal ending here, because the console already removed it.
 */
const pending: (() => Promise<void>)[] = [];

test.afterEach(async () => {
  for (const undo of pending.splice(0)) await undo();
});

function remember(request: APIRequestContext, path: string): void {
  pending.push(async () => {
    const res = await request.delete(`${MOCK_URL}${path}`);
    expect([204, 404], `cleaning up ${path}`).toContain(res.status());
  });
}

/**
 * `/garden`, seeded twice so the row that takes the removed row's place exists.
 *
 * New entries land at the end of the list, so one entry alone would only ever
 * exercise the backward branch — which is the branch `/controllers` covers.
 */
async function seedGarden(request: APIRequestContext): Promise<Seeded> {
  const ids: string[] = [];
  for (const suffix of ["a", "b"]) {
    const res = await request.post(`${MOCK_URL}/api/v0/sites/site-hcm/walled-garden`, {
      data: { kind: "fqdn", value: `e2e-refocus-${suffix}-${Date.now()}.example.com` },
    });
    if (!res.ok()) throw new Error(`POST /walled-garden → ${res.status()} ${await res.text()}`);
    const entry = (await res.json()) as { id: string };
    ids.push(entry.id);
    remember(request, `/api/v0/sites/site-hcm/walled-garden/${entry.id}`);
  }
  return { rowKey: ids[0]! };
}

/** `/accounts`, where a new account is unshifted to the top of the list. */
async function seedAccount(request: APIRequestContext): Promise<Seeded & { username: string }> {
  const username = `e2e-refocus-${Date.now()}`;
  const res = await request.post(`${MOCK_URL}/api/v0/accounts`, {
    data: { siteId: "site-hcm", username, password: "a long correct horse staple" },
  });
  if (!res.ok()) throw new Error(`POST /accounts → ${res.status()} ${await res.text()}`);
  const account = (await res.json()) as { id: string };
  remember(request, `/api/v0/accounts/${account.id}`);
  return { rowKey: account.id, username };
}

/** `/controllers`, where a new controller is pushed to the end. */
async function seedController(request: APIRequestContext): Promise<Seeded> {
  const res = await request.post(`${MOCK_URL}/api/v0/controllers`, {
    data: {
      name: `e2e-refocus-${Date.now()}`,
      vendor: "unifi",
      baseUrl: "https://10.90.0.253:8443",
      authMode: "local-admin",
      username: "foyer",
      password: "a long correct horse staple",
      tlsMode: "trust-on-first-use",
    },
  });
  if (!res.ok()) throw new Error(`POST /controllers → ${res.status()} ${await res.text()}`);
  const controller = (await res.json()) as { id: string };
  remember(request, `/api/v0/controllers/${controller.id}`);
  return { rowKey: controller.id };
}

/**
 * `refocus.ts`'s rule, evaluated in the page against the table as it stands **now**.
 *
 * A second implementation of the rule rather than a shared one, deliberately: the
 * point is to state where focus ought to be from the design's own sentence and check
 * the console agrees, and importing the console's function would make the two
 * unfalsifiably identical.
 */
async function whereTheRuleSays(
  page: Page,
  index: number,
): Promise<{ agrees: boolean; active: string; expected: string }> {
  return page.evaluate((removedIndex) => {
    const describe = (el: Element | null | undefined): string => {
      if (!el) return "nothing";
      if (el === document.body) return "<body>";
      const row = el.closest("tr")?.getAttribute("data-row-key");
      const cls = typeof el.className === "string" ? el.className : "";
      return `${el.tagName.toLowerCase()}.${cls}${row ? ` in row ${row}` : ""}`;
    };

    const body = document.querySelector("tbody");
    if (!body) return { agrees: false, active: describe(document.activeElement), expected: "no table" };

    const rows = [...body.rows];
    /* Forward from where the row was, then backward from just before it. The removed
       row's index is the first candidate on purpose: whatever is there now is what
       took its place. */
    const search = [...rows.slice(removedIndex), ...rows.slice(0, removedIndex).reverse()];

    let expected: Element | null = null;
    for (const row of search) {
      const control = row.querySelector("button:not([disabled]), a[href]");
      if (control) {
        expected = control;
        break;
      }
    }
    /* Nothing left in the table at all — not even the empty state's own action. The
       control that adds to the list is the last thing on the page still about it. */
    expected ??=
      body
        .closest(".page")
        ?.querySelector(".page-actions button:not([disabled]), .page-actions a[href]") ?? null;

    return {
      agrees: expected !== null && document.activeElement === expected,
      active: describe(document.activeElement),
      expected: describe(expected),
    };
  }, index);
}

/** The `data-row-key` of the row that now sits at a given index, and of the focused row. */
async function rowKeyAt(page: Page, index: number): Promise<string | null> {
  return page.evaluate((i) => {
    const body = document.querySelector("tbody");
    return body?.rows[i]?.getAttribute("data-row-key") ?? null;
  }, index);
}

async function focusedRowKey(page: Page): Promise<string | null> {
  return page.evaluate(
    () => (document.activeElement as HTMLElement | null)?.closest("tr")?.getAttribute("data-row-key") ?? null,
  );
}

/** Confirms whatever dialog is up, typing the resource's name where one is asked for. */
async function confirm(page: Page, typeName?: string): Promise<void> {
  const dialog = openDialog(page);
  await expect(dialog).toHaveCount(1);
  const field = dialog.locator('input[type="text"]');
  if ((await field.count()) > 0 && typeName) await field.fill(typeName);
  await dialog.locator(".dialog-foot .btn-danger").click();
}

/** Deletes a row through the console from the keyboard, and returns its index. */
async function deleteThroughTheConsole(
  page: Page,
  row: Locator,
  typeName?: string,
): Promise<number> {
  const index = await row.evaluate((el) => (el as HTMLTableRowElement).sectionRowIndex);
  await row.locator("button.btn-ghost.btn-danger").first().focus();
  await page.keyboard.press("Enter");
  await confirm(page, typeName);
  await expect(row, "The row is still there, so nothing was deleted.").toHaveCount(0, {
    timeout: 10_000,
  });
  return index;
}

test.describe("a confirmed delete leaves focus somewhere deliberate", () => {
  test("/garden — the row that took its place", async ({ page, request }) => {
    const seeded = await seedGarden(request);
    await page.goto("/garden");
    await tableReady(page);

    const row = page.locator(`tbody tr[data-row-key="${seeded.rowKey}"]`);
    await expect(row).toHaveCount(1);
    const index = await deleteThroughTheConsole(page, row);

    await expectFollowsTheRule(page, index, "/garden");
    /* And it is the *forward* branch: the row that moved up into the removed row's
       index, not the one before it. Two entries are seeded for exactly this — with
       one, the deleted row would be last and only the backward branch would ever
       run, which is the branch `/controllers` below covers. */
    expect(
      await focusedRowKey(page),
      "Focus did not land on the row that took the removed row's place.",
    ).toBe(await rowKeyAt(page, index));
  });

  test("/accounts — the first control in the row, not the one in the same column", async ({
    page,
    request,
  }) => {
    const seeded = await seedAccount(request);
    await page.goto("/accounts");
    await tableReady(page);

    const row = page.locator(`tbody tr[data-row-key="${seeded.rowKey}"]`);
    await expect(row).toHaveCount(1);
    const index = await deleteThroughTheConsole(page, row, seeded.username);

    await expectFollowsTheRule(page, index, "/accounts");

    /* The consequence worth naming: the landing control is not another Delete. This
       screen is the only one of the three whose rows carry two actions, so it is the
       only one where the rule's "first control in the row" says something. */
    expect(
      await page.evaluate(() => (document.activeElement as HTMLElement | null)?.className ?? ""),
      "Focus landed on a `.btn-danger`, which is the destructive control on these " +
        "rows. One keystroke after confirming a deletion is not where a Delete " +
        "button should be waiting.",
    ).not.toContain("btn-danger");
    expect(await focusedRowKey(page)).toBe(await rowKeyAt(page, index));
  });

  test("/controllers — the nearest row before it", async ({ page, request }) => {
    const seeded = await seedController(request);
    await page.goto("/controllers");
    await tableReady(page);

    const row = page.locator(`tbody tr[data-row-key="${seeded.rowKey}"]`);
    await expect(row).toHaveCount(1);
    const index = await deleteThroughTheConsole(page, row);

    await expectFollowsTheRule(page, index, "/controllers");
    /* The backward branch: a controller is pushed to the end of the list, so the row
       deleted here was the last one and there is nothing at its index to move up. */
    expect(
      await focusedRowKey(page),
      "Focus did not land on the row before the one that was deleted, which is the " +
        "only place left when the last row goes.",
    ).toBe(await rowKeyAt(page, index - 1));
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The rule's own effect cannot be put back from the page — it is armed at the press
   * and spent on the render where the rows change identity, and neither is reachable
   * from a `page.evaluate`. What *is* reachable is the mechanism underneath it: a
   * focused element being removed from the document.
   *
   * `/garden`, focus on a row's delete control:
   *
   *     confirmed delete, as it stands   button.btn.btn-sm.btn-ghost in row garden-stale
   *     the focused row simply removed   <body>
   *
   * `<body>` is what was measured on all five destructive surfaces, and it is what
   * the platform does when the focused element goes: nothing. So there is a real gap
   * between "the row is gone" and "focus is somewhere", the assertions above are
   * measuring it, and they can go red.
   *
   * **The product-side failure**: without the single `armRefocus(ev.currentTarget)`
   * call in `Garden.tsx`:
   *
   *     /garden       ✘  "disagrees: focus on <body>, rule says
   *                       button.btn btn-sm btn-ghost in row garden-onbfyamh"
   *     /accounts     ✓
   *     /controllers  ✓
   *
   * **One red and two green against one hook** is the same shape as the
   * `ConfirmDialog` table in `confirm-dialog-focus-return`, and it is what says these
   * three cases are three call sites rather than three renderings of one. A lock that
   * drove a single screen would be satisfied by a repair that reached one of them. The
   * revert is not in this repository; the case below is the half that re-derives on
   * every run.
   */
  test("the lock is not vacuous: removing the focused row leaves focus on <body>", async ({
    page,
  }) => {
    await page.goto("/garden");
    await tableReady(page);

    const trigger = page.locator("tbody tr:not(.empty-row) button.btn-ghost").first();
    await trigger.focus();
    await expect(trigger).toBeFocused();

    await trigger.evaluate((el) => el.closest("tr")!.remove());

    await expect
      .poll(() => focusReport(page), {
        timeout: 2_000,
        message:
          "Removing the row that held focus did not leave focus on <body>, so the " +
          "platform is choosing a landing spot after all and the rule in " +
          "`refocus.ts` is answering a question nobody is asking. Re-measure the five " +
          "destructive surfaces before trusting this file.",
      })
      .toBe("<body>");
  });
});

/** The shared assertion: focus is where the rule says, and is not nowhere. */
async function expectFollowsTheRule(page: Page, index: number, route: string): Promise<void> {
  /*
   * Polled, because the refocus runs in a passive effect on the render where `rows`
   * changes identity — one task after the row leaves the DOM. A single read taken the
   * instant the row disappears reads the state *before* the rule has run and reports
   * a working console as `<body>`. That mistake is easy to make, against `Modal` as
   * much as here.
   *
   * The polled value carries the whole verdict rather than a boolean, so the failure
   * output says where focus actually is and where the rule says it should be —
   * "expected true, received false" would send the next reader to a debugger.
   */
  await expect
    .poll(
      async () => {
        const seen = await whereTheRuleSays(page, index);
        return `${seen.agrees ? "agrees" : "disagrees"}: focus on ${seen.active}, rule says ${seen.expected}`;
      },
      {
        timeout: 5_000,
        message:
          `${route}: focus after the deletion is not where \`screens/refocus.ts\` ` +
          "says it should be. Before the focus fix this was `<body>` on all five " +
          "destructive surfaces — the top of the document, and the operator's place " +
          "in a list they were triaging gone.",
      },
    )
    .toMatch(/^agrees:/);
}
