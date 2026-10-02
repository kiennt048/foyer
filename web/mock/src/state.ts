/**
 * Mutable world state.
 *
 * The mock is stateful on purpose. A stateless mock demonstrates nothing about the
 * flows that actually matter: redeeming a voucher must mark it used, create a grant
 * and make a session appear; revoking it must end that session. Reviewing a
 * prototype where the same voucher can be redeemed forever teaches nobody anything.
 */
import { eraseByEmail, eraseByPhone, eraseConsents } from "./guestdata.js";
import { erasePhoneCodes } from "./phone.js";
import { createHash, randomBytes, randomUUID, timingSafeEqual } from "node:crypto";
import { claimSetup, newSetupCode, openSetup, reopenSetup } from "./auth.js";
import type {
  AuditEntry,
  DataSubjectKind,
  Failure,
  Nas,
  NasProbe,
  Operator,
  OperatorPermission,
  PortalAuthorization,
  RetentionCategoryId,
  RetentionPolicy,
  Plan,
  Session,
  Voucher,
} from "@foyer/contract";
import {
  ROLE_PERMISSIONS,
  VOUCHER_ALPHABET,
  VOUCHER_LENGTH,
  needsCoaVendor,
  normaliseVoucherCode,
} from "@foyer/contract";
import {
  buildWorld,
  freshnessStateAt,
  PLANS,
  UNVERIFIED_CAPABILITIES,
  type World,
} from "@foyer/fixtures";

/**
 * **The state a real installation actually starts from**, and the only world in
 * this repository that can render it.
 *
 * `FOYER_MOCK_FRESH=1` serves a deployment that has been migrated and first-booted
 * and nothing more: **no operator** — Foyer ships no credential, so first boot opens
 * the console's first-access setup instead (`SETUP_CODE` below) — and **no sites**,
 * therefore no devices, no vouchers, no sessions, no accounts, no garden entries and
 * no portal settings, because every one of those hangs off a site.
 *
 * It exists because of two things together, and the second is what makes it
 * necessary rather than nice:
 *
 *   * **The operation that creates the first site is invisible elsewhere**, because
 *     every other world here already has three sites. Every drive there starts
 *     after the step that creates one.
 *   * **One venue per install.** `createSite` refuses the second call, so in the
 *     ordinary fixture world it refuses ALWAYS — the `201` the contract declares
 *     would be a response nothing can be driven to produce, which is the exact
 *     vacuity `ProblemType` and `AuditKind` have rules against.
 *
 * So the refusal does not need a workaround; it needs a world with no sites.
 * `operators` is empty too: `deploy/first-boot.sh` creates no administrator, it
 * opens the setup, and the first thing a real install does is complete it
 * (`first-run.test.ts` does the same).
 */
export const FRESH_DEPLOYMENT = process.env.FOYER_MOCK_FRESH === "1";

function freshDeployment(): World {
  const w = buildWorld();
  w.sites = [];
  w.nas = [];
  w.controllers = [];
  w.batches = [];
  w.vouchers = [];
  w.sessions = [];
  w.accounts = [];
  w.authAttempts = [];
  w.gardenEntries = [];
  w.portalSettings = [];
  w.presentMacs = new Set();
  /* Empty rather than removed: the audit log is append-only and a fresh install has
     written nothing to it yet. */
  w.audit = [];
  /* Nobody: the setup code is how the first administrator comes to exist. */
  w.operators = [];
  return w;
}

export const world: World = FRESH_DEPLOYMENT ? freshDeployment() : buildWorld();

/**
 * The rows the fixtures authored, captured before anything can be added to them.
 *
 * Not a naming convention: a snapshot of the ids that existed at boot. Everything
 * that appears later came in through the portal, and the two populations are treated
 * differently below — so the test of "is this row designed?" has to be exact rather
 * than a prefix somebody can accidentally match.
 */
const DESIGNED_SESSION_IDS: ReadonlySet<string> = new Set(world.sessions.map((s) => s.id));

/**
 * How many portal-created sessions one site keeps.
 *
 * ── the failure this exists to stop ──────────────────────────────────────────
 *
 * Measured: a fresh world has 33 sessions on `site-hcm`, one of them with a username.
 * An unbounded mock left running while the suites drove the portal had 168, with 28 — and the
 * 135 extra rows are all healthy, all recent, all identical in shape. They arrive at
 * the front of the list, so they push the authored ones down: the orphan that has
 * been "online" for 40 days, the counter that wraps through zero, the duplicate MAC,
 * the row whose clock is four minutes ahead. The console asks for 100 rows. Past
 * roughly 100 arrivals the pathological rows are not on the page at all.
 *
 * `docs/rules/frontend.md` calls a self-healing fixture world worse than a static
 * one, "because it removes the case it exists to carry". Displacement removes it just
 * as effectively as deletion, and more quietly: nothing is missing, the screen is
 * simply full of something else. `FROZEN_SESSION_IDS` and `sim.test.ts` were built to
 * stop those rows from *changing*, and neither of them can see this.
 *
 * ── why a bound, and not a sort ──────────────────────────────────────────────
 *
 * The obvious alternative was to pin the authored rows to the top of a default order.
 * Rejected: it invents an ordering no backend will have — `radacct` has no "this row
 * is interesting" column — so the console would be reviewed against a list whose
 * shape is a property of the mock. A review of the console is an operator saying yes
 * to what they can see, and a sort that only exists here makes that yes about the
 * wrong thing.
 *
 * A bound claims something narrower and true: **the fixture world is a specimen, not
 * a deployment.** Every generated row is the viewer's own traffic, and it is worth
 * keeping only while it is recent — long enough to watch your own session appear at
 * the top of the table, which is one of the four verbs `sim.ts` exists to make
 * visible. The hundredth copy of it demonstrates nothing that the first did not.
 *
 * ── why this number ─────────────────────────────────────────────────────────
 *
 * Not taste: 33 designed rows on the busiest site plus this window must fit inside
 * one default page (`limitOf`, 50), so no authored row can be pushed off the first
 * page by arrivals no matter how many arrive. `state.test.ts` asserts that
 * relationship rather than this number, so raising either one fails there.
 *
 * The world only grows when a session is created, so trimming there is enough — there
 * is no tick hook, and `FOYER_MOCK_LIVE=0` therefore bounds the world exactly as the
 * live clock does.
 */
export const LIVE_SESSION_WINDOW = 12;

/**
 * A portal-created session joins the world, and the oldest ones beyond the window
 * leave it.
 *
 * Eviction never considers a designed row, which is the property that matters and
 * the one `state.test.ts` drives. What it does do is delete an `online` row, which no
 * backend would — so it is stated in the boot banner beside the compressed clock and
 * the frozen-world switch rather than left to be discovered. Retention is Foyer's
 * (`docs/rules/radius.md`); a window twelve deep is a very short one.
 */
function admitSession(session: Session): void {
  world.sessions.unshift(session);

  const surplus: string[] = [];
  let kept = 0;
  /* Front to back is newest to oldest, because arrivals `unshift`. The first
     `LIVE_SESSION_WINDOW` generated rows for this site stay; everything behind them
     goes. */
  for (const s of world.sessions) {
    if (s.siteId !== session.siteId || DESIGNED_SESSION_IDS.has(s.id)) continue;
    if (kept < LIVE_SESSION_WINDOW) kept += 1;
    else surplus.push(s.id);
  }
  if (!surplus.length) return;

  const evicted = new Set(surplus);
  for (let i = world.sessions.length - 1; i >= 0; i--) {
    if (evicted.has(world.sessions[i]!.id)) world.sessions.splice(i, 1);
  }
  /* A voucher pointing at a row that is no longer there would be the mock telling
     the console to fetch a `404`. The voucher itself keeps its state: it is still in
     use, we simply no longer hold the accounting row for it. */
  for (const v of world.vouchers) {
    if (v.currentSessionId && evicted.has(v.currentSessionId)) v.currentSessionId = null;
  }
}

export function site(siteId: string) {
  return world.sites.find((s) => s.id === siteId);
}

/**
 * Recomputes freshness against the current clock.
 *
 * Fixtures are built once at boot, so a sample that was two minutes old then is an
 * hour old after the mock has been running for an hour. Leaving the state baked in
 * would make the prototype claim data is fresh when the timestamp beside it says
 * otherwise — the exact contradiction this design exists to avoid, reproduced in
 * the tool built to demonstrate avoiding it.
 *
 * Real accounting behaves the same way: `stale` is a function of now, not of when
 * the row was written.
 *
 * Since the clock in `sim.ts` moves the world between requests, this also has to
 * agree with the alarm derivation to the letter — a row saying `sampled` beside an
 * alarm counting it as stale is precisely the contradiction the design exists to
 * avoid. So there is one definition, in `@foyer/fixtures`, and both call it.
 */
export function withCurrentFreshness(s: Session): Session {
  const fresh = withDerivedFreshness(s);
  return withDerivedState(fresh);
}

function withDerivedFreshness(s: Session): Session {
  if (!s.freshness.sampledAt) return s;
  const state = freshnessStateAt(s.freshness, Date.now());
  if (state === s.freshness.state) return s;
  return { ...s, freshness: { ...s.freshness, state } };
}

/**
 * Recomputes `Session.state` against the current clock, from the rule the contract
 * states — **`SessionState`.**
 *
 * Without it, this mock would serve whatever the fixtures authored while the Go
 * server derived a state from `radacct`, so the two could disagree about how many
 * guests are on the network and neither would be wrong, because nothing said. The
 * rule is not invented here: it is the one both implementations compute —
 * `2 × max(interval, 300 s)` of silence on an open session — written down so that
 * *which field carries it* stops being a per-implementation choice.
 *
 * Two guards, and both are load-bearing:
 *
 *  - **`ended` and `unknown` are never overwritten.** A session with an `Acct-Stop`
 *    is closed and a session with no accounting at all has no silence to measure —
 *    the controller-API path where there is no Access-Accept in which to ask for
 *    interim updates. `sess-no-accounting` must keep reading *unknown*, never
 *    *orphaned*, because nothing went quiet: nothing ever spoke.
 *  - **The floor is `max(interval, 300)`**, matching the server's
 *    `greatest(coalesce(acctinterval, 300), 300)`. `Freshness.stale` has no floor,
 *    which is exactly why the two fields can disagree on a NAS that asked for less
 *    than five minutes — and why they agree everywhere else.
 *
 * It is applied on read rather than written into the fixtures, for the same reason
 * freshness is: the authored rows stay exactly as ugly as they were built, and a
 * world that healed itself would remove the case it exists to carry.
 */
