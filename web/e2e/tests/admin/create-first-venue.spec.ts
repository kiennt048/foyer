import { expect, test, type Page } from "@playwright/test";
import { admin } from "../helpers/admin.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  A stranger installs it, and the first thing they must do is name the venue
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The contract explains why `createSite` has to exist — *a migrated database holds
 * zero sites and nothing else in the product could put the first row in* — and the
 * console is what calls it. The release bar is *a stranger installs it and a phone
 * gets online*; a first run that requires `curl` fails it before the stopwatch is
 * interesting.
 *
 * `SiteGate` renders a page that says *"No venue yet"*, and that page must offer
 * something to do about it. Telling an operator clearly that they are stuck is a
 * worse place to stop than confusion.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *  WHY THE VENUE LIST IS INTERCEPTED, AND WHAT THAT DOES AND DOES NOT PROVE
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **This suite's mock serves the ordinary fixture world, which has three venues.**
 * `SiteGate` therefore resolves `ready` on every route in every other spec here, the
 * no-venue branch never renders, and the control this file exists for is never on
 * screen. A world that cannot express a property makes every check of that property
 * vacuous, and that is not hypothetical: a world whose rows all live in one site
 * hides a dropped site scope from the schema-validating mock, the whole end-to-end
 * suite and the differential harness alike.
 *
 * The world that can express it is `FOYER_MOCK_FRESH=1`, and it cannot be reached
 * from here: `playwright.config.ts` starts **one** mock, shared by every project, and
 * `env` only reaches a mock this config started. Emptying the real world instead
 * would take the three venues away from twenty-five other spec files.
 *
 * So the venue list is answered `[]` **for this browser only** — the technique
 * `first-run.spec.ts` already uses for its own sabotage, and for the same reason. It
 * is honest about which half it proves:
 *
 *  - **What it proves.** The console's half: that
 *    a browser shown a deployment with no venue is offered a control, that the
 *    control sends `POST /sites` carrying the name the operator typed, and that when
 *    that call succeeds the console stops asking and moves on to the next step.
 *  - **What it does not.** That the server creates the row. That is `web/mock`'s
 *    `first-run.test.ts` — which runs under `FOYER_MOCK_FRESH=1` and is the only
 *    world in this repository where the `201` can happen — and `internal/apistore`'s
 *    tests against real PostgreSQL.
 *
 * **The third case is the exception and is worth the asymmetry**: it lets the `POST`
 * reach the real mock, which refuses it `409 /problems/one-site-per-install` because
 * the fixture world genuinely has three venues. That refusal is real, and this is the
 * one place the console renders that type in front of a browser.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *  RUNNING IT, AND WHAT RED LOOKS LIKE
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `reuseExistingServer` is on outside CI and the dev ports are hardcoded at
 * 5170/5171/5173, so a run attaches to whichever checkout already owns those ports.
 * *A green run is somebody else's tree until proven otherwise.* `FOYER_MOCK_ADMIN_PASSWORD`
 * must match the value in `helpers/env.ts`, or the `admin-auth` project refuses every
 * sign-in — which is the failure that variable exists to produce.
 *
 * Under the sabotage the vitest file records — `<CreateVenue />` removed from
 * `SiteGate`'s `none` branch — all three cases fail on the control's absence rather
 * than on a bare timeout with no assertion behind it:
 *
 * ```
 * ✘ offers a control that creates one
 * ✘ naming it creates it, and the console moves on to the next step
 * ✘ a venue created elsewhere is refused, and the refusal has a way out
 *     Error: expect(locator).toBeVisible() failed
 *     Expected: visible / element(s) not found
 * ```
 *
 * The console half is additionally driven in vitest by `web/admin/src/screens/create-venue.test.tsx`,
 * through `SiteGate` and with a genuinely empty venue list; what this file adds is
 * what a server render cannot see — the request going out and the gate opening
 * afterwards.
 */

/* ── selectors: structure and `data-*`, never copy ─────────────────────────── */

const venue = {
  form: "[data-create-venue]",
  name: "#venue-name",
  submit: "[data-create-venue-submit]",
  next: "[data-create-venue-next]",
  continue: "[data-create-venue-continue]",
} as const;

/** The one sentence read from the catalogue, mirrored from `i18n/core.ts`. */
const COPY = {
  /** `site.gate.noneTitle` — the state that must be on screen for any of this to mean anything. */
  noVenue: "No venue yet",
} as const;

/**
 * A browser that sees a deployment with no venue.
 *
 * Returns the venues the console will be shown, so a caller can make the list change
 * underneath it — which is what the success case needs and is the whole of how the
 * gate is observed to open.
 */
