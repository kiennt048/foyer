import { withCopy } from "../i18n/catalogue.js";
import { lazy, Suspense, useEffect, useRef, type ReactNode } from "react";
import { Link } from "react-router";
import type { Capabilities, Freshness, Plan, Session } from "@foyer/contract";
import {
  absoluteTime,
  bytes as fmtBytes,
  duration,
  freshnessLevel,
  freshnessNote,
  intervalText,
  macIsLocallyAdministered,
  relativeTime,
} from "../format.js";
import { t } from "../i18n/sessions.js";
import { isNotFound } from "../problems.js";
import { BINDINGS } from "../keys/bindings.js";
import { BindingKeys } from "../keys/Keys.jsx";
import { Chevron, Clock } from "../ui/icons.js";
import { CopyButton, Empty, ErrorNotice, Notice, PlanName } from "../ui/primitives.js";
import { SessionStatus, startIsSkewed } from "../ui/session.jsx";
import { Absent, Mono } from "../ui/table.js";

/**
 * The session detail panel.
 *
 * A **bottom** dock rather than a side rail, because this table has nine columns and
 * a side panel would squeeze it into uselessness — and a page rather than a panel
 * would take the operator away from the list they are triaging, which is the whole
 * job.
 *
 * Two things this surface does that the table cannot. It **never truncates** — every
 * identifier is here in full, because truncation is a table's compromise and a
 * detail view has no reason to inherit it. And it has room to make the accounting
 * story legible: not just what a byte figure is, but when it was sampled, what
 * interval was asked for, and who is enforcing what. The table has to state the
 * number; this is where the number gets its provenance.
 *
 * Every field is read-only, forever. `radacct` belongs to FreeRADIUS.
 *
 * **One action lives here and it is not an edit** — disconnecting the guest, which
 * asks the *device* to end the session (`disconnectSession`). It is lazily loaded, so
 * its code and copy stay out of the entry chunk, and it renders nothing on a session
 * that has ended or for an operator who may not.
 */
const SessionDisconnect = lazy(() => withCopy(() => import("./SessionDisconnect.js")));

export interface SessionDockProps {
  session: Session | null;
  /**
   * The capabilities of the access point this session is on, or `null` when the
   * join has not resolved.
   *
   * Handed a `Session` and nothing else, the dock could not answer one figure on it: **whether the plan's rate limit is applied by anything.**
   * `Plan.enforcedBy` covers the time and data limits and says nothing about
   * `downKbps`/`upKbps`, and no other field on `Plan` does either. The only thing
   * that answers it is `Capabilities.bandwidthLimits` on the session's NAS, so the
   * join has to arrive from outside — see `rateApplication` below.
   *
   * `null` is a real third answer, not a placeholder for `false`. A NAS the list
   * did not return is not a NAS that cannot shape traffic, and rendering it as one
   * would be inventing a fact in the direction of alarm.
   */
  capabilities: Capabilities | null;
  /**
   * Whether this open was asked for — `PanelState.deliberate`.
   *
   * The panel is told *why* it is here rather than what to do about it: `Enter`, a
   * click and a link that names a session are the operator asking for the record;
   * an arrow key is not. What that buys is below, at the one effect that reads it.
   */
  deliberate: boolean;
  /** True when the cursor points at a row the current filters exclude. */
  hidden: boolean;
  /** What the detached fetch threw, or `null`. The dock says which of the two it is. */
  error: unknown;
  isLoading: boolean;
  /** 1-based position in the visible list, for orientation. Null when off-list. */
  position: { index: number; total: number } | null;
  duplicateMac: boolean;
  onPrevious: () => void;
  onNext: () => void;
  onClose: () => void;
}

