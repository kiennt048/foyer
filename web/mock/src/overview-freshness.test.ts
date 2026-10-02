/**
 * `clientsOnline`, `orphanedSessions`, and the rows each one's freshness is derived
 * over.
 *
 * Two servers can disagree here for the same world — one answering `sampled`, the
 * other `stale` — and the honest answer is not obvious. A `store.CountSessions`
 * that answers `online` and `orphaned` as two figures but takes one
 * `min(last_update)` over both makes the timestamp travelling with the online count
 * come from a forty-day orphan that count does not contain.
 *
 * **The rule: a freshness is derived over exactly the rows its figure counts.**
 * This file pins it on the oracle side, and `internal/apistore`'s
 * `TestTheAdapterAnswersTheContractFromRealRows` pins the same three clauses on the
 * adapter's.
 *
 * ── how each case fails ─────────────────────────────────────────────────────
 *
 * *"the orphan is not in the count"* is the one assertion here that a wrong
 * timestamp derivation does not turn red, because the row set is right either way.
 * So it carries its own proof of non-vacuity: it asserts that this world **can tell
 * the two rules apart**, by computing the union answer beside the online-only one
 * and requiring them to differ. A world where every open session is fresh satisfies
 * "answers sampled" under either rule and is evidence for neither — an input the
 * harness cannot be wrong about (`docs/rules/testing.md`).
 *
 * The other two fail against these wrong derivations:
 *
 *  - *a slow NAS gone quiet* → `sampled`. The predicate
 *    `online.some(s => s.freshness.state === "stale")` asks each row about its
 *    **own** requested interval and not about the 300-second floor the figure
 *    declares. A NAS that asked for an hour and has been silent for fifteen minutes
 *    is not individually stale, so the figure would claim to be sampled while being
 *    a quarter of an hour old. That predicate is also very nearly unreachable, which
 *    is why it is easy not to see it be wrong: every fixture interval is 300 or null,
 *    and at 300 a row's freshness and its `orphaned` threshold are the same 600
 *    seconds, so it stops being counted at the instant it would have gone stale. The
 *    one row that could reach it is `sess-clock-skew`, whose `startedAt` is four
 *    minutes in the future — and only during the few minutes of the mock's uptime
 *    when its sample has aged out and its skewed start has not.
 *  - *nothing online* → `sampled` with `sampledAt: null`, a state that names a
 *    sample and then declines to date it. The adapter answers `live`, so that would
 *    be a second divergence between the servers.
 */
import { afterEach, describe, expect, it } from "vitest";
import type { Overview, Session } from "@foyer/contract";
import { app } from "./server.js";
import { world } from "./state.js";

/** A venue of its own, so the arithmetic below is over rows this file wrote. */
const SITE = "site-lab";

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;

const saved = world.sessions.slice();
afterEach(() => {
  world.sessions.length = 0;
  world.sessions.push(...saved);
});

/**
 * A session in {@link SITE}, cloned from a real fixture row so every field the
 * contract requires is present and only the ones under test are chosen here.
 *
 * `startedAt` is set from `agoMs` as well, because both servers date a row by the
 * **latest** of its start and its last update: a row whose start is newer than its
 * last update has been heard from at its start.
 */
function openSession(id: string, agoMs: number, intervalSeconds: number | null): Session {
  const template = saved[0];
  if (!template) throw new Error("the fixture world has no sessions to take a shape from");
  const at = new Date(Date.now() - agoMs).toISOString();
  return {
    ...template,
    id,
    acctSessionId: id,
    siteId: SITE,
    state: "online",
    startedAt: new Date(Date.now() - agoMs - HOUR).toISOString(),
    endedAt: null,
    lastUpdateAt: at,
    terminateCause: null,
    freshness: { state: "sampled", sampledAt: at, intervalSeconds },
  };
}

/** Replaces the world's sessions with exactly these. */
function only(...rows: Session[]): void {
  world.sessions.length = 0;
  world.sessions.push(...rows);
}

const overview = async (): Promise<Overview> => {
  const res = await app.request(`/api/v0/overview?siteId=${SITE}`);
  expect(res.status).toBe(200);
  return (await res.json()) as Overview;
};

