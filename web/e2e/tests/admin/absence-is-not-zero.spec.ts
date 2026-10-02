import { expect, test, type Page } from "@playwright/test";
import { admin } from "../helpers/admin.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `absence-is-not-zero` — what the console says when it cannot ask
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The product's own thesis, applied to the console's own backend. It has now failed
 * three times in the same shape and each one looked like good news on screen:
 *
 *  - the dashboard drew a green tick reading *"Nothing needs action"* when the alarm
 *    request had not been answered (fixed, and its four-branch render is the pattern
 *    the two below follow);
 *  - with every request aborted, `/garden`'s urgent *"Nothing is enforcing a
 *    walled garden in this site"* **disappeared** while the permanent caution stayed,
 *    so a failing `/garden` looked *calmer* than a healthy one, and `+ Allow a
 *    destination` stayed fully enabled and full-colour over a table reading *Could
 *    not load this data*;
 *  - **the paused query** — React Query's `isLoading` is `isPending && isFetching`,
 *    so an offline query renders as *not loading, no error, no rows*, and every list
 *    screen answered that with its never-had-data state: *"No sessions."*, *"No
 *    accounts."* with an **Add account** button under it. Eleven screens.
 *
 * The last one has two guards and they are independent on purpose. `Table` does not
 * conclude *empty* from silence — `ui/table.test.tsx` owns that truth table, four
 * cases, of which a `Table` inferring emptiness fails exactly the first. And
 * `main.tsx` sets `networkMode: "always"` so the console never *declines to ask*: the default gates
 * every request on the browser's opinion about the internet, and this console only
 * ever calls back to the origin that served it. Either change removes the sentence;
 * both together mean neither can bring it back.
 *
 * This file is the end-to-end half — the rule holding in a running console rather
 * than in a render.
 */

/** Aborts every API call, which is how a backend that is not there behaves. */
async function withBackendDown(page: Page): Promise<void> {
  await page.route("**/api/v0/**", (route) => route.abort("failed"));
}

interface GardenState {
  urgent: number;
  levels: string[];
  titles: string[];
  addDisabled: boolean;
  rows: number;
}

async function gardenState(page: Page): Promise<GardenState> {
  return page.evaluate(() => {
    const notices = [...document.querySelectorAll<HTMLElement>(".notice")];
    const add = [...document.querySelectorAll<HTMLButtonElement>(".page-actions button")][0];
    return {
      urgent: notices.filter((n) => n.dataset.level === "urgent").length,
      levels: notices.map((n) => n.dataset.level ?? ""),
      titles: notices.map((n) => n.querySelector(".notice-title")?.textContent?.trim() ?? ""),
      addDisabled: Boolean(
        add && (add.disabled || add.getAttribute("aria-disabled") === "true"),
      ),
      rows: document.querySelectorAll("tbody tr:not(.empty-row):not(.row-detail)").length,
    };
  });
}

test.describe("a failing screen does not look calmer than a working one", () => {
  test("/garden states the verdict it cannot reach, and stops offering the write", async ({
    page,
  }) => {
    /* The healthy render first, in the same run, so the comparison is between two
       measurements rather than between a measurement and a memory. */
    await page.goto("/garden");
    await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
    const healthy = await gardenState(page);

    expect(healthy.rows, "no garden entries loaded, so there is nothing to compare").
      toBeGreaterThan(0);
    expect(
      healthy.addDisabled,
      "the add control is already unavailable with a working backend, so the " +
        "assertion below cannot tell the states apart",
    ).toBe(false);

    await withBackendDown(page);
    await page.goto("/garden");
    await expect(page.locator(".empty-row").first()).toBeVisible();
    const down = await gardenState(page);

    expect(
      down.urgent,
      "With the backend down /garden renders no urgent notice at all. Every verdict " +
        "on that screen reads `garden?.effectiveKind === …`, which collapses to the " +
        `reassuring branch when nothing answered.\n  healthy: ${JSON.stringify(healthy.titles)}` +
        `\n  down:    ${JSON.stringify(down.titles)}`,
    ).toBeGreaterThan(0);

    expect(
      down.urgent,
      `A failing /garden is quieter than a healthy one: ${down.urgent} urgent notices ` +
        `against ${healthy.urgent}. That is the finding, not a threshold.`,
    ).toBeGreaterThanOrEqual(healthy.urgent);

    /* The permanent structural caution needs no data and is still true, so it must
       still be there — "everything vanished" would satisfy the count above. */
    expect(
      down.levels.filter((l) => l === "caution-minor").length,
      "the pre-sign-in quota caution is a permanent fact and disappeared with the data",
    ).toBeGreaterThan(0);

    expect(
      down.addDisabled,
      "`+ Allow a destination` is still live over a table that could not load. An " +
        "operator can open the dialog and submit against a backend that is not " +
        "answering.",
    ).toBe(true);

    /* And it is disabled in the way that keeps focus reachable — `aria-disabled`
       plus a guarded handler — so pressing it does nothing rather than opening a
       dialog over a screen that cannot save anything. */
    await page.locator(".page-actions button").first().dispatchEvent("click");
    await expect(page.locator("dialog[open]")).toHaveCount(0);
  });
});

