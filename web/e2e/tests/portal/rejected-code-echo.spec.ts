import { expect, test } from "@playwright/test";
import { MOCK_ACCOUNT_PASSWORD } from "../helpers/env.js";
import { freshVoucherCode } from "../helpers/mock.js";
import { recordNetwork } from "../helpers/network.js";
import { enabledAccountUsername, openPortal, portal, ungroup } from "../helpers/portal.js";

/**
 * A rejected voucher code comes back in the field.
 *
 * This is the smallest change in the portal and the one a guest is most likely to
 * feel. Twelve characters, read off a thermal receipt, typed one-handed on a phone
 * that has just associated. Get one wrong and — without this — the field comes back
 * empty and all twelve are retyped. The frontend design says so: "server-side
 * validation errors re-render the same document with the value preserved and the
 * message adjacent to the field."
 *
 * **Why it has to be the server's job, which is what these tests actually pin down.**
 * There is no client state to hold the value in. The submit is a real form POST
 * answered `303` — it must be, because the CNA only re-checks captivity after a full
 * navigation — so the failure lands on a *new document*. The CNA's data store is
 * ephemeral and the flow may not depend on JavaScript, so `sessionStorage`, a
 * client-side re-render and a script-restored field are all unavailable. The value
 * can only survive in the redirect.
 *
 * The handshake is therefore part of the contract between the mock and the portal,
 * and it is asserted as a handshake rather than as an outcome:
 *
 *   `303 Location: /{lang}/voucher/?e=<reason>&code=<normalised code>`
 *
 * `?e=` alone would render the message and lose the value; `?code=` alone would
 * refill the field and say nothing about why. `enhanced.spec.ts` already asserts the
 * *outcome* — the field is not empty after a rejected submit. What is here is the
 * mechanism, because the outcome test would pass again the moment somebody
 * reintroduced client-side state to hold the value, and that version breaks on the
 * phones the outcome test cannot reach.
 */
test.describe("a rejected voucher code", () => {
  /**
   * A code the mock will reject.
   *
   * Deliberately not "a code that was already redeemed": that would consume a fixture
   * on every run, and the mock is stateful. Twelve characters of the voucher alphabet
   * that no batch contains is rejected for the same reason and costs nothing. The
   * alphabet excludes `0 O 1 I L` — a code containing them would be rejected by
   * normalisation rather than by the lookup, which is a different mechanism.
   */
  const UNKNOWN_CODE = "ZZZZ99ZZ99ZZ";

  test("travels back in the redirect, alongside the reason", async ({ page }) => {
    const trace = await recordNetwork(page);

    const lang = await openPortal(page);
    await page.goto(`/${lang}/voucher/`);
    await page.locator(portal.codeInput).fill(UNKNOWN_CODE);
    await page.locator(portal.submit).click();

    await expect(page).toHaveURL(new RegExp(`/${lang}/voucher/`));

    const redirect = trace.responses.find(
      (r) => r.method === "POST" && r.url.includes("/api/v0/portal/voucher"),
    );
    expect(redirect, "The voucher submit produced no response to the API at all.").toBeDefined();
    expect(
      redirect!.status,
      "A failed redemption must answer 303 like a successful one. Anything else is a " +
        "response the CNA does not treat as a navigation.",
    ).toBe(303);

    const location = new URL(redirect!.location ?? "", page.url());
    expect(
      location.searchParams.get("e"),
      "The failure carried no reason, so the guest gets no message.",
    ).toBeTruthy();
    expect(
      location.searchParams.get("code"),
      "The rejected code was not carried back in the redirect. There is no client " +
        "state to hold it — a 303 lands on a new document, the CNA stores nothing, " +
        "and the flow may not depend on script — so this is the only channel it has. " +
        "Without it a guest who mistyped one character of twelve retypes all twelve.",
    ).toBe(UNKNOWN_CODE);

    /* And the document that answers it puts the value back on screen, grouped for
       the eye but ungrouping to exactly what was submitted. */
    await expect(page.locator(portal.error)).toBeVisible();
    const shown = await page.locator(portal.codeInput).inputValue();
    expect(ungroup(shown)).toBe(UNKNOWN_CODE);
  });

  test("comes back in the field with no script running", async ({ browser }) => {
    /* The case the whole design rests on. If this passes only with JavaScript, the
       value is being restored by the enhancement layer, and the enhancement layer is
       re-fetched over the venue's worst link on every Android launch. */
    const noScript = await browser.newContext({ javaScriptEnabled: false });
    try {
      const page = await noScript.newPage();
      const lang = await openPortal(page);
      await page.goto(`/${lang}/voucher/`);
      await page.locator(portal.codeInput).fill(UNKNOWN_CODE);
      await page.locator(portal.submit).click();

      await expect(page).toHaveURL(new RegExp(`/${lang}/voucher/`));
      await expect(page.locator(portal.error)).toBeVisible();
      expect(
        ungroup(await page.locator(portal.codeInput).inputValue()),
        "The rejected code survives only with JavaScript. It has to survive without.",
      ).toBe(UNKNOWN_CODE);
    } finally {
      await noScript.close();
    }
  });

  test("a real rejected code is echoed and is still redeemable afterwards", async ({
    page,
    context,
  }) => {
    /*
     * The counterpart, so the tests above cannot pass because every submit fails.
     *
     * A genuine unused code is submitted with one character wrong — the actual guest
     * error — the near-miss comes back in the field, and then the corrected code is
     * accepted. The last step is what proves the echo did not quietly consume the
     * voucher on the way past.
     */
    const code = await freshVoucherCode(context.request);
    const typo = `${code.slice(0, -1)}${code.endsWith("Z") ? "Y" : "Z"}`;
    expect(typo, "The near-miss must actually differ from the real code.").not.toBe(code);

    const lang = await openPortal(page);
    await page.goto(`/${lang}/voucher/`);
    await page.locator(portal.codeInput).fill(typo);
    await page.locator(portal.submit).click();

    await expect(page).toHaveURL(new RegExp(`/${lang}/voucher/`));
    expect(ungroup(await page.locator(portal.codeInput).inputValue())).toBe(typo);

    /* Correct it and continue — the flow is not a dead end. */
    await page.locator(portal.codeInput).fill(code);
    await page.locator(portal.submit).click();
    await expect(page).toHaveURL(new RegExp(`/${lang}/connected/?$`));
  });

  /**
   * The echo is a query parameter, so it is attacker-controlled like every other one.
   *
   * A hand-written link is the delivery vehicle a captive portal is *most* exposed
   * to: the guest expects to be sent somewhere they did not type. `render.ts`
   * normalises the parameter to the voucher wire form before it goes anywhere near
   * the DOM, so the worst a crafted link can do is pre-fill the field with twelve
   * characters of `[A-Z0-9]`. Escaping alone would stop the injection; normalising
   * also stops the field being used to render text that looks like it came from the
   * venue.
   */
  test("cannot be used to put arbitrary text in the field", async ({ page }) => {
    const payload = "<img src=x onerror=alert(1)> pay at the counter";
    await page.goto(`/en/voucher/?code=${encodeURIComponent(payload)}`);

    const value = await page.locator(portal.codeInput).inputValue();
    expect(
      value,
      "A crafted ?code= reached the field unnormalised. The parameter is a literal " +
        "echo of a form field and is attacker-controlled; it must be reduced to the " +
        "voucher alphabet before it is rendered.",
    ).toMatch(/^[A-Z0-9-]*$/);
    expect(ungroup(value).length).toBeLessThanOrEqual(12);

    /* Nothing was parsed as markup, which is the failure that would matter most. */
    expect(await page.locator("form[data-portal-form] img").count()).toBe(0);
  });
});

