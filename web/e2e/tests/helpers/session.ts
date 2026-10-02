import { expect, type APIRequestContext, type BrowserContext, type Page } from "@playwright/test";
import { MOCK_ADMIN_PASSWORD, MOCK_URL } from "./env.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  Signing the suite in — and the one fact that makes it possible at all
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **One cookie reaches both origins, because cookies are not port-scoped.**
 *
 * The console is served by Vite on `:5173` and the mock answers on `:5170`. Two
 * origins, by every rule a browser applies to script, storage, CSP and CORS — and
 * *not* by the rule it applies to cookies. RFC 6265 §8.5 is explicit that the port is
 * not part of a cookie's scope: a cookie set for host `localhost` is sent to every
 * port on `localhost`. So `Set-Cookie: foyer_admin_session=…; Path=/; HttpOnly;
 * SameSite=Lax` — **with no `Domain` attribute**, which makes it host-only for
 * `localhost` — is one session for the console, for the Vite proxy, and for the nine
 * direct `APIRequestContext` calls in `helpers/mock.ts` that talk to `:5170` without
 * a browser in the loop.
 *
 * Driven, not reasoned about (`curl` against the running stack):
 *
 * ```
 *   POST http://localhost:5173/api/v0/auth/session   (through the Vite proxy)
 *     ← 200  set-cookie: foyer_admin_session=…; Path=/; HttpOnly; SameSite=Lax
 *   GET  http://localhost:5170/api/v0/operators      (straight at the mock)
 *     ← 403  {"type":"/problems/forbidden",…,"params":{"required":"operators:manage"}}
 * ```
 *
 * The `403` is the proof: the mock could only refuse *on role* because it recognised
 * the session that the other port handed out.
 *
 * **This is what silently breaks** if anyone gives the cookie an explicit `Domain`,
 * moves the mock to a different host, or serves the console and the API from
 * genuinely different hostnames. It would not break loudly — `helpers/mock.ts` would
 * simply start behaving as an anonymous caller again, which today is indistinguishable
 * from a signed-in one because `REQUIRE_SESSION` is `false`. `session-spans-both-
 * origins.spec.ts` is the lock; this comment is why it exists.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *  GROUNDWORK FOR `REQUIRE_SESSION`, AND WHY THE SWITCH IS NOT THROWN HERE
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `web/mock/src/server.ts` carries the switch and the conditions for throwing it:
 * the console must call `GET /auth/session` on boot, route to a sign-in screen on
 * `401`, and **the end-to-end suite must sign in before it browses**. The console does
 * the first two. The suite does not yet do the third, and this file is the half of it
 * that can be built without touching two hundred tests: `117` admin `test()` calls
 * across `26` spec files browse with no session at all, and flipping the constant
 * converts one missing feature into that many failures.
 *
 * When somebody does throw it, the shape is already decided and the traps are known:
 *
 *  1. **Sign in fresh on every run.** `adminSessions` in `web/mock/src/state.ts` is a
 *     `Map` in the mock's memory. A `storageState` JSON checked into the repository —
 *     or even carried between runs in `test-results/` — names a session id that no
 *     longer exists, and every request under it answers `401`. So a `globalSetup` that
 *     *writes* the state each run, never a file anybody edits.
 *  2. **Two states, not one.** A single administrator `storageState` at project level
 *     makes the whole suite administrator-only and puts the `403` path out of reach —
 *     and that path is half of what `tests/admin-auth/role-gating.spec.ts` measures.
 *     `ADMINISTRATOR` and `STAFF` below are the two.
 *  3. **Four entries must stay outside any blanket state**: `/sign-in`, `/sign-in`
 *     after a refusal, `/password`'s signed-out variant, and the staff `403` scans.
 *     A project-level `storageState` converts all four into signed-in renderings
 *     without a single assertion changing — coverage shrinking while the dashboard
 *     goes green, which is the failure the `/en/connected/` entry in
 *     `tests/a11y/routes.spec.ts` argues against at length.
 *
 * Until then, every caller here signs in **explicitly, per test**, which is the shape
 * that survives the switch being thrown: a test that says who it is stays correct
 * under enforcement, and a test that says nothing is the one that has to change.
 */

/**
 * The two fixture operators this suite signs in as.
 *
 * **No password in the repository, and that is a rule rather than an oversight.**
 * `docs/rules/security.md` §7 forbids a static credential here, so no fixture
 * operator carries one: the mock *generates* the first administrator's password at
 * world-build time the way first boot does, and every fixture operator is seeded
 * with it. `FOYER_MOCK_ADMIN_PASSWORD` pins that generated value so a scripted
 * driver can sign in without reading a banner — see `MOCK_ADMIN_PASSWORD` in
 * `helpers/env.ts`, which `playwright.config.ts` passes to the mock it starts.
 *
 * **The mock does not accept any password** — which is why the pin exists at all.
 *
 * Usernames rather than ids, because that is what the sign-in form takes.
 */
