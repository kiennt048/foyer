/**
 * The handlers, driven without a socket.
 *
 * `validate.test.ts` proves the *validator* can catch drift. This file proves the
 * *handlers* actually go through it — which for an error branch is a different
 * claim, because an error branch that nothing calls is never validated at all.
 *
 * `rotateNasSecret`'s 409 is the case that made this file necessary. It is the one
 * refusal in the contract with real consequences behind it, it was written and
 * documented, and without this file **nothing would ever execute it**: the mock
 * validates responses at runtime, so a `Problem` body no request produces is a body
 * no schema has ever seen. `docs/rules/testing.md` — "a test that has never
 * failed is not necessarily proving anything"; a branch that has never *run* is
 * worse, and this one is reached exactly when an operator is already having a bad
 * day and the console is the only thing they can still read.
 *
 * `app.request()` runs the real router, the real branch and the real `conform()`
 * call, with no port bound and no simulation clock started.
 */
import { consentVersion, keepGuestInput, marketingConsents, putGuestDataSettings } from "./guestdata.js";
import { readFileSync } from "node:fs";
import { beforeAll, describe, expect, it } from "vitest";
import type {
  Account,
  AuditEntry,
  AuthAttempt,
  GardenEntry,
  PortalStatus,
  Nas,
  NasCreate,
  NasProbe,
  NetworkSettings,
  Operator,
  OperatorPermission,
  OperatorRole,
  PortalBlock,
  PortalDesign,
  PortalSettings,
  WalledGarden,
} from "@foyer/contract";
import {
  FAULT_PARAMS,
  IMAGE_MAX_BYTES,
  LOGO_MAX_BYTES,
  ROLE_PERMISSIONS,
  weakestGarden,
} from "@foyer/contract";
import { DRIVER_SHAPES } from "@foyer/contract";
import { CONTROLLER_MINIMUM_VERSION, SESSION_IDS, VENDOR_BY_DRIVER, buildWorld } from "@foyer/fixtures";
import { requirementsByOperation } from "./permissions.js";
import { claimSetup, openSetup } from "./auth.js";
import { app, rotationRefused } from "./server.js";
import {
  CREDENTIALS_EXPIRED_PREFIX,
  DEV_PASSWORD,
  acceptTerms,
  getGrant,
  getPortalClient,
  newPortalClient,
  setMustChangePassword,
  UNREACHABLE_PREFIX,
  withCurrentFreshness,
  world,
} from "./state.js";
import { siteDay } from "./sitetime.js";
import { conform, conformResponse, contractDocument } from "./validate.js";

const rotate = (nasId: string) =>
  app.request(`/api/v0/nas/${nasId}/secret`, { method: "POST" });

const register = (body: Partial<NasCreate>) =>
  app.request("/api/v0/nas", {
    method: "POST",
    headers: { "content-type": "application/json" },
    /* `coa` and not `unifi`: manual registration is refused for a driver that has a
       controller, which is the point of the rework. Callers that care about the
       driver name it; the default only has to be one this endpoint accepts.

       `ipAddress` is in the defaults because it is **required** — FreeRADIUS matches
       a packet to a client by its source address and nothing else. It is spelled out
       here rather than left off, and the two ways of getting it wrong have their own
       drives in `REFUSALS` rather than riding on every caller of this helper.

       `coaVendor` is here for the same reason and it is required for `coa` alone:
       that member names a *family*, and Cisco and Aruba identify a session
       differently, so the wrong dialect matches nothing and a careless NAS answers
       an ACK. Dropping it has its own drive in `REFUSALS`. It is ignored for every
       other driver, so a caller overriding `driver` need not think about it. */
    body: JSON.stringify({
      siteId: "site-hcm",
      name: "AP",
      driver: "coa",
      ipAddress: "10.20.0.99",
      coaVendor: "cisco",
      ...body,
    }),
  });

/**
 * A device of the given driver, created the way that driver is actually created.
 *
 * Manual registration for a driver with no controller; a controller plus discovery
 * for the rest — because `POST /nas` now refuses the controller-backed kinds, which
 * is the whole point of the rework. These tests are about what a *device* looks like
 * once it exists, so they must not be about which endpoint made it: pinning them to
 * the manual path would have meant either weakening the refusal or dropping coverage
 * of the three drivers most operators own.
 */
async function deviceOfDriver(driver: NonNullable<NasCreate["driver"]>): Promise<Nas> {
  if (DRIVER_SHAPES[driver].controller === "none") {
    const res = await register({ driver, name: `AP ${driver}` });
    expect(res.status, driver).toBe(201);
    return ((await res.json()) as { nas: Nas }).nas;
  }

  const created = await app.request("/api/v0/controllers", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({
      name: `ctl ${driver} ${Math.random().toString(36).slice(2, 7)}`,
      vendor: driver,
      baseUrl: "https://10.20.0.250",
      controllerId: driver === "omada" ? "a".repeat(32) : undefined,
      authMode: driver === "omada" ? "hotspot-operator" : "api-key",
      tlsMode: "trust-on-first-use",
      apiKey: "k",
      username: "u",
      password: "p",
    }),
  });
  expect(created.status, driver).toBe(201);
  const ctl = (await created.json()) as { id: string };

  expect((await app.request(`/api/v0/controllers/${ctl.id}/probe`, { method: "POST" })).status).toBe(200);
  const disc = await app.request(`/api/v0/controllers/${ctl.id}/discover`, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ siteId: "site-hcm" }),
  });
  expect(disc.status, driver).toBe(200);
  const { accessPointIds } = (await disc.json()) as { accessPointIds: string[] };
  expect(accessPointIds.length, driver).toBeGreaterThan(0);
  return (await (await app.request(`/api/v0/nas/${accessPointIds[0]}`)).json()) as Nas;
}

/** Registers a device and probes it, both through the real router. */
async function registerAndProbe(body: Partial<NasCreate>): Promise<{ nas: Nas; probe: NasProbe }> {
  const created = await register(body);
  expect(created.status).toBe(201);
  const { nas } = (await created.json()) as { nas: Nas };

  const probed = await app.request(`/api/v0/nas/${nas.id}/probe`, { method: "POST" });
  expect(probed.status).toBe(200);
  const probe = (await probed.json()) as NasProbe;
  /* The mock validates on the way out, so a 200 is already a schema pass. Asserted
     again from the outside so a failure names the operation rather than a 500. */
  expect(() => conformResponse("probeNas", 200, probe)).not.toThrow();
  return { nas, probe };
}

/** The stored record after the probe — a second call the wizard does not need. */
async function reread(nasId: string): Promise<Nas> {
  const res = await app.request(`/api/v0/nas/${nasId}`);
  expect(res.status).toBe(200);
  return (await res.json()) as Nas;
}

/** Named so a failure says which fixture property stopped holding. */
const where = (nas: Nas) =>
  `${nas.id} (rotateSecret=${String(nas.capabilities.rotateSecret)}, reachability=${nas.reachability})`;

describe("the harness the error branches are driven through", () => {
  it("boots no listener, because the runner sets the variable the guard reads", () => {
    /*
     * `server.ts` calls `serve()` only when `process.env.VITEST` is unset. If Vitest
     * ever stopped setting it, that guard would become permanently true and the
     * import above would quietly take :5170 from a running dev server — so the guard
     * is asserted rather than trusted.
     */
    expect(
      process.env.VITEST,
      "server.ts skips serve() on process.env.VITEST. The runner no longer sets it, " +
        "so importing the module now binds a port. Change the guard, not this test.",
    ).toBeTruthy();
  });
});

describe("rotateNasSecret refuses exactly the combination with no route back", () => {
  const refused = () => world.nas.filter(rotationRefused);
  const manualButReachable = () =>
    world.nas.filter((n) => !n.capabilities.rotateSecret && n.reachability === "ok");
  const pushableButUnreachable = () =>
    world.nas.filter((n) => n.capabilities.rotateSecret && n.reachability !== "ok");

  it("has a fixture that reaches the branch at all", () => {
    /*
     * The prerequisite for every assertion below, stated first because it is the one
     * that can silently stop being true. A refusal condition no fixture satisfies is
     * a branch that never runs, and the rest of this block would then pass by
     * skipping rather than by proving anything.
     */
    expect(
      refused().map((n) => n.id),
      "No fixture NAS has `rotateSecret: false` *and* an unreachable state, so the " +
        "409 in `rotateNasSecret` cannot be reached. Add one in `web/fixtures`.",
    ).not.toHaveLength(0);
  });

  it("answers 409 with a problem document the contract declares", async () => {
    const nas = refused()[0]!;
    const before = { secretSet: nas.secretSet, lastUsed: nas.secretLastUsedAt };

    const res = await rotate(nas.id);

    expect(res.status, where(nas)).toBe(409);
    /*
     * RFC 9457's media type, not `application/json`. The runtime validator compares
     * bodies to schemas and cannot see a content type, so this is the half of the
     * declaration nothing in the stack was able to check.
     */
    expect(res.headers.get("content-type")).toContain("application/problem+json");

    const body = (await res.json()) as unknown;
    /*
     * Validated against the *operation's* 409 rather than against the `Problem`
     * component. The handler shares `problem()` with every other refusal, so only an
     * operation-keyed check can tell that this body is the one the document declares
     * for this status.
     */
    expect(() => conformResponse("rotateNasSecret", 409, body)).not.toThrow();
    expect((body as { status: number }).status).toBe(409);

    /* A refusal that half-rotated would be worse than either outcome. */
    expect(nas.secretSet, "the refused rotation still changed the NAS").toBe(before.secretSet);
    expect(nas.secretLastUsedAt).toBe(before.lastUsed);
  });

  it("does not refuse on an unpushable secret alone — that is manual, not impossible", async () => {
    /*
     * The distinction this whole task exists to protect. A driver that cannot push is
     * a driver whose operator walks to the device and types the value in; Foyer can
     * still generate it, so the console still offers to. Only "cannot push" *and*
     * "cannot reach" together leave no route back.
     */
    const nas = manualButReachable()[0];
    expect(
      nas,
      "No reachable NAS has `rotateSecret: false`, so nothing proves the driver " +
        "capability does not refuse on its own. Add one in `web/fixtures`.",
    ).toBeDefined();

    const res = await rotate(nas!.id);
    expect(res.status, where(nas!)).toBe(200);
    const body = (await res.json()) as unknown;
    expect(() => conformResponse("rotateNasSecret", 200, body)).not.toThrow();
  });

  it("does not refuse on unreachability alone — the driver can still push it later", async () => {
    const nas = pushableButUnreachable()[0];
    expect(
      nas,
      "No unreachable NAS has `rotateSecret: true`, so nothing proves reachability " +
        "does not refuse on its own. Add one in `web/fixtures`.",
    ).toBeDefined();

    const res = await rotate(nas!.id);
    expect(res.status, where(nas!)).toBe(200);
    const body = (await res.json()) as unknown;
    expect(() => conformResponse("rotateNasSecret", 200, body)).not.toThrow();
  });

  it("answers an unknown access point with a problem document too", async () => {
    const res = await rotate("nas-does-not-exist");
    expect(res.status).toBe(404);
    expect(res.headers.get("content-type")).toContain("application/problem+json");
    const body = (await res.json()) as unknown;
    expect(() => conformResponse("rotateNasSecret", 404, body)).not.toThrow();
  });
});

describe("probeNas returns everything the probe measured", () => {
  /*
   * If `messageAuthenticator` and `accountingConfigured` lived only on `Nas`,
   * `probeNas` followed by `getNas` would be the only complete rendering of a probe
   * and any client that forgot the second call would show a device it had half
   * measured. Two of the four findings that most change what Foyer can do — a
   * device exposed to BlastRADIUS, and one from which no session data will ever
   * arrive — would be the two missing.
   *
   * Every case below goes through the real router, so `conformResponse` runs on the
   * body. That is deliberate: the runtime validator can only judge a response
   * something actually produced.
   */

  it("says what it measured, and the stored record agrees", async () => {
    const { nas, probe } = await registerAndProbe({ driver: "coa", ipAddress: "10.20.0.90" });

    expect(probe.reachability).toBe("ok");
    expect(probe.messageAuthenticator).toBe("enforced");
    expect(probe.accounting).toBe("configured");

    /* The claim that makes the wizard's second call redundant rather than wrong:
       the record after the probe says the same thing the probe said. */
    const stored = await reread(nas.id);
    expect(stored.messageAuthenticator).toBe(probe.messageAuthenticator);
    expect(stored.accountingConfigured).toBe(probe.accounting === "configured");
    expect(stored.reachability).toBe(probe.reachability);
  });

  it("reports the two findings that are otherwise learned the hard way", async () => {
    // A device that refuses `require_message_authenticator` is exposed to
    // BlastRADIUS for as long as it is deployed, and one with no accounting profile
    // produces no session records at all. Neither is visible anywhere else.
    const mikrotik = await registerAndProbe({ driver: "mikrotik", ipAddress: "10.30.0.90" });
    expect(mikrotik.probe.messageAuthenticator).toBe("refused");

    /* The accounting finding belongs to a controller-API driver, so it has to come
       through the path that driver actually uses. On that path there is no
       Access-Accept in which to request interim updates, which is why the answer is
       `missing` and not merely "none yet". */
    const omada = await deviceOfDriver("omada");
    const probed = await app.request(`/api/v0/nas/${omada.id}/probe`, { method: "POST" });
    expect(probed.status).toBe(200);
    expect(((await probed.json()) as NasProbe).accounting).toBe("missing");
  });

  it("measures nothing when it reaches nothing, and says so rather than guessing", async () => {
    const { nas, probe } = await registerAndProbe({
      driver: "coa",
      ipAddress: `${UNREACHABLE_PREFIX}17`,
    });

    expect(probe.reachability).toBe("unreachable");
    expect(probe.messageAuthenticator).toBe("not-measured");
    expect(probe.accounting).toBe("not-measured");

    /* `not-measured` is not `missing`. A device nothing could reach has not been
       shown to be silent, and the difference decides whether an operator goes
       looking for a network fault or for an accounting profile. */
    expect(probe.accounting).not.toBe("missing");

    // The record is left exactly as registration left it. An unreachable device
    // does not forget, and a probe that reached nothing may not write.
    const stored = await reread(nas.id);
    expect(stored.messageAuthenticator).toBe("unknown");
    expect(stored.lastSeenAt).toBeNull();
  });

  it("never repeats a stored measurement as a fresh one", async () => {
    /*
     * The specific lie the third state exists to prevent. This device is measured
     * once — the record now holds `enforced` — and then goes off the air. The
     * second probe must not hand back a value it did not establish, even though the
     * record still carries a perfectly good one.
     */
    const { nas } = await registerAndProbe({ driver: "coa", ipAddress: "10.20.0.92" });
    const measured = await reread(nas.id);
    expect(measured.messageAuthenticator).toBe("enforced");
    expect(measured.accountingConfigured).toBe(true);

    const device = world.nas.find((n) => n.id === nas.id)!;
    device.reachability = "unreachable";

    const res = await app.request(`/api/v0/nas/${nas.id}/probe`, { method: "POST" });
    const again = (await res.json()) as NasProbe;
    expect(again.messageAuthenticator).toBe("not-measured");
    expect(again.accounting).toBe("not-measured");

    // And the record still holds what was last known — the probe did not erase it.
    expect((await reread(nas.id)).messageAuthenticator).toBe("enforced");
  });

  it("tells an unreachable device apart from a rejected key, both from the interface", async () => {
    /*
     * The half a person sees. `createNas` registers as `unknown`, and a probe that
     * treated anything not already failing as reachable would make a device
     * registered through the wizard *always* probe green, so the flow's unreachable
     * branch could not be produced at all — only demonstrated against a pre-seeded
     * fixture, which is no use to somebody approving the flow.
     *
     * Both RFC 5737 ranges are announced in the boot banner, because a rule only a
     * test knows is a rule a viewer cannot use.
     */
    const down = await registerAndProbe({ driver: "coa", ipAddress: `${UNREACHABLE_PREFIX}5` });
    expect(down.probe.reachability).toBe("unreachable");

    const expired = await registerAndProbe({ driver: "coa", ipAddress: `${CREDENTIALS_EXPIRED_PREFIX}5` });
    expect(expired.probe.reachability).toBe("credentials-expired");

    // The realistic default stays realistic: nothing about the happy path was made
    // stranger to buy the failure path.
    const fine = await registerAndProbe({ driver: "coa", ipAddress: "10.20.0.93" });
    expect(fine.probe.reachability).toBe("ok");
  });

  it("leaves the two broken fixtures broken, however often they are probed", async () => {
    /*
     * A viewer pressing "Probe again" on the console's only unreachable device
     * must not turn it green — that would spend the fixture set's demonstration of
     * the state on a single click, permanently, for that session.
     */
    for (const id of ["nas-unifi-3", "nas-omada-1"]) {
      const before = world.nas.find((n) => n.id === id)!.reachability;
      const res = await app.request(`/api/v0/nas/${id}/probe`, { method: "POST" });
      const probe = (await res.json()) as NasProbe;
      expect(probe.reachability, id).toBe(before);
      expect(probe.messageAuthenticator, id).toBe("not-measured");
    }
  });
});

describe("a device carries the CoA port the operator typed, and never one this mock invented", () => {
  /*
   * A per-driver default CoA port in the mock — 3799 for `unifi`, `mikrotik` or `coa`,
   * `null` for `omada` or `ruckus` — would be wrong in the same way on both halves.
   * `Nas.coaPort` is the operator's override; the default is applied at *send* time
   * by `internal/nas.CoAAddr`, and `internal/apistore` stores `in.CoaPort` verbatim
   * and stores none at all for a discovered device. Such an oracle would invent a
   * value the server it is an oracle for never writes — on both creation paths — and
   * the console's null branch would be unreachable in the only world it is ever
   * driven against.
   *
   * The property asserted here is the one the two servers actually share, and it
   * is driven across **every** driver `DRIVER_SHAPES` declares rather than a list
   * typed out here, so a sixth driver arrives already covered.
   */
  const drivers = Object.keys(DRIVER_SHAPES) as (keyof typeof DRIVER_SHAPES)[];

  it("has more than one driver to drive, and covers every one the contract declares", () => {
    /* Without this the loop below is satisfied by an empty table. */
    expect(drivers.length).toBeGreaterThan(1);
  });

  it("leaves the port unset on every driver when the request names none", async () => {
    /*
     * `null` and not 3799, for `mikrotik` and `coa` as much as for `omada`. The
     * registration wizard sends no port — it has no field for one — so this is the
     * state **every hand-registered device** is in, and it is the state
     * `internal/apistore` puts them in too. A mock that filled it in is a mock that
     * agreed with the Go server about nothing on this field.
     */
    for (const driver of drivers) {
      const nas = await deviceOfDriver(driver);
      expect(
        nas.coaPort,
        `${driver}: the mock invented a CoA port the Go server never writes`,
      ).toBeNull();
    }
  });

  it("still takes what the operator typed, on the driver whose kind names a family", async () => {
    // 1700 rather than 3799 on purpose: `docs/rules/nas-drivers.md` records 1700
    // as a *Cisco* habit and the `coa` kind covers Cisco **and** Aruba, so the kind
    // does not identify the port any more than it identifies the vendor. CoA is UDP,
    // so guessing wrong is a timeout rather than a refusal — which is why the field
    // is per-NAS, and why nothing may guess it on the operator's behalf.
    const res = await register({ driver: "coa", coaPort: 1700, name: "Cisco" });
    expect(((await res.json()) as { nas: Nas }).nas.coaPort).toBe(1700);
  });
});

/**
 * The garden, and the one thing about it that is not a list of strings.
 *
 * `effectiveKind` is the **weakest** garden any enabled device in the site can
 * enforce, and every other assertion here exists because the tempting
 * implementations are all wrong in the same direction: they report the best case.
 * A site is only as enclosed as its most permissive device, and an operator told
 * "FQDN/SNI" about a site containing a driver that gardens nothing has been told
 * their pre-authentication traffic is restricted when it is not.
 */
describe("the walled garden reports the weakest device, not the best one", () => {
  const gardenOf = async (siteId = "site-hcm") => {
    const res = await app.request(`/api/v0/sites/${siteId}/walled-garden`);
    expect(res.status).toBe(200);
    return (await res.json()) as WalledGarden;
  };

  it("has a fixture site whose devices actually disagree", async () => {
    /* Without this the rest of the block is vacuous — a site where every driver
       reports the same kind cannot tell a weakest-of from a first-of. */
    const g = await gardenOf();
    const kinds = new Set(g.drivers.map((d) => d.walledGarden));
    expect(
      kinds.size,
      "site-hcm no longer contains devices with differing walled-garden support, so " +
        "this block proves nothing. Every fixture set must keep a low-capability driver.",
    ).toBeGreaterThan(1);
    expect([...kinds]).toContain("none");
  });

  it("reports none where any enabled device gardens nothing", async () => {
    const g = await gardenOf();
    expect(g.effectiveKind).toBe("none");
    expect(g.effectiveKind).toBe(weakestGarden(g.drivers.map((d) => d.walledGarden)));
  });

  it("recomputes when the weakest device is disabled, rather than serving a stored value", async () => {
    /* The whole point of deriving it. A stored `effectiveKind` is correct on the day
       it is written and wrong on the first registration afterwards. */
    const before = await gardenOf();
    const weakest = before.drivers.filter((d) => d.walledGarden === "none");
    expect(weakest.length).toBeGreaterThan(0);

    for (const d of weakest) {
      const res = await app.request(`/api/v0/nas/${d.nasId}`, {
        method: "PATCH",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ enabled: false }),
      });
      expect(res.status).toBe(200);
    }
    try {
      const after = await gardenOf();
      expect(after.effectiveKind).not.toBe("none");
      expect(after.drivers.map((d) => d.nasId)).not.toContain(weakest[0]!.nasId);
    } finally {
      for (const d of weakest) {
        await app.request(`/api/v0/nas/${d.nasId}`, {
          method: "PATCH",
          headers: { "content-type": "application/json" },
          body: JSON.stringify({ enabled: true }),
        });
      }
    }
  });

  it("ships the entries the portal cannot serve itself without", async () => {
    const g = await gardenOf();
    const required = g.entries.filter((e) => e.origin === "required");
    expect(
      required.length,
      "`docs/rules/security.md` §5 — the portal FQDN, its OCSP/CRL endpoints and " +
        "the RFC 8908 endpoint must be in the garden or the portal cannot serve itself.",
    ).toBeGreaterThanOrEqual(4);
  });

  it("refuses to delete a required entry, with a problem document", async () => {
    const g = await gardenOf();
    const req = g.entries.find((e) => e.origin === "required")!;
    const res = await app.request(`/api/v0/sites/site-hcm/walled-garden/${req.id}`, {
      method: "DELETE",
    });
    expect(res.status).toBe(409);
    expect(res.headers.get("content-type")).toContain("application/problem+json");

    /* Still there. A refusal that removed it anyway would pass a status assertion. */
    expect((await gardenOf()).entries.map((e) => e.id)).toContain(req.id);
  });

  it("adds and removes an operator entry", async () => {
    const add = await app.request("/api/v0/sites/site-hcm/walled-garden", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ kind: "fqdn", value: "test-added.example.com" }),
    });
    expect(add.status).toBe(201);
    const entry = (await add.json()) as GardenEntry;
    expect(entry.origin).toBe("operator");

    const dup = await app.request("/api/v0/sites/site-hcm/walled-garden", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ kind: "fqdn", value: "TEST-ADDED.example.com" }),
    });
    expect(dup.status, "duplicate detection is case-sensitive").toBe(409);

    const del = await app.request(`/api/v0/sites/site-hcm/walled-garden/${entry.id}`, {
      method: "DELETE",
    });
    expect(del.status).toBe(204);
    expect((await gardenOf()).entries.map((e) => e.id)).not.toContain(entry.id);
  });

  it("rejects a URL where a hostname belongs, rather than storing something unmatched", async () => {
    const res = await app.request("/api/v0/sites/site-hcm/walled-garden", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ kind: "fqdn", value: "https://example.com/pay" }),
    });
    expect(res.status).toBe(422);
  });
});

