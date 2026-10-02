import { expect, test } from "@playwright/test";
import { MOCK_ACCOUNT_PASSWORD } from "../helpers/env.js";
import { freshVoucherCode, voucherByCode } from "../helpers/mock.js";
import {
  accountLink,
  enabledAccountUsername,
  openPortal,
  portal,
  voucherLink,
} from "../helpers/portal.js";

/**
 * The portal flow with JavaScript switched off entirely.
 *
 * This is a first-class requirement, not a curiosity. The portal is a multi-page
 * application whose every step is a real document navigation, and script is
 * decoration that never carries the flow (see the frontend design). The
 * cheapest way for that to rot is for someone to add one convenience that quietly
 * becomes load-bearing — a `preventDefault`, a client-side route, a value only the
 * enhancement script writes into the form. With JS off, all three fail here rather
 * than on a guest's phone.
 *
 * It is also not hypothetical: the Android login WebView calls `clearCache(true)`
 * on every launch, so the script is re-fetched over the venue's worst link every
 * single time, and a script that fails to arrive must cost nothing.
 */
test.describe("portal, JavaScript disabled", () => {
  test.use({ javaScriptEnabled: false });

  test("welcome → voucher → connected completes with no script at all", async ({
    page,
    context,
  }) => {
    const code = await freshVoucherCode(context.request);

    /* Welcome page. */
    const lang = await openPortal(page);
    await expect(page.locator(portal.shell)).toBeVisible();
    await expect(page.locator(portal.heading)).toBeVisible();

    /* The choice of method is a link, so it works without script. */
    const toVoucher = voucherLink(page, lang);
    await expect(toVoucher).toBeVisible();
    await toVoucher.click();
    await expect(page).toHaveURL(new RegExp(`/${lang}/voucher/?$`));

    /* Voucher page. No grouping happens — that is the enhancement — so the raw
       twelve characters go in, and the server normalises. */
    const input = page.locator(portal.codeInput);
    await expect(input).toBeVisible();
    await input.fill(code);
    await page.locator(portal.submit).click();

    /* Connected page, reached by 303. */
    await expect(page).toHaveURL(new RegExp(`/${lang}/connected/?$`));
    await expect(page.locator(portal.connectedBadge)).toBeVisible();

    /* And the grant is real, not merely rendered. */
    const voucher = await voucherByCode(context.request, code);
    expect(voucher?.state).toBe("in-use");
  });

  test("the account flow also completes with no script", async ({ page, context }) => {
    const username = await enabledAccountUsername(context.request);

    const lang = await openPortal(page);
    const toAccount = accountLink(page, lang);
    await expect(toAccount).toBeVisible();
    await toAccount.click();
    await expect(page).toHaveURL(new RegExp(`/${lang}/account/?$`));

    await page.locator(portal.usernameInput).fill(username);
    await page.locator(portal.passwordInput).fill(MOCK_ACCOUNT_PASSWORD);
    await page.locator(portal.submit).click();

    await expect(page).toHaveURL(new RegExp(`/${lang}/connected/?$`));
    await expect(page.locator(portal.connectedBadge)).toBeVisible();
  });

  test("the language switch is a link, so it works with no script", async ({ page }) => {
    const lang = await openPortal(page);
    await page.goto(`/${lang}/voucher/`);

    const link = page.locator(portal.languageSwitch);
    await expect(link).toBeVisible();

    /* A real anchor with a real href — not a button that stores a preference. The
       CNA's data store is ephemeral, so the language has to live in the URL. */
    const href = await link.getAttribute("href");
    expect(href).toMatch(/^\/(en|vi)\/voucher\/$/);
    expect(href).not.toMatch(new RegExp(`^/${lang}/`));

    await link.click();
    expect(new URL(page.url()).pathname).toBe(href);
  });
});
