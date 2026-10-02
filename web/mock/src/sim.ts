/**
 * The world advances.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *  WHY THIS EXISTS
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * A snapshot cannot be reviewed for the behaviours this console is *about*. The
 * four things the design turns on are all verbs:
 *
 *   - a counter that advances in a five-minute **step** rather than continuously,
 *     which is the actual shape of `radacct` data and the reason there is no
 *     bandwidth graph anywhere in this product;
 *   - a heartbeat that **stops** when Interim-Updates stop, with the freshness
 *     qualifier appearing beside a status that has *not* changed;
 *   - a refetch **arriving** while the operator reads the table;
 *   - an access point **going** unreachable and coming back, with the alarm list,
 *     the dashboard count and the detail page all moving together.
 *
 * None of those can be argued about from a static fixture. A review of the console
 * is an operator looking at this and saying yes or no, and it should be looking at
 * a living system.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *  THE COMPRESSED CLOCK — READ THIS BEFORE BELIEVING ANY CADENCE YOU SEE
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * One tick stands in for one `Acct-Interim-Interval`. In production that interval
 * has a widely-implemented **300-second floor** (RFC 2869; `docs/protocol-reality.md`).
 * Here a tick is `TICK_MS`, 20 seconds by default, so **simulated time runs 15×
 * real time**. Nothing in the product advances this fast.
 *
 * What is *not* compressed, deliberately:
 *
 *   - `intervalSeconds` on the wire is still **300**. The UI reads it and says
 *     "sampled every 5 minutes", which is the truth about the protocol.
 *   - Every timestamp the mock emits is expressed as an offset from the real
 *     `Date.now()`, so a row is always internally coherent: a session whose sample
 *     is three intervals old says "15 minutes ago" *and* says `stale` *and* says
 *     its interval is 5 minutes. A viewer reading the screen is reading the
 *     truth; only the wall-clock rate at which the screen changes is compressed.
 *   - Counters still move in **discrete steps**, flat in between. That
 *     discontinuity is the product truth and the whole reason the quota wording
 *     says "sampled", so it is never smoothed.
 *
 * The one thing a viewer could be misled about is therefore the *rate*: steps
 * arrive every 20 seconds here and every 5 minutes in the field. Three defences:
 * the boot banner says so loudly, every response carries an `X-Foyer-Mock-Clock`
 * header saying so, and `FOYER_MOCK_LIVE=0` freezes the world entirely for anyone
 * who would rather review a still.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *  THE SCHEDULE
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * One cycle is 12 ticks — 1 simulated hour, 4 real minutes at the default tick.
 * Everything below is a pure function of `tick % 12`, so two people looking at the
 * app five minutes apart see the same phase, and a screenshot can be reasoned
 * about rather than merely admired. There is no `Math.random()` at tick time; byte
 * steps are hashed from `(sessionId, tick)`.
 *
 *   phase                0  1  2  3  4  5  6  7  8  9 10 11
 *   ─────────────────────────────────────────────────────────────────────────
 *   online sessions      ●  ●  ●  ●  ●  ●  ●  ●  ●  ●  ●  ●   byte step, every tick
 *   sess-interim-gap     ●  ●  ●  ·  ·  ·  ·  ·  ·  ·  ·  ●   silent 3–10
 *                                    ?  └──── stale ────┘        recovers at 11
 *   sess-stale-mikrotik  ·  ·  ·  ·  ·  ●  ●  ●  ●  ·  ·  ·   silent 9–4
 *                        └─── stale ──┘                 ?        recovers at 5
 *   nas-unifi-2          ok ok ok ok ok ok ✗  ✗  ✗  ✗  ok ok   unreachable 6–9
 *   sess-counter-wrap    ~340 MiB per tick → its 32-bit octet counter wraps
 *                        back through zero roughly once per cycle
 *
 * `?` marks the phase where the gap is exactly two intervals and therefore sits
 * exactly on the `> 2 × interval` threshold: at tick time it is still `sampled`,
 * and the read path tips it to `stale` a second later. That is the transition the
 * operator watches happen, so it is left on the boundary rather than rounded away.
 *
 * The two accounting-gap windows **overlap** on purpose. Complementary windows
 * left four phases per cycle in which every online row was healthy, and a world
 * that is briefly perfect is a world someone will screenshot. As drawn, the two
 * definite-stale runs — phases 5–10 and 11–4 — tile the cycle exactly, so **there
 * is always at least one degraded row on screen** while both transitions, decay
 * and recovery, still happen inside one cycle.
 *
 * `sess-stale-mikrotik`'s resting gap at phase 0 is four intervals, which is
 * exactly the "20 minutes stale" the fixture is authored with — so tick 0 and
 * phase 0 are the same world, and the schedule has no seam at boot.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *  WHAT NEVER MOVES
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `FROZEN_SESSION_IDS`. A living world that quietly heals itself is worse than a
 * static one, because it removes the cases the design was built against. Those
 * rows are re-anchored each tick so their *authored offsets* survive — the orphan
 * stays 40 days orphaned instead of ageing to 41, and the clock-skew row keeps
 * starting four minutes in the future instead of drifting into the past and losing
 * its anomaly. Their counters are never touched. `sim.test.ts` asserts it.
 */