export function SessionDock({
  session,
  capabilities,
  deliberate,
  hidden,
  error,
  isLoading,
  position,
  duplicateMac,
  onPrevious,
  onNext,
  onClose,
}: SessionDockProps) {
  const ref = useRef<HTMLDivElement>(null);

  /*
   * Focus moves into the panel when the operator asked for it, and `Esc` hands it
   * back to the row — the screen owns the second half, because it is the only thing
   * that knows which row that was. Focus lands on the panel container rather than on
   * the close button: the first thing a screen reader should read here is the
   * record, not the way out of it.
   *
   * `deliberate` is the whole of what changed, and it was not a mistake in either
   * half. This effect was right for an open the operator asked for; `panelIsOpen`
   * is right that a cursor with no dismissal is an open panel. Together they moved
   * focus off the list on the first `ArrowDown` and back onto it on the second.
   *
   * Keyed on the flag rather than on mount, and that is the load-bearing part: a
   * cursor move opens the panel, so by the time `Enter` says *this record, please*
   * the panel is already mounted and a mount effect would never run again.
   */
  useEffect(() => {
    if (!deliberate) return;
    ref.current?.focus();
  }, [deliberate]);

  return (
    <div className="detail-dock" role="complementary" aria-label={t("sessions.detail.region")}>
      <div className="detail-panel" ref={ref} tabIndex={-1}>
        <div className="detail-head">
          <div className="detail-head-title">
            {session ? (
              <>
                {/* The same pill the row carries, so the panel cannot contradict
                    the list it was opened from. */}
                <SessionStatus session={session} />
                <span className="mono detail-mac">{session.callingStationId}</span>
                <CopyButton value={session.callingStationId} subject={t("sessions.mac.subject")} />
              </>
            ) : (
              <span className="detail-mac">{t("sessions.detail.region")}</span>
            )}
          </div>

          <div className="detail-head-nav">
            {position ? (
              <span className="detail-position">
                {t("sessions.detail.position", position.index, position.total)}
              </span>
            ) : null}
            {/* The panel's own navigation doubles as the place `J` and `K` are
                discoverable. It never repeats the table header's actions. */}
            <button
              type="button"
              className="btn btn-sm btn-ghost btn-icon"
              title={t("sessions.detail.previous")}
              aria-label={t("sessions.detail.previous")}
              onClick={onPrevious}
            >
              <Chevron size={14} style={{ transform: "rotate(-90deg)" }} />
            </button>
            <button
              type="button"
              className="btn btn-sm btn-ghost btn-icon"
              title={t("sessions.detail.next")}
              aria-label={t("sessions.detail.next")}
              onClick={onNext}
            >
              <Chevron size={14} style={{ transform: "rotate(90deg)" }} />
            </button>
            <button type="button" className="btn btn-sm" onClick={onClose}>
              {t("common.close")}
              <BindingKeys spec={BINDINGS.detailClose} />
            </button>
          </div>
        </div>

        <div className="detail-body">
          {isLoading ? <p className="detail-loading">{t("common.loading")}</p> : null}

          {/* `not-found` keeps its calm, neutral treatment: a session that has aged out
              of the record is an ordinary thing to walk into from a bookmark. Any
              other failure is urgent and says so, rather than borrowing the sentence
              for the one case it is not. */}
          {error ? (
            isNotFound(error) ? (
              <Notice level="neutral" title={t("sessions.detail.notFound")} />
            ) : (
              <ErrorNotice error={error} />
            )
          ) : null}

          {!isLoading && !error && !session ? (
            <Empty
              title={t("sessions.detail.empty.title")}
              description={t("sessions.detail.empty.description")}
            />
          ) : null}

          {session ? (
            <Suspense fallback={null}>
              <SessionDisconnect session={session} />
            </Suspense>
          ) : null}

          {session ? (
            <SessionDetail
              session={session}
              capabilities={capabilities}
              hidden={hidden}
              duplicateMac={duplicateMac}
            />
          ) : null}
        </div>
      </div>
    </div>
  );
}

/* ── the record ───────────────────────────────────────────────────────────── */

