import { expect, test, type Page } from "@playwright/test";
import { ADMINISTRATOR, shellSettled, signInAs, signOut } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  The row-count region on the two administrator lists — **text**, not presence
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The assertion anybody writes first here is `expect(page.locator("[role=status]"))
 * .toHaveCount(1)`, and it is **already green on both routes with nothing driven at
 * all**. `Table` renders its region on every screen in the console, present and empty:
 *
 * ```tsx
 * <p className="visually-hidden" role="status" aria-live="polite" data-role="row-count-live">
 *   {announcement ? <span key={announcement.seq}>{announcement.text}</span> : null}
 * </p>
 * ```
 *
 * Present-and-empty is correct — a live region added to the document at the same
 * moment as its text announces nothing in most screen readers — and it is exactly what
 * makes the presence assertion worthless. A console can carry one on every route and
 * be silent everywhere, which is the refused-route defect `role-gating.spec.ts` locks.
 *
 * So both halves are driven, on both routes:
 *
 *  - **it speaks** — after a filter changes the count, the region names the new one;
 *  - **it is silent** — after a refetch that changes nothing, it says nothing. A
 *    region that announces on every poll is a region an operator turns off, and these
 *    lists refetch on window focus with a 10 s `staleTime`.
 *
 * `tests/admin/row-count-announced.spec.ts` holds the same pair for `/accounts` and
 * `/sessions`. This file is not a duplicate of it: `/operators` and `/audit` were built
 * after it, are administrator-only, and are in no other spec — and the second of them
 * filters through a `<select>` rather than a search box, which is a different code path
 * into the same region.
 */

const LIVE = '[data-role="row-count-live"]';
const ROWS = "tbody tr:not(.empty-row)";

const liveText = async (page: Page) => ((await page.locator(LIVE).textContent()) ?? "").trim();
const rowCount = (page: Page) => page.locator(ROWS).count();

interface ListRoute {
  path: string;
  /** Narrows the list from inside the page, exactly as an operator would. */
  narrow: (page: Page) => Promise<void>;
}

const ROUTES: ListRoute[] = [
  {
    path: "/operators",
    /* The search box. `dang` matches one of the five fixture operators; the assertion
       below reads the resulting count off the page rather than expecting a number. */
    narrow: async (page) => page.locator(".toolbar .search input").fill("dang"),
  },
  {
    path: "/audit",
    /* A `<select>`, which is the whole reason this route is here as well as
       `/operators`: it is a different control reaching the same region. The value is
       taken from the dropdown rather than named, so a fixture change cannot make this
       silently select nothing. */
    narrow: async (page) => {
      const select = page.locator(".toolbar .field select").first();
      const options = await select
        .locator("option")
        .evaluateAll((els) => els.map((el) => (el as HTMLOptionElement).value).filter(Boolean));
      expect(
        options.length,
        "The audit actor filter offers no actor, so nothing can be narrowed.",
      ).toBeGreaterThan(0);
      await select.selectOption(options[0]!);
    },
  },
];

test.afterEach(async ({ context }) => {
  await signOut(context);
});

