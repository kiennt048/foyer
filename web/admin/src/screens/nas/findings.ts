import type {
  Capabilities,
  Nas,
  NasProbe,
  ProbeWarning,
  ProbeWarningKind,
} from "@foyer/contract";
import { STATUS_LEVELS, type StatusLevel } from "../../format.js";
import { t } from "../../i18n/nas.js";

/**
 * A probe result, turned into the sentences an operator has to read once.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Why this is derived here and not read off the wire.**
 *
 * `NasProbe.checks[]` already carries `name`, `result` and `detail`, and the obvious
 * implementation renders that array. It is the wrong source for two independent
 * reasons, and the second is the one that matters:
 *
 * 1. Those are sentences the *server* composed, in the server's language. The
 *    console speaks its operator's language and the mock's are Vietnamese, so rendering them puts
 *    another language on the screen an operator was told is theirs. The contract's
 *    direction is `kind` plus params, copy from the client's catalogue, and this
 *    module is the shape that change lands in.
 * 2. They name the **mechanism**, and the operator needs the **consequence**. "The
 *    device accepts `require_message_authenticator = yes`" is true and useless at
 *    registration; "an Access-Accept cannot be forged out of a genuine reject" is
 *    the thing they are being asked to care about.
 *
 * So the structured half of the probe — `reachability` and `capabilities` — is what
 * is read, and the copy comes from the catalogue.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Why a `Nas` is needed as well as a `NasProbe`.**
 *
 * Two of the four consequences that most change what Foyer can do are **not in
 * `NasProbe` at all**: `messageAuthenticator` and `accountingConfigured` live on
 * `Nas`. A caller therefore has to re-read the device after probing it. That is a
 * contract finding, not a convenience — recorded in the report rather than papered
 * over here.
 */

export type FindingKind =
  | "reachability"
  | "messageAuthenticator"
  | "accounting"
  | "walledGarden"
  | "hardQuota"
  | "bandwidthLimits"
  | "dynamicVlan"
  | "liveReauthorize"
  | "rotateSecret";

/**
 * How many rows a probe always produces — every kind, every time.
 *
 * A probe never returns *fewer* findings because something could not be measured;
 * it returns the same nine with "not measured" on the ones that could not run. The
 * wizard's busy state draws this many skeleton rows so the panel does not resize
 * under the operator when the answer lands, and a test asserts the two agree.
 */
export const FINDING_COUNT = 9;

export interface Finding {
  kind: FindingKind;
  level: StatusLevel;
  title: string;
  /**
   * The two-word answer, for the pill.
   *
   * Drawn from the same catalogue entries the access-point table and detail screen
   * use, so a device that reads `Not attached` in a column does not read
   * `No accounting` here. Colour is reinforcement; this is the signal.
   */
  verdict: string;
  /** One sentence naming what this means for the rest of the product. */
  consequence: string;
  /**
   * False when the device did not answer, so the check never ran.
   *
   * Kept distinct from a failing check on purpose: "this device has no walled
   * garden" and "we could not ask this device about its walled garden" are
   * different facts with different next actions, and collapsing them is how an
   * unreachable controller gets read as a hardware limitation.
   */
  measured: boolean;
}

/**
 * The order findings are presented in — by what the operator has to do about them.
 *
 * Deliberately **not** `STATUS_LEVELS`, which runs no-action → act-now for the
 * gallery's benefit. Reversing that array would put `positive` above `neutral`, and
 * `neutral` here is "this device cannot do X" — which is a fact worth reading and
 * ranks above "this device can do X, as expected".
 */
const ORDER: readonly StatusLevel[] = [
  "urgent",
  "warning",
  "caution-major",
  "caution-minor",
  "negative",
  "neutral",
  "info",
  "positive",
] as const;

/* A level that is not in ORDER would silently sort first and nobody would notice.
   This is cheap and it is the kind of thing a ninth status level breaks. */
if (ORDER.length !== STATUS_LEVELS.length) {
  throw new Error("findings: ORDER and STATUS_LEVELS disagree about how many levels exist");
}

function rank(level: StatusLevel): number {
  return ORDER.indexOf(level);
}

/** A capability the console renders a control for: absent means the control is gone. */
function capability(
  kind: FindingKind,
  value: boolean | undefined,
  measured: boolean,
  yes: string,
  no: string,
  title: string,
  believed = false,
): Finding {
  if (!measured) return unmeasured(kind, title);
  if (believed) return unverifiedFinding(kind, title);
  return {
    kind,
    level: value === true ? "positive" : "neutral",
    title,
    verdict: value === true ? t("nas.finding.verdict.supported") : t("nas.finding.verdict.unsupported"),
    consequence: value === true ? yes : no,
    measured,
  };
}

