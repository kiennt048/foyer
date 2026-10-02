import { expect, test } from "@playwright/test";
import { admin, tableReady } from "../helpers/admin.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `offline-does-not-invent-an-answer` — a refusal to ask is not a finding
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The failure this guards: with React Query's online manager reporting offline,
 * `/operators` can render
 *
 *     No operators.
 *     Nobody can sign in to this console.
 *
 * — `aria-busy="false"`, one `.empty-row`, no error anywhere on the page — for a
 * request that was never sent. Two independent faults produce it, and this file pins
 * both because either one alone puts the sentence back:
 *
 * 1. **The console declining to make the call.** `networkMode` defaults to
 *    `"online"`, which gates every request on the browser's `online`/`offline`
 *    events. The admin API is `/api/v0` — the origin that served the document — so
 *    that gate asks about the internet before calling back to the machine the page
 *    just came from. A gated request does not fail, it *pauses*: pending forever,
 *    `error` null, nothing retried and nothing reported.
 * 2. **`Table` inferring emptiness from silence.** An `isEmpty` of
 *    `!isLoading && !error && rows.length === 0` is wrong, because `isLoading` is the
 *    *conjunction* `pending && fetching`, so a paused query satisfies all three.
 *
 * **The injection is a synthetic `offline` event, not `context.setOffline`.** The
 * online manager tracks the two window events and never reads `navigator.onLine`,
 * so dispatching the event puts it in exactly the state described above while
 * leaving the transport alone — which is the point. The question this suite has to
 * answer is whether the console's *own belief* about connectivity can change what it
 * tells an operator about their deployment, and only an unchanged transport can
 * separate that from a real outage. The second test then takes the transport away
 * for real, so the pair distinguishes "did not ask" from "asked and failed".
 *
 * Not covered here, deliberately: the *not loaded* rendering itself. With (1) fixed it
 * has no live producer, so it is a guard rather than a path, and
 * `admin/src/ui/table.test.tsx` proves it directly rather than this file proving it
 * vacuously.
 *
 * ── WHAT HOLDS EACH ─────────────────────────────────────────────────────────
 *
 * `Table` carries no test ids, so the two constants below are structural and say what
 * they stand for. (2) is held by `ui/table.tsx`, (1) by `main.tsx`'s
 * `networkMode: "always"` — so this file is a regression lock rather than a
 * reproduction.
 *
 * It does not duplicate `absence-is-not-zero`. That file cuts the transport on four
 * daily screens and asserts the rule across them. This one is the *other* injection:
 * the console's own belief, transport untouched — the half that says whether a
 * request was **sent** — plus a lazily-loaded route, the toolbar count, and the
 * blind-spot panel, none of which that file reaches.
 */

const EMPTY_STATE = `${admin.tbody} .empty-title`;

/**
 * Still asking: `Table` renders skeleton rows under `aria-busy="true"` until a query
 * has answered once.
 *
 * `tableReady` is the load-bearing half of this — it refuses to return until
 * `aria-busy` is `"false"`, and that is the assertion that goes red when the console
 * declines to ask (`14 × <tbody aria-busy="true">`). The count below states the same
 * rule where a reader will look for it.
 */
const UNANSWERED = `${admin.tbody} .skeleton`;

/**
 * Asked and failed: the error row.
 *
 * `.empty-row` is shared by all three terminal states, so the discriminator is
 * `Table`'s own alert icon — a bare `<div>` wrapper. `Empty` puts its icon inside
 * `.empty`, which `:not(.empty)` excludes, and the zero-results row has no icon at
 * all. Verified against the live rendering rather than read off the source.
 */
const FAILED = `${admin.tbody} .empty-row > td > div:not(.empty) > svg`;

/** Flips React Query's online manager without touching the wire. */
async function believeOffline(page: import("@playwright/test").Page): Promise<void> {
  await page.evaluate(() => window.dispatchEvent(new Event("offline")));
}

