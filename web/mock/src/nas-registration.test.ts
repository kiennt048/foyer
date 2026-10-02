/**
 * What a registration is entitled to say about a device nobody has spoken to.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Two claims, one shape. `createNas` builds one object literal, and inside it
 * `reachability` and `messageAuthenticator` are set to `unknown` — *nothing has
 * established this*. Setting `accountingConfigured` to `false` in the same literal
 * would be a **measurement** in this contract: the schema's own words are *"every
 * session figure for this NAS will be `absent`"*. And registering the device with
 * `sharedSecretId: null` while the route hands the operator a freshly generated
 * secret in the same response body would leave **a secret nothing names, which is a
 * secret nothing can rotate** — `rotateNasSecret` acts on the RADIUS client's secret
 * rather than on the device.
 *
 * `internal/apistore` omits the property and names the secret, and
 * `tools/codegen/differ` compares the two servers on both. This file is the same two
 * claims where a change can turn them red without two servers and a database.
 *
 * ── HOW THESE FAIL ──────────────────────────────────────────────────────────
 *
 * Against a `createNas` that writes `accountingConfigured: false` and
 * `sharedSecretId: null`:
 *
 *   × registers a device with nothing measured about its accounting
 *       → expected false to be undefined
 *   × the absence is the third state and a probe is what removes it
 *       → expected false to be undefined
 *   × names the secret it just minted, because a secret nothing names cannot be
 *     rotated
 *       → expected null to be a string
 *
 * The third case of the first test — a probe writing a boolean — is green against
 * both, which is the half that says the absence is a *state* rather than a field the
 * mock forgot: a test that only asserted absence would be satisfied by deleting the
 * property from the model.
 */
import { describe, expect, it } from "vitest";
import type { Nas, NasCreate, NasProbe } from "@foyer/contract";
import { buildWorld, deriveAlarms } from "@foyer/fixtures";
import { app } from "./server.js";
import { createNas, probeNas, world } from "./state.js";
import { conformResponse } from "./validate.js";

/** The one shape this endpoint accepts by hand: a driver with no controller. */
const register = (body: Partial<NasCreate> = {}) =>
  app.request("/api/v0/nas", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      siteId: "site-hcm",
      name: "Registered by hand",
      driver: "mikrotik",
      /* `10.x`, so the probe below answers `ok`. The two RFC 5737 ranges are the
         mock's declared way of asking for a failure and neither is being asked for
         here — see `UNREACHABLE_PREFIX` in `state.ts`. */
      ipAddress: "10.90.0.31",
      ...body,
    }),
  });

async function created(body: Partial<NasCreate> = {}): Promise<{ nas: Nas; sharedSecret: string }> {
  const res = await register(body);
  expect(res.status, "registration refused").toBe(201);
  const answer = (await res.json()) as { nas: Nas; sharedSecret: string };
  /* The mock validates on the way out, so a 201 already passed the schema. Asserted
     from the outside as well so a failure names the operation rather than a 500. */
  expect(() => conformResponse("createNas", 201, answer)).not.toThrow();
  return answer;
}

