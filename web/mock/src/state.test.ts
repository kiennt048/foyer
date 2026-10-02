/**
 * The world moves, and the rows it was built to carry stay where they can be seen.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `sim.test.ts` guards the first half of the ugly-fixture rule: the clock may not
 * *change* the pathological rows — the orphan stays 40 days orphaned, the counter
 * that reports `null` never acquires a zero. It cannot see the second half, because
 * nothing about those rows changes here at all.
 *
 * What happens without a bound: every portal arrival `unshift`s a session, nothing
 * removes one, and the console asks for 100 rows. Measured on an unbounded mock left
 * running while the suites drove the portal — 33 sessions on `site-hcm` at boot, one
 * with a username; 168 after, with 28. The 135 additions are healthy, recent and identical
 * in shape, and they sit *in front of* the authored ones. Past roughly a hundred
 * arrivals the orphan, the wrapping counter, the negative duration and the duplicate
 * MAC are not on the page a viewer is looking at.
 *
 * `docs/rules/frontend.md`: a moving fixture that heals itself is worse than a
 * static one, "because it removes the case it exists to carry". Displacement removes
 * it exactly as well as healing does, and more quietly — nothing is missing, the
 * screen is just full of something else. That is what these tests are about.
 *
 * The bound is in `state.ts` with the argument for choosing it over a pinned sort.
 */
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import type { Session } from "@foyer/contract";
import { SESSION_IDS } from "@foyer/fixtures";
import { DEFAULT_PAGE_LIMIT, app } from "./server.js";
import { DEV_PASSWORD, LIVE_SESSION_WINDOW, world } from "./state.js";

const SITE = "site-hcm";

/** The ids the fixtures authored, read before anything is driven. */
const DESIGNED = world.sessions.filter((s) => s.siteId === SITE).map((s) => s.id);

const send = (path: string, init?: RequestInit) => app.request(`/api/v0${path}`, init);

/**
 * The page size the console actually asks `/sessions` for, read out of the screen
 * that asks for it.
 *
 * **The consumer's number, not the server's.** `DEFAULT_PAGE_LIMIT` is what a client
 * with no opinion gets, and the sessions screen is not that client — it sends its own
 * `limit`. So a relationship pinned only against the default guards the wrong number:
 * lower the console to 40 and `33 + 12 <= 50` still holds, while the orphan goes to
 * page two. If the claim is "the fixtures plus a site's arrivals fit inside one page",
 * the number that decides it is the one the console asks for.
 *
 * Read from source rather than duplicated here, because a copy is one more number to
 * drift. Reaching across packages for it is deliberate: this file is asserting a
 * property of the pair, and a pair cannot be checked from one side.
 */