test.describe("a console that thinks it is offline still asks its own origin", () => {
  test("a screen opened while offline shows its rows, not an empty deployment", async ({
    page,
  }) => {
    await page.goto("/sessions");
    await tableReady(page);

    /*
     * Count what actually leaves the page.
     *
     * Without this the case fails by *timeout* — `tableReady` waiting out a query
     * that will never answer — and a timeout with no assertion behind it is the one
     * red this suite cannot tell from a loaded machine. The fault is precisely "no
     * request was sent", so it is asserted as that, by name and with a number.
     */
    const sent: string[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/api/v0/")) sent.push(r.url());
    });

    await believeOffline(page);

    /* A client-side navigation to a query key that has never been fetched — the
       shape that produced the sentence, because a key with a cache entry would have
       had rows to fall back on and hidden the fault. */
    await page.getByRole("link", { name: /operators/i }).first().click();
    await expect(page).toHaveURL(/\/operators/);

    /* Waited for rather than sampled: every screen here is a `lazy()` chunk, so the
       URL changes before the module has loaded and long before the query fires.
       Sampling at this point would be measuring the bundler. The wait is swallowed so
       that a request which never comes arrives as the named assertion below rather
       than as a timeout. */
    await page.waitForRequest((r) => r.url().includes("/api/v0/"), { timeout: 10_000 }).catch(
      () => undefined,
    );

    expect(
      sent.length,
      "The console sent no request to its own origin after being told the browser is " +
        "offline. `networkMode` is gating `/api/v0` — the origin that served this " +
        "document — on the browser's opinion about the internet, so the query is " +
        "paused rather than failed: pending for ever, no error, nothing retried.",
    ).toBeGreaterThan(0);

    const rows = await tableReady(page);
    await expect(
      page.locator(EMPTY_STATE),
      'The table claimed the collection is empty. Before this test, offline `/operators` ' +
        'said "No operators. Nobody can sign in to this console." for a request that was ' +
        "never sent.",
    ).toHaveCount(0);
    await expect(page.locator(UNANSWERED)).toHaveCount(0);
    await expect(rows.first()).toBeVisible();
  });

  test("the row count is a count, not a zero the operator would act on", async ({ page }) => {
    await page.goto("/accounts");
    await tableReady(page);

    await believeOffline(page);

    await page.getByRole("link", { name: /sessions/i }).first().click();
    await expect(page).toHaveURL(/\/sessions/);
    await tableReady(page);

    /*
     * The toolbar count carried the identical residual — `!isLoading && !isError`
     * fell through to `t("table.rows", 0)` — and it is worse than the table saying
     * it, because a number is what an operator reads at a glance and "0 rows" is
     * indistinguishable from a quiet night.
     */
    await expect(page.locator(".toolbar-count")).not.toHaveText(/^0 rows$/);
  });

  test("a real outage says the load failed, and still claims nothing about the data", async ({
    page,
    context,
  }) => {
    await page.goto("/operators");
    await tableReady(page);

    /*
     * This time the wire really is gone, so the request is made and fails. The two
     * tests differ only in that, and the difference is the whole distinction between
     * "we did not ask" and "we asked and it failed".
     *
     * The refetch is forced by typing rather than by navigating, and that is a
     * property of this console rather than a convenience: every screen is a
     * `lazy()` chunk, so a route the operator has not already visited cannot even be
     * reached with the transport down — navigating to `/operators` while offline gets
     * no `<table>` at all, and would measure module loading rather than the fetch
     * state under it. Filtering changes the query key
     * on a screen that is already mounted, which is the smallest thing that puts a
     * fresh query in front of a dead network.
     */
    await context.setOffline(true);
    await page.locator(`${admin.search}`).fill("nguyen");

    await expect(page.locator(FAILED)).toBeVisible();
    await expect(
      page.locator(EMPTY_STATE),
      "An outage rendered as a fact about the deployment.",
    ).toHaveCount(0);
    /* Nor the other confident answer: these filters excluding everything is also a
       claim, and it needs the same evidence the empty state does. */
    await expect(page.getByText(/No rows match these filters/)).toHaveCount(0);

    await context.setOffline(false);
  });
  /*
   * `/attempts` carries the same residual outside the table, and it is the screen
   * where it matters most.
   *
   * The blind-spot panel is promoted above an empty table precisely because "no
   * attempts" is the one conclusion this data cannot support — it exists to say what
   * was *never recorded*. A guard of `!isLoading && !isError` covers a failed query
   * (which has no `blindSpots` either, and would render a heading, a lede and an
   * empty list) but is one case short: a request that was never sent is neither
   * loading nor an error, so the panel whose whole job is naming what nobody looked
   * at would render an empty list of things nobody looked at.
   */
  test("the blind-spot panel does not interpret a table it never loaded", async ({
    page,
    context,
  }) => {
    await page.goto("/attempts");
    await tableReady(page);

    await context.setOffline(true);
    await page.locator(admin.search).fill("a4:83:e7:2b:19:04");

    await expect(page.locator(FAILED)).toBeVisible();
    await expect(
      page.locator("section.blind-spots"),
      "The panel that explains an empty attempts table rendered over a table that " +
        "was never fetched, which is the one reading it exists to prevent.",
    ).toHaveCount(0);

    await context.setOffline(false);
  });

});
