/**
 * The clock is allowed to move the world. It is not allowed to improve it.
 *
 * These tests exist because the failure mode of a living mock is quiet and
 * expensive: a world that heals itself while nobody is looking removes the exact
 * cases the design was built against, and any review that follows is validated
 * against a world that does not exist. The first block below is the one
 * that matters — everything the fixtures were made ugly for must survive N ticks
 * completely unchanged.
 *
 * The rest assert the four behaviours the clock exists to make *visible*: a step
 * rather than a glide, a decay, a recovery, and a NAS that flaps.
 */
import { describe, expect, it } from "vitest";
import type { Session } from "@foyer/contract";
import { buildWorld, deriveAlarms, FLAPPING_NAS_ID, SESSION_IDS } from "@foyer/fixtures";
import { CYCLE, INTERIM_SECONDS, Simulation } from "./sim.js";
import { conform } from "./validate.js";

/** Long enough to cross several cycles, so nothing survives by luck of phase. */
const N = 40;

function sim(ticks = 0) {
  const world = buildWorld();
  const s = new Simulation(world, { tickMs: 20_000 });
  for (let i = 0; i < ticks; i++) s.advance();
  return { world, s };
}

const byId = (world: { sessions: Session[] }, id: string) =>
  world.sessions.find((x) => x.id === id)!;

describe("the pathological rows survive the clock", () => {
  const { world } = sim(N);

  it("leaves the orphaned session orphaned, forty days old, with no Acct-Stop", () => {
    // The classic RADIUS accounting failure: a NAS rebooted without Acct-Stop and
    // nothing but the reaper will ever close this. A clock that quietly aged it
    // into an ended session would delete the case.
    const s = byId(world, SESSION_IDS.orphaned);
    expect(s.state).toBe("orphaned");
    expect(s.endedAt).toBeNull();
    expect(s.inputOctets).toBe(44_120_233);
    expect(s.outputOctets).toBe(91_882_004);
    const days = (Date.now() - Date.parse(s.startedAt)) / 86_400_000;
    expect(days).toBeGreaterThan(39);
    expect(days).toBeLessThan(41);
    expect(s.freshness.state).toBe("stale");
  });

  it("never gives the no-accounting session a counter", () => {
    // `null` is not `0`. The controller-API path has no Access-Accept in which to
    // request interim updates, so there is nothing and there never will be. A tick
    // that turned "we were told nothing" into "nothing was transferred" would be
    // the single most damaging thing this file could do.
    const s = byId(world, SESSION_IDS.noAccounting);
    expect(s.state).toBe("unknown");
    expect(s.inputOctets).toBeNull();
    expect(s.outputOctets).toBeNull();
    expect(s.sessionSeconds).toBeNull();
    expect(s.freshness.state).toBe("absent");
    expect(s.lastUpdateAt).toBeNull();
  });

  it("keeps the clock-skew row's negative duration, and keeps its start in the future", () => {
    // Left to drift, `startedAt` would slide into the past and the row would stop
    // being an anomaly at all while `sessionSeconds` still said -252.
    const s = byId(world, SESSION_IDS.clockSkew);
    expect(s.sessionSeconds).toBe(-252);
    expect(Date.parse(s.startedAt)).toBeGreaterThan(Date.now());
  });

  it("keeps the duplicate MAC duplicated, on one shared Acct-Session-Id", () => {
    const a = byId(world, SESSION_IDS.duplicateA);
    const b = byId(world, SESSION_IDS.duplicateB);
    expect(a.callingStationId).toBe(b.callingStationId);
    expect(a.acctSessionId).toBe(b.acctSessionId);
    expect(a.state).toBe("online");
    expect(b.state).toBe("online");
    expect(a.nasId).not.toBe(b.nasId);
    // Idle clients: updates arrive, counters do not move.
    expect(a.inputOctets).toBe(220_004_112);
    expect(b.inputOctets).toBe(4_112);
  });

  it("still satisfies every ugliness the admin tests review against", () => {
    const online = world.sessions.filter((s) => s.state === "online");
    const macs = new Map<string, number>();
    for (const s of online) macs.set(s.callingStationId, (macs.get(s.callingStationId) ?? 0) + 1);

    expect([...macs.values()].some((n) => n > 1), "the duplicate MAC healed").toBe(true);
    expect(world.sessions.some((s) => s.state === "orphaned")).toBe(true);
    expect(world.sessions.some((s) => s.state === "unknown")).toBe(true);
    expect(world.sessions.some((s) => (s.sessionSeconds ?? 0) < 0)).toBe(true);
    expect(world.nas.some((n) => n.reachability === "unreachable")).toBe(true);
    expect(world.nas.some((n) => n.reachability === "credentials-expired")).toBe(true);
    expect(world.nas.some((n) => n.messageAuthenticator === "refused")).toBe(true);

    const ids = world.sessions.map((s) => s.acctSessionId).filter(Boolean);
    expect(new Set(ids).size, "Acct-Session-Ids became unique").toBeLessThan(ids.length);
  });

  it("always leaves at least one online session with stale accounting", () => {
    // The two accounting-gap actors are on opposite phases precisely so this can
    // never reach zero. A world where every row is healthy is a world the design
    // was not built for.
    for (let tick = 0; tick < CYCLE * 2; tick++) {
      const { world: w, s } = sim(tick);
      const stale = w.sessions.filter(
        (x) => x.state === "online" && x.freshness.state === "stale",
      );
      expect(stale.length, `no stale session at tick ${tick} (phase ${s.phase})`).toBeGreaterThan(
        0,
      );
    }
  });
});

