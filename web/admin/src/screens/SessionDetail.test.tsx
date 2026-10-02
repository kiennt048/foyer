import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { Capabilities, Session } from "@foyer/contract";
import { SESSION_IDS, buildWorld } from "@foyer/fixtures";
import { sessions } from "../i18n/sessions.js";
import { SessionDock, rateApplication } from "./SessionDetail.jsx";

/**
 * The rate limit is the one granted figure the dock cannot check on its own.
 *
 * Every other capability-dependent thing in the console is a **control**, gated by
 * `<IfCapable>` on a screen that already holds a `Nas`. This is a **figure**, on a
 * screen that holds a `Session` — and `Session` carries no capabilities. So for as
 * long as the dock was handed a session and nothing else, it rendered
 * `plan.downKbps` as bare fact on devices that cannot shape a single packet, one
 * section below the row that correctly says nothing is enforcing the quota.
 *
 * Two things are asserted here, and both are needed.
 *
 * **The join** — `rateApplication` is pure, and it is where the four-way answer is
 * decided, including the one that is easy to collapse: an unresolved NAS is *not*
 * an incapable one, and must never be rendered as though nothing were applying the
 * limit. That is a statement of fact and cannot be made from a list that has merely
 * not arrived.
 *
 * **The rendering** — because a correct join nobody calls is worth nothing, and
 * because *how* it is rendered is itself the decision under test. The figure is
 * **qualified, never hidden.** `docs/rules/frontend.md` says to hide a
 * rate-limit *field* when `bandwidthLimits` is false, and that is right for a field
 * an operator fills in: setting a limit the driver cannot apply is impossible.
 * Reading what a guest was granted is not. It is the first thing an operator needs
 * when a guest says they were promised 5 Mbps, the plan genuinely does say it, and
 * hiding it deletes the half of the contradiction that makes the diagnosis. The
 * assertions below therefore check that the number is *still there* in the
 * unapplied case, as hard as they check that the qualifier arrived.
 */

const world = buildWorld();

/** The armed fixture: a plan naming a rate, on a driver that cannot apply one. */
const unenforceable = world.sessions.find((s) => s.id === SESSION_IDS.unenforceableRate)!;
const incapable = world.nas.find((n) => n.id === unenforceable?.nasId)!;
const capable = world.nas.find((n) => n.capabilities.bandwidthLimits === true)!;

function dock(session: Session, capabilities: Capabilities | null): string {
  return renderToStaticMarkup(
    <MemoryRouter>
      <SessionDock
        session={session}
        capabilities={capabilities}
        deliberate
        hidden={false}
        error={null}
        isLoading={false}
        position={null}
        duplicateMac={false}
        onPrevious={() => {}}
        onNext={() => {}}
        onClose={() => {}}
      />
    </MemoryRouter>,
  );
}

/** The `Rate limit` row, located the way the end-to-end lock locates it. */
const RATE_FIELD = /<div class="detail-field" data-field="rate-limit" data-applied="([a-z]+)"/;

const appliedState = (markup: string): string | null => RATE_FIELD.exec(markup)?.[1] ?? null;

/**
 * Just the rate-limit row's own markup.
 *
 * Sliced to the next sibling field, because the assertions here have to be about
 * *this* row: `None set.` and `Mbps` both occur elsewhere in the dock — the data
 * allowance is unset on this very plan — and a whole-document `toContain` would
 * pass on the neighbour's copy. Fields are siblings and nest no field of their own,
 * so the next opening tag is the boundary.
 */
function rateField(markup: string): string {
  const start = markup.indexOf('<div class="detail-field" data-field="rate-limit"');
  expect(start, "the dock renders no rate-limit row at all").toBeGreaterThan(-1);
  const rest = markup.slice(start);
  const next = rest.indexOf('<div class="detail-field"', 1);
  return next === -1 ? rest : rest.slice(0, next);
}

