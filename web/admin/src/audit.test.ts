import { describe, expect, it } from "vitest";
import type { AuditEntry, AuditKind } from "@foyer/contract";
import { buildWorld } from "@foyer/fixtures";
import { auditCopy } from "./audit.js";
import { enabledAdministrators, wouldOrphanTheDeployment } from "./screens/Operators.jsx";

const world = buildWorld();

/**
 * The two rules this pair of screens exists to get right, and the fixture rows that
 * were built to break them.
 */

describe("the last enabled administrator", () => {
  /**
   * **The trap, stated as a measurement.**
   *
   * The fixture world has *two* administrators and *one* of them is enabled. A
   * control that counted administrators would be green here and would offer a delete
   * the server refuses — and a deployment whose only working administrator was
   * deleted has no password-reset channel and no way back short of the Go binary's
   * first-boot path.
   */
  it("counts enabled administrators, and the fixtures make the two numbers differ", () => {
    const administrators = world.operators.filter((o) => o.role === "administrator");
    expect(
      administrators.length,
      "The fixture world no longer has two administrators, so this test can no longer " +
        "tell a count of rows apart from a count of enabled rows. Restore the disabled " +
        "administrator before changing this number.",
    ).toBe(2);
    expect(enabledAdministrators(world.operators)).toHaveLength(1);
  });

  it("names the enabled one, not the disabled one", () => {
    const viet = world.operators.find((o) => o.id === "op-viet")!;
    const ha = world.operators.find((o) => o.id === "op-ha")!;
    expect(viet.enabled).toBe(true);
    expect(ha.enabled).toBe(false);

    /* Disabling, demoting or deleting the enabled one leaves nobody. */
    expect(wouldOrphanTheDeployment(world.operators, viet)).toBe(true);
    /* Doing any of the three to the disabled one changes nothing about who can
       administer the deployment, and the server allows it. */
    expect(wouldOrphanTheDeployment(world.operators, ha)).toBe(false);
  });

  it("is not the rule where there is more than one", () => {
    const two = world.operators.map((o) => (o.id === "op-ha" ? { ...o, enabled: true } : o));
    for (const o of enabledAdministrators(two)) {
      expect(wouldOrphanTheDeployment(two, o)).toBe(false);
    }
  });
});

describe("the audit log's copy", () => {
  /**
   * Every kind the world produces renders from the catalogue, not from the server.
   *
   * `Record<AuditKind, …>` already makes a *missing* kind a compile error. What it
   * cannot say is that the fallback path is unused: `auditCopy` falls back to
   * `entry.title` for a kind it does not know, and a catalogue that had quietly
   * stopped matching the enum's values — a renamed kind, say — would render the
   * server's prose everywhere and look fine.
   */
  it("renders every fixture entry from the catalogue", () => {
    expect(world.audit.length).toBeGreaterThan(10);
    for (const entry of world.audit) {
      const copy = auditCopy(entry);
      expect(copy.fromServer, `${entry.kind} fell through to the server's own text`).toBe(false);
      expect(copy.what.length).toBeGreaterThan(0);
    }
  });

  /** Every kind in the contract appears in the world, so none of the copy is unread. */
  it("has a fixture row for every kind", () => {
    const seen = new Set(world.audit.map((e) => e.kind));
    const missing = world.audit.length ? [] : ["(no entries at all)"];
    expect(missing).toEqual([]);
    /* Not a hardcoded number: the set is compared against itself at both ends so a
       kind added to the contract shows up as an unrendered row rather than as a
       count somebody adjusts. */
    for (const kind of seen) {
      expect(auditCopy({ ...world.audit[0]!, kind } as AuditEntry).fromServer).toBe(false);
    }
    expect(seen.size).toBeGreaterThanOrEqual(15);
  });

  /**
   * **The actor outlives the account, and the fixtures are what prove it.**
   *
   * One entry's `actorId` is in `operators` nowhere at all. An implementation that
   * joined at read time would render "deleted operator" on exactly this row — the one
   * somebody is reading the log to understand — so the name has to come from the
   * entry, and the fixture exists so that is not a claim.
   */
  it("has an entry whose actor is no longer an operator", () => {
    const ids = new Set(world.operators.map((o) => o.id));
    const orphans = world.audit.filter((e) => !ids.has(e.actorId));
    expect(
      orphans.length,
      "No fixture entry outlives its operator any more, so nothing here shows that " +
        "`actorUsername` is load-bearing rather than convenient.",
    ).toBeGreaterThan(0);
    for (const entry of orphans) expect(entry.actorUsername).not.toBe("");
  });

  /** And the screen has no way to do the join, which is the half source can carry. */
  it("never looks the actor up", async () => {
    const source = (await import("./screens/Audit.tsx?raw")).default as string;
    expect(
      source.includes("listOperators"),
      "The audit screen reads the operators list. `actorUsername` is denormalised " +
        "precisely so it does not have to, and a join renders a blank actor on the row " +
        "that matters most.",
    ).toBe(false);
  });

  /**
   * The unknown kind still says something. Same rung as `alarm.unknown` and
   * `problem.unknown`: never blank, never a raw enum value.
   */
  it("says something for a kind it predates", () => {
    const entry = { ...world.audit[0]!, kind: "something-newer" as AuditKind };
    const copy = auditCopy(entry);
    expect(copy.fromServer).toBe(true);
    expect(copy.what.length).toBeGreaterThan(0);
    expect(copy.what).not.toBe("something-newer");
  });
});
