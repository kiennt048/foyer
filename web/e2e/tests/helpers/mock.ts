import { expect, type APIRequestContext } from "@playwright/test";
import { MOCK_URL } from "./env.js";

/*
 * Talking to the mock directly.
 *
 * The mock is stateful (see the frontend design) and the suite must not assume a
 * clean world: it may be a freshly booted server or one that fifty test runs have
 * already chewed through. So nothing here hardcodes an entity — every fixture the
 * tests need is *looked up* at the moment it is needed.
 */

export interface MockVoucher {
  id: string;
  batchId: string;
  code: string;
  state: "unused" | "in-use" | "exhausted" | "expired" | "revoked";
  firstUsedAt: string | null;
  expiresAt: string | null;
  revokedAt: string | null;
}

interface Page<T> {
  items: T[];
  nextCursor: string | null;
}

async function getJson<T>(request: APIRequestContext, path: string): Promise<T> {
  const res = await request.get(`${MOCK_URL}${path}`);
  if (!res.ok()) {
    throw new Error(`GET ${path} → ${res.status()} ${await res.text()}`);
  }
  return (await res.json()) as T;
}

/**
 * Batches to draw test codes from, in order.
 *
 * `batch-daily` is the café batch the task names, and it is small (40) — a suite
 * that only ever drew from it would drain it in two runs and then start failing for
 * a reason that has nothing to do with the product. The larger batches are the
 * fallback, and the error message says what to do when they are gone too.
 */
const VOUCHER_SOURCES = ["batch-daily", "batch-event", "batch-hotel"];

/**
 * An unused voucher code, fetched now rather than hardcoded.
 *
 * Hardcoding one would pass exactly once and then fail forever, which is the
 * failure mode a stateful mock invites. Every redemption in this suite consumes a
 * real code, and the suite must survive being run repeatedly against a server that
 * was not restarted in between.
 */
export async function freshVoucherCode(request: APIRequestContext): Promise<string> {
  for (const batchId of VOUCHER_SOURCES) {
    const page = await getJson<Page<MockVoucher>>(
      request,
      `/api/v0/vouchers?batchId=${batchId}&state=unused&limit=1`,
    );
    const voucher = page.items[0];
    if (voucher) return voucher.code;
  }
  throw new Error(
    `No unused voucher left in ${VOUCHER_SOURCES.join(", ")}. The mock is stateful: ` +
      "restart it (`pnpm -F @foyer/mock dev`) to reseed the fixtures.",
  );
}

/** Looks a voucher up by its code, so a test can assert what the redemption did. */
export async function voucherByCode(
  request: APIRequestContext,
  code: string,
): Promise<MockVoucher | undefined> {
  const page = await getJson<Page<MockVoucher>>(
    request,
    `/api/v0/vouchers?q=${encodeURIComponent(code)}&limit=5`,
  );
  return page.items.find((v) => v.code === code);
}

/**
 * A batch created for one test, with a voucher in it that is safe to revoke.
 *
 * Deliberately not "the first unused voucher in `batch-daily`". Revocation is
 * destructive and the mock keeps state across runs, so a test that ate a seeded
 * voucher every time would eventually start failing for a reason that has nothing to
 * do with the admin UI — which is exactly what happened while this suite was being
 * written. Creating the batch is setup, not the thing under test.
 */
export async function createVoucherBatch(
  request: APIRequestContext,
  label: string,
  count = 3,
): Promise<{ batchId: string; vouchers: MockVoucher[] }> {
  const res = await request.post(`${MOCK_URL}/api/v0/voucher-batches`, {
    data: {
      siteId: "site-hcm",
      label,
      count,
      expiryMode: "from-first-use",
      validForHours: 1,
      plan: {
        name: label,
        durationMinutes: 60,
        dataAllowanceMb: null,
        downKbps: 20_000,
        upKbps: 5_000,
        enforcedBy: "nas",
      },
    },
  });
  if (!res.ok()) throw new Error(`POST /voucher-batches → ${res.status()} ${await res.text()}`);
  const batch = (await res.json()) as { id: string };

  const page = await getJson<Page<MockVoucher>>(
    request,
    `/api/v0/vouchers?batchId=${batch.id}&limit=${count}`,
  );
  return { batchId: batch.id, vouchers: page.items };
}