test.describe("an unanswered list is not an empty list", () => {
  /**
   * The daily screens, reached by clicking the drawer rather than by `goto`.
   *
   * A document navigation fails outright when the browser is offline, so an in-app
   * navigation to a screen whose code is already there and whose data is not is the
   * only way to put a *list query* into this state in a real browser.
   *
   * "Already there" needs care: `/sessions` and `/vouchers` are
   * lazy routes, fetched by `App.tsx`'s `preloadDaily` as soon as the shell has
   * painted. So the test waits for the network to settle before it cuts it — which is
   * also the property this proves for them: once the console has loaded, a dropped
   * connection does not cost an operator the daily screens.
   *
   * ── WHAT OFFLINE MEANS HERE, AND WHY IT CHANGED ────────────────────────────
   *
   * `setOffline` cuts the transport, so the console asks and the request fails. That
   * is the honest outcome and it is a *second* fix's doing: `main.tsx` sets
   * `networkMode: "always"`, because the default gates every request on the browser's
   * opinion about the internet and this console only ever calls back to the origin
   * that served it. Before that, these four screens sent **no request at all** and
   * the query sat at `fetchStatus: "paused"` for ever.
   *
   * So the assertion is the rule rather than one of its renderings: whichever of the
   * two honest states the console is in — *still asking* or *asked and failed* — it
   * must never be the third one, `Empty`, which claims the collection is empty. The
   * paused half of that is pinned by the truth table in `admin/src/ui/table.test.tsx`,
   * where it can be produced exactly.
   */
  const DAILY = ["/sessions", "/access-points", "/vouchers", "/accounts"];

  for (const route of DAILY) {
    test(`${route} offline never says "nothing here"`, async ({ page }) => {
      await page.goto("/");
      await expect(page.locator(".card-grid .card .metric-foot").first()).toBeVisible();
      await page.waitForLoadState("networkidle");

      await page.context().setOffline(true);
      await page.locator(`nav.nav a[href="${route}"]`).click();
      await expect(page).toHaveURL(new RegExp(`${route}$`));

      /* Long enough for the request to fail, be retried once, and settle. */
      await page.waitForTimeout(2500);

      const state = await page.evaluate(() => {
        const row = document.querySelector(".empty-row");
        return {
          /* `Empty` is the never-had-data component. Its presence is the defect —
             the error state uses the same `.empty-row` container and is correct. */
          claimsEmpty: Boolean(document.querySelector(".empty-row .empty")),
          text: row?.textContent?.trim().slice(0, 120) ?? null,
          busy: document.querySelector("tbody")?.getAttribute("aria-busy"),
          skeletons: document.querySelectorAll("tbody .skeleton").length,
          says: Boolean(row),
        };
      });

      expect(
        state.claimsEmpty,
        `Offline, this screen renders its never-had-data state: "${state.text}". The ` +
          "request either failed or was never sent, and the console is reporting an " +
          "empty collection — on two of these screens with a button inviting a write.",
      ).toBe(false);

      /* And it is not silent either: still asking, or saying it could not. */
      expect(
        state.skeletons > 0 || state.says,
        "Offline, this screen shows neither a pending request nor a failure — an " +
          "empty table with nothing said over it.",
      ).toBe(true);

      /* The discriminator: whatever it showed is a state it comes *out* of. The same
         navigation online must reach real rows, or "never render Empty" would pass
         everything above. */
      await page.context().setOffline(false);
      await page.reload();
      await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false", {
        timeout: 15_000,
      });
      await expect(page.locator(admin.rows).first()).toBeVisible();
    });
  }
});
