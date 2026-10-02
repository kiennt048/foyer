import { expect, test, type Page } from "@playwright/test";
import { tableReady } from "../helpers/admin.js";
import { CONFIRM_SITES, OVERLAYS, openConfirm, openDialog, openOverlay } from "../helpers/overlays.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `dialogs-are-named` — every `dialog[open]` resolves an accessible name
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * A `<dialog>` with no accessible name is announced as "dialog" and nothing else.
 * The ones most at risk are Create batch, Add account, the shortcut sheet, and all
 * three `ConfirmDialog`s — which is to say the two flows with the most fields in the
 * product, the reference sheet for every keyboard shortcut in it, and every question
 * the console asks before destroying something.
 *
 * **`admin/src/screens/screens.test.ts` already pins the call sites in source**, and
 * this is deliberately not that test. It scans for `<Modal>` without `labelledBy` or
 * `label`, which catches the copy — a new screen cloning the nearest dialog — and
 * cannot see whether the id it points at resolves to anything. `labelledBy` naming an
 * element that was renamed, moved into a collapsed branch, or duplicated by `useId`
 * across two mounted dialogs is green in source and silent in a browser. So this
 * asserts the **computed** name, on the running console, with the dialog open.
 *
 * The wizard's shared-secret step is not a separate entry: `RegisterWizard` renders
 * one `<Modal>` and swaps its body, so it is the same element already covered by
 * `access-points · register`.
 */

/** Every overlay this console can put on screen without creating anything. */
async function eachDialog(
  page: Page,
  visit: (id: string) => Promise<void>,
): Promise<string[]> {
  const seen: string[] = [];

  for (const overlay of OVERLAYS) {
    await openOverlay(page, overlay);
    await visit(overlay.id);
    await openDialog(page).press("Escape");
    seen.push(overlay.id);
  }

  for (const site of CONFIRM_SITES) {
    await openConfirm(page, site);
    await visit(site.id);
    await openDialog(page).press("Escape");
    seen.push(site.id);
  }

  /* The two that open from the keyboard and have no trigger element at all. The
     palette is the reason `Modal` takes a `label` as well as a `labelledBy`: its
     first row is a search field, and inventing a heading to hide from sighted
     operators would be worse than naming it in an attribute. */
  await page.goto("/sessions");
  await tableReady(page);
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.locator("dialog.palette")).toHaveCount(1);
  await visit("command palette");
  await page.keyboard.press("Escape");
  seen.push("command palette");

  await page.keyboard.press("ControlOrMeta+/");
  await expect(page.locator("dialog.sheet")).toHaveCount(1);
  await visit("shortcut sheet");
  await page.keyboard.press("Escape");
  seen.push("shortcut sheet");

  return seen;
}

test.describe("every dialog says what it is", () => {
  test("each one resolves a non-empty accessible name", async ({ page }) => {
    const seen = await eachDialog(page, async (id) => {
      await expect(
        openDialog(page),
        `${id}: this dialog has no accessible name, so it is announced as "dialog" ` +
          "and nothing else. Point `labelledBy` at its own `.dialog-title`, or give " +
          "`label` a name where there is no visible heading to point at.",
      ).toHaveAccessibleName(/\S/);
    });

    expect(
      seen.length,
      "No dialog was opened, so the assertion above ran zero times.",
    ).toBe(OVERLAYS.length + CONFIRM_SITES.length + 2);
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * `toHaveAccessibleName(/\S/)` is exactly the kind of assertion that can be
   * satisfied by the wrong thing — a name computed from the dialog's own text
   * content, say — so the defect is put back from the page and the check is watched
   * refusing it.
   *
   * **Baseline without the fix:** `/vouchers` · Create batch:
   *
   *     as it stands                          accessible name "Create voucher batch"
   *     `aria-labelledby` and `aria-label`
   *       removed from the open dialog        no accessible name; the assertion above
   *                                           fails after its 1.5 s retry window
   *
   * The second line is the state of a dialog with no naming attribute. Nothing
   * else in the markup names them, which is the point: the heading is *inside* the dialog and
   * contributes nothing to the element's own name.
   */
  test("the lock is not vacuous: a dialog stripped of its naming attributes has no name", async ({
    page,
  }) => {
    await openOverlay(page, OVERLAYS[0]!);
    const dialog = openDialog(page);
    await expect(dialog).toHaveAccessibleName(/\S/);

    await dialog.evaluate((el) => {
      el.removeAttribute("aria-labelledby");
      el.removeAttribute("aria-label");
    });

    await expect(
      dialog,
      "A dialog with neither `aria-labelledby` nor `aria-label` still resolves a " +
        "name, so the assertion in the test above would pass for the five unnamed " +
        "dialogs that were measured. Find out what is naming it before trusting this file.",
    ).toHaveAccessibleName("");
  });
});