function SessionDetail({
  session,
  capabilities,
  hidden,
  duplicateMac,
}: {
  session: Session;
  capabilities: Capabilities | null;
  hidden: boolean;
  duplicateMac: boolean;
}) {
  const f = session.freshness;
  const every = intervalText(f);
  const d = duration(session.sessionSeconds);
  const plan = session.plan;
  const planDuration = plan?.durationMinutes ? (duration(plan.durationMinutes * 60)?.text ?? null) : null;
  const randomised = session.macIsRandomised || macIsLocallyAdministered(session.callingStationId);
  const skewed = startIsSkewed(session);
  const applied = rateApplication(plan, capabilities);

  return (
    <>
      <Alerts session={session} hidden={hidden} duplicateMac={duplicateMac} />

      <div className="detail-grid">
        {/* ── usage ─────────────────────────────────────────────────────────── */}
        <section className="detail-section">
          <h3 className="section-title">{t("sessions.detail.usage")}</h3>

          <div className="figures">
            {/*
              A wrapped counter is shown as the floor it is. `outputOctetsTotal` adds
              4 GiB per observed wrap, and two wraps between two accounting records
              are indistinguishable from one — at the 300-second interim floor that
              is 114 Mbps, which a hotel's uplink reaches — so the value is prefixed
              "at least" and the caption says why. Never clamped, never hidden.
            */}
            <Figure
              label={t("sessions.col.down")}
              value={byteFigure(session.outputOctets, session.outputOctetsTotal, session.countersWrapped)}
              f={f}
              anomaly={session.countersWrapped}
              anomalyWhy={t("sessions.wrapped.why", fmtBytes(session.outputOctets) ?? "")}
            />
            <Figure
              label={t("sessions.col.up")}
              value={byteFigure(session.inputOctets, session.inputOctetsTotal, session.countersWrapped)}
              f={f}
              anomaly={session.countersWrapped}
              anomalyWhy={t("sessions.wrapped.why", fmtBytes(session.inputOctets) ?? "")}
            />
            <Figure
              label={t("sessions.col.duration")}
              value={d ? d.text : null}
              anomaly={d?.anomaly ?? false}
              anomalyWhy={t("sessions.skewWhy")}
              f={f}
            />
          </div>

          <dl className="detail-fields">
            <Field
              label={t("sessions.col.lastUpdateTime")}
              note={f.state === "absent" ? t("freshness.absent") : undefined}
            >
              <Timestamp iso={session.lastUpdateAt} absent={t("freshness.noAccounting")} />
            </Field>
          </dl>
        </section>

        {/* ── provenance ────────────────────────────────────────────────────── */}
        <section className="detail-section">
          <h3 className="section-title">{t("sessions.detail.sampling")}</h3>
          <dl className="detail-fields">
            <Field
              label={t("sessions.detail.interval")}
              note={every ? t("sessions.detail.intervalWhy") : t("sessions.detail.intervalWhyAbsent")}
            >
              {every ?? <None>{t("sessions.detail.intervalNone")}</None>}
            </Field>
            <Enforcement session={session} />
          </dl>
          {/*
            Outside the `<dl>`, and that is correctness rather than tidiness.
            A `<p>` as a direct child of a definition list is `definition-list`,
            serious: a `<dl>` may contain only `<dt>`/`<dd>` groups, `<div>`,
            `<script>` and `<template>`, so this paragraph inside it would break the list
            structure a screen reader announces the whole section through.

            The sweep walks a list of URLs and the dock needs a session id that only
            exists once the table has rendered, so axe sees this surface only through
            a route that reaches it.

            It belongs here on the merits too: it is prose about the section, not the
            definition of a term.
          */}
          {every ? (
            <p className="detail-prose">{t("sessions.detail.overshoot", every)}</p>
          ) : null}
        </section>

        {/* ── what was granted ──────────────────────────────────────────────── */}
        <section className="detail-section">
          <h3 className="section-title">{t("sessions.detail.granted")}</h3>
          <dl className="detail-fields">
            <Field label={t("sessions.detail.plan")}>
              {plan?.name ? <PlanName plan={plan} /> : <None>{t("sessions.detail.planNone")}</None>}
            </Field>
            <Field label={t("sessions.detail.timeLimit")}>
              {/* `duration()` rather than the raw figure: "1440 minutes" sitting
                  next to a plan whose own name says one day made the reader do
                  arithmetic Foyer had already written a formatter for. */}
              {planDuration ? planDuration : <None>{t("sessions.detail.noLimit")}</None>}
            </Field>
            <Field
              label={t("sessions.detail.dataAllowance")}
              note={plan?.dataAllowanceMb ? t("sessions.detail.dataAllowanceWhy") : undefined}
            >
              {plan?.dataAllowanceMb ? (
                fmtBytes(plan.dataAllowanceMb * 1e6)
              ) : (
                <None>{t("sessions.detail.noLimit")}</None>
              )}
            </Field>
            {/*
              The rate is *stated*, never hidden, and it travels with who is applying
              it — the same shape as "Quota enforced by" one section above.

              Hiding it on `bandwidthLimits: false` was considered and is wrong. The
              rule qualified after `rotateSecret`: a capability says what the driver
              can do on the operator's behalf, not what the product may offer — so ask
              whether a false one makes the thing *impossible* or merely *unenforced*.
              A rate-limit **field** on a NAS screen is a control, and a control the
              driver cannot honour is impossible: hide it. This is not a control. It is
              the record of what the guest was granted, the plan genuinely does say it,
              and it is the first thing an operator needs when a guest complains that
              they were promised 5 Mbps. Hiding it would leave the dock unable to
              answer that, and would delete exactly the half of the contradiction that
              makes the diagnosis — "the plan promises 5 Mbps and this device applies
              nothing" needs both halves on screen.
            */}
            <Field
              label={t("sessions.detail.rateLimit")}
              name="rate-limit"
              applied={applied}
              note={RATE_NOTE[applied]}
              emphasis={applied === "unapplied"}
            >
              {applied === "none" ? (
                <None>{t("sessions.detail.noLimit")}</None>
              ) : (
                t("sessions.detail.rateValue", rate(plan?.downKbps), rate(plan?.upKbps))
              )}
            </Field>
          </dl>
        </section>

        {/* ── identity ──────────────────────────────────────────────────────── */}
        <section className="detail-section">
          <h3 className="section-title">{t("sessions.detail.identity")}</h3>
          <dl className="detail-fields">
            <Field label={t("sessions.detail.authenticatedBy")}>
              {IDENTITY_KIND[session.identityKind ?? "unknown"] ?? t("sessions.identityKind.unknown")}
            </Field>
            <Field label={t("sessions.detail.username")}>
              {session.username ?? <None>{t("sessions.detail.usernameNone")}</None>}
            </Field>
            <Field
              label={t("sessions.col.mac")}
              note={randomised ? t("sessions.mac.randomisedWhy") : t("sessions.detail.macWhy")}
            >
              {/* Never truncated here. The whole point of a detail surface. */}
              <Mono>{session.callingStationId}</Mono>
            </Field>
            {/* There is no hostname row, and there must not be one again: the field
                is gone from the contract because recovering it needs DHCP
                fingerprinting, which is out of scope. */}
            <Field label={t("sessions.detail.ipAddress")}>
              {session.ipAddress ? (
                <Mono>{session.ipAddress}</Mono>
              ) : (
                <None>{t("sessions.detail.ipAddressNone")}</None>
              )}
            </Field>
          </dl>
        </section>

        {/* ── network ───────────────────────────────────────────────────────── */}
        <section className="detail-section">
          <h3 className="section-title">{t("sessions.detail.network")}</h3>
          <dl className="detail-fields">
            <Field label={t("sessions.detail.accessPoint")}>
              <Link
                className="row-link"
                to={`/access-points/${session.nasId}`}
                title={t("sessions.detail.accessPointLink")}
              >
                {session.nasName ?? session.nasId}
              </Link>
            </Field>
            <Field label={t("sessions.detail.ssid")}>
              {session.ssid ?? <None>{t("sessions.detail.ssidNone")}</None>}
            </Field>
          </dl>
        </section>

        {/* ── the record itself ─────────────────────────────────────────────── */}
        <section className="detail-section">
          <h3 className="section-title">{t("sessions.detail.record")}</h3>
          <dl className="detail-fields">
            <Field label={t("sessions.detail.sessionId")}>
              <Mono>{session.id}</Mono>
            </Field>
            <Field
              label={t("sessions.detail.acctSessionId")}
              note={session.acctSessionId ? t("sessions.detail.acctSessionIdWhy") : undefined}
            >
              {session.acctSessionId ? (
                <Mono>{session.acctSessionId}</Mono>
              ) : (
                <None>{t("sessions.detail.acctSessionIdNone")}</None>
              )}
            </Field>
            {/* The skew is named here, on the fact that carries it, and not only on
                the duration it happens to poison. A start time reading "in 4
                minutes" in the same style as "2 hours ago" is the finding itself
                rendered as ordinary. */}
            <Field
              label={t("sessions.col.startTime")}
              note={skewed ? t("sessions.skewWhy") : undefined}
              emphasis={skewed}
            >
              <Timestamp iso={session.startedAt} anomaly={skewed} />
            </Field>
            {skewed ? (
              // The two clocks, side by side. A duration that reads "clock skew" is
              // honest but unactionable; the pair of timestamps is what an operator
              // takes to the access point. Rendered only while the start really is
              // in the future — otherwise the pair would disprove its own caption.
              <Field label={t("sessions.detail.serverTime")} note={t("sessions.skewWhy")}>
                <Timestamp iso={new Date().toISOString()} />
              </Field>
            ) : null}
            <Field label={t("sessions.detail.endTime")}>
              <Timestamp iso={session.endedAt} absent={t("sessions.detail.endTimeNone")} />
            </Field>
            <Field label={t("sessions.detail.terminateCause")}>
              {session.terminateCause ?? <None>{t("sessions.detail.terminateCauseNone")}</None>}
            </Field>
          </dl>
        </section>
      </div>

      <p className="detail-prose detail-footnote">{t("sessions.detail.readOnly")}</p>
    </>
  );
}