import type { Nas, Session } from "@foyer/contract";
import {
  FLAPPING_NAS_ID,
  FROZEN_SESSION_IDS,
  SESSION_IDS,
  freshnessStateAt,
  noise,
  type World,
} from "@foyer/fixtures";

/** The real floor, per RFC 2869. Emitted on the wire; never compressed. */
export const INTERIM_SECONDS = 300;

/** Real milliseconds per tick. One tick stands in for one `INTERIM_SECONDS`. */
export const DEFAULT_TICK_MS = Number(process.env.FOYER_MOCK_TICK_MS ?? 20_000);

/** Ticks per cycle. See the schedule above. */
export const CYCLE = 12;

/** Off means the world is exactly the static fixture snapshot. */
export const LIVE = process.env.FOYER_MOCK_LIVE !== "0";

/**
 * `Acct-Input-Octets` and `Acct-Output-Octets` are 32-bit (RFC 2866 §5.3–5.4).
 * `Acct-Input-Gigawords`/`Acct-Output-Gigawords` (RFC 2869 §5.1–5.2) exist for
 * exactly one reason, and a NAS that omits them reports a total that drops by this
 * much mid-session.
 */
const OCTET_WRAP = 2 ** 32;

const MIB = 1024 * 1024;
const INTERVAL_MS = INTERIM_SECONDS * 1000;

/** The phases in which each accounting-gap actor receives no Interim-Update. */
const SILENT_PHASES: Record<string, readonly number[]> = {
  // Healthy at rest; goes quiet at phase 3, definitely stale from 5, recovers at 11.
  [SESSION_IDS.interimGap]: [3, 4, 5, 6, 7, 8, 9, 10],
  // Degraded at rest — which is how the fixture is authored, at a resting gap of
  // four intervals — and it recovers at phase 5 while the operator watches.
  [SESSION_IDS.staleMikrotik]: [9, 10, 11, 0, 1, 2, 3, 4],
};

/** The phases in which `FLAPPING_NAS_ID` is unreachable. */
const NAS_DOWN_PHASES: readonly number[] = [6, 7, 8, 9];

const iso = (ms: number) => new Date(ms).toISOString();

/**
 * How many consecutive intervals an actor has now missed, at this tick.
 *
 * Computed from the phase rather than accumulated in a counter, so any tick can be
 * evaluated on its own and a restarted process lands in the same place.
 */
function gapIntervals(phases: readonly number[] | undefined, tick: number): number {
  if (!phases) return 0;
  const idx = phases.indexOf(((tick % CYCLE) + CYCLE) % CYCLE);
  return idx < 0 ? 0 : idx + 1;
}

/**
 * The bytes a session gains in one interim interval.
 *
 * Deterministic in `(id, tick)`: the same session gains the same amount at the
 * same tick in every process. The per-session base rate is drawn once from the id,
 * so a heavy client stays heavy — a table where every row moves at the same speed
 * looks synthetic and hides the outliers the operator is looking for.
 */
