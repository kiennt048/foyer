/**
 * Where the three dev servers live. Overridable so the suite can point at a lab.
 *
 * **`playwright.config.ts` imports these rather than declaring its own**, and that
 * import is the whole of what this comment is for. A second copy there can disagree
 * — starting servers on 5670/5671/5673 and checking 5170/5171/5173 — and the result
 * is a suite that cannot start at all from a clean checkout: three servers launched, three ports polled for sixty
 * seconds, no test executed.
 *
 * The port a server is STARTED on is derived from the URL it is CHECKED on, so an
 * override moves both. Overriding one and not the others is still possible and still
 * wrong; there is no defence against that here beyond saying so.
 */
export const MOCK_URL = process.env.FOYER_MOCK_URL ?? "http://localhost:5170";
export const PORTAL_URL = process.env.FOYER_PORTAL_URL ?? "http://localhost:5171";
export const ADMIN_URL = process.env.FOYER_ADMIN_URL ?? "http://localhost:5173";

/** The language prefixes the portal serves. Neither is assumed to be the default. */
export const PORTAL_LANGUAGES = ["en", "vi"] as const;
export type PortalLanguage = (typeof PORTAL_LANGUAGES)[number];

/** `/en/voucher/` → `en`. Returns null for a URL with no language prefix. */
export function languageOf(url: string): PortalLanguage | null {
  const path = new URL(url).pathname;
  const match = /^\/(en|vi)(\/|$)/.exec(path);
  return (match?.[1] as PortalLanguage | undefined) ?? null;
}

/** `/en/voucher/` → `voucher/`. The part the language switch has to preserve. */
export function pathWithinLanguage(url: string): string {
  return new URL(url).pathname.replace(/^\/(en|vi)\/?/, "");
}

/**
 * The password this suite signs in with, and the value it pins on the mock.
 *
 * **The mock verifies the password.** It generates one per
 * process the way first boot does, and prints it in its startup banner —
 * unreadable to a scripted driver, which is what `FOYER_MOCK_ADMIN_PASSWORD` is
 * for. `playwright.config.ts` passes this value to the mock's `webServer` entry
 * and `helpers/session.ts` submits it, so the two cannot drift.
 *
 * It is a harness knob rather than a credential: nothing in the repository is
 * configured with it, nothing deployed accepts it, and the Go binary refuses to
 * start on a known default. Overridable so that a suite pointed at an
 * already-running mock can be told what that mock was pinned with.
 *
 * **`reuseExistingServer` is on outside CI**, so a mock started by something else
 * — `pnpm dev` in another checkout — keeps its own password and this one will not
 * open it. That is a sign-in refusal with a clear cause, which is the failure this
 * env var is designed to produce rather than a silent administrator session.
 */
export const MOCK_ADMIN_PASSWORD =
  process.env.FOYER_MOCK_ADMIN_PASSWORD ?? "e2e-throwaway-not-a-credential";

/**
 * The password a **guest** signs in with on the portal, and it is the same string.
 *
 * **The mock verifies this one too.** A `loginWithAccount` that accepted any
 * non-empty password and searched every site's accounts would let a café's portal
 * sign in a hotel's guest — the operator-password defect one surface further out, and
 * the half reachable by anyone within radio range.
 *
 * One value rather than two because the mock has one: `docs/rules/security.md` §7
 * forbids a static
 * credential, so it generates a single first-boot password per process and seeds it
 * to the fixture operators and the fixture guest accounts alike. It is aliased here
 * rather than used under the administrator's name, because a guest account is not an
 * administrator and a reader should not have to infer that they coincide.
 */
export const MOCK_ACCOUNT_PASSWORD = MOCK_ADMIN_PASSWORD;
