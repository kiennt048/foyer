import { expect, test, type Locator, type Page } from "@playwright/test";
import { ADMINISTRATOR, shellSettled, signInAs, signOut } from "../helpers/session.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  `/password` — the two fields, and the hint that leaves when it is needed most
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The one password operation this product has, and until now it was in no spec and in
 * neither accessibility sweep.
 *
 * ── WHY THE ASSERTION IS THE PAIR AND NOT `aria-invalid` ───────────────────
 *
 * `aria-invalid` alone is green here already, and green on `/sign-in` too. A field can
 * be marked invalid and describe nothing; it can name an `aria-describedby` id that is
 * not in the document, which no browser reports and no `toHaveAttribute` can see.
 * `describedByText` below resolves every id and reads it, so the three cases —
 * marked-and-described, marked-and-silent, described-by-nothing — are distinguishable.
 *
 * ── DRIVING A FIELD ERROR WITHOUT MUTATING ANYTHING ────────────────────────
 *
 * The mock holds a password per operator, in memory, and verifies it at sign-in as
 * well as here. Nothing static is stored either way: the value is generated per process the way
 * first boot does, and this suite pins it with `FOYER_MOCK_ADMIN_PASSWORD`, so
 * `docs/rules/security.md` §7 still holds — no fixture operator carries a
 * credential in the repository.
 *
 * Sending a **wrong** current password and a **too-short** new one refuses both fields
 * in one `422` and reaches `current.session.password = body.newPassword` never: the
 * handler returns before it. So this file changes no password, in the session or
 * anywhere else, and the run after it starts from the same world.
 */

const CURRENT = "#current-password";
const NEXT = "#new-password";
const SUBMIT = ".page form button[type=submit]";

/** Wrong on purpose. The mock compares it against what sign-in was given. */
const WRONG_CURRENT = "not-the-password-this-session-was-opened-with";
/** Below `PASSWORD_MIN_LENGTH`, so the second field is refused in the same answer. */
const TOO_SHORT = "abc";

test.afterEach(async ({ context }) => {
  await signOut(context);
});

/**
 * Everything `aria-describedby` resolves to, in order, as text.
 *
 * Each id is asserted to exist first. An `aria-describedby` naming an id that is not
 * in the document is the failure mode that looks identical to a working one from the
 * attribute alone: present, plausible, and announced by nothing.
 */
async function describedByText(page: Page, field: Locator): Promise<string[]> {
  const attr = (await field.getAttribute("aria-describedby")) ?? "";
  const ids = attr.split(/\s+/).filter(Boolean);
  const out: string[] = [];
  for (const id of ids) {
    /* `[id="…"]` rather than `#…`: composed in Node, where `CSS.escape` does not
       exist, and these ids contain hyphens. */
    const node = page.locator(`[id="${id}"]`);
    await expect(
      node,
      `\`aria-describedby\` names "${id}" and no element has that id — the field ` +
        "looks described and a screen reader is told nothing.",
    ).toHaveCount(1);
    out.push(((await node.textContent()) ?? "").trim());
  }
  return out;
}

/** Signs in, opens the form, and gets both fields refused in one answer. */
async function refuseBothFields(page: Page): Promise<void> {
  await page.goto("/password");
  await shellSettled(page);
  await expect(page.locator(CURRENT)).toBeVisible();

  await page.locator(CURRENT).fill(WRONG_CURRENT);
  await page.locator(NEXT).fill(TOO_SHORT);
  await page.locator(SUBMIT).click();

  await expect(
    page.locator(".page .field-error"),
    "The refusal did not name both fields. `POST /auth/password` answers one `422` " +
      "carrying `currentPassword: incorrect` and `newPassword: below-minimum` — if " +
      "only one arrived, the mock's validation order changed and the two tests here " +
      "are measuring one field between them.",
  ).toHaveCount(2);
}

