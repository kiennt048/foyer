/**
 * **The first five minutes of a real installation**, driven against a world that has
 * been migrated and first-booted and nothing more.
 *
 * This file exists because of two things that hold together:
 *
 *   * **`createSite` is invisible to the ordinary world.** Every fixture world has
 *     three sites, so every drive in `server.test.ts` starts after the step that
 *     makes the first one. That is *a world that cannot express a property makes
 *     every check of that property vacuous*, at deployment scale.
 *   * **One venue per install.** `createSite` refuses the second call, so in the
 *     ordinary world it refuses always, and the `201` the contract declares is a
 *     response no drive there could reach.
 *
 * So the refusal does not need a workaround. It needs a world with no sites, and
 * `FOYER_MOCK_FRESH=1` is that world: nobody to sign in as — Foyer
 * ships no credential, so the installer opens a one-time setup — and no sites, therefore no devices, no vouchers, no sessions, no accounts.
 *
 * **The env variable is set before the import and the import is dynamic**, because
 * `state.ts` reads it while building the world at module load. Vitest isolates test
 * files into their own module registries, so `server.test.ts` in the same run still
 * gets the ordinary world.
 */
import { describe, expect, it } from "vitest";

process.env.FOYER_MOCK_FRESH = "1";
const { app } = await import("./server.js");
const { SETUP_CODE, world } = await import("./state.js");

/**
 * **Every request after the setup is signed in, because a real first run is.**
 *
 * `deploy/first-boot.sh` opens the console's first-access setup and prints its code;
 * the operator creates the first administrator with it, signed in by the same
 * answer, and then makes the venue. Driving these steps anonymously would also cost
 * this file its audit coverage: `audit()` refuses to attribute a write to nobody, so
 * with no session **no entry is written at all**. The cookie is filled in by the
 * setup drive below and every later request carries it.
 */
let session = "";

const send = (path: string, init?: RequestInit) =>
  app.request(`/api/v0${path}`, {
    ...init,
    headers: { "content-type": "application/json", cookie: session, ...(init?.headers ?? {}) },
  });

/** The password the first administrator chooses — derived, never a literal (§7). */
const OWN_PASSWORD = `${SETUP_CODE ?? ""}-mine`.toLowerCase();