export interface MockNas {
  id: string;
  name: string;
  driver: string;
  /* Which of the two CoA vendors a `driver: coa` device is, or `null` for nobody-said
     and for every driver that names its vendor by naming itself. Here so a test can
     select a device by what the world already holds rather than by an id — and so the
     round-trip assertion in `coa-vendor.spec.ts` can first prove the value it is about
     to look for is not already somewhere in the fixtures. */
  coaVendor?: string | null;
  coaPort?: number | null;
  reachability: string;
  capabilities: {
    liveReauthorize?: boolean;
    walledGarden?: "none" | "ip-range" | "fqdn-sni";
    hardQuota?: boolean;
    bandwidthLimits?: boolean;
    dynamicVlan?: boolean;
    rotateSecret?: boolean;
    canDeauthGuest?: boolean;
  };
}

export function getNas(request: APIRequestContext, nasId: string): Promise<MockNas> {
  return getJson<MockNas>(request, `/api/v0/nas/${nasId}`);
}

/**
 * The access points in one site, so a test can *select* its own fixtures.
 *
 * **`siteId` is optional and the default is not "everything".** `listNas` with no
 * `siteId` is answered for `site-hcm`, so without a `siteId` the helper is blind to
 * the two other sites — and `deleteNasNamed` below is built on it, so a test that
 * registers a device outside `site-hcm` and omits the site has its cleanup silently
 * remove **nothing** and its `expectNasRemoved` report a leak it cannot see.
 *
 * That is a familiar shape one layer up: a helper whose answer does not depend on the
 * scope it is asked about looks correct for as long as every caller uses one site.
 * `first-run.spec.ts` is a caller that does not.
 */
export async function listNas(
  request: APIRequestContext,
  siteId?: string,
): Promise<MockNas[]> {
  const scope = siteId ? `&siteId=${encodeURIComponent(siteId)}` : "";
  const p = await getJson<Page<MockNas>>(request, `/api/v0/nas?limit=100${scope}`);
  return p.items;
}

/**
 * The mock's simulated tick, read from `X-Foyer-Mock-Clock`.
 *
 * Monotonic within a process and back to zero when one starts, which makes it the
 * cheapest honest way for a test to tell **"the row is gone because I deleted it"**
 * from **"the row is gone because the world was rebuilt underneath me"**. The mock
 * runs under `tsx watch` and restarts whenever anything in its module graph is
 * written — in a shared checkout that is something that happens *to* a run rather
 * than something the run did, and it is the same trap `README.md` documents as "a red
 * run is a stale process until proven otherwise", seen from the other side.
 *
 * Returns `-1` when the clock is frozen (`FOYER_MOCK_LIVE=0`), which carries no tick.
 * Callers treat "cannot tell" as "assume it was mine" — the bias has to be towards
 * reporting a leak, because the failure this guards is silent accumulation.
 */
export async function mockTick(request: APIRequestContext): Promise<number> {
  const res = await request.get(`${MOCK_URL}/health`);
  const match = /tick=(\d+)/.exec(res.headers()["x-foyer-mock-clock"] ?? "");
  return match ? Number(match[1]) : -1;
}

/**
 * Removes the access point a test registered, and proves it is no longer there.
 *
 * `tickAtRegistration` is `mockTick()` read *before* the device was created. If the
 * delete finds nothing and the clock has since gone backwards, the mock restarted and
 * rebuilt its world from the fixtures: the device is genuinely absent, nothing
 * leaked, and failing the run for it would be reporting an environment as a product
 * defect. If the clock has not gone backwards, the device really did go missing
 * somewhere this helper cannot see — which is a leak, and is said as one.
 */
export async function expectNasRemoved(
  request: APIRequestContext,
  name: string,
  tickAtRegistration: number,
  /** The site the device was registered in. Omitted means `site-hcm` — see `listNas`. */
  siteId?: string,
): Promise<void> {
  const removed = await deleteNasNamed(request, name, siteId);
  if (removed === 1) return;
  if (removed > 1) {
    throw new Error(
      `"${name}" matched ${removed} access points. Every registration in this suite ` +
        "names its device uniquely, so cleanup by name has exactly one target.",
    );
  }
  expect(
    await mockTick(request),
    `The test registered "${name}" and it was not there to remove, on a mock that ` +
      "has not restarted. The mock keeps its world across runs, so a device that " +
      "escaped this cleanup is in every later row count, dashboard figure and " +
      "screenshot until someone restarts it by hand.",
  ).toBeLessThan(tickAtRegistration);
}