/**
 * The value that was answered but is **believed rather than measured** — named by
 * `NasProbe.unverified`.
 *
 * A third standing, and it is not either of the other two. *Not measured* is a probe
 * that could not run; this is a probe that ran and returned a value its driver cannot
 * back with a measurement or a citation. Rendering it as `positive` would show an
 * operator a green tick over unmeasured claims — and the UniFi driver names three on
 * every run, so this is not a corner case: it is the ordinary path.
 *
 * `neutral`, never `positive`, and never `warning` either. The value is not known to
 * be wrong; it is known not to be known, and a capability decides whether a control
 * exists at all (`docs/rules/nas-drivers.md`).
 */
function unverifiedFinding(kind: FindingKind, title: string): Finding {
  return {
    kind,
    level: "neutral",
    title,
    verdict: t("nas.finding.verdict.unverified"),
    consequence: t("nas.finding.unverified"),
    /* `false`, because that flag is what every caller reads to decide whether this
       row may be presented as a fact. A believed value is not one. */
    measured: false,
  };
}

/** The check that never ran, which is not the same answer as a check that failed. */
function unmeasured(kind: FindingKind, title: string): Finding {
  return {
    kind,
    level: "neutral",
    title,
    verdict: t("nas.finding.verdict.notMeasured"),
    consequence: t("nas.finding.unmeasured"),
    measured: false,
  };
}

function reachabilityFinding(reachability: Nas["reachability"]): Finding {
  const base = {
    kind: "reachability" as const,
    title: t("nas.finding.reachability.title"),
    measured: true,
  };
  switch (reachability) {
    case "ok":
      return {
        ...base,
        level: "positive",
        verdict: t("nas.reach.ok"),
        consequence: t("nas.finding.reachability.ok"),
      };
    case "unreachable":
      return {
        ...base,
        level: "urgent",
        verdict: t("nas.reach.unreachable"),
        consequence: t("nas.finding.reachability.unreachable"),
      };
    case "credentials-expired":
      return {
        ...base,
        level: "urgent",
        verdict: t("nas.reach.credentialsExpired"),
        consequence: t("nas.finding.reachability.credentialsExpired"),
      };
    default:
      return {
        ...base,
        level: "neutral",
        verdict: t("nas.reach.unknown"),
        consequence: t("nas.finding.reachability.unknown"),
        measured: false,
      };
  }
}

function gardenFinding(
  kind: Capabilities["walledGarden"],
  measured: boolean,
  believed = false,
): Finding {
  const title = t("nas.finding.walledGarden.title");
  if (!measured) return unmeasured("walledGarden", title);
  if (believed) return unverifiedFinding("walledGarden", title);
  switch (kind ?? "none") {
    case "fqdn-sni":
      return {
        kind: "walledGarden",
        level: "positive",
        title,
        verdict: t("nas.garden.fqdnSni"),
        consequence: t("nas.finding.walledGarden.fqdnSni"),
        measured,
      };
    case "ip-range":
      /* Weaker, not broken. `caution-minor` is "a fault requiring attention", which
         is exactly right for a garden that works and allows more than it appears to. */
      return {
        kind: "walledGarden",
        level: "caution-minor",
        title,
        verdict: t("nas.garden.ipRange"),
        consequence: t("nas.finding.walledGarden.ipRange"),
        measured,
      };
    default:
      /* `warning`, not `caution-minor`: unbounded pre-authentication egress is a
         security posture, and the detail screen already carries it as a standing
         warning. The two screens must not disagree about how bad it is. */
      return {
        kind: "walledGarden",
        level: "warning",
        title,
        verdict: t("nas.garden.none"),
        consequence: t("nas.finding.walledGarden.none"),
        measured,
      };
  }
}

/**
 * Every finding a probe supports, ordered by what the operator must do about it.
 *
 * `nas` is the device **as it stands after the probe** — see the note above about
 * `messageAuthenticator` and `accountingConfigured` not being in `NasProbe`.
 */