describe("a deployment that has just been installed", () => {
  /*
   * Non-vacuity first, and it is not ceremony.
   *
   * Every assertion below is about what happens when there are no sites. If the env
   * switch stopped being read — a rename, a build step that strips it, a change to
   * when `state.ts` evaluates — this file would go on passing against the ordinary
   * three-site world for the wrong reason, and `createSite` would then be answering
   * 409 while a test named *the first site* watched it happen.
   */
  it("starts with no sites at all, which is what makes the rest of this file mean anything", () => {
    expect(
      world.sites,
      "FOYER_MOCK_FRESH did not reach state.ts — every assertion in this file is now " +
        "about the ordinary fixture world and none of them is testing what it says",
    ).toEqual([]);
    expect(world.nas).toEqual([]);
    expect(world.sessions).toEqual([]);
    /* And nobody to sign in as: Foyer ships no credential. The setup below is how
       the first administrator comes to exist. */
    expect(world.operators).toEqual([]);
    expect(SETUP_CODE, "a fresh deployment has no setup code, so nobody could ever get in").toBeTruthy();
  });

  /*
   * ── the first thing a real installation does ─────────────────────────────────
   *
   * No password this product could name opens it (there is no shipped one), the console is told to show the setup,
   * a wrong code is refused, the right one creates the administrator once and signs
   * that browser in, and after that the setup is closed for good.
   */
  it("offers the setup, and no fixed password signs anybody in", async () => {
    const setup = await send("/auth/setup");
    expect(((await setup.json()) as { required: boolean }).required).toBe(true);
    for (const [username, password] of [
      ["admin", "Foyer@123"],
      ["admin", "admin"],
    ] as const) {
      const res = await send("/auth/session", {
        method: "POST",
        body: JSON.stringify({ username, password }),
      });
      expect(res.status, `${username} / ${password} signed in to a fresh install`).toBe(401);
    }
  });

  it("refuses a code that is not the installer's", async () => {
    const res = await send("/auth/setup", {
      method: "POST",
      body: JSON.stringify({ setupCode: "0000-0000-0000-0000", username: "quan.ly", password: OWN_PASSWORD }),
    });
    expect(res.status).toBe(422);
    const body = (await res.json()) as { params?: { fields?: Record<string, string> } };
    expect(body.params?.fields?.setupCode).toBe("incorrect");
  });

  it("creates the administrator with the installer's code, once, signed in", async () => {
    const res = await send("/auth/setup", {
      method: "POST",
      /* Retyped the way a person reads it off a terminal. */
      body: JSON.stringify({
        setupCode: SETUP_CODE!.toLowerCase().replaceAll("-", " "),
        username: "Quan.Ly",
        password: OWN_PASSWORD,
      }),
    });
    expect(res.status).toBe(200);
    session = res.headers.get("set-cookie")?.split(";")[0] ?? "";
    const body = (await res.json()) as {
      operator: { username: string; role: string };
      mustChangePassword: boolean;
    };
    expect(body.operator).toMatchObject({ username: "quan.ly", role: "administrator" });
    /* A password of their own: nothing to force a change of. */
    expect(body.mustChangePassword).toBe(false);

    const again = await send("/auth/setup", {
      method: "POST",
      body: JSON.stringify({ setupCode: SETUP_CODE, username: "second", password: OWN_PASSWORD }),
    });
    expect(again.status, "the setup answered a second time").toBe(409);
    const state = await send("/auth/setup");
    expect(((await state.json()) as { required: boolean }).required).toBe(false);
  });

  it("listSites answers with an empty list rather than an error", async () => {
    /* After the setup above, which is the order a real first run happens in. */

    const res = await send("/sites");
    expect(res.status).toBe(200);
    expect(await res.json()).toEqual([]);
  });

  /*
   * The first venue, end to end.
   *
   * A `201` on its own is satisfied by a handler minting an id nothing else accepts.
   * What matters is a site `createNas` will take, so the device registration is
   * the assertion that actually closes it. `mikrotik` because it needs no
   * controller — which is exactly what a first install has.
   */
  it("creates its first venue, and a device can be registered into it", async () => {
    const created = await send("/sites", {
      method: "POST",
      body: JSON.stringify({ name: "  Quán Cà Phê Số 7  " }),
    });
    expect(created.status).toBe(201);
    const site = (await created.json()) as { id: string; name: string; environment: string };
    expect(site.id).toBeTruthy();
    /* Trimmed. The venue's name is typed, and a leading space is not part of it. */
    expect(site.name).toBe("Quán Cà Phê Số 7");
    /* `live`, and `SiteCreate` accepts no environment: `lab` is the badge that says
       the site may be pointed at fake gear. */
    expect(site.environment).toBe("live");

    const listed = (await (await send("/sites")).json()) as { id: string }[];
    expect(listed.map((s) => s.id)).toContain(site.id);

    const device = await send("/nas", {
      method: "POST",
      body: JSON.stringify({
        siteId: site.id,
        name: "Front of house",
        driver: "mikrotik",
        ipAddress: "10.90.0.4",
      }),
    });
    expect(
      device.status,
      "the site it just made cannot carry a device, so createSite closes nothing",
    ).toBe(201);
    const registered = (await device.json()) as { nas: { siteId: string } };
    expect(registered.nas.siteId).toBe(site.id);
  });

  /*
   * And the second call, from the same world, immediately after the first.
   *
   * This is the whole of the one-venue rule: **not** that a deployment may never
   * hold two sites — the schema permits many and the tenant seam is deliberate
   * insurance — but that this operation will not make the second one.
   *
   * It runs after the case above rather than in its own describe on purpose: the
   * refusal only means something once the world has been driven from zero to one by
   * the product itself. A refusal asserted against a world that started with sites
   * is the assertion `server.test.ts` already carries.
   */
  it("refuses the second venue once the first exists", async () => {
    const refused = await send("/sites", {
      method: "POST",
      body: JSON.stringify({ name: "Chi nhánh Hai" }),
    });
    expect(refused.status).toBe(409);
    const body = (await refused.json()) as { type: string };
    expect(body.type).toBe("/problems/one-site-per-install");
    /* And nothing was half-written. */
    const listed = (await (await send("/sites")).json()) as { name: string }[];
    expect(listed.map((s) => s.name)).not.toContain("Chi nhánh Hai");
    expect(listed).toHaveLength(1);
  });

  /**
   * It writes `site-created`, and **this is the only world in which anything can**.
   *
   * `AuditKind` has a member for creating a venue because `internal/api`'s
   * invariant 10 says an action with no member is left unrecorded rather than filed
   * under a near-enough one — `nas-registered` would name a device as the subject of
   * a change that made a venue. Without the member, the first act of every
   * installation would go unrecorded.
   *
   * It is asserted **here** and not in `server.test.ts` because `createSite` refuses
   * the second venue, so in the ordinary three-site world this entry can never be
   * written. `server.test.ts`'s drive sweep states that condition in an `else`
   * rather than skipping the kind silently — a skip that names a phase instead of a
   * condition looks like a check and is not one.
   *
   * Invariant 10's own prohibition is asserted beside it, because "wrote the right
   * entry" and "did not file a venue under a device's kind" are two claims. It is
   * asserted over the subject rather than over the count: the device registered two
   * tests up writes a legitimate `nas-registered`, and a test that counted those to
   * zero would be measuring that nothing was signed in.
   */
  it("records `site-created` for the venue it made, under a subject that is the venue", () => {
    const entries = world.audit.filter((e) => e.kind === "site-created");
    expect(
      entries.length,
      "creating the first venue recorded nothing — the log that holds every later " +
        "change to this site says nothing about where it came from",
    ).toBe(1);
    const entry = entries[0]!;
    /* Invariant 10 from the other side: the kind exists so this entry need not be
       filed under a near-enough one, and a subject of any other kind would be
       `nas-registered`'s mistake wearing the right name. */
    expect(entry.subject?.kind).toBe("site");
    expect(entry.subject?.id).toBe(world.sites[0]!.id);
    expect(entry.subject?.name).toBe(world.sites[0]!.name);
    expect(entry.siteId).toBe(world.sites[0]!.id);
    expect(entry.actorUsername).toBeTruthy();
    /* No composed prose, the rule every kind carries. */
    expect(entry.title).toBeUndefined();
    expect(entry.detail).toBeUndefined();

    expect(
      world.audit.filter((e) => e.kind === "nas-registered" && e.subject?.kind === "site"),
      "a venue is the subject of a `nas-registered` entry — invariant 10's exact " +
        "prohibition, and the reason `site-created` was added rather than borrowed",
    ).toEqual([]);
  });
});