test.describe("/password", () => {
  /**
   * The signed-out variant — a real state, and one of the four entries a blanket
   * `storageState` would quietly convert into a signed-in scan.
   *
   * `PasswordScreen` says so rather than rendering an empty form that can only answer
   * `401`, which is the same argument as the portal's not-connected page.
   */
  test("signed out, it says there is no password to change and offers the way in", async ({
    page,
  }) => {
    await page.goto("/password");
    await expect(page.locator("h1.page-title")).toBeVisible();

    await expect(
      page.locator(".page form"),
      "A signed-out console rendered the change-password form. There is no password " +
        "of *yours* to change until there is a you, and the form can only answer 401.",
    ).toHaveCount(0);
    await expect(page.locator(`.page .notice a[href="/sign-in"]`)).toHaveCount(1);
  });

  test("signed in, both fields are refused as a marked-and-described pair", async ({
    page,
    context,
  }) => {
    await signInAs(context, ADMINISTRATOR);
    await refuseBothFields(page);

    for (const [name, selector] of [
      ["current password", CURRENT],
      ["new password", NEXT],
    ] as const) {
      const field = page.locator(selector);
      await expect(field, `${name}: not marked invalid.`).toHaveAttribute("aria-invalid", "true");
      const described = await describedByText(page, field);
      expect(
        described.filter(Boolean).length,
        `${name}: \`aria-describedby\` resolves to nothing with any text in it, so ` +
          "the message on screen is not the message announced.",
      ).toBeGreaterThan(0);
    }

    /*
     * And the message **on screen** is one of the nodes the field points at — not just
     * *some* text. That is what separates a described field from one that is still
     * pointing only at its hint, which is the hint defect below with its shape inverted.
     *
     * Asserted through the node's **id** rather than by comparing prose. The mock
     * sends `"That is not your current password."` and the console renders its own
     * catalogue copy, `"Not correct."` — driven, and the reason this assertion was
     * rewritten. A lock that compared the two strings would fail on a translation
     * rather than on a regression, which `README.md` forbids for this whole suite.
     */
    for (const [name, selector] of [
      ["current password", CURRENT],
      ["new password", NEXT],
    ] as const) {
      const errorNode = page.locator(`.field:has(${selector}) .field-error`);
      await expect(errorNode, `${name}: no error node rendered.`).toHaveCount(1);
      const errorId = await errorNode.getAttribute("id");
      expect(errorId, `${name}: the error message has no id, so nothing can point at it.`)
        .toBeTruthy();

      const described = ((await page.locator(selector).getAttribute("aria-describedby")) ?? "")
        .split(/\s+/)
        .filter(Boolean);
      expect(
        described,
        `${name}: the message on screen ("${(await errorNode.textContent())?.trim()}") is ` +
          "not among the nodes the field describes itself with, so it is visible and " +
          "unannounced.",
      ).toContain(errorId);
    }
  });

  /**
   * ── THE EVIDENCE ────────────────────────────────────────────────────────────
   *
   * The `.field-error` nodes keep their ids and their text, and the *description* is
   * cut: `aria-describedby` is stripped from both fields and the same refusal is read
   * again. The message is still on screen and still styled — which is precisely the
   * shape of the failure this file exists to catch, because it is invisible in a
   * browser and total for a screen reader.
   */
  test("the lock is not vacuous: with the description cut, the messages are unreachable", async ({
    page,
    context,
  }) => {
    await signInAs(context, ADMINISTRATOR);
    await refuseBothFields(page);

    await page
      .locator(`${CURRENT}, ${NEXT}`)
      .evaluateAll((els) => els.forEach((el) => el.removeAttribute("aria-describedby")));

    for (const selector of [CURRENT, NEXT]) {
      await expect(page.locator(selector)).not.toHaveAttribute("aria-describedby", /./);
      expect(
        await describedByText(page, page.locator(selector)),
        "A field with no `aria-describedby` still resolved to a description, so the " +
          "helper is not reading the attribute it claims to.",
      ).toEqual([]);
    }

    /* And the messages are still there, visibly. Nothing about the page looks wrong. */
    await expect(page.locator(".page .field-error")).toHaveCount(2);
    await expect(page.locator(`[id="current-password-error"]`)).toBeVisible();
  });
});

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  THE HINT — it must not leave exactly when it is needed
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `Password.tsx` concatenates hint and error on both fields. The failure this guards
 * is a field that *swaps* the hint for the error:
 *
 * ```tsx
 * // swaps — wrong
 * aria-describedby={fieldErrors.currentPassword ? "current-password-error" : "current-password-hint"}
 *
 * // concatenates — right
 * aria-describedby={fieldErrors.newPassword ? "new-password-hint new-password-error" : "new-password-hint"}
 * ```
 *
 * With the swap, the sentence explaining *why the current password is asked for at
 * all*, given that the browser is already signed in — an unattended console is the
 * ordinary case behind a counter — **stops being announced the moment the operator
 * gets it wrong**, which is the moment they most need to know what is being asked of
 * them. The hint is still on screen; only its announcement is gone.
 *
 * **An asymmetry inside a single form is the tell that it is an accident rather than
 * a decision.** A deliberate rule would apply to both fields.
 *
 * The assertion below is therefore written as *the two fields agree*, derived from the
 * page, rather than as a hardcoded expectation about one of them — so a fix that
 * changed the rule in the other direction would still have to change both.
 */
