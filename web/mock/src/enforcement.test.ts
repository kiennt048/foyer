import { describe, expect, it } from "vitest";
import type { Nas, VoucherBatch } from "@foyer/contract";
import { app } from "./server.js";
import { createNas, probeNas, world } from "./state.js";

/**
 * **A reader of `accountingConfigured`, and the one a guest reads.**
 *
 * Deciding a batch's `enforcedBy` with `devices.some((n) => n.accountingConfigured)`
 * would be a truthiness test on a three-valued field:
 * `store.RadiusClient.AccountingConfigured` is a `*bool` whose comment says *"Nil
 * is not measured"*, the property is optional on `Nas`, and `createNas` leaves it
 * **absent** on a device nothing has probed. `undefined` is falsy, so *nobody has
 * looked* would collapse into *measured silent* — and the two are not the same
 * sentence at all.
 *
 * The cost is that `unenforced` is a **claim about the network**, not a hedge. The
 * contract: *"the plan states a limit nothing is enforcing. The UI must not present
 * this as a cap."* Emitting that about radios nobody has spoken to manufactures a
 * measurement out of the absence of one — the same mistake as writing `false` at
 * registration, one layer up, and this time it reaches the guest.
 *
 * ── WHAT *UNMEASURED* MEANS HERE, AND WHO DECIDED IT ────────────────────────
 *
 * `foyer-sampled`, and Go decided it first. `internal/portal/service.go`'s
 * `enforcement()` returns `EnforcedByNothing` for `AccountingAbsent` **only** and
 * `EnforcedBySampling` for everything else, and `internal/portal/placement_test.go`
 * carries the third case by name — *"nobody has measured it" → EnforcedBySampling* —
 * with the reason in place: *"reporting `unenforced` for a device nobody has probed
 * would tell an operator that a working quota is not being applied."* The guess is
 * closed at NAS registration, which is where `docs/rules/radius.md` asks for it,
 * rather than papered over in a batch.
 *
 * The two values are not symmetric and that is the whole argument. `foyer-sampled`
 * is a hedge that travels with its own caveat — sampled, with overshoot, interval
 * stated. `unenforced` is an assertion. An unmeasured field may become the hedge; it
 * may not become the assertion.
 *
 * So this mock agrees with Go on all three inputs, which matters because the mock
 * is the oracle the console is checked against.
 *
 * ── HOW THIS FAILS ──────────────────────────────────────────────────────────
 *
 * Against `devices.some((n) => n.accountingConfigured)`:
 *
 *   × a site nobody has probed is not told its quota is enforced by nothing
 *       → a batch in a site of freshly registered devices reports `unenforced`:
 *         expected 'unenforced' to be 'foyer-sampled'
 *
 * **And only that one moves**, which is the point of the two beneath it. `!false`
 * and `false === false` are both `true`, so a world whose devices have all been
 * probed cannot tell the two expressions apart: *measured silent* stays
 * `unenforced` and *measured accounting* stays `foyer-sampled` either way, green
 * against both. A test built on the fixture devices — every one of which carries
 * an explicit `true` or `false` — would pass against the defect. The
 * unmeasured device has to be **registered and left unprobed** for the assertion to
 * reach anything, which is why `createNas` is called here rather than a `Nas`
 * literal being typed out: the third state comes from the code that produces it.
 */

/** A site of its own per case, so no two drives can see each other's devices. */
let siteSeq = 0;
const freshSite = () => `site-enforcement-${++siteSeq}`;

/**
 * A device in the state registration leaves it in: capabilities all false,
 * `reachability: "unknown"`, and `accountingConfigured` **absent**.
 *
 * Through `createNas` rather than a literal. The property under test is one this
 * function is responsible for *not* setting, and a hand-written record would assert
 * the absence against a hand-written copy of the rule instead of against the rule — the
 * stand-in derived from what it stands in for.
 */
