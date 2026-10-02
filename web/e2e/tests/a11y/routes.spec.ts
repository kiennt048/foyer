import AxeBuilder from "@axe-core/playwright";
import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { admin } from "../helpers/admin.js";
import { ADMIN_URL, PORTAL_URL } from "../helpers/env.js";
import { expectNasRemoved, freshVoucherCode, mockTick } from "../helpers/mock.js";
import { portal } from "../helpers/portal.js";
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

const OUT_DIR = fileURLToPath(new URL("../../test-results/", import.meta.url));

/**
 * Accessibility smoke over every route in both applications, in both colour schemes.
 *
 * **Every route gates. There is no flag and no exemption.** Any WCAG violation, on
 * any route, in either scheme, fails the run.
 *
 * There is no strict-mode switch and no per-application split, because **a gate that
 * is off everywhere because one area is not ready is a gate that never turns on.**
 * Both themes are declared per token through `light-dark()`, so `color-contrast` is a
 * token concern rather than a per-screen one.
 *
 * Zero is the measured state everywhere, which means any failure here is a regression
 * introduced by the change in front of you, not inherited debt — nobody has to argue
 * about whether it was already broken.
 *
 * **Do not reintroduce a reporting mode to land a change that breaks this.** The
 * reporting mode existed to make an unmeasured area visible, not to make a measured
 * one optional.
 *
 * The colour-scheme split is not decoration: contrast failures are theme-specific
 * by construction, and a suite that only ever scanned one theme would certify half
 * the product.
 */

/* WCAG only. `best-practice` is deliberately excluded — it is advice, and mixing
   advice into a count that is supposed to reach zero is how a count stops meaning
   anything. */
const TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];

interface RouteSpec {
  app: "portal" | "admin";
  url: string;
  /** Something that must be on screen before scanning, so axe does not race a skeleton. */
  ready: string;
  /**
   * What the test is called, when the URL alone does not say.
   *
   * Two rows may share a URL and differ only in the state they are reached in —
   * `/connected/` with and without a grant are two genuinely different documents.
   * Without distinct names they read as a duplicate in the output and get
   * "deduplicated" by the next person tidying the list.
   */
  name?: string;
  /** Puts the browser into the state this route is about, before it is visited. */
  setup?: (page: Page) => Promise<void>;
  /**
   * Drives the surface into the state this route is about, **after** `goto` and
   * before `ready`.
   *
   * `setup` cannot express this, and the difference is not cosmetic: it runs before
   * the navigation, so it can only arrange things the *server* will answer with — a
   * cookie, a grant, a redemption. A step that exists only once a click has happened
   * is out of its reach entirely.
   *
   * That gap is what this hook closes. The registration wizard's second and third
   * steps are reachable only by filling the first and submitting it, so without this
   * hook axe never sees them — and a `serious` `scrollable-region-focusable`
   * violation on the findings list is exactly the kind of defect that lives there.
   * The gate fails the build on any violation anywhere, which makes the surfaces the
   * gate cannot *reach* the only place a regression can now hide.
   *
   * A hook that clicks is allowed to have consequences, and the ones here create a
   * device. See `registered` below: what a route makes, this file takes back.
   */
  after?: (page: Page) => Promise<void>;
}

/**
 * Redeem a voucher, so `/connected/` is the page a guest actually reaches.
 *
 * The success page is the one carrying the plan facts, the sampling qualifier and
 * the return link — the densest surface in the portal and the one it would be worst
 * to stop measuring. It is only reachable with a grant cookie, so it has to be
 * earned rather than navigated to.
 *
 * `freshVoucherCode` looks one up rather than hardcoding it: the mock is stateful and
 * every redemption consumes a real code. The cost is one voucher per colour scheme
 * per run, against a seeded batch of forty.
 */
async function redeemVoucher(page: Page): Promise<void> {
  const code = await freshVoucherCode(page.context().request);
  await page.goto(`${PORTAL_URL}/en/voucher/`);
  await page.locator(portal.codeInput).fill(code);
  await page.locator(portal.submit).click();
  await expect(
    page.locator(portal.connectedBadge),
    "Redemption did not reach the connected page, so the success variant was never scanned.",
  ).toBeVisible();
}

