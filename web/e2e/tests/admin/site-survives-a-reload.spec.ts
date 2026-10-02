import { expect, test, type APIRequestContext, type Page } from "@playwright/test";
import { admin } from "../helpers/admin.js";
import { MOCK_URL } from "../helpers/env.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `site-survives-a-reload` — which venue an operator is looking at is in the URL
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `SiteProvider` held the venue in `useState("site-hcm")`, so a document load
 * of any kind — reload, Back, a bookmark, a link pasted into a chat — put the operator
 * back on the deployment's first venue. A support call that says *"look at this
 * session"* could not be answered with a URL, and `tests/admin/first-run.spec.ts` had
 * to re-select the venue after every `goto` to avoid registering its device into the
 * café instead of the empty site.
 *
 * The seed was also a **fixture id**, so a migrated, first-booted deployment opened on
 * a venue that does not exist and asked the server about it on every screen.
 *
 * ── THE TRAP THIS FILE IS BUILT AGAINST ─────────────────────────────────────
 *
 * A reload assertion that reads a default-venue screen is
 * **green whether or not the defect exists**, because the venue it returns to and the
 * venue you selected are the same one. So every case here works on a venue that is
 * *not* `listSites()[0]`, and that venue is **derived** rather than named — a spec that
 * says `site-hanoi` goes on passing the day somebody reorders the fixtures, while
 * measuring a world it no longer describes.
 *
 * The assertions are on the **venue's own name in the switcher**, not on the presence
 * of rows: two venues both render a table, and only one of them renders the right one.
 *
 * ── WHAT RED LOOKS LIKE ─────────────────────────────────────────────────────
 *
 * Against a provider that holds the venue in memory — `useState("site-hcm")` with a
 * working in-session setter, the address never consulted — all four go red. The
 * sabotage has to be **faithful**: one whose setter does nothing fails these cases at
 * `selectSite`, one step earlier, and leaves the reload assertion itself unproven.
 *
 * ```
 * 1) is still the venue after a full page load
 *      expect(locator).toHaveText(expected) failed
 *      Received: "Cà phê Hoa Sữa — Nguyễn Huệ, Quận 1"      ← after the reload
 * 2) is what a pasted link resolves to, in a browser that has never seen it
 *      Received: "Cà phê Hoa Sữa — Nguyễn Huệ, Quận 1"
 * 3) survives an in-app navigation that says nothing about it
 *      expect(page).toHaveURL(expected)                           ← no `site=` in the address
 * 4) is named, not guessed, when the address asks for one that is not here
 *      Expected substring: "site-does-not-exist"                  ← it rendered the café's sessions
 * ```
 */

interface Site {
  id: string;
  name: string;
}

async function sites(request: APIRequestContext): Promise<Site[]> {
  const res = await request.get(`${MOCK_URL}/api/v0/sites`);
  if (!res.ok()) throw new Error(`GET /sites → ${res.status()}`);
  return (await res.json()) as Site[];
}

/**
 * The deployment's venue, and one that is not it.
 *
 * `first` is what the console shows when the address says nothing — asked of the mock
 * rather than assumed, because writing it down is exactly how a fix gets that fact wrong.
 */
async function twoVenues(request: APIRequestContext): Promise<{ first: Site; other: Site }> {
  const all = await sites(request);
  const first = all[0];
  const other = all.find((s) => s.id !== first?.id);
  if (!first || !other) {
    throw new Error(
      `This file needs a deployment with at least two venues so that "the venue in the ` +
        `address" can differ from "the venue by default". The mock reported ` +
        `${all.length}. Against a one-venue deployment the property is real and ` +
        `unobservable, which is the whole reason the defect went unnoticed.`,
    );
  }
  return { first, other };
}

/** An address that names this venue, wherever in the query string it sits. */
const namesVenue = (id: string) => new RegExp(`[?&]site=${id}(&|$)`);

/** What the sidebar says the operator is looking at. */
function shownVenue(page: Page) {
  return page.locator(".site-switch-value");
}

/** Drives the header switcher, the way an operator changes venue. */
async function selectSite(page: Page, name: string): Promise<void> {
  await page.locator('.site-switch [aria-haspopup="menu"]').last().click();
  const item = page.locator('[role="menuitemradio"]', { hasText: name });
  await expect(item, `no venue named "${name}" in the switcher`).toHaveCount(1);
  await item.click();
  await expect(shownVenue(page)).toHaveText(name);
}

