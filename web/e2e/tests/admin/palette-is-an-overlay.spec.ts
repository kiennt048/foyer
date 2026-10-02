import { expect, test, type Page } from "@playwright/test";
import { tableReady } from "../helpers/admin.js";
import { focusReport } from "../helpers/overlays.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `palette-is-an-overlay` — the command palette is a modal dialog
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * A hand-written `div.palette-backdrop` with a scrim painted on it has four
 * consequences, all **driven** rather than deduced:
 *
 *   - `Tab` from the filter walks all 21 commands and then straight out into the
 *     sidebar *behind* the scrim;
 *   - **one `Shift+Tab` leaves it entirely**;
 *   - a control in `main` can still be focused programmatically while it is open;
 *   - `Escape` answers **only while the input has focus**, because the handler is
 *     the input's own `onKeyDown` and `useKeyboardSuspend(true)` silences the
 *     registry for plain keys. `⌘K` is the only exit from anywhere else.
 *
 * All four are one missing mechanism, so the answer is one thing: the palette goes
 * through `Modal` like the console's nine other dialogs, and the trap, the inert document and
 * the return of focus become the platform's.
 *
 * **Two harness facts this file is written around:**
 *
 * - Every modal `<dialog>` tab cycle in Chromium includes exactly one press landing
 *   on `<body>` before it re-enters. That is the platform, not a defect, so nothing
 *   here counts presses — each press is checked to be *inside the dialog or `<body>`*
 *   and never on a named element outside it.
 * - The palette is a combobox: its options are `tabIndex={-1}` and the arrow keys
 *   move `aria-activedescendant` while focus stays in the field. So "Tab walks 21
 *   commands" is not merely wrong, it is the wrong shape — there is exactly one tab
 *   stop inside, and that is the design.
 */

const PALETTE = "dialog.palette";

/** Opens the palette from the keyboard, which is the only way it opens. */
async function openPalette(page: Page): Promise<void> {
  await page.goto("/sessions");
  await tableReady(page);
  await page.keyboard.press("ControlOrMeta+k");
  await expect(page.locator(PALETTE), "⌘K opened no palette.").toHaveCount(1);
  expect(
    await page.locator(PALETTE).evaluate((el) => (el as HTMLDialogElement).matches(":modal")),
    "The palette is on screen but not modal. `<dialog open>` gives up the trap, the " +
      "inert document and the backdrop while looking identical — which is the state " +
      "the hand-written `div.palette-backdrop` was in, one element name later.",
  ).toBe(true);
}

/** Where a press landed: inside the palette, on `<body>`, or somewhere it must not be. */
async function tabLanding(page: Page): Promise<{ ok: boolean; where: string }> {
  const where = await focusReport(page);
  const inside = await page.evaluate(
    (sel) => Boolean(document.activeElement?.closest(sel)),
    PALETTE,
  );
  return { ok: inside || where === "<body>", where };
}

