import type { Alarm, AlarmKind } from "@foyer/contract";
import { t } from "./i18n/index.js";

/**
 * Alarm copy, resolved from the kind the server sent.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The server sends a kind and its params, never composed `title` and `detail`
 * sentences: those would be in the server's language, so the first screen of the
 * console could greet its operator in a language they had not chosen. Three separate
 * problems, and the third is why this is a contract rule rather than a translation
 * pass: the same sentence would live in two places and drift, a server cannot know
 * the reader's language when it raises an alarm, and client-side translation would
 * be structurally impossible.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **The fallback is a real path and it is deliberately last.**
 *
 * `Alarm.title`/`.detail` survive in the contract for exactly one case: a newer
 * backend raising a kind this console predates. A console that reached for them
 * first — or that let them win when it *does* know the kind — would recreate the
 * drift the contract rules out, so the order here is: catalogue, then server text, then
 * an explicit "this console does not recognise it". Never a blank card, and never a
 * raw enum value.
 */

const TITLE: Record<AlarmKind, (params: Record<string, unknown>) => string> = {
  "nas-unreachable": () => t("alarm.nas-unreachable.title"),
  "nas-credentials-expired": () => t("alarm.nas-credentials-expired.title"),
  "nas-message-authenticator-refused": () => t("alarm.nas-message-authenticator-refused.title"),
  "nas-no-walled-garden": () => t("alarm.nas-no-walled-garden.title"),
  "nas-no-accounting": () => t("alarm.nas-no-accounting.title"),
  "sessions-orphaned": (p) => t("alarm.sessions-orphaned.title", Number(p.count ?? 0)),
  "sessions-stale": (p) => t("alarm.sessions-stale.title", Number(p.count ?? 0)),
  "controller-version-unsupported": () => t("alarm.controller-version-unsupported.title"),
};

/**
 * The sentence under the title, and it takes `params` now.
 *
 * It was `Record<AlarmKind, string>` while every body was a constant. The first kind
 * that needed a value in the body is the one that shows why: `minimumVersion` is
 * what turns *unsupported* into *9.1.105 or newer*, which is the difference between
 * a verdict and an instruction — and putting it in the title instead would have made
 * the title the long half of a card whose title is read first and at a glance. So the
 * signature changed rather than the copy being bent to fit the signature.
 */
const BODY: Record<AlarmKind, (params: Record<string, unknown>) => string> = {
  "nas-unreachable": () => t("alarm.nas-unreachable.body"),
  "nas-credentials-expired": () => t("alarm.nas-credentials-expired.body"),
  "nas-message-authenticator-refused": () => t("alarm.nas-message-authenticator-refused.body"),
  "nas-no-walled-garden": () => t("alarm.nas-no-walled-garden.body"),
  "nas-no-accounting": () => t("alarm.nas-no-accounting.body"),
  "sessions-orphaned": () => t("alarm.sessions-orphaned.body"),
  "sessions-stale": () => t("alarm.sessions-stale.body"),
  "controller-version-unsupported": (p) =>
    t(
      "alarm.controller-version-unsupported.body",
      String(p.version ?? ""),
      String(p.minimumVersion ?? ""),
    ),
};

export interface AlarmCopy {
  title: string;
  body: string;
  /** True when the catalogue had nothing and the server's text is being shown. */
  fromServer: boolean;
}

export function alarmCopy(alarm: Alarm): AlarmCopy {
  const render = TITLE[alarm.kind as AlarmKind];
  if (render) {
    const params = (alarm.params ?? {}) as Record<string, unknown>;
    return {
      title: render(params),
      body: BODY[alarm.kind as AlarmKind](params),
      fromServer: false,
    };
  }
  /* A kind newer than this console. The server's sentence is better than nothing and
     worse than the catalogue, which is exactly the order it is used in. */
  if (alarm.title) {
    return { title: alarm.title, body: alarm.detail ?? "", fromServer: true };
  }
  return { title: t("alarm.unknown.title"), body: t("alarm.unknown.body"), fromServer: true };
}

/**
 * One card per finding, with the access points named inside it.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The server raises one alarm per access point, deliberately: the subject carries
 * an id, and that link to the one device at fault is the reason the model is
 * per-object. **Grouping happens here, in the rendering, and nowhere else.** A
 * group is a view; the data stays exactly as it arrived.
 *
 * The field's answer, from Grafana's alerting guidance:
 *
 * > Paging separately for each symptom quickly turns into notification spam, even
 * > though there is a single root cause. Notification grouping consolidates
 * > related alerts into a single notification.
 *
 * Without grouping, a lab with three access points registered by hand shows
 * three "No walled garden on this device" notices and two "Cannot reach the
 * access point" notices — five cards, two facts, and an operator's eye on five
 * lines where two would do.
 *
 * **Grouped by `kind` AND `level`.** Grouping on kind alone would fold an urgent
 * `nas-message-authenticator-refused` into a minor `nas-no-walled-garden` and
 * lower the severity of the one thing that must not be lowered — BlastRADIUS,
 * CVE-2024-3596, which the server raises loudly and per-NAS on purpose.
 *
 * Order is the server's: the first appearance of a kind fixes where its group
 * sits, so grouping never reorders the list an operator was reading.
 */
export interface AlarmGroup {
  /** The kind, the level, and the first alarm of each — enough to render. */
  key: string;
  kind: AlarmKind;
  level: Alarm["level"];
  alarms: Alarm[];
}

export function groupAlarms(alarms: readonly Alarm[]): AlarmGroup[] {
  const byKey = new Map<string, AlarmGroup>();
  for (const alarm of alarms) {
    const key = `${alarm.kind}|${alarm.level}`;
    const existing = byKey.get(key);
    if (existing) {
      existing.alarms.push(alarm);
      continue;
    }
    byKey.set(key, { key, kind: alarm.kind as AlarmKind, level: alarm.level, alarms: [alarm] });
  }
  return [...byKey.values()];
}
