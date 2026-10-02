import { expect, test, type Locator, type Page } from "@playwright/test";
import { tableReady } from "../helpers/admin.js";
import { CONFIRM_SITES, OVERLAYS, openConfirm, openDialog, openOverlay } from "../helpers/overlays.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `shortcuts-do-not-reach-through-a-modal`
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Of the focus and keyboard failures this suite guards, this is the one with the
 * worst consequence for an operator: **without it, with Create batch open and focus
 * on its submit button, `g` `d` navigates to the dashboard and the half-filled form
 * is gone.** It reproduces on four routes.
 *
 * The mechanism is worth keeping written down, because `showModal()` looks like it
 * should prevent it. `showModal()` inerts the document behind the dialog,
 * which stops a *click* reaching the page and does nothing whatsoever to a `keydown`
 * on `window` — and the registry's one listener is on `window`. `registry.tsx`
 * already declined to act while focus was in a text input, so the hole was exactly
 * buttons, links and the `role="group"` dialog bodies: which is to say every dialog
 * in the console at the moment its primary control has focus.
 *
 * **The focus target is asserted before the keys are pressed**, and that assertion is
 * the whole test. Focus landing in a text field would make every case below pass
 * against a console with no suspension at all, because the registry has always
 * declined there — the green would be measuring the one part that was never broken.
 */

/**
 * A control inside the dialog that the registry would happily dispatch through.
 *
 * Buttons first. `ConfirmDialog`'s **type-name tier** renders no Cancel and keeps its
 * danger button really `disabled` until the resource's own name is typed, so on
 * `/accounts` the dialog opens with no enabled button in it at all — `armed` is what
 * that dialog looks like a keystroke before the operator commits, which is the worst
 * possible moment to lose it. Failing both, any explicit tab stop: the measurement
 * named the `role="group"` dialog bodies as part of the same hole.
 */
async function focusSomethingThatIsNotAField(
  page: Page,
  dialog: Locator,
  armed?: string,
): Promise<string> {
  let button = dialog.locator("button:not([disabled])").first();

  if ((await button.count()) === 0 && armed) {
    await dialog.locator('input[type="text"]').first().fill(armed);
    button = dialog.locator("button:not([disabled])").first();
    await expect(
      button,
      "Typing the resource's own name did not enable anything in the type-name " +
        "confirmation, so there is still no control to stand on.",
    ).toHaveCount(1);
  }

  if ((await button.count()) > 0) await button.focus();
  else await dialog.locator('[tabindex="0"]').first().focus();

  const where = await page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null;
    return {
      tag: el?.tagName ?? "none",
      inDialog: Boolean(el?.closest("dialog[open]")),
      typing:
        el instanceof HTMLInputElement ||
        el instanceof HTMLTextAreaElement ||
        el instanceof HTMLSelectElement ||
        Boolean(el?.isContentEditable),
    };
  });

  expect(where.inDialog, "Focus is not inside the open dialog at all.").toBe(true);
  expect(
    where.typing,
    `Focus landed on a ${where.tag}, which is a typing target. The registry has ` +
      "always declined plain keys there, so this case would be green against a " +
      "console with no suspension whatsoever — the hole that was measured is buttons, " +
      "links and the dialog bodies, not fields.",
  ).toBe(false);

  return where.tag.toLowerCase();
}

/** `g` then `d` — the sequence that navigates to the dashboard. */
async function pressGoDashboard(page: Page): Promise<void> {
  await page.keyboard.press("g");
  await page.keyboard.press("d");
  /* The registry's sequences resolve synchronously on the second key; this is only
     the router being given a turn to do the navigation this asserts did not happen. */
  await page.waitForTimeout(300);
}

function path(page: Page): string {
  return new URL(page.url()).pathname;
}

