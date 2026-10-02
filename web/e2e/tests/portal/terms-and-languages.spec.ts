import { expect, test, type Page } from "@playwright/test";
import { PORTAL_LANGUAGES, PORTAL_URL, languageOf, type PortalLanguage } from "../helpers/env.js";
import { freshVoucherCode, portalContext } from "../helpers/mock.js";
import {
  expectedSwitcherLanguages,
  openPortal,
  portal,
  useVenueProfile,
} from "../helpers/portal.js";

/**
 * Two things the welcome page owes the guest, and one the venue must not be able to
 * take away.
 *
 * Neither assertion reads a word of copy. The consent sentence is located
 * structurally (`section.terms p.boundary`) and then *captured at runtime*, so the
 * "exactly once" count is computed against whatever the portal actually says today,
 * in whichever language, without this file knowing the sentence.
 */

/** Collapses the whitespace an HTML renderer introduces, so counting is meaningful. */
function flatten(text: string): string {
  return text.replace(/\s+/g, " ").trim();
}

async function bodyText(page: Page): Promise<string> {
  return flatten(await page.locator("body").innerText());
}

function occurrences(haystack: string, needle: string): number {
  if (!needle) return 0;
  let count = 0;
  let from = 0;
  for (;;) {
    const at = haystack.indexOf(needle, from);
    if (at === -1) return count;
    count += 1;
    from = at + needle.length;
  }
}

/**
 * The billing-and-consent-boundary sentence is Foyer's, not the venue's.
 *
 * `docs/rules/security.md` §6 requires the product to say that a captive portal is
 * a billing and consent boundary rather than a security one. As the last sentence of
 * the portal's *default* terms, any venue that supplied its own terms would delete it
 * silently, by doing the one thing the terms field exists for. So it is a separate key
 * rendered unconditionally beneath the venue's block.
 *
 * The defect that arrangement invites is the opposite one: rendering it *twice* in
 * the configuration where the default terms are also on screen. So the count is
 * asserted, not merely the presence — and it is asserted against a venue that
 * supplies its own terms, because that is the configuration where the sentence would
 * otherwise vanish and the configuration nobody reviews.
 */
test.describe("the consent sentence", () => {
  for (const lang of PORTAL_LANGUAGES) {
    test(`appears exactly once on the welcome page · ${lang}`, async ({ page, request }) => {
      const context = await portalContext(request);

      /* The premise, asserted rather than assumed. If the venue stopped supplying its
         own terms this test would still pass while testing nothing, because the
         default terms carry the sentence's own wording family. */
      const supplied = context.terms.summary[lang];
      expect(
        supplied,
        `The venue supplies no ${lang} terms, so this test cannot see the case it ` +
          "exists for: a venue whose own terms replace the portal's default.",
      ).toBeTruthy();

      await page.goto(`/${lang}/`);

      /* The venue's text is what is on screen — so the sentence below it is surviving
         a venue that wrote its own terms, not riding along inside the default. */
      const venueBlock = page.locator(portal.termsSummary);
      await expect(venueBlock).toHaveCount(1);
      expect(flatten(await venueBlock.innerText())).toBe(flatten(supplied!));

      const boundary = page.locator(portal.consentBoundary);
      await expect(
        boundary,
        "The billing-and-consent-boundary sentence is missing from the welcome page. " +
          "security.md §6 requires the product to say it, and a venue supplying its " +
          "own terms must not be able to remove it.",
      ).toHaveCount(1);

      /* Captured, not written down: the count below is against whatever the portal
         says today, in whichever language, and survives every rewording. */
      const sentence = flatten(await boundary.innerText());
      expect(sentence.length, "The consent sentence rendered empty.").toBeGreaterThan(20);

      expect(
        occurrences(await bodyText(page), sentence),
        "The consent sentence is on the welcome page more than once — it is being " +
          "rendered both on its own and inside the terms it was extracted from.",
      ).toBe(1);

      /* And it is a sibling of the venue's terms, never inside them. If it were
         inside, a venue that supplied terms would delete it again. */
      expect(
        occurrences(flatten(await venueBlock.innerText()), sentence),
        "The consent sentence is inside the venue's terms paragraph. That is the " +
          "arrangement that let a venue delete it by writing its own terms.",
      ).toBe(0);
    });
  }

  test("does not follow the guest past authentication", async ({ page, context }) => {
    /*
     * Consent is taken before the guest authenticates, because after authorization
     * the window may already be gone. The corollary is that repeating the
     * sentence afterwards is not extra honesty — it is a second consent moment for a
     * decision already made, on a page whose job is to say the network is up.
     *
     * A real redemption, so this is genuinely the post-authentication document and
     * not `/connected/` visited cold.
     */
    const code = await freshVoucherCode(context.request);

    const lang = await openPortal(page);
    const sentence = flatten(await page.locator(portal.consentBoundary).innerText());

    await page.goto(`/${lang}/voucher/`);
    await page.locator(portal.codeInput).fill(code);
    await page.locator(portal.submit).click();
    await expect(page).toHaveURL(new RegExp(`/${lang}/connected/?$`));
    await expect(page.locator(portal.connectedBadge)).toBeVisible();

    await expect(page.locator(portal.terms)).toHaveCount(0);
    await expect(page.locator(portal.consentBoundary)).toHaveCount(0);
    expect(
      occurrences(await bodyText(page), sentence),
      "The consent sentence is repeated on the success page. Consent is taken before " +
        "authentication; restating it afterwards asks for a decision already made.",
    ).toBe(0);
  });

  for (const step of ["voucher/", "account/"]) {
    test(`is not repeated on the ${step} step`, async ({ page }) => {
      await page.goto(`/en/${step}`);
      await expect(
        page.locator(portal.terms),
        "The terms block belongs on the welcome page only. Repeating it turns every " +
          "step into a consent gate and pushes the one control on the screen down.",
      ).toHaveCount(0);
    });
  }
});

