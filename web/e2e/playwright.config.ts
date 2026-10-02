import { defineConfig, devices } from "@playwright/test";
import { ADMIN_URL, MOCK_ADMIN_PASSWORD, MOCK_URL, PORTAL_URL } from "./tests/helpers/env.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  WHAT THIS SUITE CAN AND CANNOT PROVE
 * ─────────────────────────────────────────────────────────────────────────────
 *
 *  **Playwright cannot emulate the Captive Network Assistant.** Portal E2E going
 *  green means nothing about real phones. This suite prevents regressions in what
 *  is already understood; it is not a substitute for testing the portal on a real
 *  device, inside a real CNA.
 *
 *  See `docs/rules/testing.md` and `docs/protocol-reality.md` §"Playwright
 *  still cannot prove any of this". The same statement is repeated at the top of
 *  `tests/portal/navigation-not-xhr.spec.ts`, next to the test it qualifies.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 */

/*
 * **Imported, not redeclared.** A separate copy of the three URLs here can disagree
 * with the ports the `command`s below start the servers on: with the mock on 5670,
 * the portal on 5671 and the admin on 5673 while the URLs default to 5170 / 5171 /
 * 5173, Playwright starts all three servers, polls ports nothing is listening on,
 * and fails with `Timed out waiting 60000ms from config.webServer` — **from a clean
 * checkout, on every run, with no test having executed.** `FOYER_*_URL` variables set
 * in one shell hide the mismatch for whoever set them, and `tests/helpers/mock.ts`,
 * which reaches the mock directly through `helpers/env.ts`, surfaces it as
 * `ECONNREFUSED ::1:5170`.
 *
 * One declaration, in `tests/helpers/env.ts`, which the tests already read.
 */
/* The port a server is STARTED on, read out of the URL it is CHECKED on, so the two
   cannot part company again and an override moves both. */
const portOf = (url: string) => new URL(url).port;

