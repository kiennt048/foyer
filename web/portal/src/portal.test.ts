import { describe, expect, it } from "vitest";
import type { Language, PortalStatus } from "@foyer/contract";
import { en } from "./i18n/en.js";
import { vi } from "./i18n/vi.js";
import {
  DEFAULT_LANGUAGE,
  LANGUAGES,
  languageName,
  negotiateLanguage,
  otherLanguage,
  resolveLanguages,
} from "./i18n/index.js";
import { renderDocument } from "./document.js";
import { renderPage } from "./render.js";
import { PAGES } from "./pages/index.js";

/** Derived, never hardcoded, so flipping the default cannot leave a test behind. */
const LANGS: readonly Language[] = LANGUAGES;

function documentFor(lang: Language, path: string): string {
  const page = renderPage({ lang, path, context: null, status: null, error: null });
  if (!page) throw new Error(`no page at ${path}`);
  return renderDocument({ lang, title: page.title, body: page.body, css: "" });
}

/**
 * These are the named portal tests. Each one guards a
 * failure that is either invisible in a browser or fatal on real hardware, and
 * none may be deleted without a replacement.
 */
describe("the portal survives the captive browser", () => {
  it("submits by form navigation, never by fetch", () => {
    // The one that matters most. The CNA re-checks captivity only after a full URL
    // navigation; a fetch-submitted login leaves it showing "Cancel", which
    // disassociates the device. The guest is authenticated and then thrown off the
    // network — and Playwright reports a pass.
    for (const lang of LANGS) {
      for (const path of ["voucher/", "account/"]) {
        const html = documentFor(lang, path);
        expect(html).toContain('method="post"');
        expect(html).toMatch(/action="\/api\/v0\/portal\/(voucher|account)"/);
      }
    }
  });

  it("carries the language as a submitted field, not as an assumption", () => {
    // The form POST is answered by a server that redirects back into a language
    // prefix. It learns the language from this hidden field, so the flow returns to
    // the language the guest was reading whatever any server-side default is.
    for (const lang of LANGS) {
      for (const path of ["voucher/", "account/"]) {
        expect(documentFor(lang, path)).toContain(`name="lang" value="${lang}"`);
      }
    }
  });

  it("opens no new windows and offers no unsupported scheme", () => {
    // target=_blank replaces the page with no back affordance on Android and cannot
    // open a window in the CNA at all. mailto:/intent:// hit a full-page Android
    // security interstitial. tel: and sms: are the only non-http schemes that work.
    for (const lang of LANGS) {
      for (const page of PAGES) {
        const html = documentFor(lang, page.path);
        expect(html).not.toContain("_blank");
        expect(html).not.toContain("mailto:");
        expect(html).not.toContain("intent://");
        expect(html).not.toContain("market://");
        expect(html).not.toContain("window.open");
      }
    }
  });

  it("requests nothing from outside itself", () => {
    // No CDN, no font host, no analytics. Every external resource would also have
    // to be added to the walled garden on every driver, which is the reason not to
    // have one rather than a reason to maintain a list.
    for (const lang of LANGS) {
      for (const page of PAGES) {
        const html = documentFor(lang, page.path);
        expect(html).not.toMatch(/(?:src|href)="https?:\/\//);
        expect(html).not.toContain("//fonts.");
      }
    }
  });

  it("carries the metas the captive WebViews need", () => {
    for (const lang of LANGS) {
      const html = documentFor(lang, "");
      // Without this Android lays out at ~980px and scales to fit.
      expect(html).toContain('name="viewport"');
      expect(html).toContain("width=device-width");
      // What makes env(safe-area-inset-*) mean anything at all.
      expect(html).toContain("viewport-fit=cover");
      // Android never auto-darkens the captive-portal WebView: it calls neither
      // setForceDark nor setAlgorithmicDarkeningAllowed.
      expect(html).toContain('<meta name="color-scheme" content="dark light">');
      expect(html).toContain(`<html lang="${lang}"`);
    }
  });

  it("puts the terms before authentication, not after", () => {
    // Android destroys the login WebView the instant the network validates, so
    // anything shown after authorization may never be seen.
    const welcome = renderPage({
      lang: DEFAULT_LANGUAGE,
      path: "",
      context: {
        venueName: "V",
        methods: ["voucher"],
        languages: [DEFAULT_LANGUAGE],
        terms: { summary: { [DEFAULT_LANGUAGE]: "SAMPLE TERMS" } },
      },
      status: null,
      error: null,
    })!;
    expect(welcome.body).toContain("SAMPLE TERMS");

    const connected = renderPage({
      lang: DEFAULT_LANGUAGE,
      path: "connected/",
      context: null,
      status: null,
      error: null,
    })!;
    expect(connected.body).not.toContain("SAMPLE TERMS");
  });

  it("states the terms even when the venue supplies none", () => {
    // An empty terms section is worse than no section: the guest consents to
    // nothing legible. The catalogue default is what a deployment that never
    // configured terms actually shows.
    for (const lang of LANGS) {
      const body = renderPage({ lang, path: "", context: null, status: null, error: null })!.body;
      expect(body, `${lang} welcome`).toContain(
        lang === "en" ? en["welcome.termsSummary"] : vi["welcome.termsSummary"],
      );
    }
  });

  it("resolves the venue's terms by language, and never translates them", () => {
    /*
     * The named test for the fallback chain. Terms are legal text: the portal picks
     * which language to show, and may not paraphrase, machine-translate, or let the
     * markup claim text is in a language it is not.
     *
     *   1. the venue's text for the language being read wins;
     *   2. failing that, another language it supplied, carrying `lang` with it;
     *   3. failing that, the portal's own catalogue default.
     */
    const welcomeWith = (lang: Language, summary: Record<string, string>) =>
      renderPage({
        lang,
        path: "",
        context: { venueName: "V", methods: ["voucher"], languages: LANGS.slice(), terms: { summary } },
        status: null,
        error: null,
      })!.body;

    // 1 — both languages supplied, so each reader gets their own and neither page
    // carries the other's text. This is the case the language switcher exists for.
    const both = { en: "VENUE TERMS EN", vi: "VENUE TERMS VI" };
    const en1 = welcomeWith("en", both);
    expect(en1).toContain("VENUE TERMS EN");
    expect(en1).not.toContain("VENUE TERMS VI");
    const vi1 = welcomeWith("vi", both);
    expect(vi1).toContain("VENUE TERMS VI");
    expect(vi1).not.toContain("VENUE TERMS EN");
    // Same language as the document, so no redundant attribute on the terms.
    // (The masthead's language switch carries its own `lang`; this is the terms.)
    expect(en1).toContain("<p>VENUE TERMS EN</p>");

    // 2 — the venue supplied Vietnamese only. An English reader sees it rather than
    // an empty terms section, and it is tagged as Vietnamese so a screen reader
    // pronounces it and the browser can offer to translate what the portal must not.
    const viOnly = welcomeWith("en", { vi: "VENUE TERMS VI" });
    expect(viOnly).toContain('<p lang="vi">VENUE TERMS VI</p>');
    // Not silently substituted for the portal's own text, and not translated.
    expect(viOnly).not.toContain(en["welcome.termsSummary"]);

    // 3 — an empty map is a venue that configured nothing, and is not the same as a
    // venue that configured an empty string. Both land on the catalogue default.
    for (const lang of LANGS) {
      const fallback = welcomeWith(lang, {});
      const expected = lang === "en" ? en["welcome.termsSummary"] : vi["welcome.termsSummary"];
      expect(fallback, `${lang} empty map`).toContain(expected);
      expect(welcomeWith(lang, { [lang]: "   " }), `${lang} blank string`).toContain(expected);
    }
  });

  it("says the portal is a billing and consent boundary, not a security one", () => {
    // `docs/rules/security.md` §6: the captive portal is a billing and consent
    // boundary, not a security boundary, and user-facing docs must say so. It is
    // the honest sentence every other captive portal leaves out, and a copy pass
    // that quietly drops it is exactly what this test exists to catch.
    //
    // It is asserted against `welcome.boundary`, not `welcome.termsSummary`: the
    // terms are the *venue's*, and a venue that supplies its own replaces them
    // wholesale. The string has to exist and say all three things, in a key the
    // venue cannot reach. The rendering guarantee is the test below.
    expect(en["welcome.boundary"]).toMatch(/billing and consent boundary/);
    expect(en["welcome.boundary"]).toMatch(/not a security boundary/);
    expect(en["welcome.boundary"]).toMatch(/HTTPS/);
    expect(vi["welcome.boundary"]).toMatch(/HTTPS/);
    expect(vi["welcome.boundary"]).toMatch(/ranh giới bảo mật/);
  });

  it("renders the boundary sentence whatever the venue does with its terms, and once", () => {
    /*
     * The named test for the boundary sentence.
     *
     * The boundary sentence is Foyer's, is owed to the guest, and is not the venue's
     * to remove. As the tail of the default terms it would be present exactly when
     * nobody had configured anything, and silently deleted the moment a venue did the
     * ordinary thing the terms field exists for. Three cases,
     * because those are the three shapes a deployment can be in, and the failure of
     * the naive fix (render it *and* keep it in the default) is a portal that says
     * it twice on the one configuration reviewers look at most.
     */
    const count = (haystack: string, needle: string) => haystack.split(needle).length - 1;

    const welcome = (lang: Language, summary?: Record<string, string>) =>
      renderPage({
        lang,
        path: "",
        context: summary
          ? { venueName: "V", methods: ["voucher"], languages: LANGS.slice(), terms: { summary } }
          : null,
        status: null,
        error: null,
      })!.body;

    for (const lang of LANGS) {
      const sentence = lang === "en" ? en["welcome.boundary"] : vi["welcome.boundary"];

      // 1 — no venue terms at all. The catalogue default is on screen, and the
      // boundary line is on screen once, not twice.
      expect(count(welcome(lang), sentence), `${lang}: no venue terms`).toBe(1);

      // 2 — the venue wrote its own terms, in this language, and said nothing about
      // any of this. That is the case that would delete a sentence kept in the terms.
      const own = welcome(lang, { [lang]: "HOUSE RULES. NO REFUNDS." });
      expect(own).toContain("HOUSE RULES. NO REFUNDS.");
      expect(count(own, sentence), `${lang}: venue supplied its own terms`).toBe(1);

      // 3 — the venue's terms are in *another* language, so the terms block falls
      // back and carries that language with it. The boundary line does not: it is
      // the portal's own sentence and the portal has it in the language being read.
      const foreign = welcome(lang, { [otherLanguage(lang)]: "FOREIGN TERMS" });
      expect(foreign).toContain(`lang="${otherLanguage(lang)}">FOREIGN TERMS`);
      expect(count(foreign, sentence), `${lang}: venue terms in another language`).toBe(1);
    }

    // And it is not shipped on the pages that come after authorization — Android
    // destroys the login WebView the moment the network validates, so a guest may
    // never see them. Consent belongs before the grant, which is where this is.
    for (const path of ["voucher/", "account/", "connected/", "problem/"]) {
      const body = renderPage({
        lang: DEFAULT_LANGUAGE,
        path,
        context: null,
        status: null,
        error: null,
      })!.body;
      expect(body, `${path} should not repeat the boundary line`).not.toContain(
        en["welcome.boundary"],
      );
    }
  });
});

describe("the portal does not promise what the protocol cannot deliver", () => {
  it("labels a data allowance as an allowance, never as a remaining balance", () => {
    const page = renderPage({
      lang: DEFAULT_LANGUAGE,
      path: "connected/",
      context: null,
      error: null,
      status: {
        state: "connected",
        plan: { name: "Test", durationMinutes: 60, dataAllowanceMb: 500, enforcedBy: "nas" },
        venueName: "V",
        grantedAt: new Date().toISOString(),
        expiresAt: null,
        originalUrl: null,
      },
    })!;
    // Every byte figure travels with its qualifier. There is no freshness at which
    // a remaining-bytes number is true: radacct advances only on Interim-Update or
    // Stop, with a 300-second floor, and often never arrives at all.
    expect(page.body).toContain(en["connected.allowanceNote"]);
  });

  it("says so when nothing is enforcing the stated limit", () => {
    const page = renderPage({
      lang: DEFAULT_LANGUAGE,
      path: "connected/",
      context: null,
      error: null,
      status: {
        state: "connected",
        plan: { name: "Test", durationMinutes: null, dataAllowanceMb: 10_000, enforcedBy: "unenforced" },
        venueName: "V",
        grantedAt: new Date().toISOString(),
        expiresAt: null,
        originalUrl: null,
      },
    })!;
    expect(page.body).toContain(en["connected.unenforcedNote"]);
  });

  it("qualifies an unenforced time limit even when there is no data figure", () => {
    // The success page may state a time limit as a cap only if the NAS is enforcing
    // it. A plan with a duration, no allowance and `unenforced` must not render the
    // number with no qualifier at all — a limit presented as a cap that nothing on
    // the network will apply.
    const page = renderPage({
      lang: DEFAULT_LANGUAGE,
      path: "connected/",
      context: null,
      error: null,
      status: {
        state: "connected",
        plan: { name: "Test", durationMinutes: 120, dataAllowanceMb: null, enforcedBy: "unenforced" },
        venueName: "V",
        grantedAt: new Date().toISOString(),
        expiresAt: null,
        originalUrl: null,
      },
    })!;
    expect(page.body).toContain(en["connected.unenforcedNote"]);
    // The allowance sentence belongs to a byte figure and there is none here.
    expect(page.body).not.toContain(en["connected.allowanceNote"]);
  });

  it("says a sampled figure is sampled, and how far behind it can be", () => {
    const page = renderPage({
      lang: DEFAULT_LANGUAGE,
      path: "connected/",
      context: null,
      error: null,
      status: {
        state: "connected",
        plan: { name: "Test", durationMinutes: 1440, dataAllowanceMb: 5_000, enforcedBy: "foyer-sampled" },
        venueName: "V",
        grantedAt: new Date().toISOString(),
        expiresAt: null,
        originalUrl: null,
      },
    })!;
    expect(page.body).toContain(en["connected.sampledNote"]);
    expect(en["connected.sampledNote"]).toMatch(/sampled/i);
    expect(en["connected.sampledNote"]).toMatch(/minutes behind/i);
  });

  it("renders the return destination as a link, never as an automatic bounce", () => {
    const page = renderPage({
      lang: DEFAULT_LANGUAGE,
      path: "connected/",
      context: null,
      error: null,
      status: {
        state: "connected",
        plan: { name: "T", durationMinutes: 60, dataAllowanceMb: null, enforcedBy: "nas" },
        venueName: "V",
        grantedAt: new Date().toISOString(),
        expiresAt: null,
        originalUrl: "http://vnexpress.net/",
      },
    })!;
    // The victim of an open redirect here is on an untrusted network, expects to be
    // redirected, and has just typed credentials.
    expect(page.body).toContain("vnexpress.net");
    expect(page.body).not.toContain("http-equiv=\"refresh\"");
    expect(page.body).not.toContain("location.replace");
  });

  it("never renders a singular quantity with a plural unit", () => {
    // "1 hours" is the cheapest possible tell that nobody read the copy.
    const page = renderPage({
      lang: "en",
      path: "connected/",
      context: null,
      error: null,
      status: {
        state: "connected",
        plan: { name: "T", durationMinutes: 60, dataAllowanceMb: null, enforcedBy: "nas" },
        venueName: "V",
        grantedAt: new Date().toISOString(),
        expiresAt: null,
        originalUrl: null,
      },
    })!;
    expect(page.body).toContain("1 hour");
    expect(page.body).not.toContain("1 hours");
  });
});

describe("i18n", () => {
  it("defaults to English, the source of truth", () => {
    // `en.ts` defines MessageKey; `vi.ts` is typed as a total map of it. A key added
    // in English and missing in Vietnamese is a compile error, not a runtime gap.
    expect(DEFAULT_LANGUAGE).toBe("en");
    expect(LANGUAGES[0]).toBe("en");
    expect(otherLanguage("en")).toBe("vi");
    expect(otherLanguage("vi")).toBe("en");
  });

  it("has no missing key in either language", () => {
    const enKeys = Object.keys(en).sort();
    const viKeys = Object.keys(vi).sort();
    // A missing key must be a build error, not a string that silently renders in
    // the wrong language on a screen the guest sees for eleven seconds.
    expect(viKeys).toEqual(enKeys);
    for (const [key, value] of Object.entries(en)) {
      expect(value.trim(), `en.${key} is empty`).not.toBe("");
    }
    for (const [key, value] of Object.entries(vi)) {
      expect(value.trim(), `vi.${key} is empty`).not.toBe("");
    }
  });

  it("keeps the English copy to the rules the spec sets", () => {
    // No "please" — it lengthens every string without adding meaning, on a screen
    // read one-handed in daylight. And every error names a next action, which in
    // practice means more than one sentence.
    for (const [key, value] of Object.entries(en)) {
      expect(value.toLowerCase(), `en.${key} says "please"`).not.toContain("please");
    }
    for (const [key, value] of Object.entries(en)) {
      if (!key.startsWith("error.")) continue;
      expect(value.split(/(?<=\.)\s+/).length, `en.${key} names no next action`).toBeGreaterThan(1);
    }
  });

  /*
   * **A new reason whose copy repeats an old reason's is a fix that changed
   * nothing**, and every other lock on this catalogue would stay green through
   * it: the key exists, it is non-empty, it names a next action, and both
   * languages have it.
   *
   * `error.ap-unconfirmed` is separate from `error.not-present` precisely because
   * one sentence cannot be true of both — a guest under an access point the venue
   * will not vouch for **is** on the network. Pasting the `not-present` sentence
   * under this key would merge them again, silently. Asserted in both languages,
   * because a translation that fell back to the neighbouring string is the same
   * defect arriving through the other file.
   */
  it("does not tell an unconfirmed guest what it tells an absent one", () => {
    for (const [lang, catalogue] of [
      ["en", en],
      ["vi", vi],
    ] as const) {
      expect(
        catalogue["error.ap-unconfirmed"],
        `${lang}: ap-unconfirmed repeats not-present, so the two situations read identically`,
      ).not.toBe(catalogue["error.not-present"]);
    }
  });

  it("renders every page in every language", () => {
    for (const lang of LANGS) {
      for (const page of PAGES) {
        const rendered = renderPage({ lang, path: page.path, context: null, status: null, error: null });
        expect(rendered, `${lang}/${page.path}`).not.toBeNull();
        expect(rendered!.body.length).toBeGreaterThan(100);
      }
    }
  });

  it("offers the other language on every page", () => {
    // Language is a URL path segment, not a cookie: the CNA's data store is
    // ephemeral, so a stored preference does not survive the window.
    for (const page of PAGES) {
      for (const lang of LANGS) {
        expect(documentFor(lang, page.path)).toContain(
          `href="/${otherLanguage(lang)}/${page.path}"`,
        );
      }
    }
  });
});

describe("the language switcher offers what the venue offers", () => {
  const welcomeFor = (lang: Language, languages: Language[] | undefined) =>
    renderPage({
      lang,
      path: "",
      context: languages
        ? { venueName: "V", methods: ["voucher"], languages, terms: { summary: {} } }
        : null,
      status: null,
      error: null,
    })!.body;

  it("hides the switcher entirely when the venue offers one language", () => {
    /*
     * A venue that speaks one language got an English switcher anyway, because the
     * control was wired to `otherLanguage(lang)` and `PortalContext.languages` was
     * consumed by nothing. The rule is the one the admin UI already follows for
     * driver capabilities: remove the control, never render one that does nothing.
     */
    for (const lang of LANGS) {
      const body = welcomeFor(lang, [lang]);
      expect(body, `${lang}: single-language venue`).not.toContain("lang-switch");
      expect(body).not.toContain(`/${otherLanguage(lang)}/`);
    }
  });

  it("offers each language it does not currently show, labelled in its own name", () => {
    // The endonym is the only label that works: the reader who needs the switch is
    // by definition reading a language they may not know, so "Vietnamese" is no use
    // to somebody looking for Tiếng Việt.
    for (const lang of LANGS) {
      const other = otherLanguage(lang);
      const body = welcomeFor(lang, LANGS.slice());
      expect(body).toContain(`href="/${other}/"`);
      expect(body).toContain(languageName(other));
      // Never a link to the page the guest is already on.
      expect(body).not.toContain(`class="lang-switch" href="/${lang}/"`);
    }
  });

  it("ignores a language the portal has no catalogue for, and always keeps the current one", () => {
    // What a venue advertises is a wish; what the portal can render is a fact. An
    // unknown code offered in the switcher is a 404 or a page of `undefined`.
    expect(resolveLanguages("en", ["en", "vi", "fr" as Language])).toEqual(["en", "vi"]);
    expect(resolveLanguages("en", ["fr" as Language])).toEqual(["en"]);
    // The guest is looking at this language whatever the venue says about it.
    expect(resolveLanguages("vi", ["en"])).toEqual(["en", "vi"]);
    // Order follows the catalogue, so the control does not reshuffle per deployment.
    expect(resolveLanguages("en", ["vi", "en"])).toEqual(["en", "vi"]);
    // No context at all — the static build — offers everything the portal ships.
    expect(resolveLanguages("en", undefined)).toEqual(LANGUAGES.slice());
  });
});

describe("a rejected voucher code comes back in the field", () => {
  const voucherPage = (error: string | null, code: string | null) =>
    renderPage({
      lang: DEFAULT_LANGUAGE,
      path: "voucher/",
      context: null,
      status: null,
      error,
      code,
    })!.body;

  /**
   * Just the `<input class="code-input">` tag.
   *
   * Asserting `value=` against the whole document would be a test that always
   * passes: every form carries a hidden `<input name="lang" value="…">`, so the
   * substring is present whatever the field does.
   */
  const codeInput = (body: string) => /<input[^>]*class="code-input"[^>]*>/.exec(body)![0];

  it("re-renders the submitted code, grouped, after a rejection", () => {
    /*
     * The design's rule: "Server-side validation errors re-render the same document
     * with the value preserved and the message adjacent to the field — because there
     * is no client state to preserve it in."
     *
     * Without this, one wrong character out of twelve, typed one-handed in daylight
     * off a thermal receipt, costs the guest all twelve. It is the single most-used
     * control in the product.
     */
    const body = voucherPage("not-found", "ABCDEFGHJKMN");
    expect(body).toContain('value="ABCD-EFGH-JKMN"');
    expect(body).toContain('aria-invalid="true"');
    // The message is adjacent to the field for a screen reader too, not only visually.
    expect(body).toContain('id="code-error"');
    expect(body).toContain('aria-describedby="code-help code-error"');
  });

  it("renders a clean field when there is nothing to restore", () => {
    const body = voucherPage(null, null);
    expect(codeInput(body)).not.toContain("value=");
    expect(body).not.toContain("aria-invalid");
    expect(body).toContain('aria-describedby="code-help"');
  });

  it("reduces anything the query string carries to the voucher wire form", () => {
    /*
     * `?code=` is attacker-controlled like every other NAS-adjacent parameter. Escaping
     * alone would stop the injection; normalising also stops a crafted link
     * pre-filling the field with text that looks like it came from the venue.
     */
    const injected = voucherPage("shape", '"><script>alert(1)</script>');
    expect(injected).not.toContain("<script>");
    expect(injected).not.toContain("alert(1)");
    // Only what survives `[^A-Z0-9]` removal, and only twelve of it.
    expect(injected).toContain('value="SCRI-PTAL-ERT1"');

    expect(voucherPage("shape", "abcd efgh jkmn")).toContain('value="ABCD-EFGH-JKMN"');

    // Length-capped at the contract's twelve, however long the parameter is.
    const long = voucherPage("shape", "ABCDEFGHJKMNPQRSTUVW");
    expect(long).toContain('value="ABCD-EFGH-JKMN"');
    expect(long).not.toContain("PQRS");

    // An empty or separator-only parameter is not a value.
    expect(codeInput(voucherPage("shape", "---"))).not.toContain("value=");
  });

  it("never echoes a username or a password back through the URL", () => {
    // A username is half of a credential pair, and the password has to be retyped
    // regardless — so carrying it would buy half a form for a permanent liability in
    // every access log between the portal and the browser.
    const body = renderPage({
      lang: DEFAULT_LANGUAGE,
      path: "account/",
      context: null,
      status: null,
      error: "bad-credentials",
      code: "SOMEUSERNAME",
    })!.body;
    expect(body).not.toContain("SOMEUSERNAME");
    for (const tag of body.match(/<input[^>]*>/g) ?? []) {
      // The hidden language field is the only `value` any form page may carry.
      if (tag.includes('name="lang"')) continue;
      expect(tag, "an account field carries a value it should not").not.toContain("value=");
    }
    // The failure belongs to the pair, so both fields point at the one message.
    expect(body).toContain('id="account-error"');
    expect(body.match(/aria-describedby="account-error"/g)?.length).toBe(2);
  });
});

describe("the success page reads the state, not just the plan", () => {
  /*
   * The named test for the success page's state.
   *
   * `/connected/` is a document. It can be reloaded, bookmarked, or reopened by the
   * CNA after the grant ended — and `PortalStatus.state` exists because the server
   * knows which of `connected | expired | revoked` is true. The page read
   * `ctx.status?.plan` and never `ctx.status.state`, so all four outcomes rendered
   * the green tick and "You're online.": measured by redeeming a voucher, revoking it
   * through the admin API, reloading, and getting the same page — and by `curl`ing
   * the route with no cookie at all.
   *
   * Four cases because there are four outcomes, and the fourth is not in the enum:
   * the absence of a status is the common one, since the CNA destroys its cookie jar
   * when the window closes.
   */
  const connectedPage = (status: PortalStatus | null, lang: Language = DEFAULT_LANGUAGE) =>
    renderPage({ lang, path: "connected/", context: null, status, error: null })!;

  const statusOf = (state: PortalStatus["state"]): PortalStatus => ({
    state,
    plan: { name: "Test", durationMinutes: 60, dataAllowanceMb: 500, enforcedBy: "nas" },
    venueName: "V",
    grantedAt: new Date().toISOString(),
    expiresAt: null,
    originalUrl: "http://vnexpress.net/",
  });

  it("says the guest is online only when the server says they are connected", () => {
    const page = connectedPage(statusOf("connected"));
    expect(page.body).toContain(en["connected.lede"]);
    expect(page.body).toContain("badge-ok");
    expect(page.body).toContain(en["connected.closeHint"]);
  });

  for (const [state, key] of [
    ["expired", "connected.expired"],
    ["revoked", "connected.revoked"],
  ] as const) {
    it(`reports a ${state} grant as ${state}, with the way back`, () => {
      const page = connectedPage(statusOf(state));
      expect(page.body).toContain(en[key]);
      // None of the success page's furniture may survive: a green tick over a
      // cancelled grant is the exact failure this branch exists to remove, and
      // "you can close this window" would send them away from the one screen that
      // can still help.
      expect(page.body).not.toContain(en["connected.lede"]);
      expect(page.body).not.toContain("badge-ok");
      expect(page.body).not.toContain(en["connected.closeHint"]);
      // No plan, no allowance, no "continue to" — none of it is true any more.
      expect(page.body).not.toContain("Test");
      expect(page.body).not.toContain("vnexpress.net");
      expect(page.body).toContain(en["connected.signIn"]);
      expect(page.body).toContain(`href="/${DEFAULT_LANGUAGE}/"`);
      // The tab title has to agree with the page. It said "Connected" either way.
      expect(page.title).toContain(en["problem.title"]);
    });
  }

  it("treats an absent status as not signed in, never as connected", () => {
    // `curl http://localhost:5171/en/connected/` with no cookie rendered "You're
    // online. You can close this window." This is that case.
    const page = connectedPage(null);
    expect(page.body).toContain(en["connected.unknown"]);
    expect(page.body).not.toContain(en["connected.lede"]);
    expect(page.body).not.toContain("badge-ok");
    expect(page.title).toContain(en["problem.title"]);
  });

  it("has the copy in both languages and states a mechanism before a consequence", () => {
    for (const key of [
      "connected.expired",
      "connected.revoked",
      "connected.unknown",
      "connected.notOnNetwork",
    ] as const) {
      expect(vi[key].length).toBeGreaterThan(10);
      // Each names a next action. An error that only reports a state leaves the
      // guest holding a receipt in front of a staff member who cannot help.
      expect(en[key]).toMatch(/Ask staff|Sign in|Show this screen to staff/);
    }
    const body = connectedPage(statusOf("revoked"), "vi").body;
    expect(body).toContain(vi["connected.revoked"]);
  });

  /*
   * `not-on-network`: the venue granted, and the access point never let the device
   * on. Without this state the portal would answer `connected` — a guest reading
   * "You're online. You can close this window." in a café with no internet, holding
   * a voucher that is now spent.
   *
   * The two assertions that matter are both negative, and neither is obvious:
   *
   *  - none of the success page's furniture survives, the same rule `expired` and
   *    `revoked` carry — "you can close this window" would send them away from the
   *    one screen that can still help;
   *  - the copy must **not** tell them to ask for a new code. It is the only one of
   *    the four where the guest did nothing wrong and their code is fine: a new code
   *    goes through the controller that just refused, so that instruction charges a
   *    guest twice for one outage.
   */
  it("tells a guest the network refused them without blaming their code", () => {
    const page = connectedPage(statusOf("not-on-network"));
    expect(page.body).toContain(en["connected.notOnNetwork"]);
    expect(page.body).not.toContain(en["connected.lede"]);
    expect(page.body).not.toContain("badge-ok");
    expect(page.body).not.toContain(en["connected.closeHint"]);
    expect(page.title).toContain(en["problem.title"]);
    // The one that separates this state from the other three.
    expect(en["connected.notOnNetwork"]).not.toMatch(/new code/i);
    expect(vi["connected.notOnNetwork"]).not.toMatch(/mã mới/i);

    const viBody = connectedPage(statusOf("not-on-network"), "vi").body;
    expect(viBody).toContain(vi["connected.notOnNetwork"]);
  });

  /*
   * **Every state the contract declares has a sentence, checked by the compiler.**
   *
   * `notConnectedMessage` falls back to `connected.unknown` for a state it does not
   * recognise, which is correct at runtime — a state a newer backend sends must not
   * blank the page — and is exactly what makes drift silent: a guest gets *this
   * device is not signed in* about a situation that is nothing of the kind, and
   * nothing is red.
   *
   * So the map below is typed `Record<PortalStatus["state"], …>`. Adding a member to
   * the contract's enum and regenerating turns `pnpm -F @foyer/portal typecheck` red
   * on this line, before anybody reaches a browser. It is the same mechanism `vi.ts`
   * uses against a forgotten translation.
   */
  it("has a distinct sentence for every state the contract can send", () => {
    const sentences: Record<NonNullable<PortalStatus["state"]>, string | null> = {
      connected: null, // the success page, not a NotConnected sentence
      expired: en["connected.expired"],
      revoked: en["connected.revoked"],
      "not-on-network": en["connected.notOnNetwork"],
    };

    const rendered = new Set<string>();
    for (const [state, expected] of Object.entries(sentences)) {
      const body = connectedPage(statusOf(state as NonNullable<PortalStatus["state"]>)).body;
      if (expected === null) {
        expect(body).toContain(en["connected.lede"]);
        continue;
      }
      expect(body, `state ${state} did not render its own copy`).toContain(expected);
      // Distinct, not merely present. Two states sharing a sentence is the drift
      // the fallback hides, and it renders identically to a correct page.
      expect(rendered.has(expected), `two states share one sentence: ${state}`).toBe(false);
      rendered.add(expected);
    }
    expect(rendered.size).toBe(Object.values(sentences).filter(Boolean).length);
  });
});

describe("switching language keeps what the guest already had", () => {
  const withQuery = (query: string, path = "voucher/") =>
    renderPage({
      lang: "en",
      path,
      context: null,
      status: null,
      error: "not-found",
      code: "ABCDEFGHJKMN",
      query,
    })!.body;

  it("carries the query string onto the language links", () => {
    /*
     * `/en/voucher/?e=not-found&code=ABCD…` → `/vi/voucher/` would lose both the error
     * and the twelve characters the guest had just typed. A guest who cannot read the
     * error loses their work by trying to read it in their own language, in the one
     * control they reach for *because* they are struggling.
     *
     * The root redirect keeps the query for exactly this reason, and so does the
     * switcher.
     */
    const body = withQuery("?e=not-found&code=ABCDEFGHJKMN");
    expect(body).toContain('href="/vi/voucher/?e=not-found&amp;code=ABCDEFGHJKMN"');
  });

  it("carries the NAS parameters, which are the only evidence VerifyPresence has", () => {
    // `ap`, `id`, `t`, `ssid`, `url` — `docs/protocol-reality.md`. Dropping them on a
    // language switch drops the MAC and AP a grant cannot be created without.
    const body = withQuery("?ap=f0:9f:c2:11:22:33&id=8a:de:2b:91:c4:07&ssid=Foyer", "");
    expect(body).toContain("ap=f0:9f:c2:11:22:33");
    expect(body).toContain("id=8a:de:2b:91:c4:07");
  });

  it("adds no question mark when there is nothing to carry", () => {
    expect(withQuery("")).toContain('href="/vi/voucher/"');
    expect(withQuery("?")).toContain('href="/vi/voucher/"');
  });

  it("cannot be used to leave the origin or break out of the attribute", () => {
    // Relayed verbatim — the parameters are transport and re-encoding them would
    // corrupt a `url` from a NAS that did not percent-encode it — so the escaping at
    // the render boundary is what has to hold.
    const body = withQuery('?x="><script>alert(1)</script>');
    expect(body).not.toContain("<script>");
    expect(body).not.toContain('"><script');
    // Everything after the path is a query; it cannot become another origin.
    for (const href of body.match(/class="lang-switch" href="([^"]*)"/g) ?? []) {
      expect(href).toContain('href="/vi/');
    }
  });
});

