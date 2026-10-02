import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { MOCK_URL } from "../helpers/env.js";
import { tableReady } from "../helpers/admin.js";
import { focusReport, openDialog } from "../helpers/overlays.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `pending-does-not-eat-focus` — `disabled` on the control you are standing on
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * No dialog is involved in this one at all. `Accounts.tsx:144` set
 * `disabled={setEnabled.isPending}` on the button that had focus. Instrumented with
 * the request slowed to 2.5 s: the element is never removed, React simply disables
 * it, **the user agent blurs it to `<body>` with a null `relatedTarget`**, and nothing
 * restores it when the request settles. Focus was on `<body>` 600 ms in and still on
 * `<body>` after the request *failed* — so the operator is nowhere, with nothing
 * announced, on the one screen where something has just gone wrong.
 *
 * **Fourteen controls carried the pattern**, including `ConfirmDialog`'s own danger
 * button, which means pressing `Enter` on *Stop allowing it* dropped focus for the
 * whole request. Both are driven here: the smallest case (`/accounts` Disable) and
 * the worst one.
 *
 * `aria-disabled` is the answer — it announces the same unavailability, keeps the
 * element focusable, and moves the enforcement into the handler — so the assertions
 * are *focus stayed*, and the attribute is checked beside it so that a control which
 * silently stopped saying it was busy fails here too.
 *
 * **The slowing is the test.** Against a mock on localhost the pending state lasts a
 * frame, and every one of these cases is green without a fix at that speed. The
 * request is held for 2.5 s from `page.route`, which is what the original
 * measurement did by hand.
 *
 * `screens.test.ts` scans the source for the same rule and is not this test: it reads
 * `disabled={…isPending}` at the call site and cannot see focus, because
 * `@foyer/admin` has no jsdom. Note the regex it needs — `(?<![-\w])` rather than
 * `\b`, because a word boundary sits between the hyphen and the `d` of
 * `aria-disabled` and the obvious pattern reports every repair as the defect.
 */

const HOLD_MS = 2_500;

/** Everything this file created, torn down whether or not the test got that far. */
const created: { accounts: string[]; garden: string[] } = { accounts: [], garden: [] };

async function createAccount(request: APIRequestContext): Promise<{ id: string; username: string }> {
  const username = `e2e-pending-${Date.now()}`;
  const res = await request.post(`${MOCK_URL}/api/v0/accounts`, {
    data: { siteId: "site-hcm", username, password: "a long correct horse staple" },
  });
  if (!res.ok()) throw new Error(`POST /accounts → ${res.status()} ${await res.text()}`);
  const account = (await res.json()) as { id: string; username: string };
  created.accounts.push(account.id);
  return account;
}

async function createGardenEntry(request: APIRequestContext): Promise<{ id: string }> {
  const res = await request.post(`${MOCK_URL}/api/v0/sites/site-hcm/walled-garden`, {
    data: { kind: "fqdn", value: `e2e-pending-${Date.now()}.example.com` },
  });
  if (!res.ok()) throw new Error(`POST /walled-garden → ${res.status()} ${await res.text()}`);
  const entry = (await res.json()) as { id: string };
  created.garden.push(entry.id);
  return entry;
}

/*
 * The world is left as it was found, and it is *asserted* rather than attempted.
 *
 * Same rule as `expectNasRemoved`: the mock keeps its state across runs, so a row
 * this file leaves behind is in every later row count, dashboard figure and
 * screenshot until somebody restarts the mock by hand. A 404 here means the test
 * already removed it, which is the normal ending for the confirmation case.
 */
test.afterEach(async ({ request }) => {
  for (const id of created.accounts.splice(0)) {
    const res = await request.delete(`${MOCK_URL}/api/v0/accounts/${id}`);
    expect([204, 404], `cleaning up account ${id}`).toContain(res.status());
  }
  for (const id of created.garden.splice(0)) {
    const res = await request.delete(`${MOCK_URL}/api/v0/sites/site-hcm/walled-garden/${id}`);
    expect([204, 404], `cleaning up garden entry ${id}`).toContain(res.status());
  }
});

/** Holds one method on one path open for long enough to stand in the pending state. */
async function slow(page: Page, glob: string, method: string): Promise<void> {
  await page.route(glob, async (route) => {
    if (route.request().method() === method) await new Promise((r) => setTimeout(r, HOLD_MS));
    await route.continue();
  });
}