/**
 * Removes every access point carrying this exact name. Returns how many it removed.
 *
 * Registration writes a row nothing else takes back out, and the mock keeps its world
 * across runs. A suite that registers without deleting grows the access-point list by
 * one per run, per colour scheme, for reasons nobody recorded — and every row count,
 * dashboard figure and screenshot taken afterwards is describing a world that drifted
 * rather than one that was authored.
 *
 * **It is not the only such flow.** Every portal
 * grant `unshift`es a session row, and no delete can pair with it — the console cannot
 * delete a session and never will, because `radacct` belongs to FreeRADIUS. The bound
 * is at the source instead: `admitSession` and `LIVE_SESSION_WINDOW` in
 * `web/mock/src/state.ts` keep a site's arrivals inside one page and never evict an
 * authored row. The reading side carries its own guards anyway — `restsOn` and
 * `SESSIONS_WIDEST_ROWS` — because volume was one reason page one can lose the
 * fixtures and not the only one.
 *
 * Located **by name, not by id**, because the wizard *replaces* the URL the instant
 * the device is created — the history entry is gone before the shared secret reaches
 * the screen — so the browser never holds the id. The name is the one handle the
 * caller chose and can therefore be certain of, which is why every caller passes a
 * unique one.
 *
 * The count comes back rather than a `void` so the caller can assert the cleanup
 * actually happened; a test should not have to be trusted to leave the world as it
 * found it.
 */
export async function deleteNasNamed(
  request: APIRequestContext,
  name: string,
  /** The site to look in. Omitted means `site-hcm` — see `listNas`. */
  siteId?: string,
): Promise<number> {
  const mine = (await listNas(request, siteId)).filter((n) => n.name === name);
  for (const n of mine) {
    const res = await request.delete(`${MOCK_URL}/api/v0/nas/${n.id}`);
    if (!res.ok()) throw new Error(`DELETE /nas/${n.id} → ${res.status()} ${await res.text()}`);
  }
  return mine.length;
}

/**
 * The drivers that cannot push a new shared secret onto the device.
 *
 * Selected from the live capability model rather than named, for the same reason
 * `expectedGatedControls` is computed: a test that hardcodes `nas-mt-1` stops
 * covering anything the day the fixture's driver gains the capability, and does so
 * silently.
 */
export function driversThatCannotPushASecret(all: MockNas[]): MockNas[] {
  return all.filter((n) => n.capabilities.rotateSecret === false);
}

/**
 * One representative access point per distinct (`rotateSecret`, `reachability`) pair.
 *
 * What the rotate-secret assertions are about is the *state*, not the row. The mock
 * is stateful and every registration test leaves an access point behind, so walking
 * the whole list would grow a little longer on every run while covering nothing new
 * — the kind of slow drift that eventually reads as a flake.
 */
export function oneNasPerSecretState(all: MockNas[]): MockNas[] {
  const seen = new Map<string, MockNas>();
  for (const nas of all) {
    const key = `${String(nas.capabilities.rotateSecret)}:${nas.reachability}`;
    if (!seen.has(key)) seen.set(key, nas);
  }
  return [...seen.values()];
}

/**
 * The one combination `rotateNasSecret` answers `409` for: the driver cannot push
 * the value **and** nothing can currently reach the device to be told it by hand.
 *
 * Mirrors `rotationRefused` in `web/mock/src/server.ts`. Neither condition refuses
 * on its own — see the `rotateNasSecret` description in `openapi.yaml`.
 */
export function rotationRefused(nas: MockNas): boolean {
  return nas.capabilities.rotateSecret === false && nas.reachability !== "ok";
}

/**
 * How many capability-gated controls *ought* to render for this NAS.
 *
 * Mirrors `<IfCapable>`: four gated actions on the access-point detail screen, one
 * per capability, and `walledGarden: "none"` counts as unsupported. Computing the
 * expectation from the contract rather than from a number typed into the test means
 * the assertion survives a fixture change — and it fails loudly if the gate and the
 * capability model ever disagree.
 */
