import { expect, test, type Page } from "@playwright/test";
import { admin, tableReady } from "../helpers/admin.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `row-activation` — the rows Tab reaches and Enter could not use
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Measured:
 *
 *     /sessions        33 rows   row is a tab stop   Enter opens the dock
 *     /vouchers         4 rows   row is a tab stop   Enter does **nothing**
 *     /access-points    7 rows   row is a tab stop   Enter does **nothing**
 *     /accounts, /controllers, /garden, /attempts    rows are not tab stops
 *
 * `onClick` on a `<tr>` does not fire for `Enter` — a row is not a button — so on two
 * screens a row took focus, showed a pointer cursor, and did nothing when a keyboard
 * user pressed the key that means *activate*. WCAG 2.1.1, and **invisible to axe**,
 * because the row is genuinely focusable and genuinely has a name.
 *
 * **Both halves, or the test is satisfied by making everything focusable.** The four
 * screens with no tab stop are not the same defect and must not be "fixed": their
 * rows have no `onRowClick`, so they are not interactive, and their action buttons
 * are ordinary tab stops inside the cells already. Making a non-interactive row
 * focusable would add four tab stops per row to a 21-row table.
 *
 * What "does what a click does" means here is **the click**, driven. Every expectation
 * below is measured from the pointer on the same row rather than typed in, because
 * the three screens do three different things — a dock on one, a navigation on two —
 * and a test that spelled those out would be re-implementing the screens and would
 * stop covering a fourth.
 */

/** The tables whose rows are a tab stop, and therefore must answer Enter and Space. */
const INTERACTIVE = ["/sessions", "/vouchers", "/access-points"];

/** The tables whose rows are not, and must stay that way. */
const NON_INTERACTIVE = ["/accounts", "/controllers", "/garden", "/attempts"];

/** What the console looks like after something happened to a row. */
interface Outcome {
  url: string;
  dock: number;
}

/**
 * The console's state, read **in one pass inside the page**.
 *
 * Not `page.url()` followed by a separate `locator.count()` — two reads at two
 * moments, and the driver's cached frame URL is updated by an event that has its own
 * latency, so the pair can report a URL from before a navigation beside a DOM from
 * after it, or the reverse. Neither half is wrong; the tuple is, and only ever for a
 * few milliseconds, which is the worst way for it to be.
 */
async function outcome(page: Page): Promise<Outcome> {
  return page.evaluate((dockSelector) => {
    const url = new URL(location.href);
    return {
      url: `${url.pathname}${url.search}`,
      dock: document.querySelectorAll(dockSelector).length,
    };
  }, admin.detailDock);
}

/**
 * How long the page must hold still before a reading is taken from it, and how long
 * it is given to hold still at all. The deadline is a backstop, not the mechanism:
 * every call below settles on the quiet window.
 */
const QUIET_MS = 200;
const SETTLE_DEADLINE_MS = 5_000;

