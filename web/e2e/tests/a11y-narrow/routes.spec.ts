import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { admin } from "../helpers/admin.js";
import { ADMIN_URL } from "../helpers/env.js";
import { ADMINISTRATOR, STAFF, THROWAWAY_PASSWORD, signInAs, signOut } from "../helpers/session.js";

/*
 * The page builder's preview is the guest portal rendered into a `sandbox=""` frame,
 * and a frame with no script permission is one axe cannot inject into: it waits on
 * it until the test times out (`frame.evaluate` on a script-less frame never
 * settles). The markup inside is the portal's own, which this sweep already scans
 * on its own routes, in both languages and both schemes — so the frame's container
 * is excluded here, which is what stops axe descending into it.
 */
const axeFor = (page: Page) =>
  new AxeBuilder({ page }).withTags(TAGS).exclude(".preview-stage");

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  The accessibility sweep at 390 × 844 — the viewport nobody had ever scanned
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `tests/a11y/routes.spec.ts` walks every route in both applications, in both colour
 * schemes, at 1440 × 900. The admin has a second layout — a header
 * with a drawer, a column set two thirds smaller, and a per-row disclosure that
 * renders a definition list inside a table body. **None of that markup exists at the
 * width the sweep runs at**, so none of it had ever been in front of axe.
 *
 * ── ONE COLOUR SCHEME, ON PURPOSE ───────────────────────────────────────────
 *
 * Dark only. **Do not "fix" the asymmetry with the desktop sweep by adding a light
 * project here.** The two schemes exist there because contrast failures are
 * theme-specific by construction and a suite that scanned one theme would certify
 * half the product — and that sweep already runs both, over these same routes, at
 * desk width. What is new at 390px is *layout*: which elements exist, how they nest,
 * what has a name, what is a landmark, what can be reached. None of those answers
 * change with the palette, so a second scheme here would double the runtime of the
 * slowest project in the suite to re-measure something already measured.
 *
 * ── WHAT IT ADDS BEYOND THE NINE ROUTES ─────────────────────────────────────
 *
 * Two states with no URL, for the reason the desktop sweep's `after` hook exists:
 * once the gate fails the build on any violation anywhere, **the surfaces the sweep
 * cannot reach are the only place a violation can survive.** A `serious`
 * `scrollable-region-focusable` on the registration wizard's findings list, for
 * instance, is invisible to a sweep that only walks URLs.
 *
 *  - **the drawer, open** — a hand-written modal. It is not a `<dialog>`, so
 *    `role="dialog"`, `aria-modal`, its accessible name and its focus order are all
 *    the console's own work rather than the platform's;
 *  - **a row with its disclosure expanded** — a `<dl>` inside a `<tbody>`, which is
 *    the exact shape in which a `<p>` straight inside a definition list is a
 *    `serious` violation.
 *
 * All eleven states are expected at zero violations.
 */

const TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];

interface NarrowRoute {
  url: string;
  /** On screen before scanning, so axe never races a skeleton. */
  ready: string;
  /** When the URL alone does not say which state this is. */
  name?: string;
  /** Drives the surface into its state, after `goto` and before `ready`. */
  after?: (page: Page) => Promise<void>;
  /**
   * Arranges what the *server* will answer with, **before** the navigation.
   *
   * `after` cannot express this and the difference is not cosmetic: it runs once the
   * document is already loaded, so it can only reach states a click produces. A
   * session is decided by the request, which is why the two `403` entries below and
   * the signed-in `/password` need this hook and could not be written with the other.
   * Same split as the desktop sweep's.
   */
  setup?: (page: Page) => Promise<void>;
}