async function withNoVenue(page: Page): Promise<{ setVenues: (v: unknown[]) => void }> {
  let venues: unknown[] = [];
  await page.route("**/api/v0/sites", async (route) => {
    /* Reads only. The `POST` is a different case per test and is routed separately;
       letting this handler answer it would make every case below a test of Playwright. */
    if (route.request().method() !== "GET") return route.fallback();
    await route.fulfill({
      status: 200,
      contentType: "application/json",
      body: JSON.stringify(venues),
    });
  });
  return {
    setVenues: (v) => {
      venues = v;
    },
  };
}

test.describe("a deployment with no venue", () => {
  test("offers a control that creates one", async ({ page }) => {
    await withNoVenue(page);
    await page.goto("/");

    /*
     * The state first, and it is not ceremony. A `resolving` gate — the venue list
     * still in flight — also has no screen on it, so an assertion that only looked
     * for the absence of the dashboard would pass while the console was merely
     * still asking.
     */
    await expect(page.getByText(COPY.noVenue).first()).toBeVisible();
    await expect(page.locator(venue.form)).toBeVisible();
    await expect(page.locator(venue.name)).toBeVisible();
    await expect(page.locator(venue.submit)).toBeVisible();

    /* The field is named, not merely present. An input a screen reader announces as
       "edit text" is the same defect `dialogs-are-named` was opened for. */
    await expect(page.locator(venue.name)).toHaveAccessibleName(/.+/);

    /* The two first-run surfaces read as one flow, and this sentence is what says so
       before the operator commits to the first step. */
    await expect(page.locator(venue.next)).toBeVisible();
  });

  /**
   * The guarantee, end to end and from the console alone: **a world with no venue,
   * and an operator who ends up with one.**
   *
   * The `POST` is fulfilled here rather than sent on, because the real mock's world
   * has three venues and would refuse it — see the header. What is asserted about the
   * server is only what this browser can honestly know: that the request went out,
   * with the name that was typed, to the operation the contract defines.
   */
  test("naming it creates it, and the console moves on to the next step", async ({ page }) => {
    const world = await withNoVenue(page);
    const NAME = `Quán cà phê ${Date.now()}`;
    const created = { id: "site-created", name: NAME, environment: "live" };

    let posted: unknown = null;
    await page.route("**/api/v0/sites", async (route) => {
      if (route.request().method() !== "POST") return route.fallback();
      posted = route.request().postDataJSON();
      /* The venue exists from this moment, so the next `GET` answers with it — which
         is what makes the gate open on its own rather than on a navigation. */
      world.setVenues([created]);
      await route.fulfill({
        status: 201,
        contentType: "application/json",
        body: JSON.stringify(created),
      });
    });

    await page.goto("/");
    await expect(page.locator(venue.form)).toBeVisible();

    await page.locator(venue.name).fill(NAME);
    await page.locator(venue.submit).click();

    /*
     * The gate opens because the fact it was gating on stopped being true — no
     * navigation, no success page. The dashboard is behind it, and the *other*
     * first-run surface is already asking for the next thing.
     */
    await expect(page.locator(venue.form)).toHaveCount(0);
    await expect(page.locator(admin.cards).first()).toBeVisible();
    await expect(page.locator("section[data-first-run]")).toBeVisible();
    await expect(page.locator('section[data-first-run] [data-step="device"]')).toBeVisible();

    /* Asserted after the console has settled, so this cannot pass on a request that
       was never made: `posted` is null until the route handler runs. */
    expect(posted, "the console did not POST /sites with the name that was typed").toEqual({
      name: NAME,
    });
  });

  /**
   * The refusal, driven against the real server.
   *
   * Reachable without pressing twice: the venue list is fetched, the form renders, and
   * *then* somebody else creates the venue — a second tab, another operator,
   * `deploy/first-boot.sh`, a `curl`. Here the fixture world's three venues stand in
   * for "somebody got there first", and the mock's `409` is genuine.
   *
   * **And it must not be a second dead end.** The refusal means a venue now exists, so
   * the notice carries a control that re-asks for the list. A form that can only ever
   * be refused, on the page that exists to give the console a way forward, would be
   * the same dead end one layer along.
   */
  test("a venue created elsewhere is refused, and the refusal has a way out", async ({ page }) => {
    /* Only the `GET` is intercepted. The `POST` goes to the mock, whose world has
       three venues, so `createSite` refuses it exactly as a shipped deployment would
       refuse a second one. */
    await withNoVenue(page);
    await page.goto("/");
    await expect(page.locator(venue.form)).toBeVisible();

    await page.locator(venue.name).fill("A second venue");
    await page.locator(venue.submit).click();

    /* `role="alert"`, mounted with its text — `ErrorNotice`'s own contract. */
    await expect(page.getByRole("alert").first()).toBeVisible();
    await expect(page.locator(venue.continue)).toBeVisible();
  });
});