test.describe("the venue an operator chose", () => {
  test("is still the venue after a full page load", async ({ page, request }) => {
    const { first, other } = await twoVenues(request);

    await page.goto("/sessions");
    await expect(shownVenue(page)).toHaveText(first.name);

    await selectSite(page, other.name);
    await page.reload();

    /*
     * The venue first and the address second, in that order on purpose: under the
     * defect the switch itself worked and only the reload lost it, so the sentence
     * that has to be red is this one. Asserting the URL first would report *the
     * parameter is missing* — true, and one layer away from what an operator sees.
     *
     * Not `expect.poll`, and not a wait: `toHaveText` resolves against whatever the
     * switcher settles on, and the defect settles too — on the wrong venue. A lock
     * that could be satisfied by waiting longer would be measuring time rather than
     * the mechanism (`docs/rules/testing.md`).
     */
    await expect(
      shownVenue(page),
      "after a reload the console is showing a different venue than the operator chose",
    ).toHaveText(other.name);
    await expect(page).toHaveURL(namesVenue(other.id));
  });

  test("is what a pasted link resolves to, in a browser that has never seen it", async ({
    browser,
    request,
  }) => {
    const { first, other } = await twoVenues(request);

    /* A fresh context: no history, no storage, nothing this console could remember
       from. Everything it knows about the venue has to come out of the address, which
       is the property that makes a link worth sending to a colleague. */
    const fresh = await browser.newContext();
    const page = await fresh.newPage();
    try {
      await page.goto(`/sessions?site=${other.id}`);
      await expect(shownVenue(page)).toHaveText(other.name);
      expect(other.name).not.toBe(first.name);
    } finally {
      await fresh.close();
    }
  });

  test("survives an in-app navigation that says nothing about it", async ({ page, request }) => {
    const { other } = await twoVenues(request);

    await page.goto("/");
    await selectSite(page, other.name);

    /*
     * The sidebar's links are `<NavLink to="/sessions">` and carry no venue — none of
     * the console's thirty-one internal link targets do. Every one of them is a
     * chance to move the operator to another venue's data without saying so, which is
     * why the provider holds the answer rather than each link repeating it.
     */
    await page.locator('nav.nav a[href="/sessions"]').first().click();
    await expect(page.locator(admin.pageTitle)).not.toBeEmpty();
    await expect(shownVenue(page)).toHaveText(other.name);
    /* …and the address has been brought back into line, so the link the operator
       copies from here is still about this venue.
       `toHaveURL` and not a single read of `page.url()`: the rewrite is an effect, so
       it lands a render after the switcher's text — a single read goes **red only in
       the full suite** while the isolated run stays green, which is
       `docs/rules/testing.md`'s 3–9 ms boundary in a new place. Bounded on the other side too: nothing but the
       rewrite can put `site=` in this address, so this cannot pass by waiting. */
    await expect(page).toHaveURL(namesVenue(other.id));
  });

  /**
   * A bookmark that outlived the venue it names.
   *
   * The wrong answers are a blank screen and a silent redirect to the first venue, and
   * the second is the dangerous one: an operator acting on another venue's sessions
   * under a URL they trusted. So the console says which id it could not find, and
   * offers the venues it has.
   */
  test("is named, not guessed, when the address asks for one that is not here", async ({
    page,
    request,
  }) => {
    const { first } = await twoVenues(request);
    const missing = "site-does-not-exist";

    await page.goto(`/sessions?site=${missing}`);

    const main = page.locator("main");
    await expect(main).toContainText(missing);
    await expect(
      main.locator(admin.table),
      "a venue that is not in this deployment rendered a table of somebody's sessions",
    ).toHaveCount(0);
    await expect(shownVenue(page)).not.toHaveText(first.name);

    /* And it is a dead end the operator can leave: the venues that do exist are on the
       page, and pressing one goes there. */
    await main.getByRole("button", { name: first.name }).click();
    await expect(shownVenue(page)).toHaveText(first.name);
    await expect(main.locator(admin.table)).toHaveCount(1);
  });
});
