import type { ProbeWarning } from "@foyer/contract";
import { t } from "../../i18n/nas.js";
import { Notice, Pill } from "../../ui/primitives.js";
import { type Finding, splitProbeWarnings } from "./findings.js";

/**
 * The probe result, rendered.
 *
 * One component, used by the registration wizard and by the access-point detail
 * screen, because the two must not be able to disagree about what a probe found.
 * The wizard is where an operator reads it for the first time and the detail screen
 * is where they come back to it; a second rendering would drift within a month.
 *
 * Every row carries its verdict **as a word**. Colour is reinforcement — a row that
 * meant something only by its tint would say nothing to a screen reader and nothing
 * on a printed screenshot, which is how these get circulated.
 */
export function Findings({ findings }: { findings: Finding[] }) {
  return (
    <ul className="findings">
      {findings.map((f) => (
        <li key={f.kind} className="finding" data-level={f.level} data-measured={f.measured}>
          <div className="finding-head">
            <span className="finding-title">{f.title}</span>
            <Pill level={f.level} label={f.verdict} />
          </div>
          <p className="finding-consequence">{f.consequence}</p>
        </li>
      ))}
    </ul>
  );
}

/**
 * The warnings a probe produced — **two notices, because they are two kinds of
 * thing.**
 *
 * They were one: a single `warning` notice headed *"Before you rely on this"*, holding
 * a flat list. On UniFi that list has two items on **every** probe and neither is about
 * the device being registered — one says the vendor's API cannot confirm an access
 * point, the other says some capability values are believed rather than measured. Both
 * are permanent, neither is a fault, and an operator registering a third controller
 * has already read them twice. `version-below-floor` — the one warning here that is
 * about *this* controller and has something to do about it — was the third bullet
 * under the same heading, in the same ink, behind the same glyph.
 *
 * So the standing pair keeps a notice of its own at `info`, with the `Info` glyph and
 * a title that says outright that this is what the driver is. What the probe *found*
 * keeps `warning`, `Alert`, and a title that says outright that it is about this
 * device. `splitProbeWarnings` decides which is which and is the only place that
 * decision exists.
 *
 * Found first, because it is the one with an action in it.
 *
 * `<ul>` rather than sentences run together: each item is its own thing to weigh, and
 * the marker is what says how many there are without a count in the copy.
 *
 * The `notice-stack` is inside this component rather than at each call site, so the
 * wizard and the detail screen cannot end up with different spacing between the two —
 * the same argument the panel's own header makes about the findings list.
 */
export function ProbeWarnings({ warnings }: { warnings: readonly ProbeWarning[] }) {
  const { standing, found } = splitProbeWarnings(warnings);
  if (!standing.length && !found.length) return null;
  return (
    <div className="notice-stack" style={{ margin: "0.75rem 0" }}>
      {found.length ? (
        <Notice level="warning" title={t("nas.probeWarning.foundTitle")}>
          <ul className="notice-list">
            {found.map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </Notice>
      ) : null}
      {standing.length ? (
        <Notice level="info" title={t("nas.probeWarning.standingTitle")}>
          {t("nas.probeWarning.standingLead")}
          <ul className="notice-list">
            {standing.map((line) => (
              <li key={line}>{line}</li>
            ))}
          </ul>
        </Notice>
      ) : null}
    </div>
  );
}

/**
 * What the list looks like while the device is being asked.
 *
 * Skeletons rather than a spinner, for the reason the rest of the console uses
 * them: a spinner says "wait" and a skeleton says "here is the shape of what is
 * coming", and the shape is the point on a screen whose whole job is to set an
 * expectation. `count` is the number of findings a completed probe produces, so the
 * panel does not resize under the operator when the answer lands.
 */
export function FindingsSkeleton({ count }: { count: number }) {
  return (
    <div className="findings" aria-hidden="true">
      {Array.from({ length: count }, (_, i) => (
        <div key={i} className="finding" data-level="neutral">
          <div className="finding-head">
            <span className="skeleton" style={{ width: `${7 + (i % 3) * 2}rem` }} />
            <span className="skeleton" style={{ width: "4.5rem" }} />
          </div>
          <span className="skeleton" style={{ width: `${62 + ((i * 11) % 30)}%` }} />
        </div>
      ))}
    </div>
  );
}

/** The one-line answer above the list. */
export function Verdict({
  reachable,
  attention,
}: {
  reachable: boolean;
  attention: number;
}) {
  if (!reachable) return <p className="findings-verdict">{t("nas.wizard.verdict.unmeasured")}</p>;
  return (
    <p className="findings-verdict">
      {attention === 0 ? t("nas.wizard.verdict.clean") : t("nas.wizard.verdict.findings", attention)}
    </p>
  );
}