export const ADMINISTRATOR = "dang.quoc.viet";
export const STAFF = "le.minh.tuan";

/**
 * The password this suite types, and the one the mock was started with.
 *
 * **Not a credential.** It is the value `playwright.config.ts` pins on the mock
 * through `FOYER_MOCK_ADMIN_PASSWORD`, so the mock's generated password is
 * replaced by a known one for the length of the run and by nothing else.
 * Re-exported under its old name because thirteen call sites use it; the single
 * definition lives in `helpers/env.ts` beside the config that passes it.
 */
export const THROWAWAY_PASSWORD = MOCK_ADMIN_PASSWORD;

export interface MockAuthSession {
  operator: { id: string; username: string; role: "administrator" | "staff"; enabled: boolean };
  permissions: string[];
  csrfToken: string;
  /**
   * This operator still holds the credential the install ships with, and the server
   * refuses everything but signing out and changing it until they do.
   *
   * Required rather than optional, matching the contract: a caller that can treat it
   * as absent is one that can forget the state exists, which is how a console renders
   * screens the server will refuse every call from.
   */
  mustChangePassword: boolean;
}

/**
 * Signs the browser context in, by talking to the mock rather than by driving the
 * form.
 *
 * `context.request` shares its cookie jar with the browser context, so the session
 * cookie the mock sets is the one every subsequent `page.goto` carries. Straight at
 * `MOCK_URL` rather than through the console's own origin, deliberately: it is the
 * cheapest possible demonstration of the port-independence this file rests on, and it
 * keeps the Vite proxy out of a failure path that has nothing to do with it.
 *
 * The form is driven by `tests/admin-auth/sign-in.spec.ts` — once, where the flow is
 * the subject. Everywhere else, signing in is setup and driving three fields for it
 * would make every one of those tests fail when the sign-in form changes.
 */
export async function signInAs(
  context: BrowserContext,
  username: string,
): Promise<MockAuthSession> {
  const res = await context.request.post(`${MOCK_URL}/api/v0/auth/session`, {
    data: { username, password: THROWAWAY_PASSWORD },
  });
  expect(
    res.status(),
    `Signing in as ${username} was refused. The mock verifies the password, ` +
      "so this is one of two things and they are worth telling apart. " +
      "Either the mock this suite reached was not started by it — `reuseExistingServer` " +
      "is on outside CI, and a mock left running by another checkout generated its own " +
      "password — or `buildOperators` in `web/fixtures/src/index.ts` no longer carries " +
      "this username, or no longer has it enabled. Check who owns port 5170 first.",
  ).toBe(200);
  return (await res.json()) as MockAuthSession;
}

/**
 * Ends the session this context holds, and forgets the cookie.
 *
 * `DELETE /auth/session` answers `204` whether or not there was a session, so this is
 * safe in an `afterEach` that runs after a failure. Worth doing even though Playwright
 * gives every test a fresh context: `adminSessions` is a `Map` in the mock's memory
 * and the mock is shared with every other run on this machine.
 */
export async function signOut(context: BrowserContext): Promise<void> {
  await context.request.delete(`${MOCK_URL}/api/v0/auth/session`);
  await context.clearCookies();
}

/** Who the *server* thinks is signed in, asked directly. `null` for nobody. */
export async function whoAmI(request: APIRequestContext): Promise<MockAuthSession | null> {
  const res = await request.get(`${MOCK_URL}/api/v0/auth/session`);
  if (res.status() === 401) return null;
  expect(res.status(), "GET /auth/session answered neither 200 nor 401.").toBe(200);
  return (await res.json()) as MockAuthSession;
}

/**
 * Waits until the console's boot probe has answered and the shell reflects it.
 *
 * `auth.tsx` renders **ungated for one request** on a cold load — `usePermitted`
 * returns `true` for a missing session, which is the honest answer while nothing is
 * known — so a role assertion taken before the probe lands measures the window rather
 * than the product. The sidebar is the handle because it is the one part of the shell
 * that is a direct function of `AuthSession.permissions`.
 */
export async function shellSettled(page: Page): Promise<void> {
  await expect(
    page.locator(".sidebar-who"),
    "The sidebar never named an operator, so `GET /auth/session` has not answered " +
      "and every permission on screen is still the ungated default rather than this " +
      "operator's. `AccountArea` in `admin/src/App.tsx` renders `.sidebar-who` only " +
      "for a known session; the signed-out branch renders a link instead.",
  ).toHaveCount(1);
}
