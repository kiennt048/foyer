/**
 * Which language the console speaks, decided once, before anything renders.
 *
 * The people this console is for run cafés and hotels in Vietnam. The portal speaks
 * Vietnamese, and so does the console.
 *
 * ── how the field decides it, and what Foyer takes ──────────────────────────
 *
 *   - **Keycloak** resolves the account console's locale as: the user's explicit
 *     choice (a cookie, then the `locale` user attribute), then the browser's
 *     `Accept-Language`, then the realm's default. **Grafana** is the same shape:
 *     a user preference, then team and org defaults, then the browser.
 *   - **UniFi** and **Omada** keep a per-administrator language in account
 *     settings and start a new administrator on the browser's language.
 *   - **Grafana and Keycloak reload the page on a change** rather than re-render in
 *     place. Every one of them has module-level copy that a live switch would miss.
 *
 * So: **an explicit choice, then the browser, then English**, and a change reloads.
 *
 * *Per browser, not per operator.* A per-operator preference needs a column and a
 * migration, and none is reserved for it; the choice is stored in this
 * browser's `localStorage`, which is where Keycloak keeps its first tier too (a
 * cookie). A venue's portal default language is **not** consulted: it is chosen for
 * the guests, the sign-in screen renders before any venue is known, and Keycloak's
 * realm default sits *below* the browser for the same reason — the person reading is
 * better evidence than the place.
 *
 * ── why this file is in the entry chunk and the copy is not ─────────────────
 *
 * This is the decision and nothing else: a few hundred bytes. Each Vietnamese
 * catalogue is a module of its own under `vi/`, fetched by `loadLocale` in
 * `catalogue.ts` only when this says `vi`, and `tools/check-admin-budget.mjs`
 * refuses a build that puts any of them in the entry. An English reader downloads
 * no Vietnamese; a Vietnamese reader downloads the core catalogue once and each
 * screen's with that screen.
 */

export type Lang = "en" | "vi";

/** Every language the console speaks, in the order the switcher lists them. */
export const LANGS: readonly Lang[] = ["en", "vi"];

/** Where this browser keeps the operator's choice. */
export const LANG_STORAGE_KEY = "foyer.admin.lang";

const isLang = (v: unknown): v is Lang => v === "en" || v === "vi";

/**
 * The language to speak: the stored choice if there is one, else the first of the
 * browser's preferred languages the console speaks, else English. Pure, so the
 * precedence is testable without a browser.
 */
export function resolveLang(stored: string | null | undefined, preferred: readonly string[]): Lang {
  if (isLang(stored)) return stored;
  for (const tag of preferred) {
    const primary = tag.toLowerCase().split("-")[0];
    if (isLang(primary)) return primary;
  }
  return "en";
}

/** Storage can throw — a private window, blocked site data — and must not stop the console. */
function readStored(): string | null {
  try {
    return typeof localStorage === "undefined" ? null : localStorage.getItem(LANG_STORAGE_KEY);
  } catch {
    return null;
  }
}

function browserLanguages(): readonly string[] {
  if (typeof navigator === "undefined") return [];
  return navigator.languages?.length ? navigator.languages : navigator.language ? [navigator.language] : [];
}

/** The language this page load speaks. Fixed for its lifetime: a change reloads. */
export const LANG: Lang = resolveLang(readStored(), browserLanguages());

if (typeof document !== "undefined") document.documentElement.lang = LANG;

/**
 * Keep `next` for this browser and reload in it. A reload rather than a re-render
 * for the reason Grafana and Keycloak reload: copy computed when a module loaded —
 * column headers, option labels — would stay in the old language otherwise.
 */
export function chooseLang(next: Lang): boolean {
  try {
    localStorage.setItem(LANG_STORAGE_KEY, next);
  } catch {
    /* Storage refused (a private window, blocked site data): a reload would come
       back in the browser's language, so it would change nothing. Say so. */
    return false;
  }
  if (next !== LANG) location.reload();
  return true;
}

/**
 * Each language named in itself — "English", "Tiếng Việt" — the way the portal's
 * switcher and every product surveyed name them: a reader who cannot read the
 * current language can still find their own.
 */
export function languageName(lang: Lang): string {
  return lang === "vi" ? "Tiếng Việt" : "English";
}

/** The `Intl` locale for dates and numbers. `en-GB` for the reason `index.ts` gives. */
export const INTL_LOCALE: string = LANG === "vi" ? "vi-VN" : "en-GB";