/**
 * The stream that answers "why can't this guest get online".
 *
 * Each assertion guards a shape the data genuinely has and a console would
 * reasonably assume away. The absences are the point: a row with no MAC, a row with
 * no NAS, and a reject with no reason are all *correct* data, and a handler that
 * tidied any of them would delete the answer to the question the screen exists for.
 */
describe("auth attempts carry their own absences", () => {
  const attempts = async (query = "", siteId = "site-hcm") => {
    const res = await app.request(`/api/v0/auth-attempts?siteId=${siteId}${query}`);
    expect(res.status).toBe(200);
    return (await res.json()) as {
      items: AuthAttempt[];
      macCoverage: string;
      blindSpots: { kind: string }[];
    };
  };

  it("states what it cannot see, and the list is never empty", async () => {
    const { blindSpots } = await attempts();
    /* Required non-empty by the contract's own description. An empty result set for
       a MAC means *no record*, and without these the console renders that as
       *nothing went wrong*. A wrong shared secret is discarded before any row is
       written — it is the most common real fault and it is invisible here. */
    expect(blindSpots.length).toBeGreaterThan(0);
    expect(blindSpots.map((b) => b.kind)).toContain("shared-secret-mismatch");
  });

  it("says whether this deployment records MACs at all", async () => {
    expect(["recorded", "not-recorded", "unknown"]).toContain((await attempts()).macCoverage);
  });

  it("keeps the row with no MAC, in a deployment that records MACs", async () => {
    /* Per-row absence inside `macCoverage: recorded`. A console that renders
       coverage once at the top of the page and stops is wrong for this row. */
    const { items, macCoverage } = await attempts();
    expect(macCoverage).toBe("recorded");
    expect(items.some((a) => a.callingStationId === null)).toBe(true);
  });

  it("keeps the row that matches no registered client", async () => {
    /* `nasId: null` is the symptom of the most common controller-deployment fault —
       a newly adopted access point in no `clients.conf` entry. Dropping a row that
       cannot be joined would delete exactly the evidence the operator needs. */
    expect((await attempts()).items.some((a) => a.nasId === null)).toBe(true);
  });

  it("never invents a reason for a RADIUS reject", async () => {
    /* `radpostauth` has no reason column, and the reject is decided inside an EAP or
       PAP exchange Go never sees. The enum must not grow a `radius-reject-because-*`. */
    const rejects = (await attempts()).items.filter((a) => a.source === "radius" && a.outcome === "rejected");
    expect(rejects.length).toBeGreaterThan(0);
    for (const r of rejects) expect(r.kind).toBe("radius-reject");
  });

  it("carries a failure no other screen can show", async () => {
    /* A valid voucher, a verified device, and a controller that refused. Invisible
       in sessions, in vouchers and on the access-point screen. */
    const { items } = await attempts();
    expect(items.some((a) => a.source === "driver" && a.kind === "authorize-failed")).toBe(true);
    expect(items.some((a) => a.kind === "presence-unverified")).toBe(true);
  });

  /**
   * The opposite guard, on a catalogue that is easy to leave uncounted.
   *
   * `AuthEventKind` is a closed enum and `admin/src/screens/Attempts.tsx` keys
   * `KIND_LABEL` on it as a `Record`, so a kind **missing** from the console is a
   * compile error. Nothing in the compiler says the opposite thing: a kind such as
   * `voucher-exhausted` or `account-unknown` can have reviewed-looking copy and no
   * fixture row, so its sentence is never on a screen.
   *
   * Both are the *discriminating* half of a pair — exhausted against expired,
   * unknown against bad-password — which is the worst kind to leave unrendered: the
   * copy either side of it looks fine, and the row that tells the operator which of
   * two different afternoons they are having is the one that never appears.
   *
   * The expected set is the enum rather than a list here, so a kind added tomorrow
   * arrives as a failure instead of as a value nobody thought to add.
   */
  it("carries every kind in the enum, so no attempt copy goes unreviewed", async () => {
    const declared = (contractDocument().components.schemas.AuthEventKind as { enum: string[] })
      .enum;
    /* Every site, not the default one: the stream is scoped per site and a kind that
       only ever occurs somewhere else is still a kind this world can render. */
    const sites = (await (await app.request("/api/v0/sites")).json()) as { id: string }[];
    expect(sites.length, "no sites, so this sweep asks nothing").toBeGreaterThan(0);
    const seen = new Set<string>();
    for (const s of sites) {
      for (const a of (await attempts(`&limit=200`, s.id)).items) seen.add(a.kind);
    }
    expect(
      declared.filter((k) => !seen.has(k)),
      "In `AuthEventKind`, with a console catalogue entry, and no row in this world " +
        "carries it — so the copy has never been on a screen and cannot be reviewed. " +
        "Write the fixture row, or take the kind out of the contract.",
    ).toEqual([]);
  });

  /**
   * And the same guard on the list beside it.
   *
   * `AuthBlindSpot.kind` is the other closed enum this screen renders, and
   * `BLIND_SPOT_TEXT` is an exhaustive `switch` — the same compile-time totality with
   * the same silence in the other direction. `no-radius-configured` was the value
   * nothing produced: it is what a site with no RADIUS client at all should say, and
   * `AUTH_BLIND_SPOTS` was one constant served for every site, so the one site where
   * it is true got the list belonging to a site where it is not.
   */
  it("names every blind spot in the enum across the sites it describes", async () => {
    const declared = (
      contractDocument().components.schemas.AuthBlindSpot as {
        properties: { kind: { enum: string[] } };
      }
    ).properties.kind.enum;
    const sites = (await (await app.request("/api/v0/sites")).json()) as { id: string }[];
    const seen = new Set<string>();
    for (const s of sites) {
      for (const b of (await attempts("", s.id)).blindSpots) seen.add(b.kind);
    }
    expect(
      declared.filter((k) => !seen.has(k)),
      "In `AuthBlindSpot.kind` and no site names it, so the console's sentence for it " +
        "has never been rendered — on the one screen whose whole job is saying what it " +
        "cannot see.",
    ).toEqual([]);
  });

  it("never returns a credential, under any field name", async () => {
    /* The stock FreeRADIUS post-auth query writes the submitted cleartext password
       into `radpostauth.pass` on every attempt. Foyer's generated query must not
       emit that column, and this schema has no field it could arrive in — asserted
       here rather than trusted, because the cost of being wrong is a console that
       displays guests' passwords. */
    const { items } = await attempts();
    expect(items.length).toBeGreaterThan(0);
    for (const a of items) {
      for (const key of Object.keys(a)) {
        expect(key.toLowerCase()).not.toMatch(/pass|secret|credential/);
      }
    }
  });

  it("finds a row by MAC however the operator punctuated it", async () => {
    const withMac = (await attempts()).items.find((a) => a.callingStationId);
    const mac = withMac!.callingStationId!;
    for (const form of [mac, mac.replace(/:/g, ""), mac.replace(/:/g, "-").toUpperCase()]) {
      const hit = await attempts(`&q=${encodeURIComponent(form)}`);
      expect(hit.items.length, `no match for ${form}`).toBeGreaterThan(0);
    }
  });

  it("filters by outcome without losing the envelope", async () => {
    const rejected = await attempts("&outcome=rejected");
    expect(rejected.items.length).toBeGreaterThan(0);
    for (const a of rejected.items) expect(a.outcome).toBe("rejected");
    /* The envelope has to survive a filter: an operator narrowing to rejects is
       exactly the operator who most needs to be told what is not recorded. */
    expect(rejected.blindSpots.length).toBeGreaterThan(0);
  });
});

/**
 * Every site can serve its own portal.
 *
 * Separate from the block above because it is a claim about the *fixtures* rather
 * than about the handler, and it is the kind of claim that is true on the day it is
 * written and quietly false after the next site is added. A site whose garden lacks
 * the portal's own address is a site where guests meet a network that does not work,
 * and nothing else in the suite would notice.
 */
describe("no site is missing the entries its portal needs", () => {
  it("seeds the required set for every site, not just the one with fixtures", async () => {
    const sites = (await (await app.request("/api/v0/sites")).json()) as { id: string }[];
    expect(sites.length).toBeGreaterThan(1);

    for (const site of sites) {
      const res = await app.request(`/api/v0/sites/${site.id}/walled-garden`);
      expect(res.status, site.id).toBe(200);
      const g = (await res.json()) as WalledGarden;
      const required = g.entries.filter((e) => e.origin === "required");
      expect(
        required.length,
        `${site.id} has ${required.length} required garden entries. The portal cannot ` +
          `serve itself without its own FQDN, its OCSP/CRL endpoints and the RFC 8908 ` +
          `endpoint (docs/rules/security.md §5).`,
      ).toBeGreaterThanOrEqual(4);
    }
  });
});

/**
 * Portal settings: the refusals, which are the only part with teeth.
 *
 * Each of these fails *silently* if the server accepts it. A portal with no sign-in
 * method renders perfectly and cannot be used; an oversized logo is inlined into
 * every page and paid for on every launch; an oversized photograph is a bill rather
 * than an error. None of them looks wrong on the screen that created it.
 */
describe("portal settings refuse what fails quietly", () => {
  const patch = (body: unknown, siteId = "site-hcm") =>
    app.request(`/api/v0/sites/${siteId}/portal`, {
      method: "PATCH",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });

  it("refuses a portal with no way to sign in", async () => {
    const res = await patch({ methods: [] });
    expect(res.status).toBe(422);
    expect(res.headers.get("content-type")).toContain("application/problem+json");
  });

  it("refuses a portal with no language", async () => {
    expect((await patch({ languages: [] })).status).toBe(422);
  });

  it("refuses a blank venue name", async () => {
    expect((await patch({ venueName: "   " })).status).toBe(422);
  });

  it("refuses a logo that is linked rather than inlined", async () => {
    expect((await patch({ logoDataUri: "https://cdn.example.com/logo.png" })).status).toBe(422);
  });

  it("refuses a logo over the inline limit", async () => {
    const big = `data:image/png;base64,${"A".repeat(LOGO_MAX_BYTES * 2)}`;
    expect((await patch({ logoDataUri: big })).status).toBe(422);
  });

  it("treats a cleared terms box as absent, not as empty terms", async () => {
    /* The resolution order has to see a missing key. A present empty string resolves
       to "" and renders nothing where it should have fallen through to the venue's
       other language, or to Foyer's own wording. */
    const res = await patch({ terms: { summary: { en: "Real terms.", vi: "   " } } });
    expect(res.status).toBe(200);
    const s = (await res.json()) as PortalSettings;
    expect(Object.keys(s.terms.summary ?? {})).toEqual(["en"]);
  });

  it("refuses a photograph over the per-image budget", async () => {
    const res = await app.request("/api/v0/sites/site-hcm/portal/hero-image", {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        filename: "big.jpg",
        contentType: "image/jpeg",
        dataBase64: Buffer.alloc(IMAGE_MAX_BYTES + 1).toString("base64"),
      }),
    });
    expect(res.status).toBe(413);
    expect(res.headers.get("content-type")).toContain("application/problem+json");
  });

  it("refuses SVG, which can carry script on a pre-authentication page", async () => {
    const res = await app.request("/api/v0/sites/site-hcm/portal/hero-image", {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        filename: "x.svg",
        contentType: "image/svg+xml",
        dataBase64: Buffer.from("<svg/>").toString("base64"),
      }),
    });
    expect(res.status).toBe(422);
  });

  it("serves an accepted photograph from the same origin, at the path it reported", async () => {
    const put = await app.request("/api/v0/sites/site-hcm/portal/hero-image", {
      method: "PUT",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        filename: "ok.jpg",
        contentType: "image/jpeg",
        dataBase64: Buffer.alloc(2048, 7).toString("base64"),
      }),
    });
    expect(put.status).toBe(200);
    const image = (await put.json()) as { url: string; bytes: number };
    expect(image.bytes).toBe(2048);
    /* Root-relative and same-origin — the claim the whole design rests on. The
       portal's FQDN is already inside the walled garden, so this is reachable before
       a guest signs in; an absolute URL would not be. */
    expect(image.url.startsWith("/")).toBe(true);
    expect(image.url.startsWith("//")).toBe(false);

    const asset = await app.request(image.url);
    expect(asset.status, "the path it reported does not serve the bytes").toBe(200);
    expect(asset.headers.get("content-type")).toContain("image/jpeg");

    /* And the guest-facing context carries it, as a path rather than a data URI. */
    const ctx = (await (await app.request("/api/v0/portal/context")).json()) as {
      heroImageUrl: string | null;
    };
    expect(ctx.heroImageUrl).toBe(image.url);

    await app.request("/api/v0/sites/site-hcm/portal/hero-image", { method: "DELETE" });
  });

  it("keeps the venue profiles answering when one is asked for by name", async () => {
    /* The settings record is the default source, but the profiles are the portal's
       review mechanism and several end-to-end locks select one. If editing settings
       ever shadowed them, those locks would silently start testing the wrong world. */
    for (const id of ["solo", "foreign-terms", "no-terms"]) {
      const res = await app.request(`/api/v0/portal/context?venue=${id}`);
      expect(res.status, id).toBe(200);
      expect(res.headers.get("X-Foyer-Mock-Venue"), id).toBe(id);
    }
    /* And with nothing asked for, the operator's record answers — which is the half
       that makes the admin screen mean anything. */
    const dflt = await app.request("/api/v0/portal/context");
    expect(dflt.headers.get("X-Foyer-Mock-Venue")).toBe("site-settings");
  });
});

/**
 * Alarms are kinds, and the server does not compose the sentence.
 *
 * This is the shape of the defect rather than one instance of it: the dashboard —
 * the first screen anyone opens — rendered `Alarm.title` and `Alarm.detail`
 * verbatim, and the server wrote them in its own language. A console cannot
 * translate a sentence it was handed, and a server cannot know who is reading.
 *
 * The fallback fields still exist, for a kind a client predates. Asserting they are
 * *absent* here is the load-bearing part: a server that always sends text is a
 * server whose clients never implement the kind, and the fallback quietly becomes
 * the real path again.
 */
describe("alarms carry a kind, not a sentence", () => {
  const alarms = async (siteId = "site-hcm") => {
    const res = await app.request(`/api/v0/alarms?siteId=${siteId}`);
    expect(res.status).toBe(200);
    return (await res.json()) as {
      kind: string;
      level?: string;
      title?: string;
      detail?: string;
      params?: Record<string, unknown>;
      subject?: { kind?: string; id?: string; name?: string };
    }[];
  };

  it("raises several, so this is not vacuous", async () => {
    expect((await alarms()).length).toBeGreaterThan(2);
  });

  it("sends no composed prose at all", async () => {
    for (const a of await alarms()) {
      expect(a.kind, "an alarm with no kind cannot be rendered by any client").toBeTruthy();
      expect(a.title, `${a.kind} carries server prose`).toBeUndefined();
      expect(a.detail, `${a.kind} carries server prose`).toBeUndefined();
    }
  });

  it("sends the params its copy needs", async () => {
    /* The counted kinds are the ones whose sentence cannot be static. If the param
       is missing the console renders "0 sessions", which is worse than wrong — it
       is an alarm that contradicts itself. */
    const counted = (await alarms()).filter(
      (a) => a.kind === "sessions-orphaned" || a.kind === "sessions-stale",
    ) as { kind: string; params?: { count?: number } }[];
    expect(counted.length).toBeGreaterThan(0);
    for (const a of counted) expect(a.params?.count, a.kind).toBeGreaterThan(0);
  });

  /**
   * The opposite guard, and the reason a gap here goes unnoticed.
   *
   * `AlarmKind` is a closed enum and the console's catalogue is a `Record` over it,
   * so a new kind cannot be forgotten in the client — the compiler says so. Nothing
   * in the compiler says the opposite thing: that a kind nobody raises has copy no
   * viewer has ever seen, and a screen state no fixture reaches. A
   * `deriveAlarms` that iterated `w.nas` and `w.sessions` and never looked at
   * `w.controllers` would leave `controller-version-unsupported` drawn on the
   * controllers screen and rendered on none.
   *
   * The expected set is the enum itself, so a kind added tomorrow arrives here as a
   * failure rather than as a value nobody thought to add to a list.
   */
  it("raises every kind in the enum, so no alarm copy goes unreviewed", async () => {
    const declared = (contractDocument().components.schemas.AlarmKind as { enum: string[] }).enum;
    const seen = new Set<string>((await alarms()).map((a) => a.kind));
    expect(
      declared.filter((k) => !seen.has(k)),
      "In `AlarmKind`, with a console catalogue entry, and nothing in this world raises " +
        "it — so the copy has never been on a screen and the state cannot be reviewed.",
    ).toEqual([]);
  });

  it("says which controller, which version, and what would be supported", async () => {
    /* `minimumVersion` is the field that makes the alarm an instruction. Without it
       the operator is told their controller is unsupported and has to go and find
       out what would not be — the same argument `ControllerProbe.minimumVersion`
       carries, applied to the surface an operator actually passes. */
    const found = (await alarms()).filter((a) => a.kind === "controller-version-unsupported");
    expect(found.length, "the fixture world raises none").toBeGreaterThan(0);
    for (const a of found) {
      expect(a.params?.controllerName).toBeTruthy();
      expect(a.params?.vendor).toBeTruthy();
      expect(a.params?.version, "the version it is running").toBeTruthy();
      expect(a.params?.minimumVersion, "what would be supported").toBeTruthy();
      /* Not the same claim as unreachable, and the subject has to be resolvable to a
         row on `/controllers` rather than to an access point. */
      expect(a.subject?.kind).toBe("controller");
      expect(world.controllers.some((c) => c.id === a.subject?.id)).toBe(true);
      expect(a.level).toBe("urgent");
    }
  });

  /**
   * The half a fixture alone cannot carry.
   *
   * A probe that read `versionSupported = credentialOk ? true : …` would, on the
   * first press of **Probe** on the row below, report it supported, clear the pill
   * and remove the alarm — a fixture healed by the one path that was supposed to
   * establish it. Driving the probe is the only way to see that.
   */
  it("does not heal the controller whose version is below the floor", async () => {
    const before = world.controllers.find((c) => c.versionSupported === false)!;
    expect(before, "no unsupported controller to probe").toBeTruthy();
    const version = before.version;

    const res = await send(`/controllers/${before.id}/probe`, { method: "POST" });
    expect(res.status).toBe(200);
    const probe = (await res.json()) as {
      reachability: string;
      versionSupported: boolean | null;
      minimumVersion: string | null;
      checks: { kind: string; result: string }[];
    };

    /* Reachable and the credential accepted — this is not a connectivity failure
       wearing a different label. */
    expect(probe.reachability).toBe("ok");
    expect(probe.checks.find((c) => c.kind === "credential")?.result).toBe("pass");
    expect(probe.versionSupported, "the probe reported it supported").toBe(false);
    expect(probe.minimumVersion).toBeTruthy();
    /* And the dialog agrees with the pill: a green tick here on a controller the
       dashboard calls unsupported is two answers to one question. */
    expect(probe.checks.find((c) => c.kind === "version")?.result).toBe("fail");
    /* And the check the contract calls *what Foyer is here for*. It is the one an
       operator opens this dialog to read, and it cannot pass on a version whose
       driver API is not there. */
    expect(probe.checks.find((c) => c.kind === "guest-authorization")?.result).toBe("fail");

    const after = world.controllers.find((c) => c.id === before.id)!;
    expect(after.versionSupported, "the probe overwrote it").toBe(false);
    expect(after.version).toBe(version);
    expect(
      (await alarms()).some(
        (a) => a.kind === "controller-version-unsupported" && a.subject?.id === before.id,
      ),
      "the alarm disappeared after a probe",
    ).toBe(true);
  });

  /**
   * The other direction.
   *
   * `CONTROLLER_MINIMUM_VERSION.ruckus` is `null` — **no floor has been established**,
   * and `docs/protocol-reality.md` records it as an open question. A probe that read
   * `met === null ? true : met` would bring a reachable Ruckus back **supported**: a
   * verdict on a comparison the server could not make.
   *
   * It is the same error as the one the *unreachable* branch already refuses, pointing
   * the other way. That branch says reporting unsupported for a box nobody reached
   * sends the operator to upgrade software that may be fine; this one would say the
   * version is fine when nothing had looked at it, on the one screen an operator opens
   * to find out. `null` is the third value both branches need, and the console renders
   * it as *Not established* already.
   *
   * Driven rather than read off the fixture, because the fixture Ruckus has no
   * credential and therefore never reaches this branch — which is exactly how such a
   * bug would survive a fixture-only check.
   */
  it("establishes nothing about a vendor with no floor, and says so", async () => {
    expect(
      CONTROLLER_MINIMUM_VERSION.ruckus,
      "Ruckus has a floor now. If somebody measured one, this test is the wrong shape " +
        "and `docs/protocol-reality.md` is where the number belongs.",
    ).toBeNull();

    const created = await send("/controllers", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        name: "SmartZone reachable",
        vendor: "ruckus",
        baseUrl: "https://10.40.0.11:8443",
        authMode: "local-admin",
        tlsMode: "verify",
        username: "u",
        password: "p",
      }),
    });
    expect(created.status).toBe(201);
    const { id } = (await created.json()) as { id: string };

    const res = await send(`/controllers/${id}/probe`, { method: "POST" });
    expect(res.status).toBe(200);
    const probe = (await res.json()) as {
      reachability: string;
      version: string | null;
      versionSupported: boolean | null;
      minimumVersion: string | null;
      checks: { kind: string; result: string }[];
    };

    /* Reachable, and the credential accepted. Everything that *was* measured passed,
       which is what makes the unmeasured claim easy to miss. */
    expect(probe.reachability).toBe("ok");
    expect(probe.checks.find((c) => c.kind === "credential")?.result).toBe("pass");
    expect(probe.version, "it read a version off the controller").toBeTruthy();

    expect(
      probe.versionSupported,
      "the probe reported a verdict against a floor that does not exist",
    ).toBeNull();
    expect(probe.minimumVersion, "there is no floor to state").toBeNull();
    /* And the dialog agrees with the pill, in the third case as in the other two: a
       green tick beside a version nothing compared is the same two-answers-to-one-
       question this list already refuses for `false`. */
    expect(
      probe.checks.find((c) => c.kind === "version")?.result,
      "a green tick for a check that was not made",
    ).toBe("skip");

    /* And the stored record, because the pill and the alarm are drawn from that
       rather than from the probe body. `false` would raise
       `controller-version-unsupported` against a controller nobody measured. */
    const stored = world.controllers.find((c) => c.id === id)!;
    expect(stored.versionSupported).toBeNull();
    expect(
      (await alarms()).some(
        (a) => a.kind === "controller-version-unsupported" && a.subject?.id === id,
      ),
      "an unsupported-version alarm for a vendor with no floor",
    ).toBe(false);
  });
});

