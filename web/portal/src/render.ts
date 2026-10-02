import { ACCENT_PATTERN, localDate, narrowDesign, normaliseVoucherCode, VOUCHER_LENGTH } from "@foyer/contract";
import type { Language, PortalContext, PortalDesign, PortalStatus } from "@foyer/contract";
import render from "preact-render-to-string";
import { LANGUAGES, resolveLanguages, translator, type Translate } from "./i18n/index.js";
import { findPage } from "./pages/index.js";
import type { PageContext, ResolvedDataCollection, ResolvedTerms } from "./pages/types.js";

export interface RenderInput {
  lang: Language;
  /** Path under the language prefix. `""` is the welcome page. */
  path: string;
  context: PortalContext | null;
  status: PortalStatus | null;
  error: string | null;
  /**
   * The raw `?code=` from the redirect: the voucher code that was just rejected.
   * Attacker-controlled like every other query parameter, and sanitised here rather
   * than at the point of use.
   */
  code?: string | null;
  /**
   * The whole query string, `?`-prefixed, as it arrived. Relayed onto the language
   * links so switching language does not throw the guest's context away — see
   * `PageContext.query` and `Shell`.
   */
  query?: string | null;
  /** `?sent=1` on the phone page — see `PageContext.phoneSent`. */
  phoneSent?: boolean;
  /**
   * The calendar day blocks are dated against, `YYYY-MM-DD`. Defaults to the local
   * date; a test or a preview pins it.
   */
  today?: string;
  /** The raw `?n=` from the redirect: a success to read. Allowlisted here. */
  notice?: string | null;
}

export interface RenderedPage {
  title: string;
  body: string;
  /** False when this document does not need the enhancement script at all. */
  enhance: boolean;
}

/**
 * Renders one page to a string.
 *
 * Shared by the dev middleware and the static build, so what a developer clicks
 * through in development is produced by the same code that emits the artefact.
 */
export function renderPage(input: RenderInput): RenderedPage | null {
  const page = findPage(input.path);
  if (!page) return null;

  const t = translator(input.lang);

  const ctx: PageContext = {
    lang: input.lang,
    t,
    venueName: input.context?.venueName ?? "Foyer",
    networkName: input.context?.networkName ?? null,
    logoDataUri: safeLogo(input.context?.logoDataUri),
    heroImageUrl: safeHeroUrl(input.context?.heroImageUrl),
    methods: input.context?.methods ?? ["voucher", "account"],
    languages: resolveLanguages(input.lang, input.context?.languages),
    terms: resolveTerms(input.lang, t, input.context?.terms),
    query: normaliseQuery(input.query),
    error: input.error,
    submittedCode: resolveSubmittedCode(input.code),
    phoneSent: input.phoneSent === true,
    phoneCodeMinutes: resolveCodeMinutes(input.context?.phoneCodeMinutes),
    status: input.status,
    design: safeDesign(input.context?.design, input.today ?? localDate()),
    dataCollection: resolveDataCollection(input.lang, input.context?.dataCollection),
    notice: input.notice === "consent-withdrawn" ? "consent-withdrawn" : null,
  };

  return {
    title: page.title(ctx),
    body: render(page.render(ctx)),
    enhance: page.enhance === true,
  };
}


/**
 * The code lifetime a page states when the venue's is unknown: `otp.DefaultLimits`,
 * and `internal/portal.DefaultPhoneCodeMinutes` on the Go twin.
 */
export const DEFAULT_PHONE_CODE_MINUTES = 5;

/** The venue's code lifetime, inside the contract's 1–10 or the default. */
function resolveCodeMinutes(m: number | null | undefined): number {
  return typeof m === "number" && Number.isInteger(m) && m >= 1 && m <= 10 ? m : DEFAULT_PHONE_CODE_MINUTES;
}

/**
 * A logo, or nothing — and the `data:` check is a security gate, not a formality.
 *
 * `logoDataUri` is venue-authored and reaches the portal through the API, so it is
 * attacker-influenced the moment an admin account is. Rendered into `src` unchecked
 * it accepts `javascript:` on engines that still honour it in that position, and an
 * external `http://` would be a request on a page that must render before the
 * client has internet — the exact thing the byte gate forbids in the built output
 * but cannot see in a runtime value.
 *
 * A same-origin path is the second accepted shape: the portal serves it itself,
 * and its own address is already required to be in the walled garden. The shape is
 * `safeHeroUrl`'s, not a second rule.
 *
 * SVG is excluded even as a data URI: an SVG is a document that can carry script,
 * and this one would render pre-authentication.
 */
