import { expect, test, type Locator, type Route } from "@playwright/test";
import { MOCK_URL } from "../helpers/env.js";
import { ADMINISTRATOR, shellSettled, signInAs, signOut } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `/operators`' row actions need a pending guard
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Both `PATCH`-sending row controls carry `aria-disabled` plus
 * `if (update.isPending) return;` while a `PATCH` is in flight.
 *
 * Without them the row controls carry neither `disabled` nor `aria-disabled` while a
 * `PATCH` is in flight. **That is not the focus-loss defect**: no focus is lost, because
 * nothing becomes `disabled` and so the user agent never blurs anything. What is
 * missing is the other half of the same pattern — the console says nothing about the
 * request it is waiting on, and nothing stops a second one.
 *
 * The repository's answer everywhere else is `aria-disabled` **plus a guarded
 * handler**, and the pairing is the point: an `aria-disabled` button is still
 * clickable, so the attribute is a claim about the control and the handler is what
 * honours it. `ConfirmDialog`, `SignInScreen` and `PasswordScreen` all do this, and so
 * do these controls. Both halves are asserted below, separately, so a
 * repair that adds the attribute and forgets the guard is still red.
 *
 * ── THE OBSERVATION WAITS; IT DOES NOT SAMPLE ONE INSTANT ───────────────────
 *
 * `patches` is incremented by the route handler in Node the moment the request is
 * intercepted, which is *before* React has committed the render that carries
 * `isPending` — the attribute appears **3–9 ms** later. A single read immediately after
 * `expect.poll` sees the first `PATCH` therefore falls on the wrong side of a one-frame
 * window about half the time, and reliably so on a busy machine; the tell is the
 * *second* half of the same test reporting `1 PATCH for two presses`, which is only
 * possible if `isPending` was true by the second press. Two halves disagreeing about
 * the same instant says "race" rather than "defect".
 *
 * So the observation **waits** for the console to say something, bounded by
 * `ANNOUNCE_WINDOW_MS`. This does not weaken it: it is still an observation and not
 * an assertion — the loop returns whatever the control says when the window closes —
 * so against a console with no guard at all it returns
 * `{ ariaDisabled: null, disabled: false }` and the assertion below is red.
 *
 * A `MutationObserver` stripping `aria-disabled` from the row actions as fast as React
 * writes it turns the case red on `ariaDisabled: null` with the full window elapsed,
 * while the *second* assertion still reports `1 PATCH for two presses`: the halves
 * fail independently, so the wait has not quietly turned this into one assertion
 * about two things.
 *
 * ── NOTHING HERE MUTATES ────────────────────────────────────────────────────
 *
 * The `PATCH` is **held at the browser and never reaches the mock**: the route handler
 * counts it, waits long enough for a second press to happen, and aborts. So the
 * console spends the whole window in `isPending` — which is the state under test —
 * and the server is never asked to change anything. The last test re-reads the
 * operator from the mock and asserts it is untouched.
 */

/**
 * `/api/v0/operators/op-tuan` — the single-operator path, not the collection.
 *
 * **A predicate over `URL`, and not a bare `RegExp`.** `page.route` tests a `RegExp`
 * against the *whole* URL, origin and all, so a pattern anchored with `^\/api` matches
 * nothing at all — and a `page.route` that matches nothing does not fail, it silently
 * lets the request through. That is not a harmless miss here: this file presses a
 * control that **disables an operator**, and a route that matches nothing sends real
 * `PATCH`es to the shared mock. The operators survive (the presses toggle in pairs)
 * and the audit log does not — it is append-only, so every `operator-updated` entry
 * stays in it permanently.
 *
 * Every interception in `tests/admin-auth/` is a predicate for this reason. If you add
 * another, add it the same way.
 */
const OPERATOR_PATH = (url: URL) => /^\/api\/v0\/operators\/[^/]+$/.test(url.pathname);
/** Long enough for a second press, short enough not to approach the test timeout. */
const HOLD_MS = 2_500;
/**
 * How long the control is given to say something before the observation gives up.
 *
 * Comfortably longer than the 3–9 ms measured above, and comfortably shorter than
 * `HOLD_MS` — the window has to close while the `PATCH` this is about is still in
 * flight, or the observation would be reporting on a request that had already ended.
 */
const ANNOUNCE_WINDOW_MS = 1_000;

/** A staff row: its three controls are live, unlike the last administrator's. */
const TARGET = "le.minh.tuan";

interface MockOperator {
  id: string;
  username: string;
  role: string;
  enabled: boolean;
}

async function readOperator(
  request: { get: (url: string) => Promise<{ status(): number; json(): Promise<unknown> }> },
  username: string,
): Promise<MockOperator | undefined> {
  const res = await request.get(`${MOCK_URL}/api/v0/operators`);
  expect(res.status()).toBe(200);
  const body = (await res.json()) as { items: MockOperator[] };
  return body.items.find((o) => o.username === username);
}

/** What a control says about a request it has in flight. */
interface Announcement {
  ariaDisabled: string | null;
  disabled: boolean;
}

async function readAnnouncement(control: Locator): Promise<Announcement> {
  return control.evaluate((el) => ({
    ariaDisabled: el.getAttribute("aria-disabled"),
    disabled: (el as HTMLButtonElement).disabled,
  }));
}

/**
 * Says nothing at all — neither half of the pattern present.
 *
 * Deliberately not `ariaDisabled !== "true"`: a console that reached for a real
 * `disabled` attribute has *said something*, and must be carried to the assertion
 * below so it fails there — where the message explains why `aria-disabled` is the
 * pattern here — rather than being waited out and reported as silence.
 */
function isSilent(a: Announcement): boolean {
  return a.ariaDisabled === null && !a.disabled;
}