// ── every refusal this server can compose, driven once ───────────────────────

/** The parts of a problem document, or of a `Failure`, that the guards below read. */
interface ProblemBody {
  type?: string;
  title?: string;
  status?: number;
  detail?: string;
  params?: {
    fields?: Record<string, string>;
    fieldParams?: Record<string, Record<string, unknown>>;
  } & Record<string, unknown>;
  errors?: Record<string, string>;
}

interface Emission {
  /** Named so a failure tells a reader what to go and run, not just what broke. */
  how: string;
  status: number;
  /** The problem itself. Not always the body — `Failure` travels inside a `200`. */
  problem: ProblemBody;
  /** `false` for the one failure that is not a problem document, so the title guard skips it. */
  document: boolean;
}

const send = (path: string, init?: RequestInit) =>
  app.request(`/api/v0${path}`, {
    ...init,
    headers: { "content-type": "application/json", ...(init?.headers ?? {}) },
  });

const patchPortal = (body: unknown) =>
  send("/sites/site-hcm/portal", { method: "PATCH", body: JSON.stringify(body) });

/** A one-block design, the block overridden — enough to reach each design rule. */
const designWith = (block: Partial<PortalBlock>): PortalDesign => ({
  accent: null,
  headline: {},
  blocks: [
    {
      id: "b1",
      kind: "text",
      placement: "welcome",
      visible: true,
      content: { vi: { title: "Xin chào" } },
      ...block,
    },
  ],
});

const refusal = (how: string, run: () => Response | Promise<Response>) => ({
  how,
  async emit(): Promise<Emission> {
    const res = await run();
    return { how, status: res.status, problem: (await res.json()) as ProblemBody, document: true };
  },
});


/**
 * The two operators these drives sign in as, named rather than indexed.
 *
 * Neither is the fixture whose `lastLoginAt` is `null` — that row exists to be an
 * operator who has **never signed in**, and a drive that signed in as them would heal
 * the fixture it was written to carry (`docs/rules/frontend.md`).
 */
const ADMINISTRATOR_USERNAME = "dang.quoc.viet";
const STAFF_USERNAME = "le.minh.tuan";

/**
 * Sign in, and return the cookie.
 *
 * **`DEV_PASSWORD` is the password, and it is not a static credential.** No fixture
 * operator carries one — `docs/rules/security.md` §7 forbids a static credential
 * anywhere in this repository — so the mock generates one per process the way
 * `deploy/first-boot.sh` does, and this file reads the generated value rather than
 * typing a value of its own. Passing a different `password` is how the refusal branch
 * is driven; see *"refuses the wrong password"* below.
 */
async function signInAs(username: string, password = DEV_PASSWORD): Promise<string> {
  const res = await send("/auth/session", {
    method: "POST",
    body: JSON.stringify({ username, password }),
  });
  expect(res.status, `${username} could not sign in`).toBe(200);
  return res.headers.get("set-cookie")?.split(";")[0] ?? "";
}

/**
 * The single enabled administrator, asserted to be single.
 *
 * The `/problems/last-administrator` drives are meaningless if the fixtures happen to
 * have two — the refusal would not fire and the drive would pass by answering `200`.
 * Asked of the world rather than hardcoded, so a fixture that gains a second
 * administrator fails here with a sentence rather than silently downgrading two
 * drives into nothing.
 */
function onlyEnabledAdministrator() {
  const admins = world.operators.filter((o) => o.role === "administrator" && o.enabled);
  expect(
    admins.length,
    "the last-administrator refusal cannot fire unless there is exactly one enabled administrator",
  ).toBe(1);
  return admins[0]!;
}

/**
 * A real request for every refusal in `server.ts`, and nothing but real requests.
 *
 * Two separate guarantees rest on this list, and they need different kinds of
 * completeness — which is why it is one list rather than a few hand-picked paths:
 *
 *   - **Every value of `ProblemType` is reachable.** The whole argument for a closed
 *     enum is that a client catalogue keyed on it is a `Record<ProblemType, Copy>`
 *     whose gaps are compile errors, and that argument is worth nothing if a value
 *     describes something no server sends: an entry that can never be shown to render
 *     correctly is a mechanism that looks like a check and is not. The expected set is computed from
 *     `openapi.yaml`, so adding a value fails this file until something can emit it.
 *
 *   - **Every refusal is *answered* correctly**, which is a per-branch claim rather
 *     than a per-type one. `/problems/validation` alone is fourteen branches with
 *     seven different field faults between them; one drive would leave thirteen
 *     unchecked. So the expected set for that half is computed from the **titles
 *     `server.ts` passes to `problem()`**, read out of the source, and a branch added
 *     there fails this file until it is driven here.
 *
 * Two choices worth stating, because both were arguable:
 *
 *   - **The fault injector accepts one value.** A header that could conjure any type
 *     would make every value "reachable" through the same three lines and prove
 *     nothing. `/problems/internal` is injected because the mock has no honest crash
 *     to drive; everything else here goes through the branch that really produces it.
 *   - **`/problems/session-end-failed` is not injected**, though it is just as hard
 *     to reach: it *has* a real branch (`revokeVoucher` in `state.ts`), and faking it
 *     would leave that branch never executed — the failure this file's own header
 *     comment exists to describe. It is driven instead, by putting a guest on an
 *     access point nothing can reach and then revoking their voucher.
 */
const REFUSALS = [
  // ── portal ─────────────────────────────────────────────────────────────────
  refusal("GET /portal/context?present=0 — the forged-redirect case", () =>
    send("/portal/context?present=0"),
  ),
  refusal("POST /portal/agree at a venue that does not offer terms-only access", async () => {
    /* Set from here rather than inherited: whether terms-only access is on is shared
       mutable state, and a drive that depended on the order of the file above it
       would pass or fail for reasons unrelated to what it claims to test. */
    await patchPortal({ methods: ["voucher", "account"] });
    return send("/portal/agree", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: "lang=en",
    });
  }),
  refusal("GET /portal/status from a browser that has never been let on", () =>
    send("/portal/status"),
  ),

  // ── the sign-in budget ─────────────────────────────────────────────────────
  //
  // Against a username nobody holds, so the bucket this spends is nobody's: a drive
  // that exhausted a fixture operator's budget would make every later sign-in in
  // this file answer 429 for thirty seconds.
  refusal("POST /auth/session a sixth time inside thirty seconds", async () => {
    const attempt = () =>
      send("/auth/session", {
        method: "POST",
        body: JSON.stringify({ username: "rate.limit.drive", password: "not-a-password" }),
      });
    for (let i = 0; i < 5; i++) await attempt();
    const res = await attempt();
    expect(res.status).toBe(429);
    expect(Number(res.headers.get("retry-after"))).toBeGreaterThanOrEqual(1);
    return res;
  }),
  // ── the body ceilings (`auth.ts`) ──────────────────────────────────────────
  //
  // Anonymous, so the anonymous 16 KiB ceiling: a public operation gets it whoever
  // asks, and nothing is read.
  refusal("POST /auth/session with a 17 KiB body — the anonymous ceiling", () =>
    send("/auth/session", {
      method: "POST",
      body: JSON.stringify({ username: "x".repeat(17 << 10), password: "y" }),
    }),
  ),
  // ── the first-access setup ─────────────────────────────────────────────────
  //
  // The ordinary world is an install whose setup completed long ago.
  refusal("POST /auth/setup on an install with no setup open", () =>
    send("/auth/setup", {
      method: "POST",
      body: JSON.stringify({ setupCode: "0000-0000-0000-0000", username: "x", password: "y" }),
    }),
  ),
  // Opened for this drive alone and closed again, so no other drive sees it open.
  refusal("POST /auth/setup with a code that is not the installer's", async () => {
    const code = "7777-7777-7777-7777";
    openSetup(code);
    try {
      return await send("/auth/setup", {
        method: "POST",
        body: JSON.stringify({
          setupCode: "0000-0000-0000-0000",
          username: "first.admin",
          password: "long-enough-password",
        }),
      });
    } finally {
      claimSetup(code);
    }
  }),
  // ── privacy ────────────────────────────────────────────────────────────────
  refusal("PUT /privacy/retention with an accounting period of 0 days", () =>
    send("/privacy/retention", {
      method: "PUT",
      body: JSON.stringify({ changes: [{ category: "accounting", days: 0 }] }),
    }),
  ),
  refusal("PUT /privacy/retention with an audit period of fifteen years", () =>
    send("/privacy/retention", {
      method: "PUT",
      body: JSON.stringify({ changes: [{ category: "audit", days: 5475 }] }),
    }),
  ),
  refusal("POST /privacy/erasures naming a MAC that is twelve hex digits inside a username", () =>
    send("/privacy/erasures", {
      method: "POST",
      body: JSON.stringify({ subject: { kind: "mac", mac: "user-cafe00112233" } }),
    }),
  ),
  refusal("POST /privacy/erasures naming a phone number that is an e-mail address", () =>
    send("/privacy/erasures", {
      method: "POST",
      body: JSON.stringify({ subject: { kind: "phone", phone: "lan@example.com" } }),
    }),
  ),
  refusal("POST /privacy/erasures naming an e-mail address that is a phone number", () =>
    send("/privacy/erasures", {
      method: "POST",
      body: JSON.stringify({ subject: { kind: "email", email: "0912 345 678" } }),
    }),
  ),

  // ── operator settings (the admin-configurability rule) ─────────────────────
  refusal("PUT /privacy/maintenance with a ten-second interval and a 20 000-row batch", () =>
    send("/privacy/maintenance", {
      method: "PUT",
      body: JSON.stringify({
        intervalSeconds: 10,
        staleAfterMissedUpdates: 4,
        staleCeilingPerPass: 200,
        retentionBatchSize: 20000,
      }),
    }),
  ),
  refusal("PUT /sites/site-hanoi/guest-wifi-key with a seven-character password", () =>
    send("/sites/site-hanoi/guest-wifi-key", {
      method: "PUT",
      body: JSON.stringify({ passphrase: "1234567" }),
    }),
  ),
  refusal("PATCH an account on to 802.1X with no password to make the verifier from", () =>
    send("/accounts/acc-1", { method: "PATCH", body: JSON.stringify({ eapTtlsPap: true }) }),
  ),

  // ── guest data ─────────────────────────────────────────────────────────────
  refusal("PUT /sites/site-hanoi/guest-data asking for consent with nobody to contact", () =>
    send("/sites/site-hanoi/guest-data", {
      method: "PUT",
      body: JSON.stringify({
        fields: [{ field: "name", required: false }],
        marketingConsent: { enabled: true, text: { en: "Send me offers." } },
      }),
    }),
  ),

  // ── the network tag ────────────────────────────────────────────────────────
  refusal("PUT /network/settings with a javascript: portal address and a zero burst", () =>
    send("/network/settings", {
      method: "PUT",
      body: JSON.stringify({
        portalUrl: "javascript:alert(1)",
        venueUrl: null,
        throttles: {
          consoleAddress: { burst: 10, intervalMs: 6000 },
          consoleAccount: { burst: 5, intervalMs: 30000 },
          portalAddress: { burst: 0, intervalMs: 1000 },
          portalAccount: { burst: 5, intervalMs: 30000 },
        },
        bodyCeilings: { portalBytes: 16384, consoleBytes: 1048576, anonymousBytes: 16384, headerBytes: 65536 },
      }),
    }),
  ),
  refusal("PUT /network/portal-certificate with something that is not a certificate", () =>
    send("/network/portal-certificate", {
      method: "PUT",
      body: JSON.stringify({ certificatePem: "not a certificate", privateKeyPem: "nor a key" }),
    }),
  ),

  // ── one venue per install ──────────────────────────────────────────────────
  //
  // The fixture world has three sites, so the FIRST createSite call already is the
  // fourth. That is what makes this drive trivially reachable here and the `201`
  // unreachable — the success branch is in `first-run.test.ts`, against
  // FOYER_MOCK_FRESH=1.
  refusal("POST /sites in a deployment that already has a venue", () =>
    send("/sites", {
      method: "POST",
      body: JSON.stringify({ name: "A second venue this install may not have" }),
    }),
  ),

  // ── the shared 404, and the injector ───────────────────────────────────────
  refusal("GET /nas/does-not-exist", () => send("/nas/does-not-exist")),
  refusal("X-Foyer-Mock-Fault with a word this mock does not inject", () =>
    send("/sites", { headers: { "X-Foyer-Mock-Fault": "spontaneous-combustion" } }),
  ),
  refusal("X-Foyer-Mock-Fault: internal on any request", () =>
    send("/sites", { headers: { "X-Foyer-Mock-Fault": "internal" } }),
  ),

  // ── portal settings ────────────────────────────────────────────────────────
  refusal("PATCH the portal with a blank venue name", () => patchPortal({ venueName: "   " })),
  refusal("PATCH the portal with no way to sign in", () => patchPortal({ methods: [] })),
  refusal("PATCH the portal with no language", () => patchPortal({ languages: [] })),
  refusal("PATCH the portal with a linked logo rather than an inlined one", () =>
    patchPortal({ logoDataUri: "https://cdn.example.net/logo.png" }),
  ),
  refusal("PATCH the portal with a logo over the byte limit", () =>
    patchPortal({
      logoDataUri: `data:image/png;base64,${Buffer.alloc(LOGO_MAX_BYTES + 64).toString("base64")}`,
    }),
  ),
  refusal("PATCH terms-only access on with no time limit", () =>
    patchPortal({ methods: ["voucher", "click-through"], clickThroughMinutes: null }),
  ),
  refusal("PATCH phone sign-in on before a provider is configured", () =>
    patchPortal({ methods: ["voucher", "phone"] }),
  ),
  // ── the admin-configurability rule's messaging settings ───────────────────
  refusal("PUT phone abuse limits with a day limit under the hour limit", () =>
    send("/sites/site-hcm/portal/phone/abuse-controls", {
      method: "PUT",
      body: JSON.stringify({
        limits: {
          perNumberPerHour: 4,
          perNumberPerDay: 3,
          perDevicePerHour: 5,
          perAddressPerHour: 60,
          codeMinutes: 5,
          maxAttempts: 5,
        },
        mobilePrefixes: ["09"],
      }),
    }),
  ),
  refusal("POST a phone test message with no number", () =>
    send("/sites/site-hcm/portal/phone/test-message", { method: "POST", body: JSON.stringify({}) }),
  ),
  refusal("PUT a deployment SMS cap of zero", () =>
    send("/phone-sign-in/spend-cap", { method: "PUT", body: JSON.stringify({ dailyLimit: 0 }) }),
  ),
  refusal("PUT a site time zone nobody has heard of", () =>
    send("/sites/site-hcm/time-zone", { method: "PUT", body: JSON.stringify({ timeZone: "Mars/Olympus_Mons" }) }),
  ),
  refusal("PUT phone sign-in with a message that has no {code}", () =>
    send("/sites/site-hcm/portal/phone", {
      method: "PUT",
      body: JSON.stringify({
        provider: "esms-sms",
        brandname: "FOYER",
        dailyLimit: 10,
        sessionMinutes: 60,
        messageTemplate: "Welcome to the venue",
        apiKey: "mock-api-key",
        secretKey: "mock-secret-key",
      }),
    }),
  ),

  // ── page design ────────────────────────────────────────────────────────────
  // One drive per rule the schema cannot state, and one for the schema itself.
  refusal("PATCH a page design whose block text is over its length", () =>
    patchPortal({ design: designWith({ content: { vi: { title: "x".repeat(81) } } }) }),
  ),
  refusal("PATCH a page design with a link before sign-in", () =>
    patchPortal({ design: designWith({ kind: "button", url: "https://example.com", placement: "welcome" }) }),
  ),
  refusal("PATCH a page design whose button is not a link", () =>
    patchPortal({ design: designWith({ kind: "button", url: "javascript:alert(1)", placement: "connected" }) }),
  ),
  refusal("PATCH a page design whose campaign ends before it starts", () =>
    patchPortal({ design: designWith({ startsOn: "2026-10-02", endsOn: "2026-10-01" }) }),
  ),

  // ── hero image ─────────────────────────────────────────────────────────────
  refusal("PUT a hero image with no image in it", () =>
    send("/sites/site-hcm/portal/hero-image", {
      method: "PUT",
      body: JSON.stringify({ contentType: "image/png", dataBase64: "" }),
    }),
  ),
  refusal("PUT a hero image in a format the pre-auth page will not render", () =>
    send("/sites/site-hcm/portal/hero-image", {
      method: "PUT",
      body: JSON.stringify({ contentType: "image/gif", dataBase64: "AAAA" }),
    }),
  ),
  refusal("PUT a hero image one byte over the limit", () =>
    send("/sites/site-hcm/portal/hero-image", {
      method: "PUT",
      body: JSON.stringify({
        contentType: "image/png",
        dataBase64: Buffer.alloc(IMAGE_MAX_BYTES + 1).toString("base64"),
      }),
    }),
  ),

  // ── walled garden ──────────────────────────────────────────────────────────
  refusal("POST a walled-garden entry with no destination", () =>
    send("/sites/site-hcm/walled-garden", {
      method: "POST",
      body: JSON.stringify({ kind: "fqdn", value: "" }),
    }),
  ),
  /* The three refusals of one field that made `FieldFault` necessary: `required`,
     `not-a-hostname` and `not-a-cidr` all arrive as `(/problems/validation, value)`,
     so a catalogue keyed on the pair renders one of their sentences for all three. */
  refusal("POST a walled-garden FQDN that is a URL", () =>
    send("/sites/site-hcm/walled-garden", {
      method: "POST",
      body: JSON.stringify({ kind: "fqdn", value: "https://payments.example.com/checkout" }),
    }),
  ),
  refusal("POST a walled-garden range that is not CIDR", () =>
    send("/sites/site-hcm/walled-garden", {
      method: "POST",
      body: JSON.stringify({ kind: "ip-range", value: "10.20.0.0" }),
    }),
  ),
  refusal("POST a walled-garden entry that is already there", () => {
    const existing = world.gardenEntries.find((e) => e.siteId === "site-hcm")!;
    return send("/sites/site-hcm/walled-garden", {
      method: "POST",
      body: JSON.stringify({ kind: existing.kind, value: existing.value }),
    });
  }),
  refusal("DELETE the garden entry the portal needs to serve itself", () => {
    const required = world.gardenEntries.find(
      (e) => e.siteId === "site-hcm" && e.origin === "required",
    )!;
    return send(`/sites/site-hcm/walled-garden/${required.id}`, { method: "DELETE" });
  }),

  // ── sites ──────────────────────────────────────────────────────────────────
  /* The one refusal `createSite` makes, and it is hand-written: this server
     validates responses against the schema and not requests, so `required: [name]`
     in `SiteCreate` refuses nothing at runtime on its own. */
  refusal("POST a site with no venue name", () =>
    send("/sites", { method: "POST", body: JSON.stringify({ name: "   " }) }),
  ),

  // ── controllers ────────────────────────────────────────────────────────────
  refusal("POST a controller with nothing filled in", () =>
    send("/controllers", { method: "POST", body: JSON.stringify({}) }),
  ),
  refusal("POST a controller whose base URL carries a path", () =>
    send("/controllers", {
      method: "POST",
      body: JSON.stringify({
        name: "UniFi",
        vendor: "unifi",
        baseUrl: "https://10.20.0.2/network/default",
        authMode: "api-key",
        tlsMode: "verify",
        apiKey: "k",
      }),
    }),
  ),
  refusal("DELETE a controller whose access points are still adopted", () => {
    const adopted = world.controllers.find((ctl) =>
      world.nas.some((n) => n.controllerId === ctl.id),
    )!;
    return send(`/controllers/${adopted.id}`, { method: "DELETE" });
  }),
  refusal("POST discovery at a controller that has never answered", () => {
    const down = world.controllers.find((ctl) => ctl.reachability !== "ok")!;
    return send(`/controllers/${down.id}/discover`, {
      method: "POST",
      body: JSON.stringify({ siteId: "site-hcm" }),
    });
  }),

  // ── access points ──────────────────────────────────────────────────────────
  refusal("POST /nas with a blank name", () =>
    send("/nas", {
      method: "POST",
      body: JSON.stringify({ siteId: "site-hcm", driver: "coa", name: "", coaVendor: "cisco" }),
    }),
  ),
  /* `coa` is one enum member for Cisco and Aruba, and they identify a session
     differently — an `audit-session-id` AV-pair against a `Calling-Station-Id` in
     Aruba's own spelling. RFC 5176 §2.3 makes the NAS match on every
     session-identification attribute in the request, so the wrong one matches
     nothing: a careful device answers `Error-Cause 503` and a careless one ignores
     the request and **acknowledges it**, which is a revocation that reports success
     and revoked nobody. There is no safe default, so the registration asks.

     A valid address and a name, so the refusal under test is this one. */
  refusal("POST /nas as coa without saying which CoA vendor", () =>
    send("/nas", {
      method: "POST",
      body: JSON.stringify({
        siteId: "site-hcm",
        driver: "coa",
        name: "Campus edge",
        ipAddress: "10.20.0.96",
      }),
    }),
  ),
  refusal("POST /nas for a driver whose devices come from a controller", () =>
    send("/nas", {
      method: "POST",
      /* A valid address, so that the refusal under test is the driver's and not the
         address's. The two checks run in one pass and a body missing both would
         answer `/problems/validation` — leaving `/problems/needs-controller` with no
         drive at all, which the enum sweep catches and this comment forestalls. */
      body: JSON.stringify({
        siteId: "site-hcm",
        driver: "unifi",
        name: "By hand",
        ipAddress: "10.20.0.98",
      }),
    }),
  ),
  refusal("POST /nas with no address", () =>
    send("/nas", {
      method: "POST",
      body: JSON.stringify({
        siteId: "site-hcm",
        driver: "coa",
        name: "Addressless",
        coaVendor: "cisco",
      }),
    }),
  ),
  refusal("POST /nas with something that is not an address or a range", () =>
    send("/nas", {
      method: "POST",
      /* Not a typo to be forgiving about: `radius_clients.address` is `inet`, and an
         unparseable value that reached the column would come back as a 500. */
      body: JSON.stringify({
        siteId: "site-hcm",
        driver: "coa",
        name: "Fat fingers",
        ipAddress: "10.20.0.two",
        coaVendor: "cisco",
      }),
    }),
  ),
  refusal(
    "rotate the secret of a device whose driver cannot push and which nothing can reach",
    () => send(`/nas/${world.nas.filter(rotationRefused)[0]!.id}/secret`, { method: "POST" }),
  ),

  // ── sessions: the one action, and its two refusals ─────────────────────────
  /* Both are asked to revoke the voucher and the grant as well, because the claim
     under test is that a refusal changes nothing — a refusal that happened after the
     withdrawals would pass a status-code check and fail the guest. */
  refusal("POST disconnect on a session that has already ended", () =>
    send(`/sessions/${world.sessions.find((s) => s.state === "ended")!.id}/disconnect`, {
      method: "POST",
      body: JSON.stringify({ revokeVoucher: true, revokeGrant: true }),
    }),
  ),
  refusal("POST disconnect on a device whose driver cannot end a session", () => {
    const cannot = world.sessions.find(
      (s) =>
        s.state !== "ended" &&
        world.nas.find((n) => n.id === s.nasId)?.capabilities.canDeauthGuest === false,
    )!;
    return send(`/sessions/${cannot.id}/disconnect`, {
      method: "POST",
      body: JSON.stringify({ revokeVoucher: true, revokeGrant: true }),
    });
  }),

  // ── vouchers and accounts ──────────────────────────────────────────────────
  refusal("POST a voucher batch with no label and no count", () =>
    send("/voucher-batches", {
      method: "POST",
      body: JSON.stringify({ siteId: "site-hcm", label: "", count: 0 }),
    }),
  ),
  refusal("POST an account with nothing filled in", () =>
    send("/accounts", {
      method: "POST",
      body: JSON.stringify({ siteId: "site-hcm", username: "", password: "" }),
    }),
  ),
  /* The other half of the pair `FieldFault` was written for: `username` is refused
     two ways, so `(/problems/validation, username)` cannot key a client's copy. */
  refusal("POST an account with a username somebody already has", () =>
    send("/accounts", {
      method: "POST",
      body: JSON.stringify({
        siteId: "site-hcm",
        username: world.accounts[0]!.username,
        password: "correct horse",
      }),
    }),
  ),

  // ── the console's own authentication ───────────────────────────────────────
  /*
   * `401` from `GET /auth/session` is a **normal answer** and is driven here anyway.
   * The reason is the same as for everything else in this list: a client keys its
   * routing on `/problems/not-signed-in`, and a value in `ProblemType` that no
   * request produces is a value whose handling nobody has ever seen work.
   */
  refusal("GET /auth/session with no cookie — the state every console starts in", () =>
    send("/auth/session"),
  ),
  /*
   * A temporary password somebody else set (`foyerctl create-operator
   * --must-change-password`).
   *
   * Set here and cleared here, rather than left on a fixture operator, for the reason
   * the terms-only drive above gives: shared mutable state that another drive
   * inherits makes this list order-dependent. Everything else in this file signs in
   * as that administrator and would be refused by the gate.
   *
   * A real sign-in and a real cookie, because the gate reads the *session's*
   * operator: sending with no cookie takes the `!REQUIRE_SESSION` branch above the
   * check and would drive nothing at all — which is a refusal that passes by never
   * being reached.
   */
  refusal("GET /nas while the signed-in operator is on a temporary password", async () => {
    const admin = onlyEnabledAdministrator();
    const cookie = await signInAs(admin.username);
    setMustChangePassword(admin.id, true);
    try {
      return await send("/nas?siteId=site-hcm", { headers: { cookie } });
    } finally {
      setMustChangePassword(admin.id, false);
    }
  }),
  refusal("POST /auth/session with an empty body", () =>
    send("/auth/session", { method: "POST", body: JSON.stringify({}) }),
  ),
  refusal("POST /auth/session with a username nobody has", () =>
    send("/auth/session", {
      method: "POST",
      body: JSON.stringify({ username: "nobody.at.all", password: "correct horse battery" }),
    }),
  ),
  refusal("POST /auth/password with no session at all", () =>
    send("/auth/password", {
      method: "POST",
      body: JSON.stringify({ currentPassword: "a", newPassword: "bbbbbbbbbbbbbb" }),
    }),
  ),
  /* Two drives, because `currentPassword` and `newPassword` are refused four ways
     between them and `(type, field)` cannot key a client's copy for any of it. */
  refusal("POST /auth/password with the wrong current password and a short new one", async () => {
    const cookie = await signInAs(ADMINISTRATOR_USERNAME);
    return send("/auth/password", {
      method: "POST",
      headers: { cookie },
      body: JSON.stringify({ currentPassword: "not what was typed", newPassword: "short" }),
    });
  }),
  refusal("POST /auth/password with the password it already is", async () => {
    /* The one this operator actually has, since sign-in verifies it. Typing a
       string here instead would drive the *wrong-current-password* refusal and
       silently stop driving `unchanged`, which is the fault this case is for. */
    const password = DEV_PASSWORD;
    const cookie = await signInAs(ADMINISTRATOR_USERNAME, password);
    return send("/auth/password", {
      method: "POST",
      headers: { cookie },
      body: JSON.stringify({ currentPassword: password, newPassword: password }),
    });
  }),

  // ── operators ──────────────────────────────────────────────────────────────
  /*
   * The role gate, driven with a **real staff session**. This is the one refusal in
   * this list that requires the mock's authorization to be live, and it is live
   * precisely so this drive exists: `/problems/forbidden` is a value the console has
   * to have copy for, and copy for a refusal nothing produces has never been shown to
   * be right. See `REQUIRE_SESSION` in `server.ts` for what is and is not enforced.
   */
  refusal("GET /operators as a staff session", async () => {
    const cookie = await signInAs(STAFF_USERNAME);
    return send("/operators", { headers: { cookie } });
  }),
  refusal("POST /operators with nothing filled in", () =>
    send("/operators", { method: "POST", body: JSON.stringify({}) }),
  ),
  refusal("PATCH the only enabled administrator to disabled", () =>
    send(`/operators/${onlyEnabledAdministrator().id}`, {
      method: "PATCH",
      body: JSON.stringify({ enabled: false }),
    }),
  ),
  refusal("DELETE the only enabled administrator", () =>
    send(`/operators/${onlyEnabledAdministrator().id}`, { method: "DELETE" }),
  ),

  // ── reports ────────────────────────────────────────────────────────────────
  //
  // An export is always bounded: no range, an inverted one and one past the
  // operation's `x-max-range-days` are three different refusals, and the third
  // carries its figure.
  refusal("GET /exports/sessions with no range", () => send("/exports/sessions?siteId=site-hcm")),
  refusal("GET /exports/sessions with a range that ends before it starts", () =>
    send("/exports/sessions?siteId=site-hcm&from=2026-09-02T00:00:00Z&to=2026-09-01T00:00:00Z"),
  ),
  refusal("GET /exports/guests over more than a quarter", () =>
    send("/exports/guests?siteId=site-hcm&from=2026-01-01T00:00:00Z&to=2026-06-01T00:00:00Z"),
  ),
  refusal("GET /trends by week over more than a year", () =>
    send("/trends?siteId=site-hcm&granularity=week&from=2024-01-01T00:00:00Z&to=2026-01-01T00:00:00Z"),
  ),
];

