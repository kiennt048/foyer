import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { MOCK_URL } from "../helpers/env.js";

/**
 * The first-run panel at phone width.
 *
 * `responsive-shell` pins nine routes at 390px and **cannot pin this one**: it walks
 * `NAV` on the default site, where the panel does not exist because that site is
 * configured. So without this file the one rendering that is only ever seen on a
 * brand-new deployment — the first screen a stranger who has just run
 * `deploy/first-boot.sh` looks at — is outside every width assertion in the suite.
 *
 * **The failure it guards is easy to make.** The device step's action slot holds two
 * buttons, and with a `.notice` whose action does not wrap, at 390px the two of them
 * take the width: the body wraps to **one word per line for eleven lines** — 53px of
 * prose inside a 358px notice.
 *
 * `document.scrollWidth` stays **390 the whole time**. The shell absorbs the squeeze
 * exactly as it is supposed to, so `responsive-shell`'s rule would be green against
 * this even if it reached the route. That is the reason there are two
 * measurements below rather than one: a page that does not scroll sideways and a page
 * that is readable are different claims, and an assertion can be
 * aimed somewhere the defect does not appear.
 */

interface Site {
  id: string;
  name: string;
}

async function json<T>(request: APIRequestContext, path: string): Promise<T> {
  const res = await request.get(`${MOCK_URL}${path}`);
  if (!res.ok()) throw new Error(`GET ${path} → ${res.status()} ${await res.text()}`);
  return (await res.json()) as T;
}

/** The site with nothing in it — derived, never named. See `tests/admin/first-run.spec.ts`. */
async function emptySite(request: APIRequestContext): Promise<Site> {
  const sites = await json<Site[]>(request, "/api/v0/sites");
  for (const s of sites) {
    const nas = await json<{ items: unknown[] }>(request, `/api/v0/nas?siteId=${s.id}`);
    const batches = await json<{ items: unknown[] }>(
      request,
      `/api/v0/voucher-batches?siteId=${s.id}`,
    );
    if (nas.items.length === 0 && batches.items.length === 0) return s;
  }
  throw new Error(
    "No site in the mock is empty, so the first-run panel cannot be reached. Either the " +
      "fixtures changed or a spec left a device or a batch behind.",
  );
}

/** At 390 the switcher is in the header rather than the sidebar; the control is the same. */
async function selectSite(page: Page, name: string): Promise<void> {
  await page.locator('.site-switch [aria-haspopup="menu"]').last().click();
  await page.locator('[role="menuitemradio"]', { hasText: name }).click();
  await expect(page.locator(".site-switch-value")).toHaveText(name);
}

test("the first-run panel fits a 390px viewport", async ({ page, request }) => {
  const site = await emptySite(request);
  await page.goto("/");
  await selectSite(page, site.name);

  const panel = page.locator("section[data-first-run]");
  await expect(panel).toBeVisible();

  const m = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
    innerWidth: window.innerWidth,
  }));
  expect(m.innerWidth, "this project's 390px viewport did not apply").toBe(390);
  expect(
    m.scrollWidth,
    `the dashboard is ${m.scrollWidth}px wide against a ${m.clientWidth}px viewport with ` +
      "the first-run panel on it — horizontal scroll on the first screen of a new " +
      "deployment. `responsive-shell` measures the same thing on nine other routes and " +
      "the cause is usually one declaration in the `@media (max-width: 900px)` block.",
  ).toBe(m.clientWidth);

  /*
   * ── And the prose has room, which the measurement above cannot see ─────────
   *
   * A `.notice-body` squeezed to 53px still fits the document; it just becomes a
   * column of single words. So this is a second measurement, not a stricter version
   * of the first.
   *
   * **The bound sits in a gap that was measured rather than at a number somebody
   * liked** — the same discipline `primitives.test.tsx` uses for `VERDICT_MAX`. On the
   * device step, at 390px, in a 358px notice:
   *
   *   two controls in the action slot   53px    the defect, driven
   *   one control                      175px    what ships
   *
   * A third of the notice is 119px: above everything the collapse produces and below
   * everything the intended layout produces, with room on both sides for a shell that
   * moves a little. Expressed against the notice rather than the viewport so it stays
   * true if the page gutters change, and both edges of the gap are asserted below —
   * a bound that only checks one edge is satisfied by moving it to the other.
   */
  const COLLAPSED = 53;
  const SHIPS = 175;
  const share = (notice: number) => notice / 3;
  expect(share(358), "the bound has drifted onto the collapse it exists to catch").toBeGreaterThan(
    COLLAPSED,
  );
  expect(share(358), "the bound has drifted onto the layout that ships").toBeLessThan(SHIPS);

  const bodies = panel.locator(".notice-body");
  const count = await bodies.count();
  expect(count, "the panel rendered no prose at all").toBeGreaterThan(0);

  for (let i = 0; i < count; i++) {
    const measured = await bodies.nth(i).evaluate((el) => ({
      body: el.getBoundingClientRect().width,
      notice: el.closest(".notice")!.getBoundingClientRect().width,
      text: (el.textContent ?? "").slice(0, 48),
    }));
    expect(
      measured.body,
      `"${measured.text}…" has ${Math.round(measured.body)}px inside a ` +
        `${Math.round(measured.notice)}px notice. Something in the row beside it is ` +
        "taking the width — `.notice` is a flex row whose action slot does not wrap, " +
        "so a second control in it collapses the sentence to one word per line.",
    ).toBeGreaterThan(share(measured.notice));
  }
});