describe("a NAS-supplied link target is parsed before it becomes an href", () => {
  /*
   * `docs/rules/frontend.md`: never render a NAS-supplied value as a link target
   * without validating the scheme — the portal is a prime open-redirect target
   * precisely because the guest expects to be redirected, and here they have just
   * typed credentials on an untrusted network.
   *
   * Three gates gate this value: the server allowlists it, the contract's `pattern`
   * makes a violation fail validation instead of reaching a browser, and this one.
   * Only this one is adjacent to the DOM.
   */
  const connectedWith = (originalUrl: string | null) =>
    renderPage({
      lang: DEFAULT_LANGUAGE,
      path: "connected/",
      context: null,
      error: null,
      status: {
        state: "connected",
        plan: { name: "T", durationMinutes: 60, dataAllowanceMb: null, enforcedBy: "nas" },
        venueName: "V",
        grantedAt: new Date().toISOString(),
        expiresAt: null,
        originalUrl,
      },
    })!.body;

  it("renders an http or https destination", () => {
    expect(connectedWith("https://vnexpress.net/tin-tuc")).toContain("vnexpress.net");
    expect(connectedWith("http://example.test/")).toContain("example.test");
  });

  it("renders no link at all for anything else", () => {
    for (const hostile of [
      "javascript:alert(1)",
      "data:text/html,<script>alert(1)</script>",
      "//evil.example/",
      "http://portal.example@evil.example/",
      "https://user:pw@evil.example/",
      "not a url",
    ]) {
      const body = connectedWith(hostile);
      expect(body, hostile).not.toContain("class=\"continue\"");
      expect(body, hostile).not.toContain("evil.example");
      expect(body, hostile).not.toContain("javascript:");
      expect(body, hostile).not.toContain("data:text/html");
    }
  });
});

