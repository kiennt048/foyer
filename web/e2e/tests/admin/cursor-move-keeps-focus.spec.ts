import { expect, test, type Page } from "@playwright/test";
import { admin, tableReady } from "../helpers/admin.js";
import { focusReport } from "../helpers/overlays.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `cursor-move-keeps-focus` — the first arrow key threw focus out of the list
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Measured on `/sessions` starting with focus on the first row:
 *
 *     focused row     sess-orphaned-no-stop
 *     after Arrow x1  (DIV)                  ← focus is in the detail dock
 *     after Arrow x2  sess-stale-mikrotik    ← back in the list
 *     after Arrow x3  sess-no-accounting
 *
 * **Both halves were deliberate and the combination was not.** `SessionDock` focuses
 * its panel on mount with a reason written beside it — *the first thing a screen
 * reader should read here is the record, not the way out of it* — which is right for
 * an open the operator asked for. `list/detail.ts` is equally explicit that a cursor
 * move is not such a gesture: *"Moving the cursor is not deliberate — it happens on
 * every `J`. Pressing `Enter`, or clicking a row, is."* But `panelIsOpen` is
 * `cursor !== null && !dismissed`, so the first move opened the panel and the panel
 * focused itself regardless of what opened it.
 *
 * Two things this file has to do or it proves nothing:
 *
 * - **Check that the panel is open** at the moment focus is asserted to be on a row.
 *   Focus staying put because nothing opened is the same green as focus staying put
 *   because the open was not deliberate, and only one of them is the fix.
 * - **Watch focus for a while, not once.** The panel focuses itself from an effect,
 *   which runs a task after the keypress. A single read taken the instant the arrow
 *   key resolves is taken *before* the defect would have happened.
 */

/** The row that has focus, or `null` when focus is anywhere else. */
async function focusedRow(page: Page): Promise<string | null> {
  return page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null;
    if (!el || el === document.body) return null;
    /* The row itself, not a control inside one: the cursor is which row has focus,
       and a copy button in a cell is a different place to be standing. */
    return el.matches("tr[data-row-key]") ? el.getAttribute("data-row-key") : null;
  });
}

/**
 * Focus is on a row now, and is still on a row after everything settles.
 *
 * The window is the whole point. `SessionDock`'s focus call is in a `useEffect`; a
 * `toBeFocused()` on the row that happened to resolve first would go green against
 * the console that then moved focus into the panel a millisecond later.
 */
async function staysOnARow(page: Page, ms: number): Promise<string | null> {
  const deadline = Date.now() + ms;
  const first = await focusedRow(page);
  let last = first;
  while (Date.now() < deadline) {
    last = await focusedRow(page);
    if (last === null) return null;
    await page.waitForTimeout(50);
  }
  return last;
}

