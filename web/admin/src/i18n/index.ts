import { makeT } from "./catalogue.js";
import { core } from "./core.js";

/* `core` is deliberately *not* re-exported here. A catalogue has exactly one module
   that defines it — `admin.test.ts` sweeps `i18n/*.ts` and refuses a key that appears
   twice, and a re-export makes every core key a duplicate. Import copy from the file
   that owns it. */
export type Copy = typeof core;
export type CopyKey = keyof Copy;

/**
 * The copy lookup for the shell and for every eagerly loaded screen. Total by
 * construction, and deliberately *narrow*.
 *
 * `K extends keyof typeof core` means a key that is not in the core catalogue does
 * not compile. There is no fallback path, because there is nothing to fall back to:
 * `core.ts` *is* the key type for anything in the entry chunk. That is the point — a
 * missing key is a build error, never a blank cell discovered by a customer.
 *
 * **A lazily loaded screen imports a different `t`** — `t` from `i18n/portal.js`,
 * `i18n/nas.js` and so on — which resolves that screen's namespace *and* core. So the
 * refusal says one of two things: the key does not exist, or it exists in
 * a namespace this module is not entitled to, which is the same sentence as "you are
 * about to pull a screen's copy into the entry chunk". `core.ts` has the whole rule.
 *
 * This is a plain function rather than a hook, with two languages as with one: the
 * language is fixed for a page's life (`lang.ts`) and a change
 * reloads, so a hook would return a constant while implying that it might not — and
 * `format.ts`, which needs the same strings, is not a component. The Vietnamese is
 * installed under this lookup by `loadLocale` (`catalogue.ts`) before the first
 * render.
 */
export const t = makeT(core);

/**
 * The locale used for absolute dates and times: `vi-VN` when the console speaks
 * Vietnamese, else `en-GB` rather than `en-US` — this console is read in Vietnam,
 * where 09/08 means the ninth of August, and an operator comparing a Foyer timestamp
 * against a NAS log must not have to guess which way round the numbers go.
 */
export { INTL_LOCALE as LOCALE } from "./lang.js";