function octetStep(s: Session, tick: number): { input: number; output: number } {
  const perInterval =
    s.id === SESSION_IDS.counterWrap
      ? // ~10 Mbps sustained. Twelve of these is 4 GiB, which is what makes the
        // 32-bit wrap watchable rather than theoretical.
        340 * MIB
      : // 1–120 MiB per 300 s ≈ 27 kbps to 3.4 Mbps. A café, not a datacentre.
        (1 + noise(s.id, 0) * 119) * MIB;

  // Per-interval variation, mean 1. Real interim deltas are not a constant.
  const jitter = 0.4 + 1.2 * noise(s.id, tick + 1);
  // Uplink share, fixed per session. Guests download.
  const upShare = 0.06 + 0.18 * noise(`${s.id}:up`, 0);

  const output = Math.floor(perInterval * jitter);
  return { input: Math.floor(output * upShare), output };
}

/** Only the session whose NAS omits Gigawords wraps; everyone else is 64-bit clean. */
function reportedOctets(s: Session, value: number): number {
  return s.id === SESSION_IDS.counterWrap ? value % OCTET_WRAP : value;
}

/** The two reported counters, before a step. Compared against them after it. */
interface ReportedOctets {
  input: number | null;
  output: number | null;
}

/** Timestamp offsets, in ms before `world.now`. Negative means "in the future". */
interface SessionAnchor {
  startedAt: number;
  endedAt: number | null;
  lastUpdateAt: number | null;
  sampledAt: number | null;
}

interface NasAnchor {
  lastSeenAt: number | null;
  secretLastUsedAt: number | null;
  createdAt: number;
  reachability: Nas["reachability"];
}

export interface SimulationOptions {
  tickMs?: number;
}

export class Simulation {
  readonly tickMs: number;
  private t = 0;
  private timer: ReturnType<typeof setInterval> | null = null;
  private readonly sessionAnchors = new Map<string, SessionAnchor>();
  private readonly nasAnchors = new Map<string, NasAnchor>();
  /** Observed 32-bit wraps per session id. See `observeCounters`. */
  private readonly wraps = new Map<string, number>();

  constructor(
    private readonly world: World,
    opts: SimulationOptions = {},
  ) {
    this.tickMs = opts.tickMs ?? DEFAULT_TICK_MS;
    for (const s of world.sessions) this.anchorSession(s, world.now);
    for (const n of world.nas) this.anchorNas(n, world.now);
  }

  /** Ticks applied so far. `0` is the world exactly as the fixtures authored it. */
  get tick(): number {
    return this.t;
  }

  /** `tick % CYCLE`. The whole schedule is a function of this. */
  get phase(): number {
    return this.t % CYCLE;
  }

  /** Simulated seconds per real second. 15 at the default tick. */
  get speedup(): number {
    return INTERIM_SECONDS / (this.tickMs / 1000);
  }

  start(): this {
    if (this.timer) return this;
    this.timer = setInterval(() => this.advance(), this.tickMs);
    // The HTTP server keeps the loop alive; this must not keep it alive on its own.
    this.timer.unref?.();
    return this;
  }

  stop(): void {
    if (this.timer) clearInterval(this.timer);
    this.timer = null;
  }

  /** One interim interval passes. Idempotent in the sense that matters: pure in `(id, tick)`. */
  advance(now = Date.now()): void {
    this.t += 1;
    const tick = this.t;

    for (const s of this.world.sessions) {
      const anchor = this.anchorSession(s, now);

      if (FROZEN_SESSION_IDS.includes(s.id)) {
        this.reanchorSession(s, anchor, now);
        continue;
      }

      // A session with no accounting has nothing to advance. `null` is not `0`:
      // the NAS has not told us the client transferred nothing, it has told us
      // nothing at all — and a tick must never convert the second into the first.
      if (s.state !== "online" || s.inputOctets == null || s.outputOctets == null) continue;

      this.advanceSession(s, tick, now, gapIntervals(SILENT_PHASES[s.id], tick));
    }

    for (const n of this.world.nas) {
      const anchor = this.anchorNas(n, now);
      if (n.id === FLAPPING_NAS_ID) this.flapNas(n, tick, now);
      else this.reanchorNas(n, anchor, now);
    }
  }