test.describe("moving the cursor does not move focus off the list", () => {
  test("ArrowDown leaves focus on a row, with the panel open behind it", async ({ page }) => {
    await page.goto("/sessions");
    await tableReady(page);

    const row = page.locator('tbody tr[tabindex="0"]').first();
    await row.focus();
    const start = await focusedRow(page);
    expect(start, "Focus is not on a row to begin with.").not.toBeNull();

    await page.keyboard.press("ArrowDown");

    /* The panel really is open — this is the state in which the defect existed. If
       the dock were closed, focus staying on the row would be saying nothing. */
    await expect(
      page.locator(admin.detailDock),
      "One ArrowDown did not open the detail panel, so this is not the state " +
        "that was measured: `panelIsOpen` is `cursor !== null && !dismissed`, and the cursor " +
        "has just been set. Nothing below can distinguish the fix from a dock that " +
        "no longer opens.",
    ).toBeVisible();

    const after = await staysOnARow(page, 700);
    expect(
      after,
      `Focus left the list on the first ArrowDown — it is on ${await focusReport(page)}. ` +
        "That is the defect this file guards: the panel focuses itself on mount, and a cursor " +
        "move is mounting it. A keyboard operator pressing Down once is moved off the " +
        "list they are traversing, and the second press brings them back.",
    ).not.toBeNull();
  });

  test("a second ArrowDown moves the cursor, and focus goes with it", async ({ page }) => {
    await page.goto("/sessions");
    await tableReady(page);
    await page.locator('tbody tr[tabindex="0"]').first().focus();

    await page.keyboard.press("ArrowDown");
    const first = await staysOnARow(page, 400);
    await page.keyboard.press("ArrowDown");
    const second = await staysOnARow(page, 700);

    /*
     * Both presses, and the first one is the load-bearing assertion here as well as
     * in the test above. With the fix reverted, the first arrow puts focus in the
     * panel and **the second puts it back on a row**, which is the header's own table — so a
     * case that only compared the two row keys would be green against the defect. It is
     * asserted here rather than left to the neighbouring test because a lock that
     * depends on another lock's coverage is one deletion away from proving nothing.
     */
    expect(first, "Focus left the list on the first ArrowDown.").not.toBeNull();
    expect(second, "Focus left the list on the second ArrowDown.").not.toBeNull();
    expect(
      second,
      "The cursor did not move between the two presses, so 'focus stayed on a row' " +
        "is being satisfied by a list that is not moving at all.",
    ).not.toBe(first);
    /* Focus follows the cursor, which is the decision the rest of the list keyboard
       hangs off: the cursor is not a second, invisible notion of "current row". */
    expect(new URL(page.url()).searchParams.get("session")).toBe(second);
  });

  test("Enter moves focus into the dock, and Escape brings it back", async ({ page }) => {
    await page.goto("/sessions");
    await tableReady(page);
    await page.locator('tbody tr[tabindex="0"]').first().focus();

    await page.keyboard.press("ArrowDown");
    const row = await staysOnARow(page, 300);
    expect(row).not.toBeNull();

    await page.keyboard.press("Enter");
    await expect(
      page.locator(admin.detailPanel),
      "Enter did not move focus into the panel. This is the half that was always " +
        "right and it has to stay right: an open the operator asked for should put " +
        "them on the record, and it is the *only* reason the panel focuses itself.",
    ).toBeFocused();

    await page.keyboard.press("Escape");
    await expect(
      page.locator(`tbody tr[data-row-key="${row}"]`),
      "Escape closed the panel and left focus behind. Focus and escape-to-blur are a " +
        "pair — the row the panel was about is where the operator was.",
    ).toBeFocused();
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The defect cannot be re-injected here — the console's own state machine decides
   * whether an open was deliberate, and no attribute on the page can put that back —
   * so what is re-derived instead is that **the measurement can see it**. The panel
   * is focused from the page, which is precisely the DOM state the defect leaves after
   * the first arrow key, and `staysOnARow` is run against it.
   *
   * `/sessions`, focus on the first row:
   *
   *     after ArrowDown                      row sess-live-a3206e1d
   *     with `.detail-panel` focused by hand  null  ← what the header's "(DIV)" line is
   *
   * **The product-side failure**: with `SessionDock`'s effect as `useEffect(…, [])` and
   * no `deliberate` guard, three of the four cases in this file go red:
   *
   *     ArrowDown leaves focus on a row     ✘  "Focus left the list on the first
   *                                            ArrowDown — it is on div.detail-panel"
   *     Enter moves focus into the dock     ✘  focus was never on a row to press from
   *     this case                           ✘  "Focus is not on a row to sabotage."
   *     a second ArrowDown moves the cursor  ✓
   *
   * **The one that survives is the header's own table** — the second press puts focus
   * back on a row — so comparing the two row keys alone is green against the defect,
   * which is why that case asserts the first press as well. The revert is not in this
   * repository; this case is the half that re-derives on every run.
   */
  test("the lock is not vacuous: focus in the dock reads as off the list", async ({ page }) => {
    await page.goto("/sessions");
    await tableReady(page);
    await page.locator('tbody tr[tabindex="0"]').first().focus();
    await page.keyboard.press("ArrowDown");
    expect(await staysOnARow(page, 300), "Focus is not on a row to sabotage.").not.toBeNull();

    await page.locator(admin.detailPanel).focus();

    expect(
      await focusedRow(page),
      "With focus explicitly inside the detail panel, `focusedRow` still reports a " +
        "row. The three tests above are then green whatever the console does with " +
        "focus — a mechanism that only looks like a check.",
    ).toBeNull();
    expect(await focusReport(page)).toContain("detail-panel");
  });
});
