import type { Freshness, Session } from "@foyer/contract";
import { LOCALE, t } from "./i18n/index.js";

/**
 * Formatters. Every one of them has an opinion about a case the naive version
 * gets wrong, and each of those cases is in the fixtures.
 *
 * No string is written here. A formatter that inlines its own words is a formatter
 * that has to be found again when the copy changes, so every word comes from the
 * catalogue and only the arithmetic lives in this file.
 */

/** Bytes, never with false precision. `null` is not zero and must not read as it. */
export function bytes(n: number | null | undefined): string | null {
  if (n == null) return null;
  if (n < 1000) return `${n} B`;
  const units = ["kB", "MB", "GB", "TB"];
  let v = n / 1000;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

/**
 * A **budget** figure: a ceiling, or the counter standing next to one.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Base 1024, labelled kB, and that is deliberate — do not "fix" it.**
 *
 * It is technically KiB. The label stays `kB` because this repository's entire
 * vocabulary is *the 50 kB portal budget*, *100 kB of images*, *8 kB of logo* —
 * the documentation, the contract's own docstrings and `tools/check-budget.mjs` all say
 * so, and every one of those numbers is `n * 1024`. Renaming the famous 50 kB budget
 * to "51 kB" on the one screen an operator reads it would be a worse lie than the
 * unit label is.
 *
 * **Why this is not `bytes` above.** `bytes` is decimal and stays decimal: it renders
 * accounting counters, where the number is traffic a NAS reported and SI is what
 * everything measuring it means. The two formatters exist because a ceiling and a
 * network counter are different quantities — but a ceiling and *the counter beside
 * it* are not. On different bases while both said "kB", one limit would read
 * **100 kB** on `/portal` and **102 kB** in the refusal about it.
 *
 * One unit throughout, never MB: the two figures in "41 kB of 100 kB" have to be
 * comparable by eye, which is the whole reason this function exists.
 */
export function budgetBytes(n: number): string {
  return `${(n / 1024).toFixed(n < 10 * 1024 ? 1 : 0)} kB`;
}

export interface Duration {
  text: string;
  /** Set when the NAS and server clocks disagree — the value is not a duration. */
  anomaly: boolean;
}

/**
 * A session duration.
 *
 * Negative values are real: they come from clock skew between the NAS and the
 * server and are observed in production FreeRADIUS deployments. Rendering
 * `-00:04:12` implies the value means something. It does not, so it is named.
 */
export function duration(seconds: number | null | undefined): Duration | null {
  if (seconds == null) return null;
  if (seconds < 0) return { text: t("freshness.clockSkew"), anomaly: true };
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  // A zero component is dropped rather than printed. "40 days 0 hours" spends a
  // column's width saying nothing.
  if (d > 0) {
    const text = h > 0 ? `${t("unit.days", d)} ${t("unit.hours", h)}` : t("unit.days", d);
    return { text, anomaly: false };
  }
  if (h > 0) {
    const text =
      m > 0 ? `${t("unit.hours", h)} ${t("unit.minutesShort", m)}` : t("unit.hours", h);
    return { text, anomaly: false };
  }
  return { text: t("unit.minutesShort", m), anomaly: false };
}

/** Relative time for the cell. The absolute form lives in the tooltip. */
export function relativeTime(iso: string | null | undefined): string | null {
  if (!iso) return null;
  const then = Date.parse(iso);
  if (Number.isNaN(then)) return null;
  const secs = Math.round((Date.now() - then) / 1000);
  const future = secs < 0;
  const s = Math.abs(secs);
  if (s < 45) return future ? t("time.soon") : t("time.justNow");
  if (s < 3600) {
    const n = Math.round(s / 60);
    return future ? t("time.inMinutes", n) : t("time.minutesAgo", n);
  }
  if (s < 86400) {
    const n = Math.round(s / 3600);
    return future ? t("time.inHours", n) : t("time.hoursAgo", n);
  }
  const n = Math.round(s / 86400);
  return future ? t("time.inDays", n) : t("time.daysAgo", n);
}

/**
 * The absolute form, always with the UTC offset.
 *
 * Without the offset an operator comparing a Foyer timestamp against a NAS log has
 * no way to know whether a discrepancy is real — which is exactly the situation the
 * clock-skew fixture exists to make visible.
 */
export function absoluteTime(iso: string | null | undefined): string | null {
  if (!iso) return null;
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return null;
  const offsetMin = -d.getTimezoneOffset();
  const sign = offsetMin >= 0 ? "+" : "−";
  const oh = Math.floor(Math.abs(offsetMin) / 60);
  const om = Math.abs(offsetMin) % 60;
  const offset = `UTC${sign}${oh}${om ? `:${String(om).padStart(2, "0")}` : ""}`;
  return `${d.toLocaleString(LOCALE, { dateStyle: "medium", timeStyle: "short" })} (${offset})`;
}

/**
 * Splits an identifier so the tail is never truncated.
 *
 * MACs, `Acct-Session-Id`s and audit session IDs all carry their discriminating
 * characters at the end. Truncating from the right hides exactly the part that
 * tells two rows apart.
 */
export function splitForTruncation(value: string, keepTail = 8): { head: string; tail: string } {
  if (value.length <= keepTail) return { head: "", tail: value };
  return { head: value.slice(0, value.length - keepTail), tail: value.slice(-keepTail) };
}

/** The OUI, when the address is a real one. Randomised addresses have no vendor. */
export function macIsLocallyAdministered(mac: string): boolean {
  const first = Number.parseInt(mac.slice(0, 2), 16);
  return Number.isFinite(first) && (first & 0b10) !== 0;
}

/**
 * The status vocabulary, keyed on **required action** rather than severity.
 *
 * This is an array first and a type second, and the order matters: the type is
 * derived from the array, so the array is the only declaration. Anything that has
 * to enumerate the levels — the design-system gallery, the test that checks each
 * one has colours and a pill rule — reads this rather than restating it, which is
 * what makes "someone adds a ninth level" a compile or test failure instead of a
 * status that renders as an unstyled transparent block.
 *
 * Ordered from "no action" to "act now", which is also the order the gallery and
 * the alarm list present them in.
 */
export const STATUS_LEVELS = [
  "neutral",
  "info",
  "positive",
  "negative",
  "caution-minor",
  "caution-major",
  "warning",
  "urgent",
] as const;

export type StatusLevel = (typeof STATUS_LEVELS)[number];

/**
 * Session state → status level, keyed on what the operator must do.
 *
 * `orphaned` is `caution-minor`: it is a fault requiring correction, not an outage.
 * `unknown` is `neutral`, because no accounting arriving is not itself a problem
 * with this session — it is a property of the NAS, alarmed separately.
 */
export function sessionLevel(state: Session["state"]): StatusLevel {
  switch (state) {
    case "online":
      return "positive";
    case "ended":
      return "neutral";
    case "orphaned":
      return "caution-minor";
    case "unknown":
      return "neutral";
    default:
      return "neutral";
  }
}

export function freshnessLevel(f: Freshness): StatusLevel | null {
  switch (f.state) {
    case "stale":
      return "caution-minor";
    case "absent":
      return "neutral";
    default:
      return null;
  }
}

/**
 * The sentence that has to travel with any accounting-derived number.
 *
 * `radacct` advances only on Interim-Update or Stop, `Acct-Interim-Interval` has a
 * 300-second floor, and on the controller-API path there is no Access-Accept in
 * which to request updates at all.
 */
export function freshnessNote(f: Freshness): string {
  const every = intervalText(f);
  switch (f.state) {
    case "live":
      return t("freshness.live");
    case "sampled":
      return every ? t("freshness.sampled", every) : t("freshness.sampledUnknown");
    case "stale":
      return every ? t("freshness.stale", every) : t("freshness.staleUnknown");
    case "absent":
      return t("freshness.absent");
    default:
      return "";
  }
}

/** The sampling interval in words, or `null` when the NAS never declared one. */
export function intervalText(f: Freshness): string | null {
  return f.intervalSeconds ? t("unit.minutes", Math.round(f.intervalSeconds / 60)) : null;
}
