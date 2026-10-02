/**
 * Copy for the site's time zone, on the sign-in tab of `/portal`, loaded with it.
 *
 * Its own namespace in the portal screen's lazy chunk, so the entry carries none of
 * it; `core.ts` has the rule.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const siteTime = {
  "siteTime.title": "Time zone",
  "siteTime.lede":
    "Where this venue's day starts. It decides when the daily message limit resets, what counts as today on the dashboard and in reports, and when a page block's start and end dates begin.",
  "siteTime.label": "Time zone",
  "siteTime.now": (time: string) => `It is ${time} there now.`,
  "siteTime.save": "Save time zone",
  "siteTime.saved": "Time zone saved",
  "siteTime.saveFailed": "Time zone not saved",
};

/** The shell's copy plus this panel's. */
void loadLocale(() => import("./vi/sitetime.js"));

export const t = makeT({ ...core, ...siteTime });