/* ── pieces ───────────────────────────────────────────────────────────────── */

const IDENTITY_KIND: Record<string, string> = {
  get voucher() {
    return t("sessions.identityKind.voucher");
  },
  get account() {
    return t("sessions.identityKind.account");
  },
  get "mac-auth"() {
    return t("sessions.identityKind.macAuth");
  },
  get dot1x() {
    return t("sessions.identityKind.dot1x");
  },
  get unknown() {
    return t("sessions.identityKind.unknown");
  },
};

const rate = (kbps: number | null | undefined): string =>
  kbps ? `${Math.round(kbps / 1000)} Mbps` : t("common.unknown");

/* ── the rate limit, and whether anything is applying it ──────────────────── */

/**
 * What the plan's rate limit actually is on this access point.
 *
 * `none` — the plan states no rate, so there is nothing to qualify.
 * `applied` — the driver can shape traffic, so the figure is a fact.
 * `unapplied` — the driver cannot, so the figure is what was written down and
 *   nothing more. This is the case the whole function exists for.
 * `unknown` — the NAS behind this session has not been resolved. Deliberately not
 *   folded into `unapplied`: "no device is applying this" is a statement of fact
 *   and must not be made from a list that simply has not arrived.
 *
 * Kept pure and exported so the gating is testable without a DOM — the join is the
 * part that is easy to get wrong, and it is the part a screenshot cannot prove.
 */