const STATE_INTERVAL_FLOOR_SECONDS = 300;

function withDerivedState(s: Session): Session {
  if (s.state === "ended" || s.endedAt) return s;
  /* No accounting at all. Decided before any arithmetic — see above. */
  if (s.state === "unknown" || s.freshness.state === "absent" || !s.freshness.sampledAt) return s;

  const heardAt = Math.max(
    Date.parse(s.freshness.sampledAt),
    s.lastUpdateAt ? Date.parse(s.lastUpdateAt) : 0,
    Date.parse(s.startedAt),
  );
  const interval = Math.max(s.freshness.intervalSeconds ?? 0, STATE_INTERVAL_FLOOR_SECONDS);
  const silent = (Date.now() - heardAt) / 1000 > 2 * interval;
  const state: Session["state"] = silent ? "orphaned" : "online";
  return state === s.state ? s : { ...s, state };
}

// ── portal clients ───────────────────────────────────────────────────────────

/**
 * A simulated client. In production this is derived from the NAS redirect
 * parameters and confirmed by `NASDriver.VerifyPresence()`; here it stands in for
 * the same binding so the portal can be exercised end to end.
 */
export interface PortalClient {
  id: string;
  mac: string;
  nasId: string;
  siteId: string;
  /** Whether the driver has confirmed this MAC is really associated. */
  verified: boolean;
  /**
   * Whether the controller will actually let this MAC on **after** the grant is
   * written.
   *
   * A second boolean rather than a mode on `verified`, because the two failures
   * happen at opposite ends of the flow and produce opposite pages. `verified:
   * false` is refused *before* anything is granted and the guest keeps their code;
   * this one fails *after* the `303` is on the wire, so the grant is real, the
   * voucher is spent, and the guest is already reading the connected page.
   *
   * It exists so `PortalStatus.state`'s `not-on-network` can be produced by driving
   * this mock rather than by seeding a row — the standing every value in a closed
   * enum has here, and the reason `Revoke` exists on the Go in-memory store.
   */
  authorizes: boolean;
  grantId: string | null;
  originalUrl: string | null;
}

const clients = new Map<string, PortalClient>();

/**
 * What the NAS said, before anybody has checked any of it.
 *
 * Spelled the way UniFi spells it, because that is the only spelling this project
 * has measured: `id` is the client MAC, `ap` the access point's, and `ssid` and
 * `url` are the other two unsigned values a controller appends. `internal/portal`'s
 * `claimFrom` reads the same names and accepts `mac` as an alias for `id`, for the
 * same reason — more than one vendor spells it that way, and a portal that refused
 * would be refusing a guest over a vendor's naming.
 *
 * **Every field here is attacker-controlled.** Anyone within radio range can call
 * the portal with values of their choosing, which is the whole reason
 * `VerifyPresence` exists (`docs/rules/security.md` §4).
 *
 * **And none of them is ever echoed back.** `ssid` is read and capped and then goes
 * nowhere, which is not an oversight: returning any of these would make the portal
 * an oracle for what it believes about an attacker-supplied address, and the
 * contract's `portal` tag says so. `originalUrl` is the one value that reaches a
 * response, and it reaches it as a link the guest chooses to follow after passing
 * the allowlist twice.
 */
export interface NasClaim {
  /** UniFi's `id`, or `mac`. Normalised, or "" when it was not an address. */
  mac: string;
  /** UniFi's `ap`. Normalised, or "" when it was not an address. */
  apMac: string;
  /** UniFi's `ssid`. Venue text that reaches a rendered page, so it is capped. */
  ssid: string;
  /** UniFi's `url`. Allowlisted on the way *in*; null when it was refused. */
  originalUrl: string | null;
}

/** Whether this request carries a NAS redirect at all. */
function isNasClaim(c: NasClaim): boolean {
  return c.mac !== "" || c.apMac !== "";
}

/**
 * The access point the NAS named, resolved to a device this deployment registered.
 *
 * **Derived from `world.nas`, never from a knob.** Without this lookup
 * `newPortalClient` would mint a client from nothing, so an access point no
 * deployment had ever heard of would be answered `200` with a venue's name, its
 * languages and its terms — and *the oracle would have no way to represent
 * `VerifyPresence` failing at all*, so no review and no E2E test could see it.
 *
 * It matches on the MAC and on nothing else, because that is what
 * `internal/api/memory`'s `Placement` matches on — not `enabled`, not the
 * controller's reachability. A narrower rule here would be a second source of truth
 * for a fact that already has one (`docs/rules/database.md`: derive the stand-in
 * from what it stands in for). An unreachable access point still *places* a guest;
 * that failure arrives later, at the controller call, and `?nas=nas-unifi-3` drives
 * it.
 *
 * `undefined` is a refusal, never a default. A portal that fell back to "the only
 * site" for an AP it has never heard of is a portal authorizing devices on a network
 * it does not manage — and in a world with one site that default is invisible until
 * the second site exists.
 */
function placementFor(apMac: string): Nas | undefined {
  const wanted = normaliseMac(apMac);
  if (!wanted) return undefined;
  return world.nas.find((n) => Boolean(n.mac) && normaliseMac(n.mac!) === wanted);
}

/**
 * Mint a client, or refuse.
 *
 * `null` means **the claim named an access point nothing here has registered**, and
 * it is the answer `internal/portal`'s `mint` gives when `Placement` returns
 * `ErrNotFound`: no session row, and therefore no cookie. The caller must not
 * persist anything — a refused entry that left a cookie behind would poison the next
 * request, and a second, well-formed redirect has to be able to recover.
 *
 * ── the one place this deliberately differs from the product ─────────────────
 *
 * With **no** NAS parameters at all the product refuses: `Resolve` returns
 * `ErrNoClaim` and the guest is told. This mock mints one anyway, on the review
 * default or on `?nas=`, because the front door that supplies those parameters —
 * `GET /` on the guest listener — is deliberately **not in the contract** and so is
 * not in this server. Without the fallback every portal screen would be unreachable
 * from `pnpm dev`, and a screen a viewer cannot open is a screen nobody approves.
 *
 * That fallback is not the risk. The risk is a claim, when one *is* made, being
 * ignored, so the refusal has no way to happen. It has one, and both outcomes are
 * drivable from a browser.
 */
export function newPortalClient(opts: {
  present: boolean;
  authorizes?: boolean;
  nasId?: string;
  claim?: NasClaim;
}): PortalClient | null {
  const claim = opts.claim;
  let nas: Nas | undefined;
  if (claim && isNasClaim(claim)) {
    // Both halves are required and each refuses on its own — the same two gates
    // `internal/portal` applies in the same order. A redirect naming a client and
    // no access point cannot be placed; one naming an access point and no client
    // has nothing to verify.
    nas = claim.mac === "" ? undefined : placementFor(claim.apMac);
  } else {
    /* `nasId` is the review affordance behind `?nas=` — see `portalClient` in
       `server.ts`. Unknown ids fall back rather than throwing, and the caller reports
       which device actually answered. */
    nas =
      (opts.nasId ? world.nas.find((n) => n.id === opts.nasId) : undefined) ??
      world.nas.find((n) => n.id === "nas-unifi-1");
  }
  // No placement, no client. In a fresh deployment (`FOYER_MOCK_FRESH=1`) there is
  // no device at all, and that is the honest answer there too.
  if (!nas) return null;

  const mac = (claim && normaliseMac(claim.mac)) || randomMac();
  const c: PortalClient = {
    id: randomUUID(),
    mac,
    // The device the claim placed this guest on. The access point's own address is
    // deliberately not copied alongside it: `nasId` reaches the row that has it, and
    // a second copy of a fact is what `database.md` has a section about.
    nasId: nas.id,
    siteId: nas.siteId,
    // The security rule this models: a grant is never created for a MAC the driver
    // has not confirmed present. `present: false` exercises the rejection path,
    // which is what a portal called directly with forged query parameters looks like.
    verified: opts.present,
    // Defaults to true: a controller that refuses is the exceptional case, and a
    // mock that failed authorization by default would make every other drive of
    // this world exercise the unhappy path.
    authorizes: opts.authorizes ?? true,
    grantId: null,
    // The URL the NAS actually sent, allowlisted on the way in. A fixture constant
    // here would leave this server unable to disagree with an implementation that
    // failed to validate it — on the one field of the
    // connected page that comes from the guest's own browsing, and this project's
    // prime open-redirect target. With no claim there is no NAS to have sent one,
    // so the review default stands in and the connected page has a link to look at.
    originalUrl: claim ? claim.originalUrl : "http://vnexpress.net/",
  };
  clients.set(c.id, c);
  if (opts.present) world.presentMacs.add(mac);
  return c;
}

export function getPortalClient(id: string | undefined): PortalClient | undefined {
  return id ? clients.get(id) : undefined;
}

/**
 * Forget a binding rather than re-point it.
 *
 * `docs/rules/security.md` §4 says to reject a mid-flow MAC change, and the
 * narrow reading is the true one: what may never happen is an existing binding being
 * carried over to a different address, because a grant issued after a presence check
 * on one MAC would then answer for another. Nothing stops an attacker deleting their
 * own cookie and starting again, and this does not pretend to — the new claim mints
 * a new client and passes the same checks.
 */
export function forgetPortalClient(id: string): void {
  clients.delete(id);
}

function randomMac(): string {
  const b = randomBytes(6);
  b[0] = (b[0]! | 0b10) & 0b11111110; // locally administered, not multicast
  return [...b].map((x) => x.toString(16).padStart(2, "0")).join(":");
}

/**
 * `AA-BB-CC-DD-EE-FF`, `aabb.ccdd.eeff` and `AABBCCDDEEFF` are one address.
 *
 * Mirrors `internal/portal.NormaliseMAC`, including the part that matters: an input
 * that does not yield exactly twelve hex digits becomes `""` rather than a partial
 * address, because a truncated MAC that still looks like a MAC is a lookup against
 * the wrong device.
 */
