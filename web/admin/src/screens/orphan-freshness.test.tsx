import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Metric } from "@foyer/contract";
import { core } from "../i18n/core.js";
import { Foot } from "../ui/primitives.js";
import { orphanFoot } from "./Dashboard.js";

/**
 * The orphan count's line on the dashboard, console side.
 *
 * `orphanedSessions` is a `Metric`, not a bare `integer`, so the card is not a
 * `PlainCard` printing a fixed caption. `docs/rules/frontend.md` requires any
 * figure derived from accounting to travel with its freshness, and this one is the
 * figure whose entire meaning is *these have not been heard from*.
 *
 * ── what is under test, and it is not "a freshness is rendered" ──────────────
 *
 * The easy version is `<MetricCard metric={o.orphanedSessions} />`
 * and nothing else. It typechecks, it renders a freshness, and it is **wrong in
 * words**: `metricFoot` prints `metric.sampled` — *"Sampled 40 days ago"* — which
 * one card to the left means *this count is forty days behind*, and which here
 * would say the same thing about a count that is exact. The freshness object is
 * derived by one rule on both servers; the sentence is not shared, and that is the
 * decision these cases pin.
 *
 * ── how each fails ───────────────────────────────────────────────────────────
 *
 * Every case here goes red against `describe={metricFoot}` — the one-line version —
 * which is the shape a future edit would most plausibly collapse back to. That sabotage is re-injected below rather than described,
 * because a claim about a red run in a comment is not evidence and this repository
 * has a rule about it. `metricFootSabotage` reproduces `metricFoot`'s output for
 * the fixtures here, so the assertions are checked against a live competing
 * rendering on every run.
 */

/** A `Metric` shaped like the one both servers now answer for orphans. */
function orphans(count: number, agoMs: number | null): Metric {
  return {
    value: count,
    freshness: {
      // `stale` whenever the count is non-zero, `live` when it is not — see the
      // contract's `Overview` description. `intervalSeconds` is null on both
      // branches: this set has no one cadence behind it.
      state: agoMs === null ? "live" : "stale",
      sampledAt: new Date(Date.now() - (agoMs ?? 0)).toISOString(),
      intervalSeconds: null,
    },
  };
}

const DAY = 24 * 60 * 60 * 1000;

/**
 * What `metricFoot` would print for these fixtures — the rendering this decision
 * rejects, kept executable so the cases below cannot pass against it by accident.
 *
 * It is derived from `core` rather than typed out, so a change to the shared
 * sentence moves this too instead of leaving a stale copy behind (the
 * second-source-of-truth shape `docs/rules/database.md` names).
 */
function metricFootSabotage(m: Metric): string {
  if (m.freshness.state === "live") return core["metric.live"];
  // No interval, so `metricFoot` falls to the bare form.
  return core["metric.sampled"](relative(m));
}

/** The console's own relative-time wording, for building expectations. */
function relative(m: Metric): string {
  const days = Math.round((Date.now() - Date.parse(m.freshness.sampledAt!)) / DAY);
  return core["time.daysAgo"](days);
}

describe("the orphan count's line names its subject, not its sample", () => {
  it("reports how long ago the oldest of them was heard from", () => {
    const m = orphans(3, 40 * DAY);
    const foot = orphanFoot(m);

    expect(
      foot.text,
      "the useful sentence about an orphan count is when the oldest of them was last " +
        "heard from: ten minutes and forty days are the same integer and different faults",
    ).toBe(core["dashboard.orphanedOldest"](core["time.daysAgo"](40)));
    /* The timestamp has to survive into the DOM, not merely into a string — the
       whole point of widening the field was that the console could show it. */
    const html = renderToStaticMarkup(<Foot act={foot.act}>{foot.text}</Foot>);
    expect(html).toContain(core["time.daysAgo"](40));

    /* **The sabotage, executed rather than described.** */
    expect(
      foot.text,
      "this is `metricFoot`'s sentence. Beside the online card it reads as `this count " +
        "is forty days behind`, which is true there and false here — the count is exact " +
        "and what is old is its subject",
    ).not.toBe(metricFootSabotage(m));
  });

  it("does not carry the online figure's claim in its tooltip either", () => {
    const foot = orphanFoot(orphans(3, 40 * DAY));
    expect(foot.title).toBe(core["dashboard.orphanedNote"]);
    /* `freshnessNote` for a `stale` metric with no interval. Asserted against the
       catalogue rather than the string, for the same reason as above. */
    expect(
      foot.title,
      "`These figures are old` is a claim about the number. The number is current; " +
        "the sessions are old",
    ).not.toBe(core["freshness.staleUnknown"]);
  });

  it("says what the figure counts, and no age, when there are none", () => {
    const m = orphans(0, null);
    const foot = orphanFoot(m);

    expect(foot.text, "a zero has no oldest row to date").toBe(core["dashboard.orphanedFoot"]);
    /*
     * `definition`, not `live`. `Foot` draws a heartbeat for `live` — the console's
     * signal that updates are arriving — and there is nothing arriving for a set
     * with no rows in it. The freshness object still says `live`, correctly; the
     * act is a rendering choice and this is where they part.
     */
    expect(foot.act).toBe("definition");
    const html = renderToStaticMarkup(<Foot act={foot.act}>{foot.text}</Foot>);
    expect(html, "a heartbeat over an empty set claims updates nothing is sending").not.toContain(
      "heartbeat",
    );
    /* **The positive control, because the assertion above is an absence.** A
       `not.toContain` on a marker that `Foot` never renders under any act would be
       green whatever this function returns — an assertion aimed where the subject
       does not appear, which `docs/rules/testing.md` records as its own species
       of check that is not one. So the marker is shown to be reachable here rather
       than assumed, on every run. */
    expect(
      renderToStaticMarkup(<Foot act="live">{foot.text}</Foot>),
      "`Foot` no longer marks the live act in the DOM, so the assertion above has " +
        "nothing to find and cannot fail",
    ).toContain("heartbeat");
  });

  it("is the provenance act when there is an age, so the line is marked as one", () => {
    /* The four acts exist because a 25-minute-old figure and a live one looked
       equally trustworthy in one 11px style. A date rendered
       without the act is that defect returning. */
    expect(orphanFoot(orphans(1, 10 * DAY)).act).toBe("provenance");
  });
});