export function probeFindings(probe: NasProbe, nas: Nas): Finding[] {
  const measured = probe.reachability === "ok";
  const c = probe.capabilities;
  /*
   * The capability values this driver cannot back with a measurement.
   *
   * Read off the probe rather than listed here, so the day somebody takes the
   * measurement the row stops saying *believed* without anybody editing this file —
   * and so a driver this console has never heard of is handled by the same rule. The
   * names are `Capabilities`' own properties, which is what makes the lookup below a
   * lookup rather than a mapping table.
   */
  const believed = new Set(probe.unverified);

  const findings: Finding[] = [
    reachabilityFinding(probe.reachability),

    /*
     * Gated on `measured`, not only on the recorded value.
     *
     * A device that is currently unreachable may still carry `enforced` from the
     * last time anything reached it. Rendering that in a panel headed "what this
     * probe found" would be presenting a stale measurement as a fresh one, on the
     * one screen whose entire premise is that capabilities are measured rather than
     * assumed. An unreachable probe measures nothing.
     */
    !measured || nas.messageAuthenticator === "unknown"
      ? unmeasured("messageAuthenticator", t("nas.finding.messageAuthenticator.title"))
      : {
          kind: "messageAuthenticator",
          level: nas.messageAuthenticator === "enforced" ? "positive" : "warning",
          title: t("nas.finding.messageAuthenticator.title"),
          verdict:
            nas.messageAuthenticator === "enforced"
              ? t("nas.msgauth.enforced")
              : t("nas.msgauth.refused"),
          consequence:
            nas.messageAuthenticator === "enforced"
              ? t("nas.finding.messageAuthenticator.enforced")
              : t("nas.finding.messageAuthenticator.refused"),
          measured: true,
        },

    !measured
      ? unmeasured("accounting", t("nas.finding.accounting.title"))
      : {
          kind: "accounting",
          /* `caution-minor` matches the detail screen's notice for the same fact. It
             is a fault requiring attention rather than an outage — sessions still
             work, they are simply invisible. */
          level: nas.accountingConfigured ? "positive" : "caution-minor",
          title: t("nas.finding.accounting.title"),
          verdict: nas.accountingConfigured
            ? t("nas.accounting.configured")
            : t("nas.accounting.missing"),
          consequence: nas.accountingConfigured
            ? t("nas.finding.accounting.configured")
            : t("nas.finding.accounting.missing"),
          measured,
        },

    gardenFinding(c.walledGarden, measured, believed.has("walledGarden")),

    !measured
      ? unmeasured("hardQuota", t("nas.finding.hardQuota.title"))
      : believed.has("hardQuota")
        ? unverifiedFinding("hardQuota", t("nas.finding.hardQuota.title"))
        : {
          kind: "hardQuota",
          /* Not `neutral` like the other absent capabilities. A driver that cannot
             enforce quota does not remove a control — it changes what every quota
             figure in the console *means*, which is a caution rather than a fact. */
          level: c.hardQuota ? "positive" : "caution-minor",
          title: t("nas.finding.hardQuota.title"),
          verdict: c.hardQuota
            ? t("nas.finding.verdict.quotaDevice")
            : t("nas.finding.verdict.quotaSampled"),
          consequence: c.hardQuota ? t("nas.finding.hardQuota.yes") : t("nas.finding.hardQuota.no"),
          measured,
        },

    capability(
      "bandwidthLimits",
      c.bandwidthLimits,
      measured,
      t("nas.finding.bandwidthLimits.yes"),
      t("nas.finding.bandwidthLimits.no"),
      t("nas.finding.bandwidthLimits.title"),
      believed.has("bandwidthLimits"),
    ),
    capability(
      "dynamicVlan",
      c.dynamicVlan,
      measured,
      t("nas.finding.dynamicVlan.yes"),
      t("nas.finding.dynamicVlan.no"),
      t("nas.finding.dynamicVlan.title"),
      believed.has("dynamicVlan"),
    ),
    capability(
      "liveReauthorize",
      c.liveReauthorize,
      measured,
      t("nas.finding.liveReauthorize.yes"),
      t("nas.finding.liveReauthorize.no"),
      t("nas.finding.liveReauthorize.title"),
      believed.has("liveReauthorize"),
    ),
    capability(
      "rotateSecret",
      c.rotateSecret,
      measured,
      t("nas.finding.rotateSecret.yes"),
      t("nas.finding.rotateSecret.no"),
      t("nas.finding.rotateSecret.title"),
      believed.has("rotateSecret"),
    ),
  ];

  /* Stable within a rank, so the declaration order above is the tiebreak and the
     list does not reshuffle between two probes that found the same thing. */
  return findings
    .map((f, i) => ({ f, i }))
    .sort((a, b) => rank(a.f.level) - rank(b.f.level) || a.i - b.i)
    .map(({ f }) => f);
}

/**
 * How many findings are not simply "as expected".
 *
 * Counts everything that is not `positive`, which includes the plain absences — a
 * device that cannot do VLANs is not a fault, but it is one of the things the
 * operator came here to learn, and a headline that counted only faults would read
 * "nothing to know" above a list of four things to know.
 */
export function attentionCount(findings: Finding[]): number {
  return findings.filter((f) => f.level !== "positive").length;
}