export function normaliseMac(raw: string): string {
  const hex = raw.toLowerCase().replace(/[^0-9a-f]/g, "");
  if (hex.length !== 12) return "";
  return (hex.match(/../g) ?? []).join(":");
}

// ── grants ───────────────────────────────────────────────────────────────────

export interface Grant {
  id: string;
  clientId: string;
  mac: string;
  planKey: keyof typeof PLANS;
  voucherId: string | null;
  username: string | null;
  grantedAt: number;
  expiresAt: number | null;
  state: "connected" | "expired" | "revoked" | "not-on-network";
  /** A plan built for this grant rather than a fixture's — phone sign-in's
      venue-set duration. Absent means `PLANS[planKey]`. */
  plan?: Plan;
  /** The number a phone guest verified. */
  phone?: string;
}

const grants = new Map<string, Grant>();

/**
 * The venue each grant was made in, recorded when it is made.
 *
 * Beside the map rather than on `Grant`, and at creation rather than resolved through
 * the client later: `forgetPortalClient` deletes a binding when a guest's cookie is
 * replaced, and a grant resolved through a deleted client would drop out of the
 * venue's export while `internal/store`'s row — which carries `site_id` — stayed in.
 */
const grantSite = new Map<string, string>();

/** The device each grant's guest arrived through, recorded beside `grantSite` for the same reason. */
const grantNas = new Map<string, string>();

function recordGrant(grant: Grant, client: PortalClient) {
  grants.set(grant.id, grant);
  grantSite.set(grant.id, client.siteId);
  grantNas.set(grant.id, client.nasId);
}

/**
 * Every grant made in one venue, oldest first. For the `exportGuests` document and
 * the trends.
 */