const register = (siteId: string, driver: Nas["driver"]): Nas =>
  createNas({ siteId, name: `AP ${driver}`, driver, ipAddress: "10.90.0.1" });

/**
 * The batch this all decides, created through the real route.
 *
 * A data allowance and **no rate**: a rate that no device applies short-circuits to
 * `unenforced` two lines earlier and never reaches the accounting question. And
 * every device here lacks `hardQuota`, so the batch does not short-circuit to `nas`
 * either. Those are the two guards that would otherwise make this test green over
 * nothing, so they are asserted rather than assumed.
 */
async function enforcedBy(siteId: string): Promise<string> {
  const devices = world.nas.filter((n) => n.siteId === siteId && n.enabled);
  expect(devices.length, "an empty site short-circuits to `unenforced`").toBeGreaterThan(0);
  expect(
    devices.every((n) => !n.capabilities.hardQuota),
    "a device enforcing the allowance itself short-circuits to `nas`, so accounting is never asked",
  ).toBe(true);

  const res = await app.request("/api/v0/voucher-batches", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      siteId,
      label: `Enforcement ${siteId}`,
      count: 1,
      plan: { name: "Day pass", dataAllowanceMb: 500 },
      expiryMode: "from-creation",
      validForHours: 24,
    }),
  });
  expect(res.status, "the batch was not created, so there is no enforcement to read").toBe(201);
  return ((await res.json()) as VoucherBatch).plan.enforcedBy!;
}

describe("what enforces a batch is read from three accounting states, not two", () => {
  it("a site nobody has probed is not told its quota is enforced by nothing", async () => {
    const siteId = freshSite();
    const devices = [register(siteId, "mikrotik"), register(siteId, "omada")];

    /* The world actually carries the third state. Without this the case below is the
       vacuous one the header describes: if registration had gone back to writing
       `false`, the assertion would still be red but for the wrong reason, and nobody
       reading the failure would know which. */
    for (const d of devices) {
      expect(
        d.accountingConfigured,
        "registration measured something, so this drive is not about the unmeasured case",
      ).toBeUndefined();
    }

    expect(await enforcedBy(siteId)).toBe("foyer-sampled");
  });

  it("a device measured silent still means nothing is applying the cap", async () => {
    /* Green against the truthiness test and the three-valued one alike, deliberately.
       `false` is a real measurement — the controller-API path with no accounting
       profile attached, from which no byte counter will ever arrive — and
       `docs/rules/radius.md` is explicit that a limit nothing samples must not be
       presented as a cap. This is the half that stops the rule from degrading into
       "always say sampled". */
    const siteId = freshSite();
    const omada = register(siteId, "omada");
    probeNas(omada);
    expect(omada.accountingConfigured, "the probe did not measure a silence").toBe(false);

    expect(await enforcedBy(siteId)).toBe("unenforced");
  });

  it("a device measured accounting is sampled, with the overshoot the UI states", async () => {
    const siteId = freshSite();
    const mt = register(siteId, "mikrotik");
    probeNas(mt);
    expect(mt.accountingConfigured, "the probe did not measure accounting").toBe(true);

    expect(await enforcedBy(siteId)).toBe("foyer-sampled");
  });

  /**
   * The three states are three inputs, and two of them must not answer alike.
   *
   * Cheap, and it is what stops a later "simplification" from mapping *unmeasured*
   * back onto either measured value while every assertion above stays green — the
   * same guard `accounting-pill.test.tsx` closes on the rendering side.
   */
  it("unmeasured is answered as its own case, not as one of the other two", async () => {
    const unmeasured = freshSite();
    register(unmeasured, "omada");

    const silent = freshSite();
    probeNas(register(silent, "omada"));

    /* Same driver on both sides, so the only thing that differs is whether anything
       has looked — and the answers differ. */
    expect(await enforcedBy(unmeasured)).not.toBe(await enforcedBy(silent));
  });
});