test.describe("the current-password hint", () => {
  test("a refused field keeps its hint in the description", async ({ page, context }) => {
    await signInAs(context, ADMINISTRATOR);
    await refuseBothFields(page);

    const ids = async (selector: string) =>
      ((await page.locator(selector).getAttribute("aria-describedby")) ?? "")
        .split(/\s+/)
        .filter(Boolean);

    const current = await ids(CURRENT);
    const next = await ids(NEXT);

    /* The other field's behaviour, read from the page rather than assumed — if the
       "correct" half regresses, this test says so instead of quietly inverting. */
    expect(
      next,
      "The new-password field no longer concatenates its hint and its error, so the " +
        "asymmetry this test is about has been resolved in the wrong direction: both " +
        "fields now drop the hint when it matters.",
    ).toEqual(["new-password-hint", "new-password-error"]);

    expect(
      current,
      "The current-password field dropped its hint from `aria-describedby` the moment " +
        "it was refused.\n" +
        "  The hint is the " +
        "sentence explaining why the " +
        "current password is asked for at all when the browser is already signed in — " +
        "an unattended console behind a counter is the ordinary case — and it stops " +
        "being announced exactly when the operator has got it wrong and most needs to " +
        "know what is being asked. It is still on screen; only its announcement is " +
        "gone, which is why nothing in a browser and nothing in axe can see this.\n" +
        `  new password  → ${JSON.stringify(next)}\n` +
        `  current       → ${JSON.stringify(current)}\n` +
        "  The two fields are in the same form. The asymmetry is the tell.",
    ).toEqual(["current-password-hint", "current-password-error"]);
  });
});

/**
 * A last honesty check: **this file changes nothing in the shared world.**
 *
 * A successful password change writes an `operator-password-changed` entry to
 * `world.audit`, which is global, shared with every other run on this machine, and
 * read by `audit.spec.ts` and by both accessibility sweeps. So *"the refusal was
 * refused"* is not enough — the log is asked whether anything was appended.
 *
 * The cost of getting this wrong is invisible from the tests themselves: a mutation
 * would leave every assertion in this file green and quietly grow the audit fixture on
 * every run.
 */
test("the drives above append nothing to the audit log", async ({ page, context, request }) => {
  const countEntries = async () => {
    const res = await request.get("/api/v0/audit?limit=200");
    expect(res.status()).toBe(200);
    return ((await res.json()).items as unknown[]).length;
  };

  const before = await countEntries();
  await signInAs(context, ADMINISTRATOR);
  await refuseBothFields(page);

  expect(
    await countEntries(),
    "A drive in this file wrote to the audit log, so it completed a password change " +
      "it was meant to have had refused. `POST /auth/password` returns its `422` " +
      "before both `current.session.password = …` and its `audit(…)` call, so this is " +
      "a change in the mock or in what this file sends — not a flake.",
  ).toBe(before);
});
