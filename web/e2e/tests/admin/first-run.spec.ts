import { expect, test, type APIRequestContext, type Locator, type Page } from "@playwright/test";
import { admin, tableReady } from "../helpers/admin.js";
import { MOCK_URL } from "../helpers/env.js";
import { expectNasRemoved, listNas, mockTick } from "../helpers/mock.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  THE FIRST-RUN PATH — an empty site, to a voucher batch, and back out again
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The design (`docs/architecture.md`) makes *"a stranger installs it and a phone gets
 * online"* a timed release bar and names the first moment of user value: **an operator with one
 * access point creates a voucher, and a phone gets online.** `deploy/first-boot.sh`
 * covers the host side. This is the console's side, driven end to end.
 *
 * **Nothing in this suite had ever opened a site with nothing in it.** 26 admin spec
 * files, 364 tests, and every one of them looks at `site-hcm` — the deliberately ugly
 * fixture site — or at the hotel. The empty site existed the whole time and was only
 * ever an input to `no-radius-configured`. What it showed when an operator actually
 * looked at it had not been checked, and the answer was **`0/0` access points, "All
 * reachable", "Nothing needs action."** on a deployment that could not put a single
 * guest online.
 *
 * ── WHAT THIS FILE HOLDS ────────────────────────────────────────────────────
 *
 *  1. The empty site names what to do first, and **every route still works** while it
 *     is half-configured. A first-run flow that only works if you finish it is a flow
 *     people work around.
 *  2. The path itself: device, then codes, driven through the screens that already own
 *     those writes rather than through a second copy of their forms.
 *  3. **Abandonment, twice.** Cancelled mid-registration, and abandoned after the
 *     device exists with a full reload in between. Nothing is lost and nothing is
 *     duplicated, which is a property of the panel holding *no state at all*: every
 *     step is re-derived from the rows, so there is nothing to resume and nothing to
 *     resume twice.
 *  4. **The sabotage, in both of its forms.**
 *
 *     The one that runs every time: `listVoucherBatches` is answered `{items: []}` for
 *     the browser only, so the codes step loses its completion check while the mock
 *     still holds the batch and can still be asked. The panel is *required* to come
 *     back — evidence re-derived on every run rather than recorded once, the shape `responsive-shell` and `palette-is-an-overlay` use.
 *
 *     And the one that cannot be: the lock itself. With `needsBatch` changed to
 *     `offersVoucher` in `admin/src/screens/FirstRun.tsx` — the completion check
 *     deleted outright — *"the codes step, and then the panel is gone"* goes red
 *     saying:
 *
 *       > this site holds 1 access point and 1 voucher batch, and the console is still
 *       > asking for them on the screen the operator lands on — every step it still
 *       > shows is an instruction to do again what has just been done, and doing it
 *       > produces a second batch of codes for one venue with no way to tell which is
 *       > which.
 *
 *     The two are not redundant: the interception proves the panel reads that check, and the source
 *     edit proves the **lock** is what would report its removal.
 *
 * ── THE SITE IS DERIVED, NEVER NAMED ────────────────────────────────────────
 *
 * `site-lab` is the empty one today, and this file never says so. It asks the mock
 * which site has no devices and no batches. That is `docs/rules/testing.md`'s
 * *derive the stand-in from what it stands in for*: a hardcoded id goes on passing the
 * day somebody populates that site, while measuring a world it no longer describes.
 *
 * It also makes the leak check free. If **no** site is empty, either the fixtures
 * changed or a previous run of this file left its device or its batch behind — and the
 * mock keeps its world across runs, so that device would be in every later row count,
 * dashboard figure and screenshot until somebody restarted it by hand. The failure
 * says which.
 *
 * ── THE SELECTED SITE, AND WHY `landOn` STILL SELECTS IT ────────────────────
 *
 * The venue is a `?site=` parameter, so a full page load keeps the operator on the
 * venue they chose; `tests/admin/site-survives-a-reload.spec.ts` is the lock on it.
 *
 * `landOn` still opens `/` and drives the switcher, and that is deliberate rather than
 * left over. The parameter is **absent when the venue is the deployment's first one**,
 * which is the console's own rule for view scope — `/sessions` carries no `?state=`
 * when nothing is filtered — so a bare `goto` is still a document load that names no
 * venue and still resolves to the café. Selecting is what an operator does, and it is
 * what puts the id in the address for everything that follows.
 *
 * The consequence for this file is that a URL assertion is about the **path**: the
 * address after a click into the empty site carries `?site=…`, because that venue is
 * not the default one.
 */