test.describe("the command palette is a real overlay", () => {
  test("Tab never leaves it", async ({ page }) => {
    await openPalette(page);

    /* The stops the *product* has, not a number: one press more than there are
       focusable things inside, so at least one wrap must happen. */
    const stops = await page
      .locator(`${PALETTE} input, ${PALETTE} button:not([tabindex="-1"])`)
      .count();

    for (let i = 0; i <= stops + 2; i++) {
      await page.keyboard.press("Tab");
      const { ok, where } = await tabLanding(page);
      expect(
        ok,
        `Tab #${i + 1} left the palette — focus is on ${where}. Before the focus fix this ` +
          "walked out into the sidebar *behind the scrim*, where every control was " +
          "still operable and none of it was visible.",
      ).toBe(true);
    }

    /* And it came back, rather than parking on `<body>` for good. */
    await expect(page.locator(`${PALETTE} input`)).toBeFocused();
  });

  test("Shift+Tab from the first stop does not escape either", async ({ page }) => {
    await openPalette(page);
    /* The field is where `autoFocus` leaves it, and it is the first stop. One
       `Shift+Tab` from here is the press a non-modal palette would leak. */
    await expect(page.locator(`${PALETTE} input`)).toBeFocused();

    for (let i = 0; i < 4; i++) {
      await page.keyboard.press("Shift+Tab");
      const { ok, where } = await tabLanding(page);
      expect(
        ok,
        `Shift+Tab #${i + 1} left the palette — focus is on ${where}. Backwards is ` +
          "the half a hand-written trap forgets, and here it was the whole failure: " +
          "one press was enough.",
      ).toBe(true);
    }
  });

  test("Escape closes it from a command, not only from the input", async ({ page }) => {
    await openPalette(page);

    const option = page.locator(`${PALETTE} [role="option"]`).first();
    await expect(
      option,
      "The palette rendered no commands, so there is nothing to stand on and this " +
        "test would be asserting Escape from the field — the one case that always " +
        "worked.",
    ).toHaveCount(1);
    await option.focus();

    /* The precondition *is* the test. A handler on the search field's own `onKeyDown`
       would make Escape pressed anywhere else do nothing at all, leaving ⌘K the only
       way out; a case that pressed Escape with focus still in the field would be green
       against exactly that palette. */
    expect(
      await page.evaluate(() => document.activeElement?.getAttribute("role")),
      "Focus is not on a command, so this is not the case that was measured.",
    ).toBe("option");

    await page.keyboard.press("Escape");
    await expect(
      page.locator(PALETTE),
      "Escape from a command left the palette open. Before the focus fix the handler was " +
        "the input's own and the registry was suspended for plain keys, so this was " +
        "a trap with one exit and it was a chord.",
    ).toHaveCount(0);
  });

  test("a control in main cannot be focused while it is open", async ({ page }) => {
    await page.goto("/sessions");
    await tableReady(page);

    /* The control case first: the same call, on the same element, with nothing open.
       Without it, "the focus was refused" and "the selector matched nothing" are the
       same green. */
    const closed = await focusFirstControlInMain(page);
    expect(closed, "No control in `main` could be focused with nothing open.").toBe("focused");

    await page.keyboard.press("ControlOrMeta+k");
    await expect(page.locator(PALETTE)).toHaveCount(1);

    const open = await focusFirstControlInMain(page);
    expect(
      open,
      "A control in `main` took focus with the palette open, so the document behind " +
        "it is not inert and the overlay is decoration. This is also what silently " +
        "broke the palette's own two focus-moving commands: `.focus()` on an inert " +
        "element is refused without an error, which is why `App.tsx` holds the " +
        "command in a ref and runs it after the palette has gone.",
    ).toBe("refused");
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The four cases above are worth exactly as much as this one, and the defect is
   * re-injectable from the page: the palette's own element is closed and re-opened
   * the **non-modal** way — `<dialog open>`, which is what a hand-written backdrop
   * `div` amounts to. Same element, same styling, same scrim, none of the mechanism.
   *
   * **Baseline without the fix:** `/sessions`, palette open:
   *
   *     as it stands                  Tab: input → <body> → input;  main control refused
   *     re-opened as `<dialog open>`  Tab: input → main's own controls;  main control focused
   *
   * So both assertions above can go red, and neither is passing because Chromium
   * happens to keep focus where it started.
   */
  test("the lock is not vacuous: re-opened non-modal, the palette traps nothing", async ({
    page,
  }) => {
    await openPalette(page);

    await page.locator(PALETTE).evaluate((el) => {
      const dialog = el as HTMLDialogElement;
      dialog.close();
      dialog.setAttribute("open", "");
    });
    expect(
      await page.locator(PALETTE).evaluate((el) => (el as HTMLDialogElement).matches(":modal")),
      "The palette is still modal after being re-opened the non-modal way, so this " +
        "sabotage changed nothing and proves nothing.",
    ).toBe(false);

    expect(
      await focusFirstControlInMain(page),
      "A `main` control is still refused focus behind a non-modal dialog, which " +
        "would mean the inertness assertion above is passing for some other reason.",
    ).toBe("focused");

    await page.locator(`${PALETTE} input`).focus();
    let escaped = false;
    for (let i = 0; i < 4 && !escaped; i++) {
      await page.keyboard.press("Tab");
      escaped = !(await tabLanding(page)).ok;
    }
    expect(
      escaped,
      "Four presses of Tab from inside a **non-modal** dialog never left it, so the " +
        "trap assertions above would be green with no trap at all. Find out what is " +
        "holding focus before trusting this file.",
    ).toBe(true);
  });
});

/**
 * Tries to move focus to the first action control in `main`, and reports what
 * happened.
 *
 * Programmatic rather than a click, because that is the thing an inert document
 * refuses **silently** — no error, no exception, focus simply does not move.
 */
async function focusFirstControlInMain(page: Page): Promise<string> {
  return page.evaluate(() => {
    const el = document.querySelector<HTMLElement>("main button, main a[href]");
    if (!el) return "no control in main";
    el.focus();
    return document.activeElement === el ? "focused" : "refused";
  });
}