function consolePageLimit(): number {
  const path = fileURLToPath(new URL("../../admin/src/screens/Sessions.tsx", import.meta.url));
  const match = /listSessions\(\{[^}]*\blimit:\s*(\d+)/.exec(readFileSync(path, "utf8"));
  expect(
    match,
    "No `limit:` found in the console's `listSessions` call. The screen has changed " +
      "shape, so this pin can no longer see the number it exists to guard — which is " +
      "a reason to go red, not to pass. Re-point it at wherever the sessions screen " +
      "now states its page size.",
  ).not.toBeNull();
  return Number(match![1]);
}

/**
 * One guest joins through the portal, the way the console's own e2e does it.
 *
 * The account path rather than the voucher path on purpose: it is repeatable. A
 * hundred redemptions would exhaust the fixtures' vouchers, and a test that consumed
 * the world it is asserting about would be measuring its own side effects.
 */
async function arrive(username: string): Promise<void> {
  const context = await send("/portal/context");
  const cookie = context.headers.get("set-cookie")?.split(";")[0] ?? "";
  /* The password the mock generated for this process. A literal such as `anything`
     would pass only against a server that accepted any
     non-empty string. */
  const res = await send("/portal/account", {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded", cookie },
    body: `lang=en&username=${encodeURIComponent(username)}&password=${encodeURIComponent(DEV_PASSWORD)}`,
  });
  expect(res.status, "the guest did not get on, so nothing arrived").toBe(303);
  expect(res.headers.get("location")).toContain("/connected/");
}

async function listSessions(query = ""): Promise<Session[]> {
  const res = await send(`/sessions?siteId=${SITE}${query}`);
  expect(res.status).toBe(200);
  return ((await res.json()) as { items: Session[] }).items;
}

describe("arrivals cannot push the authored rows out of sight", () => {
  const account = world.accounts.find((a) => a.siteId === SITE && a.enabled)!;

  /* Comfortably more than the window, and more than the fixtures' whole session
     count, so the unbounded behaviour would put every authored row past position 100
     — which is what the console asks for. */
  const ARRIVALS = 60;

  it("keeps the relationship the window is chosen for", () => {
    /* The number in `state.ts` is not taste and this is where that is enforced: the
       authored rows plus a full window must fit inside one default page, so no
       arrival can move an authored row onto page two whatever the client's limit. A
       fixture set that grows past this fails here, which is the intended outcome —
       the two numbers are one decision. */
    expect(
      DESIGNED.length + LIVE_SESSION_WINDOW,
      `${DESIGNED.length} authored rows + a window of ${LIVE_SESSION_WINDOW} does not ` +
        `fit in a default page of ${DEFAULT_PAGE_LIMIT}`,
    ).toBeLessThanOrEqual(DEFAULT_PAGE_LIMIT);
  });

  /*
   * And against the limit the console actually sends, which is the one that decides it.
   *
   * The case above pins the window against `DEFAULT_PAGE_LIMIT`, and there is no defect
   * today: 33 authored rows plus a window of 12 is 45, which is inside 50 and therefore
   * inside the 100 the screen asks for, a fortiori. The gap is in what it *guards*. It
   * guards the server's default; nothing guarded the request. Lower `Sessions.tsx` to
   * `limit: 40` and the assertion above stays green at 45 ≤ 50 while the orphan lands on
   * page two — the buried-orphan failure arriving through a different door. This case
   * fails exactly that way.
   */
  it("keeps that relationship against the limit the console actually asks for", async () => {
    const asked = consolePageLimit();

    expect(
      DESIGNED.length + LIVE_SESSION_WINDOW,
      `${DESIGNED.length} authored rows + a window of ${LIVE_SESSION_WINDOW} does not fit ` +
        `in the ${asked} rows the sessions screen asks for. The console's page size and ` +
        "`LIVE_SESSION_WINDOW` are one decision, not two — whichever of them moved, the " +
        "authored anomalies are now reachable only on page two, and every lock that " +
        "measures them is measuring a page of healthy rows instead.",
    ).toBeLessThanOrEqual(asked);

    /* And served, not only arithmetic: the guarantee is about what a client gets back,
       so it is asked for the way the console asks for it. */
    for (let i = 0; i < ARRIVALS; i++) await arrive(account.username);
    const onPage = new Set((await listSessions(`&limit=${asked}`)).map((s) => s.id));
    expect(
      DESIGNED.filter((id) => !onPage.has(id)),
      `authored rows missing from the ${asked}-row page the console requests`,
    ).toEqual([]);
  });

  /*
   * Was the exercise hard enough to mean anything?
   *
   * This closes a hole the other cases here cannot see: every assertion below is about what
   * survives `ARRIVALS` grants, and all of them pass just as happily against the
   * unbounded code if only a handful of grants actually landed. A driver that quietly
   * did less work than the bug needs reports a green tick for a test that measured
   * nothing.
   *
   * The relationship case above pins that the two numbers cannot drift apart. This
   * one pins that the world was actually pushed past the point where the bug bit.
   */
  it("drove more arrivals than fit on the console's first page", async () => {
    for (let i = 0; i < ARRIVALS; i++) await arrive(account.username);
    expect(
      ARRIVALS,
      `Fewer arrivals than this file needs to be meaningful. Unbounded, the authored ` +
        `rows were pushed off the first page only once more than ${DEFAULT_PAGE_LIMIT} ` +
        `live sessions existed; with ${ARRIVALS} the assertions below would pass ` +
        `against the bug they were written for.`,
    ).toBeGreaterThan(DEFAULT_PAGE_LIMIT - DESIGNED.length);
  });

  /*
   * **Present and pathological are different claims, and the failure this file
   * guards is an instance of the second hiding behind the first.**
   *
   * Eviction protects the orphan's *place*. Nothing protects its *state* — and it is
   * one click away: `revokeVoucher` matches `state === "online" || state ===
   * "orphaned"`, and `SESSION_IDS.orphaned` carries `voucherId: inUse[0]`. Revoking
   * that voucher ends the session the fixture exists to keep open, `Status` falls
   * from 202px to 138px, and `narrow-columns-fit` starts measuring a healthier
   * product without ever losing a row.
   *
   * Nothing in the suite revokes a seeded voucher, so this is a live edge
   * rather than a live bug. It is asserted here because the edge is a `page.click()`
   * wide and the mechanism that would catch it is not the window.
   *
   * **What this adds over `sim.test.ts`, honestly.** That file already asserts the
   * orphan stays orphaned — against the *clock*, reading the world directly. Healing
   * the fixture turns both red. What is only here is the
   * pairing: still orphaned **and** still on the page a client is actually served,
   * after enough arrivals to have buried it. Presence and health are separate claims
   * and each file was guarding one of them.
   */
  it("keeps the orphan orphaned rather than merely present", async () => {
    const orphan = (await listSessions()).find((s) => s.id === SESSION_IDS.orphaned);
    expect(orphan, "the orphan is not on the first page at all").toBeDefined();
    expect(orphan?.state, "the orphan was healed — it is present but no longer the case it carries").toBe(
      "orphaned",
    );
    expect(orphan?.endedAt, "an orphan with a stop record is not an orphan").toBeNull();
  });

  it("keeps every authored row, and every one of them on the first page", async () => {
    for (let i = 0; i < ARRIVALS; i++) await arrive(account.username);

    /* The first page, asked for the way a client with no opinion asks for it. Read
       through the real handler rather than off `world.sessions`, because the failure
       being guarded is about what a client is *served*. */
    const firstPage = await listSessions();
    const onPage = new Set(firstPage.map((s) => s.id));

    expect(
      DESIGNED.filter((id) => !world.sessions.some((s) => s.id === id)),
      "an authored row was evicted — eviction must never consider one",
    ).toEqual([]);
    expect(
      DESIGNED.filter((id) => !onPage.has(id)),
      `${ARRIVALS} guests arrived and pushed authored rows off the first page. That is ` +
        "the ugly-fixture rule failing by displacement rather than by healing.",
    ).toEqual([]);

    /* Named individually as well as counted. A regression that dropped exactly the
       interesting ones would satisfy the set comparison above if the fixtures grew
       filler rows to match, and these five are the cases the design was argued
       against. */
    for (const id of [
      SESSION_IDS.orphaned,
      SESSION_IDS.counterWrap,
      SESSION_IDS.clockSkew,
      SESSION_IDS.duplicateA,
      SESSION_IDS.noAccounting,
    ]) {
      expect(onPage.has(id), `${id} is not on the first page`).toBe(true);
    }
  });

  it("keeps the window's worth of arrivals, and the newest one first", async () => {
    const before = new Set(world.sessions.map((s) => s.id));
    for (let i = 0; i < ARRIVALS; i++) await arrive(account.username);

    const all = await listSessions("&limit=200");
    const generated = all.filter((s) => !DESIGNED.includes(s.id));
    expect(generated.length).toBeLessThanOrEqual(LIVE_SESSION_WINDOW);
    expect(generated.length, "arrivals stopped appearing at all").toBeGreaterThan(0);

    /* The window exists to keep, not only to drop. Watching your own session appear
       at the top of the table is one of the four verbs `sim.ts` is built to make
       visible, and a bound that cost that would have bought the wrong thing. */
    expect(all[0]!.id, "the newest arrival is not first").not.toBe(DESIGNED[0]);
    expect(before.has(all[0]!.id), "the newest arrival is not new").toBe(false);
  });

  it("leaves no voucher pointing at a session that is gone", async () => {
    for (let i = 0; i < ARRIVALS; i++) await arrive(account.username);
    const present = new Set(world.sessions.map((s) => s.id));
    /* Eviction deletes a row the console could otherwise link to. A dangling
       `currentSessionId` is the mock telling a client to go and fetch a 404, which is
       a defect it would be inventing rather than modelling. */
    expect(
      world.vouchers
        .map((v) => v.currentSessionId)
        .filter((id): id is string => Boolean(id) && !present.has(id!)),
    ).toEqual([]);
  });
});
