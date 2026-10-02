import type { APIRequestContext, BrowserContext, Page } from "@playwright/test";
import { MOCK_URL, PORTAL_LANGUAGES, PORTAL_URL, languageOf, type PortalLanguage } from "./env.js";

/*
 * Portal selectors, in one place.
 *
 * Everything here selects by role, structure or `href` — never by copy. The portal
 * and admin ship in more than one language and their copy changes, and a suite that
 * asserts on strings would break on a translation rather than on a regression. Structure is the contract; wording is not.
 */
export const portal = {
  shell: ".shell",
  languageSwitch: "a.lang-switch",
  heading: "h1",
  methodLinks: ".methods a.method",
  form: "form[data-portal-form]",
  codeInput: "input.code-input",
  usernameInput: "input#username",
  passwordInput: "input#password",
  submit: "form[data-portal-form] button[type=submit]",
  error: "[role=alert]",
  connectedBadge: ".badge-ok",
  connectedFacts: ".facts",
  /** The venue's terms block on the welcome page. */
  terms: "section.terms",
  /** The venue's own terms text, inside that block. */
  termsSummary: "section.terms > p:not(.boundary)",
  /**
   * Foyer's own sentence — the billing-and-consent-boundary statement that
   * `docs/rules/security.md` §6 requires the product to make. It is a sibling of
   * the venue's terms, never part of them, which is the whole point: as a line of the
   * *default* terms, a venue that supplied its own would delete it by doing the
   * ordinary thing the field exists for.
   */
  consentBoundary: "section.terms p.boundary",
} as const;

/**
 * Which language links the switcher ought to offer, given what the venue advertises.
 *
 * Mirrors `resolveLanguages` in the portal: intersect the venue's list with the
 * languages the portal actually has strings for, always include the one being read
 * (the guest is looking at it), keep catalogue order, and drop the current one —
 * a link to the page you are on is not a switch.
 *
 * Computed from the venue's advertised set rather than typed in, for the same reason
 * `expectedGatedControls` is computed from the NAS's capabilities: an expectation
 * written as a constant would pass just as well against a switcher that ignores the
 * venue and renders a hardcoded pair, which is the regression this exists to catch.
 */
export function expectedSwitcherLanguages(
  advertised: readonly string[] | undefined,
  current: PortalLanguage,
): PortalLanguage[] {
  const wanted = new Set<string>(advertised?.length ? advertised : PORTAL_LANGUAGES);
  wanted.add(current);
  return PORTAL_LANGUAGES.filter((l) => wanted.has(l) && l !== current);
}

/**
 * Opens the portal at `/` and reports which language it landed on.
 *
 * Deliberately does not assume which language is the default — that can change.
 * A test that hardcoded
 * `/vi/` would fail on a copy change rather than on a behaviour change.
 */
export async function openPortal(page: Page): Promise<PortalLanguage> {
  await page.goto("/");
  const lang = languageOf(page.url());
  if (!lang) throw new Error(`Portal / did not land on a language prefix: ${page.url()}`);
  return lang;
}

/** The link to the voucher step, found by its href rather than its label. */
export function voucherLink(page: Page, lang: PortalLanguage) {
  return page.locator(`${portal.methodLinks}[href="/${lang}/voucher/"]`);
}

/** The link to the account step. */
export function accountLink(page: Page, lang: PortalLanguage) {
  return page.locator(`${portal.methodLinks}[href="/${lang}/account/"]`);
}

/** A username the mock will accept. Looked up, never hardcoded. */
export async function enabledAccountUsername(request: APIRequestContext): Promise<string> {
  const res = await request.get(`${MOCK_URL}/api/v0/accounts?siteId=site-hcm&limit=50`);
  const page = (await res.json()) as { items: { username: string; enabled: boolean }[] };
  const account = page.items.find((a) => a.enabled);
  if (!account) throw new Error("No enabled account in the fixtures.");
  return account.username;
}

/** `ABCD-EFGH-JKMN` → `ABCDEFGHJKMN`. What the wire is supposed to carry. */
export function ungroup(code: string): string {
  return code.replace(/[^A-Za-z0-9]/g, "").toUpperCase();
}

/** Reads the `code` field out of a form-urlencoded POST body. */
export function formField(postData: string | null, name: string): string | null {
  if (!postData) return null;
  return new URLSearchParams(postData).get(name);
}

/**
 * Selects a mock venue profile for the rest of the browser context.
 *
 * The cookie and not the query string, because the portal is **server-rendered**:
 * the dev middleware relays cookies to the mock on the guest's behalf and does not
 * relay a query parameter, so a profile chosen with `?venue=` reaches the API and
 * never the page. That asymmetry is the whole reason `web/mock` grew the cookie.
 *
 * Set on the mock's origin *and* the portal's, because the two are separate origins
 * in development and the middleware forwards whatever the browser sends it.
 */
export async function useVenueProfile(context: BrowserContext, id: string): Promise<void> {
  await context.addCookies(
    [MOCK_URL, PORTAL_URL].map((origin) => ({
      name: "foyer_mock_venue",
      value: id,
      url: origin,
    })),
  );
}
