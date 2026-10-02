import { expect, test } from "@playwright/test";
import { CONFIRM_SITES, focusReport, openConfirm, openDialog } from "../helpers/overlays.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `confirm-dialog-focus-return` — the *other* dialog, and the *other* defect
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `ConfirmDialog` is not `Modal`, and the `Modal` focus fix does not reach it. The
 * primitive is correct on its own terms — it calls `el.close()` when `open` goes
 * false, and the platform's return of focus rides on that call. What differs is the
 * **call site** — baseline without the fix:
 *
 *     Accounts.tsx:242    always mounted, `open` toggles   →  returns to the trigger
 *     Controllers.tsx:298 `{confirmDelete ? <…> : null}`   →  <body>
 *     Garden.tsx:289      `{confirmDelete ? <…> : null}`   →  <body>
 *
 * **Two red and one green against one primitive** says the call site, not `ui/`, is
 * where the fix belongs. That asymmetry is kept as a
 * test, because a repair that patches one site is otherwise "satisfied" by a suite
 * that only ever drove one of them. All three are driven here and the list is the
 * inventory, so a fourth confirmation is covered by adding a row
 * to it.
 *
 * `screens.test.ts` guards the same rule in source — a bare `open` prop is the tell
 * for the conditional mount — and it cannot observe focus, because `@foyer/admin` has
 * no jsdom. This is the behavioural half.
 */

test.describe("a destructive confirmation returns focus to the row control that opened it", () => {
  for (const site of CONFIRM_SITES) {
    test(`${site.id} — Escape`, async ({ page }) => {
      const { trigger, dialog } = await openConfirm(page, site);

      await dialog.press("Escape");
      await expect(openDialog(page), `${site.id}: Escape did not close it.`).toHaveCount(0);

      await expect(
        trigger,
        `${site.id}: focus is on ${await focusReport(page)} after Escape. The row is ` +
          "still there — nothing was deleted — so the control that asked the question " +
          "is exactly where the operator should be standing.",
      ).toBeFocused();
    });
  }

  /**
   * The Cancel half, and the count is derived because it is **two**, not three.
   *
   * `/accounts` escalates to the type-name tier, which renders no Cancel button on
   * purpose: there is no symmetric pair of buttons to mis-click, and Escape and the
   * backdrop are the dismissals. So "Cancel on all three" is a sentence the product
   * cannot satisfy, and the honest lock derives which sites have one rather than
   * asserting a number — the same shape as the probe result having no cancel control
   * in `dialog-focus-return`.
   */
  test("and by Cancel, on every confirmation that offers one", async ({ page }) => {
    const withCancel: string[] = [];
    const withoutCancel: string[] = [];

    for (const site of CONFIRM_SITES) {
      const { trigger, dialog } = await openConfirm(page, site);
      /* In a `ConfirmDialog` foot there are only two kinds of button — the danger
         one and Cancel — so what is left after excluding the destructive one is the
         dismissal. No click is needed to find out, unlike `Modal`'s feet. */
      const cancel = dialog.locator(".dialog-foot button:not(.btn-danger)");

      if ((await cancel.count()) === 0) {
        withoutCancel.push(site.id);
        await expect(
          dialog.locator('input[type="text"]'),
          `${site.id}: a confirmation with no Cancel button and no name to type is ` +
            "one with no stated reason for having neither.",
        ).toHaveCount(1);
        await dialog.press("Escape");
        continue;
      }

      withCancel.push(site.id);
      await cancel.first().click();
      await expect(openDialog(page)).toHaveCount(0);
      await expect(
        trigger,
        `${site.id}: focus is on ${await focusReport(page)} after Cancel.`,
      ).toBeFocused();
    }

    expect(
      withoutCancel,
      "The set of confirmations with no Cancel button has changed. It was exactly " +
        "one — `/accounts`, whose type-name tier deliberately renders none. A new " +
        "entry here is either a second type-name escalation or a Cancel button that " +
        "went missing, and the two are not the same news.",
    ).toEqual(["accounts · delete"]);
    expect(withCancel.length).toBe(CONFIRM_SITES.length - withoutCancel.length);
    expect(
      withCancel.length,
      "No confirmation offered a Cancel, so the focus assertion above never ran.",
    ).toBeGreaterThan(1);
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The failure on these sites is literally "the element is removed from the document
   * while still open", so that is what this puts back — from the page, on `/accounts`.
   * That is the deliberate choice: its call site needs no special handling, so a sabotage
   * that turns it red is showing the assertion responds to the mechanism rather than
   * to the screen.
   *
   * **Baseline without the fix:** `/accounts` · delete:
   *
   *     Escape                                →  button.btn.btn-sm.btn-ghost (the trigger)
   *     the open dialog removed instead       →  <body>
   *
   * `<body>` is also what `/controllers` and `/garden` give without the focus fix, on
   * this same primitive, for exactly this reason.
   */
  test("the lock is not vacuous: a confirmation removed while open leaves focus on <body>", async ({
    page,
  }) => {
    const site = CONFIRM_SITES.find((s) => s.id === "accounts · delete")!;
    const { trigger } = await openConfirm(page, site);

    await openDialog(page).evaluate((el) => el.remove());
    await expect(openDialog(page)).toHaveCount(0);

    await expect
      .poll(() => focusReport(page), {
        timeout: 2_000,
        message:
          "Removing an open `ConfirmDialog` from the document did not leave focus on " +
          "<body>, so `el.close()` is not what the return of focus depends on and " +
          "the three-way table in this file's header has a different cause than the one recorded.",
      })
      .toBe("<body>");

    await expect(trigger).not.toBeFocused();
  });
});