  /** The boot banner. Loud on purpose; see the header. */
  describe(): string[] {
    const seconds = Math.round(this.tickMs / 1000);
    const cycleMinutes = ((CYCLE * this.tickMs) / 60_000).toFixed(1);
    return [
      "",
      "  ⚠  SIMULATED CLOCK — THE CADENCE YOU SEE IS NOT THE PRODUCT'S CADENCE",
      "",
      `     One tick every ${seconds}s stands in for one 300-second Acct-Interim-Interval,`,
      `     so simulated time runs ${Math.round(this.speedup)}× real time. In the field a byte`,
      "     counter steps once every 5 minutes at best, and on many deployments never.",
      "",
      `     Cycle: ${CYCLE} ticks = ${cycleMinutes} real minutes = 1 simulated hour.`,
      "       · every online session steps its counters once per tick, flat in between",
      "       · sess-interim-gap goes silent at phase 3, stale by 5, recovers at 11",
      "       · sess-stale-mikrotik starts stale and recovers at phase 5",
      "       · nas-unifi-2 is unreachable at phases 6–9",
      "       · sess-counter-wrap wraps its 32-bit octet counter about once per cycle,",
      "         after which countersWrapped stays true and *OctetsTotal is a floor",
      "",
      "     intervalSeconds on the wire is still 300. Timestamps stay coherent with",
      "     each other. Only the wall-clock rate is compressed.",
      "",
      "     FOYER_MOCK_LIVE=0 freezes the world.  FOYER_MOCK_TICK_MS sets the tick.",
      "",
    ];
  }

  /** One line, for the `X-Foyer-Mock-Clock` header. */
  header(): string {
    return `simulated; 1 tick=${Math.round(this.tickMs / 1000)}s stands in for 300s interim; ${Math.round(this.speedup)}x real time; tick=${this.t} phase=${this.phase}`;
  }

  // ── session mechanics ──────────────────────────────────────────────────────

  private advanceSession(s: Session, tick: number, now: number, gap: number): void {
    if (gap === 0) {
      // An Interim-Update arrived. Everything the NAS reports moves at once, in
      // one step, and then nothing moves again until the next one.
      const before: ReportedOctets = {
        input: s.inputOctets ?? null,
        output: s.outputOctets ?? null,
      };
      const step = octetStep(s, tick);
      s.inputOctets = reportedOctets(s, (s.inputOctets ?? 0) + step.input);
      s.outputOctets = reportedOctets(s, (s.outputOctets ?? 0) + step.output);
      this.observeCounters(s, before);
      // `Acct-Session-Time` is carried *by* the update. No update, no new duration —
      // which is why this sits inside the branch.
      s.sessionSeconds = (s.sessionSeconds ?? 0) + INTERIM_SECONDS;
    }

    const gapMs = gap * INTERVAL_MS;
    const sampledAt = now - gapMs;
    s.lastUpdateAt = iso(sampledAt);
    // The gap is real elapsed time the NAS has not accounted for, so the session
    // began earlier than its reported duration. That discrepancy is the visible
    // consequence of an accounting gap, and it should be visible.
    s.startedAt = iso(now - (s.sessionSeconds ?? 0) * 1000 - gapMs);
    this.setFreshness(s, sampledAt, now);
  }