describe("venue-authored text says what language it is in", () => {
  it("marks a plan name written in a language the document is not", () => {
    /*
     * All five fixture plan names are Vietnamese, because a café in District 1
     * writes its price list in Vietnamese. They render verbatim on the English
     * success page inside `<html lang="en">` — correct data, and without a marker a
     * screen reader pronounces `Nhân viên` in English and a browser's translation
     * offer never fires. The terms block was given this treatment deliberately; the
     * plan name is the same situation and did not have it.
     */
    const page = (lang: Language, nameLang: Language | undefined) =>
      renderPage({
        lang,
        path: "connected/",
        context: null,
        error: null,
        status: {
          state: "connected",
          plan: { name: "Nhân viên", nameLang, durationMinutes: 60, enforcedBy: "nas" },
          venueName: "V",
          grantedAt: new Date().toISOString(),
          expiresAt: null,
          originalUrl: null,
        },
      })!.body;

    expect(page("en", "vi")).toContain('<span lang="vi">Nhân viên</span>');
    // Not marked when it agrees with the document: a redundant `lang` is noise, and
    // the attribute has to mean something for a screen reader to act on it.
    expect(page("vi", "vi")).not.toContain('lang="vi">Nhân viên');
    expect(page("vi", "vi")).toContain("Nhân viên");
    // A venue that never said is not guessed at. (Asserted against the fact value
    // rather than the document: the language switcher legitimately carries
    // `lang="vi"` on its own link, and asserting on the whole body would pass or
    // fail for the wrong reason.)
    expect(page("en", undefined)).toContain('<span class="fact-value">Nhân viên</span>');
  });
});

