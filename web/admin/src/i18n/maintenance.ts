/**
 * Copy for the maintenance section of `/privacy`, loaded with that screen.
 *
 * English, the source of truth; the Vietnamese is `vi/maintenance.ts`, loaded only
 * when the console speaks it (`lang.ts`).
 */
import { loadLocale, makeT, plural } from "./catalogue.js";
import { core } from "./core.js";

export const maintenance = {
  "maintenance.title": "Maintenance schedule",
  "maintenance.body":
    "Foyer cleans up by itself: expired portal and console sessions, vouchers past their expiry, sessions an access point never closed, the retention sweep and old phone codes. A change here takes effect at once, without a restart.",
  "maintenance.interval": "Run every",
  "maintenance.interval.hint": "The time between one pass and the next. Retention periods are enforced to within this.",
  "maintenance.stale": "Close a silent session after",
  "maintenance.stale.hint": (minutes: number) =>
    `Missed accounting updates. At a five-minute reporting interval that is ${plural(minutes, "minute", "minutes")} of silence. The console marks a session orphaned at 2, so 3 is the lowest.`,
  "maintenance.ceiling": "Most sessions one pass may close",
  "maintenance.ceiling.hint":
    "A pass that would close more closes none and reports a failure until somebody looks: an outage and a fault look the same from here.",
  "maintenance.batch": "Retention rows per statement",
  "maintenance.batch.hint": "Smaller holds locks for less time on a busy database; larger finishes a first sweep sooner.",
  "maintenance.default": "These are the defaults.",
  "maintenance.updated": (who: string, when: string) => `Last changed by ${who}, ${when}.`,
  "maintenance.save": "Save maintenance settings",
  "maintenance.saved": "Maintenance settings saved.",
  "maintenance.failed": "The maintenance settings were not saved",
  "maintenance.restore": "Fill in the defaults",
};

void loadLocale(() => import("./vi/maintenance.js"));

export const t = makeT({ ...core, ...maintenance });