  /**
   * Publishes exactly what a *server* could work out from two consecutive `radacct`
   * reads — which is all `Session.countersWrapped` and `Session.*OctetsTotal` are
   * allowed to be.
   *
   * The simulation knows the true 64-bit totals and deliberately does not publish
   * them. It publishes the counter as reported, a flag raised when that counter went
   * *down*, and the counter plus 4 GiB per observed decrease. A real Foyer reading
   * the stock accounting table can compute that and nothing more, so the admin
   * cannot be designed against a figure the backend will not be able to produce.
   *
   * Here the reconstruction happens to be exact: `sess-counter-wrap` gains ~340 MiB
   * per interval and so can never wrap twice between two samples. On a gigabit
   * uplink at the 300-second interim floor it could, which is why the schema says
   * "lower bound" and why the mock stays inside the weaker claim rather than the
   * true one.
   *
   * The flag is **sticky**: a counter that has wrapped once leaves every later figure
   * a floor, and a flag that cleared itself at the next clean interval would switch
   * off precisely when the operator stopped looking.
   */
  private observeCounters(s: Session, before: ReportedOctets): void {
    const dropped =
      (before.input !== null && (s.inputOctets ?? 0) < before.input) ||
      (before.output !== null && (s.outputOctets ?? 0) < before.output);
    if (dropped) this.wraps.set(s.id, (this.wraps.get(s.id) ?? 0) + 1);

    const wraps = this.wraps.get(s.id) ?? 0;
    s.countersWrapped = wraps > 0;
    s.inputOctetsTotal = s.inputOctets == null ? null : s.inputOctets + wraps * OCTET_WRAP;
    s.outputOctetsTotal = s.outputOctets == null ? null : s.outputOctets + wraps * OCTET_WRAP;
  }

  private reanchorSession(s: Session, a: SessionAnchor, now: number): void {
    s.startedAt = iso(now - a.startedAt);
    if (a.endedAt !== null) s.endedAt = iso(now - a.endedAt);
    if (a.lastUpdateAt !== null) s.lastUpdateAt = iso(now - a.lastUpdateAt);
    if (a.sampledAt !== null) this.setFreshness(s, now - a.sampledAt, now);
  }

  private setFreshness(s: Session, sampledAt: number, now: number): void {
    const next = { ...s.freshness, sampledAt: iso(sampledAt) };
    s.freshness = { ...next, state: freshnessStateAt(next, now) };
  }

  private anchorSession(s: Session, base: number): SessionAnchor {
    let a = this.sessionAnchors.get(s.id);
    if (!a) {
      const at = (v: string | null | undefined) => (v ? base - Date.parse(v) : null);
      a = {
        startedAt: at(s.startedAt) ?? 0,
        endedAt: at(s.endedAt),
        lastUpdateAt: at(s.lastUpdateAt),
        sampledAt: at(s.freshness.sampledAt),
      };
      this.sessionAnchors.set(s.id, a);
    }
    return a;
  }

  // ── nas mechanics ──────────────────────────────────────────────────────────

  private flapNas(n: Nas, tick: number, now: number): void {
    const down = gapIntervals(NAS_DOWN_PHASES, tick);
    if (down === 0) {
      n.reachability = "ok";
      n.lastSeenAt = iso(now);
      n.secretLastUsedAt = iso(now - 40_000);
      return;
    }
    // Unreachable. "Last seen" is when it stopped answering, and it recedes for as
    // long as the outage lasts — so the alarm, the list row and the detail page all
    // tell the same story about how long this has been going on.
    n.reachability = "unreachable";
    n.lastSeenAt = iso(now - down * INTERVAL_MS);
  }

  private reanchorNas(n: Nas, a: NasAnchor, now: number): void {
    // A NAS whose reachability has changed since boot has been touched by something
    // else — a probe, an operator — and the mock does not overwrite that.
    if (n.reachability !== a.reachability) return;
    if (a.lastSeenAt !== null) n.lastSeenAt = iso(now - a.lastSeenAt);
    if (a.secretLastUsedAt !== null) n.secretLastUsedAt = iso(now - a.secretLastUsedAt);
  }

  private anchorNas(n: Nas, base: number): NasAnchor {
    let a = this.nasAnchors.get(n.id);
    if (!a) {
      const at = (v: string | null | undefined) => (v ? base - Date.parse(v) : null);
      a = {
        lastSeenAt: at(n.lastSeenAt),
        secretLastUsedAt: at(n.secretLastUsedAt),
        createdAt: at(n.createdAt) ?? 0,
        reachability: n.reachability,
      };
      this.nasAnchors.set(n.id, a);
    }
    return a;
  }
}