/**
 * ── WHY THE BASELINE IS SETTLED AND NOT SIMPLY TAKEN ─────────────────────────
 *
 * `react-router` 8 runs a navigation inside `startTransition`. The history entry is
 * written immediately, so the URL is new at once; the render that mounts the detail
 * dock is concurrent and commits on a later task. Between the two there is a state
 * that is **half of each**, and it was what the pointer baseline recorded.
 *
 * Measured on `/sessions`, 20 clicks, unthrottled, no instrumentation in the page:
 *
 *     immediate read after `click()`   {url: "/sessions?session=…", dock: 0}   20/20
 *     the same read 2.5 s later        {url: "/sessions?session=…", dock: 1}   20/20
 *     url → dock gap                   7 ms min, 20 ms median, 38 ms max
 *
 * So the baseline was **never** the state a click leaves behind — it was a 7-to-38ms
 * transient, every single time. The two assertions below then polled for five
 * seconds towards that transient and passed only when a sample happened to land
 * inside the same narrow window on the keyboard's side. That is the flake, and it is
 * also worse than a flake: while `clicked.dock` was `0`, **neither key was ever
 * required to open the dock at all** — the assertion reduced to the query string.
 *
 * ── AND WHY IT IS NOT "WAIT UNTIL IT DIFFERS FROM IDLE" ──────────────────────
 *
 * The obvious repair is to poll until the outcome stops equalling `idle`. **Do not.**
 * It would fold the `clicked !== idle` assertion below into the wait that produces
 * its own left-hand side, and that assertion is the one holding this file up: without
 * it, a table that ignored every input would satisfy both key assertions by leaving
 * everything at `idle`. A guard that cannot fail is not a guard.
 *
 * So the wait is on the page **holding still**, a predicate written without reference
 * to `idle`, to `clicked`, or to anything either screen is supposed to do:
 *
 *   - every DOM mutation and every change of `location` re-arms the clock;
 *   - the clock must then run `QUIET_MS` with neither happening;
 *   - and the browser must have reached an idle callback *after* the last change,
 *     which is what says React has no transition still queued. This is the part that
 *     cannot be replaced by a longer constant: it scales with how slow the machine
 *     is, and the gap between the URL landing and the commit landing is silent —
 *     no mutation happens during a concurrent render, only when it commits.
 *
 * On a table that ignores every input this settles the moment the quiet window runs,
 * returns `idle`, and the guard below fails exactly as it should. That is asserted,
 * from the page, in `the guard is not vacuous` at the bottom of this file.
 */
async function settledOutcome(page: Page): Promise<Outcome> {
  await page.evaluate(
    async ({ quiet, deadline }) => {
      await new Promise<void>((resolve) => {
        const started = performance.now();
        let lastChange = started;
        let idleAt = -1;
        let href = location.href;
        let pending = 0;

        const armIdle = (): void => {
          if (pending) cancelIdleCallback(pending);
          pending = requestIdleCallback(() => {
            idleAt = performance.now();
          });
        };

        const changed = (): void => {
          lastChange = performance.now();
          armIdle();
        };

        const observer = new MutationObserver(changed);
        observer.observe(document.documentElement, {
          subtree: true,
          childList: true,
          attributes: true,
          characterData: true,
        });
        armIdle();

        const tick = (): void => {
          const now = performance.now();
          if (location.href !== href) {
            href = location.href;
            changed();
          }
          const quietEnough = now - lastChange >= quiet && idleAt >= lastChange;
          if (quietEnough || now - started >= deadline) {
            observer.disconnect();
            if (pending) cancelIdleCallback(pending);
            resolve();
            return;
          }
          setTimeout(tick, 25);
        };
        tick();
      });
    },
    { quiet: QUIET_MS, deadline: SETTLE_DEADLINE_MS },
  );

  return outcome(page);
}

/**
 * Loads a route, puts the cursor's row in a known state, and returns its key.
 *
 * The roving tabindex means exactly one row is reachable with Tab — the cursor if it
 * is on screen, otherwise the first row — so `tr[tabindex="0"]` is *the* row a
 * keyboard user arrives on, and using anything else would test a row the operator
 * cannot reach that way.
 */
async function reachTabStop(page: Page, route: string): Promise<string> {
  await page.goto(route);
  await tableReady(page);
  const row = page.locator('tbody tr[tabindex="0"]').first();
  await expect(
    row,
    `${route}: no row is a tab stop, so there is nothing here to activate. If this ` +
      "table stopped being interactive it belongs in NON_INTERACTIVE, not in a " +
      "weakened assertion.",
  ).toHaveCount(1);
  await row.focus();
  await expect(row).toBeFocused();
  return (await row.getAttribute("data-row-key")) ?? "";
}