/**
 * The negative half — the one that is only obvious once somebody explains it, which
 * is exactly the kind of rule that gets "fixed" by a well-meaning improvement.
 *
 * A failed account login does **not** come back with the username filled in, and it
 * is not an oversight. The username is half of a credential pair; carrying it back
 * means putting it in the query string, and therefore in the browser history, the
 * `Referer` of every subsequent request, and every access and proxy log between the
 * guest and the portal — permanently, for a guest who has not authenticated. The
 * guest has to retype the password regardless, so the trade is half a form filled in
 * exchange for a standing logging liability.
 *
 * The voucher code is different in kind, which is why the tests above want the
 * opposite behaviour: by the time it is echoed it has already failed, it is
 * single-use, and it cannot be redeemed by whoever reads it out of a log.
 */
test.describe("a rejected account login", () => {
  test("never comes back with the username in the field or in the URL", async ({
    page,
    context,
  }) => {
    const trace = await recordNetwork(page);

    /* An unknown username — the guest mistyped it, which is the case this is about.
       Distinctive on purpose: if it ever reaches a URL, it is unmistakable. */
    const username = "e2e-no-such-guest-9f3c";
    const password = "e2e-not-a-real-password-4b21";

    const lang = await openPortal(page);
    await page.goto(`/${lang}/account/`);
    await page.locator(portal.usernameInput).fill(username);
    await page.locator(portal.passwordInput).fill(password);
    await page.locator(portal.submit).click();

    /* Back on the form with a message — the flow is not a dead end. */
    await expect(page).toHaveURL(new RegExp(`/${lang}/account/`));
    await expect(page.locator(portal.error)).toBeVisible();

    await expect(
      page.locator(portal.usernameInput),
      "The account form came back with the username filled in. Echoing it requires " +
        "putting it in the query string, and therefore in history, Referer headers " +
        "and every access log on the way — for a guest who has not authenticated.",
    ).toHaveValue("");
    await expect(page.locator(portal.passwordInput)).toHaveValue("");

    /*
     * The counterpart: a real account still logs in. Without it this test would pass
     * just as well against a portal where account login is simply broken — and the
     * success path is checked for the same leak, because a `303` that carried the
     * username to `/connected/` would be logged exactly as thoroughly.
     */
    const real = await enabledAccountUsername(context.request);
    await page.locator(portal.usernameInput).fill(real);
    await page.locator(portal.passwordInput).fill(MOCK_ACCOUNT_PASSWORD);
    await page.locator(portal.submit).click();
    await expect(page).toHaveURL(new RegExp(`/${lang}/connected/?$`));

    /*
     * And no credential appears in *any* URL the browser saw — not just the one on
     * screen. A redirect that carried one for a single hop is already in the logs.
     */
    const urls = [
      page.url(),
      ...trace.requests.map((r) => r.url),
      ...trace.responses.map((r) => r.location ?? ""),
    ];
    for (const url of urls) {
      for (const secret of [username, password, real, MOCK_ACCOUNT_PASSWORD]) {
        expect(url, `A credential appeared in a URL: ${url}`).not.toContain(secret);
      }
    }
  });
});