describe("clientsOnline carries a freshness derived over the rows it counts", () => {
  it("does not age the online count with an orphan it does not contain", async () => {
    /* Thirty seconds old, and counted. */
    const fresh = openSession("sess-online-fresh", 30_000, 300);
    /* Forty days with no Acct-Stop: the classic RADIUS accounting failure, counted
       by `orphanedSessions` and by nothing else. Kept ugly on purpose — it is the
       row that makes the difference visible. */
    const orphan = openSession("sess-online-orphan", 40 * DAY, 300);
    only(fresh, orphan);

    const o = await overview();
    expect(o.clientsOnline.value, "the orphan must not be counted as online").toBe(1);
    expect(o.orphanedSessions?.value, "and it must be counted somewhere").toBe(1);

    /*
     * **The non-vacuity half.** Under the rule this file rejects — one timestamp
     * over every open session — the same world answers `stale`, forty days old. If
     * these two ever agree, the world stopped being able to tell the two
     * implementations apart and every assertion below is worth nothing.
     */
    const unionAge = Date.now() - Date.parse(orphan.freshness.sampledAt!);
    expect(
      unionAge / 1000 > 2 * 300,
      "this world no longer distinguishes a freshness over the online rows from one " +
        "over every open row, so it is not evidence for either",
    ).toBe(true);

    expect(
      o.clientsOnline.freshness.state,
      "the one session this figure counts was updated thirty seconds ago; a `stale` " +
        "here is the forty-day orphan's age wearing the online count's label",
    ).toBe("sampled");
    expect(o.clientsOnline.freshness.sampledAt).toBe(fresh.freshness.sampledAt);
    expect(o.clientsOnline.freshness.intervalSeconds).toBe(300);
  });

  it("is stale when a counted row is older than twice the declared interval", async () => {
    /*
     * A NAS that asked for an hour, silent for fifteen minutes. Still `online` —
     * `2 × max(3600, 300)` has not elapsed — so the figure counts it, and the
     * figure is therefore fifteen minutes old against the five-minute floor it
     * declares. Individually the row is not stale at all, which is exactly why the
     * per-row predicate could not see this.
     */
    only(openSession("sess-online-slow", 15 * MIN, 3600));

    const o = await overview();
    expect(o.clientsOnline.value, "a slow NAS is quiet, not gone").toBe(1);
    expect(o.orphanedSessions?.value).toBe(0);
    expect(
      o.clientsOnline.freshness.state,
      "the count is fifteen minutes old and declares a 300-second interval; a reader " +
        "must be able to reach `stale` from those two numbers alone",
    ).toBe("stale");
    expect(o.clientsOnline.freshness.intervalSeconds).toBe(300);
  });

  it("is live, and dated, when it counts nothing", async () => {
    /* One orphan and nothing online: zero is what this server knows at this
       instant, and the orphan's forty days must not become the zero's age either. */
    only(openSession("sess-online-only-orphan", 40 * DAY, 300));

    const o = await overview();
    expect(o.clientsOnline.value).toBe(0);
    expect(o.orphanedSessions?.value).toBe(1);
    expect(
      o.clientsOnline.freshness.state,
      "there is no sample behind a figure with no rows in it",
    ).toBe("live");
    expect(
      o.clientsOnline.freshness.sampledAt,
      "`sampled` with a null `sampledAt` names a sample and then declines to date it",
    ).not.toBeNull();
  });
});

/**
 * ─────────────────────────────────────────────────────────────────────────────
 *  The second half: the orphan count dates itself
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * Were `orphanedSessions` a bare `integer`, the one figure whose whole meaning is
 * *these have not been heard from* could not say when any of them last was. Every
 * assertion in this block would be **red against that shape for the same reason**:
 * a number has no `freshness` to read. That is a weak red on its own — a compile
 * error is not a measurement — so each case below is also pinned against the
 * derivation that would be wrong rather than merely absent, and each of these fails:
 *
 *  - *the oldest, not the newest* → sabotage `Math.min` to `Math.max` in
 *    `orphanFreshness`, and a set holding a ten-minute orphan and a forty-day one
 *    reports ten minutes. That is the difference between "an access point rebooted"
 *    and "nothing has reaped these since July", which is the whole reason the field
 *    exists.
 *  - *no interval* → sabotage `orphanFreshness` to return `countedFreshness(...)`,
 *    the online figure's derivation, and the card gains `intervalSeconds: 300` — a
 *    cadence for a set of rows defined by nothing arriving. **Only the interval
 *    assertion goes red under this one**, and the reason is worth keeping: `min`
 *    over the orphaned rows is the same timestamp either way, so the two
 *    derivations differ here in what they *declare*, not in what they measured.
 *  - *two figures, two timestamps* → derive the orphan figure over `online`
 *    instead of `orphaned`. This is the shared-timestamp defect above arriving from
 *    the other side, and it reddens all three cases at once.
 *
 * The empty case is the one that cannot be sabotaged from the world, because both
 * branches answer `live`: it is pinned by asserting the date is *now* rather than
 * the orphans' age, which is the shape that fails if clause 1 is ever derived from
 * the counted rows instead of from the clock.
 */