/* ── the registration wizard ──────────────────────────────────────────────── */

/**
 * Access points this file registered, drained by `afterEach`.
 *
 * An entry lands here only once the device is **confirmed** to exist — after the
 * secret step has rendered — so an empty list after a failed submit is the truth
 * rather than a cleanup with nothing to find, and `afterEach` can *assert* the
 * deletion instead of hoping for it. The mock is stateful and shared: a run that
 * leaves devices behind makes every later row count, dashboard figure and screenshot
 * describe a world that drifted rather than one that was authored.
 *
 * `tick` is the mock's clock at the moment of registration, and it is what lets the
 * assertion be strict without being flaky — see `expectNasRemoved`.
 */
const registered: { name: string; tick: number }[] = [];

/** Unique per registration, because cleanup locates the device by name. */
let registrations = 0;
const nasName = () => `a11y sweep ${Date.now()}-${++registrations}`;

/**
 * Step one → step two: the shared secret.
 *
 * The one screen in the console rendering a value no operation ever returns. It has
 * no exit that is not forward, a checkbox standing between the operator and the rest
 * of the flow, and a primary button disabled until that checkbox is ticked — three
 * states axe had never seen, on the step where being unusable costs the most.
 */
async function toSecretStep(page: Page): Promise<void> {
  const name = nasName();
  const tick = await mockTick(page.context().request);
  await page.locator("dialog[open] #nas-name").fill(name);
  /* The address is required: a `clients.conf` row has no other key, since FreeRADIUS
     matches a packet to a client by its source address. Filling only the name leaves
     the form refusing itself, and this sweep would then scan step one twice while
     reporting a pass — which is what the message on the assertion below exists to
     say out loud. */
  await page.locator("dialog[open] #nas-ip").fill("10.20.0.61");
  await page.locator("dialog[open] form button[type=submit]").click();
  await expect(
    page.locator("dialog[open] .secret"),
    "Registration never reached the secret step, so this route scanned step one twice.",
  ).toBeVisible();
  /* Only now: the device exists, and it is this run's to delete. */
  registered.push({ name, tick });
}

/**
 * Step two → step three: what the device can and cannot do.
 *
 * The payoff of the flow and the densest surface in the console — nine findings, each
 * a title, a verdict pill and a consequence, inside a region that scrolls — where a
 * `serious` `scrollable-region-focusable` violation lives if the region is not
 * focusable, which makes it the last entry anyone should be willing to leave out.
 */
async function toProbeStep(page: Page): Promise<void> {
  await toSecretStep(page);
  /* Copying the secret ticks the acknowledgement too, and would be the more faithful
     gesture — but the clipboard is a permission the browser may refuse, and a sweep
     that depends on one is a sweep that goes red for the environment. The checkbox
     reaches the same state with nothing environmental in the way. */
  await page.locator("dialog[open] .ack input[type=checkbox]").check();
  await page.locator("dialog[open] .dialog-foot .btn-primary").click();
}

/**
 * The device screen → the rotation's reveal.
 *
 * The same one-time value the wizard shows, reached by the other of the two
 * operations that mint one. `nas-unifi-1` is a discovered device whose driver cannot
 * push a secret, which is the case where the reveal is the only way in: Foyer
 * generated the value, the API is write-only, and without this dialog the device
 * could not be configured by any operator at all.
 */
async function toRotatedSecret(page: Page): Promise<void> {
  await page.locator(".card:has(p.secret-rotate-note) button").click();
  await expect(page.locator("dialog[open] .dialog-foot button.btn-danger")).toBeVisible();
  await page.locator("dialog[open] .dialog-foot button.btn-danger").click();
  await expect(
    page.locator("dialog[open] .secret"),
    "Rotating never reached the reveal, so this route scanned the detail screen twice.",
  ).toBeVisible();
}

/* ── the four authentication routes ───────────────────────────────────────── */