describe("registering a device states what registration knows and no more", () => {
  it("registers a device with nothing measured about its accounting", async () => {
    const { nas } = await created();

    /*
     * The two neighbours first, and they are the argument rather than decoration.
     * They are `unknown` for exactly the reason the third field must be absent, and
     * a run where they had drifted to something else would make the third assertion
     * a statement about nothing.
     */
    expect(nas.reachability, "the neighbour that carries the same claim").toBe("unknown");
    expect(nas.messageAuthenticator, "the other neighbour").toBe("unknown");

    /*
     * `undefined`, not `false`. `Nas.accountingConfigured` is optional precisely so
     * the property can be missing, and `store.RadiusClient.AccountingConfigured` is
     * a `*bool` whose comment says *"Nil is not measured"*.
     *
     * Both spellings are asserted, because they fail differently: `toBeUndefined`
     * catches the value, and the key check catches an explicit `undefined` written
     * into the literal, which serialises away over HTTP but would leave the object
     * claiming a property it has not measured everywhere the mock is imported
     * directly.
     */
    expect(nas.accountingConfigured, "a measurement about an unprobed radio").toBeUndefined();
    expect(Object.keys(nas)).not.toContain("accountingConfigured");
  });

  it("the absence is the third state and a probe is what removes it", async () => {
    /*
     * Non-vacuity. Absence would be just as satisfied by a mock that never writes
     * this field at all, and that model would be worse than the defect: the console
     * would never be able to say *not attached* about a device that really is
     * silent. So the same device is probed, and the property has to arrive.
     */
    const { nas } = await created({ name: "Probed once", ipAddress: "10.90.0.32" });
    expect(nas.accountingConfigured).toBeUndefined();

    const probed = await app.request(`/api/v0/nas/${nas.id}/probe`, { method: "POST" });
    expect(probed.status).toBe(200);
    const probe = (await probed.json()) as NasProbe;
    expect(probe.accounting, "a reachable probe measured nothing").not.toBe("not-measured");

    const after = await app.request(`/api/v0/nas/${nas.id}`);
    const stored = (await after.json()) as Nas;
    expect(typeof stored.accountingConfigured, "the probe wrote no measurement").toBe("boolean");
    expect(stored.accountingConfigured).toBe(probe.accounting === "configured");
  });

  it("names the secret it just minted, because a secret nothing names cannot be rotated", async () => {
    const { nas, sharedSecret } = await created({ name: "Names its secret", ipAddress: "10.90.0.33" });

    /* The secret is in the answer — that half was never in doubt, and it is what
       makes the other half a contradiction rather than an omission. */
    expect(sharedSecret.length, "no secret was handed over at all").toBeGreaterThan(0);

    expect(typeof nas.sharedSecretId, "a secret handed over that no row claims").toBe("string");
    expect(nas.sharedSecretId).not.toBe("");

    /* One device, one secret: a hand-registered device belongs to no controller-level
       RADIUS profile, so it may not be handed another device's id. */
    const others = await app.request("/api/v0/nas?siteId=site-hcm&limit=200");
    const { items } = (await others.json()) as { items: Nas[] };
    const sharing = items.filter((n) => n.id !== nas.id && n.sharedSecretId === nas.sharedSecretId);
    expect(sharing.map((n) => n.id), "the minted id already belonged to another device").toEqual([]);
  });
});

/**
 * The alarm that reads the same field, one package over.
 *
 * `deriveAlarms` raises `nas-no-accounting` on `n.accountingConfigured === false &&
 * n.secretSet`, like every other predicate in that same loop, which compares against
 * a value: `=== "unreachable"`, `=== "credentials-expired"`, `=== "refused"`,
 * `=== "none"`. `messageAuthenticator === "refused"` four lines above it is the
 * identical field shape — `unknown` raises nothing.
 *
 * **A two-valued reader stays hidden until the value widens.** While `createNas`
 * wrote `accountingConfigured: false` on every device it registered, absence never
 * occurred and `!accountingConfigured` would be wrong in a way nothing could reach.
 * With the field honest, `!undefined` is `true`, and the dashboard would tell an
 * operator **"No accounting profile attached — no session records will arrive from
 * this device"** about a radio nothing has spoken to, in the future tense. Widening
 * a value exposes every two-valued reader of it.
 *
 * ── HOW THIS FAILS ──────────────────────────────────────────────────────────
 *
 * With the predicate written as `if (!n.accountingConfigured && n.secretSet)`:
 *
 *   × a device nothing has probed raises no claim about its accounting
 *       → the dashboard says "no session records will arrive" about a device
 *         nothing has measured: expected [ "alarm-nas-acct-nas-…" ] to deeply
 *         equal []
 *
 * The measured-absent half stays green either way, which is what says the rule is a
 * narrowing rather than a deletion: `!false` and `false === false` are both true, so
 * only the absent case moves.
 */