describe("the dock states a rate limit its access point cannot apply", () => {
  it("still has the fixture pairing to test against", () => {
    /*
     * Guards every assertion below. If `sess-unenforceable-rate` loses its plan, or
     * its device gains `bandwidthLimits`, the interesting cases here quietly become
     * the boring ones and nothing fails. The same pairing is what
     * `web/e2e/tests/admin/capability-gating.spec.ts` keeps alive from the other end.
     */
    expect(unenforceable, `${SESSION_IDS.unenforceableRate} is gone from the fixtures.`).toBeTruthy();
    expect(unenforceable.plan?.downKbps ?? null).not.toBeNull();
    expect(
      incapable.capabilities.bandwidthLimits,
      `${incapable.id} can now apply a rate limit, so the session on it is no longer ` +
        "the case this file is about. Every fixture set must keep a low-capability " +
        "driver — see `docs/rules/frontend.md`.",
    ).toBe(false);
    expect(capable.capabilities.bandwidthLimits).toBe(true);
  });

  it("answers four ways, and never reads an unresolved NAS as an incapable one", () => {
    const plan = unenforceable.plan!;

    expect(rateApplication(plan, incapable.capabilities)).toBe("unapplied");
    expect(rateApplication(plan, capable.capabilities)).toBe("applied");
    expect(
      rateApplication(plan, null),
      "An unresolved access point was reported as one that cannot apply a rate. " +
        '"Nothing is applying this" is a statement of fact and must not be made from ' +
        "a NAS list that has simply not arrived.",
    ).toBe("unknown");

    /* No rate stated is nothing to qualify — including the 0 kbps that the field has
       always read as "None set." */
    expect(rateApplication(null, incapable.capabilities)).toBe("none");
    expect(rateApplication({ ...plan, downKbps: null, upKbps: null }, incapable.capabilities)).toBe(
      "none",
    );
    expect(rateApplication({ ...plan, downKbps: 0, upKbps: 0 }, capable.capabilities)).toBe("none");
  });

  it("qualifies the figure on a driver that cannot apply it, and keeps the figure", () => {
    const markup = dock(unenforceable, incapable.capabilities);
    const field = rateField(markup);

    expect(appliedState(markup)).toBe("unapplied");
    expect(field).toContain(sessions["sessions.detail.rateUnappliedWhy"]);

    /*
     * The half a "hide it" implementation would fail. The plan really does state
     * 5 Mbps, an operator opening this dock is usually answering "what was this
     * guest promised", and a console that cannot answer that has removed the
     * evidence rather than the contradiction.
     */
    const down = `${Math.round(unenforceable.plan!.downKbps! / 1000)} Mbps`;
    expect(
      field,
      `The rate no longer appears. It is qualified, not hidden: ${down} is what the ` +
        "plan says, and the operator needs both that and the fact that nothing is " +
        "applying it. Hiding it leaves the dock unable to answer what a guest was " +
        "granted — see the comment at the top of this file.",
    ).toContain(down);
    expect(field).not.toContain(sessions["sessions.detail.noLimit"]);

    /* And the row is marked as the anomaly it is, the same way the enforcement row
       one section above marks an unenforced quota. Colour is never the only signal —
       the sentence above carries it too — but the row must not read as ordinary. */
    expect(field).toContain('data-emphasis="true"');
  });

  it("says something different where the driver can apply it, so the qualifier discriminates", () => {
    /*
     * The same session, the same plan, one capability flipped. A screen that hard-coded
     * the caveat — or that emitted the attribute without reading anything — satisfies
     * every assertion in the test above and fails here.
     */
    const markup = dock(unenforceable, capable.capabilities);
    const field = rateField(markup);

    expect(appliedState(markup)).toBe("applied");
    expect(field).toContain(sessions["sessions.detail.rateAppliedWhy"]);
    expect(field, "A device that can shape traffic is being told it cannot.").not.toContain(
      sessions["sessions.detail.rateUnappliedWhy"],
    );
    expect(field).not.toContain('data-emphasis="true"');
  });

  it("claims nothing while the access point is unresolved", () => {
    const markup = dock(unenforceable, null);
    const field = rateField(markup);

    expect(appliedState(markup)).toBe("unknown");
    expect(field).toContain(sessions["sessions.detail.rateUnknownWhy"]);
    expect(field).not.toContain(sessions["sessions.detail.rateUnappliedWhy"]);
    expect(field).not.toContain(sessions["sessions.detail.rateAppliedWhy"]);
  });

  it("adds no caveat to a plan that states no rate at all", () => {
    /*
     * The controlled other half of the pair: `sess-no-accounting` is on the *same*
     * unenforcing device and differs only in whether its plan names a rate. Anything
     * this file makes appear on one and not the other is attributable to that one
     * difference and to nothing else.
     */
    const noRate = world.sessions.find((s) => s.id === SESSION_IDS.noAccounting)!;
    expect(noRate.nasId, "the controlled pair has been split across two devices").toBe(
      unenforceable.nasId,
    );
    expect(noRate.plan?.downKbps ?? null).toBeNull();

    const markup = dock(noRate, incapable.capabilities);
    const field = rateField(markup);
    expect(appliedState(markup)).toBe("none");
    expect(field).toContain(sessions["sessions.detail.noLimit"]);
    expect(field).not.toContain(sessions["sessions.detail.rateUnappliedWhy"]);
    expect(field).not.toContain("detail-note");
  });
});
