import { expect, test, type Locator, type Page } from "@playwright/test";
import { ADMINISTRATOR, THROWAWAY_PASSWORD, signOut } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `/sign-in` — the flow, the two refusals, and where focus lands afterwards
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The way into the console, and until now the only route in it with no lock of any
 * kind: not in either accessibility sweep, not in one end-to-end spec.
 *
 * ── THE TWO REFUSALS ARE NOT ONE ────────────────────────────────────────────
 *
 * The obvious assertion here is *"a wrong password marks the password field"*. It is
 * **red against correct code**, and driving it is the only way to find that out:
 *
 * ```
 *   POST /auth/session {"username":"nobody","password":"x"}
 *     ← 401  {"type":"/problems/bad-credentials","title":"Wrong username or password",
 *             "status":401}
 * ```
 *
 * No `params.fields`, so `fieldMessages` returns `{}`, so `aria-invalid` is set on
 * **neither** input and **zero** `.field-error` nodes render. That is the design and
 * it is stated in three places — the mock's handler, `signIn` in `openapi.yaml`, and
 * `SignInScreen`'s own comment: an answer that said *which* of the two was wrong would
 * make this form a place to ask whether somebody has an account here, and would turn
 * disabling an account into a way of publishing that it exists.
 *
 * So the cases are split. `401` is one `role="alert"` notice and no field marking;
 * `422` — an empty submit, which the form does not pre-empt — is where the field
 * treatment lives, and where the `aria-invalid` / `aria-describedby` **pair** is
 * asserted. Asserting `aria-invalid` alone would be green in both places already.
 *
 * ── WHERE FOCUS LANDS ───────────────────────────────────────────────────────
 *
 * The last two tests lock where focus lands after signing in, and each carries its
 * own argument.
 */

const USERNAME = "#signin-username";
const PASSWORD = "#signin-password";
const SUBMIT = ".signin form button[type=submit]";
/** The form's own refusal notice. `ErrorNotice` renders `role="alert"`. */
const NOTICE = ".signin [role=alert]";
const FIELD_ERROR = ".signin .field-error";

/** Ends whatever session a test created. The mock's session map is shared. */
test.afterEach(async ({ context }) => {
  await signOut(context);
});

async function fillAndSubmit(page: Page, username: string, password: string): Promise<void> {
  await page.goto("/sign-in");
  await expect(page.locator(USERNAME)).toBeVisible();
  if (username) await page.locator(USERNAME).fill(username);
  if (password) await page.locator(PASSWORD).fill(password);
  await page.locator(SUBMIT).click();
}

/**
 * The `aria-invalid` / `aria-describedby` pair, and the node the description resolves
 * to.
 *
 * **Three assertions, because the first two are already green apart.** A field can
 * carry `aria-invalid="true"` and describe nothing; it can carry an `aria-describedby`
 * that names an id which is not in the document — the browser reports no error, the
 * screen reader announces nothing, and `toHaveAttribute` passes. Resolving the id and
 * reading the text is the only one of the three that can tell those apart.
 */
async function expectRefusedField(field: Locator, message: string): Promise<void> {
  const page = field.page();
  await expect(field, `${message}: the field is not marked invalid.`).toHaveAttribute(
    "aria-invalid",
    "true",
  );
  const describedBy = await field.getAttribute("aria-describedby");
  expect(describedBy, `${message}: the field describes nothing.`).toBeTruthy();

  const ids = (describedBy ?? "").split(/\s+/).filter(Boolean);
  const texts: string[] = [];
  for (const id of ids) {
    /* `[id="…"]` rather than `#…`: this string is composed in Node, where `CSS.escape`
       does not exist, and the ids in this console contain hyphens. */
    const node = page.locator(`[id="${id}"]`);
    await expect(
      node,
      `${message}: \`aria-describedby\` names "${id}", and no element in the document ` +
        "has that id. The attribute is present, the field looks described, and a " +
        "screen reader announces nothing.",
    ).toHaveCount(1);
    texts.push(((await node.textContent()) ?? "").trim());
  }
  expect(
    texts.filter(Boolean).length,
    `${message}: every node \`aria-describedby\` resolves to is empty.`,
  ).toBeGreaterThan(0);
}

