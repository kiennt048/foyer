import { expect, test, type Page } from "@playwright/test";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `responsive-shell` — the highest-value assertion of the narrow layout
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * At 390 × 844 the admin console must not scroll sideways on any operator route.
 *
 * The failure this locks is **one number nine times**: without the narrow layout,
 * `document.scrollWidth` reads 1 574 px against a 390 px viewport on every one of the
 * nine routes, which is what says it is one structural cause rather than nine screen
 * bugs. See the long comment at `admin/src/styles/admin.css`'s
 * `@media (max-width: 900px)` block.
 *
 * **A green assertion here proves nothing on its own**, which is why the sabotage
 * case below is part of the suite rather than something somebody did once at a
 * terminal. A check can look like it is checking something and not be; the cheap
 * way to avoid that is to make the thing fail on purpose and ship the evidence beside the lock.
 *
 * The comparison is `scrollWidth` against `documentElement.clientWidth` — the layout
 * viewport — rather than against the literal 390. A classic (non-overlay) scrollbar
 * takes width from the layout viewport and not from `innerWidth`, so a run on a
 * machine configured that way would fail an equality against 390 while the page was
 * behaving perfectly. `innerWidth` is asserted separately, so a project whose
 * viewport did not apply is still caught.
 */

/* The nine destinations in `NAV` — what an operator can reach from the drawer.
   `ready` keeps the measurement off a skeleton: a table that has not loaded is
   narrow for the wrong reason and would make this whole file green and useless. */
interface Route {
  path: string;
  ready: string;
}

const OPERATOR_ROUTES: Route[] = [
  { path: "/", ready: ".card-grid .card .metric-foot" },
  { path: "/sessions", ready: "tbody[aria-busy=false]" },
  { path: "/controllers", ready: "tbody[aria-busy=false]" },
  { path: "/access-points", ready: "tbody[aria-busy=false]" },
  { path: "/vouchers", ready: "tbody[aria-busy=false]" },
  { path: "/accounts", ready: "tbody[aria-busy=false]" },
  /* Not a table. The form renders in stages and a `tbody` selector would wait
     forever, so this names the terms editor for the same reason the axe sweep does. */
  { path: "/portal", ready: "#venue-name" },
  { path: "/attempts", ready: "tbody[aria-busy=false]" },
  { path: "/garden", ready: "tbody[aria-busy=false]" },
];

interface Measurement {
  scrollWidth: number;
  clientWidth: number;
  innerWidth: number;
}

async function measure(page: Page): Promise<Measurement> {
  return page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
    innerWidth: window.innerWidth,
  }));
}

async function reach(page: Page, route: Route): Promise<void> {
  await page.goto(route.path);
  await expect(page.locator(route.ready).first()).toBeVisible();
}