/**
 * A probe warning, as the sentence an operator reads — copy from this catalogue,
 * never from the server.
 *
 * `Record<ProbeWarningKind, …>` over the contract's closed enum, so a kind added
 * there does not build until it has copy. The same property `CATALOGUE`, `FAULT` and
 * the audit catalogue carry, and for the same reason: a warning nobody can read is a
 * warning nobody acts on, and these are the conditions an operator is being asked to
 * accept before trusting a device.
 *
 * `detail` from the server is the fallback and is **only** used for a kind this
 * console does not recognise. See `Alarm.detail` in the contract for why it exists in
 * spite of rather than because of.
 */
const WARNING: Record<ProbeWarningKind, (p: Record<string, unknown>) => string> = {
  "access-point-unconfirmed": () => t("nas.probeWarning.accessPointUnconfirmed"),
  "version-below-floor": (p) =>
    t("nas.probeWarning.versionBelowFloor", text(p.version), text(p.minimumVersion)),
  "capabilities-unverified": (p) => t("nas.probeWarning.capabilitiesUnverified", count(p.count)),
};

const text = (v: unknown): string => (typeof v === "string" ? v : "");
const count = (v: unknown): number => (typeof v === "number" && Number.isFinite(v) ? v : 0);

export function probeWarningText(w: ProbeWarning): string {
  const copy = WARNING[w.kind as ProbeWarningKind];
  if (copy) return copy(w.params ?? {});
  return w.detail ?? t("nas.probeWarning.unknown");
}

/**
 * Whether a warning is a **standing property of the driver** or a **condition this
 * probe found** — two different things that were wearing one notice.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `access-point-unconfirmed` is a **standing** property of a driver that has no
 * mechanism for `docs/rules/security.md` §4's access-point clause: it is appended on every probe,
 * healthy or not, and says nothing about the device in front of the operator.
 * `capabilities-unverified` has the same shape — every controller driver names
 * capabilities it cannot back with a measurement, so it fires on every run too.
 *
 * **UniFi is not an instance of it.** `uplinkDeviceId` is measured and the driver
 * confirms the access point, so `UniFiDriver.unconditionalWarnings()` is empty and
 * `canConfirmAccessPoint()` is true. The classification below is about what a driver
 * *is*, and a CoA driver with no such mechanism emits this on every run.
 *
 * That distinction is worth keeping in view: an absent notice means **this driver
 * checks** on UniFi and **somebody forgot** on a driver that does not, and nothing in
 * the warning list separates them. `internal/nas.ShippedConfirmsAccessPoint` exists
 * for exactly that reason and is what a screen should ask, rather than reading an
 * absence as an answer.
 *
 * That is what the split is for. **A warning that appears on every healthy probe is
 * one an operator learns to skip** — and the one it takes down with it is
 * `version-below-floor`, which is about *this* controller, is actionable, and would
 * render as the third bullet of a list whose first two never change.
 *
 * Making the constant pair quieter is not the fix either. Dropping them entirely is
 * wrong, because they must still be read once: a registration screen
 * that omits `access-point-unconfirmed` is telling an operator that a check happened.
 * They need to read as **what this driver is**, not as **what is wrong today** — so
 * they keep a notice of their own, with its own level, glyph and title. Three signals,
 * none of them colour alone.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `Record<ProbeWarningKind, …>` for the same reason `WARNING` above is one: a kind
 * added to the contract does not build until somebody has decided which of the two it
 * is. A kind this console has **never heard of** is not in the record at all, and
 * `found` is the answer there — filing an unrecognised condition as "always true, and
 * nothing to do about it" is the one mistake that cannot be noticed from the screen.
 */
const WARNING_STANDING: Record<ProbeWarningKind, "standing" | "found"> = {
  /* The vendor's API has no field for it. No controller, no version and no amount of
     configuration changes that. */
  "access-point-unconfirmed": "standing",
  /* Nothing on the device closes this — a lab measurement on Foyer's side does. The
     findings list already names *which* capabilities; this is the same fact as a
     condition, and it is as permanent as the other one until somebody measures. */
  "capabilities-unverified": "standing",
  /* This controller, this version, and an upgrade fixes it. */
  "version-below-floor": "found",
};

export interface SplitWarnings {
  /** True of this driver on every probe. Nothing to do about it; read it once. */
  standing: string[];
  /** Found on this device today, and there is something to do about it. */
  found: string[];
}

export function splitProbeWarnings(warnings: readonly ProbeWarning[]): SplitWarnings {
  const split: SplitWarnings = { standing: [], found: [] };
  for (const w of warnings) {
    const where = WARNING_STANDING[w.kind as ProbeWarningKind] ?? "found";
    split[where].push(probeWarningText(w));
  }
  return split;
}