test.describe("signing in", () => {
  /**
   * The whole point of the screen, and the assertion the other tests rest on.
   *
   * `.sidebar-who` rather than a URL: arriving at `/` proves a navigation, not a
   * session, and `auth.tsx` renders the console **ungated for one request** on a cold
   * load. The sidebar names an operator only once the boot probe has answered.
   */
  test("a correct sign-in reaches the console as that operator", async ({ page }) => {
    await fillAndSubmit(page, ADMINISTRATOR, THROWAWAY_PASSWORD);

    await expect(page.locator(".sidebar-who")).toHaveCount(1);
    await expect(
      page.locator(".sidebar-who"),
      "The console signed in as somebody, and not as the operator that was typed.",
    ).toContainText(/Việt|dang\.quoc\.viet/);
    expect(new URL(page.url()).pathname, "Sign-in did not leave `/sign-in`.").not.toBe("/sign-in");
  });

  /**
   * `401` — one notice, and **neither field marked**.
   *
   * A *disabled* operator rather than an unknown username, because it is the harder
   * half of the same rule: `pham.thu.ha` exists, is an administrator, and left in
   * March. The answer must be indistinguishable from the one a stranger gets, or
   * disabling an account publishes that it exists.
   *
   * The `.field-error` count is asserted at **zero**. That is the assertion that makes
   * this test the opposite of the vacuous one described at the top of this file: it
   * fails the moment somebody "improves" the refusal by marking a field.
   */
  test("a refused credential is one notice, and marks neither field", async ({ page }) => {
    await fillAndSubmit(page, "pham.thu.ha", THROWAWAY_PASSWORD);

    await expect(page.locator(NOTICE)).toHaveCount(1);
    await expect(
      page.locator(NOTICE),
      "The refusal notice did not render the catalogue copy for " +
        "`/problems/bad-credentials`.",
    ).toContainText(/Wrong username or password/i);

    await expect(
      page.locator(FIELD_ERROR),
      "A refused sign-in marked a field. `/problems/bad-credentials` carries no " +
        "`params.fields` precisely so that it cannot: an answer that said which of " +
        "the two was wrong would turn this form into a way of asking whether somebody " +
        "has an account here, and would make disabling an account publish its " +
        "existence. See the handler in `web/mock/src/server.ts` and `signIn` in " +
        "`openapi.yaml` before changing this.",
    ).toHaveCount(0);
    await expect(page.locator(USERNAME)).not.toHaveAttribute("aria-invalid", "true");
    await expect(page.locator(PASSWORD)).not.toHaveAttribute("aria-invalid", "true");

    /* And it really was refused — no session exists. Without this the test above is
       satisfied by a form that never submitted at all. */
    const probe = await page.context().request.get("/api/v0/auth/session");
    expect(probe.status(), "A refused sign-in left a session behind.").toBe(401);
  });

  /**
   * `422` — and this is where the field treatment lives.
   *
   * The form does no client-side validation, so an empty submit reaches the server and
   * comes back with `params.fields` naming both. Two `.field-error` nodes, each paired
   * with `aria-invalid` and each resolving through `aria-describedby`.
   */
  test("an empty submit names both fields, paired and described", async ({ page }) => {
    await fillAndSubmit(page, "", "");

    await expect(page.locator(FIELD_ERROR)).toHaveCount(2);
    await expectRefusedField(page.locator(USERNAME), "username");
    await expectRefusedField(page.locator(PASSWORD), "password");

    /* The ids the screen actually uses, named here so a rename is a red test rather
       than a silently unreachable description. */
    await expect(page.locator(USERNAME)).toHaveAttribute(
      "aria-describedby",
      "signin-username-error",
    );
    await expect(page.locator(PASSWORD)).toHaveAttribute(
      "aria-describedby",
      "signin-password-error",
    );
  });

  /**
   * ── THE EVIDENCE FOR THE TWO ABOVE ──────────────────────────────────────────
   *
   * Re-injected from the page rather than recorded in a commit message: the error
   * nodes are removed from the document and the same two drives are repeated. React
   * does not put them back — the nodes are detached while their fibers still point at
   * them — so what remains is a form that refuses and says nothing, which is exactly
   * the state the two tests above are supposed to be able to see.
   */
  test("the lock is not vacuous: with the refusal markup removed, both drives go quiet", async ({
    page,
  }) => {
    await fillAndSubmit(page, "", "");
    await expect(page.locator(FIELD_ERROR)).toHaveCount(2);
    await page.locator(FIELD_ERROR).evaluateAll((els) => els.forEach((el) => el.remove()));
    await expect(
      page.locator(FIELD_ERROR),
      "The `422` field errors could not be removed, so the sabotage proves nothing.",
    ).toHaveCount(0);

    await fillAndSubmit(page, "pham.thu.ha", THROWAWAY_PASSWORD);
    await expect(page.locator(NOTICE)).toHaveCount(1);
    await page.locator(NOTICE).evaluateAll((els) => els.forEach((el) => el.remove()));
    await expect(
      page.locator(NOTICE),
      "The `401` notice could not be removed, so the sabotage proves nothing.",
    ).toHaveCount(0);
  });
});

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  THE SIGN-IN TRANSITION — focus must not drop to `<body>`
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * ── HOW THESE TWO DISCRIMINATE ──────────────────────────────────────────────
 *
 * There is no permanent sabotage case for these two. Blurring focus to `<body>` from
 * the page after the sign-in navigation — the state an early return in `refocus.ts`
 * would leave — turns **both arms red** on `Expected: not "<body>"`, which is how a
 * selector that had stopped matching is told apart from a pass.
 *
 * ── THE DEFECT THEY GUARD AGAINST ───────────────────────────────────────────
 *
 * `useFocusOnNavigation` must not exempt the first page of a session like this:
 *
 * ```ts
 * const previous = mountedUnder;
 * mountedUnder = pathname;
 * if (previous === null || previous === pathname) return;
 * heading.current?.focus();
 * ```
 *
 * The exemption is written for *"the browser has already placed focus correctly and
 * nothing has changed"*. On a sign-in neither half is true: focus was on a submit
 * button that the navigation destroys. **Both arms would fire**, because
 * `SignInScreen` renders outside `Shell` and has no `PageHead`, so the module-level
 * `mountedUnder` is never updated while `/sign-in` is up.
 *
 * ── WHY THERE ARE TWO TESTS AND NOT ONE ─────────────────────────────────────
 *
 * This is the trap, and it matters more than the fix. A lock that drives only the
 * *cold* sign-in is repaired by seeding `mountedUnder` — and **stays green while the
 * return-to-where-you-were case is still broken**. That case is the common one under
 * enforcement: it is the entire reason `location.state.from` exists. Both arms, or the
 * lock is worse than none.
 */