/* ── the copy this file is allowed to read ─────────────────────────────────── */

/**
 * Mirrored from `web/admin/src/i18n/`, and the only place this suite reads copy.
 *
 * `helpers/admin.ts` says selectors are structure and `data-*`, never copy — and it is
 * right, because a label under translation must not fail a suite. These two are the
 * exception for the reason `coa-vendor.spec.ts` keeps its own `COPY` block: **the
 * defect was a specific false sentence.** "All reachable" over `0/0` is not a
 * structural fault; there is no attribute that distinguishes it from "All reachable"
 * over `12/12`, and a test that checked the structure would have been green against
 * the thing it exists to catch.
 */
const COPY = {
  /** `dashboard.nasHealthy` — the claim that must not be made about an empty estate. */
  healthy: "All reachable",
  /** `dashboard.nasNone` — what replaced it. */
  none: "Nothing is serving guests yet",
  /** `dashboard.nothingToDo` */
  nothingToDo: "Nothing needs action.",
} as const;

/* ── selectors ─────────────────────────────────────────────────────────────── */

const firstRun = {
  panel: "section[data-first-run]",
  step: (name: string) => `section[data-first-run] [data-step="${name}"]`,
  act: (name: string) => `[data-first-run-act="${name}"]`,
  outside: "section[data-first-run] [data-first-run-outside] li",
} as const;

/** The access-points card's caption. Third of four, and located by its own label. */
function accessPointFoot(page: Page): Locator {
  return page
    .locator(admin.cards)
    .filter({ has: page.locator(admin.metricValue) })
    .nth(2)
    .locator(admin.metricFoot);
}

/* ── the world, asked rather than assumed ──────────────────────────────────── */

interface Site {
  id: string;
  name: string;
}

async function json<T>(request: APIRequestContext, path: string): Promise<T> {
  const res = await request.get(`${MOCK_URL}${path}`);
  if (!res.ok()) throw new Error(`GET ${path} → ${res.status()} ${await res.text()}`);
  return (await res.json()) as T;
}

const nasCount = async (request: APIRequestContext, siteId: string) =>
  (await json<{ items: unknown[] }>(request, `/api/v0/nas?siteId=${siteId}`)).items.length;

const batchCount = async (request: APIRequestContext, siteId: string) =>
  (await json<{ items: unknown[] }>(request, `/api/v0/voucher-batches?siteId=${siteId}`)).items
    .length;

/**
 * The site with no access points and no voucher batches, **resolved once.**
 *
 * Once, and not per test: these cases *fill* the site — that is the whole point of
 * them — so a per-test lookup would find the formerly empty site no longer empty and
 * report the leak message about its own previous case. The emptiness is a precondition of the file, checked at the start of
 * the file.
 *
 * Fails by name when there is none, because the two reasons that can happen are the
 * two things a reader needs told apart: the fixture world changed, or this file leaked.
 */
let site: Site;

test.beforeAll(async ({ request }) => {
  const sites = await json<Site[]>(request, "/api/v0/sites");
  for (const s of sites) {
    if ((await nasCount(request, s.id)) === 0 && (await batchCount(request, s.id)) === 0) {
      site = s;
      return;
    }
  }
  throw new Error(
    "No site in the mock has zero access points and zero voucher batches, so there is " +
      "no empty deployment to open. Either the fixture world gained a device in every " +
      "site, or a previous run of first-run.spec.ts left its own behind — the mock " +
      "keeps its world across runs. Restart the mock and check `afterAll` below.",
  );
});

/** Drives the header switcher. In-app, so nothing else about the page reloads. */
async function selectSite(page: Page, name: string): Promise<void> {
  const caret = page.locator('.site-switch [aria-haspopup="menu"]').last();
  await caret.click();
  const item = page.locator('[role="menuitemradio"]', { hasText: name });
  await expect(item, `no site named "${name}" in the switcher`).toHaveCount(1);
  await item.click();
  await expect(page.locator(".site-switch-value")).toHaveText(name);
}

/** Opens the dashboard on the empty site, the way an operator arrives at it. */
async function landOn(page: Page): Promise<void> {
  await page.goto("/");
  await selectSite(page, site.name);
  await expect(page.locator(`${admin.cards} .skeleton`)).toHaveCount(0);
}

