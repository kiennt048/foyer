import { describe, expect, it } from "vitest";
import type { Alarm } from "@foyer/contract";
import { groupAlarms } from "./alarms.js";

/**
 * Grouping is a *view* decision, and the things it must not break are all
 * downstream of it: the severity of a BlastRADIUS refusal, the link to the one
 * access point at fault, and the order an operator was already reading.
 *
 * Every test here goes red when its guarantee is broken on purpose. The interesting
 * one is `same kind, different level` — a grouping can look right and still fold an
 * urgent alarm into a minor one, which is the failure this function exists to
 * prevent and the one a naive `kind`-only key makes.
 */

const alarm = (over: Partial<Alarm> & Pick<Alarm, "id" | "kind" | "level">): Alarm => ({
  ...over,
  subject: over.subject ?? { kind: "nas", id: `ap-${over.id}`, name: `AP ${over.id}` },
});

describe("groupAlarms", () => {
  it("leaves a single alarm in one group, unchanged", () => {
    const one = alarm({ id: "1", kind: "nas-no-walled-garden", level: "caution-minor" });
    const groups = groupAlarms([one]);
    expect(groups).toHaveLength(1);
    expect(groups[0]!.alarms).toEqual([one]);
  });

  it("folds the same kind at the same level into one group", () => {
    // The measured case: a venue with three hand-registered access points, none
    // of which supports a walled garden. One fault, three cards.
    const groups = groupAlarms([
      alarm({ id: "1", kind: "nas-no-walled-garden", level: "caution-minor" }),
      alarm({ id: "2", kind: "nas-no-walled-garden", level: "caution-minor" }),
      alarm({ id: "3", kind: "nas-no-walled-garden", level: "caution-minor" }),
    ]);
    expect(groups).toHaveLength(1);
    expect(groups[0]!.alarms).toHaveLength(3);
  });

  it("does NOT fold the same kind at different levels", () => {
    // BlastRADIUS, CVE-2024-3596. The server raises it urgently and per-NAS on
    // purpose, and a `kind`-only key would drop an urgent alarm into a minor
    // group and lower the severity of the one thing that must not be lowered.
    const groups = groupAlarms([
      alarm({ id: "1", kind: "nas-message-authenticator-refused", level: "urgent" }),
      alarm({ id: "2", kind: "nas-message-authenticator-refused", level: "urgent" }),
      alarm({ id: "3", kind: "nas-message-authenticator-refused", level: "caution-minor" }),
    ]);
    expect(groups).toHaveLength(2);
    expect(groups.map((g) => g.alarms.length)).toEqual([2, 1]);
    expect(groups[0]!.level).toBe("urgent");
  });

  it("keeps the order the server sent, so grouping never reorders the list", () => {
    const groups = groupAlarms([
      alarm({ id: "1", kind: "nas-unreachable", level: "urgent" }),
      alarm({ id: "2", kind: "nas-no-walled-garden", level: "caution-minor" }),
      alarm({ id: "3", kind: "nas-unreachable", level: "urgent" }),
      alarm({ id: "4", kind: "nas-no-accounting", level: "warning" }),
    ]);
    expect(groups.map((g) => g.kind)).toEqual([
      "nas-unreachable",
      "nas-no-walled-garden",
      "nas-no-accounting",
    ]);
  });

  it("keeps every alarm's subject, because the link to the device is the point", () => {
    // Grouping is a view. If this loses a subject, the card names a count and
    // an operator cannot get from "3 access points" to the one that is at fault.
    const groups = groupAlarms([
      alarm({ id: "a", kind: "nas-no-walled-garden", level: "caution-minor" }),
      alarm({ id: "b", kind: "nas-no-walled-garden", level: "caution-minor" }),
    ]);
    expect(groups[0]!.alarms.map((a) => a.subject?.id)).toEqual(["ap-a", "ap-b"]);
    expect(groups[0]!.alarms.map((a) => a.subject?.name)).toEqual(["AP a", "AP b"]);
  });

  it("returns nothing for nothing, so the empty state is the server's to claim", () => {
    // `dashboard.alarmsFailed` exists because an empty list and an unanswered
    // request are opposite facts. Grouping must not turn one into the other.
    expect(groupAlarms([])).toEqual([]);
  });
});