/**
 * `/sign-in`, `/operators`, `/audit` and `/password`: **51 scans across the four
 * routes, both schemes, both widths, plus seven driven states — zero violations
 * expected.** Any failure here is a regression in the change in front of you.
 *
 * **Three of the states below have no URL**, for the same reason the wizard's do: once
 * the gate fails the build on any violation anywhere, the surfaces the sweep cannot
 * reach are the only place a violation can survive. The refused sign-in, the create
 * dialog and the delete confirmation are each a document's worth of content that
 * `goto` alone never renders.
 *
 * **Two of them need a session, and it is a *staff* one.** The mock's role gate is live
 * whenever a session exists, so signing in as `le.minh.tuan` is what puts the `403`
 * rendering on screen — a `<tr class="empty-row">` carrying catalogue copy, which is a
 * different document from the table. It is also the state where a refusal can go
 * unannounced, and **axe reports it clean**: that is not a contradiction, it is the point. The gate
 * cannot see a refusal that is announced to nobody, which is why
 * `tests/admin-auth/role-gating.spec.ts` exists beside this file.
 *
 * The base `/operators` and `/audit` entries are deliberately **not** signed in.
 * `useCan` answers `true` with no session — argued at length in `admin/src/auth.tsx` —
 * so the markup is identical to an administrator's, it is the state the console boots
 * into while `REQUIRE_SESSION` is `false`, and it costs a round trip less.
 */

/** A refusal on screen, with no credential in the repository behind it. */
async function refusedSignIn(page: Page): Promise<void> {
  /* A disabled fixture operator: the answer must be indistinguishable from the one an
     unknown username gets, and it exercises the same `401` branch. The password is
     the harness pin, so the refusal is `enabled: false` rather than a wrong string. */
  await page.locator("#signin-username").fill("pham.thu.ha");
  await page.locator("#signin-password").fill(THROWAWAY_PASSWORD);
  await page.locator(".signin form button[type=submit]").click();
}

/** The create-operator dialog: five fields, three hints, and a select. */
async function operatorCreateDialog(page: Page): Promise<void> {
  await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
  await page.locator("main .page-head button").first().click();
}

/**
 * The delete confirmation — the `type-name` tier, which renders **no cancel button**
 * and a text field standing between the operator and an irreversible action.
 *
 * The row is found rather than assumed: the last enabled administrator's three controls
 * are `disabled`, by design, so `:not([disabled])` picks one of the others. Nothing is
 * confirmed, so nothing is deleted.
 */
async function operatorDeleteConfirmation(page: Page): Promise<void> {
  await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
  await page.locator(`${admin.rows} .row-actions button.btn-ghost:not([disabled])`).first().click();
}

/** The erasure's confirmation, opened on a MAC nobody has; nothing is confirmed. */
async function privacyEraseConfirmation(page: Page): Promise<void> {
  await expect(page.locator("main input[type=number]").first()).toBeVisible();
  await page.getByLabel("MAC address", { exact: true }).fill("02-00-5E-10-00-96");
  await page.getByRole("button", { name: "Erase…" }).click();
}

