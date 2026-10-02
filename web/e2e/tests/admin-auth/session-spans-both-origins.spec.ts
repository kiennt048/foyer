import { expect, test } from "@playwright/test";
import { ADMIN_URL, MOCK_URL } from "../helpers/env.js";
import { STAFF, THROWAWAY_PASSWORD, signOut } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  One session reaches both origins — **groundwork for `REQUIRE_SESSION`**
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The mock's `REQUIRE_SESSION` is `false` and this file does not throw it: ~117
 * admin `test()` calls across 26 spec files browse with no session at all, and flipping
 * the constant converts one missing feature into two hundred failures. The whole
 * plan is written down at the top of `helpers/session.ts`.
 *
 * **This file locks the non-obvious half of that plan**, which is what makes a single
 * `storageState` viable at all:
 *
 * > The console is served by Vite on `:5173` and the mock answers on `:5170`. Two
 * > origins by every rule a browser applies to script, storage, CSP and CORS — and
 * > **not** by the rule it applies to cookies. RFC 6265 §8.5: the port is not part of
 * > a cookie's scope. The mock sets `foyer_admin_session` with `Path=/` and **no
 * > `Domain`**, which makes it host-only for `localhost`, and `localhost:5170` and
 * > `localhost:5173` are the same host.
 *
 * That is the property `helpers/mock.ts`'s nine direct `APIRequestContext` calls will
 * silently depend on the day enforcement is turned on. It is also the property that
 * breaks **quietly** rather than loudly if anyone gives the cookie an explicit
 * `Domain`, moves the mock to another hostname, or splits the two behind different
 * names: today those helpers would simply go back to being anonymous callers, which
 * with `REQUIRE_SESSION` false is indistinguishable from a signed-in one.
 *
 * So it is asserted now, while it is cheap and while the answer is known — rather than
 * discovered as a two-hundred-test failure by the person throwing the switch.
 *
 * The direction driven is the useful one: sign in **through the console's own origin**,
 * the way a browser does, and then prove the *mock's* origin recognises the session.
 */

const COOKIE = "foyer_admin_session";

test.afterEach(async ({ context }) => {
  await signOut(context);
});

test("a session opened through the console's origin is recognised by the mock's", async ({
  context,
}) => {
  expect(
    new URL(ADMIN_URL).hostname,
    "The console and the mock are no longer on the same hostname, so the cookie this " +
      "file is about no longer spans them. `helpers/mock.ts` will need its own " +
      "sign-in the day `REQUIRE_SESSION` is thrown — see " +
      "`helpers/session.ts`.",
  ).toBe(new URL(MOCK_URL).hostname);
  expect(
    new URL(ADMIN_URL).port,
    "The console and the mock are on the same port, so this file is measuring " +
      "nothing: there is only one origin left to span.",
  ).not.toBe(new URL(MOCK_URL).port);

  /* Through the Vite proxy — the origin a browser actually talks to. */
  const signIn = await context.request.post(`${ADMIN_URL}/api/v0/auth/session`, {
    data: { username: STAFF, password: THROWAWAY_PASSWORD },
  });
  expect(signIn.status()).toBe(200);

  /*
   * The cookie itself, before anything is inferred from behaviour.
   *
   * `domain` must be the bare host: a leading dot, or any other value, means the
   * server has started scoping it and the reasoning above no longer applies. Playwright
   * models cookies the way the spec does and has no port field at all, which is itself
   * the point being made.
   */
  const cookie = (await context.cookies()).find((c) => c.name === COOKIE);
  expect(cookie, `No \`${COOKIE}\` cookie was set by the sign-in.`).toBeTruthy();
  expect(
    cookie!.domain,
    `\`${COOKIE}\` is scoped to "${cookie!.domain}" rather than to the bare host. A ` +
      "`Domain` attribute is exactly the change that quietly breaks the shared " +
      "session — see the header of this file.",
  ).toBe(new URL(ADMIN_URL).hostname);
  expect(cookie!.path).toBe("/");

  /*
   * And the other origin recognises it. Two probes, because they fail differently:
   * the first says the session is *readable* there, the second says it is *authorised*
   * there — the `403` can only be produced by a server that resolved the cookie to an
   * operator and then looked at their role.
   */
  const whoami = await context.request.get(`${MOCK_URL}/api/v0/auth/session`);
  expect(
    whoami.status(),
    `A session opened on :${new URL(ADMIN_URL).port} was not recognised on ` +
      `:${new URL(MOCK_URL).port}. Cookies are not port-scoped (RFC 6265 §8.5), so ` +
      "either the cookie gained a `Domain`, the hosts diverged, or the mock stopped " +
      "reading the cookie it sets.",
  ).toBe(200);
  expect((await whoami.json()).operator.username).toBe(STAFF);

  const refused = await context.request.get(`${MOCK_URL}/api/v0/operators`);
  expect(
    refused.status(),
    "The mock did not apply this operator's role to a request on its own origin. A " +
      "`200` here means the session was not seen at all — `REQUIRE_SESSION` is false, " +
      "so an unrecognised caller is served rather than refused, which is precisely " +
      "the silent failure this lock exists for.",
  ).toBe(403);
  expect((await refused.json()).params.required).toBe("operators:manage");
});

/**
 * The other direction, and the one `helpers/session.ts` actually uses: opened at the
 * mock, honoured by the console.
 *
 * Driven through a real page load rather than through another `APIRequestContext`
 * call, because that is the claim that matters — `signInAs` talks to `:5170` and every
 * spec in this directory then expects `page.goto` on `:5173` to be that operator.
 */
test("a session opened at the mock is honoured by the console", async ({ page, context }) => {
  const signIn = await context.request.post(`${MOCK_URL}/api/v0/auth/session`, {
    data: { username: STAFF, password: THROWAWAY_PASSWORD },
  });
  expect(signIn.status()).toBe(200);

  await page.goto("/");
  await expect(
    page.locator(".sidebar-who"),
    "The console booted as nobody with a session cookie in the jar. Every spec in " +
      "`tests/admin-auth/` signs in at the mock's origin and browses at the console's, " +
      "so this is the assumption they all rest on.",
  ).toHaveCount(1);
  await expect(page.locator(".sidebar-who")).toContainText(STAFF);

  /* And it is that operator specifically, not merely *an* operator: the two
     administrator destinations are absent. */
  await expect(page.locator('nav.nav a[href="/operators"]')).toHaveCount(0);
});