/* ── cleanup, and it is not optional ───────────────────────────────────────── */

const registered: { name: string; tick: number }[] = [];
const created: string[] = [];

test.afterAll(async ({ request }) => {
  for (const { name, tick } of registered.splice(0)) {
    await expectNasRemoved(request, name, tick, site?.id);
  }
  for (const batchId of created.splice(0)) {
    /*
     * `DELETE /mock/voucher-batches/:id` — the harness affordance, outside `/api/v0`
     * and deliberately not a product operation. It refuses a seeded batch with a
     * `409`, so it cannot empty the ugly world out from under another file.
     */
    const res = await request.delete(`${MOCK_URL}/mock/voucher-batches/${batchId}`);
    expect(
      res.ok(),
      `the batch this file created (${batchId}) is still in the mock: ${await res.text()}`,
    ).toBe(true);
  }
});

/* ══ 1 · the empty site ═══════════════════════════════════════════════════════ */

test.describe.serial("the first-run path, from a site with nothing in it", () => {
  test("an empty site says what to do first, and does not report itself healthy", async ({
    page,
    request,
  }) => {
    await landOn(page);

    /*
     * The figure and its caption, which were the defect.
     *
     * `0/0` is right. "All reachable" beneath it is the console's own thesis —
     * absence is not zero — failing on the landing screen: an estate of nothing is
     * not a healthy estate, and this is the first sentence a stranger who has just
     * run `first-boot.sh` reads about their deployment.
     */
    await expect(accessPointFoot(page)).toHaveText(COPY.none);
    await expect(
      accessPointFoot(page),
      "a deployment with no access points reported them all reachable",
    ).not.toHaveText(COPY.healthy);

    /* And the panel, with both steps outstanding. */
    await expect(page.locator(firstRun.panel)).toBeVisible();
    await expect(page.locator(firstRun.step("device"))).toHaveAttribute("data-state", "todo");
    await expect(page.locator(firstRun.step("codes"))).toHaveAttribute("data-state", "todo");

    /*
     * The green tick, and the heading over it.
     *
     * "Nothing needs action." is true *about alarms* and reads as a flat contradiction
     * printed under a list of things to do. Suppressing only the card left `NEEDS
     * ACTION` standing over nothing, which is the empty frame this console has been
     * fixed for twice — so the assertion is on the section, not on the sentence.
     */
    await expect(page.getByText(COPY.nothingToDo)).toHaveCount(0);
    await expect(
      page.locator(`${admin.sections} .section-title`).filter({ hasText: /needs action/i }),
      "a section heading with nothing under it",
    ).toHaveCount(0);

    /*
     * What Foyer cannot do, named rather than automated (*"the five minutes that
     * matter are after the container starts"*). Three lines here: the device has to be
     * pointed at Foyer, a controller API key is issued in the controller's own UI, and
     * DNS plus a certificate the guest's phone trusts. None is checkable from the
     * console, which is exactly why they are stated.
     *
     * A count, not the sentences: the *number* is derived from what the site holds
     * — the Cisco access-list line appears only for a CoA device, the walled-garden
     * line only where no device can enforce one — and the next case measures that it
     * moves.
     */
    await expect(page.locator(firstRun.outside)).toHaveCount(3);
  });

  test("every route still works while the site is half-configured", async ({ page, request }) => {
    await landOn(page);

    /*
     * The requirement a wizard usually fails: an operator who abandons this has a
     * half-configured deployment, not a broken one. This panel cannot trap anybody —
     * it is a section on a page, not a modal — so the thing worth measuring is that
     * the *rest of the console* survives having no data at all.
     *
     * Driven through the sidebar rather than by `goto`, both because that is what an
     * operator does and because a `goto` would drop the selected site and quietly
     * measure the café instead.
     */
    const links = page.locator(admin.navItems);
    const count = await links.count();
    expect(count, "the sidebar rendered no destinations").toBeGreaterThan(5);

    for (let i = 0; i < count; i++) {
      const link = links.nth(i);
      const href = await link.getAttribute("href");
      await link.click();
      await expect(page.locator(admin.pageTitle), `${href} rendered no title`).not.toBeEmpty();
      /* An urgent notice is how this console renders *the request failed*. An empty
         site must produce none of them: empty is a state, not a fault. */
      await expect(
        page.locator('main .notice[data-level="urgent"]'),
        `${href} rendered a failure on a site that is merely empty`,
      ).toHaveCount(0);
      /* And the site is still the one we chose — a route that resets it would take
         the operator to another venue's data without saying so. */
      await expect(page.locator(".site-switch-value")).toHaveText(site.name);
    }
  });

  /* ══ 2 · the path ══════════════════════════════════════════════════════════ */

  test("registration abandoned halfway leaves nothing behind", async ({ page, request }) => {
    await landOn(page);

    await page.locator(firstRun.act("device")).click();
    await tableReady(page);
    await page.locator("main .page-head button").first().click();

    const dialog = page.locator("dialog[open]");
    await expect(dialog.locator("#nas-driver")).toBeVisible();
    await dialog.locator("#nas-name").fill("first-run abandoned — never submitted");
    await dialog.locator("#nas-ip").fill("10.91.0.1");

    /* Out through the dialog's own cancel, which is the step that has one. */
    await dialog.locator('form button[type="button"]').last().click();
    await expect(page.locator("dialog[open]")).toHaveCount(0);

    expect(
      await nasCount(request, site.id),
      "a cancelled registration created a device anyway",
    ).toBe(0);

    /* Back to the dashboard in-app, and the panel says exactly what it said before —
       no half-finished step, no resumable cursor, because there is no state to hold. */
    await page.locator(admin.navItems).first().click();
    await expect(page.locator(firstRun.step("device"))).toHaveAttribute("data-state", "todo");
    await expect(page.locator(firstRun.step("codes"))).toHaveAttribute("data-state", "todo");
  });

  test("the device step, then a full reload, and the panel picks up where it was", async ({
    page,
    request,
  }) => {
    const name = `first-run ${Date.now()}`;
    await landOn(page);

    /* ── the operator follows the panel's own action ─────────────────────────── */
    await page.locator(firstRun.act("device")).click();
    /* The path, not the whole address: the venue this case works in is not the
       deployment's first, so the address carries `?site=…`. */
    await expect(page).toHaveURL(/\/access-points(\?|$)/);
    await tableReady(page);
    await page.locator("main .page-head button").first().click();

    const dialog = page.locator("dialog[open]");
    await expect(dialog.locator("#nas-driver")).toBeVisible();
    await dialog.locator("#nas-name").fill(name);
    await dialog.locator("#nas-ip").fill("10.91.0.2");
    /*
     * Answer whatever the form asks and nothing it does not — `coa-vendor.spec.ts`'s
     * rule. A helper that knew which drivers need a vendor would be asserting its own
     * knowledge; this one takes the first real option of every empty select, so a
     * driver whose required field the form fails to render fails at the server.
     */
    for (const select of await dialog.locator("form select").all()) {
      if ((await select.inputValue()) !== "") continue;
      const real = select.locator("option").nth(1);
      await select.selectOption((await real.getAttribute("value")) ?? "");
    }

    const tick = await mockTick(request);
    await dialog.locator('form button[type="submit"]').click();

    /* The secret step: shown once, and the only way out is through it. */
    const ack = dialog.locator('input[type="checkbox"]');
    await expect(ack).toBeVisible();
    await ack.check();
    await dialog.locator('.dialog-foot button[type="button"]').click();

    /* The probe step. Its verdict is not this file's subject — that it exists is. */
    await expect(dialog.locator(".findings-note").first()).toBeVisible();
    registered.push({ name, tick });

    await page.keyboard.press("Escape");
    await expect(page.locator("dialog[open]")).toHaveCount(0);

    expect(await nasCount(request, site.id), "the device was not registered in this site").toBe(1);

    /* ── abandon: a full reload, which is the honest way to leave and come back ── */
    await landOn(page);

    await expect(page.locator(firstRun.panel)).toBeVisible();
    await expect(
      page.locator(firstRun.step("device")),
      "the step did not notice the device that now exists",
    ).toHaveAttribute("data-state", "done");
    await expect(page.locator(firstRun.step("codes"))).toHaveAttribute("data-state", "todo");

    /* Nothing duplicated: coming back did not re-register, re-prompt or double-count. */
    expect(
      await nasCount(request, site.id),
      "re-entering the first-run path created a second device",
    ).toBe(1);
    await expect(page.locator(firstRun.step("device"))).toHaveCount(1);

    /*
     * And the outside-Foyer list has *moved*, which is what says it is derived from the
     * site rather than printed. The controller API-key line was there when no device
     * existed and both paths were still open; a standalone MikroTik-family device
     * closes that path, and an unprobed device reports `walledGarden: "none"`, which
     * opens the line saying the garden list is enforced by nothing here.
     */
    const outside = await page.locator(firstRun.outside).allInnerTexts();
    expect(outside.join(" "), "the API-key line survived a site with no controller").not.toMatch(
      /API key/i,
    );
    expect(
      outside.join(" "),
      "no line about a device that cannot restrict pre-sign-in traffic",
    ).toMatch(/before they sign in/i);

    await expect(accessPointFoot(page)).not.toHaveText(COPY.none);
  });

  test("the codes step, and then the panel is gone", async ({ page, request }) => {
    expect(
      await nasCount(request, site.id),
      "this case runs after the device step and needs its device",
    ).toBe(1);

    await landOn(page);
    await page.locator(firstRun.act("codes")).click();
    await expect(page).toHaveURL(/\/vouchers(\?|$)/);
    await tableReady(page);

    await page.locator("main .page-head button").first().click();
    const dialog = page.locator("dialog[open]");
    await expect(dialog.locator("#b-label")).toBeVisible();
    await dialog.locator("#b-label").fill(`first-run ${Date.now()}`);
    await dialog.locator("#b-count").fill("3");

    const answered = page.waitForResponse(
      (r) => r.request().method() === "POST" && /\/api\/v0\/voucher-batches$/.test(r.url()),
    );
    await dialog.locator('form button[type="submit"]').click();
    const res = await answered;
    expect(res.status(), await res.text()).toBe(201);
    created.push(((await res.json()) as { id: string }).id);

    /*
     * ── straight back, without a reload ──────────────────────────────────────
     *
     * In-app, so the query cache survives the trip and the panel has to be right about
     * a batch that was created seconds ago on another screen. Two seconds after the
     * write, a panel serving a cached "there are none" would ask the operator to create
     * the batch they just created — the harm this file is named for, arriving through
     * the cache instead of through a missing check.
     *
     * **Resting this on `/vouchers`'s `invalidateQueries(["batches"])` reaching the
     * panel by key prefix would make it unable to fail**: with the prefix broken it
     * stays green, because `expect` polls for seven seconds and the console's
     * ten-second staleness expires underneath it. A guarantee that holds only because
     * the test waited is a single-sample race in the other direction — the wait does
     * the work.
     *
     * The panel's queries are `staleTime: 0`, so mounting the dashboard re-derives
     * every step from the server and this holds by construction rather than by timing.
     *
     * With the freshness taken back out (`staleTime` at the console's 10 s and the key
     * given a prefix nothing invalidates), this fails saying:
     *
     *   > the batch was created seconds ago on the screen this panel links to, and the
     *   > panel is still asking for it — the same instruction, given twice
     *
     * ── AND WHY THE RESPONSE IS AWAITED FIRST ────────────────────────────────
     *
     * *"The panel is not asking for the batch"* is an **absence**, and this panel is
     * also absent while its queries are in flight — deliberately, because an unanswered
     * request is not the same fact as an empty list. So the naive order is a race by
     * construction: `toHaveCount(0)` is satisfied by the loading state, resolves in
     * milliseconds, and the case is green before anything has been asked.
     *
     * It was, in the full suite, on the second run. A `page.on("request")` counter
     * beside it read `0` — a `request` event crossing to Node after an assertion that
     * had already passed on the loading state, which is `testing.md`'s 3–9 ms boundary
     * in a new place. Isolated-green and in-suite-red: a timing signature, not a flake.
     *
     * So the response is registered **before** the click, awaited, and its body checked.
     * Once the panel has been *told* there is a batch, the step's absence is the panel's
     * doing and nothing else's.
     */
    const answeredBatches = page.waitForResponse(
      (r) => r.request().method() === "GET" && /\/api\/v0\/voucher-batches\?/.test(r.url()),
    );
    await page.locator(admin.navItems).first().click();
    await expect(page).toHaveURL(/\/(\?|$)/);

    const told = (await (await answeredBatches).json()) as { items: unknown[] };
    expect(
      told.items.length,
      "the panel asked for this site's batches and was answered with none, so the " +
        "server or the site is the subject here rather than the console",
    ).toBe(1);

    await expect(
      page.locator(firstRun.step("codes")),
      "the batch was created seconds ago on the screen this panel links to, the panel " +
        "has been told it exists, and it is still asking for one — the same " +
        "instruction, given twice",
    ).toHaveCount(0);

    /* ── the sentence the release bar names, reached ──────────────────────────── */
    await landOn(page);

    /*
     * **The lock.** One access point and one batch of codes, and the panel is gone —
     * not collapsed, not ticked, gone.
     *
     * The case below takes one step's completion check away and requires this to
     * fail, which is the only reason this assertion is worth anything.
     */
    await expect(
      page.locator(firstRun.panel),
      `this site holds ${await nasCount(request, site.id)} access point and ` +
        `${await batchCount(request, site.id)} voucher batch, and the console is still ` +
        "asking for them on the screen the operator lands on — every step it still " +
        "shows is an instruction to do again what has just been done, and doing it " +
        "produces a second batch of codes for one venue with no way to tell which is " +
        "which. A step is done when the thing it asked for exists; if this is red, one " +
        "of them has stopped reading whether it does.",
    ).toHaveCount(0);

    /*
     * And *Needs action* is back — the section, not the sentence.
     *
     * What is under it is derived from the alarms rather than typed here. The device
     * this file registers is probed on the wizard's third step, and the mock's probe
     * finds it **refuses Message-Authenticator** — a real BlastRADIUS alarm on a real
     * device, so the section correctly holds a warning instead of the green tick.
     * Which of the two appears is a property of the probe; that the section exists
     * again is the property under test.
     */
    const alarms = await json<unknown[]>(request, `/api/v0/alarms?siteId=${site.id}`);
    await expect(
      page.locator(".section-title").filter({ hasText: /needs action/i }),
    ).toHaveCount(1);
    if (alarms.length === 0) {
      await expect(page.getByText(COPY.nothingToDo)).toBeVisible();
    } else {
      await expect(page.locator(`${admin.notice}`).first()).toBeVisible();
    }

    await expect(accessPointFoot(page)).not.toHaveText(COPY.none);
  });

  /* ══ 3 · the sabotage ══════════════════════════════════════════════════════ */

  test("SABOTAGE: a step that cannot see what it asked for asks for it twice", async ({
    page,
    request,
  }) => {
    const batches = await batchCount(request, site.id);
    expect(batches, "this case needs the batch the previous one created").toBe(1);

    /*
     * **Removing one step's completion check, from outside the product.**
     *
     * The codes step is done when `listVoucherBatches` answers with a row. Take that
     * answer away — for the browser only, so the mock still holds the batch and can
     * still be asked — and the step has no way to know it was ever finished.
     *
     * A source edit would have been the other way to do this, and it is worse: it
     * proves the lock can fail against a build nobody will ever run. This is the shape
     * `responsive-shell` and `palette-is-an-overlay` use — re-inject the defect from
     * outside, so the evidence is re-derived on every run instead of being recorded
     * once in a commit message.
     */
    await page.route("**/api/v0/voucher-batches?*", async (route) => {
      await route.fulfill({
        status: 200,
        contentType: "application/json",
        body: JSON.stringify({ items: [], nextCursor: null }),
      });
    });

    await landOn(page);

    await expect(
      page.locator(firstRun.panel),
      "a step whose completion check was removed left the panel closed, so the panel " +
        "is not reading that check at all and the lock above proves nothing",
    ).toBeVisible();
    await expect(page.locator(firstRun.step("codes"))).toHaveAttribute("data-state", "todo");

    /*
     * The harm, stated as the failure message it would produce.
     *
     * The mock — asked directly, past the interception — still holds the batch. So the
     * console is offering *Create batch* for codes that already exist, on the screen an
     * operator lands on. Doing it produces a second batch of codes for one venue, and
     * the operator has no way to tell from here which of the two they are handing out.
     */
    expect(
      await batchCount(request, site.id),
      "the interception reached the mock's own state, so this case is measuring the " +
        "world rather than the console's view of it",
    ).toBe(batches);
    await expect(
      page.locator(firstRun.act("codes")),
      `the site holds ${batches} voucher batch and the console is asking the operator ` +
        "to create one — the same instruction, given twice, on the landing screen",
    ).toBeVisible();

    /* The device step is untouched by the sabotage, which is what says the panel reads
       one check per step rather than one check for the panel. */
    await expect(page.locator(firstRun.step("device"))).toHaveAttribute("data-state", "done");
  });
});