/**
 * The one failure that is not a refusal: revoking succeeded and the driver call that
 * would have ended the guest's session did not.
 *
 * Driven end to end because the branch is real — `revokeVoucher` in `state.ts`. A
 * guest joins on the access point the fixtures keep unreachable, redeems a voucher
 * there, and the operator then revokes it. `?nas=` is what makes that reachable at
 * all; without it every simulated guest is on a healthy device and this branch never
 * runs.
 */
async function driveSessionEndFailed(): Promise<Emission> {
  const how = "revoke a voucher whose guest is on an access point nothing can reach";
  const unreachable = world.nas.find((n) => n.reachability === "unreachable")!;

  const jar = await send(`/portal/context?nas=${unreachable.id}`);
  expect(jar.status, "the guest could not get a portal page on the unreachable AP").toBe(200);
  expect(jar.headers.get("X-Foyer-Mock-Nas")).toBe(unreachable.id);
  const cookie = jar.headers.get("set-cookie")?.split(";")[0] ?? "";

  const voucher = world.vouchers.find((v) => v.state === "unused")!;
  const redeemed = await app.request("/api/v0/portal/voucher", {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded", cookie },
    body: `lang=en&code=${voucher.code}`,
  });
  expect(redeemed.status, "the voucher was not redeemed, so there is no session to fail on").toBe(
    303,
  );
  expect(redeemed.headers.get("location")).toContain("/connected/");

  const res = await send(`/vouchers/${voucher.id}/revoke`, { method: "POST" });
  const body = (await res.json()) as { sessionEnded: boolean; failure?: ProblemBody };
  expect(body.sessionEnded, "the session ended after all, so nothing failed").toBe(false);
  return { how, status: res.status, problem: body.failure ?? {}, document: false };
}

/** Every drive, run once. The guards below read this rather than re-driving. */
async function driveAll(): Promise<Emission[]> {
  const out: Emission[] = [];
  for (const r of REFUSALS) out.push(await r.emit());
  out.push(await driveSessionEndFailed());
  return out;
}

/**
 * Every `title` literal `server.ts` hands to `problem()`, read out of the source.
 *
 * The expected set for "every refusal is driven", computed from the thing it is a
 * claim about rather than typed here — the same reason the capability-gating test
 * computes its control count from the capability model. A branch added to `server.ts`
 * fails this file until somebody drives it.
 */
function refusalTitlesInSource(): { callSites: number; matched: number; titles: string[] } {
  const source = readFileSync(new URL("./server.ts", import.meta.url), "utf8");
  const calls = source.split("problem(c, ").slice(1);
  const titles: string[] = [];
  for (const call of calls) {
    const found = /title: "([^"]+)"/.exec(call.slice(0, 400));
    if (found) titles.push(found[1]!);
  }
  /* `callSites` travels with the titles so the reader of this list can be held to
     having found one for each. A regex that silently stops matching returns an empty
     array, and an empty expected set passes — which is the exact shape this file
     exists to refuse.
   *
   * **`matched` is that count and `titles` is the deduplicated set, and they are
   * different numbers.** Comparing the *unique* titles against the call sites only
   * holds while no two refusals share a title — a second refusal legitimately reusing
   * "Missing information" would turn the check red for a reason that has nothing to
   * do with the extraction, and, worse, it would stay green if two call sites shared
   * a title and one of them lost its `title:` line altogether. Counting matches is
   * what the sentence beside it claims to be counting. */
  return { callSites: calls.length, matched: titles.length, titles: [...new Set(titles)].sort() };
}

/**
 * A failed revoke is reachable from `/vouchers` alone.
 *
 * `driveSessionEndFailed` above reaches the same branch and does it the long way
 * round: a guest joins through the portal on the unreachable access point, redeems a
 * voucher there, and *then* the operator revokes it. That proves the server composes
 * the failure. It does not give a viewer a way to see the screen, because nobody
 * opening the console has a portal session to drive first — and without this
 * fixture, that would be the only route.
 *
 * So this is the same failure through the door an operator uses: a voucher already in
 * the world, already in use, whose session is on the device nothing can reach.
 *
 * It is deliberately the **last** thing in this file to touch that voucher, because
 * driving it revokes it — the fixture case survives in `validate.test.ts`, which
 * builds its own world and asserts the *relationship* rather than an id.
 */
describe("a voucher the fixtures put out of reach", () => {
  it("revokes, fails to end the session, and says which device and why", async () => {
    const unreachable = new Set(world.nas.filter((n) => n.reachability !== "ok").map((n) => n.id));
    const stranded = world.vouchers.find((v) => {
      if (v.state !== "in-use") return false;
      const s = world.sessions.find(
        (x) => x.voucherId === v.id && (x.state === "online" || x.state === "orphaned"),
      );
      return Boolean(s && unreachable.has(String(s.nasId)));
    });
    expect(
      stranded,
      "No in-use voucher sits on an unreachable device, so this drive is asserting " +
        "nothing and `/vouchers` cannot show a failed revoke without the portal.",
    ).toBeTruthy();

    const session = world.sessions.find((s) => s.voucherId === stranded!.id)!;
    const nas = world.nas.find((n) => n.id === session.nasId)!;

    const res = await send(`/vouchers/${stranded!.id}/revoke`, { method: "POST" });
    expect(res.status).toBe(200);
    const body = (await res.json()) as {
      voucher: { state: string };
      sessionEnded: boolean;
      failure?: { type?: string; params?: Record<string, unknown>; detail?: string };
    };

    /* Both halves, because the whole point of the copy is that they differ: the code
       is dead and the guest is still online. A toast saying only "Revoked" is wrong
       about the second. */
    expect(body.voucher.state, "the code was not revoked").toBe("revoked");
    expect(body.sessionEnded, "the driver call cannot have succeeded here").toBe(false);
    expect(body.failure?.type).toBe("/problems/session-end-failed");
    /* Named, not counted. `nasName` is what the operator walks to and
       `nasReachability` is which of three different afternoons this is — unreachable,
       expired credentials, or a device row that is not there at all. */
    expect(body.failure?.params?.nasName).toBe(nas.name);
    expect(body.failure?.params?.nasReachability).toBe(nas.reachability);
    /* And no prose. `Failure` carries the same `ProblemType` keyspace as a refusal
       precisely so this sentence lives in the console's language, not the server's. */
    expect(body.failure?.detail, "the mock composed a sentence again").toBeUndefined();

    /* The session is untouched: it could not be ended, so nothing may report that it
       was. A mock that quietly marked it `ended` here would be inventing the success
       the response says did not happen. */
    const after = world.sessions.find((s) => s.id === session.id)!;
    expect(after.state).toBe(session.state);
    expect(after.endedAt).toBeNull();
  });
});

/**
 * The teardown a harness needs and the product does not.
 *
 * `web/e2e`'s revoke cases create a batch per run and the contract has no delete, so
 * they accumulate across runs. A restart clears them, because the mock is in memory;
 * that is what makes it easy to ignore, and it stops being true the day anything
 * persists.
 *
 * The answer is a mock affordance rather than a contract operation, and the argument
 * is written where the route is. What is asserted here is the half that makes it safe
 * to hand to a suite running parallel workers against one server: it removes what a
 * test made, and it **cannot** reach what the fixtures made.
 */
describe("the mock lets a test drop what it created, and nothing else", () => {
  const createBatch = async (label: string) => {
    const res = await send("/voucher-batches", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({
        siteId: "site-hcm",
        label,
        count: 3,
        plan: { name: label, durationMinutes: 60 },
        expiryMode: "from-first-use",
        validForHours: 1,
      }),
    });
    expect(res.status, "the batch was not created, so the teardown has nothing to drop").toBe(201);
    return (await res.json()) as { id: string };
  };

  it("removes the batch and the vouchers under it", async () => {
    const batch = await createBatch("teardown drives me");
    expect(world.vouchers.filter((v) => v.batchId === batch.id).length).toBe(3);

    const res = await app.request(`/mock/voucher-batches/${batch.id}`, { method: "DELETE" });
    expect(res.status).toBe(200);
    expect(await res.json()).toMatchObject({ batchId: batch.id, vouchersRemoved: 3 });

    expect(world.batches.some((b) => b.id === batch.id), "the batch survived").toBe(false);
    /* The vouchers go with it. A batch removed on its own leaves rows pointing at a
       batch that is not there, which is a defect the mock would be inventing. */
    expect(world.vouchers.some((v) => v.batchId === batch.id), "orphaned vouchers").toBe(false);

    /* And it is gone from the API, not merely from the array. */
    const listed = (await (await send("/voucher-batches?siteId=site-hcm&limit=200")).json()) as {
      items: { id: string }[];
    };
    expect(listed.items.some((b) => b.id === batch.id)).toBe(false);
  });

  it("refuses a seeded batch, which is the half that makes it safe to run in parallel", async () => {
    /*
     * The load-bearing assertion. `web/e2e` runs workers in parallel against one
     * mock, so a teardown that could reach a fixture batch is a teardown that empties
     * the ugly world out from under every other worker — and the failure lands in a
     * file that did nothing wrong. `batch-event` is 500 vouchers and the pagination
     * case; `batch-old` is the only genuinely finished batch in the world.
     */
    const seeded = world.batches[0]!;
    const vouchersBefore = world.vouchers.filter((v) => v.batchId === seeded.id).length;
    expect(vouchersBefore, "the seeded batch has no vouchers, so this proves nothing").toBeGreaterThan(0);

    const res = await app.request(`/mock/voucher-batches/${seeded.id}`, { method: "DELETE" });
    expect(res.status).toBe(409);
    expect((await res.json()) as { error: string }).toMatchObject({ error: "seeded" });

    expect(world.batches.some((b) => b.id === seeded.id), "a fixture batch was dropped").toBe(true);
    expect(world.vouchers.filter((v) => v.batchId === seeded.id).length).toBe(vouchersBefore);
  });

  it("answers plainly for a batch that is not there", async () => {
    const res = await app.request("/mock/voucher-batches/batch-that-never-was", {
      method: "DELETE",
    });
    expect(res.status).toBe(404);
    /* Not a problem document. An RFC 9457 body here would put a refusal in the
       product's `ProblemType` keyspace that no product route composes, and the
       drivability sweep below would then be asserting about a mock affordance. */
    expect(res.headers.get("content-type")).not.toContain("problem+json");
  });
});

/**
 * The catalogue is not a list of failures nobody can produce.
 *
 * `ProblemType` is only worth being a closed enum if a client can be held to covering
 * it, and that only means something if every value is a thing a server really sends.
 */
describe("every problem type can be produced by driving this server", () => {
  const catalogue = () => {
    const schema = contractDocument().components.schemas.ProblemType as {
      enum: string[];
      "x-client-synthesised"?: string[];
    };
    return { all: schema.enum, clientOnly: schema["x-client-synthesised"] ?? [] };
  };

  it("exempts only what the contract exempts, and only what no server can send", () => {
    const { all, clientOnly } = catalogue();
    /* `/problems/unreachable` is what a client renders when the request never
       arrived — no response exists, so no server can send it. The exemption lives in
       the document so that exempting a *new* value is a visible contract edit rather
       than a line added to this file. */
    expect(clientOnly).toEqual(["/problems/unreachable"]);
    expect(clientOnly.filter((t) => !all.includes(t)), "exempts a type not in the enum").toEqual([]);
  });

  it("emits every server-sent value, with the expected set computed from the enum", async () => {
    const { all, clientOnly } = catalogue();
    const expected = all.filter((t) => !clientOnly.includes(t)).sort();
    /* `?? "(no type)"` rather than a filter: an emission with no type has to show up
       as something the enum does not contain, not disappear from the comparison. */
    const seen = [
      ...new Set((await driveAll()).map((e) => e.problem.type ?? "(no type)")),
    ].sort();

    expect(
      expected.filter((t) => !seen.includes(t)),
      "In `ProblemType` and nothing here emits it. Add a drive to `REFUSALS`, or — if " +
        "no server can ever send it — say so in the document's `x-client-synthesised`.",
    ).toEqual([]);
    expect(
      seen.filter((t) => !expected.includes(t)),
      "Emitted by this server and not a server-sent member of `ProblemType`.",
    ).toEqual([]);
  });

  it("drives every refusal branch, with the expected set computed from the source", async () => {
    const seen = [
      ...new Set(
        (await driveAll())
          .filter((e) => e.document)
          .map((e) => e.problem.title)
          .filter((t): t is string => typeof t === "string"),
      ),
    ].sort();
    const source = refusalTitlesInSource();
    /* Non-vacuity first, because an empty expected set passes silently: every
       `problem()` call site in `server.ts` must have yielded a title. */
    expect(
      source.matched,
      `read ${source.matched} titles from ${source.callSites} problem() call sites — ` +
        "the extraction has stopped seeing them, so the set below is not the real one",
    ).toBe(source.callSites);

    expect(
      source.titles.filter((t) => !seen.includes(t)),
      "`server.ts` composes this refusal and nothing in `REFUSALS` reaches it, so its " +
        "body has never been validated and its copy has never been looked at.",
    ).toEqual([]);
  });

  it("reaches every status the mock claims to answer with, including 500", async () => {
    /* `ProblemStatus` gained `500` for `/problems/internal`. A widened union nothing
       exercises is a wider claim and no more coverage. */
    const seen = new Set((await driveAll()).map((e) => e.status));
    for (const status of [401, 403, 404, 409, 413, 422, 500]) {
      expect(seen.has(status), `nothing in REFUSALS answers ${status}`).toBe(true);
    }
  });
});