/**
 * Venue branding: reachable before the guest has internet, or not rendered at all.
 *
 * Both of these values are venue-authored and arrive through the API, so they are
 * attacker-influenced the moment an admin account is — and they land in `src`, on a
 * page served before authentication, in a browser with no address bar. The portal
 * validates them at the render boundary rather than trusting the API, for the same
 * reason `originalUrl` is parsed three times: a regex in a schema is a shape test,
 * and the browser uses a parser.
 */
describe("venue branding cannot reach off-origin", () => {
  const welcome = (context: Record<string, unknown>) =>
    renderPage({
      path: "",
      lang: "en",
      context: context as never,
      status: null,
      error: null,
    })!.body;

  it("renders a logo that is an inline raster image", () => {
    const uri = "data:image/png;base64,iVBORw0KGgo=";
    expect(welcome({ venueName: "V", logoDataUri: uri })).toContain(uri);
  });

  it("renders a logo that is a same-origin path, and refuses an external one", () => {
    expect(welcome({ venueName: "V", logoDataUri: "/venue/site-hcm/logo.png" })).toContain(
      'src="/venue/site-hcm/logo.png"',
    );
    for (const bad of [
      "//cdn.example.com/logo.png",
      "https://cdn.example.com/logo.png",
      "/ven ue/logo.png",
    ]) {
      const body = welcome({ venueName: "V", logoDataUri: bad });
      expect(body, bad).not.toContain(`src="${bad}"`);
      expect(body).toContain("venue-mark");
    }
  });
  it.each([
    ["javascript:alert(1)", "a scheme that executes"],
    ["http://cdn.example.com/logo.png", "an external origin"],
    ["//cdn.example.com/logo.png", "a protocol-relative origin"],
    ["data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=", "an SVG, which is a document that can carry script"],
    ["data:text/html;base64,PGgxPmhpPC9oMT4=", "a document wearing an image field"],
  ])("refuses %s — %s", (uri) => {
    const body = welcome({ venueName: "V", logoDataUri: uri });
    expect(body).not.toContain(uri);
    // And it falls back rather than rendering a broken element: the masthead still
    // has a mark, so the page does not look half-loaded.
    expect(body).toContain("venue-mark");
  });

  it("renders a same-origin photograph and refuses anything else", () => {
    expect(welcome({ venueName: "V", heroImageUrl: "/venue/site-hcm/hero-abc" })).toContain(
      'src="/venue/site-hcm/hero-abc"',
    );
    for (const bad of [
      "//evil.example/x.jpg",
      "https://evil.example/x.jpg",
      "javascript:alert(1)",
      "data:image/png;base64,iVBORw0KGgo=",
    ]) {
      expect(welcome({ venueName: "V", heroImageUrl: bad }), bad).not.toContain("venue-hero");
    }
  });

  it("reserves the photograph's box before its bytes arrive", () => {
    // Without intrinsic dimensions the image reflows the page as it loads, and what
    // moves is the terms and the consent sentence underneath it.
    const body = welcome({ venueName: "V", heroImageUrl: "/venue/s/h" });
    expect(body).toMatch(/width="\d+"/);
    expect(body).toMatch(/height="\d+"/);
  });

  it("keeps the photograph out of the accessibility tree", () => {
    // The venue is named in the masthead directly above. A logo or photograph that
    // announces itself makes a screen reader say the venue twice.
    const body = welcome({ venueName: "V", heroImageUrl: "/venue/s/h", logoDataUri: "data:image/png;base64,iVBORw0KGgo=" });
    expect(body).not.toMatch(/<img[^>]*class="venue-hero"[^>]*alt="[^"]+"/);
    expect(body).not.toMatch(/<img[^>]*class="venue-mark"[^>]*alt="[^"]+"/);
  });
});