test.describe("a row that Tab reaches answers Enter and Space", () => {
  for (const route of INTERACTIVE) {
    test(`${route} — Enter and Space do what a click does`, async ({ page }) => {
      /* The pointer first. This is the expectation for the two keys below, and it is
         *taken* rather than written down — but taken once the page has stopped
         moving, because what a click *leaves behind* is the expectation, not what it
         passes through on the way. See `settledOutcome`. */
      await reachTabStop(page, route);
      const idle = await settledOutcome(page);
      await page.locator('tbody tr[tabindex="0"]').first().click();
      const clicked = await settledOutcome(page);

      expect(
        clicked,
        `${route}: clicking the row changed nothing, so this screen has no row ` +
          "activation to speak of and the two assertions below would pass against a " +
          "table that ignores every input.",
      ).not.toEqual(idle);

      for (const key of ["Enter", " "]) {
        const rowKey = await reachTabStop(page, route);
        await page.keyboard.press(key === " " ? "Space" : key);
        /* The navigation cases replace the document; the dock case does not. Polled
           so both settle without a race written for one of them. */
        await expect
          .poll(() => outcome(page), {
            timeout: 5_000,
            message:
              `${route}: pressing ${key === " " ? "Space" : key} on row ${rowKey} did ` +
              "not do what clicking it does: the row takes focus, shows a pointer " +
              "cursor, and swallows the key that means activate.",
          })
          .toEqual(clicked);
      }
    });
  }

  /**
   * `Space` must not also scroll the page.
   *
   * A list that jumps a screenful every time a row is opened is worse than one that
   * ignores the key, and `preventDefault` is the only thing standing between the two.
   * `/sessions` is where this is measurable — 48 rows against an 900px viewport — and
   * the scrollability is asserted, because on a short page this would be green for
   * the same reason a missing `preventDefault` would.
   */
  test("/sessions — Space activates without scrolling the page", async ({ page }) => {
    await reachTabStop(page, "/sessions");
    const scrollable = await page.evaluate(
      () => document.documentElement.scrollHeight > window.innerHeight + 100,
    );
    expect(
      scrollable,
      "The sessions page does not scroll at this viewport, so `Space` could not have " +
        "scrolled it and this test cannot see the default action it is about.",
    ).toBe(true);

    const before = await page.evaluate(() => window.scrollY);
    await page.keyboard.press("Space");
    await expect(page.locator(admin.detailDock)).toBeVisible();
    expect(
      await page.evaluate(() => window.scrollY),
      "Space opened the record and scrolled the list out from under it.",
    ).toBe(before);
  });

  /**
   * The second half, and the one that makes the first half mean something.
   *
   * Both sets are computed in the same pass so that a selector that stopped matching
   * anything cannot report "no table has focusable rows" as four passes. The
   * assertion is on the *pair*: three interactive tables all have a tab stop, four
   * non-interactive ones have none, and the four still have rows and controls.
   */
  test("no row is a tab stop on the tables whose rows are not interactive", async ({ page }) => {
    const withStops: string[] = [];
    const withoutStops: string[] = [];

    for (const route of [...INTERACTIVE, ...NON_INTERACTIVE]) {
      await page.goto(route);
      await tableReady(page);

      const rows = await page.locator(admin.rows).count();
      expect(rows, `${route}: no rows at all, so nothing here is being measured.`).toBeGreaterThan(
        0,
      );

      const focusable = await page.locator("tbody tr[tabindex]").count();
      (focusable > 0 ? withStops : withoutStops).push(route);
    }

    /*
     * Deliberately **not** asserted: that every non-interactive table still offers
     * some control inside a cell. It was, and `/attempts` measured zero — twenty-one
     * rows of authentication attempts with nothing to press, which is correct. It is
     * a read-only record; the operator reads it with their screen reader's own table
     * navigation, and the "give every row something to do" instinct is the one the
     * design argues against.
     */

    expect(withStops, "A table's rows stopped being reachable with Tab.").toEqual(INTERACTIVE);
    expect(
      withoutStops,
      "A table whose rows are not interactive has been given a tab stop per row. " +
        "Their rows have no `onRowClick`, their action buttons are already tab " +
        "stops, and 21 rows × 4 stops is what this costs on /attempts.",
    ).toEqual(NON_INTERACTIVE);
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The defect is re-injectable from the page, and precisely: the row's `keydown` is
   * swallowed in the capture phase before React's delegated listener can see it,
   * which is exactly the console with `activateRow` removed. The `onClick` on the
   * `<tr>` is untouched — and that is the point, because it is what makes the defect
   * invisible: the row still opens on a click, all the way through.
   *
   * **Baseline without the fix:** `/vouchers`, focus on the first row:
   *
   *     as it stands                  Enter → /vouchers/batch-daily
   *     the row's keydown swallowed   Enter → /vouchers   (nothing at all)
   *
   * The second line is what a keyboard operator gets when a row's keydown swallows
   * the key.
   */
  /**
   * ── THE GUARD, HELD OPEN ─────────────────────────────────────────────────────
   *
   * `clicked !== idle` is what stops the two key assertions above being satisfied by
   * a table that ignores every input. The settle that now produces `clicked` is the
   * one change that could quietly close it: a wait written as *poll until it differs
   * from `idle`* would manufacture the very difference the guard is there to find,
   * and nothing in the suite would ever go red again.
   *
   * So the sabotage here is the pointer's own defect, injected from the page in the
   * same idiom as the keydown case below: the row's `click` is swallowed in the
   * capture phase, which is a `<tr>` with no `onRowClick` — the state of `/accounts`,
   * `/controllers`, `/garden` and `/attempts`, and what `/sessions` would be if its
   * handler were dropped. A settle that waits for a difference cannot return `idle`
   * here; this one must, and quickly, because nothing on the page ever changes.
   *
   * **Both halves are the assertion.** The value, because a wait that throws on
   * timeout fails the equality; and the elapsed time, because a wait that falls back
   * to returning the last reading after burning `SETTLE_DEADLINE_MS` would satisfy
   * the value alone. Terminating on the quiet window is the behaviour under test, so
   * the bound is stated against `QUIET_MS` rather than as a number typed in.
   */
  test("the guard is not vacuous: with the row's click swallowed, the settled outcome is idle", async ({
    page,
  }) => {
    await reachTabStop(page, "/sessions");
    const idle = await settledOutcome(page);

    await page.evaluate(() => {
      document.addEventListener(
        "click",
        (event) => {
          if ((event.target as HTMLElement | null)?.closest("tbody tr")) {
            event.stopPropagation();
          }
        },
        true,
      );
    });

    await page.locator('tbody tr[tabindex="0"]').first().click();

    const startedAt = Date.now();
    const clicked = await settledOutcome(page);
    const elapsed = Date.now() - startedAt;

    expect(
      clicked,
      "With the row's click swallowed the console cannot have moved, so the settle " +
        "invented a difference that was not there. That is what a wait written as " +
        "`poll until it differs from idle` does, and it would leave the " +
        "`clicked !== idle` guard above unable to fail against any table at all.",
    ).toEqual(idle);

    expect(
      elapsed,
      `The settle took ${elapsed}ms on a page that never changed, so it ran to its ` +
        `${SETTLE_DEADLINE_MS}ms deadline instead of terminating on the ${QUIET_MS}ms ` +
        "quiet window. A settle that waits out its deadline is waiting for something, " +
        "and the only thing left to wait for here is the difference the guard owns.",
    ).toBeLessThan(SETTLE_DEADLINE_MS / 2);
  });

  test("the lock is not vacuous: with the row's keydown swallowed, Enter does nothing", async ({
    page,
  }) => {
    await reachTabStop(page, "/vouchers");
    const idle = await outcome(page);

    await page.evaluate(() => {
      document.addEventListener(
        "keydown",
        (event) => {
          const target = event.target as HTMLElement | null;
          if ((event.key === "Enter" || event.key === " ") && target?.tagName === "TR") {
            event.stopPropagation();
          }
        },
        true,
      );
    });

    await page.locator('tbody tr[tabindex="0"]').first().focus();
    await page.keyboard.press("Enter");
    await page.waitForTimeout(600);

    expect(
      await outcome(page),
      "Enter still activated the row with its keydown stopped before React could " +
        "see it, so something other than `activateRow` is answering the key and the " +
        "assertions above are not measuring what they claim to.",
    ).toEqual(idle);
  });
});