/**
 * No response body composes prose in a language the reader did not choose.
 *
 * A guard rather than a review: the mock is where a Vietnamese sentence gets typed,
 * and every one of them reached a screen. Fixture *data* — a venue's name, an
 * operator's name — is exempt, because that is the world being described rather than
 * the product talking.
 */
describe("the server does not talk to the operator in its own language", () => {
  const VIETNAMESE =
    /[ăâđêôơưĂÂĐÊÔƠƯẠ-ỹ]/;

  /**
   * ── what this does not assert, and why that would be the wrong test ────────
   *
   * Asserting that the prose **exists** and is English would hold in place exactly
   * the failure the contract names: *"a server that always sends text is a server
   * whose clients never implement the kind."* A guard that requires `detail` is a
   * guard that guarantees the console keeps rendering it, and the whole of
   * `ProblemType` is an argument that it must not.
   *
   * So it asserts the opposite for `detail`, over every refusal branch in
   * `server.ts` rather than a few paths somebody remembered to add.
   *
   * ── which prose survives, and why each one is a separate decision ──────────
   *
   *   - **`detail` — gone, on every product answer.** It is the composed sentence:
   *     the field where the drift and the wrong language live, and the one a
   *     conforming server omits once its clients implement the type. There is exactly
   *     one exception, asserted separately below — the `/problems/internal` the mock
   *     sends when it has *crashed or violated its own contract*. That `detail` is
   *     addressed to whoever is running the mock, in the terminal they started it in;
   *     no client renders it. The injected `/problems/internal`, which stands in for
   *     a product 500, carries none.
   *
   *   - **`title` — stays, and must stay English.** The contract requires it, because
   *     it is the only thing a console that predates a `type` can render at all. It
   *     is the fallback of last resort rather than the normal path, which is why it
   *     is a short label and not a sentence.
   *
   *   - **`errors` values — stay, and must stay English.** The keys are the
   *     load-bearing half: they are what tells a form which inputs to mark. The
   *     values are the fallback for a `(type, field, fault)` triple this client has
   *     no copy for, and a client that knows the triple reads `params.fields`
   *     instead. Deleting them would leave a newer server unable to say anything at
   *     all to an older console — the case the contract deliberately keeps.
   *
   * The language check therefore covers `title` and `errors`, which is the whole of
   * the prose a product answer carries.
   */
  it("answers every refusal with a keyable type and no composed sentence", async () => {
    const emissions = await driveAll();
    expect(emissions.length, "nothing was driven, so this asserts nothing").toBeGreaterThan(20);

    for (const e of emissions) {
      expect(e.problem.type, `${e.how} has no type, so a client cannot key copy on it`).toBeTruthy();

      expect(
        e.problem.detail,
        `${e.problem.type} (${e.how}) still composes a sentence: "${e.problem.detail}". ` +
          "A server that always sends text is a server whose clients never implement " +
          "the kind — send the type and its params instead.",
      ).toBeUndefined();

      const prose = [e.problem.title, ...Object.values(e.problem.errors ?? {})].join(" ");
      expect(VIETNAMESE.test(prose), `${e.how} answered in Vietnamese: ${prose}`).toBe(false);
    }
  });

  it("keeps the fault kinds and the field messages on the same keys", async () => {
    /* `params.fields` is what a client with copy renders from and `errors` is what it
       falls back to, so a key in one and not the other is a field that can be marked
       and not described, or described only in the server's language. Asserted on the
       equality rather than on either half: either half alone passes for a server that
       has drifted. */
    for (const e of await driveAll()) {
      const faults = Object.keys(e.problem.params?.fields ?? {}).sort();
      const messages = Object.keys(e.problem.errors ?? {}).sort();
      expect(faults, `${e.how} marks fields it cannot describe, or the reverse`).toEqual(messages);
    }
  });

  it("carries a fault kind for every field it refuses, over every fault in the enum", async () => {
    /* The point of `FieldFault` is that a client keys copy on `(type, field, fault)`.
       A drive set that only ever produced `required` would leave the other nine
       members exactly as unproven as the open string they replaced, so the expected
       set is the enum again rather than a number. */
    const declared = (
      contractDocument().components.schemas.FieldFault as { enum: string[] }
    ).enum.slice();
    const seen = new Set(
      (await driveAll()).flatMap((e) => Object.values(e.problem.params?.fields ?? {})),
    );
    expect(
      declared.filter((f) => !seen.has(f)),
      "In `FieldFault` and no drive produces it, so no client's copy for it can be " +
        "shown to be right. Drive it, or take it out of the contract.",
    ).toEqual([]);
  });

  /*
   * ── the figure in the sentence ───────────────────────────────────────────────
   *
   * `FieldFault` divides the work: the client owns the sentence, the server supplies
   * the facts in it. That holds while the sentence is complete on its own — *Required.*
   * — and breaks the moment one contains a number, because a number is exactly what a
   * client cannot know. Without the figure, a five-character password is refused and
   * the console renders **"At least 0."**: the fault is in the enum, the copy for it
   * reads `params.minimumCount`, and the refusal has nowhere to put one.
   *
   * Nothing goes red. A catalogue entry that compiles, an alert that mounts with text,
   * and a wrong sentence — one rung below where the other guards in this file look.
   * The four below are that rung.
   */
  it("declares the same fault figures in the document and in the types", () => {
    /* `x-fault-params` is the authority and `FAULT_PARAMS` is a mirror of it that a
       compiler can read. Two declarations of one fact drift; asserted in both
       directions so neither can gain an entry alone. */
    const declared = (
      contractDocument().components.schemas.FieldFault as {
        "x-fault-params"?: Record<string, string[]>;
      }
    )["x-fault-params"];
    expect(
      declared,
      "`x-fault-params` left the document, so nothing declares the figures",
    ).toBeTruthy();
    expect(Object.fromEntries(Object.entries(FAULT_PARAMS).map(([k, v]) => [k, [...v]]))).toEqual(
      declared,
    );

    const faults = (contractDocument().components.schemas.FieldFault as { enum: string[] }).enum;
    expect(
      Object.keys(declared!).filter((f) => !faults.includes(f)),
      "names a fault that is not in `FieldFault`",
    ).toEqual([]);
  });

  it("sends every figure a refused field's sentence needs, on the field it belongs to", async () => {
    /* The expected set is computed from the contract, not typed here: a fault that
       gains a param in `x-fault-params` fails this until every emission carries it.
       Read off `params.fieldParams[field]` rather than the top level on purpose — a
       refusal marks several fields at once, so a figure at the top level belongs to
       no particular one and two fields refused the same way would collide there. */
    const declared = (
      contractDocument().components.schemas.FieldFault as {
        "x-fault-params": Record<string, string[]>;
      }
    )["x-fault-params"];

    let checked = 0;
    for (const e of await driveAll()) {
      for (const [field, fault] of Object.entries(e.problem.params?.fields ?? {})) {
        const needed = declared[fault];
        if (!needed) continue;
        const carried = e.problem.params?.fieldParams?.[field];
        expect(
          carried,
          `${e.how} refused \`${field}\` \`${fault}\` and sent no figures for it. ` +
            "The client's copy for that fault has a number in it and cannot invent one — " +
            'it renders "At least 0."',
        ).toBeTruthy();
        for (const key of needed) {
          const value = carried?.[key];
          /* Not merely present. `count(undefined)` and `count(0)` render the same
             wrong sentence, so a zero passes a presence check and fails an operator.
             Most declared figures are counts. A param that is a *name* rather than a
             number — `certificate-refused` carries `reason`, a name the screen that
             uploads a certificate keys its sentence on — is held to the same standard
             a count is, present and not empty, because `""` renders the same blank
             sentence a zero does. */
          expect(
            typeof value === "string"
              ? value.length > 0
              : typeof value === "number" && Number.isFinite(value) && value > 0,
            `${e.how}: \`${field}\` needs \`${key}\` and carries ${JSON.stringify(value)}`,
          ).toBe(true);
          checked += 1;
        }
      }
    }
    expect(
      checked,
      "no drive refuses a field whose fault names a figure, so this asserts nothing",
    ).toBeGreaterThan(0);
  });

  it("drives every fault that declares a figure, so no declaration is unproven", async () => {
    /* The same argument as the enum sweep above, one level in. A fault could be
       driven — satisfying that test — through a path where it names no params, and
       the declaration would still never have been exercised. */
    const declared = Object.keys(
      (
        contractDocument().components.schemas.FieldFault as {
          "x-fault-params": Record<string, string[]>;
        }
      )["x-fault-params"],
    );
    const withFigures = new Set(
      (await driveAll()).flatMap((e) =>
        Object.entries(e.problem.params?.fields ?? {})
          .filter(([field]) => e.problem.params?.fieldParams?.[field])
          .map(([, fault]) => fault),
      ),
    );
    expect(
      declared.filter((f) => !withFigures.has(f)),
      "declares figures in `x-fault-params` and no drive produces them, so the " +
        "declaration has never been shown to be honoured.",
    ).toEqual([]);
  });

  it("keeps the figures on fields it actually refused", async () => {
    /* `fieldParams` is a subset of `fields`, never a set of its own: a figure for a
       field nothing marked is a fact about nothing, and a client keying on `fields`
       would never read it. */
    for (const e of await driveAll()) {
      const marked = Object.keys(e.problem.params?.fields ?? {});
      const figured = Object.keys(e.problem.params?.fieldParams ?? {});
      expect(
        figured.filter((f) => !marked.includes(f)),
        `${e.how} sends figures for a field it did not refuse`,
      ).toEqual([]);
    }
  });

  it("keeps the one diagnostic that is not a product answer, and keeps it out of the product", async () => {
    /*
     * The named exception, asserted in both directions rather than described.
     *
     * `onError` answers `/problems/internal` carrying the message from whatever went
     * wrong — a `ContractViolation` naming the invariant that broke, or the thrown
     * error itself. That sentence is the mock talking to whoever is running it, in
     * the terminal they started it in; no client renders it, and it is the only
     * `detail` left in this server. Driven here by handing a handler a body that is
     * not JSON, which is a real crash rather than a switch somebody could flip in
     * front of a viewer.
     *
     * The injected `/problems/internal` is the opposite case: it stands in for a
     * product 500 and must have a product answer's shape. If the exception ever stops
     * being an exception, one of these two assertions goes red.
     */
    const crashed = await send("/sites/site-hcm/portal", { method: "PATCH", body: "{not json" });
    expect(crashed.status).toBe(500);
    expect(crashed.headers.get("content-type")).toContain("application/problem+json");
    const body = (await crashed.json()) as ProblemBody;
    expect(body.type).toBe("/problems/internal");
    expect(body.detail, "the crash path is the one place a sentence survives").toBeTruthy();
    /* And it is still a problem document. `onError` builds this one by hand — a throw
       inside the error handler is a crash with no answer at all — so nothing else
       checks it. */
    expect(() => conform("Problem", body)).not.toThrow();

    const injected = await send("/sites", { headers: { "X-Foyer-Mock-Fault": "internal" } });
    const product = (await injected.json()) as ProblemBody;
    expect(product.type).toBe("/problems/internal");
    expect(
      product.detail,
      "the injected 500 stands in for a product answer and must have a product answer's shape",
    ).toBeUndefined();
  });
});

/**
 * Click-through: the mode with no credential, and the two refusals that hold it up.
 *
 * Every other way in has something behind it. This one has a button, so what would
 * ordinarily be belt-and-braces is the only thing there: the venue must have offered
 * the method, and it must have said for how long.
 */
describe("terms-only access refuses to be unbounded or unoffered", () => {
  const patch = (body: unknown) =>
    app.request("/api/v0/sites/site-hcm/portal", {
      method: "PATCH",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
    });

  it("refuses the method without a time limit", async () => {
    const res = await patch({ methods: ["voucher", "click-through"], clickThroughMinutes: null });
    expect(res.status).toBe(422);
  });

  it("refuses clearing the limit while the method stays on", async () => {
    /* Checked against the resulting state and not the patch. Turning the method on
       in one request and the minutes off in another is the obvious way past a check
       that only looks at what it was handed. */
    const on = await patch({ methods: ["voucher", "click-through"], clickThroughMinutes: 30 });
    expect(on.status).toBe(200);
    expect((await patch({ clickThroughMinutes: null })).status).toBe(422);
  });

  it("lets a guest on when the venue offers it, and refuses when it does not", async () => {
    await patch({ methods: ["voucher", "account", "click-through"], clickThroughMinutes: 30 });

    const jar = await app.request("/api/v0/portal/context");
    const cookie = jar.headers.get("set-cookie")?.split(";")[0] ?? "";

    const agree = await app.request("/api/v0/portal/agree", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded", cookie },
      body: "lang=en",
    });
    /* 303 on every outcome. The CNA re-checks captivity only after a navigation. */
    expect(agree.status).toBe(303);
    expect(agree.headers.get("location")).toContain("/en/connected/");
    expect(agree.headers.get("cache-control")).toBe("no-store");

    /* And the grant is real, not a redirect to a page that lies. */
    const status = await app.request("/api/v0/portal/status", { headers: { cookie } });
    expect(status.status).toBe(200);

    /* Turned off, the endpoint stops working — a button that is hidden while its
       endpoint still grants is an endpoint somebody finds. */
    await patch({ methods: ["voucher", "account"] });
    const refused = await app.request("/api/v0/portal/agree", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: "lang=en",
    });
    expect(refused.status).toBe(404);

    await patch({ methods: ["voucher", "account", "click-through"], clickThroughMinutes: 30 });
  });
});


/**
 * The failure a guest can see and nothing else would report.
 *
 * A valid voucher, a device the driver confirmed present, a grant that is written —
 * and then a controller that will not let the device on. The `303` has already been
 * flushed by then, because the CNA re-checks captivity only after a document
 * navigation and nothing may come before that, so the guest is reading the connected
 * page while the call is still in flight.
 *
 * Unrecorded, every one of those guests would be told `connected`, and the failure
 * would reach nobody: not the audit log — which cannot hold it, since an `AuditEntry`
 * requires an `actorId` and the actor here is a guest with a voucher — not
 * `listAuthAttempts`, and not their own screen. `AuthSource: driver` and
 * `AuthEventKind: authorize-failed` are what carry it.
 *
 * `?authorize=0` is what makes it drivable, and it is deliberately not `?present=0`:
 * that one refuses *before* anything is granted and the guest keeps their code.
 */
describe("a controller that refuses after the redirect reaches both readers", () => {
  const arrive = async (query = "") => {
    const jar = await app.request(`/api/v0/portal/context?${query}`);
    expect(jar.status).toBe(200);
    return jar.headers.get("set-cookie")?.split(";")[0] ?? "";
  };

  const redeem = (cookie: string, code: string) =>
    app.request("/api/v0/portal/voucher", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded", cookie },
      body: `lang=en&code=${code}`,
    });

  const statusFor = async (cookie: string) => {
    const res = await app.request("/api/v0/portal/status", { headers: { cookie } });
    expect(res.status).toBe(200);
    return (await res.json()) as PortalStatus;
  };

  it("still answers 303 to the connected page, and then tells the truth", async () => {
    const cookie = await arrive("authorize=0");
    const voucher = world.vouchers.find((v) => v.state === "unused")!;

    const redeemed = await redeem(cookie, voucher.code);
    /* Unchanged, and it must be. A failed controller call may not turn this into a
       response the CNA does not treat as a captivity re-check — the guest is still
       mid-flow, and its button would stay "Cancel", which disassociates them. */
    expect(redeemed.status).toBe(303);
    expect(redeemed.headers.get("location")).toContain("/en/connected/");

    /* The truth arrives on the next request, which is the only channel left. */
    const status = await statusFor(cookie);
    expect(
      status.state,
      "the guest is being told they are online while no packet of theirs leaves the access point",
    ).toBe("not-on-network");
    /* The plan still travels: what failed is the network, not the sale, and a guest
       at the counter has to be able to say what they bought. */
    expect(status.plan).toBeTruthy();
  });

  it("writes the one record of it that exists anywhere", async () => {
    const cookie = await arrive("authorize=0");
    const voucher = world.vouchers.find((v) => v.state === "unused")!;
    await redeem(cookie, voucher.code);

    const res = await app.request("/api/v0/auth-attempts?siteId=site-hcm&limit=200&source=driver");
    const { items } = (await res.json()) as { items: AuthAttempt[] };
    const mine = items.filter((a) => a.kind === "authorize-failed" && a.voucherId === voucher.id);
    expect(mine.length, "the failure left no row an operator could find").toBe(1);

    const row = mine[0]!;
    expect(row.outcome).toBe("rejected");
    /* The MAC is the only identifier a café operator has when a guest is standing in
       front of them. A record nobody can join to a device is a record nobody uses. */
    expect(row.callingStationId).toBeTruthy();
    /* Params say which of two failures it was, because they need different people:
       an unreachable controller is somebody checking a container, and a 401 is
       somebody re-issuing an API key. */
    expect(row.params).toMatchObject({ failure: "refused", status: 401 });
  });

  it("admits no session for a device that never got on the network", async () => {
    /* The grant stands and the voucher stays spent — the venue did decide, and
       un-claiming on an outage would be a world where one code lets two devices on.
       But `radacct` records what the *network* observed, and it observed nothing. */
    const before = world.sessions.length;
    const cookie = await arrive("authorize=0");
    const voucher = world.vouchers.find((v) => v.state === "unused")!;
    await redeem(cookie, voucher.code);

    expect(world.sessions.length, "a session appeared for a device that was never let on").toBe(
      before,
    );
    expect(world.vouchers.find((v) => v.id === voucher.id)!.state).toBe("in-use");
  });

  /**
   * Every `PortalStatus.state` is produced by driving this world.
   *
   * The same standing `AuditKind`, `ProblemType` and `AuthEventKind` carry, and the
   * reason is recorded in `state.ts` beside `Revoke`: a member of a closed enum that
   * nothing here can produce is a page that can never be shown to render correctly.
   * `not-on-network` was the newest and would have been the second one to arrive with
   * no drive behind it.
   *
   * The expected set is read out of the contract rather than typed here, so a state
   * added tomorrow arrives as a failure instead of as a value nobody thought to add.
   */
  it("can produce every state the contract declares", async () => {
    const declared = (
      (contractDocument().components.schemas.PortalStatus as {
        properties: { state: { enum: string[] } };
      }).properties.state
    ).enum;
    const seen = new Set<string>();

    // connected
    const ok = await arrive();
    const v1 = world.vouchers.find((v) => v.state === "unused")!;
    await redeem(ok, v1.code);
    seen.add((await statusFor(ok)).state);

    // revoked
    await app.request(`/api/v0/vouchers/${v1.id}/revoke`, { method: "POST" });
    seen.add((await statusFor(ok)).state);

    // not-on-network
    const refused = await arrive("authorize=0");
    const v2 = world.vouchers.find((v) => v.state === "unused")!;
    await redeem(refused, v2.code);
    seen.add((await statusFor(refused)).state);

    /* expired — the clock passing a number, so the number is moved rather than
       waited out. The state itself is still computed by `getGrant`, which is the
       thing under test: expiry is derived on read here for the same reason
       `Grant.StateAt` derives it in Go, and a timer that never fires would leave a
       guest reading "connected" beside a timestamp that ran out an hour ago. */
    const expiring = await arrive();
    const v3 = world.vouchers.find((v) => v.state === "unused")!;
    await redeem(expiring, v3.code);
    const client = getPortalClient(expiring.split("=")[1]);
    expect(client, "the mock client cookie did not resolve, so nothing was driven").toBeTruthy();
    const grant = getGrant(client!.grantId);
    expect(grant, "no grant to expire").toBeTruthy();
    grant!.expiresAt = Date.now() - 1000;
    seen.add((await statusFor(expiring)).state);

    expect(
      declared.filter((d) => !seen.has(d)),
      "declared in `PortalStatus.state` and unreachable by driving this world, so the " +
        "portal copy for it has never been on a screen",
    ).toEqual([]);
  });
});

// ── the console's own authentication ─────────────────────────────────────────

/**
 * The role model lives in the contract, and the console reads it off the wire.
 *
 * The failure this is written against is the one capability gating is prone to:
 * a client that compiles in its own table of what a role means keeps using that table
 * after the server's answer changes, and the drift presents as a control that renders
 * and then `403`s. `AuthSession.permissions` is the wire answer; `ROLE_PERMISSIONS`
 * is the v1 derivation the *server* implements. These assert the two agree, over the
 * whole of `OperatorRole` rather than over the one role somebody remembered.
 */