const ROUTES: RouteSpec[] = [
  /* The portal, in both languages: the copy differs, and so can the layout. */
  { app: "portal", url: `${PORTAL_URL}/en/`, ready: ".shell h1" },
  { app: "portal", url: `${PORTAL_URL}/en/voucher/`, ready: "input.code-input" },
  { app: "portal", url: `${PORTAL_URL}/en/account/`, ready: "input#password" },
  /*
   * `/connected/` is two documents, and both are swept.
   *
   * Besides the success page, it renders an honest not-connected variant for an
   * uncookied request or a reload after revocation — a real state a guest reaches,
   * which deserves sweeping — and the success page has to be earned.
   *
   * Retargeting the single entry at `.shell h1` would have made the suite green in
   * one line and quietly converted this route into a scan of the *failure* variant
   * only. Coverage shrinking silently while the dashboard goes green is the exact
   * failure this suite exists to prevent.
   */
  {
    app: "portal",
    url: `${PORTAL_URL}/en/connected/`,
    name: "/en/connected/ (no grant)",
    ready: "[role=alert]",
  },
  {
    app: "portal",
    url: `${PORTAL_URL}/en/connected/`,
    name: "/en/connected/ (after redemption)",
    ready: ".badge-ok",
    setup: redeemVoucher,
  },
  { app: "portal", url: `${PORTAL_URL}/en/problem/`, ready: "[role=alert]" },
  { app: "portal", url: `${PORTAL_URL}/vi/`, ready: ".shell h1" },
  { app: "portal", url: `${PORTAL_URL}/vi/voucher/`, ready: "input.code-input" },

  { app: "admin", url: `${ADMIN_URL}/`, ready: ".card-grid .card .metric-foot" },
  /*
   * The same route on a site with nothing in it — a *different* rendering, the way
   * `/attempts?q=zz-no-such-device` is, and the only one a stranger who has just
   * installed Foyer ever sees.
   *
   * It carries a section heading, two notices with actions and a list of what Foyer
   * cannot do, none of which exist on the row above: that site is configured, so the
   * panel is `null` there. Reached by a click rather than by a URL — the venue *is* in
   * the address, so `${ADMIN_URL}/?site=<id>` would work now, but the id has
   * to be looked up from the mock first and `after` is where a lookup can happen. The
   * click is one step where a URL would be two, and `setup` can express neither.
   */
  {
    app: "admin",
    url: `${ADMIN_URL}/`,
    name: "/ (a site with nothing in it)",
    ready: "section[data-first-run] .notice",
    after: async (page) => {
      await page.locator('.site-switch [aria-haspopup="menu"]').last().click();
      /* The empty site by its own emptiness, never by name: `helpers/mock.ts`'s
         `listNas` learned this lesson one layer down. `data-active` is on every item,
         so the one to press is the one whose site has no devices — asked of the mock
         through the page's own origin, which the Vite proxy forwards. */
      const empty = await page.evaluate(async () => {
        const sites = (await (await fetch("/api/v0/sites")).json()) as { id: string; name: string }[];
        for (const s of sites) {
          const nas = (await (await fetch(`/api/v0/nas?siteId=${s.id}`)).json()) as {
            items: unknown[];
          };
          if (nas.items.length === 0) return s.name;
        }
        return null;
      });
      if (!empty) throw new Error("no site in the mock is empty, so the panel is unreachable");
      await page.locator('[role="menuitemradio"]', { hasText: empty }).click();
    },
  },
  { app: "admin", url: `${ADMIN_URL}/sessions`, ready: "tbody[aria-busy=false]" },
  /*
   * The session detail dock — a surface a route table cannot reach on its own.
   *
   * The sweep walks a list of URLs and the dock needs a session id that only exists
   * once the list has rendered, so it is invisible to a route table by construction.
   * The `after` hook is the mechanism that closes it: it
   * clicks the first row, exactly as an operator would, rather than the list growing
   * a special case.
   *
   * Worth the entry: it is the densest surface in the admin — three figures with
   * their own freshness qualifiers, a nine-column table behind it, and a region that
   * scrolls. A scrollable region that cannot be focused is `serious`, and this hook
   * shape is what reaches it, as it does on the registration wizard.
   */
  {
    app: "admin",
    url: `${ADMIN_URL}/sessions`,
    name: "/sessions (detail dock open)",
    ready: '.detail-dock[role="complementary"]',
    after: async (page) => {
      await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
      await page.locator(admin.rows).first().click();
    },
  },
  { app: "admin", url: `${ADMIN_URL}/access-points`, ready: "tbody[aria-busy=false]" },
  { app: "admin", url: `${ADMIN_URL}/controllers`, ready: "tbody[aria-busy=false]" },
  /*
   * The controller registration dialog, and the probe result behind it.
   *
   * Two states, because they are two documents' worth of content: the form asks a
   * different credential per vendor, and the probe renders a findings list plus an
   * urgent notice for the presence check. Neither is reachable from a URL — the same
   * gap the registration wizard's entries close, for the same reason.
   */
  {
    app: "admin",
    url: `${ADMIN_URL}/controllers`,
    name: "/controllers (add dialog)",
    ready: "dialog[open] #ctl-vendor",
    after: async (page) => {
      await page.locator("main .page-head button").first().click();
      await expect(page.locator("dialog[open] #ctl-vendor")).toBeVisible();
      /* Omada, because it is the branch with the extra field and the credential
         warning — a state no default sweep would otherwise scan. */
      await page.locator("dialog[open] #ctl-vendor").selectOption("omada");
      await expect(page.locator("dialog[open] #ctl-cid")).toBeVisible();
    },
  },
  {
    app: "admin",
    url: `${ADMIN_URL}/controllers`,
    name: "/controllers (probe findings)",
    ready: "dialog[open] ul.findings li",
    after: async (page) => {
      await expect(page.locator(admin.tbody).first()).toHaveAttribute("aria-busy", "false");
      await page.locator(`${admin.rows} button`).first().click();
      await expect(page.locator("dialog[open]")).toBeVisible();
      /*
       * **Named, not counted.** `.dialog-foot button` with `.first()` would be a lock
       * on the *order of a footer* rather than on the control it means, and the next
       * button added to the foot (`Close`, `Probe`, `Discover…`) would make this step
       * *close* the dialog and wait for a findings list inside an element that was
       * gone — reported as a setup timeout, which means **nothing here is scanned**
       * and the route silently stops being covered rather than going red on a
       * violation.
       *
       * A change to this dialog's foot also concerns `dialog-focus-return.spec.ts`
       * (its `withoutCancel` list); the two are found together.
       *
       * The accessible name is exactly "Probe": the icon beside the label is
       * `aria-hidden`, and the pending label is a different word ("Probing…").
       */
      await page
        .locator("dialog[open] .dialog-foot")
        .getByRole("button", { name: "Probe", exact: true })
        .click();
      await expect(page.locator("dialog[open] ul.findings li").first()).toBeVisible();
    },
  },
  /*
   * The registration wizard, all three steps.
   *
   * Step one has a URL, partly for this reason: `/access-points/new` renders the list with the dialog open on step one,
   * so it costs a single row here. Steps two and three have no URL and must not —
   * the second renders a shared secret that exists exactly once, and a URL that
   * reproduced it would itself be the defect. That is what `after` is for, and the
   * three rows together are the argument: a surface the sweep cannot reach is a
   * surface where a gate that fails the build on any violation protects nothing.
   *
   * Each of the last two registers a device, which is the same stateful-mock cost
   * `redeemVoucher` above already pays. `afterEach` takes them back out.
   */
  {
    app: "admin",
    url: `${ADMIN_URL}/access-points/new`,
    name: "/access-points/new (wizard · details)",
    ready: "dialog[open] .wizard-trail",
  },
  {
    app: "admin",
    url: `${ADMIN_URL}/access-points/new`,
    name: "/access-points/new (wizard · shared secret)",
    ready: "dialog[open] .secret",
    after: toSecretStep,
  },
  {
    app: "admin",
    url: `${ADMIN_URL}/access-points/new`,
    name: "/access-points/new (wizard · probe findings)",
    /* `ul.findings li` and not `.findings`: the busy state renders a skeleton with
       the same class on a `div`, so the looser selector would let axe scan the
       placeholder and report a clean result for a panel with no content in it. */
    ready: "dialog[open] ul.findings li",
    after: toProbeStep,
  },
  /*
   * The rotation's reveal — **the row that was missing, and the one that matters.**
   *
   * It renders a value no operation returns, it has no exit that is not forward, a
   * checkbox standing between the operator and the rest of the console, and a
   * primary button disabled until that checkbox is ticked. It is the wizard's
   * secret step in every respect that an accessibility gate can see, and it had
   * **no row here at all** — so the surface where the defect lived was the one
   * surface axe was never shown.
   *
   * There is no URL for it, for the reason the wizard's step two has none: a URL
   * that reproduced a one-time value would itself be the defect. `after` is the only
   * way in.
   */
  {
    app: "admin",
    url: `${ADMIN_URL}/access-points/nas-unifi-1`,
    name: "/access-points/:id (rotated shared secret)",
    ready: "dl.kv",
    after: toRotatedSecret,
  },
  { app: "admin", url: `${ADMIN_URL}/access-points/nas-unifi-1`, ready: "dl.kv" },
  { app: "admin", url: `${ADMIN_URL}/access-points/nas-omada-1`, ready: "dl.kv" },
  { app: "admin", url: `${ADMIN_URL}/vouchers`, ready: "tbody[aria-busy=false]" },
  /*
   * The batch create dialog, driven into the state where its warnings are on screen.
   *
   * The dialog is where a batch's limits are set, and its notices appear only once a
   * rate or an allowance has been typed — so scanning the route alone scans a form
   * with half its content missing. Typing is the only way in; there is no URL that
   * opens it pre-filled, and inventing one to make it sweepable would be a product
   * change made for a test.
   */
  {
    app: "admin",
    url: `${ADMIN_URL}/vouchers`,
    name: "/vouchers (create dialog, limits stated)",
    ready: "dialog[open] #b-data",
    after: async (page) => {
      await page.locator("main .page-head button").first().click();
      await expect(page.locator("dialog[open] #b-data")).toBeVisible();
      await page.locator("dialog[open] #b-data").fill("500");
      const down = page.locator("dialog[open] #b-down");
      if (await down.count()) await down.fill("20000");
      /* The notices are what this entry exists for. If none renders, the fixture
         site no longer contains a device that cannot enforce a limit and this route
         is scanning the plain form — which the row above already covers. */
      await expect(page.locator("dialog[open] .notice").first()).toBeVisible();
    },
  },
  { app: "admin", url: `${ADMIN_URL}/vouchers/batch-daily`, ready: "tbody[aria-busy=false]" },
  { app: "admin", url: `${ADMIN_URL}/accounts`, ready: "tbody[aria-busy=false]" },
  /* The portal settings screen, one entry per tab — each tab is a different form,
     and a sweep of the first would never see the other two. `ready` names the last
     thing each tab renders, because the form renders in stages and axe would
     otherwise sweep a page that is still one card. */
  { app: "admin", url: `${ADMIN_URL}/portal`, ready: ".block-list" },
  { app: "admin", url: `${ADMIN_URL}/portal?tab=signin`, name: "/portal (sign-in tab)", ready: "#network-name" },
  { app: "admin", url: `${ADMIN_URL}/portal?tab=terms`, name: "/portal (terms tab)", ready: "textarea#terms-en" },
  /*
   * The page builder with a block open and the kind menu showing — the two states
   * that carry most of its controls, and which no URL reaches.
   */
  {
    app: "admin",
    url: `${ADMIN_URL}/portal`,
    name: "/portal (a block open, the block menu showing)",
    ready: ".block-list",
    after: async (page) => {
      await page.locator(".block-summary").first().click();
      await expect(page.locator(".block-editor")).toBeVisible();
      await page.getByRole("button", { name: /Add a block/ }).click();
      await expect(page.locator(".kind-menu")).toBeVisible();
    },
  },
  { app: "admin", url: `${ADMIN_URL}/attempts`, ready: "tbody[aria-busy=false]" },
  /*
   * The attempts screen with nothing in it — a *different* rendering, not an empty
   * version of the one above. The blind-spot panel moves ahead of the table and the
   * empty state replaces the rows, so the sweep would never see either of them from
   * the unfiltered route. A query that matches nothing is the cheapest way in.
   */
  {
    app: "admin",
    url: `${ADMIN_URL}/attempts?q=zz-no-such-device`,
    name: "/attempts (nothing matched)",
    ready: ".blind-spots",
  },
  { app: "admin", url: `${ADMIN_URL}/garden`, ready: "tbody[aria-busy=false]" },
  /*
   * The design-system gallery. Not in `NAV` and never offered to an operator, but it
   * is the review surface for a design change and the regression surface for
   * contrast — which makes it the one page where an accessibility regression would
   * be introduced and then used to sign off the change that introduced it.
   *
   * The `ready` selector names the **dark** panel deliberately. The route is lazily
   * loaded and renders both schemes as sibling panels, so a selector matching the
   * light one can resolve before the second has painted, and axe would scan half a
   * page. One visit covers both themes in a single load, which is why this entry is
   * cheap enough to be worth having.
   */
  { app: "admin", url: `${ADMIN_URL}/design`, ready: '.gal-theme[data-scheme="dark"] .pill' },

  /* ── the four authentication routes, and their four driven states ────────── */

  { app: "admin", url: `${ADMIN_URL}/sign-in`, ready: ".signin form button[type=submit]" },
  {
    app: "admin",
    url: `${ADMIN_URL}/sign-in`,
    name: "/sign-in (credentials refused)",
    /* `[role=alert]` inside the card: the refusal notice, which is the whole of what
       this state adds. A `.signin` selector would be satisfied by the plain form. */
    ready: ".signin [role=alert]",
    after: refusedSignIn,
  },
  { app: "admin", url: `${ADMIN_URL}/operators`, ready: "tbody[aria-busy=false]" },
  {
    app: "admin",
    url: `${ADMIN_URL}/operators`,
    name: "/operators (create dialog)",
    ready: "dialog[open] #operator-password",
    after: operatorCreateDialog,
  },
  {
    app: "admin",
    url: `${ADMIN_URL}/operators`,
    name: "/operators (delete confirmation)",
    /* The field, not the dialog: `ConfirmDialog` is mounted always and toggled with
       `open`, so a `dialog` selector matches the closed one and axe would scan a
       confirmation with nothing in it. */
    ready: "dialog[open] input[type=text]",
    after: operatorDeleteConfirmation,
  },
  {
    app: "admin",
    url: `${ADMIN_URL}/operators`,
    name: "/operators (staff session · refused)",
    ready: "tbody tr.empty-row",
    setup: async (page) => void (await signInAs(page.context(), STAFF)),
  },
  { app: "admin", url: `${ADMIN_URL}/audit`, ready: "tbody[aria-busy=false]" },
  {
    app: "admin",
    url: `${ADMIN_URL}/audit`,
    name: "/audit (staff session · refused)",
    ready: "tbody tr.empty-row",
    setup: async (page) => void (await signInAs(page.context(), STAFF)),
  },
  /* Retention periods and erasure: five labelled number fields with hints, a
     radio group, and the erasure's confirmation — the one irreversible action on the
     page, so its dialog is swept open. */
  { app: "admin", url: `${ADMIN_URL}/privacy`, ready: "main input[type=number]" },
  {
    app: "admin",
    url: `${ADMIN_URL}/privacy`,
    name: "/privacy (erase confirmation)",
    ready: "dialog[open] .btn-danger, dialog[open] button",
    after: privacyEraseConfirmation,
  },
  /*
   * `/password` is two documents and both are swept — the same argument as
   * `/en/connected/`. Signed out it renders a notice and a link, because there is no
   * password of *yours* to change until there is a you; signed in it renders the form,
   * which is the half carrying the fields, the hints and the length rule.
   *
   * Retargeting one entry at `h1.page-title` would have covered both in one line and
   * quietly converted this route into a scan of whichever variant the sweep happened
   * to reach. **This is also one of the four entries a blanket `storageState` would
   * silently sign in** — see `helpers/session.ts`.
   */
  {
    app: "admin",
    url: `${ADMIN_URL}/password`,
    name: "/password (signed out)",
    ready: ".page .notice a[href='/sign-in']",
  },
  {
    app: "admin",
    url: `${ADMIN_URL}/password`,
    name: "/password (signed in)",
    ready: "#current-password",
    setup: async (page) => void (await signInAs(page.context(), ADMINISTRATOR)),
  },
];