/**
 * Language negotiation — against the venue's stock, not the guest's wish alone.
 *
 * The rule a dev middleware can easily miss, and that a review against it would
 * approve without: **a venue advertising one language has one language to give**, and honouring an English `Accept-Language` there lands the
 * guest on Vietnamese terms with a switcher that should not exist.
 */
describe("the portal greets a guest in a language the venue actually has", () => {
  it("prefers by quality value, not by the order tags arrive in", () => {
    // `startsWith("en")` gets this wrong, and the header is quality-ordered by spec.
    expect(negotiateLanguage("en;q=0.4, vi;q=0.9", ["en", "vi"])).toBe("vi");
    expect(negotiateLanguage("vi;q=0.2, en;q=0.8", ["en", "vi"])).toBe("en");
  });

  it("treats a tag with no q as the strongest preference", () => {
    expect(negotiateLanguage("vi, en;q=0.9", ["en", "vi"])).toBe("vi");
  });

  it("matches on the primary subtag, so a regional tag is not an unknown language", () => {
    expect(negotiateLanguage("en-GB,en;q=0.9", ["en", "vi"])).toBe("en");
    expect(negotiateLanguage("vi-VN", ["en", "vi"])).toBe("vi");
  });

  it("gives a single-language venue its own language, whatever the guest asked for", () => {
    // The defect this function exists for. Honouring `en` here would land an English
    // reader on Vietnamese terms, next to a switcher with nowhere to switch to.
    expect(negotiateLanguage("en-US,en;q=0.9", ["vi"])).toBe("vi");
    expect(negotiateLanguage("vi", ["en"])).toBe("en");
  });

  it("falls back to the venue's first language, not the portal's default", () => {
    // A guest who reads neither is better served by what the venue can support.
    expect(negotiateLanguage("fr,de;q=0.8", ["vi"])).toBe("vi");
    expect(negotiateLanguage(null, ["vi"])).toBe("vi");
    expect(negotiateLanguage("", ["vi"])).toBe("vi");
  });

  it("reads `*` as the venue's choice rather than the portal's", () => {
    expect(negotiateLanguage("*", ["vi"])).toBe("vi");
  });

  it("ignores a zero quality value, which means 'not this one'", () => {
    expect(negotiateLanguage("en;q=0, vi;q=0.5", ["en", "vi"])).toBe("vi");
  });

  it("treats a venue that advertised nothing as offering everything", () => {
    expect(negotiateLanguage("vi", undefined)).toBe("vi");
    expect(negotiateLanguage("vi", [])).toBe("vi");
    expect(negotiateLanguage("en", [])).toBe("en");
  });

  it("never returns a language the venue did not advertise", () => {
    // The invariant, stated separately from the cases: whatever the header says, the
    // answer is something the venue can render.
    for (const header of ["en", "vi", "fr", "*", "", "en;q=0.1,vi;q=0.2", "xx-YY"]) {
      expect(negotiateLanguage(header, ["vi"]), header).toBe("vi");
      expect(negotiateLanguage(header, ["en"]), header).toBe("en");
    }
  });
});