describe("what a role may do is answered by the server, not compiled into a client", () => {
  const roles = () =>
    (contractDocument().components.schemas.OperatorRole as { enum: OperatorRole[] }).enum;

  const operatorWithRole = (role: OperatorRole) => {
    const found = world.operators.find((o) => o.role === role && o.enabled);
    expect(found, `no enabled fixture operator has the role ${role}, so it cannot be driven`)
      .toBeDefined();
    return found!;
  };

  it("has an enabled fixture operator for every role in the enum", () => {
    /* Non-vacuity first: the sweeps below iterate the roles, and a role nothing can
       sign in as would drop out of every one of them silently. */
    expect(roles().length).toBeGreaterThan(1);
    for (const role of roles()) expect(operatorWithRole(role).role).toBe(role);
  });

  it("sends exactly the permissions the contract's table derives, for every role", async () => {
    for (const role of roles()) {
      const cookie = await signInAs(operatorWithRole(role).username);
      const res = await send("/auth/session", { headers: { cookie } });
      expect(res.status, role).toBe(200);
      const body = (await res.json()) as { operator: Operator; permissions: string[] };
      expect(body.operator.role).toBe(role);
      expect([...body.permissions].sort(), `${role} is answered a set the table does not derive`)
        .toEqual([...ROLE_PERMISSIONS[role]].sort());
    }
  });

  it("gives staff a strictly smaller set than an administrator, and not an empty one", () => {
    /* Two ways this model can be vacuous and both have to be closed: a staff set
       equal to the administrator's is a role that gates nothing, and an empty one is
       a role that can do nothing — a console gating on either would be right by
       accident. */
    const admin = ROLE_PERMISSIONS.administrator;
    const staff = ROLE_PERMISSIONS.staff;
    expect(staff.length).toBeGreaterThan(0);
    expect(staff.length).toBeLessThan(admin.length);
    expect(staff.filter((p) => !admin.includes(p)), "staff may do something an administrator may not")
      .toEqual([]);
  });

  it("declares a requirement for every operation, and requires every permission somewhere", () => {
    /*
     * The other half: a permission no operation requires is a gate that has never
     * been closed, and an operation with no `x-permission` is an operation with no
     * gate at all. Both expected sets are computed from the document.
     */
    const declared = (
      contractDocument().components.schemas.OperatorPermission as { enum: OperatorPermission[] }
    ).enum;
    const required = new Set(Object.values(requirementsByOperation()));

    expect(
      declared.filter((p) => !required.has(p)),
      "In `OperatorPermission` and no operation declares `x-permission` for it, so nothing " +
        "is ever gated on it. Annotate the operation it belongs to, or take it out.",
    ).toEqual([]);

    for (const requirement of required) {
      if (requirement === "public" || requirement === "authenticated") continue;
      expect(
        declared,
        `an operation declares x-permission: ${requirement}, which is not in OperatorPermission`,
      ).toContain(requirement);
    }
  });

  it("keeps the two non-permission requirements to what they are for", () => {
    const byOperation = requirementsByOperation();
    /* `public` is the guest portal and the two ways in. Anything else marked public
       is an admin operation reachable by anyone who can route to the listener. */
    const publicOps = Object.entries(byOperation)
      .filter(([, r]) => r === "public")
      .map(([id]) => id)
      .sort();
    expect(publicOps).toEqual([
      "acceptTerms",
      // The first-access setup: whoever completes it has no account yet.
      "completeAdminSetup",
      "getAdminSetup",
      "getAuthSession",
      "getPortalContext",
      "getPortalStatus",
      "loginWithAccount",
      "redeemVoucher",
      "requestPhoneCode",
      "signIn",
      "verifyPhoneCode",
      // The guest's own way out of a marketing consent, from the portal.
      "withdrawOwnMarketingConsent",
    ]);
  });
});

/**
 * Authentication is **not** enforced, and this pins that it is a decision.
 *
 * Written as a test rather than left as a comment because it is the one property the
 * admin console and its 237 end-to-end tests currently depend on. If somebody throws
 * the switch without doing the three things `REQUIRE_SESSION` names, this says so in
 * one line instead of two hundred.
 */
describe("the enforcement switch is off, deliberately, and the role gate is not", () => {
  it("answers every kind of admin operation with no session at all", async () => {
    for (const path of ["/sites", "/nas?siteId=site-hcm", "/overview?siteId=site-hcm", "/audit"]) {
      expect((await send(path)).status, path).toBe(200);
    }
    /* A mutation too, not only reads: the switch is about the whole surface. */
    const created = await send("/sites/site-hcm/walled-garden", {
      method: "POST",
      body: JSON.stringify({ kind: "fqdn", value: "switch-is-off.example.test" }),
    });
    expect(created.status).toBe(201);
    const entry = (await created.json()) as GardenEntry;
    expect((await send(`/sites/site-hcm/walled-garden/${entry.id}`, { method: "DELETE" })).status)
      .toBe(204);
  });

  it("refuses a staff session where the contract says administrator, which is not the same switch", async () => {
    const cookie = await signInAs(STAFF_USERNAME);
    for (const path of ["/operators", "/audit"]) {
      const res = await send(path, { headers: { cookie } });
      expect(res.status, `${path} is administrator-only and answered a staff session`).toBe(403);
      expect(((await res.json()) as ProblemBody).type).toBe("/problems/forbidden");
    }
    /* And it is not a blanket refusal of staff: they run the counter. */
    for (const path of ["/vouchers", "/sessions?siteId=site-hcm", "/nas?siteId=site-hcm"]) {
      expect((await send(path, { headers: { cookie } })).status, path).toBe(200);
    }
  });

  /*
   * **Both branches of the password check, and the second one is the whole test.**
   *
   * A `beginAdminSession` that matched on username and `enabled` and never looked at
   * the password would make *every* drive in this file, every review and the
   * differential harness run against a console any string signed you in to. Against
   * this fixture administrator it would answer:
   *
   *     password "correct-or-not"               -> 200
   *     password "definitely-not-the-password"  -> 200
   *
   * The accept branch alone cannot see that — it is green either way. So the refusal
   * is asserted here beside it, and both are driven from `DEV_PASSWORD` rather than
   * from a string typed into this file: a wrong password that is wrong because
   * somebody hardcoded the right one is a second copy of a credential, and it goes
   * stale the day the generator changes.
   *
   * `401` is the contract's, not a choice made here — `signIn` in `openapi.yaml`
   * declares `200` and `401` and says the `401` never reveals which of the two was
   * wrong. The byte-for-byte comparison of the three refusals is the next test.
   */
  it("signs in with the right password and refuses the wrong one", async () => {
    const attempt = (password: string) =>
      send("/auth/session", {
        method: "POST",
        body: JSON.stringify({ username: ADMINISTRATOR_USERNAME, password }),
      });

    const right = await attempt(DEV_PASSWORD);
    expect(right.status, "the generated password does not sign in").toBe(200);
    expect(right.headers.get("set-cookie") ?? "").toContain("foyer_admin_session=");

    const wrong = await attempt(`${DEV_PASSWORD}x`);
    expect(
      wrong.status,
      "a wrong password signed in — the mock is accepting every string anybody types",
    ).toBe(401);
    expect(wrong.headers.get("set-cookie"), "a refused sign-in set a session").toBeNull();
    expect(await wrong.json()).toMatchObject({ type: "/problems/bad-credentials", status: 401 });

    /* One character short of right, so length alone cannot be what refuses it. */
    const nearly = await attempt(DEV_PASSWORD.slice(0, -1));
    expect(nearly.status).toBe(401);
  });

  /**
   * **The cookie's name is the contract's, and the deviation is the transport.**
   *
   * `securitySchemes.sessionCookie.name` is `__Host-foyer_admin_session`. The prefix
   * is not an attribute a server may skip: a user agent refuses a
   * cookie so named unless it is `Secure`, `Path=/` and carries no `Domain` — so it
   * *means* `Secure`, and this mock serves plain HTTP on localhost, where a `secure`
   * cookie is one the browser discards.
   *
   * So the two halves are asserted together, from the **declared** name rather than
   * from a string typed here. The name is read out of `openapi.yaml`, so an edit to
   * the document that dropped the prefix fails this rather than quietly agreeing
   * with it — which is exactly what an assertion like the one beside this does on
   * its own: `toContain("foyer_admin_session=")` is satisfied by the prefixed name,
   * by the bare name, and by any drift between them.
   *
   * The invariant underneath both branches is the one that matters: **a `__Host-`
   * name never travels without `Secure`.** A mock that computed the two separately
   * could send a name every browser drops while looking like it was doing the right
   * thing.
   */
  it("names the session cookie what the contract names it, wherever the transport allows", async () => {
    const declared = (
      contractDocument().components as unknown as {
        securitySchemes: Record<string, { name?: string }>;
      }
    ).securitySchemes.sessionCookie?.name;
    expect(
      declared,
      "the contract declares no name for `sessionCookie`, so this test compares nothing",
    ).toBeTruthy();
    expect(
      declared,
      "the contract's session cookie lost its `__Host-` prefix — " +
        "and it is the browser rather than Foyer that enforces the Secure/Path=//no-Domain " +
        "shape it stands for",
    ).toMatch(/^__Host-/);
    const bare = declared!.replace(/^__Host-/, "");

    const signIn = (origin: string) =>
      app.request(`${origin}/api/v0/auth/session`, {
        method: "POST",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ username: ADMINISTRATOR_USERNAME, password: DEV_PASSWORD }),
      });

    const overTLS = (await signIn("https://console.example")).headers.get("set-cookie") ?? "";
    expect(overTLS, "over TLS the mock must set the name the contract declares").toContain(
      `${declared}=`,
    );
    expect(overTLS, "a `__Host-` name without `Secure` is a cookie every browser drops").toMatch(
      /;\s*Secure/i,
    );

    const overPlain = (await signIn("http://localhost:5170")).headers.get("set-cookie") ?? "";
    expect(overPlain, "the documented deviation is the prefix, not a different name").toContain(
      `${bare}=`,
    );
    expect(
      overPlain,
      "a `__Host-` name on plain HTTP — the browser discards it and the dev server " +
        "cannot hold a session",
    ).not.toContain("__Host-");
    expect(overPlain).not.toMatch(/;\s*Secure/i);
  });

  /*
   * The three refusals are one answer, and a wrong password is the third of them.
   */
  it("answers a wrong password exactly as it answers a stranger", async () => {
    const wrongPassword = await send("/auth/session", {
      method: "POST",
      body: JSON.stringify({ username: ADMINISTRATOR_USERNAME, password: "not-the-password" }),
    });
    const unknown = await send("/auth/session", {
      method: "POST",
      body: JSON.stringify({ username: "no.such.person.at.all", password: DEV_PASSWORD }),
    });
    expect(wrongPassword.status).toBe(401);
    expect(unknown.status).toBe(401);
    expect(
      await wrongPassword.json(),
      "the refusals differ, so the sign-in form says whether an account exists",
    ).toEqual(await unknown.json());
  });

  /*
   * And the operation that puts a credential into the world. `createOperator` threw
   * the password away, which nothing could see while any password worked: the console
   * answered `201` for an account that could not be used.
   */
  it("lets an operator it just created sign in, and only with the password it was given", async () => {
    const username = `created.${Date.now().toString(36)}`;
    const password = "a-long-enough-password";
    const created = await send("/operators", {
      method: "POST",
      body: JSON.stringify({ username, role: "staff", password }),
    });
    expect(created.status).toBe(201);

    const signIn = (p: string) =>
      send("/auth/session", { method: "POST", body: JSON.stringify({ username, password: p }) });
    expect((await signIn(password)).status, "a created operator cannot sign in").toBe(200);
    expect((await signIn(DEV_PASSWORD)).status, "any password signs the new operator in").toBe(401);
  });

  it("tells a disabled operator apart from nobody in no way at all", async () => {
    /* The username oracle. A disabled account must refuse exactly as an unknown one
       does, or disabling an account publishes that it exists. Compared byte for byte
       rather than status-only, because a differing `params` or `title` leaks it just
       as well as a differing status. */
    const disabled = world.operators.find((o) => !o.enabled);
    expect(disabled, "no disabled fixture operator, so this asserts nothing").toBeDefined();

    const attempt = (username: string) =>
      send("/auth/session", {
        method: "POST",
        body: JSON.stringify({ username, password: "correct horse battery staple" }),
      });

    const forDisabled = await attempt(disabled!.username);
    const forUnknown = await attempt("no.such.person.at.all");
    expect(forDisabled.status).toBe(401);
    expect(forUnknown.status).toBe(401);
    expect(await forDisabled.json()).toEqual(await forUnknown.json());
  });

  it("issues a CSRF token that is not the session cookie and is not shared", async () => {
    /* Issued and not checked — see `REQUIRE_SESSION`. What can be asserted today is
       that it is real: a per-session value, in the body, because the cookie is
       `httpOnly` and a token the client cannot read is a token it cannot echo. */
    const first = await send("/auth/session", {
      method: "POST",
      body: JSON.stringify({ username: ADMINISTRATOR_USERNAME, password: DEV_PASSWORD }),
    });
    const second = await send("/auth/session", {
      method: "POST",
      body: JSON.stringify({ username: ADMINISTRATOR_USERNAME, password: DEV_PASSWORD }),
    });
    const a = (await first.json()) as { csrfToken: string };
    const b = (await second.json()) as { csrfToken: string };
    expect(a.csrfToken.length).toBeGreaterThan(16);
    expect(a.csrfToken).not.toBe(b.csrfToken);

    const setCookie = first.headers.get("set-cookie") ?? "";
    expect(setCookie).toContain("foyer_admin_session=");
    expect(setCookie, "the session cookie must be unreadable by script").toContain("HttpOnly");
    expect(setCookie).toContain("SameSite=Lax");
    expect(setCookie, "the token must not be the cookie").not.toContain(a.csrfToken);
  });

  it("never returns a password, under any field name, from anything that takes one", async () => {
    /*
     * `AdminSession` holds the string the operator typed — the one place in this
     * repository a password exists at all — so the guard the accounts screen already
     * has applies here with more force. Driven against every operation that receives
     * a password rather than against the shape of one object.
     */
    /* The generated one, so the sign-in below is a `200` and the sweep reads a body
       that actually carries an operator. A wrong password would make this drive pass
       against a refusal, which is the assertion aimed at the wrong surface. */
    const password = DEV_PASSWORD;
    const responses = [
      await send("/auth/session", {
        method: "POST",
        body: JSON.stringify({ username: ADMINISTRATOR_USERNAME, password }),
      }),
      await send("/operators", {
        method: "POST",
        body: JSON.stringify({ username: `drive.${Date.now()}`, role: "staff", password }),
      }),
    ];
    for (const res of responses) {
      const body = await res.text();
      expect(body, `${res.status} carries the password back`).not.toContain(password);
      for (const [key, value] of Object.entries(JSON.parse(body) as Record<string, unknown>)) {
        /*
         * The name check is the cheap half and it stays. What it cannot see is
         * whether the thing under the name could *be* a credential, and that
         * matters: `AuthSession.mustChangePassword` matches `/pass/i` and is a
         * boolean, so a name-only guard goes red about a field that cannot carry a
         * secret in any encoding.
         *
         * **Not narrowed to a list of allowed names**, which was the tempting
         * repair and is the one that rots: an exemption for `mustChangePassword`
         * would go on excusing that name after somebody changed what it holds. The
         * property is that a credential-shaped NAME may not carry a credential-shaped
         * VALUE, and a boolean is not one — `password: "..."` still fails, and so
         * does a boolean's name if it ever becomes a string.
         */
        if (typeof value === "boolean") continue;
        expect(key, "a key that could carry a credential").not.toMatch(/pass|secret|credential/i);
      }
    }
  });

  it("refuses to leave the deployment with nobody who can administer it", async () => {
    const admin = onlyEnabledAdministrator();
    /* Three routes to the same locked-out state, and all three are refused. The rule
       is about `enabled`, not `role`: the fixtures hold a second administrator who is
       disabled, so a check that counted administrators would pass here and lock a
       real deployment out. */
    for (const [how, res] of [
      ["disable", await send(`/operators/${admin.id}`, { method: "PATCH", body: JSON.stringify({ enabled: false }) })],
      ["demote", await send(`/operators/${admin.id}`, { method: "PATCH", body: JSON.stringify({ role: "staff" }) })],
      ["delete", await send(`/operators/${admin.id}`, { method: "DELETE" })],
    ] as const) {
      expect(res.status, how).toBe(409);
      expect(((await res.json()) as ProblemBody).type).toBe("/problems/last-administrator");
    }
    /* And nothing moved. A refusal that half-applied would be worse than a mistake. */
    expect(world.operators.find((o) => o.id === admin.id)).toMatchObject({
      role: "administrator",
      enabled: true,
    });
  });

  it("lets an administrator demote themselves once somebody else can administer", async () => {
    /* The narrow reading, asserted so it is not tightened into "you may never demote
       an administrator". What is refused is the state nobody can undo, and a second
       enabled administrator is exactly what makes this one undoable. */
    const spare = await send("/operators", {
      method: "POST",
      body: JSON.stringify({
        username: `spare.admin.${Date.now().toString(36)}`,
        role: "administrator",
        password: "a-long-enough-password",
      }),
    });
    expect(spare.status).toBe(201);
    const created = (await spare.json()) as Operator;

    const admin = world.operators.find((o) => o.role === "administrator" && o.enabled && o.id !== created.id)!;
    const demoted = await send(`/operators/${admin.id}`, {
      method: "PATCH",
      body: JSON.stringify({ role: "staff" }),
    });
    expect(demoted.status).toBe(200);

    /* Put the world back: every other drive in this file asserts there is exactly one
       enabled administrator, and a test that leaves two behind breaks them by order. */
    expect((await send(`/operators/${admin.id}`, { method: "PATCH", body: JSON.stringify({ role: "administrator" }) })).status).toBe(200);
    expect((await send(`/operators/${created.id}`, { method: "DELETE" })).status).toBe(204);
  });
});

// ── the audit log ────────────────────────────────────────────────────────────

/**
 * The log records kinds and params, never a sentence — the contract's rule for
 * server prose, applied to the one surface where getting it wrong is worst.
 *
 * An alarm in the wrong language is read once, by somebody standing in front of the
 * problem. A log is read months later, by somebody reconstructing what happened, and
 * a confidently phrased sentence in a language they do not read is indistinguishable
 * from a correct one. The `title`/`detail` fallbacks exist for a console that predates
 * a kind; asserting their **absence** is the load-bearing half, exactly as it is for
 * alarms.
 */
describe("audit entries carry a kind, not a sentence", () => {
  const entries = async (query = "") => {
    const res = await send(`/audit${query}`);
    expect(res.status).toBe(200);
    return (await res.json()).items as AuditEntry[];
  };

  it("has enough history that this is not vacuous", async () => {
    expect((await entries("?limit=200")).length).toBeGreaterThan(10);
  });

  it("sends no composed prose at all", async () => {
    for (const e of await entries("?limit=200")) {
      expect(e.kind, "an entry with no kind cannot be rendered by any client").toBeTruthy();
      expect(e.title, `${e.kind} carries server prose`).toBeUndefined();
      expect(e.detail, `${e.kind} carries server prose`).toBeUndefined();
    }
  });

  it("names its actor on the entry, so the log survives the operator's deletion", async () => {
    /*
     * The row this fixture set exists for. `op-gone` is not in `world.operators` and
     * the entry still says who it was — which is what `actorUsername` is denormalised
     * for. An implementation that joined to `operators` at read time renders "deleted
     * operator" on precisely the row somebody is reading the log to understand.
     */
    const all = await entries("?limit=200");
    const orphaned = all.filter((e) => !world.operators.some((o) => o.id === e.actorId));
    expect(orphaned.length, "no entry outlives its operator, so nothing here is proven").toBeGreaterThan(0);
    for (const e of orphaned) expect(e.actorUsername, e.id).toBeTruthy();
    /* And every entry, not only the orphaned ones. */
    for (const e of all) expect(e.actorUsername, `${e.id} has no actor name`).toBeTruthy();
  });

  it("carries every kind in the enum, so no copy goes unreviewed", async () => {
    const declared = (contractDocument().components.schemas.AuditKind as { enum: string[] }).enum;
    const seen = new Set<string>((await entries("?limit=200")).map((e) => e.kind));
    expect(
      declared.filter((k) => !seen.has(k)),
      "In `AuditKind` and no fixture row has it, so the audit screen renders copy for it " +
        "that nobody has ever looked at.",
    ).toEqual([]);
  });

  it("filters by actor and by date, which is the whole of what it claims", async () => {
    const all = await entries("?limit=200");
    const actor = all[0]!.actorId;
    const byActor = await entries(`?limit=200&actorId=${encodeURIComponent(actor)}`);
    expect(byActor.length).toBeGreaterThan(0);
    expect(byActor.every((e) => e.actorId === actor)).toBe(true);
    expect(byActor.length).toBeLessThan(all.length);

    /* The fixture set deliberately holds one entry ahead of the clock — the same
       skew the session fixtures carry, in the one table read strictly in time order. */
    const now = new Date().toISOString();
    const future = all.filter((e) => e.at > now);
    expect(future.length, "no clock skew in the log, so the ordering case is untested").toBe(1);
    const past = await entries(`?limit=200&to=${encodeURIComponent(now)}`);
    expect(past.some((e) => e.at > now)).toBe(false);
    expect(past.length).toBe(all.length - 1);
  });
});

/**
 * Every kind is produced by **driving a mutation**, not by sitting in a fixture.
 *
 * The fixture guard above proves the screen has a row for each kind. This proves the
 * *server* writes each one, which is a different claim and the one that matters for
 * the Go implementation: a kind nothing emits is a catalogue entry that can never be
 * shown to be right — the same rule `ProblemType` carries, and the sixth instance of
 * this repository's recurring shape if it were skipped.
 */