function safeLogo(uri: string | null | undefined): string | null {
  if (!uri) return null;
  if (/^data:image\/(png|jpeg|webp|gif);base64,[A-Za-z0-9+/=]+$/.test(uri)) return uri;
  if (/^\/(?!\/)[^\s"'<>]*$/.test(uri)) return uri;
  return null;
}

/**
 * The venue's design, narrowed again here rather than trusted.
 *
 * The server already drops hidden and out-of-date blocks; doing it a second time is
 * what lets the console preview a draft through this same function and see what a
 * guest would see today. The accent is re-checked because it lands inside a
 * `<style>` element, where escaping does not apply.
 */
function safeDesign(design: PortalDesign | null | undefined, today: string): PortalDesign | null {
  if (!design) return null;
  const narrowed = narrowDesign(design, today);
  return {
    ...narrowed,
    accent: narrowed.accent && ACCENT_PATTERN.test(narrowed.accent) ? narrowed.accent : null,
  };
}

/**
 * The venue image path, or nothing.
 *
 * Root-relative and same-origin only. A protocol-relative `//evil.example/x.jpg` is
 * an external origin wearing a leading slash, which is why the second character is
 * checked as well as the first.
 */
function safeHeroUrl(url: string | null | undefined): string | null {
  if (!url) return null;
  return /^\/(?!\/)[^\s"'<>]*$/.test(url) ? url : null;
}


/**
 * The query string, normalised to `""` or `?…` and nothing in between.
 *
 * It is **not** re-serialised. The root redirect relays these parameters verbatim on
 * the stated grounds that they are transport — re-encoding through `URLSearchParams`
 * would decode `+` as a space and corrupt a `url` parameter from a NAS that did not
 * percent-encode it, which is the class of bug the "never parsed" rule exists to
 * avoid. It reaches the DOM only as the tail of a same-origin `href` whose path is
 * built here, so it cannot change the origin, and the renderer escapes the attribute.
 */
function normaliseQuery(raw: string | null | undefined): string {
  if (!raw) return "";
  const trimmed = raw.startsWith("?") ? raw.slice(1) : raw;
  return trimmed ? `?${trimmed}` : "";
}

/**
 * The rejected voucher code, made safe to put back in the field.
 *
 * It arrives as a query parameter, which means anyone can put anything in it. It is
 * reduced to the voucher wire form — `[A-Z0-9]`, at most twelve characters — before
 * it is allowed anywhere near the DOM, so the worst a crafted link can do is
 * pre-fill the field with twelve characters of the voucher alphabet. Escaping alone
 * would stop the injection; normalising also stops the field being used to render
 * arbitrary text that looks like it came from the venue.
 *
 * The parameter is `?code=` — the same name as the form field it came from, so the
 * redirect is a literal echo of the submitted field rather than a mapping somebody
 * has to keep in their head. It rides alongside `?e=<reason>`.
 *
 * **Only the voucher code travels this way.** A username is half of a credential
 * pair, so echoing it would put it in the query string and therefore in every access
 * log between here and the browser — and the guest has to retype the password
 * regardless, so it buys half a form for a permanent logging liability. A rejected
 * voucher code is different in kind: by the time it is echoed it has already failed,
 * it is single-use, and it cannot be redeemed by whoever reads it.
 */
function resolveSubmittedCode(raw: string | null | undefined): string | null {
  if (!raw) return null;
  const clean = normaliseVoucherCode(raw).slice(0, VOUCHER_LENGTH);
  return clean || null;
}

/**
 * Picks the terms the guest sees, and says which language they are in.
 *
 * The contract's resolution order, which the Go backend implements identically:
 *
 * 1. The venue's text for the language being read.
 * 2. Otherwise the venue's text in any other language it supplied, carrying that
 *    language with it. Vietnamese terms in front of an English reader are bad;
 *    an empty terms section is worse, because then the guest consents to nothing
 *    legible at all. The `lang` the caller renders is what lets a screen reader
 *    and a browser translation offer handle text the portal must not touch.
 * 3. Otherwise the portal's own catalogue default, in the language being read —
 *    which is what keeps the billing-and-consent-boundary sentence on screen in a
 *    deployment that never configured terms.
 *
 * Never a translation, never a merge of two languages, never an empty section.
 *
 * **There is no `terms.url` to resolve.** The contract deliberately carries no link to
 * the venue's full terms. The reasoning, and the specific evidence that would reopen
 * it, live in the `terms` description in the contract — one copy, next to the thing
 * it governs. Do not restate it here.
 */
function resolveTerms(
  lang: Language,
  t: Translate,
  terms: PortalContext["terms"] | undefined,
): ResolvedTerms {
  const summaries = terms?.summary ?? {};
  // The active language first, then the rest in catalogue order, so the fallback is
  // deterministic rather than dependent on JSON key order. Iterating the languages
  // the portal knows rather than the keys the venue sent also means a key outside
  // the `Language` enum is ignored instead of reaching a rendered `lang` attribute.
  const order: Language[] = [lang, ...LANGUAGES.filter((l) => l !== lang)];

  for (const candidate of order) {
    const summary = summaries[candidate]?.trim();
    if (!summary) continue;
    return { summary, lang: candidate };
  }

  return { summary: t("welcome.termsSummary"), lang };
}

/**
 * The venue's data collection, resolved for one page. The consent sentence follows
 * the terms' order — the language being read, else another the venue wrote, marked
 * with its own language — and has **no catalogue fallback**: there is no default
 * promise the portal could make on a venue's behalf, so no sentence means no box.
 */
function resolveDataCollection(
  lang: Language,
  dc: PortalContext["dataCollection"] | undefined,
): ResolvedDataCollection | null {
  if (!dc) return null;
  const fields = dc.fields.filter((f) => f.field === "name" || f.field === "phone" || f.field === "email");
  let consent: ResolvedDataCollection["consent"] = null;
  const mc = dc.marketingConsent;
  if (mc) {
    for (const candidate of [lang, ...LANGUAGES.filter((l) => l !== lang)]) {
      const text = mc.text[candidate]?.trim();
      const version = mc.versions[candidate];
      if (text && version) {
        consent = { text, lang: candidate, version };
        break;
      }
    }
  }
  return { fields, consent, consentAsked: consent !== null };
}

export { PAGES } from "./pages/index.js";