export type RateApplication = "none" | "applied" | "unapplied" | "unknown";

export function rateApplication(
  plan: Plan | null | undefined,
  capabilities: Capabilities | null | undefined,
): RateApplication {
  /* The same truthiness the value used before this field grew a qualifier: a plan
     naming 0 kbps is naming no limit, and reads "None set." as it always did. */
  if (!plan || !(plan.downKbps || plan.upKbps)) return "none";
  if (!capabilities) return "unknown";
  return capabilities.bandwidthLimits ? "applied" : "unapplied";
}

/**
 * One note per state, so the figure is never bare.
 *
 * `applied` gets a sentence too, and not only for symmetry: a qualifier that appears
 * exclusively in the bad case is one an operator learns to skim past, and an
 * attribute that is only ever set one way cannot be shown to discriminate. The
 * enforcement row above states its enforcer in all three of its states for the same
 * reason.
 */
const RATE_NOTE: Record<RateApplication, string | undefined> = {
  none: undefined,
  get applied() {
    return t("sessions.detail.rateAppliedWhy");
  },
  get unapplied() {
    return t("sessions.detail.rateUnappliedWhy");
  },
  get unknown() {
    return t("sessions.detail.rateUnknownWhy");
  },
};

/**
 * An absence, stated in words.
 *
 * The dock had one `–` meaning "not measurable" and, one column later, another `–`
 * meaning "no limit was set" — opposite facts sharing a glyph, with the difference
 * living in a `title` that neither a touch user nor a keyboard user ever receives.
 * A table has to compress; a detail panel has room, and this is what the room is
 * for. `Absent` and its hyphen stay in the tables where width is genuinely scarce.
 */