export function expectedGatedControls(nas: MockNas): number {
  const c = nas.capabilities;
  /*
   * Two, not four.
   *
   * The device screen does not offer *Assign VLAN* or *Apply now to running
   * sessions*: a VLAN belongs to a policy and "apply now" to a session, so neither is
   * a property of a device record and a device screen offering to set them would be
   * offering to edit something that does not live there. What remains are the two that do have a destination —
   * a rate limit on a voucher batch, and the site's walled garden.
   *
   * This function is the model the assertion is computed from, so it has to track
   * the page. If a control is added to the capability block and not added here, the
   * suite fails and says which way — which is the whole point of computing the
   * expectation rather than typing a number into a test.
   */
  return [c.bandwidthLimits === true, (c.walledGarden ?? "none") !== "none"].filter(Boolean)
    .length;
}

/** Only the parts of a session the capability assertions reason about. */
export interface MockSession {
  id: string;
  nasId: string;
  state?: string;
  plan?: {
    downKbps?: number | null;
    upKbps?: number | null;
    enforcedBy?: "nas" | "foyer-sampled" | "unenforced";
  } | null;
}

export async function listSessions(request: APIRequestContext): Promise<MockSession[]> {
  const p = await getJson<Page<MockSession>>(request, "/api/v0/sessions?limit=200");
  return p.items;
}

/**
 * Sessions whose plan states a rate limit that the access point's driver cannot apply.
 *
 * The pairing is computed from the live model, never named, for the same reason
 * `expectedGatedControls` is: a test that hardcodes a session id stops covering
 * anything the day the fixture moves, and does so silently.
 *
 * There is no field on `Plan` that answers this. `enforcedBy` describes the time and
 * data limits; whether `downKbps` is a fact or a wish is answerable only from
 * `Capabilities.bandwidthLimits` on the session's NAS — a join the session payload
 * does not carry, which is exactly why nothing in the console currently makes it.
 */
export function ratesNoDriverCanApply(
  sessions: MockSession[],
  nas: MockNas[],
): MockSession[] {
  const cannot = new Set(
    nas.filter((n) => n.capabilities.bandwidthLimits === false).map((n) => n.id),
  );
  return sessions.filter(
    (s) => cannot.has(s.nasId) && (s.plan?.downKbps != null || s.plan?.upKbps != null),
  );
}

/**
 * The mirror case: a rate its access point **can** apply.
 *
 * Without it the qualifier assertion is satisfied by a console that hardcodes
 * `data-applied="unapplied"` on every rate it renders — which is the same class of
 * bug as the one the guard exists for, in the other direction, and it would read as
 * a very careful screen. The pair is what makes either assertion mean anything.
 */
export function ratesEveryDriverCanApply(
  sessions: MockSession[],
  nas: MockNas[],
): MockSession[] {
  const can = new Set(nas.filter((n) => n.capabilities.bandwidthLimits === true).map((n) => n.id));
  return sessions.filter(
    (s) => can.has(s.nasId) && (s.plan?.downKbps != null || s.plan?.upKbps != null),
  );
}

/**
 * What the venue advertises to the portal.
 *
 * Read at the moment a test needs it, never typed into the test. The point of most
 * of the assertions that use this is that the portal renders *what the venue said*
 * — a hardcoded expectation here would pass identically against a portal that
 * ignored the venue entirely, which is the exact regression being guarded against.
 *
 * `terms.url` is absent on purpose and is not modelled: the contract does not
 * carry a link to the venue's full terms. See the note in the `terms` schema.
 */
export interface MockPortalContext {
  venueName: string;
  networkName: string | null;
  methods: ("voucher" | "account")[];
  languages: ("en" | "vi")[];
  terms: { summary: Partial<Record<"en" | "vi", string>> };
}

/**
 * The portal context, optionally for a named venue profile.
 *
 * The `venue` parameter matters for the same reason the cookie exists: a spec that
 * selected a profile in the *browser* and then read the context over the API without
 * saying so would be comparing the page against the default venue and asserting they
 * agree — which they would, for the wrong reason.
 */
export function portalContext(
  request: APIRequestContext,
  venue?: string,
): Promise<MockPortalContext> {
  return getJson<MockPortalContext>(
    request,
    `/api/v0/portal/context${venue ? `?venue=${encodeURIComponent(venue)}` : ""}`,
  );
}

/** The portal's own view of the caller. 404 means "no grant", which is a real answer. */
export async function portalStatus(
  request: APIRequestContext,
): Promise<{ status: number; body: unknown }> {
  const res = await request.get(`${MOCK_URL}/api/v0/portal/status`);
  let body: unknown = null;
  try {
    body = await res.json();
  } catch {
    /* a body that is not JSON is still an answer */
  }
  return { status: res.status(), body };
}