describe("counters step, they do not glide", () => {
  it("advances a session by exactly one interim interval per tick", () => {
    // The discontinuity *is* the product truth. A counter that crept upward every
    // second would make the sampled-quota wording look like an excuse rather than
    // a description.
    const { world, s } = sim();
    const target = byId(world, SESSION_IDS.counterWrap);
    const before = { out: target.outputOctets!, secs: target.sessionSeconds! };

    s.advance();
    const afterOne = { out: target.outputOctets!, secs: target.sessionSeconds! };
    expect(afterOne.secs - before.secs).toBe(INTERIM_SECONDS);
    expect(afterOne.out).not.toBe(before.out);

    // And nothing at all happens between ticks. There is no interpolation and no
    // per-request drift: reading twice inside one interval returns one number.
    const snapshot = JSON.stringify(target);
    expect(JSON.stringify(target)).toBe(snapshot);
  });

  it("moves every online session, so the table is visibly alive", () => {
    const { world, s } = sim();
    const before = new Map(world.sessions.map((x) => [x.id, x.outputOctets]));
    s.advance();
    const moved = world.sessions.filter(
      (x) => x.state === "online" && x.outputOctets !== before.get(x.id),
    );
    expect(moved.length).toBeGreaterThan(15);
  });

  it("wraps a 32-bit octet counter back through zero", () => {
    // `Acct-Output-Octets` is 32-bit (RFC 2866). A NAS that omits
    // `Acct-Output-Gigawords` (RFC 2869) reports a total that drops by 4 GiB
    // mid-session, and every "used"/"remaining" arithmetic in the admin has to
    // survive it.
    const { world, s } = sim();
    const target = byId(world, SESSION_IDS.counterWrap);
    let wrapped = false;
    for (let i = 0; i < CYCLE * 2; i++) {
      const before = target.outputOctets!;
      s.advance();
      if (target.outputOctets! < before) wrapped = true;
      expect(target.outputOctets!).toBeGreaterThanOrEqual(0);
      expect(target.outputOctets!).toBeLessThan(2 ** 32);
      expect(Number.isInteger(target.outputOctets!)).toBe(true);
    }
    expect(wrapped, "the 32-bit counter never wrapped").toBe(true);
  });

  it("tells the UI the counter wrapped, and keeps telling it", () => {
    /*
     * The named test for the contract half of the counter wrap.
     *
     * Without `countersWrapped`, nothing on the wire says the fixture wrapped — a
     * session goes 3.81 GB → 14 MB across one tick and renders as a plain fact on a
     * healthy access point. `docs/protocol-reality.md` requires a shrinking total to
     * be "shown as the anomaly it is", and no component could comply without a field
     * to tell it. This is the tripwire.
     */
    const { world, s } = sim();
    const target = byId(world, SESSION_IDS.counterWrap);

    // Nothing has wrapped at t = 0. A wrap is *observed* between two accounting
    // records, so a fixture cannot assert one into existence.
    expect(target.countersWrapped).toBe(false);
    expect(target.outputOctetsTotal).toBe(target.outputOctets);

    let drops = 0;
    for (let i = 0; i < CYCLE * 2; i++) {
      const before = target.outputOctets!;
      const totalBefore = target.outputOctetsTotal!;
      s.advance();
      if (target.outputOctets! < before) {
        drops++;
        expect(target.countersWrapped, "a drop that did not raise the flag").toBe(true);
      }
      // The corrected total is what the reported counter is not: monotonic. This is
      // the figure an admin may subtract; the reported one is the evidence.
      expect(target.outputOctetsTotal!).toBeGreaterThan(totalBefore);
      expect(target.outputOctetsTotal!).toBe(target.outputOctets! + drops * 2 ** 32);
      // The reported counters are never repaired in place.
      expect(target.outputOctets!).toBeLessThan(2 ** 32);
    }
    // Two cycles, ~4 GiB each: it goes round more than once, and the correction has
    // to keep counting rather than latching at one wrap.
    expect(drops).toBeGreaterThan(1);

    // Sticky. A flag that cleared itself at the next clean interval would switch off
    // exactly when the operator stopped looking, and every later figure is still a
    // floor rather than a measurement.
    expect(target.countersWrapped).toBe(true);
    for (let i = 0; i < CYCLE; i++) s.advance();
    expect(target.countersWrapped).toBe(true);
  });

  it("never claims a wrap on a session whose NAS carries Gigawords", () => {
    // Only the one fixture omits them. A flag that crept onto healthy rows would be
    // worse than no flag: the qualifier would appear everywhere and mean nothing.
    const { world, s } = sim(CYCLE * 2);
    for (const x of world.sessions) {
      if (x.id === SESSION_IDS.counterWrap) continue;
      expect(x.countersWrapped, `${x.id} claimed a wrap`).toBe(false);
      expect(x.inputOctetsTotal ?? null).toBe(x.inputOctets ?? null);
      expect(x.outputOctetsTotal ?? null).toBe(x.outputOctets ?? null);
    }
    // And a session with no accounting has no total either. `null` is not `0`.
    const none = byId(world, SESSION_IDS.noAccounting);
    expect(none.inputOctetsTotal ?? null).toBeNull();
    expect(none.outputOctetsTotal ?? null).toBeNull();
  });

  it("does not advance a session's duration while its accounting is silent", () => {
    // `Acct-Session-Time` is carried by the update. No update, no new duration —
    // and the growing distance between "started" and "online for" is the visible
    // consequence of the gap.
    const { world, s } = sim(2); // phase 2: sess-interim-gap is still healthy
    const target = byId(world, SESSION_IDS.interimGap);
    const secs = target.sessionSeconds!;
    const out = target.outputOctets!;

    s.advance(); // phase 3 — silent
    s.advance(); // phase 4 — silent
    expect(target.sessionSeconds).toBe(secs);
    expect(target.outputOctets).toBe(out);

    const reported = target.sessionSeconds! * 1000;
    const wall = Date.now() - Date.parse(target.startedAt);
    expect(wall - reported).toBeGreaterThan(1.5 * INTERIM_SECONDS * 1000);
  });
});

