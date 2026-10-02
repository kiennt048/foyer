import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { buildWorld, controllerServesSite } from "@foyer/fixtures";
import {
  ACTION_LABEL,
  SitesCell,
  type ControllerAttachment,
  controllerAction,
  controllerAttachment,
} from "./Controllers.js";

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  Every controller state this screen can draw is reachable from the world it is
 *  approved against
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `controller-attachment.test.tsx` beside this file asserts that the three states
 * render three different sentences. It builds its own controllers to do it, which is
 * right for a test of the discriminator and is **not** evidence that anybody will
 * ever see the sentence: a state no fixture holds is a state that is absent from
 * `/controllers`, absent from the design gallery, and absent from **any review** of
 * this screen.
 *
 * An unattached controller arises from a *sequence* — register, probe, vanish — so
 * without a fixture holding it, every static assertion about controller attachment
 * would be made against a hand-written object. That is why `docs/rules/frontend.md`
 * says the fixtures are ugly on purpose: if they are tidy, a review validates a world
 * that does not exist.
 *
 * ── HOW THIS GOES RED ───────────────────────────────────────────────────────
 *
 * Delete `ctl-unifi-unattached` from `buildControllers` and both cases below fail:
 * `unattached` maps to no controller, and `attach` is a label the product can print
 * that no row in the world prints. Both messages name the missing state rather than
 * a count, because a count would be satisfied by adding any sixth controller.
 *
 * ── WHY THE EXPECTED SETS ARE NOT TYPED IN ──────────────────────────────────
 *
 * `EVERY_STATE` is a `Record<ControllerAttachment, true>`, so a fourth state added
 * to the union stops this file compiling rather than quietly leaving itself
 * unchecked; and the action labels come from `ACTION_LABEL`, which is the object the
 * screen renders from. Both are the `AlarmKind` guard's shape — *the expected set is
 * the enum itself* — which exists because `controller-version-unsupported` was drawn
 * on this very screen for weeks with nothing raising it.
 */

/**
 * Every member of the union, enumerated by the compiler and not by a person.
 *
 * If `ControllerAttachment` gains a value this object is missing a key and
 * `pnpm -F @foyer/admin typecheck` says so, which is the same guarantee `vi.ts` gets
 * from being typed as a complete map of `en.ts`.
 */
const EVERY_STATE: Record<ControllerAttachment, true> = {
  "sites-not-read": true,
  unattached: true,
  attached: true,
};

const world = buildWorld();

/** The markup as an operator reads it: tags out, entities back. */
const reads = (node: React.ReactElement) =>
  renderToStaticMarkup(node)
    .replace(/<[^>]+>/g, " ")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&")
    .replace(/\s+/g, " ")
    .trim();

describe("the fixture world reaches every controller attachment state", () => {
  it("holds at least one controller in each", () => {
    const held = new Map<ControllerAttachment, string[]>();
    for (const c of world.controllers) {
      const state = controllerAttachment(c);
      held.set(state, [...(held.get(state) ?? []), c.id]);
    }

    for (const state of Object.keys(EVERY_STATE) as ControllerAttachment[]) {
      expect(
        held.get(state) ?? [],
        `no controller in \`@foyer/fixtures\` is \`${state}\`, so that cell has never been ` +
          `rendered on /controllers, is absent from any review, and every assertion ` +
          `about it is made against an object built inside a test`,
      ).not.toEqual([]);
    }
  });

  /**
   * And the two absences are held by different rows, which is the half a count
   * cannot see.
   *
   * *Nothing has read its sites* and *read, and adopted by nobody* are two
   * afternoons and two buttons. A world carrying only the first is answered
   * identically by the predicate the attachment state replaced —
   * `vendorSites.length === 0` is exactly *sites-not-read* — so it can express the
   * screen's third state without being able to fail the defect the state exists for.
   */
  it("separates the two absences on rows rather than in a test's own object", () => {
    const notRead = world.controllers.filter((c) => controllerAttachment(c) === "sites-not-read");
    const unattached = world.controllers.filter((c) => controllerAttachment(c) === "unattached");
    expect(notRead.map((c) => c.id)).not.toEqual([]);
    expect(unattached.map((c) => c.id)).not.toEqual([]);

    // Different sentences, from the same component, over rows of the same world.
    for (const a of notRead) {
      for (const b of unattached) {
        expect(
          reads(<SitesCell c={b} />),
          `${a.id} and ${b.id} are in different states and this column says the same thing ` +
            `about both, so an operator is sent to the same button from two different places`,
        ).not.toBe(reads(<SitesCell c={a} />));
      }
    }
  });

  /**
   * And the partly-adopted shape, so that *unattached* is not merely *not attached to
   * this site*.
   *
   * `ctl-unifi` is adopted into the café with `q7-branch` free; the unattached row is
   * adopted nowhere with two free. Both have an unbound vendor site, and a cell that
   * keyed on *some site is free* — a rule that is easy to write — would
   * print one sentence for the pair. The denominators match on purpose, so
   * the only thing that can tell them apart is the bound count.
   */
  it("does not collapse adopted-with-a-free-site into unattached", () => {
    const partly = world.controllers.filter(
      (c) => c.vendorSites.some((v) => v.boundSiteId) && c.vendorSites.some((v) => !v.boundSiteId),
    );
    expect(
      partly.map((c) => c.id),
      "no controller is adopted into one venue with a second vendor site free, so the " +
        "partly-adopted shape is gone and this comparison has nothing to make",
    ).not.toEqual([]);

    const unattached = world.controllers.filter((c) => controllerAttachment(c) === "unattached");
    // Both ends, because the comparison below is a nested loop and an empty second
    // list satisfies it without running once — with the row this file is about
    // deleted, it would pass having compared nothing.
    expect(
      unattached.map((c) => c.id),
      "no controller is unattached, so this comparison runs zero times and passes",
    ).not.toEqual([]);
    for (const a of partly) {
      for (const b of unattached) {
        expect(controllerAttachment(a)).toBe("attached");
        expect(
          reads(<SitesCell c={b} />),
          `${a.id} is adopted and ${b.id} is not, and both have a free vendor site — the cell ` +
            `is keying on the free site rather than on the binding`,
        ).not.toBe(reads(<SitesCell c={a} />));
      }
    }
  });

  /**
   * The label on the row's first action, which is the only place the state appears
   * on a phone — the Sites column is `narrow: "drop"`.
   *
   * The expected set is `ACTION_LABEL` itself: every label the screen can print must
   * be printed by some controller in the world an approver is shown. *Attach to this
   * venue* was printable by nothing until `ctl-unifi-unattached` existed, which is
   * the same shape as an alarm kind with a catalogue entry and nothing raising it.
   */
  it("prints every action label the screen has, on some row of the world", () => {
    const printed = new Set(
      world.controllers
        .filter((c) => controllerServesSite(c, "site-hcm"))
        .map((c) => ACTION_LABEL[controllerAction(c)]),
    );
    for (const label of Object.values(ACTION_LABEL)) {
      expect(
        [...printed],
        `the café's controllers screen never prints "${label}", so that copy has been on no ` +
          `screen and the step it names is one no reviewer has been offered`,
      ).toContain(label);
    }
  });
});