for (const route of ROUTES) {
  test.describe(`${route.path} · the row-count region`, () => {
    test("names the new count after a filter", async ({ page, context }) => {
      await signInAs(context, ADMINISTRATOR);
      await page.goto(route.path);
      await shellSettled(page);
      await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

      /* At rest it exists and is empty — which is the state the presence assertion
         this file exists to replace would have called a pass. */
      await expect(page.locator(LIVE)).toHaveCount(1);
      expect(
        await liveText(page),
        "The region already carried text before anything was driven, so it announces " +
          "on arrival — which is a region that speaks when nothing has happened.",
      ).toBe("");

      const before = await rowCount(page);
      await route.narrow(page);
      await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

      await expect
        .poll(() => rowCount(page), {
          timeout: 7_000,
          message: `The filter did not change the row count on ${route.path}, so the ` +
            "silence or noise below says nothing. This is the harness, not the console.",
        })
        .not.toBe(before);
      const after = await rowCount(page);

      /* The number is read off the page, never typed: a fixture change moves both
         sides together, and a lock with `"1 row shown"` in it would be measuring the
         fixtures rather than the console. */
      await expect
        .poll(() => liveText(page), {
          timeout: 7_000,
          message:
            `Filtering ${route.path} from ${before} rows to ${after} announced ` +
            "nothing. A table that empties itself in silence is a table a " +
            "screen-reader operator is still waiting for.",
        })
        .toContain(String(after));
    });

    /**
     * The silence — and the refetch is **proved** rather than assumed.
     *
     * Without evidence that a refetch actually happened, *"the region stayed silent"*
     * and *"nothing occurred"* are the same green, and the second is what a test that
     * merely waited would be measuring. The trigger is a `visibilitychange`, which is
     * what react-query's focus manager listens to; the wait before it is not padding,
     * it is what makes the query stale enough to refetch at all (`staleTime: 10_000`
     * in `admin/src/main.tsx`).
     *
     * Both of these lists are static under the mock's clock — operators and audit
     * entries are not created by the simulation — so a count that changed for real
     * would itself be a finding rather than a flake.
     */
    test("says nothing after a refetch that changes nothing", async ({ page, context }) => {
      await signInAs(context, ADMINISTRATOR);

      const apiPath = `/api/v0${route.path}`;
      let requests = 0;
      page.on("request", (r) => {
        if (new URL(r.url()).pathname === apiPath) requests++;
      });

      await page.goto(route.path);
      await shellSettled(page);
      await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();
      const rows = await rowCount(page);

      await page.waitForTimeout(10_500);
      const before = requests;
      await page.evaluate(() => window.dispatchEvent(new Event("visibilitychange")));

      await expect
        .poll(() => requests, {
          timeout: 8_000,
          message:
            `No request to ${apiPath} followed the visibility change, so no refetch ` +
            "happened and the silence below proves nothing. **This is the harness, " +
            "not the console** — react-query only refetches a query that has gone " +
            "stale, and `staleTime` is 10 s in `admin/src/main.tsx`. If that number " +
            "moved, so must the wait above.",
        })
        .toBeGreaterThan(before);

      expect(await rowCount(page), "The refetch changed the row count.").toBe(rows);
      expect(
        await liveText(page),
        `A refetch that changed nothing announced a count on ${route.path}. These ` +
          "lists refetch on window focus; a region that speaks on every poll is one " +
          "an operator turns off, and a console that believes it has a live region " +
          "it does not is worse off than one with none.",
      ).toBe("");
    });

    /**
     * ── THE EVIDENCE ────────────────────────────────────────────────────────────
     *
     * Re-injected from the page: the region is removed from the document and the same
     * filter is driven again. React does not put it back — the node is detached while
     * its fiber still points at it — so what remains is the state before the live region existed, a
     * table that empties itself and says nothing.
     *
     * The `data-role` handle is asserted gone as well as the text, because a check
     * written against `textContent` alone reports the same empty string for *"the
     * region is silent"* and *"the region is not there"* — the two answers this whole
     * file exists to tell apart.
     */
    test("the lock is not vacuous: with the region removed, the same filter announces nothing", async ({
      page,
      context,
    }) => {
      await signInAs(context, ADMINISTRATOR);
      await page.goto(route.path);
      await shellSettled(page);
      await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

      await page.locator(LIVE).evaluate((el) => el.remove());
      await expect(
        page.locator(LIVE),
        "The region could not be removed, so this sabotage proves nothing.",
      ).toHaveCount(0);

      const before = await rowCount(page);
      await route.narrow(page);
      await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();
      await expect.poll(() => rowCount(page), { timeout: 7_000 }).not.toBe(before);

      await expect(
        page.locator(LIVE),
        "React put the region back, so the sabotage did not hold for the length of " +
          "the drive and the two tests above cannot be said to have been watched " +
          "failing.",
      ).toHaveCount(0);
    });
  });
}