test.afterEach(async ({ context }) => {
  await signOut(context);
});

test("a row action in flight is announced, and cannot be pressed twice", async ({
  page,
  context,
  request,
}) => {
  await signInAs(context, ADMINISTRATOR);
  const before = await readOperator(request, TARGET);
  expect(before, `The fixture operator \`${TARGET}\` is missing.`).toBeTruthy();

  let patches = 0;
  await page.route(OPERATOR_PATH, async (route: Route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    patches++;
    /* Held at the browser: the console stays in `isPending` for the whole window and
       the mock is never asked to change anything. */
    await new Promise((r) => setTimeout(r, HOLD_MS));
    await route.abort();
  });

  await page.goto("/operators");
  await shellSettled(page);
  await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

  const row = page.locator("tbody tr:not(.empty-row)").filter({ hasText: TARGET });
  await expect(row).toHaveCount(1);
  const actions = row.locator(".row-actions button");
  await expect(actions).toHaveCount(3);

  /* The first control is enable/disable — reversible in one click, which is exactly
     why it has no confirmation and exactly why a double press is cheap to make. */
  const toggle = actions.nth(0);
  await toggle.click();
  await expect
    .poll(() => patches, {
      timeout: 5_000,
      message:
        "The first press sent no `PATCH`, so there is no in-flight request and " +
        "nothing below is being measured. This is the harness, not the console.",
    })
    .toBe(1);

  /*
   * ── BOTH HALVES ARE MEASURED BEFORE EITHER IS ASSERTED ─────────────────────
   *
   * The obvious ordering — assert the attribute, then press again — never reaches the
   * second half while the first is red, which is today and which is the whole window
   * in which the second half matters. So both observations are taken first and the two
   * assertions come after, and a reader of a failing run is told what *both* halves
   * did rather than only the one that stopped it.
   */
  /*
   * Waited for, not sampled — see the header. The loop exits the instant the control
   * says anything, so a correct console costs a few milliseconds; it runs the window
   * out and returns the silence unchanged when the control says nothing, which is the
   * failure this test exists to report. Either way what comes back is an
   * *observation*, so the two assertions below still both run.
   */
  const deadline = Date.now() + ANNOUNCE_WINDOW_MS;
  let announced = await readAnnouncement(toggle);
  while (isSilent(announced) && Date.now() < deadline) {
    await page.waitForTimeout(25);
    announced = await readAnnouncement(toggle);
  }

  /* Still in flight — the handler is holding it — so this press is the double. */
  await toggle.click({ force: true });
  await page.waitForTimeout(500);
  const afterSecondPress = patches;

  expect(
    announced,
    `The control that started the request says nothing about it. It carries ` +
      `\`aria-disabled=${JSON.stringify(announced.ariaDisabled)}\` and ` +
      `\`disabled=${announced.disabled}\`.\n` +
      "  `aria-disabled` is the console's pattern here " +
      "rather than `disabled`, and for a reason worth keeping: the control has focus " +
      "(the operator just pressed it), and setting the DOM `disabled` attribute on a " +
      "focused element blurs it to `<body>` with a null `relatedTarget` — which is " +
      "the focus-loss defect and is what the fourteen `aria-disabled` controls elsewhere " +
      "in this console exist to avoid.\n" +
      `  The second half of this test measured ${afterSecondPress} \`PATCH\`es for two ` +
      "presses; 1 is correct.",
  ).toEqual({ ariaDisabled: "true", disabled: false });

  expect(
    afterSecondPress,
    `A second press while the first \`PATCH\` was in flight sent a second one — ` +
      `${afterSecondPress} requests for two presses.\n` +
      "  An `aria-disabled` button is still clickable: the attribute is a claim about " +
      "the control, and the handler is what has to honour it. A repair that adds the " +
      "attribute and forgets `if (update.isPending) return;` is still this defect, " +
      "which is why the two halves are asserted separately.\n" +
      "  `force: true` on the second press, so that a repair which reaches for a real " +
      "`disabled` attribute is measured here rather than silently skipped by " +
      "Playwright's actionability wait — and then fails the assertion above, which " +
      "requires `aria-disabled` instead and says why.",
  ).toBe(1);
});

/**
 * ── AND THE WORLD IS UNTOUCHED ──────────────────────────────────────────────
 *
 * Green, and it must stay green: the test above presses a control that disables an
 * operator, twice. It is only safe because both requests are aborted in the browser.
 *
 * Separate from the test above rather than an assertion inside it, because that one is
 * a *marked expected failure* — a mutation that happened after its first failing
 * assertion would never be reached, and the safety claim would be resting on the
 * defect still being present.
 */
test("holding the PATCH at the browser leaves the operator unchanged", async ({
  page,
  context,
  request,
}) => {
  await signInAs(context, ADMINISTRATOR);
  const before = await readOperator(request, TARGET);

  await page.route(OPERATOR_PATH, async (route: Route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    await route.abort();
  });

  await page.goto("/operators");
  await shellSettled(page);
  await expect(page.locator("tbody[aria-busy=false]").first()).toBeVisible();

  const row = page.locator("tbody tr:not(.empty-row)").filter({ hasText: TARGET });
  await row.locator(".row-actions button").nth(0).click();
  await row.locator(".row-actions button").nth(0).click();
  await page.waitForTimeout(750);

  expect(
    await readOperator(request, TARGET),
    `\`${TARGET}\` changed. The route handler in this file must abort every \`PATCH\` ` +
      "rather than letting one through — the fixture world is shared with every other " +
      "spec in the suite and with everything else running on this machine, and a " +
      "disabled staff operator would change the row counts, the audit log and the " +
      "`/operators` renderings that four accessibility scans are taken against.",
  ).toEqual(before);
});