describe("every audit kind is written by driving this server", () => {
  it("emits them all, with the expected set computed from the enum", async () => {
    const cookie = await signInAs(ADMINISTRATOR_USERNAME);
    const auth = { cookie };
    const before = new Set(world.audit.map((e) => e.id));
    const stamp = Date.now().toString(36);

    const json = (path: string, method: string, body?: unknown) =>
      send(path, { method, headers: auth, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });

    /*
     * Kinds this world cannot produce, **each with a condition this test evaluates**
     * rather than a sentence somebody wrote once.
     *
     * `docs/rules/testing.md`: a skip must name a condition a test can evaluate,
     * not a phase — a phase number cannot expire loudly, so nothing goes red on the
     * day it stops being true. Every entry below is pushed from an `else` whose `if`
     * is the drive itself, so the day the world can reach the operation the kind is
     * genuinely driven here instead.
     */
    const unreachable: string[] = [];

    // sites
    //
    // `createSite` refuses a second venue (`/problems/one-site-per-install`), and
    // this world has three. So `site-created` is driven in `first-run.test.ts`,
    // against `FOYER_MOCK_FRESH=1` — the only world in which the `201` exists at
    // all — and the refusal is asserted here rather than assumed, so a world that
    // ever starts empty drives the kind here on its own.
    const madeSite = await json("/sites", "POST", { name: `Drive ${stamp}` });
    if (madeSite.status !== 201) {
      expect(
        madeSite.status,
        "createSite neither made a venue nor refused for the reason this world has — " +
          "`site-created` is now in neither list, which is the shape `make diff-api` " +
          "prints as NO REASON RECORDED",
      ).toBe(409);
      expect(
        world.sites.length,
        "createSite refused with 409 in a world that holds no venue",
      ).toBeGreaterThan(0);
      unreachable.push("site-created");
    }

    // controllers
    const controller = (await (
      await json("/controllers", "POST", {
        name: `Drive ${stamp}`,
        vendor: "unifi",
        baseUrl: "https://10.20.9.9:8443",
        authMode: "api-key",
        tlsMode: "verify",
        apiKey: "k",
      })
    ).json()) as { id: string };
    /* `controller-updated`: the amendment that replaces a stored credential. Driven
       here rather than left to the fixture row, because the fixture proves the screen
       renders it and this proves the server writes it — two different claims. */
    expect(
      (await json(`/controllers/${controller.id}`, "PATCH", { name: `Drive ${stamp} renamed` }))
        .status,
    ).toBe(200);
    expect((await json(`/controllers/${controller.id}`, "DELETE")).status).toBe(204);

    /* `controller-discovery-run`, against a controller the world already has.
       Not the one above: discovery adopts access points, and a controller with
       adopted devices refuses deletion — which is a refusal this file drives
       elsewhere and must not trip over here. */
    const reachable = world.controllers.find((c) => c.reachability === "ok")!;
    expect(
      (await json(`/controllers/${reachable.id}/discover`, "POST", { siteId: reachable.vendorSites[0]?.boundSiteId ?? "site-hcm" }))
        .status,
    ).toBe(200);

    // access points
    const registered = (await (
      await json("/nas", "POST", {
        siteId: "site-hcm",
        driver: "coa",
        name: `Drive ${stamp}`,
        ipAddress: "10.20.0.97",
        /* `coa` names a family; Cisco and Aruba identify a session differently. */
        coaVendor: "cisco",
      })
    ).json()) as { nas: { id: string } };
    expect((await json(`/nas/${registered.nas.id}`, "DELETE")).status).toBe(204);

    const rotatable = world.nas.find((n) => !rotationRefused(n))!;
    expect((await json(`/nas/${rotatable.id}/secret`, "POST")).status).toBe(200);

    // walled garden
    const entry = (await (
      await json("/sites/site-hcm/walled-garden", "POST", {
        kind: "fqdn",
        value: `drive-${stamp}.example.test`,
      })
    ).json()) as { id: string };
    expect((await json(`/sites/site-hcm/walled-garden/${entry.id}`, "DELETE")).status).toBe(204);

    // portal settings, and terms as their own kind
    /* A *real* change, not the values sent back unchanged: the mock records the
       difference rather than any field the request mentioned, so a no-op writes no
       entry — the behaviour
       `TestASaveThatChangesNothingWritesNoRow` pins on the Go side. */
    expect(
      (await json("/sites/site-hcm/portal", "PATCH", { networkName: `Drive ${stamp}` })).status,
    ).toBe(200);
    expect(
      (await json("/sites/site-hcm/portal", "PATCH", { terms: { summary: { vi: `Điều khoản ${stamp}` } } }))
        .status,
    ).toBe(200);

    // vouchers
    const batch = (await (
      await json("/voucher-batches", "POST", {
        siteId: "site-hcm",
        label: `Drive ${stamp}`,
        count: 2,
        plan: { name: "Drive", durationMinutes: 60 },
        expiryMode: "from-creation",
        validForHours: 24,
      })
    ).json()) as { id: string };
    const voucher = world.vouchers.find((v) => v.batchId === batch.id)!;
    expect((await json(`/vouchers/${voucher.id}/revoke`, "POST")).status).toBe(200);

    // reports — the one read in the enum, recorded because it is the read that
    // produces a copy the log cannot follow.
    const day = new Date().toISOString();
    const weekAgo = new Date(Date.now() - 7 * 86_400_000).toISOString();
    expect(
      (await json(`/exports/sessions?siteId=site-hcm&from=${weekAgo}&to=${day}`, "GET")).status,
    ).toBe(200);
    // sessions — `session-disconnected`, on a copy of the row put back afterwards:
    // ending a fixture session would change what every later assertion in this file
    // counts as online.
    const live = world.sessions.find(
      (s) =>
        s.state === "online" &&
        world.nas.find((n) => n.id === s.nasId)?.capabilities.canDeauthGuest === true,
    )!;
    const kept = { ...live };
    expect((await json(`/sessions/${live.id}/disconnect`, "POST", {})).status).toBe(200);
    Object.assign(live, kept);

    // operators, and one's own password
    const operator = (await (
      await json("/operators", "POST", {
        username: `drive.${stamp}`,
        role: "staff",
        password: "a-long-enough-password",
      })
    ).json()) as Operator;
    expect((await json(`/operators/${operator.id}`, "PATCH", { enabled: false })).status).toBe(200);
    expect((await json(`/operators/${operator.id}`, "DELETE")).status).toBe(204);

    // retention periods and an erasure, each put back or harmless
    expect(
      (await json("/privacy/retention", "PUT", { changes: [{ category: "accounting", days: 60 }] })).status,
    ).toBe(200);
    expect(
      (await json("/privacy/retention", "PUT", { changes: [{ category: "accounting", days: null }] })).status,
    ).toBe(200);
    expect(
      (await json("/privacy/erasures", "POST", { subject: { kind: "mac", mac: "02-00-5E-10-00-97" } })).status,
    ).toBe(200);

    // the network tag: a settings write, a certificate removal (harmless when none
    // is stored), a manual sync and an apply
    const net = (await (await json("/network/settings", "GET")).json()) as NetworkSettings;
    expect(
      (await json("/network/settings", "PUT", {
        portalUrl: null,
        venueUrl: null,
        throttles: net.throttles,
        bodyCeilings: net.bodyCeilings,
      })).status,
    ).toBe(200);
    expect((await json("/network/portal-certificate", "DELETE")).status).toBe(200);
    expect((await json("/network/radius/sync", "POST")).status).toBe(200);
    expect((await json("/network/radius/apply", "POST")).status).toBe(200);

    // the maintenance schedule, put back to the defaults afterwards
    const defaults = { intervalSeconds: 300, staleAfterMissedUpdates: 4, staleCeilingPerPass: 200, retentionBatchSize: 500 };
    expect((await json("/privacy/maintenance", "PUT", { ...defaults, intervalSeconds: 600 })).status).toBe(200);
    expect((await json("/privacy/maintenance", "PUT", defaults)).status).toBe(200);

    // an account opted in to 802.1X with its password, then out again
    const guestAcc = (await (
      await json("/accounts", "POST", {
        siteId: "site-hanoi",
        username: `dot1x-${stamp}`,
        password: "a-long-enough-password",
        eapTtlsPap: true,
      })
    ).json()) as Account;
    expect(guestAcc.credentialKind).toBe("argon2id+bcrypt");
    expect((await json(`/accounts/${guestAcc.id}`, "PATCH", { eapTtlsPap: false })).status).toBe(200);
    expect((await json(`/accounts/${guestAcc.id}`, "DELETE")).status).toBe(204);

    // guest data and a marketing consent withdrawn on a guest's behalf
    expect(
      (await json("/sites/site-hanoi/guest-data", "PUT", {
        fields: [{ field: "email", required: false }],
        marketingConsent: { enabled: true, text: { en: `Drive ${stamp}` } },
      })).status,
    ).toBe(200);
    keepGuestInput("site-hanoi", "02:00:5e:10:00:96", null, {
      details: { email: `drive.${stamp}@example.test` },
      consentVersion: consentVersion(`Drive ${stamp}`),
    });
    const given = marketingConsents("site-hanoi")[0]!;
    expect((await json(`/sites/site-hanoi/marketing-consents/${given.id}/withdraw`, "POST")).status).toBe(200);

    // a site's calendar and the deployment-wide SMS cap (admin-configurability rule),
    // each put back
    expect((await json("/sites/site-hanoi/time-zone", "PUT", { timeZone: "Asia/Bangkok" })).status).toBe(200);
    expect((await json("/sites/site-hanoi/time-zone", "PUT", { timeZone: "Asia/Ho_Chi_Minh" })).status).toBe(200);
    expect((await json("/phone-sign-in/spend-cap", "PUT", { dailyLimit: 500 })).status).toBe(200);
    expect((await json("/phone-sign-in/spend-cap", "PUT", { dailyLimit: null })).status).toBe(200);
    expect(
      (await json("/auth/password", "POST", {
        currentPassword: DEV_PASSWORD,
        newPassword: "a-different-long-password",
      })).status,
    ).toBe(204);
    /* And back, in the same drive. The change reaches the *credential* and not only
       the session, so leaving it changed would sign the rest of this file
       out — a sweep that mutates a credential has to put it back or it is a
       dependency between tests that nothing declares. */
    expect(
      (await json("/auth/password", "POST", {
        currentPassword: "a-different-long-password",
        newPassword: DEV_PASSWORD,
      })).status,
    ).toBe(204);

    const written = new Set<string>(world.audit.filter((e) => !before.has(e.id)).map((e) => e.kind));
    const declared = (contractDocument().components.schemas.AuditKind as { enum: string[] }).enum;
    expect(
      declared.filter((k) => !written.has(k) && !unreachable.includes(k)),
      "In `AuditKind` and no drive above writes it, so nothing proves the server ever " +
        "records it. Drive it, or take it out of the contract.",
    ).toEqual([]);
    /* And an excuse that stopped being one. A kind this world *can* reach must not
       stay on the unreachable list: the branch that put it there is an `else`, so a
       name appearing in both sets means the condition was evaluated and ignored. */
    expect(
      unreachable.filter((k) => written.has(k)),
      "a kind was excused as unreachable and then driven anyway",
    ).toEqual([]);

    /*
     * **And every one of them decided whether it happened in a venue.**
     *
     * `AuditEntry.siteId` is optional in the schema, so an entry that simply omits it
     * compiles, validates and renders. A server that omitted it on **every** entry
     * would make `audit_log.site_id` uniformly null — and a uniform null cannot be
     * told apart from the honest one the contract describes: *"null for a change that
     * is not a site's"*. This mock sets the member, and this is what keeps it the
     * oracle rather than leaving it a habit.
     *
     * The assertion is on the **key**, not the value. `null` is a decision and passes;
     * an absent member is the absence of one and does not. Asserting the value would
     * mean copying the per-kind rule into this file, and the rule belongs to whoever
     * writes the entry.
     */
    const fresh = world.audit.filter((e) => !before.has(e.id));
    expect(
      fresh.filter((e) => !("siteId" in e)).map((e) => e.kind),
      "wrote an entry with no `siteId` member at all — the reader cannot tell a change " +
        "that has no venue from a venue nobody recorded",
    ).toEqual([]);
    /* A venue that is really a venue. A string here that names no site renders as a
       raw id in the audit screen's site column, which is what `Audit.tsx` falls back
       to when the switcher cannot resolve one. */
    const venues = new Set(world.sites.map((s) => s.id));
    expect(
      fresh.filter((e) => e.siteId != null && !venues.has(e.siteId)).map((e) => e.siteId),
      "an entry names a site this deployment does not have",
    ).toEqual([]);
    /* Both families, or the two assertions above are satisfied by a server that
       answers one of them for everything — all-null hides every venue, and all-site
       files the creation of an operator under a café. */
    expect(
      fresh.some((e) => e.siteId != null),
      "not one entry in a full sweep names a venue, so no audit row can ever answer a site scope",
    ).toBe(true);
    expect(
      fresh.some((e) => e.siteId === null),
      "every entry in a full sweep names a venue, so a change belonging to none was given one",
    ).toBe(true);
  });

  it("writes nothing at all when nobody is signed in", async () => {
    /*
     * The other half of the enforcement switch, and the reason it is not a hole. An
     * audit entry is a claim about *who*; with no session there is no who, and
     * attributing an unauthenticated write to the fixture administrator would put a
     * name in the log beside something they did not do. When the switch is thrown this
     * branch stops being reachable rather than starting to be wrong.
     */
    const before = world.audit.length;
    const created = await send("/sites/site-hcm/walled-garden", {
      method: "POST",
      body: JSON.stringify({ kind: "fqdn", value: `anon-${Date.now().toString(36)}.example.test` }),
    });
    expect(created.status).toBe(201);
    const entry = (await created.json()) as GardenEntry;
    expect((await send(`/sites/site-hcm/walled-garden/${entry.id}`, { method: "DELETE" })).status).toBe(204);
    expect(world.audit.length, "an unauthenticated write was attributed to somebody").toBe(before);
  });

  it("never records the secret it says was rotated", async () => {
    /* An audit entry that carried the new shared secret would be a credential store
       with a read operation, no expiry, and an administrator-only screen rendering it.
       `docs/rules/security.md` §3 — secrets are write-only in the admin API. */
    const cookie = await signInAs(ADMINISTRATOR_USERNAME);
    const rotatable = world.nas.find((n) => !rotationRefused(n))!;
    const res = await send(`/nas/${rotatable.id}/secret`, { method: "POST", headers: { cookie } });
    const { sharedSecret } = (await res.json()) as { sharedSecret: string };
    expect(sharedSecret.length).toBeGreaterThan(8);

    const log = await send("/audit?limit=200");
    expect(await log.text()).not.toContain(sharedSecret);
  });
});

/**
 * The site scope is a boundary, and this block is what can tell.
 *
 * The risk is not only that a handler is wrong — it is that **the world cannot
 * distinguish a handler that scopes from one that does not** if every session,
 * account, device and voucher batch lives in one of the three sites. A dropped
 * `siteId` then changes no answer, so a correct implementation and a leaking one
 * produce identical bytes, and every mechanism this project owns agrees with both:
 * the schema-validating mock, the E2E suite, the differential harness, and every
 * review.
 *
 * The same class of defect exists one layer down, in an adapter's SQL, where only
 * reading the query or a populated second site will show it.
 *
 * The operations are **read out of `openapi.yaml`**, not listed here. An operation
 * that gains the site scope tomorrow joins this block on its own; one that loses it
 * leaves. A hardcoded list is the shape of a check that stops covering what it
 * claims the first time somebody adds an endpoint.
 */