/**
 * Click-through — terms only, and the two properties that make it safe.
 *
 * It is the only mode with no credential, so the guarantees the other modes get from
 * having one have to come from somewhere else: the consent has to be *adjacent* to
 * what is being consented to, and the submit has to be a real navigation.
 */
describe("click-through puts the consent next to the terms", () => {
  const welcome = (methods: string[]) =>
    renderPage({
      path: "",
      lang: "en",
      context: { venueName: "V", methods, terms: { summary: {} } } as never,
      status: null,
      error: null,
    })!.body;

  it("does not appear where the venue has not offered it", () => {
    expect(welcome(["voucher", "account"])).not.toContain('class="agree"');
  });

  it("submits as a document navigation, not a fetch", () => {
    // The single most expensive thing to get wrong here: a fetch-submitted consent
    // authenticates the guest and then leaves the CNA's button reading "Cancel",
    // which disassociates the device. Playwright reports a pass either way.
    const body = welcome(["click-through"]);
    expect(body).toContain('method="post"');
    expect(body).toContain('action="/api/v0/portal/agree"');
    expect(body).toContain('type="submit"');
  });

  it("renders inside the terms block, beneath the consent sentence", () => {
    /*
     * Structural, and the reason is the whole design. The guest must not be able to
     * press this without the terms in the same glance — a button above the terms, or
     * in the method list, is consent to something that is not on screen yet.
     */
    const body = welcome(["click-through"]);
    const boundary = body.indexOf('class="boundary"');
    const agree = body.indexOf('class="agree"');
    const termsEnd = body.indexOf("</section>", boundary);
    expect(boundary).toBeGreaterThan(-1);
    expect(agree).toBeGreaterThan(boundary);
    expect(agree).toBeLessThan(termsEnd);
  });

  it("carries the language, so the redirect comes back where the guest was", () => {
    expect(welcome(["click-through"])).toContain('name="lang"');
  });
});