export default defineConfig({
  testDir: "./tests",
  outputDir: "./test-results",

  /*
   * One worker, no parallelism, and it is not a performance oversight.
   *
   * The mock is stateful on purpose (see the frontend design): redeeming a
   * voucher consumes it, revoking one ends a session. Two workers racing for the
   * same "first unused voucher" would produce a flake that looks like a product
   * bug. The suite is small; determinism is worth more than the seconds.
   */
  workers: 1,
  fullyParallel: false,

  /* A test that only passes on the second attempt is a test that is telling you
     something. Locally: no retries. On CI: one, to distinguish flake from break. */
  retries: process.env.CI ? 1 : 0,
  forbidOnly: Boolean(process.env.CI),

  timeout: 30_000,
  expect: { timeout: 7_000 },

  reporter: process.env.CI
    ? [["list"], ["html", { open: "never" }], ["github"]]
    : [["list"], ["html", { open: "never" }]],

  use: {
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "off",
    /* The guest portal is designed for a phone held one-handed; the admin is a
       desktop tool. Each project overrides this where it matters. */
    ...devices["Desktop Chrome"],
  },

  projects: [
    {
      name: "portal",
      testMatch: /portal\/.*\.spec\.ts/,
      use: {
        baseURL: PORTAL_URL,
        colorScheme: "dark",
        /* 390×844 — an iPhone-class viewport. The macOS CNA is 900×550 pt and the
           layout is centred at 420 px above mobile, so this is the narrow case. */
        viewport: { width: 390, height: 844 },
      },
    },
    {
      name: "admin",
      testMatch: /admin\/.*\.spec\.ts/,
      use: { baseURL: ADMIN_URL, colorScheme: "dark", viewport: { width: 1440, height: 900 } },
    },
    /*
     * The admin at phone width — a second layout, not a smaller one.
     *
     * Below 900px the sidebar is a header, the nine destinations are in a drawer, and
     * every table drops most of its columns into a per-row disclosure. None of that
     * markup exists in the `admin` project's 1440px renderings, so none of it can be
     * covered there. Its own directory rather than a viewport override inside
     * `tests/admin/`, so the two widths cannot be confused for one another in a report
     * and so `--project` selects the layout under test.
     *
     * `tests/admin-narrow/` does not contain the substring `admin/`, so the project
     * above does not also claim these files. That is load-bearing: it would run them
     * at 1440 where there is no drawer to open.
     */
    {
      name: "admin-narrow",
      testMatch: /admin-narrow\/.*\.spec\.ts/,
      use: { baseURL: ADMIN_URL, colorScheme: "dark", viewport: { width: 390, height: 844 } },
    },
    /*
     * ── the four authentication routes ────────────────────────────────────────
     *
     * `/sign-in`, `/operators`, `/audit` and `/password`, in their own directory
     * rather than in `tests/admin/`, and **no `storageState` here**.
     *
     * These are the only specs in the suite that care *who is signed in*, and half of
     * what they measure is a refusal: a staff session opening `/operators` gets a real
     * `403` from the mock, whose role gate is live whenever a session exists. A
     * project-level `storageState` — the obvious way to sign a project in — would make
     * every test in it the same operator and put that half out of reach. So each test
     * signs itself in, through `helpers/session.ts`, and says which operator it is.
     *
     * That is also the shape that survives `REQUIRE_SESSION` being thrown: a test that
     * states its identity stays correct under enforcement, and one that states nothing
     * is the one that has to change. `helpers/session.ts` carries the whole plan.
     *
     * `admin-auth/` does not contain the substring `admin/`, so the `admin` project
     * above does not also claim these files — the same load-bearing naming as
     * `admin-narrow/`, and the reason there is a comment about it rather than a
     * coincidence.
     *
     * `[^/]+` and not `.*`: it must not reach into `admin-auth/narrow/`, which is the
     * project below and runs at a different width.
     */
    {
      name: "admin-auth",
      testMatch: /admin-auth\/[^/]+\.spec\.ts/,
      use: { baseURL: ADMIN_URL, colorScheme: "dark", viewport: { width: 1440, height: 900 } },
    },
    /*
     * The same four routes at phone width.
     *
     * `responsive-shell` pins the nine `NAV` destinations at 390px and cannot pin
     * these: `/operators` and `/audit` are administrator-only and were built after it,
     * and **`/sign-in` renders outside `Shell` entirely** — no grid, no sidebar, no
     * drawer — so it is a genuinely new case for that lock rather than a thirteenth
     * instance of the same one. Its sabotage has to be a different stylesheet, which is
     * the clearest evidence that it is a different layout.
     *
     * A subdirectory rather than a sibling of `admin-auth/`, so one directory holds
     * everything about these four routes and the width is still what `--project`
     * selects.
     */
    {
      name: "admin-auth-narrow",
      testMatch: /admin-auth\/narrow\/.*\.spec\.ts/,
      use: { baseURL: ADMIN_URL, colorScheme: "dark", viewport: { width: 390, height: 844 } },
    },
    /* The accessibility sweep runs the same routes twice, once per colour scheme.
       A contrast failure that only exists in one theme is the normal case, not the
       exotic one — only the dark theme has been reviewed so far. */
    {
      name: "a11y-dark",
      testMatch: /a11y\/.*\.spec\.ts/,
      use: { colorScheme: "dark", viewport: { width: 1440, height: 900 } },
    },
    {
      name: "a11y-light",
      testMatch: /a11y\/.*\.spec\.ts/,
      use: { colorScheme: "light", viewport: { width: 1440, height: 900 } },
    },
    /*
     * The same sweep at phone width, and **one scheme rather than two**.
     *
     * The pair above exists because contrast failures are theme-specific and a suite
     * that scanned one theme would certify half the product. What is new below 900px
     * is layout — which elements exist, how they nest, what has a name, what can be
     * reached — and none of those answers change with the palette. Both schemes
     * already run over these same routes at desk width. See the header of
     * `tests/a11y-narrow/routes.spec.ts` before adding an `a11y-narrow-light`.
     *
     * `a11y-narrow/` deliberately does not contain the substring `a11y/`, so the two
     * projects above do not also claim this file and try to open a drawer at 1440px.
     */
    {
      name: "a11y-narrow",
      testMatch: /a11y-narrow\/.*\.spec\.ts/,
      use: { colorScheme: "dark", viewport: { width: 390, height: 844 } },
    },
  ],

  /*
   * The suite starts its own stack rather than assuming one exists.
   *
   * `reuseExistingServer` is on outside CI because the three dev servers are
   * normally already running from `pnpm dev` in `web/` — attaching to them is both
   * faster and closer to what a developer is actually looking at.
   */
  webServer: [
    {
      command: `PORT=${portOf(MOCK_URL)} PORTAL_ORIGIN=${PORTAL_URL} pnpm -F @foyer/mock dev`,
      url: `${MOCK_URL}/health`,
      cwd: "..",
      /*
       * **The mock verifies the sign-in password.** It
       * generates one per process the way first boot does and prints it in a
       * banner, which a scripted driver cannot read, so the suite pins it here and
       * submits the same value from `helpers/session.ts`. One constant, in
       * `helpers/env.ts`, so the two cannot drift.
       *
       * `env` only reaches a mock **this config starts**. With
       * `reuseExistingServer` on outside CI, a mock already listening on that port keeps
       * whatever password it generated and every sign-in here is refused — loudly,
       * at the first assertion in `signInAs`, which is the intended failure rather
       * than a silent administrator session against somebody else's tree.
       */
      env: { FOYER_MOCK_ADMIN_PASSWORD: MOCK_ADMIN_PASSWORD },
      reuseExistingServer: !process.env.CI,
      stdout: "pipe",
      stderr: "pipe",
      timeout: 60_000,
    },
    {
      command: `MOCK_ORIGIN=${MOCK_URL} pnpm -F @foyer/portal exec vite --port ${portOf(PORTAL_URL)} --strictPort`,
      url: PORTAL_URL,
      cwd: "..",
      reuseExistingServer: !process.env.CI,
      stdout: "pipe",
      stderr: "pipe",
      timeout: 120_000,
    },
    {
      command: `MOCK_ORIGIN=${MOCK_URL} pnpm -F @foyer/admin exec vite --port ${portOf(ADMIN_URL)} --strictPort`,
      url: ADMIN_URL,
      cwd: "..",
      reuseExistingServer: !process.env.CI,
      stdout: "pipe",
      stderr: "pipe",
      timeout: 120_000,
    },
  ],
});