test.describe("navigation shortcuts do not fire through an overlay", () => {
  /**
   * The control case, and it runs first on purpose.
   *
   * Everything below asserts that a URL did *not* change, which is what a broken
   * binding, a renamed sequence and a typo in this file all look like. This is the
   * one case that proves `g` `d` is live at all, driven from the same kind of focus
   * target — a button in `main`, never a field.
   */
  test("`g` `d` navigates when nothing is open", async ({ page }) => {
    await page.goto("/vouchers");
    await tableReady(page);
    await page.locator(".page-actions button").first().focus();

    await pressGoDashboard(page);
    expect(
      path(page),
      "`g` `d` did not navigate with no overlay open, so every assertion in this " +
        "file is passing because the shortcut is dead rather than because it is " +
        "suspended. Check `BINDINGS.goDashboard` before looking at any dialog.",
    ).toBe("/");
  });

  for (const overlay of OVERLAYS) {
    test(`${overlay.id} swallows it`, async ({ page }) => {
      const { dialog } = await openOverlay(page, overlay);
      const target = await focusSomethingThatIsNotAField(page, dialog);
      const before = path(page);

      await pressGoDashboard(page);

      expect(
        path(page),
        `${overlay.id}: \`g\` \`d\` navigated away from ${before} with the overlay ` +
          `open and focus on a <${target}>. Whatever the operator had typed into it ` +
          "is gone, and nothing said so.",
      ).toBe(before);
      await expect(openDialog(page), `${overlay.id}: the overlay closed.`).toHaveCount(1);
    });
  }

  for (const site of CONFIRM_SITES) {
    test(`${site.id} swallows it`, async ({ page }) => {
      const { trigger, dialog } = await openConfirm(page, site);
      /* The row's own primary value — a username on `/accounts` — which is what the
         type-name tier asks to be typed. A value, not a label: this suite reads
         fixtures and never copy. Absent on the two screens whose confirmations are
         the plain tier, which is why it is looked for rather than assumed. */
      const link = trigger.locator("xpath=ancestor::tr[1]").locator(".row-link").first();
      const name = (await link.count()) > 0 ? ((await link.textContent()) ?? "").trim() : undefined;
      const target = await focusSomethingThatIsNotAField(page, dialog, name);
      const before = path(page);

      await pressGoDashboard(page);

      expect(
        path(page),
        `${site.id}: \`g\` \`d\` navigated away from ${before} with a destructive ` +
          `confirmation open and focus on a <${target}>. Without the fix on /garden, ` +
          "the dialog unmounts mid-question.",
      ).toBe(before);
      await expect(openDialog(page)).toHaveCount(1);
    });
  }

  /**
   * The two overlays with no trigger element.
   *
   * They are here because a list that quietly stopped at the dialogs with triggers
   * would leave these two uncovered — a partial coverage that looks complete.
   */
  test("the command palette swallows it", async ({ page }) => {
    await page.goto("/vouchers");
    await tableReady(page);
    await page.keyboard.press("ControlOrMeta+k");
    const palette = page.locator("dialog.palette");
    await expect(palette).toHaveCount(1);

    /* An option rather than the field: the field is a typing target and the registry
       has always declined there. The options are `tabIndex={-1}` by design — this is
       the combobox pattern, where the arrow keys move the selection and focus stays
       in the field — so this is a programmatic focus, which is exactly what a screen
       reader's own navigation would produce. */
    await palette.locator('[role="option"]').first().focus();
    const before = path(page);

    await pressGoDashboard(page);

    expect(path(page), "`g` `d` navigated with the palette open.").toBe(before);
    await expect(palette).toHaveCount(1);
  });

  test("the shortcut sheet swallows it", async ({ page }) => {
    await page.goto("/vouchers");
    await tableReady(page);
    await page.keyboard.press("ControlOrMeta+/");
    const sheet = page.locator("dialog.sheet");
    await expect(sheet).toHaveCount(1);

    await sheet.locator(".dialog-foot button").first().focus();
    const before = path(page);

    await pressGoDashboard(page);

    expect(path(page), "`g` `d` navigated with the shortcut sheet open.").toBe(before);
    await expect(sheet).toHaveCount(1);
  });
});