const ROUTES: NarrowRoute[] = [
  { url: `${ADMIN_URL}/`, ready: ".card-grid .card .metric-foot" },
  { url: `${ADMIN_URL}/sessions`, ready: "tbody[aria-busy=false]" },
  { url: `${ADMIN_URL}/controllers`, ready: "tbody[aria-busy=false]" },
  { url: `${ADMIN_URL}/access-points`, ready: "tbody[aria-busy=false]" },
  { url: `${ADMIN_URL}/vouchers`, ready: "tbody[aria-busy=false]" },
  { url: `${ADMIN_URL}/accounts`, ready: "tbody[aria-busy=false]" },
  { url: `${ADMIN_URL}/portal`, ready: ".block-list" },
  { url: `${ADMIN_URL}/portal?tab=terms`, name: "/portal (terms tab)", ready: "textarea#terms-en" },
  { url: `${ADMIN_URL}/attempts`, ready: "tbody[aria-busy=false]" },
  { url: `${ADMIN_URL}/garden`, ready: "tbody[aria-busy=false]" },
  {
    url: `${ADMIN_URL}/sessions`,
    name: "/sessions (drawer open)",
    ready: '.sidebar-panel[data-open="true"]',
    after: async (page) => {
      await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
      await page.locator(".menu-button").click();
    },
  },
  /*
   * ── the four authentication routes ──────────────────────────────────────────
   *
   * At 390px they are a second layout rather than a smaller one, and two of them are
   * tables that drop most of their columns into a per-row disclosure — the shape in
   * which a `<p>` straight inside a `<dl>` is a `serious` violation.
   *
   * **`/sign-in` is the genuinely new case here.** It renders *outside* `Shell`: no
   * grid, no sidebar, no drawer, no `.page`. Every other entry in this file is the
   * same shell holding different content; this one is a different document.
   *
   * The `403` renderings are swept as well, and they are the entries this sweep can
   * least be trusted on: axe reports them clean, and it cannot see that a refusal
   * may be announced to nobody (`tests/admin-auth/role-gating.spec.ts` checks that). Kept anyway — a clean result on a state nobody
   * has scanned is worth less than a clean result on one that has been.
   */
  { url: `${ADMIN_URL}/sign-in`, ready: ".signin form button[type=submit]" },
  {
    url: `${ADMIN_URL}/sign-in`,
    name: "/sign-in (credentials refused)",
    ready: ".signin [role=alert]",
    after: async (page) => {
      /* A disabled fixture operator — the same `401` an unknown username gets. The
         password is the harness pin, so this refusal comes from `enabled: false` and
         from nothing else, which is the branch this entry is here to render. */
      await page.locator("#signin-username").fill("pham.thu.ha");
      await page.locator("#signin-password").fill(THROWAWAY_PASSWORD);
      await page.locator(".signin form button[type=submit]").click();
    },
  },
  { url: `${ADMIN_URL}/operators`, ready: "tbody[aria-busy=false]" },
  {
    url: `${ADMIN_URL}/operators`,
    name: "/operators (staff session · refused)",
    ready: "tbody tr.empty-row",
    setup: async (page) => void (await signInAs(page.context(), STAFF)),
  },
  { url: `${ADMIN_URL}/audit`, ready: "tbody[aria-busy=false]" },
  {
    url: `${ADMIN_URL}/audit`,
    name: "/audit (row disclosure expanded)",
    /* A second disclosure entry beside `/sessions`, because this table's dropped
       column renders an `Absent` component rather than a value — a different node
       inside the same `<dl>`, which is precisely where the session dock's violation
       was. */
    ready: "tr.row-detail .row-detail-list dt",
    after: async (page) => {
      await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
      await page.locator('[data-role="row-disclose"]').first().click();
    },
  },
  {
    url: `${ADMIN_URL}/password`,
    name: "/password (signed out)",
    ready: ".page .notice a[href='/sign-in']",
  },
  {
    url: `${ADMIN_URL}/password`,
    name: "/password (signed in)",
    ready: "#current-password",
    setup: async (page) => void (await signInAs(page.context(), ADMINISTRATOR)),
  },

  {
    url: `${ADMIN_URL}/sessions`,
    name: "/sessions (row disclosure expanded)",
    /* `tr.row-detail .row-detail-list dt` and not `.row-detail`: an empty list would
       satisfy the looser selector and axe would report a clean result for a panel
       with nothing in it — the same trap the wizard's `ul.findings li` avoids. */
    ready: "tr.row-detail .row-detail-list dt",
    after: async (page) => {
      await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
      await page.locator('[data-role="row-disclose"]').first().click();
    },
  },
];

/*
 * `/design` is not here, and that is the same exclusion `responsive-shell.spec.ts`
 * argues for at length: a lazily-loaded team surface, absent from `NAV`, that no
 * operator opens. Its markup is already swept flat in both schemes at desk width by
 * `tests/a11y/routes.spec.ts`, which is the run a design change is reviewed against.
 */

for (const route of ROUTES) {
  const label = route.name ?? new URL(route.url).pathname;

  test(`axe · admin 390px ${label}`, async ({ page }, testInfo) => {
    const reach = async () => {
      await page.goto(route.url);
      if (route.after) await route.after(page);
      await expect(page.locator(route.ready).first()).toBeVisible();
      await page.waitForLoadState("load");
    };

    if (route.setup) await route.setup(page);
    await reach();

    /* axe evaluates in the page, and a navigation invalidates that context. In
       development a portal or admin edit triggers a full reload. One retry, then it
       is real. Same handling as the desktop sweep. */
    let results;
    try {
      results = await axeFor(page).analyze();
    } catch (err) {
      if (!/Execution context was destroyed|navigation/i.test(String(err))) throw err;
      await reach();
      results = await axeFor(page).analyze();
    }

    expect(
      results.passes.length + results.violations.length + results.incomplete.length,
      `axe evaluated no rules on ${label} at 390px — the page probably did not render.`,
    ).toBeGreaterThan(0);

    /* The viewport really was narrow. A project whose viewport did not apply would
       scan the desk layout and report a clean result for markup it never saw. */
    expect(
      await page.evaluate(() => window.innerWidth),
      "This project's 390px viewport did not apply, so the desk layout was scanned.",
    ).toBe(390);

    await testInfo.attach(`axe-narrow${label.replace(/\//g, "_")}.json`, {
      body: JSON.stringify(
        results.violations.map((v) => ({
          id: v.id,
          impact: v.impact,
          help: v.help,
          helpUrl: v.helpUrl,
          nodes: v.nodes.map((n) => ({ target: n.target, failureSummary: n.failureSummary })),
        })),
        null,
        2,
      ),
      contentType: "application/json",
    });

    if (results.violations.length > 0) {
      console.log(
        `  a11y 390px ${label}\n` +
          results.violations
            .map((v) => `      ${v.impact ?? "?"} · ${v.id} × ${v.nodes.length} — ${v.help}`)
            .join("\n"),
      );
    }

    expect(
      results.violations.map((v) => `${v.id} × ${v.nodes.length}`),
      `Accessibility violations on ${label} at 390 × 844.\n` +
        "  The narrow layout was measured at zero when it was built, so this is a " +
        "regression in the change in front of you. It is also markup the desk sweep " +
        "cannot see: the drawer, the disclosure and the reduced column set only exist " +
        "below 900px. The full node list is attached to this test in the HTML report.",
    ).toEqual([]);
  });
}

/* Whatever a route signed in as, sign out of. Playwright gives every test a fresh
   context so nothing leaks between them — this is for the *mock*, whose session store
   is a `Map` in memory shared with everything else running on the machine. */
test.afterEach(async ({ context }) => {
  await signOut(context);
});