interface Finding {
  scheme: string;
  route: string;
  id: string;
  impact: string;
  nodes: number;
  help: string;
  /** A few offending selectors, so the summary is actionable without opening a trace. */
  sample: string[];
}

const findings: Finding[] = [];

for (const route of ROUTES) {
  const label = route.name ?? new URL(route.url).pathname;

  test(`axe · ${route.app} ${label}`, async ({ page }, testInfo) => {
    const scheme = testInfo.project.use.colorScheme ?? "light";

    /*
     * Getting to the thing being scanned, in one function so the retry below reaches
     * the *same* state rather than an earlier one. For a route with an `after` hook
     * the navigation alone lands on step one, and a retry that only re-issued
     * `goto` would scan the wrong surface and report it clean.
     */
    const reach = async () => {
      await page.goto(route.url);
      if (route.after) await route.after(page);
      await expect(page.locator(route.ready).first()).toBeVisible();
      await page.waitForLoadState("load");
    };

    if (route.setup) await route.setup(page);
    await reach();

    /*
     * axe injects and evaluates in the page, which a navigation invalidates. In
     * development that happens for a reason unrelated to the product: editing a
     * portal file triggers a full reload, because the portal is server-rendered and
     * there is nothing on the client to patch. One retry, then it is a real failure.
     */
    let results;
    try {
      results = await axeFor(page).analyze();
    } catch (err) {
      if (!/Execution context was destroyed|navigation/i.test(String(err))) throw err;
      await reach();
      results = await axeFor(page).analyze();
    }

    /* The only hard assertion: axe actually ran against a real page. A route that
       404s or renders nothing would otherwise report zero violations and look clean. */
    expect(
      results.passes.length + results.violations.length + results.incomplete.length,
      `axe evaluated no rules on ${route.url} — the page probably did not render.`,
    ).toBeGreaterThan(0);

    for (const v of results.violations) {
      findings.push({
        scheme: String(scheme),
        route: `${route.app} ${label}`,
        id: v.id,
        impact: v.impact ?? "unknown",
        nodes: v.nodes.length,
        help: v.help,
        sample: v.nodes.slice(0, 3).map((n) => n.target.join(" ")),
      });
    }

    await testInfo.attach(`axe-${route.app}${label.replace(/\//g, "_")}-${scheme}.json`, {
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
      const lines = results.violations
        .map((v) => `      ${v.impact ?? "?"} · ${v.id} × ${v.nodes.length} — ${v.help}`)
        .join("\n");
      console.log(`  a11y ${scheme} ${route.app} ${label}\n${lines}`);
    }

    expect(
      results.violations.map((v) => `${v.id} × ${v.nodes.length}`),
      `Accessibility violations on ${route.url} (${scheme}).\n` +
        "  Both applications are measured at zero WCAG violations in both colour " +
        "schemes, so this is a regression in the change in front of you, not " +
        "inherited debt. The full node list is attached to this test in the HTML " +
        "report. If it is a contrast finding, check the token before you check the " +
        "rule — the last two rounds of these were one token value each.",
    ).toEqual([]);
  });
}

/**
 * Whatever a route registered, take back out.
 *
 * In `afterEach` rather than at the end of the test body so it also runs when the
 * scan failed — a red route is exactly the run whose leftovers would otherwise
 * survive, and the next person to look at the access-point list would be reading a
 * world assembled by failures.
 *
 * The deletion is **asserted**, not attempted. A test may not be trusted to be tidy;
 * a cleanup nobody checks is the same promise with a longer feedback loop.
 */
/* Whatever a route signed in as, sign out of. Playwright gives every test a fresh
   context so nothing leaks between them — this is for the *mock*, whose session store
   is a `Map` in memory shared with everything else running on the machine. */
test.afterEach(async ({ context }) => {
  await signOut(context);
});

test.afterEach(async ({ request }) => {
  while (registered.length > 0) {
    const { name, tick } = registered.pop()!;
    await expectNasRemoved(request, name, tick);
  }
});

test.afterAll(async ({}, testInfo) => {
  if (findings.length === 0) {
    console.log(`\n  a11y (${testInfo.project.name}): no WCAG violations found.\n`);
    return;
  }

  /*
   * Reaching here at all means at least one route already failed — nothing is
   * reported-only any more. The summary survives the gate because it is the one view
   * that answers "is this one token or fifty rules", which is the question that
   * decides how the fix is approached — contrast findings across many routes are
   * often a single token value.
   */
  const byRule = new Map<string, { impact: string; nodes: number; routes: Set<string> }>();
  for (const f of findings) {
    const entry = byRule.get(f.id) ?? { impact: f.impact, nodes: 0, routes: new Set<string>() };
    entry.nodes += f.nodes;
    entry.routes.add(f.route);
    byRule.set(f.id, entry);
  }

  const rows = [...byRule.entries()]
    .sort((a, b) => b[1].nodes - a[1].nodes)
    .map(
      ([id, e]) =>
        `    ${e.impact.padEnd(9)} ${id.padEnd(34)} ${String(e.nodes).padStart(4)} nodes  ` +
        `${e.routes.size} route(s)`,
    );

  const summary =
    `\n  a11y summary — ${testInfo.project.name}\n` +
    `    ${findings.length} violation instances, ${byRule.size} distinct rules\n\n` +
    `${rows.join("\n")}\n\n` +
    `  Every route is gated. The tests above have failed; this is the shape of what\n` +
    `  broke, so you can tell one bad token from fifty bad rules.\n`;

  console.log(summary);
  mkdirSync(OUT_DIR, { recursive: true });
  writeFileSync(`${OUT_DIR}a11y-${testInfo.project.name}.json`, JSON.stringify(findings, null, 2));
});