function None({ children }: { children: string }) {
  return <span className="empty-value">{children}</span>;
}

/**
 * A byte figure for the dock, marked as a floor when the counter has wrapped.
 *
 * `null` stays `null` so `Figure` can draw it as absent — a device that sends no
 * accounting has not transferred nothing.
 */
function byteFigure(
  reported: number | null | undefined,
  total: number | null | undefined,
  wrapped: boolean | undefined,
): string | null {
  const text = fmtBytes(reported);
  if (text === null) return null;
  if (!wrapped) return text;
  return t("bytes.atLeast", fmtBytes(total ?? reported) ?? text);
}

/**
 * One accounting figure, with its freshness attached rather than beside it.
 *
 * `null` is drawn as absent, never as `0`: a device that sends no accounting has not
 * transferred nothing, it has told us nothing.
 */
function Figure({
  label,
  value,
  f,
  anomaly,
  anomalyWhy,
}: {
  label: string;
  value: string | null;
  f: Freshness;
  anomaly?: boolean;
  anomalyWhy?: string;
}) {
  const level = freshnessLevel(f);
  return (
    <div className="figure">
      <div className="figure-label">{label}</div>
      <div className="figure-value" data-anomaly={anomaly} title={anomaly ? anomalyWhy : undefined}>
        {value ?? <Absent hint={t("freshness.absent")} />}
      </div>
      <div className="figure-note" data-level={level ?? undefined} title={freshnessNote(f)}>
        <Clock size={11} />
        {freshnessCaption(f)}
      </div>
    </div>
  );
}

/**
 * The caption under a figure.
 *
 * Colour is never the only signal, so a stale sample says so in words rather than
 * only turning amber — "Sampled 20 minutes ago" on a five-minute interval is a fact
 * an operator has to do arithmetic on before it becomes a fault.
 */
function freshnessCaption(f: Freshness): string {
  if (f.state === "absent") return t("sessions.detail.figureAbsent");
  if (f.state === "live") return t("metric.live");
  const rel = relativeTime(f.sampledAt);
  const every = intervalText(f);
  if (f.state === "stale" && rel && every) return t("metric.overdue", rel, every);
  if (rel && every) return t("metric.sampledEvery", rel, every);
  if (rel) return t("metric.sampled", rel);
  return freshnessNote(f);
}

/** Who is actually applying the limit — the field that keeps the panel honest. */
function Enforcement({ session }: { session: Session }) {
  const by = session.plan?.enforcedBy;
  if (!by) return null;
  const label =
    by === "nas"
      ? t("sessions.detail.enforcedByNas")
      : by === "foyer-sampled"
        ? t("sessions.detail.enforcedBySampled")
        : t("sessions.detail.enforcedByNothing");
  const note =
    by === "nas"
      ? t("sessions.detail.enforcedByNasWhy")
      : by === "foyer-sampled"
        ? t("sessions.detail.enforcedBySampledWhy")
        : t("sessions.detail.enforcedByNothingWhy");
  return (
    <Field label={t("sessions.detail.enforcedBy")} note={note} emphasis={by === "unenforced"}>
      {label}
    </Field>
  );
}