test.describe("a control that starts a request keeps focus while it runs", () => {
  test("/accounts — Disable, the smallest case", async ({ page, request }) => {
    const account = await createAccount(request);
    await slow(page, "**/api/v0/accounts/*", "PATCH");

    await page.goto("/accounts");
    await tableReady(page);
    const row = page.locator(`tbody tr[data-row-key="${account.id}"]`);
    await expect(row, "The account this test created is not in the table.").toHaveCount(1);

    /* The row's *first* control, which is Disable. Reached and pressed from the
       keyboard, because the whole finding is about where the keyboard is standing. */
    const disable = row.locator("button").first();
    await disable.focus();
    await expect(disable).toBeFocused();
    await disable.press("Enter");

    /* Mid-flight. The request is held for 2.5 s, so this is inside it by a margin. */
    await page.waitForTimeout(800);
    await expect(
      disable,
      `Focus is on ${await focusReport(page)} while the request runs. The element was ` +
        "never removed — React disabled it, and the user agent blurred it. That is " +
        "the whole defect, and `aria-disabled` is the whole fix.",
    ).toBeFocused();
    await expect(
      disable,
      "The control does not say it is busy while its request is in flight. " +
        "`aria-disabled` is what announces the unavailability that `disabled` used " +
        "to; dropping it to keep focus would trade one half of this for the other.",
    ).toHaveAttribute("aria-disabled", "true");

    /* And after it settles: the list has refetched and this row re-rendered. */
    await expect(disable).not.toHaveAttribute("aria-disabled", "true", { timeout: 10_000 });
    await expect(
      disable,
      `Focus is on ${await focusReport(page)} after the request settled.`,
    ).toBeFocused();
  });

  /**
   * The worst instance of the same pattern: the danger button of a destructive
   * confirmation, which is the control that has focus *because pressing it is what
   * started the request*.
   *
   * Only the pending window is asserted. Where focus goes once the row is gone is a
   * different rule with a different answer — `destructive-action-restores-focus`, and
   * `screens/refocus.ts` is where that one is written down.
   */
  test("a destructive confirmation keeps focus on its own danger button", async ({
    page,
    request,
  }) => {
    const entry = await createGardenEntry(request);
    await slow(page, "**/api/v0/sites/*/walled-garden/*", "DELETE");

    await page.goto("/garden");
    await tableReady(page);
    const row = page.locator(`tbody tr[data-row-key="${entry.id}"]`);
    await expect(row).toHaveCount(1);

    await row.locator("button.btn-ghost").first().press("Enter");
    const danger = openDialog(page).locator(".dialog-foot .btn-danger");
    await expect(danger).toHaveCount(1);
    await danger.focus();
    await danger.press("Enter");

    await page.waitForTimeout(800);
    await expect(
      danger,
      `Focus is on ${await focusReport(page)} while the deletion runs. Without the guard, pressing ` +
        "Enter on *Stop allowing it* drops focus for the whole request, on the one " +
        "screen where the thing that just happened cannot be undone.",
    ).toBeFocused();
    await expect(danger).toHaveAttribute("aria-disabled", "true");

    /* Let it finish so the row really goes and the cleanup above finds a 404. */
    await expect(row).toHaveCount(0, { timeout: 10_000 });
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The defect is one DOM property, so it goes back in one line: `disabled` is set on
   * the focused control from the page — which is exactly what React does for a
   * `disabled` prop — and the blur is watched happening.
   *
   * **Baseline without the fix:** `/accounts`, focus on a row's first control:
   *
   *     focused                        button.btn.btn-sm
   *     after `el.disabled = true`     <body>
   *
   * No request, no dialog, no removal: the attribute alone is enough, which is why
   * fourteen controls carry `aria-disabled` instead and why that is not a stylistic
   * preference.
   */
  test("the lock is not vacuous: `disabled` on the focused control blurs it to <body>", async ({
    page,
  }) => {
    await page.goto("/accounts");
    await tableReady(page);

    const button = page.locator("tbody tr:not(.empty-row) button").first();
    await button.focus();
    await expect(button).toBeFocused();

    await button.evaluate((el) => {
      (el as HTMLButtonElement).disabled = true;
    });

    await expect
      .poll(() => focusReport(page), {
        timeout: 2_000,
        message:
          "Setting `disabled` on the focused element did not blur it, so the two " +
          "cases above would be green against a console that still disables its " +
          "pending controls. Either Chromium changed, or this is not the element " +
          "that had focus.",
      })
      .toBe("<body>");
  });
});