/**
 * The language switcher offers what the venue advertises.
 *
 * A switcher that renders a hardcoded pair is wrong in both directions: it offers a
 * language a single-language venue never asked for, and it cannot grow. The portal
 * intersects the venue's advertised list with the languages it has strings for, keeps
 * the one being read, and renders the rest.
 *
 * **What this cannot currently prove, stated so nobody reads more into a green run
 * than is there.** The discriminating case is a venue that advertises *one* language
 * and therefore gets *no control at all* — and the mock advertises `["en", "vi"]`
 * with no way to vary it, so the expectation below is derived from the venue's list
 * but the venue's list only ever has one value. Making that case reachable means a
 * mock fixture whose `languages` can be overridden per request, the way `?present=0`
 * already overrides driver confirmation. That is a change in `web/mock`, which this
 * package does not own.
 */
test.describe("the language switcher", () => {
  for (const lang of PORTAL_LANGUAGES) {
    test(`offers exactly the venue's other languages · ${lang}`, async ({ page, request }) => {
      const context = await portalContext(request);
      const expected = expectedSwitcherLanguages(context.languages, lang);

      await page.goto(`/${lang}/`);

      const links = page.locator(portal.languageSwitch);
      const hrefs = await links.evaluateAll((els) =>
        els.map((el) => (el as HTMLAnchorElement).getAttribute("href")),
      );

      expect(
        hrefs,
        "The language switcher does not match what the venue advertises. It renders " +
          "one link per language the venue offers and the portal has strings for, " +
          "minus the one being read — never a hardcoded pair.",
      ).toEqual(expected.map((l) => `/${l}/`));

      /* Each link is labelled and tagged in its own language: the person who needs
         this control is by definition reading a page in a language they may not know,
         so `lang`/`hreflang` are what let a screen reader pronounce the endonym. */
      for (const other of expected) {
        const link = links.filter({ has: page.locator(`xpath=.`) }).nth(expected.indexOf(other));
        await expect(link).toHaveAttribute("lang", other);
        await expect(link).toHaveAttribute("hreflang", other);
        expect(flatten(await link.innerText()).length).toBeGreaterThan(0);
      }

      /* Never a link to the page you are already on. */
      await expect(page.locator(`${portal.languageSwitch}[href^="/${lang}/"]`)).toHaveCount(0);
    });
  }

  test("keeps the step it was used from", async ({ page, request }) => {
    const context = await portalContext(request);
    const lang: PortalLanguage = "en";
    const expected = expectedSwitcherLanguages(context.languages, lang);
    test.skip(expected.length === 0, "This venue advertises one language; there is no switcher.");

    await page.goto(`/${lang}/voucher/`);
    const hrefs = await page
      .locator(portal.languageSwitch)
      .evaluateAll((els) => els.map((el) => (el as HTMLAnchorElement).getAttribute("href")));

    expect(hrefs).toEqual(expected.map((l) => `/${l}/voucher/`));
  });
});