describe("freshness decays and recovers while the operator watches", () => {
  const stateAt = (tick: number, id: string) => byId(sim(tick).world, id).freshness.state;

  it("takes sess-interim-gap from sampled to stale without changing its status", () => {
    expect(stateAt(2, SESSION_IDS.interimGap)).toBe("sampled");
    for (const tick of [5, 6, 7, 8, 9, 10]) {
      expect(stateAt(tick, SESSION_IDS.interimGap), `tick ${tick}`).toBe("stale");
    }
    // The status itself never moved. Freshness is a qualifier, not a state change.
    expect(byId(sim(6).world, SESSION_IDS.interimGap).state).toBe("online");
    expect(stateAt(11, SESSION_IDS.interimGap)).toBe("sampled");
  });

  it("recovers sess-stale-mikrotik, which starts degraded", () => {
    expect(stateAt(0, SESSION_IDS.staleMikrotik)).toBe("stale");
    expect(stateAt(1, SESSION_IDS.staleMikrotik)).toBe("stale");
    expect(stateAt(5, SESSION_IDS.staleMikrotik)).toBe("sampled");
    expect(stateAt(6, SESSION_IDS.staleMikrotik)).toBe("sampled");
  });

  it("keeps intervalSeconds at the real 300-second floor, never the demo tick", () => {
    // The compressed clock changes how fast the screen moves. It must not change
    // what the screen claims about the protocol.
    const { world } = sim(N);
    for (const s of world.sessions) {
      if (s.freshness.intervalSeconds != null) expect(s.freshness.intervalSeconds).toBe(300);
    }
  });
});

