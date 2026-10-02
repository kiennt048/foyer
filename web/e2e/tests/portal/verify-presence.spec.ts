import { expect, test } from "@playwright/test";
import { MOCK_URL } from "../helpers/env.js";
import { freshVoucherCode, portalStatus, voucherByCode } from "../helpers/mock.js";
import { openPortal, portal } from "../helpers/portal.js";

/**
 * `VerifyPresence` — `docs/rules/security.md` §4, made visible.
 *
 * UniFi passes `ap`, `id`, `t`, `url` and `ssid` to the portal as **unsigned query
 * parameters**. Anyone can call the portal with values of their choosing. Without a
 * driver confirming the MAC is really associated, on the claimed AP, in the claimed
 * site, and still unauthorized, the portal is a public API for authorizing arbitrary
 * MACs — including devices that never associated at all.
 *
 * The mock models this: `?present=0` produces a client the driver will not confirm.
 * That is what a portal called with forged NAS parameters looks like from the
 * inside, so the rejection path is reviewable in the prototype rather than merely
 * asserted in prose.
 *
 * The load-bearing assertion is not the error message. It is that **the voucher was
 * not consumed and no grant exists** — a portal that showed an error and created the
 * grant anyway would pass a message-only test.
 */
test.describe("a client the driver will not confirm", () => {
  test("is refused, and no grant is created", async ({ page, context }) => {
    const code = await freshVoucherCode(context.request);

    /*
     * The unverified client is established through the mock's own entry point,
     * before the browser touches the portal at all. That ordering matters: the very
     * first portal request creates the client, so anything that reached the portal
     * first would have created a *verified* one and quietly made this test vacuous.
     *
     * `context.request` shares the browser context's cookie jar, so the client id
     * issued here is the one the page will present.
     */
    const contextRes = await context.request.get(`${MOCK_URL}/api/v0/portal/context?present=0`);
    expect(
      contextRes.status(),
      "The portal context endpoint served a client the driver would not confirm.",
    ).toBe(403);

    await page.goto("/en/voucher/");
    await page.locator(portal.codeInput).fill(code);
    await page.locator(portal.submit).click();

    /* Rejected back to the form, not carried forward to the success document. */
    await expect(page).toHaveURL(/\/en\/voucher\//);
    await expect(page).not.toHaveURL(/\/connected\//);

    const error = page.locator(portal.error);
    await expect(error).toBeVisible();
    await expect(error).not.toBeEmpty();

    /* The message must not say *which* check failed. Telling an attacker whether the
       MAC, the AP or the site was wrong turns the portal into an oracle. */
    await expect(error).not.toHaveText(/\bMAC\b|\baccess point\b|\bsite\b|\bnas\b/i);

    /* The two facts that matter. */
    const voucher = await voucherByCode(context.request, code);
    expect(
      voucher?.state,
      "The voucher was consumed for a device the driver never confirmed. Without " +
        "VerifyPresence the portal is a public API for authorizing arbitrary MACs.",
    ).toBe("unused");

    const status = await portalStatus(context.request);
    expect(status.status, "A grant exists for an unverified client.").toBe(404);
  });

  test("a verified client in the same run is unaffected", async ({ browser, context }) => {
    /* The counterpart, so the test above cannot pass because the whole portal is
       broken. Same server, same fixtures, a client the driver does confirm. */
    const code = await freshVoucherCode(context.request);

    const verified = await browser.newContext();
    try {
      const page = await verified.newPage();
      const lang = await openPortal(page);
      await page.goto(`/${lang}/voucher/`);
      await page.locator(portal.codeInput).fill(code);
      await page.locator(portal.submit).click();
      await expect(page).toHaveURL(new RegExp(`/${lang}/connected/?$`));
    } finally {
      await verified.close();
    }

    expect((await voucherByCode(context.request, code))?.state).toBe("in-use");
  });

});

/**
 * The NAS parameters have to survive the front door.
 *
 * A guest arrives at the portal root carrying whatever the NAS appended —
 * `ap`, `id`, `t`, `url`, `ssid` for UniFi (`docs/protocol-reality.md`). The portal
 * root immediately redirects to a language prefix, and that redirect must carry the
 * whole query string. Dropping it has two consequences:
 *   • `VerifyPresence` has nothing to verify against — the claimed MAC, AP and site
 *     never reach the application. The check that `security.md` §4 makes mandatory
 *     cannot be wired up at all on the arrival path.
 *   • `url` is lost, so the "continue to where you were going" link on the success
 *     page can never be populated from a real arrival.
 *
 * The redirect is `portal/plugin/dev.ts`'s `302` to `/{lang}/` carrying `url.search`,
 * and this is the guard on it. Every parameter
 * is asserted individually rather than the query string compared as one blob: a
 * redirect that kept `id` and dropped `url` would break the success page only, and
 * a single blob comparison would report "the query changed" without saying what.
 *
 * **What this cannot cover, deliberately.** These tests run against the server that
 * answers `/` — the dev middleware here, the Go binary in production. The *static*
 * build artefact cannot do this and is not expected to: `portal/dist/index.html` is
 * a file, a file cannot see a request, and its `<meta http-equiv="refresh">` fallback
 * necessarily lands on a bare `/en/`. `build/build.ts` says so at the point of
 * writing that file, and pairs the meta with a script that does preserve the query.
 * So the static artefact preserves the query **only with script**, which is why the
 * no-script case below is named separately: it is the case that proves the origin
 * root is served by something that reads the request, not by a file.
 */
test.describe("the NAS-supplied query string", () => {
  /** What a UniFi arrival actually carries. Decoded values, so the assertions read. */
  const ARRIVAL = {
    ap: "00:11:22:33:44:55",
    id: "aa:bb:cc:dd:ee:ff",
    t: "1754700000",
    ssid: "Foyer Guest",
    url: "http://example.com/somewhere",
  } as const;

  const arrivalPath = `/?${new URLSearchParams(ARRIVAL)}`;

  function expectIntact(landed: URL, note: string) {
    expect(landed.pathname, "The portal root did not redirect to a language prefix.").toMatch(
      /^\/(en|vi)\/$/,
    );
    for (const [name, value] of Object.entries(ARRIVAL)) {
      expect(
        landed.searchParams.get(name),
        `The language redirect dropped or mangled \`${name}\` (${note}). VerifyPresence ` +
          "has nothing left to verify, and the original URL is gone.",
      ).toBe(value);
    }
    /* Nothing invented on the way through either. The redirect is transport for
       attacker-controlled values; adding one would be the portal asserting a fact it
       does not have. */
    expect([...landed.searchParams.keys()].sort()).toEqual(Object.keys(ARRIVAL).sort());
  }

  test("survives the language redirect", async ({ page }) => {
    await page.goto(arrivalPath);
    expectIntact(new URL(page.url()), "script enabled");
  });

  /**
   * The same arrival with no script at all.
   *
   * This is the one that says *where* the preservation happens. A script on the
   * landing page can re-append `location.search`, and that is exactly what the static
   * artefact does — but the Android login WebView calls `clearCache(true)` on every
   * launch, so the script is re-fetched over the venue's worst link every time, and a
   * guest whose script did not arrive would then be a guest `VerifyPresence` cannot
   * check. The redirect therefore has to be the server's, and with JavaScript off
   * there is nowhere else for it to come from.
   */
  test.describe("with no script", () => {
    test.use({ javaScriptEnabled: false });

    test("still survives the language redirect", async ({ page }) => {
      await page.goto(arrivalPath);
      expectIntact(new URL(page.url()), "JavaScript disabled");
    });
  });
});