function Field({
  label,
  name,
  applied,
  note,
  emphasis,
  children,
}: {
  label: string;
  /**
   * A stable handle for a field whose *meaning* something outside this file needs
   * to locate — the end-to-end locks address the console through structure and
   * `data-*`, never through copy, because the catalogue is still being edited.
   */
  name?: string;
  /** Whether the stated limit is being applied by anything. See `rateApplication`. */
  applied?: RateApplication;
  note?: string;
  emphasis?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="detail-field" data-field={name} data-applied={applied} data-emphasis={emphasis}>
      <dt>{label}</dt>
      <dd>
        <div className="detail-value">{children}</div>
        {note ? <p className="detail-note">{note}</p> : null}
      </dd>
    </div>
  );
}

/** Absolute with the UTC offset, relative underneath. A detail view shows both. */
function Timestamp({
  iso,
  absent,
  anomaly,
}: {
  iso: string | null | undefined;
  absent?: string;
  /** The value is not a plain fact — a start time in the future, say. */
  anomaly?: boolean;
}) {
  const abs = absoluteTime(iso);
  const rel = relativeTime(iso);
  if (!abs || !iso) return absent ? <None>{absent}</None> : <Absent />;
  return (
    <time dateTime={iso}>
      {abs}
      {rel ? (
        <span className="detail-relative" data-anomaly={anomaly || undefined}>
          {rel}
        </span>
      ) : null}
    </time>
  );
}

/** What is wrong with this session, said once and at the top. */
function Alerts({
  session,
  hidden,
  duplicateMac,
}: {
  session: Session;
  hidden: boolean;
  duplicateMac: boolean;
}) {
  const skew = (session.sessionSeconds ?? 0) < 0 || startIsSkewed(session);
  /* `stale` is a fault requiring attention and the list already banners it; the
     detail screen — the surface an operator opens *because* a row looked wrong —
     said nothing. Suppressed for an ended session, where nothing is overdue. */
  const stale = session.freshness.state === "stale" && session.state !== "ended";
  const wrapped = Boolean(session.countersWrapped);
  const nothing =
    !hidden &&
    !duplicateMac &&
    !skew &&
    !stale &&
    !wrapped &&
    session.state !== "orphaned" &&
    session.freshness.state !== "absent";
  if (nothing) return null;

  return (
    <div className="notice-stack detail-alerts">
      {hidden ? (
        <Notice level="info" title={t("sessions.detail.hidden")}>
          {t("sessions.detail.hiddenBody")}
        </Notice>
      ) : null}
      {stale ? (
        <Notice level="caution-minor" title={t("sessions.detail.stale.title")}>
          {t("sessions.detail.stale.body")}
        </Notice>
      ) : null}
      {wrapped ? (
        <Notice level="caution-minor" title={t("sessions.detail.wrapped.title")}>
          {t("sessions.detail.wrapped.body")}
        </Notice>
      ) : null}
      {session.state === "orphaned" ? (
        <Notice level="caution-minor" title={t("sessions.detail.orphaned.title")}>
          {t("sessions.orphanedWhy")}
        </Notice>
      ) : null}
      {session.freshness.state === "absent" ? (
        <Notice level="neutral" title={t("sessions.detail.noAccounting.title")}>
          {t("sessions.detail.noAccounting.body")}
        </Notice>
      ) : null}
      {skew ? (
        <Notice level="caution-minor" title={t("sessions.detail.skew.title")}>
          {t("sessions.skewWhy")}
        </Notice>
      ) : null}
      {duplicateMac ? (
        <Notice level="caution-minor" title={t("sessions.detail.duplicate.title")}>
          {t("sessions.mac.duplicateWhy")}
        </Notice>
      ) : null}
    </div>
  );
}