describe("a NAS goes unreachable and comes back", () => {
  const nasAt = (tick: number) => sim(tick).world.nas.find((n) => n.id === FLAPPING_NAS_ID)!;

  it("flaps on the stated phases", () => {
    expect(nasAt(1).reachability).toBe("ok");
    expect(nasAt(6).reachability).toBe("unreachable");
    expect(nasAt(9).reachability).toBe("unreachable");
    expect(nasAt(10).reachability).toBe("ok");
    // And again next cycle, so a viewer who missed it gets another chance.
    expect(nasAt(CYCLE + 7).reachability).toBe("unreachable");
  });

  it("moves the alarm list with it", () => {
    // The alarm, the dashboard count and the detail page are all derived from the
    // same row, so an operator can check them against each other. This is the
    // assertion that they cannot drift apart.
    const down = sim(7).world;
    const up = sim(11).world;
    const id = `alarm-nas-unreachable-${FLAPPING_NAS_ID}`;

    expect(deriveAlarms(down, "site-hcm").some((a) => a.id === id)).toBe(true);
    expect(deriveAlarms(up, "site-hcm").some((a) => a.id === id)).toBe(false);

    const unhealthy = (w: typeof down) =>
      w.nas.filter((n) => n.siteId === "site-hcm" && n.reachability !== "ok").length;
    expect(unhealthy(down)).toBe(unhealthy(up) + 1);
  });

  it("never touches the two access points that are meant to stay broken", () => {
    const w = sim(N).world;
    expect(w.nas.find((n) => n.id === "nas-unifi-3")!.reachability).toBe("unreachable");
    expect(w.nas.find((n) => n.id === "nas-omada-1")!.reachability).toBe("credentials-expired");
  });

  it("keeps a healthy access point's last-seen recent", () => {
    // Otherwise every NAS quietly reads "ok, last seen 40 minutes ago" after the
    // mock has been up for 40 minutes — a contradiction on screen that has nothing
    // to do with the product.
    const n = sim(N).world.nas.find((x) => x.id === "nas-unifi-1")!;
    expect(Date.now() - Date.parse(n.lastSeenAt!)).toBeLessThan(120_000);
  });
});

describe("the clock is deterministic and stays inside the contract", () => {
  it("produces identical counters from two independent runs", () => {
    // No `Math.random()` at tick time. Two people looking at the app, or two
    // screenshots taken on different machines, are comparable.
    const a = sim(17).world;
    const b = sim(17).world;
    const counters = (w: typeof a) =>
      w.sessions.map((s) => [s.id, s.inputOctets, s.outputOctets, s.sessionSeconds]);
    expect(counters(a)).toEqual(counters(b));
  });

  it("emits sessions, access points and alarms that still validate against openapi.yaml", () => {
    // The gate that must stay green. If the moving world starts emitting something
    // the schema does not allow, the schema is right and the clock is wrong.
    const w = sim(N).world;
    for (const s of w.sessions) conform("Session", s);
    for (const n of w.nas) conform("Nas", n);
    for (const a of deriveAlarms(w, "site-hcm")) conform("Alarm", a);
  });

  it("keeps every octet counter a non-negative safe integer", () => {
    const w = sim(N).world;
    for (const s of w.sessions) {
      for (const v of [s.inputOctets, s.outputOctets]) {
        if (v == null) continue;
        expect(Number.isSafeInteger(v), `${s.id}: ${v}`).toBe(true);
        expect(v).toBeGreaterThanOrEqual(0);
      }
    }
  });
});