describe("an alarm is a statement about a measurement", () => {
  it("still raises for a device measured to have no accounting profile", () => {
    /*
     * The half that keeps the repair from being "raise less". This is the alarm's
     * real subject — a controller-API device where `radacct` rows will genuinely
     * never appear — and it must survive, at the level it had.
     *
     * Driven through registration and a probe rather than read off a fixture row.
     * `nas-omada-1` cannot answer it: its controller credentials have been expired
     * for 34 days, so nothing has ever measured it, and `accountingConfigured: false`
     * on that row would be a lie. A measured silence has to be *produced* to be asserted,
     * which is the only way this world can be trusted to say "not measured" and
     * mean it.
     */
    const nas = createNas({ siteId: "site-hcm", name: "Measured silent", driver: "omada", ipAddress: "10.90.0.35" });
    probeNas(nas);
    expect(nas.accountingConfigured, "the probe measured nothing").toBe(false);

    const raised = deriveAlarms(world, nas.siteId).filter(
      (a) => a.kind === "nas-no-accounting" && a.subject?.id === nas.id,
    );
    expect(raised.length, "the measured case stopped alarming").toBe(1);
    expect(raised[0]!.level).toBe("caution-minor");
  });

  it("a device nothing has probed raises no claim about its accounting", async () => {
    /*
     * Driven through registration rather than constructed, because registration is
     * how absence arises: `createNas` leaves the property off and `probeNas` is what
     * writes it. A hand-built device would be an input that cannot be wrong — it
     * would assert the alarm's behaviour against a shape nothing in this product
     * produces.
     */
    const { nas } = await created({ name: "Never probed", ipAddress: "10.90.0.34" });
    expect(nas.accountingConfigured, "registration measured something after all").toBeUndefined();
    expect(nas.secretSet, "the predicate's other half is false, so this asks nothing").toBe(true);

    const raised = deriveAlarms(world, nas.siteId)
      .filter((a) => a.kind === "nas-no-accounting" && a.subject?.id === nas.id)
      .map((a) => a.id);
    expect(
      raised,
      'the dashboard says "no session records will arrive" about a device nothing has measured',
    ).toEqual([]);
  });

  it("the shipped world says 'not measured', and stops saying it once something measures", () => {
    /*
     * **A fixture world that cannot express the third value makes every check of it
     * vacuous.** Were all nine devices to set `accountingConfigured` explicitly, no
     * screen test, no E2E route and no differential scenario would ever run against
     * a device whose accounting nobody had measured. The two cases above reach the
     * third state only by *registering* one.
     *
     * `nas-omada-1` is that device: expired controller credentials, everything else
     * populated, and `accountingConfigured` **absent**. It is
     * the one row where absence is the truthful value rather than a hole, and the
     * one that already told the story of a controller nobody can reach.
     *
     * The halves matter separately. The first is what the fix is for: the dashboard
     * must not say *no session records will ever arrive from this device* about a
     * radio nothing has spoken to. The second is the non-vacuity — silence is only
     * correct if a measurement still produces the alarm **on this very device**, so
     * the world cannot buy the fix by deleting the signal. The credentials are fixed
     * (what the operator does with the expired key) and the device is probed.
     */
    const w = buildWorld();
    const device = w.nas.find((n) => n.id === "nas-omada-1")!;
    const accountingAlarms = (siteId: string) =>
      deriveAlarms(w, siteId).filter((a) => a.kind === "nas-no-accounting" && a.subject?.id === device.id);

    /* Non-vacuity, the other way: the world has to *contain* the third state, or the
     * assertion below is the vacuous one — it would pass against a world where
     * absence cannot occur, which is precisely the defect. */
    expect(
      device.accountingConfigured,
      "the shipped fixtures can no longer express 'not measured', so nothing in them " +
        "tests the third value",
    ).toBeUndefined();
    expect(Object.keys(device), "an explicit undefined is not an absence").not.toContain(
      "accountingConfigured",
    );
    /* And it is absent for its own reason rather than by being empty, which would be
     * a different defect wearing the same shape. */
    expect(device.secretSet, "the predicate's other half is false, so this asks nothing").toBe(true);
    expect(device.reachability, "the reason nothing could measure it").toBe("credentials-expired");
    expect(device.ipAddress, "an unmeasured device that is also empty is a different row").toBeTruthy();

    expect(
      accountingAlarms(device.siteId).map((a) => a.id),
      'the dashboard says "no session records will arrive" about a device nothing has measured',
    ).toEqual([]);

    device.reachability = "ok";
    probeNas(device);
    expect(device.accountingConfigured, "the probe measured nothing").toBe(false);
    expect(
      accountingAlarms(device.siteId).length,
      "a measured absence raises nothing, so the fix is 'raise less'",
    ).toBe(1);
  });
});