test.describe("the console fits a 390px viewport", () => {
  for (const route of OPERATOR_ROUTES) {
    test(`${route.path} does not scroll sideways`, async ({ page }) => {
      await reach(page, route);
      const m = await measure(page);

      expect(m.innerWidth, "this project's 390px viewport did not apply").toBe(390);
      expect(
        m.scrollWidth,
        `${route.path} is ${m.scrollWidth}px wide against a ${m.clientWidth}px viewport — ` +
          `${m.scrollWidth - m.clientWidth}px of horizontal scroll on a phone.\n` +
          "  This was 1 574px on every route before the narrow layout, and the cause was structural " +
          "rather than per-screen: a grid track sized by its content. If several of these " +
          "went red together, look at the `@media (max-width: 900px)` block in admin.css " +
          "before you look at the screen — the last three shell findings were one " +
          "declaration each.\n" +
          "  A table wider than the port is *not* this failure: `.table-scroll` absorbs " +
          "that and the page stays 390. This only fires when the overflow reached the " +
          "document.",
      ).toBe(m.clientWidth);
    });
  }

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The nine assertions above are worth exactly as much as this one case.
   *
   * The layout cannot be reverted from here, so the revert is injected instead: the
   * four width declarations undone, in a stylesheet appended after the console's own,
   * at equal specificity so it wins on order. If the lock can see that, it can see a
   * regression.
   *
   * **Baseline without the fix**, worst route `/controllers`:
   *
   *   as it stands                          390px, 0 of 9 routes over
   *   the four width declarations reverted  526px, 9 of 9 over  (463–526 across the nine)
   *
   * Two things about those numbers:
   *
   *  - **The overflow is modest.** The narrow layout drops session columns into a
   *    per-row disclosure, which keeps the failure small — a lock written against a
   *    much larger figure would be silently vacuous.
   *  - **Reverting a single one of the four does not always measure 390.**
   *    Per-declaration, at 390:
   *
   *      `.app { grid-template-columns: var(--sidebar-w) 1fr }`  → 438–510px, 9 of 9 over
   *      `.sidebar { min-width: auto }`                          → 390px
   *      `.main   { min-width: auto }`                           → 390px
   *      `.page   { min-width: auto }`                           → 390px
   *      the other three together, track kept                    → 390px
   *
   *    The grid track has no redundant partner: the nav strip is not in the DOM (the
   *    drawer replaces it), so one declaration carries this. That is not an argument for deleting the other three — they cost
   *    nothing and the next wide thing in the sidebar is a long site name — but it is
   *    an argument for this test asserting the group rather than the individual, so
   *    it stays true whichever of them is holding the line.
   *
   * If this case ever fails, it means the injected revert **does not overflow**:
   * something else is holding the layout, and the numbers above are stale. That
   * is a real signal, not a flake — re-measure before touching the assertion.
   */
  test("the lock is not vacuous: reverting the width declarations overflows every route", async ({
    page,
  }) => {
    const REVERTED = `
      @media (max-width: 900px) {
        .sidebar { min-width: auto; }
        .app { grid-template-columns: var(--sidebar-w) 1fr; }
        .main { min-width: auto; }
        .page { min-width: auto; }
      }`;

    const measured: { path: string; scrollWidth: number; clientWidth: number }[] = [];

    for (const route of OPERATOR_ROUTES) {
      await reach(page, route);
      /* After the route has settled, so the injection is the only variable. */
      await page.addStyleTag({ content: REVERTED });
      /* One frame for layout to settle under the new rules. */
      await page.evaluate(
        () => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))),
      );
      const m = await measure(page);
      measured.push({ path: route.path, scrollWidth: m.scrollWidth, clientWidth: m.clientWidth });
    }

    const unaffected = measured.filter((m) => m.scrollWidth <= m.clientWidth).map((m) => m.path);
    const worst = measured.reduce((a, b) => (b.scrollWidth > a.scrollWidth ? b : a));

    expect(
      unaffected,
      "The sabotage did not overflow these routes, so the assertions above cannot " +
        "distinguish the fix from its absence on them. Either the injected stylesheet " +
        "no longer names the declarations that hold the layout — check " +
        "`admin.css`'s `@media (max-width: 900px)` block against the CSS in this test — " +
        "or something else is now doing the holding, in which case the measurements in " +
        "the comment above need taking again.\n" +
        `  Measured: ${measured.map((m) => `${m.path}=${m.scrollWidth}`).join(" ")}`,
    ).toEqual([]);

    /* And it is a real overflow rather than a pixel of rounding. */
    expect(
      worst.scrollWidth - worst.clientWidth,
      `The worst route under sabotage (${worst.path}) overflowed by only ` +
        `${worst.scrollWidth - worst.clientWidth}px. The recorded baseline is 136px.`,
    ).toBeGreaterThan(20);
  });

  /**
   * ── THE ROUTE EVERY ALARM LEADS TO ──────────────────────────────────────────
   *
   * `/access-points/:id` is not in `OPERATOR_ROUTES` because it needs an id, and it
   * had never been measured at 390 by anything. It scrolled sideways on all seven
   * fixture devices — `document.scrollWidth` 530, 556, 557 and **572** on
   * `nas-omada-1`, against a 390px viewport.
   *
   * **This is where every `View` button on the dashboard lands**, so an operator
   * tapping an alarm on a phone arrived on the one page that moved under their thumb.
   *
   * The cause was a `style={{ gridTemplateColumns: "1fr 1fr" }}` inside
   * `screens/nas/NasDetail.tsx`: a layout decision living in a component, where
   * `admin.css` could not see it and no `useNarrow()` rule and no media query of the
   * narrow layout could reach it. That is why the responsive sweep did not catch it and why
   * the fix is a class — `.record-grid` — rather than a narrower inline value.
   *
   * Every device is measured rather than one, because a `1fr` track's floor is its
   * *content's* min-content width: the failure is per-record, and one green device
   * would have said nothing about the other six.
   */
  test("/access-points/:id does not scroll sideways, on any device", async ({ page }) => {
    await page.goto("/access-points");
    await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();
    const paths = await page
      .locator("tbody tr:not(.empty-row) a[href^='/access-points/']")
      .evaluateAll((els) => els.map((el) => el.getAttribute("href") ?? ""));

    expect(paths.length, "no access-point rows to open — the fixtures are not loaded").
      toBeGreaterThan(1);

    const over: string[] = [];
    for (const path of paths) {
      await page.goto(path);
      await expect(page.locator("dl.kv").first()).toBeVisible();
      const m = await measure(page);
      if (m.scrollWidth > m.clientWidth) over.push(`${path}=${m.scrollWidth}`);
    }

    expect(
      over,
      "These access-point records scroll sideways at 390. It was 530–572px on all " +
        "seven before `.record-grid` replaced the inline `1fr 1fr`; a subset failing " +
        "now means a record whose content is wider than the track's collapsed floor, " +
        "which is a `min-width: 0` question rather than a track question.",
    ).toEqual([]);
  });

  /**
   * ── THE EVIDENCE FOR THE ONE ABOVE ──────────────────────────────────────────
   *
   * The inline declaration is gone from the source, so it is injected back. `1fr 1fr`
   * on `.record-grid` is exactly what `NasDetail` carried, and it must put the page
   * over the viewport again on the record that was worst.
   */
  test("the /access-points/:id lock is not vacuous: 1fr 1fr overflows it again", async ({
    page,
  }) => {
    await page.goto("/access-points/nas-omada-1");
    await expect(page.locator("dl.kv").first()).toBeVisible();
    await page.addStyleTag({ content: `.record-grid { grid-template-columns: 1fr 1fr; }` });
    await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => r(null))));

    const m = await measure(page);
    expect(
      m.scrollWidth - m.clientWidth,
      `Putting back \`1fr 1fr\` left /access-points/nas-omada-1 at ${m.scrollWidth}px ` +
        `against a ${m.clientWidth}px viewport. It was 572px — 182 over — before the ` +
        "fix, so if it fits now, something else is holding this page and the lock " +
        "above cannot tell the fix from its absence.",
    ).toBeGreaterThan(20);
  });

  /**
   * ── THE TENTH ROUTE ─────────────────────────────────────────────────────────
   *
   * `/design` is the tenth route the admin serves and it is **not** in the list
   * above. A route quietly missing from a coverage list reads as coverage, so it is
   * named here with its reason instead.
   *
   * The gallery is a team artefact rather than an operator surface, and its overflow
   * risk comes from its own two-theme panels rather than from a table that keeps its
   * columns. **It holds 390**, and this case asserts that rather than exempting it — a
   * lock is worth more than an exemption. It stays a separate test
   * rather than a tenth entry in `OPERATOR_ROUTES` for two reasons that are still
   * true: the gallery is absent from `NAV` (asserted below), and the sabotage case
   * above injects the *table* declarations, which have never been what holds this
   * page.
   *
   * Three things would each push it past 390 (to about 508px), and they hide behind one
   * another — removing any one alone can leave the number unchanged:
   *
   *   `.gal-themes: minmax(30rem, 1fr)`   the track's 480px floor in a 358px column
   *   a sentence inside a `.btn`          473px, `white-space: nowrap`, in the same column
   *   `.gal-caption { white-space: nowrap }`  a token name that overflows its inline
   *                                       box without ever growing a rectangle — so an
   *                                       element sweep for `right > viewport` finds
   *                                       *nothing* and the page still moves
   *
   * The last one is why the sabotage below re-injects CSS rather than trusting the
   * `widest` probe: two of the three causes are invisible to a bounding-box sweep.
   */
  test("/design holds 390 too, and is still not an operator destination", async ({ page }) => {
    await page.goto("/design");
    await expect(page.locator('.gal-theme[data-scheme="dark"] .pill').first()).toBeVisible();

    const m = await measure(page);
    expect(
      m.scrollWidth,
      `/design is ${m.scrollWidth}px wide against a ${m.clientWidth}px viewport. Three ` +
        "declarations hold it at 390 — the " +
        "`.gal-themes` track floor, a sentence used as a button label, and " +
        "`.gal-caption { white-space: nowrap }`. Check all three before assuming the " +
        "shell.",
    ).toBe(m.clientWidth);

    /* Not offered to an operator. The drawer is the only navigation in the console. */
    await expect(
      page.locator('nav.nav a[href="/design"]'),
      "/design is in the navigation now, which makes it an operator surface and moves " +
        "it into OPERATOR_ROUTES above.",
    ).toHaveCount(0);
  });

  /**
   * ── THE EVIDENCE FOR THE ONE ABOVE ──────────────────────────────────────────
   *
   * Same discipline as the nine-route sabotage: the layout cannot be reverted from
   * here, so the two declarations that carry it are undone by injection at equal
   * specificity.
   * The third cause — the button label — is JSX and cannot be re-injected, which is
   * precisely why it is not relied on: either of these two alone puts the page back
   * over the viewport.
   *
   * Recorded: 390px as it stands, **439px** with
   * `.gal-caption`'s `nowrap` alone, **508px** with both.
   */
  test("the /design lock is not vacuous: reverting either declaration overflows it", async ({
    page,
  }) => {
    const REVERTS: { name: string; css: string }[] = [
      {
        name: ".gal-themes track floor",
        css: `.gal-themes { grid-template-columns: repeat(auto-fit, minmax(30rem, 1fr)); }`,
      },
      {
        name: ".gal-caption white-space",
        css: `.gal-caption { white-space: nowrap; overflow-wrap: normal; }`,
      },
    ];

    for (const revert of REVERTS) {
      await page.goto("/design");
      await expect(page.locator('.gal-theme[data-scheme="dark"] .pill').first()).toBeVisible();
      await page.addStyleTag({ content: revert.css });
      /* A style tag changes layout synchronously, but the measurement is taken after
         a frame so the grid has been re-resolved rather than read mid-flush. */
      await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => r(null))));

      const m = await measure(page);
      expect(
        m.scrollWidth - m.clientWidth,
        `Putting back \`${revert.name}\` left /design at ${m.scrollWidth}px against a ` +
          `${m.clientWidth}px viewport — it did not overflow, so the assertion above ` +
          "cannot tell the fix from its absence. Something else is holding this page " +
          "and the numbers in the comment need taking again.",
      ).toBeGreaterThan(20);
    }
  });
});
