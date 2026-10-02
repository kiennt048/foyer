import { expect, test, type Locator, type Page } from "@playwright/test";
import { tableReady } from "../helpers/admin.js";
import { expectNasRemoved, mockTick } from "../helpers/mock.js";

/**
 * The overlays: the shortcut sheet, and every dialog that creates something.
 *
 * Both halves of this file exist because the failure mode is *invisible*. An overlay
 * that is on screen, styled correctly, and visually indistinguishable from a working
 * one can be missing every behaviour that makes it an overlay — and a test that only
 * asserts it is visible passes throughout.
 */

/**
 * Asserts the top dialog is a real modal, not merely on screen.
 *
 * `<dialog open>` compiles, renders, and looks exactly right. It is also the
 * **non-modal** form: no focus trap, no `inert` on the rest of the document, no
 * answer to Escape, and no `::backdrop` — so the scrim in the stylesheet styles
 * nothing and an operator tabbing through the form walks straight out of it into the
 * table behind, with the form still on screen and still apparently focused.
 *
 * `:modal` is the mechanical difference and the only honest thing to assert: it
 * matches exactly when the element was opened with `showModal()`. Checking for a
 * visible `<dialog>` would have passed against all four broken ones.
 */
async function expectModal(dialog: Locator, what: string): Promise<void> {
  await expect(dialog, `${what}: the dialog is not on screen at all.`).toBeVisible();
  const isModal = await dialog.evaluate((el) => el.matches(":modal"));
  expect(
    isModal,
    `${what}: the dialog is open but not modal. \`<dialog open>\` gives up focus ` +
      "trapping, Escape and the backdrop while looking identical — use showModal().",
  ).toBe(true);
}

/** The one dialog currently mounted. Every creation flow renders at most one. */
function openDialog(page: Page): Locator {
  return page.locator("dialog").first();
}

test.describe("overlays are really overlays", () => {
  /**
   * The shortcut sheet closes on Escape.
   *
   * There is a lock for the session detail dock and there was none for the sheet,
   * which is precisely why the sheet regressed unnoticed: an `<input type="search">`
   * inside it carried the user agent's own meaning for Escape and consumed the press
   * before it could become a close request, so the reference for every keyboard
   * shortcut in the console could not be dismissed with the keyboard.
   */
  test("Mod+/ opens the shortcut sheet and Escape closes it", async ({ page }) => {
    await page.goto("/sessions");
    await tableReady(page);

    const sheet = page.locator("dialog.sheet");
    await expect(sheet, "The sheet is on screen before anything opened it.").toHaveCount(0);

    /* `ControlOrMeta` so this asserts the binding the console declares, on whichever
       platform the run happens to be on, rather than a Mac-only or Linux-only half. */
    await page.keyboard.press("ControlOrMeta+/");
    await expectModal(sheet, "shortcut sheet");

    /* Its own search field is focused on open — which is the condition under which
       Escape can be swallowed. Pressing Escape from anywhere else would not catch it. */
    await expect(page.locator("dialog.sheet .sheet-search")).toBeFocused();

    await page.keyboard.press("Escape");
    await expect(
      sheet,
      "The shortcut sheet survived Escape. It is the reference for every keyboard " +
        "shortcut in the console, so it is the last overlay that may need a mouse.",
    ).toHaveCount(0);
  });

  /**
   * A field with a value stages the press: clear first, close second.
   *
   * The pair matters — an Escape that closes the sheet out from under a half-typed
   * query loses the query, and an Escape that only ever clears traps the operator.
   */
  test("Escape in the sheet's search clears the query before it closes the sheet", async ({
    page,
  }) => {
    await page.goto("/sessions");
    await tableReady(page);
    await page.keyboard.press("ControlOrMeta+/");

    const sheet = page.locator("dialog.sheet");
    const search = page.locator("dialog.sheet .sheet-search");
    await expectModal(sheet, "shortcut sheet");

    await search.fill("esc");
    await page.keyboard.press("Escape");
    await expect(search, "The first Escape did not clear the query.").toHaveValue("");
    await expect(sheet, "The first Escape closed the sheet instead of clearing.").toBeVisible();

    await page.keyboard.press("Escape");
    await expect(sheet, "The second Escape did not close the sheet.").toHaveCount(0);
  });

  /* ── the four creation dialogs ────────────────────────────────────────────── */

  test("the create-voucher-batch dialog is modal", async ({ page }) => {
    await page.goto("/vouchers");
    await tableReady(page);
    await page.locator(".page-actions button").first().click();
    await expectModal(openDialog(page), "create voucher batch");
  });

  test("the create-account dialog is modal", async ({ page }) => {
    await page.goto("/accounts");
    await tableReady(page);
    await page.locator(".page-actions button").first().click();
    await expectModal(openDialog(page), "create account");
  });

  /**
   * Registering an access point is two steps and **both** are dialogs.
   *
   * The second is the one that matters most: it renders the RADIUS shared secret,
   * once, and no API returns it again. A non-modal overlay there means the operator
   * can tab past the only copy of a credential that cannot be recovered — and it is
   * also the step where an accidental dismissal leaves a registered device with a
   * secret nobody has.
   *
   * The device is deleted afterwards. The mock is stateful and shared across the
   * suite; a test that leaves a NAS behind changes what every later test counts.
   */
  test("both steps of registering an access point are modal", async ({ page, context }) => {
    await page.goto("/access-points");
    await tableReady(page);

    await page.locator(".page-actions button").first().click();
    const dialog = openDialog(page);
    await expectModal(dialog, "register access point · details");

    const name = `e2e modal ${Date.now()}`;
    const tick = await mockTick(context.request);
    await dialog.locator("#nas-name").fill(name);
    await dialog.locator("#nas-ip").fill("10.90.0.254");
    await dialog.locator('button[type="submit"]').click();

    /* Step two: the secret, shown once. Located by the secret block rather than by
       copy, and asserted as modal in its own right — the two steps are two separate
       `<dialog>` elements and only one of them was ever checked by eye. */
    await expect(page.locator("dialog .secret")).toBeVisible();
    await expectModal(openDialog(page), "register access point · shared secret");

    /* Leave the world as we found it, and prove it. The accessibility sweep also
       registers devices, so the cleanup is one function both callers share rather
       than two that agree today — including its one exemption, which is a mock that
       restarted mid-run and took the device with it (`helpers/mock.ts`). */
    await expectNasRemoved(context.request, name, tick);
  });
});
