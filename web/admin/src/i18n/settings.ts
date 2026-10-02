/**
 * Copy for the settings screens' shared parts — the collapsed "Advanced" section and
 * the number field that states its unit, range and default (`ui/settings.tsx`).
 *
 * Imported only by `/network`, `/privacy` and the portal screen's sign-in tab, all
 * lazily loaded, so these sentences travel in a chunk those screens share and the
 * entry pays nothing. `core.ts` has the rule.
 *
 * **Units are words, not symbols.** A form in milliseconds and bytes is hard to
 * read; "seconds" and "MB" are what a café owner reads without
 * stopping, and "ms" is not. KB and MB are binary (1 024), which is how the server
 * counts a request body and how every browser's download bar counts a file.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

const unit = (n: number, one: string, many: string) => (n === 1 ? one : many);

export const settings = {
  "settings.advanced.title": "Advanced settings",
  "settings.advanced.lede":
    "Technical limits, each set to a safe default. Most venues never change them; change one when support or a measurement says to.",
  "settings.reset": "Reset to default",
  "settings.range": (min: string, max: string, u: string) => `Between ${min} and ${max} ${u}.`,
  "settings.default": (d: string, u: string) => `Default ${d} ${u}.`,
  "settings.rangeCount": (min: string, max: string) => `Between ${min} and ${max}.`,
  "settings.defaultCount": (d: string) => `Default ${d}.`,
  "settings.changed": "Changed from the default.",
  "settings.unit.seconds": (n: number) => unit(n, "second", "seconds"),
  "settings.unit.minutes": (n: number) => unit(n, "minute", "minutes"),
  "settings.unit.kb": () => "KB",
  "settings.unit.mb": () => "MB",
};

void loadLocale(() => import("./vi/settings.js"));

export const t = makeT({ ...core, ...settings });