describe("orphanedSessions carries a freshness, and it is not clientsOnline's", () => {
  it("is dated by the oldest of the rows it counts, with no cadence to declare", async () => {
    /* Two orphans fifteen days apart, so `min` and `max` are different answers —
       a single orphan cannot tell them apart. Both are past `2 × 300s`. */
    const older = openSession("sess-orphans-older", 40 * DAY, 300);
    const newer = openSession("sess-orphans-newer", 25 * DAY, 300);
    /* And one fresh online row, so the two figures have two different subjects. */
    const fresh = openSession("sess-orphans-fresh", 30_000, 300);
    only(older, newer, fresh);

    const o = await overview();
    expect(o.orphanedSessions?.value, "both orphans are counted").toBe(2);
    expect(o.clientsOnline.value, "and neither is counted as online").toBe(1);

    const f = o.orphanedSessions!.freshness;
    expect(
      f.sampledAt,
      "the count is dated by the oldest of the rows it counts, never the newest: " +
        "twenty-five days is the more recent orphan, and reporting it hides the row " +
        "that says nothing is reaping these",
    ).toBe(older.freshness.sampledAt);
    expect(
      f.intervalSeconds,
      "a row joins this set after 2 × max(its own interval, 300s), so the set has no " +
        "one cadence — declaring 300 prints `every 5 minutes` beside rows defined by " +
        "nothing arriving",
    ).toBeNull();

    /*
     * **The non-vacuity half**, and it is the same one `clientsOnline` carries.
     * Two figures over disjoint row sets must answer with two different instants.
     * If they ever agree, this world stopped being able to tell a per-figure
     * derivation from a shared one and nothing above is evidence for either.
     */
    expect(
      f.sampledAt,
      "the online and orphan figures carry one timestamp between them, so neither is " +
        "derived over the rows it counts",
    ).not.toBe(o.clientsOnline.freshness.sampledAt);
  });

  it("says stale, which is true of every row it counts and redundant with the value", async () => {
    only(openSession("sess-orphans-one", 40 * DAY, 300));

    const o = await overview();
    /*
     * `stale` is asserted rather than assumed because the alternative that reads
     * well — `live`, on the argument that the count is exact at the instant it is
     * computed — is the one that makes a generic client drop the timestamp: the
     * console's `live` branch renders "Live figure" and no date at all. The
     * contract states the redundancy out loud instead of hiding it, and the card
     * spends its line on the sample time rather than on this verdict.
     */
    expect(o.orphanedSessions?.freshness.state).toBe("stale");
  });

  it("is live, and dated now, when nothing is unaccounted for", async () => {
    /* One fresh online session and no orphans. */
    only(openSession("sess-orphans-only-online", 30_000, 300));

    const o = await overview();
    expect(o.orphanedSessions?.value).toBe(0);
    const f = o.orphanedSessions!.freshness;
    expect(f.state, "there is no sample behind a figure with no rows in it").toBe("live");
    expect(f.sampledAt, "`live` with no date names a claim and then declines to date it").not.toBeNull();
    /* Dated from the clock, not from whatever rows happen to be around: an empty
       orphan count must not borrow the online figure's sample. */
    expect(
      Date.now() - Date.parse(f.sampledAt!),
      "zero orphans is what this server knows at this instant, not something it was " +
        "told when the last session checked in",
    ).toBeLessThan(60_000);
  });
});
