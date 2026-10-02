import { describe, expect, it } from "vitest";
import type { Nas } from "@foyer/contract";
import { rotationOffer, rotationRefused } from "./rotation.js";

/**
 * The three-way question, proved against the server's own rule rather than against a
 * simpler one.
 *
 * The defect this locks shows on a real UniFi controller with a discovered `U6 Lite`,
 * a driver that cannot push a secret and a device that has never answered a probe:
 * a page saying *"the device stops authenticating until someone enters the new secret
 * on it"* — and then a press refused with a `409`. A warning that predicts a path the server will not
 * take is worse than no warning: it teaches the reader that the screen's words are
 * decoration.
 *
 * So the property is the **whole table**, not the row that was broken. A test
 * asserting only "cannot push and unknown is refused" is satisfied by a
 * `rotateSecret`-only derivation that never asks about reachability — which is
 * exactly the derivation that shipped. The rows below are what makes a regression
 * visible: change the rule and the two directions of the boundary both move.
 */
const device = (canPush: boolean, reachability: Nas["reachability"]) =>
  ({
    capabilities: { rotateSecret: canPush },
    reachability,
  }) as Pick<Nas, "capabilities" | "reachability">;

describe("rotationOffer", () => {
  it("a driver that can push writes the value itself, whatever the device is doing", () => {
    // **Reachability is not consulted when the driver can push**, and that is the
    // half of the boundary a two-state derivation gets wrong in the other
    // direction: a rule that refused on `credentials-expired` would take a working
    // rotation away from a device whose controller key had merely expired, even
    // though the value is being written to the device and nobody has to carry it.
    for (const reachability of [
      "ok",
      "unreachable",
      "credentials-expired",
      "unknown",
    ] as const) {
      expect(
        rotationOffer(device(true, reachability)),
        `rotateSecret + ${reachability}`,
      ).toEqual({
        kind: "push",
      });
      expect(
        rotationRefused(device(true, reachability)),
        `rotateSecret + ${reachability}`,
      ).toBe(false);
    }
  });

  it("a driver that cannot push is manual while the device still answers", () => {
    expect(rotationOffer(device(false, "ok"))).toEqual({ kind: "manual" });
    expect(rotationRefused(device(false, "ok"))).toBe(false);
  });

  it("refuses only where there is no route back, and names every way there is not one", () => {
    /*
     * All three non-`ok` states, not just `unknown`. `unreachable` and
     * `credentials-expired` are the same dead end seen from different angles — the
     * value cannot be carried to the device by a person today — and a rule written
     * against `unknown` alone would let the other two through to a 409 the operator
     * has to discover.
     */
    for (const reachability of [
      "unreachable",
      "credentials-expired",
      "unknown",
    ] as const) {
      expect(
        rotationOffer(device(false, reachability)),
        `cannot push + ${reachability}`,
      ).toEqual({
        kind: "refused",
      });
      expect(
        rotationRefused(device(false, reachability)),
        `cannot push + ${reachability}`,
      ).toBe(true);
    }
  });

  it("agrees with the contract, which refuses with 409 on exactly this pair", () => {
    // `rotationRefused` in `web/mock/src/server.ts` and the Go handler are both
    // `!canPush && reachability !== ok`. This row is the console's third copy of that
    // rule, and a copy with no test is how the page and the server drift — which is
    // what happened.
    for (const canPush of [true, false]) {
      for (const reachability of [
        "ok",
        "unreachable",
        "credentials-expired",
        "unknown",
      ] as const) {
        const serverRefuses = !canPush && reachability !== "ok";
        expect(
          rotationRefused(device(canPush, reachability)),
          `${canPush}/${reachability}`,
        ).toBe(serverRefuses);
      }
    }
  });
});
