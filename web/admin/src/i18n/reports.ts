/**
 * Copy for the dashboard's trends panel and the CSV exports.
 *
 * The panel is a lazy chunk (`screens/Trends.tsx`), so nothing imports this but
 * the modules behind that boundary: an operator whose dashboard never finishes
 * loading the panel never downloads these sentences. `core.ts` has the rule.
 *
 * English, the source of truth; the Vietnamese is `vi/reports.ts`, loaded only when
 * the console speaks it (`lang.ts`).
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const reports = {
  "trends.title": "Trends",
  "trends.metric": "Figure",
  "trends.metric.guestDevices": "Guest devices",
  "trends.metric.vouchersRedeemed": "Vouchers redeemed",
  "trends.metric.bytes": "Data",
  "trends.range": "Period",
  "trends.range.day": "Last 14 days",
  "trends.range.week": "Last 12 weeks",
  "trends.failed.title": "Trends could not be loaded",
  "trends.chart": (figure: string, period: string) => `${figure}, ${period}, one bar per period`,
  "trends.col.period": "Period",
  "trends.col.value": "Value",
  "trends.max": (value: string) => `Highest: ${value}`,

  /* What each figure is, under the chart, because each one is less than it looks. A
     device is not a person, a redemption is a first use, and a byte total is a floor
     over the devices that report accounting at all. */
  "trends.note.guestDevices":
    "Distinct devices seen through the portal or in accounting. A phone with a private address counts once per network, not once per person.",
  "trends.note.vouchersRedeemed": "Vouchers by the day they were first used.",
  "trends.note.bytes":
    "Data from devices that send RADIUS accounting, counted on the day a session started. A floor, not a meter.",
  "trends.atLeast": (value: string) => `at least ${value}`,
  "trends.lowerBound": "A counter wrapped in at least one period, so those totals are lower bounds.",

  "export.title": "Export",
  "export.body":
    "A CSV of this venue's records for the dates chosen. Every export is recorded in the audit log, and voucher codes are never included in full.",
  "export.from": "From",
  "export.to": "To",
  "export.sessions": "Export sessions",
  "export.vouchers": "Export vouchers",
  "export.guests": "Export guests",
  "export.failed": "The export could not be made",
};

/** The shell's copy plus this panel's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/reports.js"));

export const t = makeT({ ...core, ...reports });