const FOCUS_MESSAGE =
  "Focus after signing in was not the page heading.\n" +
  "  If `useFocusOnNavigation` in `admin/src/screens/refocus.ts` returns early, nothing " +
  "moves focus — and the control it was standing on (the submit button) has just been " +
  "unmounted, which leaves the user agent no choice but `<body>`. A keyboard or " +
  "screen-reader operator signs in and is nowhere.\n" +
  "  Read `refocus.ts` and `SignIn.tsx` together: the exemption has two arms and this " +
  "console needs both of them not to fire on a sign-in.";

/** Where focus is, in words a failure message can use. `<body>` is the answer. */
async function focusReport(page: Page): Promise<string> {
  return page.evaluate(() => {
    const el = document.activeElement as HTMLElement | null;
    if (!el || el === document.body) return "<body>";
    const cls = typeof el.className === "string" && el.className ? `.${el.className}` : "";
    return `${el.tagName.toLowerCase()}${cls}`;
  });
}

test.describe("where focus lands after signing in", () => {
  /**
   * Arm one — the cold sign-in. `mountedUnder` is `null`, so `previous === null`.
   */
  test("cold sign-in leaves focus on the heading, not on <body>", async ({ page }) => {
    await fillAndSubmit(page, ADMINISTRATOR, THROWAWAY_PASSWORD);
    await expect(page.locator("h1.page-title")).toBeVisible();

    expect(await focusReport(page), FOCUS_MESSAGE).not.toBe("<body>");
    await expect(page.locator("h1.page-title"), FOCUS_MESSAGE).toBeFocused();
  });

  /**
   * Arm two — **the one that survives a partial fix**, and the common case under
   * enforcement.
   *
   * The drive, and every step of it is load-bearing:
   *
   *  1. land on `/` and let the shell settle, so a `PageHead` has mounted and
   *     `mountedUnder` is `"/"` rather than `null`;
   *  2. route a `401` onto `GET /operators` **before** going there, so the refusal is
   *     already waiting. `/operators` is reachable with no session today —
   *     `REQUIRE_SESSION` is `false` — so the refusal has to be injected rather than
   *     waited for;
   *  3. navigate **client-side** to `/operators`. A `goto` would reload the module and
   *     reset `mountedUnder` to `null`, which is arm one again wearing a different URL.
   *     `OperatorsScreen`'s `PageHead` mounts — setting `mountedUnder` to
   *     `"/operators"` — and only then does the refused query resolve;
   *  4. `whenSignedOut` routes to `/sign-in` carrying `state.from = "/operators"`,
   *     which is the value this arm needs and the only honest way to obtain it;
   *  5. **unroute**, so the sign-in's own `navigate("/operators")` reaches a real list
   *     rather than the injected refusal;
   *  6. sign in. `SignInScreen` navigates to `destination`, `OperatorsScreen`'s
   *     `PageHead` mounts under `/operators` — and `previous === pathname`, so the
   *     *second* arm of the exemption fires and focus is never moved.
   */
  test("signing back in to where you were leaves focus on the heading", async ({
    page,
  }) => {
    await page.goto("/");
    await expect(page.locator("h1.page-title")).toBeVisible();

    const notSignedIn = {
      status: 401,
      contentType: "application/problem+json",
      body: JSON.stringify({
        type: "/problems/not-signed-in",
        title: "Not signed in",
        status: 401,
      }),
    };
    const listOperators = (url: URL) => url.pathname === "/api/v0/operators";
    await page.route(listOperators, (route) =>
      route.request().method() === "GET" ? route.fulfill(notSignedIn) : route.fallback(),
    );

    /* `href` rather than the label: `README.md`'s rule is that nothing here reads a
       word of the copy, which is still being rewritten. */
    await page.locator('nav.nav a[href="/operators"]').click();

    await expect(page).toHaveURL(/\/sign-in$/);
    await expect(
      page.locator(".signin [role=status]"),
      "The console reached `/sign-in` without the `signedOut` notice, so it arrived " +
        "by some route other than a refusal and `state.from` is not set — which is " +
        "the whole subject of this test.",
    ).toHaveCount(1);

    await page.unroute(listOperators);

    await page.locator(USERNAME).fill(ADMINISTRATOR);
    await page.locator(PASSWORD).fill(THROWAWAY_PASSWORD);
    await page.locator(SUBMIT).click();

    await expect(
      page,
      "Signing in did not return to `/operators`, so `state.from` never arrived and " +
        "this test is measuring arm one a second time.",
    ).toHaveURL(/\/operators$/);
    await expect(page.locator("h1.page-title")).toBeVisible();

    expect(await focusReport(page), FOCUS_MESSAGE).not.toBe("<body>");
    await expect(page.locator("h1.page-title"), FOCUS_MESSAGE).toBeFocused();
  });
});