/**
 * The venue profiles, driven end to end.
 *
 * Three of the four decisions the welcome page makes about terms and languages are
 * reachable from the running product only through the mock's venue profiles, so
 * this is where they are seen end to end rather than only in unit tests.
 *
 * Every assertion here is structural or computed from the venue's own advertisement.
 * None reads a word of copy — the catalogues are still being edited, and a test that
 * fails on a rewording is a test that gets deleted.
 */
test.describe("what the venue advertises decides what the guest gets", () => {
  test("a venue advertising one language renders no switcher at all", async ({
    page,
    context,
    request,
  }) => {
    await useVenueProfile(context, "solo");

    const advertised = (await portalContext(request, "solo")).languages;
    expect(
      advertised,
      "the `solo` profile no longer advertises exactly one language, so this proves nothing",
    ).toHaveLength(1);

    await page.goto(`${PORTAL_URL}/${advertised[0]}/`);
    /* No control, not a disabled one — the same rule the admin follows for a driver
       capability. A disabled switcher advertises a language the venue does not have. */
    await expect(page.locator(portal.languageSwitch)).toHaveCount(0);
  });

  test("the root redirect honours the venue's stock over the guest's preference", async ({
    browser,
  }) => {
    /*
     * The defect this locks: a root redirect to the default language regardless of the
     * venue means **a venue advertising only Vietnamese greets its guests in
     * English** — the one screen a single-language venue exists not to have.
     *
     * A fresh context with an explicitly English browser, because that is the guest
     * such a redirect would send to the wrong place.
     */
    const context = await browser.newContext({ locale: "en-GB" });
    try {
      await useVenueProfile(context, "solo");
      const page = await context.newPage();
      const advertised = (await portalContext(context.request, "solo")).languages;

      await page.goto(`${PORTAL_URL}/`);
      expect(
        languageOf(page.url()),
        `An en-GB browser at a venue advertising ${advertised.join(", ")} landed on ` +
          `${page.url()}. The negotiation is Accept-Language *intersected with what ` +
          `the venue advertises*.`,
      ).toBe(advertised[0]);
    } finally {
      await context.close();
    }
  });

  test("terms in a language the guest cannot read are rendered, and marked as what they are", async ({
    page,
    context,
    request,
  }) => {
    await useVenueProfile(context, "foreign-terms");

    const ctx = await portalContext(request, "foreign-terms");
    const supplied = Object.keys(ctx.terms?.summary ?? {});
    expect(
      supplied,
      "the `foreign-terms` profile no longer supplies exactly one language of terms",
    ).toHaveLength(1);
    const written = supplied[0]!;
    const reader = PORTAL_LANGUAGES.find((l) => l !== written)!;

    await page.goto(`${PORTAL_URL}/${reader}/`);
    const summary = page.locator(portal.termsSummary);

    /* Rendered, not dropped: showing terms in the wrong language is bad and showing
       none is worse. */
    await expect(summary).toBeVisible();
    await expect(summary).not.toBeEmpty();

    /* And marked. The `lang` attribute is the entire mechanism that lets a screen
       reader pronounce it and a browser offer to translate it — the portal must not
       translate it itself, so tagging is the only honest thing left to do. */
    await expect(
      summary,
      `Terms written in ${written} rendered inside a ${reader} document without a lang attribute.`,
    ).toHaveAttribute("lang", written);
  });

  test("a venue that configured no terms still shows terms", async ({
    page,
    context,
    request,
  }) => {
    await useVenueProfile(context, "no-terms");

    const ctx = await portalContext(request, "no-terms");
    expect(Object.keys(ctx.terms?.summary ?? {})).toHaveLength(0);

    await page.goto(`${PORTAL_URL}/en/`);
    /* The catalogue default. This is what keeps the consent sentence on screen in a
       deployment nobody finished configuring — which is most of them, at first. */
    await expect(page.locator(portal.termsSummary)).not.toBeEmpty();
    await expect(page.locator(portal.consentBoundary)).toHaveCount(1);
  });
});