describe("a site scope is a boundary, not a decoration", () => {
  /** Every GET that declares the shared `SiteId` *query* parameter. */
  function siteScopedOperations(): { operationId: string; path: string }[] {
    const doc = contractDocument();
    const out: { operationId: string; path: string }[] = [];
    for (const [path, item] of Object.entries(doc.paths)) {
      const op = item.get as
        | { operationId?: string; parameters?: { $ref?: string }[] }
        | undefined;
      if (!op?.operationId) continue;
      const scoped = (op.parameters ?? []).some(
        (p) => p?.$ref === "#/components/parameters/SiteId",
      );
      if (!scoped) continue;
      /* Every one of these is a collection root today. If that stops being true the
         request below would fetch a literal `{siteId}`, so it is asserted rather
         than assumed. */
      expect(
        path,
        `${op.operationId} has a path parameter this block cannot fill`,
      ).not.toContain("{");
      out.push({ operationId: op.operationId, path });
    }
    return out;
  }

  /** Whatever rows an answer carries, however it carries them. */
  function rowsOf(body: unknown): { id?: string }[] | null {
    if (Array.isArray(body)) return body as { id?: string }[];
    const items = (body as { items?: unknown })?.items;
    return Array.isArray(items) ? (items as { id?: string }[]) : null;
  }

  /**
   * The `reports` exports answer CSV, and a row's identity is its first column —
   * `sessionId`, `voucherId`, `grantId`. Parsed here into the shape `rowsOf` reads,
   * so the assertions below hold an export to the same boundary as a list.
   */
  function csvRows(text: string): { id?: string }[] {
    const records: string[][] = [];
    let row: string[] = [];
    let cell = "";
    let quoted = false;
    const body = text.startsWith("\uFEFF") ? text.slice(1) : text;
    for (let i = 0; i < body.length; i++) {
      const ch = body[i]!;
      if (quoted) {
        if (ch === '"' && body[i + 1] === '"') {
          cell += '"';
          i++;
        } else if (ch === '"') quoted = false;
        else cell += ch;
      } else if (ch === '"') quoted = true;
      else if (ch === ",") {
        row.push(cell);
        cell = "";
      } else if (ch === "\r" && body[i + 1] === "\n") {
        row.push(cell);
        records.push(row);
        row = [];
        cell = "";
        i++;
      } else cell += ch;
    }
    const [header, ...rest] = records;
    return rest.map((r) => ({ ...Object.fromEntries(header!.map((h, i) => [h, r[i]])), id: r[0] }));
  }

  /**
   * One guest let on in each venue, so the guest export has rows to be scoped. A
   * world where both sites' grant lists are empty would answer "identical" for the
   * wrong reason — empty differs from populated as convincingly as correct does.
   */
  beforeAll(() => {
    for (const siteId of ["site-hcm", "site-hanoi"]) {
      const nas = world.nas.find((n) => n.siteId === siteId && n.reachability === "ok")!;
      const client = newPortalClient({ present: true, nasId: nas.id })!;
      expect(acceptTerms(client, 60).ok, `no grant could be made in ${siteId}`).toBe(true);
      /* And one ticked marketing box per venue, so the consent export has rows to
         be scoped. */
      putGuestDataSettings(siteId, {
        fields: [{ field: "email", required: false }],
        marketingConsent: { enabled: true, text: { en: `Offers from ${siteId}` } },
      });
      keepGuestInput(siteId, client.mac, client.grantId ?? null, {
        details: { email: `guest@${siteId}.example.test` },
        consentVersion: consentVersion(`Offers from ${siteId}`),
      });
    }
  });

  const ask = async (path: string, siteId: string) => {
    /* A bounded range for the operations that require one: the last week. */
    const op = (contractDocument().paths[path] as { get?: { parameters?: { $ref?: string }[] } })
      .get;
    const ranged = op?.parameters?.some((p) => p.$ref === "#/components/parameters/ExportFrom");
    const range = ranged
      ? `&from=${new Date(Date.now() - 7 * 86_400_000).toISOString()}` +
        `&to=${new Date(Date.now() + 3_600_000).toISOString()}`
      : "";
    const res = await app.request(`/api/v0${path}?siteId=${siteId}&limit=200${range}`);
    expect(res.status, `${path} ${siteId}`).toBe(200);
    return res.headers.get("content-type")?.startsWith("text/csv")
      ? csvRows(await res.text())
      : res.json();
  };

  it("declares more than one operation to check, or this block asks nothing", () => {
    expect(siteScopedOperations().length).toBeGreaterThan(1);
  });

  it("answers two sites differently on every operation that declares the scope", async () => {
    for (const { operationId, path } of siteScopedOperations()) {
      const hcm = await ask(path, "site-hcm");
      const hanoi = await ask(path, "site-hanoi");
      expect(
        JSON.stringify(hcm),
        `${operationId} gives the same answer for two different sites, so nothing ` +
          `about this operation would change if it stopped filtering by site at all`,
      ).not.toBe(JSON.stringify(hanoi));
    }
  });

  it("returns no row that names a different site", async () => {
    /* The exact assertion, and it is deliberately not "the two answers are
       disjoint": a controller with an unadopted vendor site belongs to no Foyer site
       and is listed under all of them, which is correct and is what makes adoption
       possible at all. A row that *names* a site and names the wrong one is a leak
       under every reading. */
    for (const { operationId, path } of siteScopedOperations()) {
      for (const siteId of ["site-hcm", "site-hanoi"]) {
        for (const row of rowsOf(await ask(path, siteId)) ?? []) {
          const named = (row as { siteId?: unknown }).siteId;
          if (typeof named !== "string") continue;
          expect(
            named,
            `${operationId} answered ${siteId} with a row belonging to ${named}. ` +
              `That is a scope leak: the operator is shown another venue's guests, ` +
              `codes or devices, beside the controls that act on them.`,
          ).toBe(siteId);
        }
      }
    }
  });

  it("gives each site rows the other does not have", async () => {
    /* Without this, the assertion above is satisfied by a world in which one of the
       two sites is empty — which is the world this whole block guards against.
       Sets rather than counts, because the suite creates devices as it runs. */
    for (const { operationId, path } of siteScopedOperations()) {
      const hcm = rowsOf(await ask(path, "site-hcm"));
      const hanoi = rowsOf(await ask(path, "site-hanoi"));
      if (!hcm || !hanoi) continue; // `getOverview` is a set of figures, not a list

      const there = new Set(hanoi.map((r) => r.id));
      expect(
        hcm.some((r) => !there.has(r.id)),
        `every row ${operationId} answers for site-hcm is also answered for ` +
          `site-hanoi, so this operation would look identical with no site filter ` +
          `at all`,
      ).toBe(true);
    }
  });

  /**
   * `getOverview` is a set of figures, and a figure is where a dropped scope hides
   * best.
   *
   * The two tests above cannot see one. `getOverview` answers an object rather than
   * a list, so the disjointness check skips it, and *"the two answers differ"* is
   * satisfied by **any** one figure differing — which five of the six do. A sixth
   * that read every voucher in the deployment, as an unscoped `vouchersRedeemedToday`
   * would, makes the hotel's dashboard report the café's redemptions as its own: `61`
   * under both sites, beside `2 clients online` and `2/2 access points`.
   *
   * Opening the second site's dashboard in a browser shows it at once; *"the bodies
   * differ"* is the shape of a check that is not one.
   *
   * The invariant below is not a copy of the handler's arithmetic, which would only
   * assert that two identical mistakes agree. **Each of these counts partitions the
   * world**: a session, a device or a voucher belongs to exactly one site, so summing
   * a figure across every site must give back the number of rows in the world it
   * counts. An unscoped figure is counted once per site and the sum overshoots.
   *
   * `alarmCount` is deliberately absent: alarms do **not** partition, because a
   * controller with an unadopted vendor site raises in every site that lists it. A
   * figure that legitimately double-counts cannot be checked this way, and pretending
   * otherwise would make this test fail for a correct reason.
   */
  it("does not report the deployment's figures as one site's", async () => {
    const sites = (await (await app.request("/api/v0/sites")).json()) as { id: string }[];
    expect(sites.length).toBeGreaterThan(1);

    const siteOfBatch = new Map(world.batches.map((b) => [b.id, b.siteId]));
    /* "Today" is each voucher's own site's calendar day, as the server counts it. */
    const redeemedToday = (v: { firstUsedAt?: string | null; batchId: string }) => {
      const siteId = world.batches.find((b) => b.id === v.batchId)?.siteId ?? "";
      return (
        Boolean(v.firstUsedAt) &&
        Date.parse(v.firstUsedAt!) <= Date.now() &&
        siteDay(siteId, Date.parse(v.firstUsedAt!)) === siteDay(siteId, Date.now())
      );
    };

    /* The world's own totals, over rows rather than over answers.
     *
     * **Through `withCurrentFreshness`, because `Session.state` is derived at read
     * time** — `SessionState` has a threshold and this server does not serve
     * whatever the fixtures authored. Counting the authored value here would
     * compare *the world as it was built* against *the world as it is answered
     * about*, which is a different claim and one that goes wrong the moment the
     * clock moves. The scope invariant below is about **which site a row is in** and
     * nothing else, so it must be measured on the same rows the handler answers
     * from. */
    const derived = world.sessions.map(withCurrentFreshness);
    const whole: Record<string, number> = {
      clientsOnline: derived.filter((s) => s.state === "online").length,
      nasTotal: world.nas.length,
      nasUnhealthy: world.nas.filter((n) => n.reachability !== "ok").length,
      orphanedSessions: derived.filter((s) => s.state === "orphaned").length,
      vouchersRedeemedToday: world.vouchers.filter(
        (v) => redeemedToday(v) && siteOfBatch.has(v.batchId),
      ).length,
    };

    /* Three of the six are `Metric` objects and three are bare integers, which is
       the contract's shape and not something to normalise away here. This helper
       reads either shape, so a figure that moves to the `Metric` side — as
       `orphanedSessions` did — keeps the partition invariant below holding without
       a second copy of it. */
    const figure = (v: unknown) =>
      typeof v === "number" ? v : ((v as { value: number }).value ?? Number.NaN);

    const summed: Record<string, number> = {};
    for (const s of sites) {
      const res = await app.request(`/api/v0/overview?siteId=${s.id}`);
      expect(res.status).toBe(200);
      const o = (await res.json()) as Record<string, unknown>;
      for (const key of Object.keys(whole)) summed[key] = (summed[key] ?? 0) + figure(o[key]);
    }

    for (const [key, total] of Object.entries(whole)) {
      expect(
        summed[key],
        `\`${key}\` summed over every site comes to ${summed[key]} where the whole ` +
          `deployment holds ${total}. Each of these counts partitions the world, so a ` +
          `sum that overshoots means the figure is being answered without its site ` +
          `scope — the dashboard of one venue reporting another venue's numbers.`,
      ).toBe(total);
    }
  });

  /**
   * The one exception, stated rather than skipped past.
   *
   * A controller is not scoped to a Foyer site — the binding is per *vendor* site,
   * and a controller nothing has adopted yet has to appear wherever the operator is
   * looking or it can never be adopted. `deriveAlarms` reads the same predicate as
   * the list — literally the same function, `controllerServesSite` — so an alarm
   * always has a row to act on.
   *
   * **Two halves, pointing opposite ways.** Either alone is satisfied by swapping
   * one defect for the other:
   *
   *   * **probed, adopted by nobody** — such a controller must be listed
   *     *everywhere*. A rule that asked `vendorSites.length === 0` would, once the
   *     probe records a controller's own sites, make probing one remove it from
   *     every site view, including the one carrying the action that binds it.
   *   * **adopted, one vendor site free** — a controller adopted into the café must
   *     *not* be listed under the hotel merely because one of its vendor sites is
   *     free. A rule that asked `some(v => !v.boundSiteId)` would put `ctl-unifi` —
   *     adopted into `site-hcm`, `q7-branch` unadopted — on the hotel's screen beside
   *     *Delete* and *Discover access points*.
   *
   * The rows are found in the world rather than named, and the search failing is an
   * assertion: a world where no controller has two vendor sites with different
   * bindings cannot tell the two rules apart, and every predicate above answers it
   * identically.
   */
  it("scopes a controller by its binding, not by its probe and not per vendor site", async () => {
    type Ctl = { id: string; vendorSites: { boundSiteId?: string | null }[] };
    const listed = async (siteId: string) => (await ask("/controllers", siteId)) as Ctl[];
    const idsIn = async (siteId: string) => (await listed(siteId)).map((c) => c.id);

    const hcm = await idsIn("site-hcm");
    const hanoi = await idsIn("site-hanoi");

    const boundTo = (c: Ctl, siteId: string) => c.vendorSites.some((v) => v.boundSiteId === siteId);
    const boundAnywhere = (c: Ctl) => c.vendorSites.some((v) => Boolean(v.boundSiteId));

    // ── adopted with a vendor site free, found rather than named ──────────
    const partly = world.controllers.filter(
      (c) => boundAnywhere(c) && c.vendorSites.some((v) => !v.boundSiteId),
    );
    expect(
      partly.length,
      "no controller in this world is adopted into one site with a second vendor site free, so " +
        "every plausible predicate answers this world identically and " +
        "asserting anything about the rule is vacuous",
    ).toBeGreaterThan(0);
    for (const c of partly) {
      const home = c.vendorSites.find((v) => v.boundSiteId)!.boundSiteId!;
      const elsewhere = home === "site-hcm" ? "site-hanoi" : "site-hcm";
      expect(
        await idsIn(elsewhere),
        `${c.id} is adopted into ${home} and was listed under ${elsewhere} because one of its ` +
          `vendor sites is unadopted, and ${elsewhere}'s operator can delete it`,
      ).not.toContain(c.id);
      expect(await idsIn(home), `${c.id} is missing from its own site`).toContain(c.id);
    }

    // ── nothing adopted elsewhere is ever listed here ─────────────────────
    for (const [siteId, rows] of [
      ["site-hcm", await listed("site-hcm")],
      ["site-hanoi", await listed("site-hanoi")],
    ] as const) {
      for (const c of rows) {
        expect(
          boundAnywhere(c) && !boundTo(c, siteId),
          `${c.id} is adopted into another site and was listed under ${siteId}`,
        ).toBe(false);
      }
    }

    const adopted = world.controllers.filter((c) => boundTo(c, "site-hanoi"));
    expect(adopted.length, "no controller is adopted into site-hanoi any more").toBeGreaterThan(0);
    for (const c of adopted) {
      expect(hanoi, `${c.id} is missing from its own site`).toContain(c.id);
      expect(hcm, `${c.id} is adopted elsewhere and shows in site-hcm`).not.toContain(c.id);
    }

    // ── bound to nothing is listed everywhere ─────────────────────────────
    //
    // **Two shapes, and `ctl-unifi-unattached` is what makes this world hold both.**
    // Bound to nothing arrives two ways — a site list nothing has read, and a site
    // list that was read and adopted by nobody — and the wrong rule
    // (`vendorSites.length === 0`) answers the *first* correctly. So a world whose
    // only unadopted controller is `ctl-ruckus` is answered identically by the wrong
    // rule and the right one, and this branch is green either way: the fixture
    // world could not fail, and the drive below it would carry the whole claim on
    // its own.
    //
    // The partition is therefore asserted before the loop rather than hoped for.
    // It is the same guard `docs/rules/frontend.md` records for the second site,
    // which can collapse to *present but empty* and leave its assertion green.
    //
    // **And it is asserted over `buildWorld()` rather than over the live `world`,
    // which is the sharper half.** `world` is this process's mock state and every
    // drive in this file mutates it: *establishes nothing about a vendor with no
    // floor*, several hundred lines above, registers a Ruckus and probes it, and the
    // probe writes `[{ id: "default", boundSiteId: null }]` into a controller whose
    // site list is empty. So by the time this runs, the live world holds a probed,
    // unbound controller **as residue from a sibling test** — and the guard passes
    // for a reason that has nothing to do with the fixtures.
    //
    // Measured, both ways, with `ctl-unifi-unattached` deleted: this test is **red
    // run alone and green in the full file**. `docs/rules/testing.md` records
    // isolated-green-and-in-suite-red as a timing signature; this is its mirror, and
    // the cause is a shared mutable world rather than a clock. An assertion about
    // what the fixtures can express has to be made against the fixtures.
    const authored = buildWorld().controllers;
    const unadopted = world.controllers.filter((c) => !boundAnywhere(c));
    expect(
      unadopted.length,
      "every controller is adopted, so the branch that lists one everywhere is gone",
    ).toBeGreaterThan(0);
    expect(
      authored.filter((c) => c.vendorSites.length === 0).map((c) => c.id),
      "no controller in `@foyer/fixtures` has been registered and never probed, so the " +
        "first way of being bound to nothing is gone from the authored world",
    ).not.toEqual([]);
    expect(
      authored.filter((c) => !boundAnywhere(c) && c.vendorSites.length > 0).map((c) => c.id),
      "every controller in `@foyer/fixtures` that is bound to nothing has an empty " +
        "vendor-site list, so this branch is answered the same way by the wrong predicate " +
        "(`vendorSites.length === 0`) — the authored world cannot fail this test, " +
        "and any row that does is residue from a drive earlier in this file",
    ).not.toEqual([]);
    for (const c of unadopted) {
      expect(hcm, `${c.id} is adopted by nobody and is invisible`).toContain(c.id);
      expect(hanoi, `${c.id} is adopted by nobody and is invisible`).toContain(c.id);
    }
  });

  /**
   * Probed and adopted by nobody, driven — and the half a fixture row cannot carry.
   *
   * `ctl-unifi-unattached` holds the *state*, so the assertions above are not
   * answered identically by the wrong rule. What it cannot hold is the **sequence**,
   * which is where the failure would happen: register a controller, probe it, and
   * watch it leave the screen. A seeded row says the destination is listed; only a
   * drive says that `probeController` — the one call that records a controller's own
   * sites, and therefore the one that creates this state in the product — is what
   * takes you there.
   *
   * This is the same division the below-floor controller's pair already draws on
   * this screen: a fixture for the state, a drive for the transition that produces
   * it, because a probe that healed the row would satisfy the fixture and break the
   * product.
   */
  it("keeps a controller on every site's screen after probing it, before anything is adopted", async () => {
    const cookie = await signInAs(ADMINISTRATOR_USERNAME);
    const created = (await (
      await send("/controllers", {
        method: "POST",
        headers: { cookie },
        body: JSON.stringify({
          name: `Probed unbound ${Date.now().toString(36)}`,
          vendor: "unifi",
          baseUrl: "https://10.20.9.7:8443",
          authMode: "api-key",
          tlsMode: "verify",
          apiKey: "k",
        }),
      })
    ).json()) as { id: string };

    const idsIn = async (siteId: string) =>
      ((await ask("/controllers", siteId)) as { id: string }[]).map((c) => c.id);

    expect(await idsIn("site-hcm"), "a registered controller is not listed at all").toContain(created.id);
    expect(await idsIn("site-hanoi"), "a registered controller is listed in only one site").toContain(created.id);

    const probed = (await (
      await send(`/controllers/${created.id}/probe`, { method: "POST", headers: { cookie } })
    ).json()) as { vendorSites: { id: string }[] };
    expect(
      probed.vendorSites.length,
      "the probe recorded no vendor sites, so this drive cannot reach the probed-unbound state",
    ).toBeGreaterThan(0);

    for (const siteId of ["site-hcm", "site-hanoi"]) {
      expect(
        await idsIn(siteId),
        `probing removed ${created.id} from ${siteId}, and the action that would adopt it ` +
          `lives on the screen it just left`,
      ).toContain(created.id);
    }
  });

  /**
   * And the two plausible wrong rules, transcribed and asked the same
   * two questions — so the world is shown to separate them rather than assumed to.
   *
   * Each gets exactly one of the pair wrong, and the test names which. A variant
   * failing for some third reason would otherwise look like evidence, which is why
   * the half it gets *right* is asserted too.
   */
  it("separates the two plausible wrong predicates", () => {
    type Ctl = { id: string; vendorSites: { boundSiteId?: string | null }[] };

    /* `buildWorld()` and not the live `world`: this test asks what **the authored
       fixtures** can express, and the live world has been mutated by every drive
       above it — one of which registers a controller and probes it, leaving exactly
       the probed-unbound shape behind. Searching there would report the fixtures as expressive
       on the strength of another test's residue. */
    const authored = buildWorld().controllers;

    const partly = authored.find(
      (c) => c.vendorSites.some((v) => v.boundSiteId) && c.vendorSites.some((v) => !v.boundSiteId),
    );
    expect(partly, "no partly adopted controller, so the per-vendor-site rule cannot be told apart here").toBeDefined();
    const home = partly!.vendorSites.find((v) => v.boundSiteId)!.boundSiteId!;
    const elsewhere = home === "site-hcm" ? "site-hanoi" : "site-hcm";

    // The probed-unbound state, **found in the world rather than written out here.**
    //
    // A literal — `{ id: "ctl-probed-unbound", vendorSites: [{ boundSiteId: null }] }`
    // — would be a stand-in for a rule whose authority sits one import away, and
    // `docs/rules/database.md` has the section: a second copy passes forever,
    // still printing its reason, describing a world that has moved. A hand-written
    // subject also makes the variants below unfalsifiable from the fixtures — they
    // would keep separating two rules against an object no screen ever renders.
    const probedUnbound = authored.find(
      (c) => c.vendorSites.length > 0 && c.vendorSites.every((v) => !v.boundSiteId),
    ) as Ctl | undefined;
    expect(
      probedUnbound,
      "no controller in `@foyer/fixtures` has been probed and adopted by nobody, so the " +
        "probe-shaped rule has no subject here and the variant below is separating two rules against nothing",
    ).toBeDefined();

    const variants = [
      {
        name: "probe-shaped (vendorSites.length === 0)",
        serves: (c: Ctl, siteId: string) =>
          c.vendorSites.length === 0 || c.vendorSites.some((v) => v.boundSiteId === siteId),
        wrong: { subject: probedUnbound!, siteId: elsewhere, product: true },
        right: { subject: partly as unknown as Ctl, siteId: elsewhere, product: false },
      },
      {
        name: "per-vendor-site (some vendor site unbound)",
        serves: (c: Ctl, siteId: string) =>
          c.vendorSites.length === 0 ||
          c.vendorSites.some((v) => !v.boundSiteId || v.boundSiteId === siteId),
        wrong: { subject: partly as unknown as Ctl, siteId: elsewhere, product: false },
        right: { subject: probedUnbound!, siteId: elsewhere, product: true },
      },
    ];

    for (const v of variants) {
      expect(
        v.serves(v.wrong.subject, v.wrong.siteId),
        `${v.name} gives the same answer for ${v.wrong.subject.id} under ${v.wrong.siteId} as the ` +
          `server does, so this world does not separate that rule from the one replacing it`,
      ).not.toBe(v.wrong.product);
      expect(
        v.serves(v.right.subject, v.right.siteId),
        `${v.name} is wrong about ${v.right.subject.id} too, which is not the half it was ` +
          `supposed to get right — the transcription is wrong`,
      ).toBe(v.right.product);
    }
  });

  /**
   * And the sharp case, because disjoint ids are satisfied by two worlds that share
   * no identifier at all.
   *
   * One MAC is present in both sites — a laptop that visits both venues and does not
   * rotate its address. Searching it is what an operator does with a guest standing
   * in front of them, and the site scope is the entire difference between *your two
   * sessions* and *somebody else's guest*.
   */
  it("keeps the address that exists in two sites on the right side of the scope", async () => {
    const shared = world.sessions.find((s) => s.id === SESSION_IDS.crossSiteMac);
    expect(shared, "the cross-site MAC fixture is gone, and with it this whole case").toBeTruthy();
    const mac = shared!.callingStationId;

    const elsewhere = world.sessions.filter(
      (s) => s.callingStationId === mac && s.siteId !== shared!.siteId,
    );
    expect(
      elsewhere.length,
      "the address is no longer shared between two sites, so searching it cannot " +
        "show a scope being honoured or dropped",
    ).toBeGreaterThan(0);

    for (const siteId of [shared!.siteId, elsewhere[0]!.siteId]) {
      const res = await app.request(
        `/api/v0/sessions?siteId=${siteId}&mac=${encodeURIComponent(mac)}&limit=200`,
      );
      expect(res.status).toBe(200);
      const { items } = (await res.json()) as { items: { siteId: string }[] };
      expect(items.length, `no session for ${mac} under ${siteId}`).toBeGreaterThan(0);
      for (const s of items) expect(s.siteId, `a ${s.siteId} row answered ${siteId}`).toBe(siteId);
    }
  });

  /**
   * `listVouchers` had the defect, and it had it in the contract's own terms.
   *
   * `siteId` is a **required** parameter on that operation and the handler ignored
   * it — for as long as the fixture world made that unobservable. A voucher code is
   * a bearer credential and the console puts *Revoke* one column from the search
   * result, so "does this code work?" resolving to another business's room key is
   * not a cosmetic leak.
   */
  it("does not answer one site's voucher search with another site's codes", async () => {
    const elsewhere = world.batches.find((b) => b.siteId !== "site-hcm");
    expect(elsewhere, "every batch is in one site again, so this asks nothing").toBeTruthy();
    const code = world.vouchers.find((v) => v.batchId === elsewhere!.id)!.code;

    const res = await app.request(
      `/api/v0/vouchers?siteId=site-hcm&q=${encodeURIComponent(code)}&limit=200`,
    );
    expect(res.status).toBe(200);
    const { items } = (await res.json()) as { items: { id: string }[] };
    expect(
      items,
      `a code from ${elsewhere!.siteId} resolved under site-hcm, which is an ` +
        `operator redeeming or revoking a voucher that is not theirs`,
    ).toEqual([]);
  });
});

/**
 * `createSite` in an install that already has a venue — which, in this world, is
 * every drive.
 *
 * **The `201` is not in this file, and that is the point.** One venue per install
 * means `createSite` refuses the second call, and the fixture world has three
 * sites. There is no drive of THIS world that can reach the success branch, so
 * pretending otherwise here would be a test asserting a response this server cannot
 * produce.
 *
 * It lives in `first-run.test.ts`, against `FOYER_MOCK_FRESH=1` — the migrated,
 * first-booted, zero-site deployment the refusal makes necessary.
 */
describe("createSite in a deployment that already has a venue", () => {
  it("refuses the second venue, and not as a name collision", async () => {
    const refused = await send("/sites", {
      method: "POST",
      body: JSON.stringify({ name: `Second venue ${Date.now().toString(36)}` }),
    });
    expect(refused.status).toBe(409);
    const body = (await refused.json()) as { type: string };
    /*
     * NOT `/problems/duplicate`. Nothing collided — the name above is unique — and a
     * client rendering *"choose another name"* would send the operator back to the
     * form to do something that cannot work. The two types differ because what the
     * operator does next differs.
     */
    expect(body.type).toBe("/problems/one-site-per-install");
  });

  it("refuses a blank name before it refuses the venue limit", async () => {
    /*
     * Ordering, asserted rather than assumed. An operator who submitted an empty
     * form must be told the field is required; being told about the venue limit
     * instead is the right rejection for the wrong reason, and it is a one-line
     * mistake to make.
     */
    const refused = await send("/sites", { method: "POST", body: JSON.stringify({ name: "  " }) });
    expect(refused.status).toBe(422);
    const body = (await refused.json()) as { params: { fields: Record<string, string> } };
    expect(body.params.fields.name).toBe("required");
  });
});

/**
 * `getNasEvidence` — every CoA-family capability ships UNVERIFIED, and this server
 * answers with that label.
 *
 * The tables are GENERATED from `internal/nas`, so what this file asserts is the
 * *route*: that it resolves the vendor the way Go does, that it answers a
 * vendor-less `coa` row with a state rather than an empty shrug, and that the
 * response conforms to the schema.
 *
 * It deliberately does not assert the contents of a note. Those come from the
 * generated table and a copy of one here would be the stale second copy the
 * generator exists to prevent.
 */
describe("getNasEvidence", () => {
  const anyOf = (driver: string, coaVendor?: string) =>
    world.nas.find((n) => n.driver === driver && (coaVendor === undefined || n.coaVendor === coaVendor));

  it("answers a device with the evidence its vendor profile carries", async () => {
    const device = world.nas.find((n) => VENDOR_BY_DRIVER[n.driver === "coa" ? `coa:${n.coaVendor ?? ""}` : n.driver]);
    expect(device, "no fixture device resolves to a vendor profile").toBeTruthy();

    const res = await send(`/nas/${device!.id}/evidence`);
    expect(res.status).toBe(200);
    const body = (await res.json()) as {
      profile: string;
      capabilities: { subject: string; evidence: string; note: string }[];
      attributes: { subject: string; evidence: string; note: string }[];
    };
    expect(body.profile).toBe("resolved");
    expect(body.attributes.length).toBeGreaterThan(0);
    /* Every fact carries a note. An unverified fact with no named measurement is a
       guess wearing a label, and the note is the only thing on that screen an
       operator can act on. */
    for (const f of [...body.capabilities, ...body.attributes]) {
      expect(f.note, `${f.subject} has no note`).not.toBe("");
      expect(["measured", "documented", "unverified"]).toContain(f.evidence);
    }
  });

  /*
   * The two lists disagree, and this server must not smooth that over. Asserted on
   * a driver the fixtures actually carry rather than one typed here.
   */
  it("the standings differ between the two lists, because the facts do", async () => {
    const device = anyOf("mikrotik");
    expect(device, "no MikroTik device in the fixture world").toBeTruthy();
    const body = (await (await send(`/nas/${device!.id}/evidence`)).json()) as {
      capabilities: { evidence: string }[];
      attributes: { evidence: string }[];
    };
    expect(new Set(body.capabilities.map((c) => c.evidence))).toEqual(new Set(["unverified"]));
    const attrStandings = new Set(body.attributes.map((a) => a.evidence));
    expect(attrStandings.has("documented"), "no attribute is documented").toBe(true);
  });

  it("a coa row with no vendor is answered, and says why it is empty", async () => {
    /* Constructed rather than found: the fixtures may or may not carry one, and a
       test that skipped when they did not would be a branch that never runs. */
    const created = await send("/nas", {
      method: "POST",
      body: JSON.stringify({
        siteId: "site-hcm",
        name: "Unassigned CoA",
        driver: "coa",
        ipAddress: "10.91.0.7",
      }),
    });
    /* `coaVendor` is required for a `coa` row at registration — the refusal that
       makes "nobody said which vendor" representable. So the case this asserts is
       reached through a row that predates the field, which is what the mock's
       fixtures carry, or not at all. */
    if (created.status === 201) {
      const made = (await created.json()) as { nas: { id: string } };
      const body = (await (await send(`/nas/${made.nas.id}/evidence`)).json()) as { profile: string };
      expect(body.profile).toBe("vendor-not-chosen");
      return;
    }
    const existing = world.nas.find((n) => n.driver === "coa" && !n.coaVendor);
    if (!existing) return;
    const body = (await (await send(`/nas/${existing.id}/evidence`)).json()) as {
      profile: string;
      attributes: unknown[];
    };
    expect(body.profile).toBe("vendor-not-chosen");
    expect(body.attributes).toEqual([]);
  });

  it("404s for a device that does not exist", async () => {
    const res = await send("/nas/does-not-exist/evidence");
    expect(res.status).toBe(404);
  });
});