export function grantsInSite(siteId: string): (Grant & { siteId: string })[] {
  const out: (Grant & { siteId: string })[] = [];
  for (const g of grants.values()) {
    if (grantSite.get(g.id) === siteId) out.push({ ...getGrant(g.id)!, siteId });
  }
  return out.sort((a, b) => a.grantedAt - b.grantedAt || (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}

/**
 * The grant, with expiry applied at read time rather than by a timer.
 *
 * Same reasoning as `withCurrentFreshness`: state that is a function of *now* has to
 * be computed from now, or the prototype claims a guest is connected while the
 * timestamp beside it says their hour ran out an hour ago. Revocation is an event and
 * is written when it happens; expiry is the clock passing a number, and nothing has
 * to happen for it to be true.
 */
export function getGrant(id: string | null): Grant | undefined {
  const grant = id ? grants.get(id) : undefined;
  if (!grant) return undefined;
  /* Expiry outranks `not-on-network`, the same order `Grant.StateAt` uses in Go: a
     guest whose hour ran out an hour ago needs to hear that, not that the controller
     refused them at the start of it. */
  if (
    (grant.state === "connected" || grant.state === "not-on-network") &&
    grant.expiresAt !== null &&
    Date.now() > grant.expiresAt
  ) {
    grant.state = "expired";
  }
  return grant;
}

// ── redemption ───────────────────────────────────────────────────────────────

export type RedeemFailure =
  | "not-found"
  | "already-used"
  | "expired"
  | "revoked"
  | "not-present";

export function redeemVoucher(
  client: PortalClient,
  rawCode: string,
): { ok: true; grant: Grant } | { ok: false; reason: RedeemFailure } {
  // Mandatory before any grant is created. Not a formality: without it the portal
  // is a public API for authorizing arbitrary MACs, including devices that never
  // associated. See docs/rules/security.md §4.
  if (!client.verified || !world.presentMacs.has(client.mac)) {
    return { ok: false, reason: "not-present" };
  }

  const code = normaliseVoucherCode(rawCode);
  /*
   * **Scoped to the venue the guest is standing in.** A search of `world.vouchers`
   * entire would let a code printed on a slip at the hotel's front desk be redeemed
   * from the café's portal across town. It is the voucher noun's copy of the scope
   * `loginWithAccount` keeps on the account noun and `listVouchers` keeps on the
   * console — the same read in three places, and the only one of the three whose
   * caller is a stranger within radio range rather than somebody who has signed in.
   *
   * A voucher carries no site of its own: it belongs to a batch, and the batch
   * belongs to a venue. So the scope is resolved through the batch rather than
   * duplicated onto the voucher, which is what the schema does and what
   * `store.GrantRepo.ClaimVoucher` does — one UPDATE joined to `voucher_batches`
   * with `AND b.site_id = $1`.
   *
   * A batch that resolves to nothing is treated as another venue's rather than as
   * this one's. It cannot happen in a world the fixtures built, and the direction a
   * missing row falls is a security decision either way.
   */
  const soldHere = new Set(
    world.batches.filter((b) => b.siteId === client.siteId).map((b) => b.id),
  );
  const voucher = world.vouchers.find((v) => v.code === code && soldHere.has(v.batchId));
  /* A voucher from another venue is `not-found`, never a fifth reason. Anything that
     distinguished the two would tell a stranger which codes exist somewhere else —
     `internal/api/memory`'s `ClaimVoucher` and `store.GrantRepo.VoucherStateFor` both
     say so in as many words, and both answer *unknown*. */
  if (!voucher) return { ok: false, reason: "not-found" };
  if (voucher.state === "revoked") return { ok: false, reason: "revoked" };
  if (voucher.state === "expired") return { ok: false, reason: "expired" };
  if (voucher.state === "exhausted" || voucher.state === "in-use") {
    return { ok: false, reason: "already-used" };
  }

  const batch = world.batches.find((b) => b.id === voucher.batchId)!;
  const now = Date.now();
  const minutes = batch.plan.durationMinutes ?? 60;

  voucher.state = "in-use";
  voucher.firstUsedAt = new Date(now).toISOString();
  if (batch.expiryMode === "from-first-use") {
    voucher.expiresAt = new Date(now + minutes * 60_000).toISOString();
  }

  const grant: Grant = {
    id: randomUUID(),
    clientId: client.id,
    mac: client.mac,
    planKey: planKeyFor(batch.plan.name),
    voucherId: voucher.id,
    username: null,
    grantedAt: now,
    expiresAt: now + minutes * 60_000,
    state: "connected",
  };
  recordGrant(grant, client);
  client.grantId = grant.id;

  const session = sessionFor(grant, client, voucher);
  deliver(grant, client, session);
  if (client.authorizes) voucher.currentSessionId = session.id;

  return { ok: true, grant };
}

/**
 * Click-through: consent is the whole of the authentication.
 *
 * Which makes `VerifyPresence` the *only* thing standing between an arbitrary MAC
 * and a grant. Every other mode has a credential behind it; this one has a button,
 * so the check that is mandatory everywhere is load-bearing on its own here
 * (`docs/rules/security.md` §4).
 *
 * The duration comes from the venue's setting and is never defaulted here. A mode
 * with no credential and no duration is unmetered public internet handed out on one
 * tap — a decision a venue makes, not one a mock invents.
 */
export function acceptTerms(
  client: PortalClient,
  minutes: number,
): { ok: true; grant: Grant } | { ok: false; reason: "not-present" } {
  if (!client.verified || !world.presentMacs.has(client.mac)) {
    return { ok: false, reason: "not-present" };
  }

  const now = Date.now();
  const grant: Grant = {
    id: randomUUID(),
    clientId: client.id,
    mac: client.mac,
    planKey: "clickThrough",
    voucherId: null,
    username: null,
    grantedAt: now,
    expiresAt: now + minutes * 60_000,
    state: "connected",
  };
  recordGrant(grant, client);
  client.grantId = grant.id;
  deliver(grant, client, sessionFor(grant, client, null));
  return { ok: true, grant };
}

/**
 * The guest's own credential, and the two things it has to check.
 *
 * ── read this before changing it ─────────────────────────────────────────────
 *
 * **This verifies the password, and it is scoped to the venue the guest is standing
 * in.** Finding the account by username across `world.accounts` — the *whole
 * world*, every site — and accepting any non-empty string as the password would let
 * a café's portal sign in a hotel's guest, on the say-so of anybody within radio
 * range, and every drive of this surface would be taken against a portal that
 * authenticated nobody.
 *
 * **It is the console's sign-in rule one surface further out, and this is the half
 * within radio range.** A repair to one caller of a rule is not a repair to the
 * rule, so both callers verify.
 *
 * Both halves have precedent in this repository and neither is new thinking:
 *
 *   - `beginAdminSession` already generates a password per process the way first
 *     boot does, seeds it, and compares in constant time. This does the same, with
 *     the same `samePassword` — one comparison, so a second copy cannot drift.
 *   - Site scope is invisible in a fixture world with one populated site, which is
 *     why `web/fixtures` populates a second one. `accounts` is `UNIQUE (tenant_id, site_id, username)` in migration `00005`, so
 *     a username is a venue's, not a deployment's, and
 *     `internal/api/memory.AuthenticateGuest` scopes on exactly that.
 *
 * The order of the two refusals is `AuthenticateGuest`'s and is deliberate: a wrong
 * password on a disabled account is `bad-credentials`, not `disabled`. Answering
 * `disabled` there would make this endpoint a place to ask whether an account exists,
 * on a surface reachable by anyone within radio range.
 */
export function loginWithAccount(
  client: PortalClient,
  username: string,
  password: string,
): { ok: true; grant: Grant } | { ok: false; reason: "bad-credentials" | "disabled" | "not-present" } {
  if (!client.verified || !world.presentMacs.has(client.mac)) {
    return { ok: false, reason: "not-present" };
  }
  const account = world.accounts.find(
    (a) => a.username === username.trim().toLowerCase() && a.siteId === client.siteId,
  );
  const stored = account ? accountPasswords.get(account.id) : undefined;
  if (!account || stored === undefined || !samePassword(password, stored)) {
    // An unknown username, an account in another venue and a wrong password are one
    // answer, for the reason `beginAdminSession` gives at length: an answer that
    // distinguished them would publish which accounts exist where.
    return { ok: false, reason: "bad-credentials" };
  }
  if (!account.enabled) return { ok: false, reason: "disabled" };

  const now = Date.now();
  // The rest of this world is coupled — redeeming marks the voucher used and makes a
  // session appear, revoking ends it — and this one write was missing, so signing in
  // through the portal left the Accounts screen reading "3 hours ago". A viewer who
  // catches one stale figure stops trusting the ones they cannot check.
  account.lastLoginAt = new Date(now).toISOString();
  const grant: Grant = {
    id: randomUUID(),
    clientId: client.id,
    mac: client.mac,
    planKey: "staffUnmetered",
    voucherId: null,
    username: account.username,
    grantedAt: now,
    expiresAt: null,
    state: "connected",
  };
  recordGrant(grant, client);
  client.grantId = grant.id;
  deliver(grant, client, sessionFor(grant, client, null));
  return { ok: true, grant };
}

/**
 * Phone sign-in's grant: the venue's duration and the number the guest verified.
 * The code was checked by `phone.ts`; presence is checked again here, for the reason
 * every other method checks it — a code proves a number, not a device.
 */
export function grantPhone(
  client: PortalClient,
  phone: string,
  minutes: number,
): { ok: true; grant: Grant } | { ok: false; reason: "not-present" } {
  if (!client.verified || !world.presentMacs.has(client.mac)) {
    return { ok: false, reason: "not-present" };
  }
  const now = Date.now();
  const grant: Grant = {
    id: randomUUID(),
    clientId: client.id,
    mac: client.mac,
    planKey: "clickThrough",
    plan: { durationMinutes: minutes, enforcedBy: "nas" },
    phone,
    voucherId: null,
    username: null,
    grantedAt: now,
    expiresAt: now + minutes * 60_000,
    state: "connected",
  };
  recordGrant(grant, client);
  client.grantId = grant.id;
  deliver(grant, client, sessionFor(grant, client, null));
  return { ok: true, grant };
}

/**
 * Ask the network to let the guest on, after the grant exists.
 *
 * **This is the step the product performs after the `303` is already flushed**, and
 * the reason it is a separate function here is that it is a separate moment there:
 * `internal/api`'s `portalRedirect` writes the redirect, flushes it, and only then
 * calls the driver. Everything before this point has already been promised to the
 * guest.
 *
 * On failure the grant stands and the voucher stays spent — the venue did decide, and
 * un-claiming a code on a controller outage would be a world where one code lets two
 * devices on — but **no session is admitted**, because no traffic ever left the
 * access point, and an `authorize-failed` row is written so the operator has the one
 * record of it that exists anywhere.
 */
function deliver(grant: Grant, client: PortalClient, session: Session | null): Grant {
  if (client.authorizes) {
    if (session) admitSession(session);
    return grant;
  }
  grant.state = "not-on-network";
  world.authAttempts.unshift({
    id: randomUUID(),
    at: new Date(grant.grantedAt).toISOString(),
    // `driver`, because this is the controller refusing after every check Foyer
    // owns has passed — invisible from `radius` and from `portal` alike.
    source: "driver",
    kind: "authorize-failed",
    outcome: "rejected",
    siteId: client.siteId,
    callingStationId: client.mac,
    calledStationId: null,
    username: grant.username,
    nasId: client.nasId,
    voucherId: grant.voucherId,
    accountId: null,
    // Never the controller's body and never the URL it was reached at. A status
    // integer says whether this is an expired key or a wrong site id; the body is
    // text somebody else wrote and the URL can carry a credential.
    params: { failure: "refused", status: 401, attempts: 1 },
  });
  return grant;
}

function planKeyFor(name: string | undefined): keyof typeof PLANS {
  const found = Object.entries(PLANS).find(([, p]) => p.name === name);
  return (found?.[0] as keyof typeof PLANS) ?? "cafe1h";
}

function sessionFor(grant: Grant, client: PortalClient, voucher: Voucher | null): Session {
  const nas = world.nas.find((n) => n.id === client.nasId)!;
  const now = Date.now();
  return {
    id: `sess-live-${grant.id.slice(0, 8)}`,
    acctSessionId: randomBytes(4).toString("hex"),
    siteId: client.siteId,
    nasId: nas.id,
    nasName: nas.name,
    state: "online",
    callingStationId: client.mac,
    macIsRandomised: true,
    username: grant.username,
    identityKind: voucher ? "voucher" : "account",
    voucherId: voucher?.id ?? null,
    ipAddress: `10.20.1.${100 + (world.sessions.length % 120)}`,
    apName: nas.name,
    ssid: "Foyer Guest",
    startedAt: new Date(now).toISOString(),
    endedAt: null,
    lastUpdateAt: new Date(now).toISOString(),
    terminateCause: null,
    // A session that has only just started has no interim update yet. It is
    // `sampled` with a zero-age sample, not `live` — nothing about it is
    // authoritative, and the UI should not imply otherwise.
    freshness: { state: "sampled", sampledAt: new Date(now).toISOString(), intervalSeconds: 300 },
    inputOctets: 0,
    outputOctets: 0,
    // Nothing has wrapped: no second accounting record exists yet to compare with.
    countersWrapped: false,
    inputOctetsTotal: 0,
    outputOctetsTotal: 0,
    sessionSeconds: 0,
    plan: grant.plan ?? PLANS[grant.planKey]!,
  };
}

// ── revocation ───────────────────────────────────────────────────────────────

/**
 * Revoking a voucher, and the separate driver call that may not survive it.
 *
 * The return carries a `Failure` and **no prose**. A `driverError` sentence composed
 * here — in Vietnamese — would go straight into a toast on an English console in
 * `admin/src/screens/Vouchers.tsx`. That is the server-composed-prose defect, and
 * the guards against it walk problem documents, which a string in the body of a
 * `200` slips past. `Failure` is the schema that closes that hole: same
 * `ProblemType` keyspace as a refusal, no HTTP members, and the numbers and names
 * the console needs in `params` rather than in a sentence.
 */
export function revokeVoucher(voucherId: string): {
  voucher: Voucher;
  sessionEnded: boolean;
  failure?: Failure;
} | null {
  const voucher = world.vouchers.find((v) => v.id === voucherId);
  if (!voucher) return null;

  voucher.state = "revoked";
  voucher.revokedAt = new Date().toISOString();

  const session = world.sessions.find(
    (s) => s.voucherId === voucherId && (s.state === "online" || s.state === "orphaned"),
  );
  if (!session) return { voucher, sessionEnded: false };

  const nas = world.nas.find((n) => n.id === session.nasId);
  // Revoking a voucher does not by itself end a session — that needs a driver call,
  // and whether it can be made depends on the NAS being reachable. The response says
  // what actually happened rather than what was intended.
  if (!nas || nas.reachability !== "ok") {
    voucher.currentSessionId = session.id;
    return {
      voucher,
      sessionEnded: false,
      failure: {
        type: "/problems/session-end-failed",
        /* Both of these change what the operator does next, and neither can be
           recovered from a sentence: unreachable is a network problem, expired
           credentials are a controller problem, and a missing NAS row is neither. */
        params: {
          nasName: nas?.name ?? null,
          nasReachability: nas?.reachability ?? "unknown",
        },
      },
    };
  }

  session.state = "ended";
  session.endedAt = new Date().toISOString();
  session.terminateCause = "Admin-Reset";
  voucher.currentSessionId = null;

  const grant = [...grants.values()].find((g) => g.voucherId === voucherId);
  if (grant) grant.state = "revoked";

  return { voucher, sessionEnded: true };
}

// ── disconnect ───────────────────────────────────────────────────────────────

export type DisconnectOutcome =
  | { kind: "not-found" }
  | { kind: "not-live" }
  | { kind: "unsupported"; nas: Nas | undefined }
  | {
      kind: "attempted";
      session: Session;
      result: {
        disconnected: boolean;
        grantsRevoked: number;
        voucherRevoked: boolean;
        voucher?: Voucher;
        failure?: Failure;
      };
    };

/**
 * `disconnectSession`, in the order the Go handler keeps and for its reasons.
 *
 * 1. Refusals that change nothing — no session, an ended one, a device whose
 *    capability says it cannot end one. This mock has no driver, so the column is
 *    all it has; the Go adapter asks the driver.
 * 2. The withdrawals asked for, **before** the device: the other order is a window
 *    in which a RADIUS client re-authenticates with the voucher it still holds.
 * 3. The device, which here means "reachable or not", as `revokeVoucher` models it.
 */
export function disconnectSession(
  sessionId: string,
  opts: { revokeGrant: boolean; revokeVoucher: boolean },
): DisconnectOutcome {
  const session = world.sessions.find((s) => s.id === sessionId);
  if (!session) return { kind: "not-found" };
  if (session.state === "ended") return { kind: "not-live" };
  const nas = world.nas.find((n) => n.id === session.nasId);
  if (!nas || !nas.capabilities.canDeauthGuest) return { kind: "unsupported", nas };

  const result: {
    disconnected: boolean;
    grantsRevoked: number;
    voucherRevoked: boolean;
    voucher?: Voucher;
    failure?: Failure;
  } = { disconnected: false, grantsRevoked: 0, voucherRevoked: false };
  const mac = normaliseMac(session.callingStationId);
  const openGrants = () =>
    [...grants.values()]
      .map((g) => getGrant(g.id)!)
      .filter(
        (g) => mac !== "" && g.state !== "revoked" && g.state !== "expired" && normaliseMac(g.mac) === mac,
      );

  if (opts.revokeVoucher) {
    const code = normaliseVoucherCode(session.username ?? "");
    const voucher =
      world.vouchers.find((v) => v.state !== "revoked" && v.id === session.voucherId) ??
      world.vouchers.find((v) => v.state !== "revoked" && v.currentSessionId === session.id) ??
      world.vouchers.find((v) => v.state !== "revoked" && code !== "" && normaliseVoucherCode(v.code) === code) ??
      world.vouchers.find(
        (v) => v.state !== "revoked" && openGrants().some((g) => g.voucherId === v.id),
      );
    if (voucher) {
      voucher.state = "revoked";
      voucher.revokedAt = new Date().toISOString();
      result.voucherRevoked = true;
      result.voucher = voucher;
    }
  }
  if (opts.revokeGrant) {
    for (const g of openGrants()) {
      grants.get(g.id)!.state = "revoked";
      result.grantsRevoked++;
    }
  }

  if (nas.reachability !== "ok") {
    result.failure = {
      type: "/problems/session-end-failed",
      /* The same facts `revokeVoucher` carries, plus why. This mock has no packet to
         lose, so an unreachable device is the one cause it can honestly produce. */
      params: { nasName: nas.name, nasReachability: nas.reachability, cause: "no-answer" },
    };
    return { kind: "attempted", session, result };
  }

  session.state = "ended";
  session.endedAt = new Date().toISOString();
  session.terminateCause = "Admin-Reset";
  for (const v of world.vouchers) {
    if (v.currentSessionId === session.id) v.currentSessionId = null;
  }
  result.disconnected = true;
  return { kind: "attempted", session, result };
}

// ── the portal's open authorizations ────────────────────────────────────────

/**
 * A grant's device can end it only when that device is a controller Foyer drives:
 * the guest was authorized there, and that is where it is withdrawn. The Go adapter
 * reads the driver; this world's controllers are UniFi, and a UniFi row's
 * `canDeauthGuest` is the driver's own value.
 */
function grantDevice(grantId: string): Nas | undefined {
  const nasId = grantNas.get(grantId);
  return nasId ? world.nas.find((n) => n.id === nasId) : undefined;
}

function grantCanEnd(grantId: string): boolean {
  const nas = grantDevice(grantId);
  return !!nas && nas.driver === "unifi" && nas.capabilities.canDeauthGuest;
}

function authorizationOut(g: Grant): PortalAuthorization {
  const plan = g.plan ?? PLANS[g.planKey]!;
  const nas = grantDevice(g.id);
  return {
    id: g.id,
    siteId: grantSite.get(g.id)!,
    clientMac: g.mac,
    method: g.voucherId ? "voucher" : g.username ? "account" : g.phone ? "phone" : "click-through",
    planName: plan.name,
    grantedAt: new Date(g.grantedAt).toISOString(),
    ...(g.expiresAt !== null ? { expiresAt: new Date(g.expiresAt).toISOString() } : {}),
    ...(g.voucherId ? { voucherId: g.voucherId } : {}),
    ...(nas ? { deviceName: nas.name } : {}),
    canDisconnect: grantCanEnd(g.id),
  };
}

/** The site's grants neither revoked nor expired, newest first. */
export function openPortalGrants(siteId: string): PortalAuthorization[] {
  return [...grants.values()]
    .filter((g) => grantSite.get(g.id) === siteId)
    .map((g) => getGrant(g.id)!)
    .filter((g) => g.state !== "revoked" && g.state !== "expired")
    .sort((a, b) => b.grantedAt - a.grantedAt || (a.id < b.id ? 1 : a.id > b.id ? -1 : 0))
    .map(authorizationOut);
}

export type GrantDisconnectOutcome =
  | { kind: "not-found" }
  | { kind: "not-live" }
  | { kind: "unsupported"; nas: Nas | undefined }
  | {
      kind: "attempted";
      grant: PortalAuthorization;
      result: {
        disconnected: boolean;
        grantsRevoked: number;
        voucherRevoked: boolean;
        voucher?: Voucher;
        failure?: Failure;
      };
    };

/**
 * `disconnectPortalGrant`, in `disconnectSession`'s order: refusals that change
 * nothing, then the withdrawals, then the controller. Radacct is not touched —
 * a guest on such a network has no session row, which is the whole point.
 */
export function disconnectPortalGrant(grantId: string, opts: { revokeVoucher: boolean }): GrantDisconnectOutcome {
  const g = getGrant(grantId);
  if (!g) return { kind: "not-found" };
  if (g.state === "revoked" || g.state === "expired") return { kind: "not-live" };
  const nas = grantDevice(g.id);
  if (!grantCanEnd(g.id)) return { kind: "unsupported", nas };
  const before = authorizationOut(g);

  const result: {
    disconnected: boolean;
    grantsRevoked: number;
    voucherRevoked: boolean;
    voucher?: Voucher;
    failure?: Failure;
  } = { disconnected: false, grantsRevoked: 0, voucherRevoked: false };
  if (opts.revokeVoucher && g.voucherId) {
    const voucher = world.vouchers.find((v) => v.id === g.voucherId && v.state !== "revoked");
    if (voucher) {
      voucher.state = "revoked";
      voucher.revokedAt = new Date().toISOString();
      result.voucherRevoked = true;
      result.voucher = voucher;
    }
  }
  g.state = "revoked";
  result.grantsRevoked = 1;

  if (nas!.reachability !== "ok") {
    result.failure = {
      type: "/problems/session-end-failed",
      params: { nasName: nas!.name, cause: "no-answer" },
    };
    return { kind: "attempted", grant: before, result };
  }
  result.disconnected = true;
  return { kind: "attempted", grant: before, result };
}

// ── nas ──────────────────────────────────────────────────────────────────────

export function generateSecret(): string {
  // crypto/rand equivalent. Never a fixed value, not even in a mock — the habit is
  // the point, and a fixture secret has a way of becoming a shipped default.
  return randomBytes(24).toString("base64url");
}

export function createNas(input: {
  siteId: string;
  name: string;
  description?: string;
  driver: Nas["driver"];
  ipAddress?: string;
  nasIdentifier?: string;
  coaPort?: number;
  coaVendor?: NonNullable<Nas["coaVendor"]>;
}): Nas {
  const nas: Nas = {
    id: `nas-${randomUUID().slice(0, 8)}`,
    siteId: input.siteId,
    /* Registered by hand, so every field is the operator's and discovery will never
       overwrite it. This path is only legal for a driver with no controller — the
       handler refuses the others, because a UniFi typed in by hand is a device Foyer
       can never authorize a guest on. */
    source: "manual",
    controllerId: null,
    /*
     * **The secret this device presents, named — because one was just minted for it.**
     *
     * The route answers `201` with a freshly generated `sharedSecret` in the same
     * body, and `null` here would be a secret handed to an operator that no row
     * claims. **A secret nothing names is a secret nothing can rotate** —
     * `rotateNasSecret` acts on the RADIUS client's secret, and the object model makes
     * `shared_secrets` the noun between the controller and the client for exactly
     * that reason, so a device with no id there is one noun short of the operation
     * the console offers on it.
     *
     * `internal/apistore.RegisterAccessPoint` creates the `shared_secrets` row and
     * writes `SharedSecretID: ptr(ss.ID)` before it creates the client. A profile of
     * one, because a device typed in by hand has no controller-level RADIUS profile
     * to belong to — which is also why `discoverFromController` overwrites this with
     * the controller's `sec-<controller>` the moment a device turns out to have one.
     * The id is minted here rather than in the route for the same reason Go mints it
     * before the client: the device is not registerable without it.
     */
    sharedSecretId: `sec-${randomUUID().slice(0, 8)}`,
    mac: null,
    radiusVia: "self",
    name: input.name,
    description: input.description,
    driver: input.driver,
    model: null,
    firmware: null,
    ipAddress: input.ipAddress ?? null,
    nasIdentifier: input.nasIdentifier ?? null,
    /*
     * **The operator's value, or nothing — and never a port this file invented.**
     *
     * A default-port table keyed on the driver (3799 for `unifi`, `mikrotik` and
     * `coa`, `null` for `omada` and `ruckus`) would be a second copy of
     * `DRIVER_SHAPES` wearing different units, and one that drifts — it would give
     * UniFi a CoA port where `DRIVER_SHAPES.unifi` is `controller-api` on both edges.
     * A hand-maintained restatement of a rule is derived from what it stands in for,
     * or it is not written.
     *
     * Not written, because the thing it would stand in for is not `DRIVER_SHAPES` at
     * all. `Nas.coaPort` is *the operator's override*, and the default lives one
     * layer down at send time — `internal/nas.DefaultCoAPort`, applied by `CoAAddr`
     * when the stored port is zero. `internal/apistore/nasdrivers.go` says so in as
     * many words: *"`radius_clients.coa_port` is nullable and zero means the driver's
     * default, which is the one place that constant belongs."* This mock does not
     * send CoA and has no business holding a second copy of that constant.
     *
     * **Filling it in has a price.** `internal/apistore` writes `in.CoaPort`
     * verbatim and its discovery writes none, so every device created against Go
     * carries `null`. A mock that filled in 3799 on either creation path would hide
     * the console's *"Does not use CoA."* reading of that null for every
     * hand-registered MikroTik and Cisco, because the only world the console is
     * driven against would be the one that had filled the field in.
     *
     * So a device registered here reaches the detail screen in the state a real one
     * reaches it in, and the branch that renders *"Not set — Foyer sends to 3799"*
     * is reachable in the fixture world.
     */
    coaPort: input.coaPort ?? null,
    /* Kept only for the driver that reads it. `coaVendor` says which of two RFC 5176
       dialects to speak, so it means nothing on a device that does not speak CoA —
       and a stored value nothing reads is a field that looks like a switch and
       configures nothing. The Go adapter drops it on the same condition. */
    coaVendor: needsCoaVendor(input.driver) ? (input.coaVendor ?? null) : null,
    capabilities: {
      // Unknown until probed. Pessimistic by default: a UI that assumes capability
      // and is wrong offers controls the hardware silently ignores.
      liveReauthorize: false,
      walledGarden: "none",
      hardQuota: false,
      bandwidthLimits: false,
      dynamicVlan: false,
      rotateSecret: false,
      canDeauthGuest: false,
    },
    reachability: "unknown",
    enabled: true,
    secretSet: true,
    secretLastUsedAt: null,
    messageAuthenticator: "unknown",
    /*
     * **`accountingConfigured` is absent here, and absent is the third value.**
     *
     * It said `false`, three lines under two fields set to `unknown` for exactly the
     * opposite reason. In this product `false` is a *measurement*: the schema's own
     * text is *"every session figure for this NAS will be `absent`"*, which is an
     * assertion about a radio nothing has spoken to. `Nas.accountingConfigured` is
     * optional precisely so the property can be missing, and
     * `store.RadiusClient.AccountingConfigured` is `*bool` and says so in place —
     * *"Nil is not measured"*. `internal/apistore.RegisterAccessPoint` sets the
     * field on nothing and the property does not appear in its answer.
     *
     * `probeNas` is what establishes it, and it writes the value it measured — so
     * the three states are register → absent, probe → `true`/`false`, and a probe
     * that reached nothing leaves what was there. The console must render the
     * absence as *not measured* rather than as *not attached*: they send an operator
     * to two different afternoons, one to a network fault and one to a controller's
     * accounting profile.
     */
    lastSeenAt: null,
    createdAt: new Date().toISOString(),
  };
  world.nas.unshift(nas);
  return nas;
}

/**
 * Addresses the mock refuses to reach, and why a viewer needs them.
 *
 * `createNas` registers a device as `unknown`, and a probe that treated anything not
 * *already* failing as reachable would make a device registered through the wizard
 * always probe green, so the unreachable branch of the flow could not be produced
 * from the interface at all. It could only be demonstrated against a pre-seeded
 * fixture, which means a viewer could not see the failure path of the flow they
 * were approving. The branch would exist; the way to reach it would not.
 *
 * So the answer comes from the address, deterministically:
 *
 * | Enter an address in            | The probe answers     |
 * |--------------------------------|-----------------------|
 * | `192.0.2.0/24`  (TEST-NET-1)   | `unreachable`         |
 * | `198.51.100.0/24` (TEST-NET-2) | `credentials-expired` |
 * | anything else                  | `ok`                  |
 *
 * Both ranges are RFC 5737, reserved for documentation and guaranteed never to be a
 * real device — which is the property that makes them safe to overload and makes
 * the rule explicable in one line. The realistic default stays realistic: a `10.x`
 * address, which is what an operator actually types, still probes green, so nothing
 * about the happy path is made stranger to buy the failure path.
 *
 * The two states are kept apart because their fixes are different — one is the
 * network or the address, the other is a key reissued on the controller in a minute
 * — and the wizard says so in three different sentences.
 *
 * The stored state is still sticky, which is what keeps the fixtures meaning what
 * they mean: `nas-unifi-3` stays unreachable and `nas-omada-1` stays
 * credentials-expired however often a viewer presses **Probe again**. Without
 * that, one click would turn the console's only two broken devices green and the
 * fixture set would quietly lose the states it exists to demonstrate.
 *
 * Announced in the boot banner, because a rule only a test knows is a rule the
 * viewer cannot use.
 */
export const UNREACHABLE_PREFIX = "192.0.2."; // RFC 5737 TEST-NET-1
export const CREDENTIALS_EXPIRED_PREFIX = "198.51.100."; // RFC 5737 TEST-NET-2

function probeReachability(nas: Nas): Nas["reachability"] {
  const ip = nas.ipAddress ?? "";
  if (ip.startsWith(UNREACHABLE_PREFIX) || nas.reachability === "unreachable") {
    return "unreachable";
  }
  if (ip.startsWith(CREDENTIALS_EXPIRED_PREFIX) || nas.reachability === "credentials-expired") {
    return "credentials-expired";
  }
  return "ok";
}

/**
 * Deterministic per-driver probe results, so the wizard behaves the same every run.
 *
 * Annotated with the contract type rather than inferred. The runtime validator can
 * only judge a response something actually produced; the annotation judges the ones
 * nothing has produced yet — a branch that would return `unknown` for
 * `messageAuthenticator`, say, is a compile error here instead of a 500 the day a
 * viewer clicks the button.
 */
export function probeNas(nas: Nas): NasProbe {
  const byDriver: Record<Nas["driver"], Nas["capabilities"]> = {
    unifi: {
      // Measured on the Integration API against a real controller.
      liveReauthorize: true,
      walledGarden: "fqdn-sni",
      hardQuota: true,
      bandwidthLimits: true,
      dynamicVlan: false,
      rotateSecret: true,
      canDeauthGuest: true,
    },
    mikrotik: {
      liveReauthorize: true,
      walledGarden: "ip-range",
      hardQuota: false,
      bandwidthLimits: true,
      dynamicVlan: true,
      rotateSecret: false,
      canDeauthGuest: true,
    },
    coa: {
      liveReauthorize: true,
      walledGarden: "fqdn-sni",
      hardQuota: false,
      bandwidthLimits: true,
      dynamicVlan: true,
      rotateSecret: false,
      canDeauthGuest: true,
    },
    ruckus: {
      liveReauthorize: false,
      walledGarden: "ip-range",
      hardQuota: false,
      bandwidthLimits: false,
      dynamicVlan: false,
      rotateSecret: false,
      canDeauthGuest: false,
    },
    omada: {
      liveReauthorize: false,
      walledGarden: "none",
      hardQuota: false,
      bandwidthLimits: false,
      dynamicVlan: false,
      rotateSecret: false,
      canDeauthGuest: false,
    },
  };

  const reachability = probeReachability(nas);
  const reachable = reachability === "ok";

  /*
   * The measurement is computed once and then written to both the record and the
   * result, rather than the result reading the record back. Reading it back is how
   * `Nas.messageAuthenticator: "unknown"` — a value meaning *never established*,
   * which no probe may ever report — reaches the wire; the annotation on this
   * function caught exactly that on the first compile.
   */
  const measured = reachable
    ? {
        capabilities: byDriver[nas.driver],
        messageAuthenticator: nas.driver === "mikrotik" ? ("refused" as const) : ("enforced" as const),
        accountingConfigured: nas.driver !== "omada",
      }
    : null;

  nas.reachability = reachability;
  if (measured) {
    nas.capabilities = measured.capabilities;
    nas.lastSeenAt = new Date().toISOString();
    nas.messageAuthenticator = measured.messageAuthenticator;
    nas.accountingConfigured = measured.accountingConfigured;
  }
  /*
   * Nothing else is touched when the probe reached nothing. The stored record keeps
   * what was last known — an unreachable device does not forget — and the *result*
   * below says `not-measured` rather than repeating it. That pair is the whole point
   * of the two fields: the record answers "what do we know", the probe answers "what
   * did I just establish", and only the second can say "nothing".
   */

  /*
   * The capability caveats this device's driver carries.
   *
   * Keyed on the *vendor*, because that is what `nas.UnverifiedCapabilities` reads:
   * the evidence belongs to the driver, not to the box. Only the drivers that are a
   * controller have a vendor to key on, so a MikroTik or a bare CoA device names
   * nothing — and that is the shipped reading rather than a permissive one, since a
   * driver declaring no evidence at all is treated as claiming nothing.
   *
   * Named even when the probe reached nothing. `messageAuthenticator` and
   * `accounting` go to `not-measured` because they are facts about *this run*; this
   * is a fact about the driver, and it is as true of a device nobody reached.
   */
  const unverified =
    nas.driver === "unifi" || nas.driver === "omada" || nas.driver === "ruckus"
      ? [...UNVERIFIED_CAPABILITIES[nas.driver]]
      : [];

  const warnings: NasProbe["warnings"] = [];
  if (nas.driver === "unifi") {
    /* Unconditional, exactly as the shipped UniFi driver appends it: the Integration
       API's client object carries no field to compare the claimed access point
       against, so `docs/rules/security.md` §4's fourth check is not made. This is
       the warning a registration screen ignoring would render as "a green tick over
       unmeasured claims", so the mock must not drop it. */
    warnings.push({ kind: "access-point-unconfirmed", params: { vendor: nas.driver } });
  }
  if (unverified.length > 0) {
    warnings.push({ kind: "capabilities-unverified", params: { count: unverified.length } });
  }

  return {
    reachability,
    capabilities: nas.capabilities,
    messageAuthenticator: measured ? measured.messageAuthenticator : "not-measured",
    accounting: !measured ? "not-measured" : measured.accountingConfigured ? "configured" : "missing",
    warnings,
    unverified,
    checks: [
      { name: "Liên lạc", result: reachable ? "pass" : "fail", detail: nas.ipAddress ?? "chưa có địa chỉ" },
      {
        name: "Message-Authenticator",
        result: !reachable ? "skip" : nas.messageAuthenticator === "enforced" ? "pass" : "fail",
        detail: !reachable
          ? // A skipped check must not report the stored answer as this probe's. Left
            // in the server's language like its neighbours, so localising these details
            // is one consistent problem here rather than two.
            "Chưa đo được trong lần này."
          : nas.messageAuthenticator === "refused"
            ? "Thiết bị từ chối. Chịu ảnh hưởng của CVE-2024-3596."
            : "Thiết bị chấp nhận require_message_authenticator = yes.",
      },
      {
        name: "Hồ sơ accounting",
        result: !reachable ? "skip" : nas.accountingConfigured ? "pass" : "fail",
        detail: !reachable
          ? "Chưa đo được trong lần này."
          : nas.accountingConfigured
            ? "Sẽ có bản ghi phiên."
            : "Không có bản ghi phiên nào từ thiết bị này. Thiếu accounting nghĩa là không đo được.",
      },
      {
        name: "Walled garden",
        result: !reachable ? "skip" : nas.capabilities.walledGarden === "none" ? "fail" : "pass",
        detail: !reachable
          ? "Chưa đo được trong lần này."
          : nas.capabilities.walledGarden === "fqdn-sni"
            ? "Lọc theo FQDN/SNI."
            : nas.capabilities.walledGarden === "ip-range"
              ? "Chỉ lọc theo dải IP — yếu hơn đáng kể so với FQDN/SNI."
              : "Trình điều khiển không hỗ trợ walled garden.",
      },
    ] as const,
  };
}

// ── the console's own session ────────────────────────────────────────────────

/**
 * The console's own credential, and the reason there is one at all.
 *
 * ── read this before changing it ─────────────────────────────────────────────
 *
 * **This mock verifies the sign-in password.** A `beginAdminSession` that matched on
 * username and `enabled` and accepted *any* non-empty string would make every drive,
 * every review and the differential harness run against a console anybody could
 * sign in to — a limitation far larger than the CSRF one the mock documents.
 *
 * The awkward part is real: `docs/rules/security.md` §7 forbids a static
 * credential anywhere in this repository, so **no fixture
 * operator carries a password** and inventing one here would ship exactly the thing
 * the rule exists to stop.
 *
 * So the mock does what the product does. `deploy/first-boot.sh` and the Go binary
 * **generate** the first administrator's password at first boot, print it once, and
 * refuse to start on a known default. This module generates one the same way, with
 * `crypto.randomBytes`, at world-build time, and prints it in the startup banner.
 * Nothing static is stored and nothing is written to disk.
 *
 * **`FOYER_MOCK_ADMIN_PASSWORD` pins it**, for the drivers that cannot read a
 * banner — `web/e2e`, `tools/codegen/differ`, anything scripted. That is a harness
 * knob, not a default: unset, the password is random per process, so a driver that
 * forgets to set it fails loudly at sign-in instead of quietly signing in as an
 * administrator.
 *
 * The credential is per operator and in memory only. It is seeded to the same
 * generated value for every fixture operator — a first boot has one password and the
 * fixture set is one deployment — and it moves from there: `createOperator` stores
 * the password it was given (otherwise a created operator could not sign in, which
 * nothing could see while any password worked), and `changeOwnPassword` replaces it.
 *
 * Never persisted, never logged, never in a response body — there is a test asserting
 * the last of those over every operation this server answers.
 *
 * **It seeds the fixtures' guest accounts too**, because the portal's
 * `loginWithAccount` is this same check one surface further out. See
 * `accountPasswords`.
 */
export const DEV_PASSWORD: string =
  process.env.FOYER_MOCK_ADMIN_PASSWORD || randomBytes(12).toString("base64url");

/** Whether the password above was pinned by a harness rather than generated. */
export const DEV_PASSWORD_PINNED = Boolean(process.env.FOYER_MOCK_ADMIN_PASSWORD);

/**
 * operator id → the password that operator signs in with. In memory, never written.
 *
 * Seeded for the fixture operators including the disabled ones: `enabled` is checked
 * separately and on purpose, so that a disabled operator with the right password is
 * refused *identically* to an unknown one. Seeding only the enabled rows would make
 * that refusal fall out of a missing map entry rather than out of the rule, which is
 * a second reason for one answer and the kind of thing `database.md` has a section
 * about.
 */
const operatorPasswords = new Map<string, string>(
  world.operators.map((o) => [o.id, DEV_PASSWORD] as const),
);

/**
 * account id → the password that guest signs in with on the **portal**. Same rules.
 *
 * A second population, not a second mechanism: §7 forbids a static credential just
 * as firmly here, so the value is the same one this process generated for the
 * console — a first boot has one password and the fixture set is one deployment —
 * and `FOYER_MOCK_ADMIN_PASSWORD` therefore pins both. That is what lets `web/e2e`
 * and `tools/codegen/differ` drive the guest surface at all: neither can read the
 * startup banner, and unset, no string opens anything.
 *
 * The disabled account is seeded for the reason `operatorPasswords` gives: `enabled`
 * is checked separately, so a disabled guest with the right password is refused by
 * the rule rather than by a missing map entry.
 */
const accountPasswords = new Map<string, string>(
  world.accounts.map((a) => [a.id, DEV_PASSWORD] as const),
);

/** Give a guest account a password — `createAccount`. */
export function setAccountPassword(accountId: string, password: string): void {
  accountPasswords.set(accountId, password);
}

/**
 * Constant-time comparison, because `security.md` §9 asks for one and because a mock
 * that models the shape of the check wrongly teaches the shape wrongly.
 *
 * `timingSafeEqual` throws on unequal lengths, which would leak the length through an
 * exception; the length check is folded into the result instead.
 */
function samePassword(submitted: string, stored: string): boolean {
  const a = Buffer.from(submitted, "utf8");
  const b = Buffer.from(stored, "utf8");
  if (a.length !== b.length) {
    /* Still compare something of equal length, so the refusal costs the same work. */
    timingSafeEqual(b, b);
    return false;
  }
  return timingSafeEqual(a, b);
}

/**
 * Operators on a temporary password somebody else set.
 *
 * Models `operators.must_change_password` (migration 00017), whose only writer is
 * `foyerctl create-operator --must-change-password`. A stored flag rather than a
 * comparison against a password: *"never changed it"* and *"chose it deliberately"*
 * are different states, and a stand-in that cannot tell them apart is an input the
 * harness cannot be wrong about. A fresh world seeds no one here, because no install
 * ships a password — see `SETUP_CODE`.
 */
const temporaryPasswordHolders = new Set<string>();

/** Is this operator on a temporary password? The gate reads it on every request. */
export function mustChangePassword(operatorId: string): boolean {
  return temporaryPasswordHolders.has(operatorId);
}

/**
 * The only writer, mirroring `foyerctl create-operator --must-change-password`.
 *
 * There is no API operation that sets it — a *server* deciding a password must
 * change is expiry policy, a different feature — so there is none here either.
 */
export function setMustChangePassword(operatorId: string, holds: boolean): void {
  if (holds) temporaryPasswordHolders.add(operatorId);
  else temporaryPasswordHolders.delete(operatorId);
}

/**
 * The code `deploy/first-boot.sh` would have printed, in a fresh deployment — and
 * nothing anywhere else, because an install with an administrator has no setup open.
 *
 * Generated per process like every other secret here, printed in the startup banner,
 * and pinned by `FOYER_MOCK_SETUP_CODE` for a driver that cannot read a banner.
 */
export const SETUP_CODE: string | null = FRESH_DEPLOYMENT
  ? process.env.FOYER_MOCK_SETUP_CODE || newSetupCode()
  : null;
if (SETUP_CODE) openSetup(SETUP_CODE);

/**
 * `completeAdminSetup`'s write: an enabled administrator with this username and
 * password, the setup closed in the same step. `"taken"` leaves the setup open.
 */
export function createSetupAdministrator(
  code: string,
  username: string,
  password: string,
): Operator | "closed" | "wrong" | "taken" {
  const claimed = claimSetup(code);
  if (claimed !== "ok") return claimed;
  const name = username.trim().toLowerCase();
  if (world.operators.some((o) => o.username === name)) {
    reopenSetup(code);
    return "taken";
  }
  const operator: Operator = {
    id: `op-${Date.now().toString(36)}-${world.operators.length}`,
    username: name,
    displayName: null,
    role: "administrator",
    enabled: true,
    createdAt: new Date().toISOString(),
    lastLoginAt: null,
  };
  world.operators.unshift(operator);
  setOperatorPassword(operator.id, password);
  return operator;
}

/**
 * Give an operator a password — `createOperator`, and the password change.
 *
 * Clearing the flag happens **here**, in the same call, for the reason
 * `OperatorRepo.SetPassword` gives about doing it in one statement: a change that
 * succeeded while the flag stayed set is a console that refuses every screen after
 * the operator did exactly what it asked, with no way out.
 */
export function setOperatorPassword(operatorId: string, password: string): void {
  operatorPasswords.set(operatorId, password);
  temporaryPasswordHolders.delete(operatorId);
}

/** What this operator signs in with. `undefined` for an operator who has no password. */
export function operatorPassword(operatorId: string): string | undefined {
  return operatorPasswords.get(operatorId);
}

export interface AdminSession {
  id: string;
  operatorId: string;
  csrfToken: string;
}

const adminSessions = new Map<string, AdminSession>();

export function operatorById(id: string): Operator | undefined {
  return world.operators.find((o) => o.id === id);
}

/**
 * Sign in, or `null` — and the caller may not learn which of the three reasons it was.
 *
 * An unknown username, a disabled operator and a wrong password return the same
 * `null` on purpose. An answer that distinguished them would make the sign-in form a
 * place to ask "does this person have an account here", and disabling an account
 * would publish its existence. `signIn` in `openapi.yaml` says the same thing to the
 * client, and `server.test.ts` compares the three answers byte for byte.
 */
export function beginAdminSession(username: string, password: string): AdminSession | null {
  const operator = world.operators.find(
    (o) => o.username === username.trim().toLowerCase() && o.enabled,
  );
  if (!operator) return null;
  const stored = operatorPasswords.get(operator.id);
  if (stored === undefined || !samePassword(password, stored)) return null;
  const session: AdminSession = {
    id: randomUUID(),
    operatorId: operator.id,
    csrfToken: randomBytes(24).toString("base64url"),
  };
  adminSessions.set(session.id, session);
  operator.lastLoginAt = new Date().toISOString();
  return session;
}

export function getAdminSession(id: string | undefined): AdminSession | undefined {
  return id ? adminSessions.get(id) : undefined;
}

export function endAdminSession(id: string | undefined): void {
  if (id) adminSessions.delete(id);
}

/**
 * Every session belonging to this operator, so deleting or disabling them can end
 * them. A console that can disable an account whose browser stays signed in has not
 * disabled anything.
 */
export function endSessionsFor(operatorId: string): void {
  for (const [id, s] of adminSessions) if (s.operatorId === operatorId) adminSessions.delete(id);
}

/** What this operator may do. The v1 derivation lives in `@foyer/contract`. */
export function permissionsFor(operator: Operator): readonly OperatorPermission[] {
  return ROLE_PERMISSIONS[operator.role];
}

/**
 * The enabled administrators, which is the set `/problems/last-administrator` counts.
 *
 * `enabled` and not merely `role`: a deployment whose only administrator is disabled
 * is a deployment nobody can administer, and there is no password-reset channel to
 * recover through. The fixture set has exactly one enabled administrator and one
 * disabled one, so a rule that counted rows rather than enabled rows passes the
 * fixtures and fails the deployment.
 */
export function enabledAdministrators(): Operator[] {
  return world.operators.filter((o) => o.role === "administrator" && o.enabled);
}

// ── the audit log ────────────────────────────────────────────────────────────

let auditSeq = 0;

/**
 * Append one entry. **Append only** — nothing in this file may update or remove one,
 * and the contract has no operation that could
 * (`contract/src/read-only.ts`).
 *
 * Newest first, because that is the order the log is read in and sorting a growing
 * array on every request is a cost the screen would pay for nothing.
 */
export function recordAudit(entry: Omit<AuditEntry, "id" | "at">): AuditEntry {
  const row: AuditEntry = {
    id: `aud-live-${++auditSeq}`,
    at: new Date().toISOString(),
    ...entry,
  };
  world.audit.unshift(row);
  return row;
}


// ── privacy: retention periods and data-subject erasure ──────────────────────

/**
 * The retention categories, their defaults and bounds, and the tables each sweeps.
 *
 * A copy of `store.RetentionCategories` in `internal/store/privacy.go`, because this
 * mock is the oracle the Go server is compared against: `make diff-api` drives
 * `getRetentionPolicy` against both and fails on a default or a bound that differs.
 * This world runs no sweep, so `lastSweep` is never set here.
 */
export const RETENTION_CATEGORIES: readonly {
  category: RetentionCategoryId;
  defaultDays: number;
  minimumDays: number;
  maximumDays: number;
  tables: string[];
}[] = [
  { category: "accounting", defaultDays: 90, minimumDays: 1, maximumDays: 3650, tables: ["radacct"] },
  {
    category: "authAttempts",
    defaultDays: 30,
    minimumDays: 1,
    maximumDays: 3650,
    tables: ["radpostauth", "authorization_events"],
  },
  { category: "portalGrants", defaultDays: 90, minimumDays: 1, maximumDays: 3650, tables: ["portal_grants"] },
  { category: "audit", defaultDays: 730, minimumDays: 30, maximumDays: 3650, tables: ["audit_log"] },
  { category: "guestAccounts", defaultDays: 0, minimumDays: 0, maximumDays: 3650, tables: ["accounts"] },
  {
    category: "marketingConsents",
    defaultDays: 730,
    minimumDays: 30,
    maximumDays: 3650,
    tables: ["marketing_consents"],
  },
];

const retentionOverrides = new Map<RetentionCategoryId, number>();

export function retentionPolicy(): RetentionPolicy {
  return {
    categories: RETENTION_CATEGORIES.map((c) => {
      const override = retentionOverrides.get(c.category);
      return {
        ...c,
        tables: [...c.tables],
        days: override ?? c.defaultDays,
        isDefault: override === undefined,
      };
    }),
  };
}

/** Every change or none; the caller has already checked the bounds. */
export function setRetention(changes: readonly { category: RetentionCategoryId; days: number | null }[]): void {
  for (const ch of changes) {
    if (ch.days === null) retentionOverrides.delete(ch.category);
    else retentionOverrides.set(ch.category, ch.days);
  }
}

/** The tables an erasure searches, per subject kind — `store.Erasers`, sorted. */
const ERASED_TABLES: Record<DataSubjectKind, string[]> = {
  mac: [
    "authorization_events",
    "marketing_consents",
    "phone_codes",
    "portal_addresses",
    "portal_grants",
    "portal_sessions",
    "radacct",
    "radpostauth",
  ],
  account: [
    "accounts",
    "authorization_events",
    "marketing_consents",
    "portal_grants",
    "portal_sessions",
    "radacct",
    "radpostauth",
  ],
  phone: [
    "authorization_events",
    "guest_details",
    "marketing_consents",
    "phone_codes",
    "portal_addresses",
    "portal_grants",
    "portal_sessions",
    "radacct",
    "radpostauth",
  ],
  email: ["guest_details", "marketing_consents"],
};

/**
 * An e-mail address in the portal form's shape, trimmed, or `""`: one `@`, something
 * each side, a dot in the domain, no whitespace, at most 254 — `store.EmailSubject`.
 */
export function erasableEmail(raw: string): string {
  const e = raw.trim();
  const at = e.lastIndexOf("@");
  if (e.length > 254 || /\s/.test(e) || e.split("@").length !== 2 || at < 1 || at === e.length - 1) return "";
  return e.slice(at + 1).includes(".") ? e : "";
}

/** `redact.Email`: the first character and the top-level domain. */
export function maskEmail(e: string): string {
  const at = e.lastIndexOf("@");
  const domain = e.slice(at + 1).toLowerCase();
  const dot = domain.lastIndexOf(".");
  return `${e[0]!.toLowerCase()}•••@•••${dot >= 0 ? domain.slice(dot) : ""}`;
}

/**
 * `foyer_audit_pseudonymise` (migration 00052) for this world: an entry's subject id
 * or name that is the subject becomes `pseudonym:` and sixteen hex digits. The mock
 * has no KEK, so the hash is unkeyed; the shape and the count are the contract's.
 */
function pseudonymiseAudit(kind: DataSubjectKind, canonical: string, is: (v: string) => boolean): number {
  const pseudonym = `pseudonym:${createHash("sha256").update(`${kind}\u0000${canonical}`).digest("hex").slice(0, 16)}`;
  let n = 0;
  for (const e of world.audit) {
    if (!e.subject) continue;
    let hit = false;
    if (is(e.subject.id)) ((e.subject.id = pseudonym), (hit = true));
    if (e.subject.name && is(e.subject.name)) ((e.subject.name = pseudonym), (hit = true));
    if (hit) n++;
  }
  return n;
}

/**
 * A phone number as a person writes one — eight to fifteen digits, an optional
 * leading `+`, spaces, dots, dashes or brackets — as its digit strings (international
 * and, for a Vietnamese number, national) and the exact spellings a username may
 * hold. `store.PhoneSubject`; `null` for anything else.
 */
export function erasablePhone(raw: string): { digits: string[]; spellings: string[] } | null {
  const s = raw.trim();
  if (!/^\+?[0-9 .()-]+$/.test(s)) return null;
  const d = s.replace(/[^0-9]/g, "");
  if (d.length < 8 || d.length > 15) return null;
  let intl = d;
  let national = "";
  if (d.length === 10 && d.startsWith("0")) (intl = `84${d.slice(1)}`), (national = d);
  else if (d.length === 11 && d.startsWith("84")) national = `0${d.slice(2)}`;
  const digits = national ? [intl, national] : [intl];
  const spellings = national ? [`+${intl}`, intl, national] : [`+${intl}`, intl];
  return { digits, spellings };
}

/**
 * A MAC in one of the four spellings a NAS sends, or `""`. Stricter than
 * `normaliseMac`, which accepts any string with twelve hex digits in it: an erasure
 * keyed on `"user-cafe00112233"` would erase a device nobody named.
 */
export function erasableMac(raw: string): string {
  const s = raw.toLowerCase();
  const shaped =
    /^[0-9a-f]{12}$/.test(s) ||
    /^[0-9a-f]{2}(:[0-9a-f]{2}){5}$/.test(s) ||
    /^[0-9a-f]{2}(-[0-9a-f]{2}){5}$/.test(s) ||
    /^[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}$/.test(s);
  return shaped ? normaliseMac(s) : "";
}

/**
 * Removes one data subject from this world and says what went, table by table.
 *
 * The world's rows stand in for the tables they are read from: sessions for
 * `radacct`, RADIUS attempts for `radpostauth`, the others for
 * `authorization_events`, grants and portal clients for the portal's two tables.
 */
export function eraseSubject(
  kind: DataSubjectKind,
  mac: string,
  accountId: string,
  phone?: { digits: string[]; spellings: string[] },
  email?: string,
): {
  removed: { table: string; rows: number }[];
  pseudonymised: { table: string; rows: number }[];
  total: number;
  masked: string;
} | null {
  const counts: Record<string, number> = {};
  const bump = (t: string) => (counts[t] = (counts[t] ?? 0) + 1);
  let masked = "";
  let audited = 0;
  if (kind === "email" && email) {
    masked = maskEmail(email);
    const gone = eraseByEmail(email);
    counts.guest_details = gone.details;
    counts.marketing_consents = gone.consents;
    audited = pseudonymiseAudit(kind, email, (v) => v.toLowerCase() === email.toLowerCase());
  } else if (kind === "phone" && phone) {
    const d = phone.digits[0]!;
    masked = "•".repeat(d.length - 3) + d.slice(-3);
    const isNumber = (v: string | null | undefined) => Boolean(v) && phone.digits.includes(v!.replace(/[^0-9]/g, ""));
    const isName = (v: string | null | undefined) => Boolean(v) && phone.spellings.includes(v!);
    counts.phone_codes = erasePhoneCodes((_, p) => isNumber(p));
    const guest = eraseByPhone(isNumber);
    counts.guest_details = guest.details;
    counts.marketing_consents = guest.consents;
    removeWhere(world.sessions, (s) => {
      if (!isName(s.username)) return false;
      bump("radacct");
      return true;
    });
    removeWhere(world.authAttempts, (a) => {
      if (!isName(a.username)) return false;
      bump(a.source === "radius" ? "radpostauth" : "authorization_events");
      return true;
    });
    const gone = new Set<string>();
    for (const [id, g] of grants) if (isNumber(g.phone)) (grants.delete(id), gone.add(id), bump("portal_grants"));
    for (const [id, cl] of clients) if (cl.grantId && gone.has(cl.grantId)) (clients.delete(id), bump("portal_sessions"));
    audited = pseudonymiseAudit(kind, phone.digits[0]!, (v) => phone.spellings.includes(v));
  } else if (kind === "mac") {
    const hex = mac.replace(/:/g, "");
    masked = `••:••:••:••:${hex.slice(8, 10)}:${hex.slice(10, 12)}`;
    /* In place: other modules hold these arrays, and a reassignment would leave them
       reading the rows this erasure was asked to remove. */
    removeWhere(world.sessions, (s) => {
      if (normaliseMac(s.callingStationId) !== mac) return false;
      bump("radacct");
      return true;
    });
    removeWhere(world.authAttempts, (a) => {
      if (!a.callingStationId || normaliseMac(a.callingStationId) !== mac) return false;
      bump(a.source === "radius" ? "radpostauth" : "authorization_events");
      return true;
    });
    for (const [id, g] of grants) if (normaliseMac(g.mac) === mac) (grants.delete(id), bump("portal_grants"));
    for (const [id, cl] of clients) if (normaliseMac(cl.mac) === mac) (clients.delete(id), bump("portal_sessions"));
    counts.marketing_consents = eraseConsents(mac);
    counts.phone_codes = erasePhoneCodes((m) => normaliseMac(m) === mac);
    audited = pseudonymiseAudit(kind, mac.replace(/:/g, ""), (v) => erasableMac(v) === mac);
  } else {
    const i = world.accounts.findIndex((a) => a.id === accountId);
    if (i < 0) return null;
    const username = world.accounts[i]!.username.toLowerCase();
    world.accounts.splice(i, 1);
    bump("accounts");
    masked = `account ${accountId}`;
    audited = pseudonymiseAudit(kind, accountId, (v) => v === accountId || v.toLowerCase() === username);
  }
  const removed = ERASED_TABLES[kind].map((table) => ({ table, rows: counts[table] ?? 0 }));
  return {
    removed,
    pseudonymised: [{ table: "audit_log", rows: audited }],
    total: removed.reduce((n, r) => n + r.rows, 0),
    masked,
  };
}

function removeWhere<T>(rows: T[], gone: (row: T) => boolean): void {
  for (let i = rows.length - 1; i >= 0; i--) if (gone(rows[i]!)) rows.splice(i, 1);
}
