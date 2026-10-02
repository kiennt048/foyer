import type { Session } from "@foyer/contract";
import { absoluteTime, duration, freshnessNote, relativeTime, sessionLevel } from "../format.js";
import { t } from "../i18n/sessions.js";
import { Alert, Clock, Slash } from "./icons.js";
import { Heartbeat, Pill, freshnessQualifier } from "./primitives.js";
import { Absent, TimeCell } from "./table.js";

/**
 * Session status and duration, shared by the table and the detail panel.
 *
 * They live here rather than in either screen because both surfaces must say the
 * same thing about the same row. A panel that renders status differently from the
 * row it opened from is a panel that quietly contradicts the list.
 */

/**
 * Status and freshness as two joined segments of one pill.
 *
 * They are independent facts. A session is Online; separately, its last sample may
 * be twenty minutes old. Encoding the second as a hue change would corrupt the
 * first, and an operator scanning the column would lose the ability to read either.
 */
export function SessionStatus({ session }: { session: Session }) {
  const level = sessionLevel(session.state);
  // The state goes in, because `stale` is a claim that an update is overdue and
  // nothing is overdue for a session that has stopped. See `freshnessQualifier`.
  const qualifier = freshnessQualifier(session.freshness, session.state);

  if (session.state === "orphaned") {
    return (
      <Pill
        level="caution-minor"
        label={t("sessions.state.orphaned")}
        icon={<Alert size={11} />}
        title={t("sessions.orphanedWhy")}
        qualifiers={[
          {
            label: relativeTime(session.lastUpdateAt) ?? t("common.unknown"),
            icon: <Clock size={11} />,
            title: absoluteTime(session.lastUpdateAt) ?? undefined,
          },
        ]}
      />
    );
  }

  if (session.state === "unknown") {
    return (
      <Pill
        level="neutral"
        label={t("sessions.state.unknown")}
        icon={<Slash size={11} />}
        // The distinction the whole design turns on: no data is not zero, and it
        // is certainly not unlimited.
        title={t("freshness.absent")}
      />
    );
  }

  return (
    <Pill
      level={level}
      label={STATE_LABEL[session.state]}
      icon={
        session.state === "online" ? (
          <Heartbeat still={session.freshness.state !== "sampled"} />
        ) : undefined
      }
      title={freshnessNote(session.freshness)}
      qualifiers={qualifier ? [qualifier] : []}
    />
  );
}

const STATE_LABEL: Record<Session["state"], string> = {
  get online() {
    return t("sessions.state.online");
  },
  get ended() {
    return t("sessions.state.ended");
  },
  get orphaned() {
    return t("sessions.state.orphaned");
  },
  get unknown() {
    return t("sessions.state.unknown");
  },
};

/**
 * True when the access point says the session started after the server's "now".
 *
 * Clock skew, and real — observed in production pfSense/FreeRADIUS deployments, not
 * hypothetical.
 */
export function startIsSkewed(session: Session): boolean {
  const started = Date.parse(session.startedAt);
  return Number.isFinite(started) && started > Date.now();
}

/**
 * The session's start time, with skew named at the point it appears.
 *
 * The duration already refuses to render a negative number, but without this the
 * *source* of that negative number sits one column away rendering "in 4 minutes" in
 * the same neutral style as "2 hours ago" — so the derived value carries the finding
 * and the fact that produced it does not. An operator scanning start times has
 * nothing to notice.
 */
export function StartTimeCell({ session }: { session: Session }) {
  if (!startIsSkewed(session)) return <TimeCell iso={session.startedAt} />;
  return (
    <span className="stack-sub-caution" title={t("sessions.skewWhy")}>
      <TimeCell iso={session.startedAt} />
    </span>
  );
}

/** A duration that names clock skew instead of rendering a negative number. */
export function DurationCell({ session }: { session: Session }) {
  const d = duration(session.sessionSeconds);
  if (!d) return <Absent hint={t("freshness.noAccounting")} />;
  if (d.anomaly) {
    return (
      <span className="stack-sub stack-sub-caution" title={t("sessions.skewWhy")}>
        {d.text}
      </span>
    );
  }
  return <span className="num">{d.text}</span>;
}
