import { expect, test } from "@playwright/test";
import { freshVoucherCode, voucherByCode } from "../helpers/mock.js";
import { recordNetwork } from "../helpers/network.js";
import { formField, openPortal, portal, ungroup, voucherLink } from "../helpers/portal.js";
import { languageOf, pathWithinLanguage } from "../helpers/env.js";

/**
 * The same flow with JavaScript on, plus the three things the enhancement layer is
 * allowed to add.
 *
 * The enhancement is deliberately small: grouping as you type, stripping the
 * grouping before submit, and marking the submit button busy. Nothing here may
 * change *how* the flow completes — only how it feels while it does.
 */
test.describe("portal, JavaScript enabled", () => {
  test("welcome → voucher → connected completes", async ({ page, context }) => {
    const code = await freshVoucherCode(context.request);

    const lang = await openPortal(page);
    await voucherLink(page, lang).click();
    await expect(page).toHaveURL(new RegExp(`/${lang}/voucher/?$`));

    await page.locator(portal.codeInput).fill(code);
    await page.locator(portal.submit).click();

    await expect(page).toHaveURL(new RegExp(`/${lang}/connected/?$`));
    await expect(page.locator(portal.connectedBadge)).toBeVisible();
    expect((await voucherByCode(context.request, code))?.state).toBe("in-use");
  });

  test("the voucher field groups as you type and strips the grouping before submit", async ({
    page,
    context,
  }) => {
    const trace = await recordNetwork(page);
    const code = await freshVoucherCode(context.request);

    const lang = await openPortal(page);
    await page.goto(`/${lang}/voucher/`);

    const input = page.locator(portal.codeInput);
    /* Typed, not filled: grouping happens on `input` events, and `fill` sets the
       value in one shot. Typing is what a guest on a phone actually does. */
    await input.pressSequentially(code, { delay: 10 });

    /* Grouped for the eye: ABCD-EFGH-JKMN. The alphabet already excludes 0/O/1/I/L
       because this is read off a thermal receipt. */
    await expect(input).toHaveValue(/^[A-Z0-9]{4}-[A-Z0-9]{4}-[A-Z0-9]{4}$/);
    expect(ungroup(await input.inputValue())).toBe(code);

    await Promise.all([
      page.waitForURL(new RegExp(`/${lang}/connected/?$`)),
      page.locator(portal.submit).click(),
    ]);

    /* Ungrouped on the wire. The separators are display only; the contract carries
       the bare code so the server never has to guess which convention was used. */
    const post = trace.requests.find(
      (r) => r.method === "POST" && r.url.includes("/api/v0/portal/voucher"),
    );
    expect(post).toBeDefined();
    expect(formField(post!.postData, "code")).toBe(code);
    expect(formField(post!.postData, "code")).not.toContain("-");
  });

  test("a pasted code in any casing or spacing is accepted", async ({ page, context }) => {
    const code = await freshVoucherCode(context.request);
    /* Kept at 14 characters so `maxlength` cannot be what makes this pass. */
    const messy = `${code.slice(0, 4).toLowerCase()} ${code.slice(4, 8)}-${code.slice(8).toLowerCase()}`;

    const lang = await openPortal(page);
    await page.goto(`/${lang}/voucher/`);
    await page.locator(portal.codeInput).fill(messy);
    await page.locator(portal.submit).click();

    await expect(page).toHaveURL(new RegExp(`/${lang}/connected/?$`));
    expect((await voucherByCode(context.request, code))?.state).toBe("in-use");
  });

  /**
   * An already-used code returns to the form with a human message **and the value
   * still in the field**.
   *
   * The second half is the part that is easy to lose. There is no client state to
   * preserve it in — the portal is server-rendered and the CNA stores nothing — so
   * preserving the value is the *server's* job, and the frontend design says
   * so explicitly ("Server-side validation errors re-render the same document
   * with the value preserved and the message adjacent to the field").
   *
   * A guest who mistypes one character of twelve, on a phone, on a bad link, and is
   * handed an empty field, types all twelve again.
   */
  test("an already-used code returns to the form with a message and the value kept", async ({
    page,
    browser,
    context,
  }) => {
    const code = await freshVoucherCode(context.request);

    /* Burn the code in a separate context — a separate portal client, its own
       cookie jar, exactly as a second guest would be. */
    const other = await browser.newContext();
    try {
      const otherPage = await other.newPage();
      const otherLang = await openPortal(otherPage);
      await otherPage.goto(`/${otherLang}/voucher/`);
      await otherPage.locator(portal.codeInput).fill(code);
      await otherPage.locator(portal.submit).click();
      await expect(otherPage).toHaveURL(new RegExp(`/${otherLang}/connected/?$`));
    } finally {
      await other.close();
    }

    const lang = await openPortal(page);
    await page.goto(`/${lang}/voucher/`);
    await page.locator(portal.codeInput).fill(code);
    await page.locator(portal.submit).click();

    /* Back on the voucher form, not on a dead end. */
    await expect(page).toHaveURL(new RegExp(`/${lang}/voucher/`));

    /* A message the guest can act on, adjacent to the field, announced. */
    const error = page.locator(portal.error);
    await expect(error).toBeVisible();
    await expect(error).not.toBeEmpty();
    /* Not a status code, not an enum name leaking out of the redirect. */
    await expect(error).not.toHaveText(/already-used|not-found|^\d{3}$/);

    /*
     * And the value survived the round trip.
     *
     * Compared after ungrouping, not against the raw twelve characters: the server
     * re-renders the field *grouped* (`ABCD-EFGH-JKMN`), which is what the guest was
     * looking at and what the enhancement script would produce anyway. Asserting on
     * the bare form would fail on a presentation detail while the thing that matters
     * — that the guest does not retype twelve characters — is working.
     */
    const kept = await page.locator(portal.codeInput).inputValue();
    expect(
      ungroup(kept),
      "The voucher field was emptied by the failed submit. The frontend design " +
        "requires the value to be preserved: there is no client state to hold it, so " +
        "the server has to re-render it. A guest who mistyped one character retypes all twelve.",
    ).toBe(code);
  });

  test("the language switch changes the URL and keeps the step", async ({ page }) => {
    const lang = await openPortal(page);
    await page.goto(`/${lang}/voucher/`);

    const before = pathWithinLanguage(page.url());
    await page.locator(portal.languageSwitch).click();

    const after = languageOf(page.url());
    expect(after, "The language switch did not land on a language prefix.").not.toBeNull();
    expect(after, "The language switch did not change language.").not.toBe(lang);
    expect(pathWithinLanguage(page.url()), "The language switch lost the step.").toBe(before);

    /* And the document itself declares the new language, not just the URL. */
    await expect(page.locator("html")).toHaveAttribute("lang", after!);
  });
});
