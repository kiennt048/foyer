import { expect, test, type Page } from "@playwright/test";
import { admin, tableReady } from "../helpers/admin.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `row-count-announced` — the rows change under the operator, said once
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `aria-live` appeared twice in the whole console — `Toasts` and the
 * register wizard's verdict — and **filtering rewrote the table with no announcement
 * of any kind**: the visible row count was the only signal, which is not a signal for
 * someone who is not looking at it.
 *
 * The half that is easy to skip is the silence, and it is the half that decides
 * whether the region survives contact with an operator. These lists refetch against a
 * mock on a compressed clock; a region that speaks on every poll is a region an
 * operator switches off, and then it is *worse* than nothing, because the console
 * believes it has one. So: silent when the count is unchanged, silent on arrival, and
 * the *Last updated* line stays out of it — that decision is recorded in `table.tsx`
 * and the design lets it stand.
 *
 * **No assertion here reads the sentence.** What is asserted is that the region names
 * the count — the number, which is a value — so a translation or a rewording moves
 * nothing. `data-role="row-count-live"` is the handle.
 */

const LIVE = '[data-role="row-count-live"]';

async function liveText(page: Page): Promise<string> {
  return (await page.locator(LIVE).textContent()) ?? "";
}

/** Rows the table is actually showing. Skeletons and the empty row are not data. */
function visibleRows(page: Page): Promise<number> {
  return page.locator(admin.rows).count();
}

test.describe("the table says how many rows are shown, and only when it changes", () => {
  test("arriving on a screen announces nothing", async ({ page }) => {
    await page.goto("/accounts");
    await tableReady(page);

    await expect(
      page.locator(LIVE),
      "The table has no live region at all. It is present from the first render and " +
        "empty until there is news, on purpose: a region added to the document at " +
        "the same moment as its text is a region that announces nothing.",
    ).toHaveCount(1);
    expect(
      await liveText(page),
      "The table announced its row count on arrival. Arriving on a screen is not a " +
        "change, and the page announces itself well enough without the table " +
        "shouting a number over the top of it.",
    ).toBe("");
  });

  test("a filter announces the new count, and clearing it announces the old one", async ({
    page,
  }) => {
    await page.goto("/accounts");
    await tableReady(page);
    const all = await visibleRows(page);
    expect(all, "No rows to filter.").toBeGreaterThan(1);

    const search = page.locator(admin.search);

    /* A query that matches nothing. `0` is the most useful count an operator can be
       told and the one they are least likely to notice on screen. */
    await search.fill("zzz-no-such-account");
    await expect
      .poll(() => visibleRows(page), { timeout: 7_000 })
      .toBe(0);
    await expect
      .poll(() => liveText(page), {
        timeout: 5_000,
        message:
          "Filtering to nothing announced nothing. The table emptied under the " +
          "operator and the only signal was visual.",
      })
      .toContain("0");

    /* And back. Filter, clear, filter again produces the same sentence twice — the
       message is wrapped in a keyed element for exactly that reason, because React
       writes no DOM for a string identical to the last one and a live region with no
       mutation announces nothing. */
    await search.fill("");
    await expect
      .poll(() => visibleRows(page), { timeout: 7_000 })
      .toBe(all);
    await expect
      .poll(() => liveText(page), {
        timeout: 5_000,
        message: "Clearing the filter did not announce the restored count.",
      })
      .toContain(String(all));
  });

  /**
   * The silence, and the refetch is *proved* rather than assumed.
   *
   * Same shape as `drawer-focus`'s resize counters: without evidence that a refetch
   * actually happened, "the region stayed silent" and "nothing occurred" are the same
   * green — and the second is what a test that merely waited would be measuring.
   *
   * The trigger is a `visibilitychange` on `window`, which is what react-query's focus
   * manager listens to; the global `staleTime` is 10 s, so the wait before it is not
   * padding, it is what makes the query stale enough to refetch at all. `/accounts`
   * is the target because its rows do not move: `/sessions` polls every fifteen
   * seconds against a mock whose clock is running, and a count that changed for real
   * would be an announcement this test would have read as a defect.
   */
  test("a refetch that changes nothing says nothing", async ({ page }) => {
    let requests = 0;
    page.on("request", (r) => {
      if (r.url().includes("/api/v0/accounts")) requests++;
    });

    await page.goto("/accounts");
    await tableReady(page);
    const rows = await visibleRows(page);

    await page.waitForTimeout(10_500);
    const before = requests;
    await page.evaluate(() => window.dispatchEvent(new Event("visibilitychange")));

    await expect
      .poll(() => requests, {
        timeout: 8_000,
        message:
          "No request to /accounts followed the visibility change, so no refetch " +
          "happened and the silence below proves nothing. **This is the harness, " +
          "not the console** — react-query only refetches a query that has gone " +
          "stale, and `staleTime` is 10 s in `admin/src/main.tsx`. If that number " +
          "moved, so must the wait above.",
      })
      .toBeGreaterThan(before);

    expect(await visibleRows(page), "The refetch changed the row count.").toBe(rows);
    expect(
      await liveText(page),
      "A refetch that changed nothing still announced a count. These lists refetch " +
        "every few seconds; a region that speaks on every poll is one an operator " +
        "turns off, and a console that believes it has a live region it does not is " +
        "worse off than one with none.",
    ).toBe("");
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The failure is *no live region on the screen at all*, and that is
   * re-injectable from the page: the region is taken out of the document and the
   * filter is driven again. React does not put it back — the node is detached while
   * its fiber still points at it — so what remains is exactly that console.
   *
   * **Baseline without the fix:** `/accounts`, filtering to no matches:
   *
   *     as it stands             live region reads "0 rows shown"
   *     the region removed       nothing on the page carries the count; the table
   *                              empties and says nothing
   *
   * The `data-role` handle is asserted to be gone as well as the text, because a
   * check written against `textContent` alone would report the same empty string for
   * "the region is silent" and "the region is not there" — which are the two answers
   * this whole lock exists to tell apart.
   */
  test("the lock is not vacuous: with the region removed, a filter announces nothing", async ({
    page,
  }) => {
    await page.goto("/accounts");
    await tableReady(page);

    await page.locator(LIVE).evaluate((el) => el.remove());
    await expect(page.locator(LIVE)).toHaveCount(0);

    await page.locator(admin.search).fill("zzz-no-such-account");
    await expect.poll(() => visibleRows(page), { timeout: 7_000 }).toBe(0);

    /* The table really did change — this is the state the assertions above call an
       announcement — and there is nothing anywhere on the page saying so. */
    const announced = (await page.locator('[role="status"], [aria-live]').allTextContents())
      .map((s) => s.trim())
      .filter(Boolean);
    expect(
      announced,
      "Something on this screen is still announcing something with the row-count " +
        "region removed. The toast region is on this page too and is *empty* — it is " +
        "present from the first render for the same reason the row count's is — so " +
        "an empty region is not news; text in one is.",
    ).toEqual([]);
  });
});
