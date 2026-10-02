/**
 * A voucher belongs to a venue, and so does the portal the guest is standing in.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * A `redeemVoucher` that searched `world.vouchers` by code and by nothing else would
 * let a code printed on a slip at the hotel's front desk be redeemed from the café's
 * portal across town — and the console's *Revoke* control sits one column from the
 * search result that finds it.
 *
 * **The same read can go out unscoped in three places**, which is what makes it a
 * shape rather than an oversight:
 *
 *  - `listVouchers`, which must honour its required `siteId`. A dropped scope is
 *    only visible once `batch-hanoi-rooms` exists, because without a second venue's
 *    batch there is nothing it could add.
 *  - `loginWithAccount`, which must not search `world.accounts` unscoped, or a
 *    café's portal signs in a hotel's guest.
 *  - **this one**, on the voucher noun, on the guest surface. The caller of the two
 *    above is either an operator who has signed in or a console screen; the caller of
 *    this one is anybody within radio range.
 *
 * ── the rule, and where it is written down ───────────────────────────────────
 *
 * `internal/api/memory`'s `ClaimVoucher` resolves the batch and refuses when
 * `batch.SiteId != siteID`; `store.GrantRepo.ClaimVoucher` is one UPDATE joined to
 * `voucher_batches` carrying `AND b.site_id = $1`. Both report the refusal as
 * **unknown** rather than as *wrong site*, and `VoucherStateFor` says why in as many
 * words: *"an answer that distinguished them would tell a stranger which codes exist
 * somewhere else."* So the guest reads `e=not-found`, and a fifth reason would be a
 * disclosure rather than a kindness.
 *
 * ── the shape these cases are built to avoid ─────────────────────────────────
 *
 * `docs/rules/frontend.md`: **a second site must be populated, not merely
 * present.** Every assertion here is satisfied by a world in which only one venue
 * sells vouchers — refusing a code that exists nowhere proves nothing about scope. So
 * the world's ability to express the leak is asserted first, by property and never by
 * id, and the refusal is asserted **as a pair** with the same code succeeding at the
 * venue that sold it: half of it alone passes just as well against a portal where
 * redemption is simply broken. Same two defences as `portal-nas-claim.test.ts`, whose
 * cases this file extends to the voucher noun.
 */
import { describe, expect, it } from "vitest";
import { app } from "./server.js";
import { normaliseMac, world } from "./state.js";

const send = (path: string, init?: RequestInit) => app.request(`/api/v0${path}`, init);

/** Two phones, each a locally administered address per `security.md` §6. */
const CAFE_GUEST_MAC = "02:1a:2b:3c:4d:5e";
const HOTEL_GUEST_MAC = "02:aa:bb:cc:dd:ee";

/** The two populated venues. */
const CAFE = "site-hcm";
const HOTEL = "site-hanoi";

/**
 * The access point a guest claims to be standing at, chosen **by property**.
 *
 * Never by id: a hardcoded `nas-hcm-01` breaks the day somebody renames a fixture
 * and — worse — passes silently if the row is removed.
 */
function apMacIn(siteId: string): string {
  const nas = world.nas.find((n) => n.siteId === siteId && Boolean(n.mac));
  expect(nas, `no access point with a MAC in ${siteId}, so no guest can arrive there`).toBeDefined();
  return normaliseMac(nas!.mac!);
}

/**
 * A code that venue has not sold yet — resolved through the batch, which is where a
 * voucher's site actually lives.
 */
function unusedCodeIn(siteId: string): string {
  const sold = new Set(world.batches.filter((b) => b.siteId === siteId).map((b) => b.id));
  const v = world.vouchers.find((x) => x.state === "unused" && sold.has(x.batchId));
  expect(v, `${siteId} has no unused code left, so nothing here redeems anything`).toBeDefined();
  return v!.code;
}

/**
 * One guest, one venue, one submitted code — the whole flow a phone performs.
 *
 * Driven through the routes rather than against `redeemVoucher` directly, because
 * the guarantee is about what somebody within radio range is served: the NAS redirect
 * is what places them, and every outcome of this operation is a `303` whose query
 * string is the only thing the next document can read.
 */
async function redeemAt(
  siteId: string,
  code: string,
  mac: string,
): Promise<{ location: string; cookie: string }> {
  const q = new URLSearchParams({ id: mac, ap: apMacIn(siteId) });
  const jar = await send(`/portal/context?${q}`);
  expect(jar.status, `a guest could not arrive at ${siteId}`).toBe(200);
  const cookie = jar.headers.get("set-cookie")?.split(";")[0] ?? "";

  const res = await send("/portal/voucher", {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded", cookie },
    body: `lang=en&code=${encodeURIComponent(code)}`,
  });
  expect(res.status, "every outcome of this operation is a 303").toBe(303);
  return { location: res.headers.get("location") ?? "", cookie };
}

describe("a voucher is redeemable in the venue that sold it and nowhere else", () => {
  /**
   * Non-vacuity, and it is doing real work: this world was one populated site until
   * `batch-hanoi-rooms` existed, and in that world a correct implementation and a
   * leaking one emit identical bytes for every guest.
   */
  it("has both venues selling codes, and the codes are not shared", () => {
    const hotelCode = unusedCodeIn(HOTEL);
    const cafeCode = unusedCodeIn(CAFE);
    expect(
      hotelCode,
      "the two venues answer the same code, so refusing it at the café would be a " +
        "statement about the code rather than about the venue",
    ).not.toBe(cafeCode);

    const soldByTheCafe = new Set(
      world.batches.filter((b) => b.siteId === CAFE).map((b) => b.id),
    );
    expect(
      world.vouchers.some((v) => v.code === hotelCode && soldByTheCafe.has(v.batchId)),
      "the café sells a voucher carrying the hotel's code, so the refusal below " +
        "would be about a collision rather than about the site scope",
    ).toBe(false);
  });

  /**
   * **The pair, and neither half means anything alone.**
   *
   * "The hotel's code is refused at the café" passes identically against a portal
   * whose redemption is broken outright; "the hotel's code works at the hotel" passes
   * identically against one with no scope at all. One code, two venues, one drive
   * each — and the second drive also proves the first did not quietly spend it, which
   * is the worse half of this defect: a café that burns a room key leaves the guest it
   * was printed for at a code that no longer works, with nothing anywhere recording
   * why.
   */
  it("refuses another venue's code as unknown, and still redeems it at its own venue", async () => {
    const code = unusedCodeIn(HOTEL);

    const atTheCafe = await redeemAt(CAFE, code, CAFE_GUEST_MAC);
    expect(
      atTheCafe.location,
      "A café's portal redeemed a hotel's room key. `world.vouchers.find` searched " +
        "the whole deployment; a voucher's venue is its batch's, and both " +
        "`internal/api/memory.ClaimVoucher` and `store.GrantRepo.ClaimVoucher` " +
        "resolve it that way and refuse.",
    ).toContain("/voucher/?e=not-found");

    /* The reason is asserted exactly, not merely "a refusal". `revoked`, `expired`
       and `already-used` each confirm to a stranger that the code exists somewhere,
       which is the disclosure `VoucherStateFor` refuses to make. */
    for (const leak of ["e=revoked", "e=expired", "e=already-used"]) {
      expect(atTheCafe.location, `the refusal named ${leak}`).not.toContain(leak);
    }

    /* And nothing was granted. A redirect back to the form is not evidence on its
       own — the grant is what puts a device on somebody else's network. */
    const status = await send("/portal/status", { headers: { cookie: atTheCafe.cookie } });
    expect(status.status, "a refused redemption left a grant behind").toBe(404);

    const atTheHotel = await redeemAt(HOTEL, code, HOTEL_GUEST_MAC);
    expect(
      atTheHotel.location,
      "The same code cannot be redeemed at the venue that sold it either, so the " +
        "refusal above was not about the site — either the café spent the code while " +
        "refusing it, or redemption is broken outright and the case above measured " +
        "nothing.",
    ).toContain("/connected/");
  });

  /**
   * The echo survives the refusal, which is not decoration on this surface.
   *
   * The failure path is a new document with no client state to hold the field, in a
   * 900 × 550 pt mini-browser, so a guest who is told *unknown* and handed back an
   * empty box retypes twelve characters one-handed. A scope that dropped the echo
   * would fix a leak and introduce a different defect on the same screen.
   */
  it("hands the refused code back to the guest", async () => {
    const code = unusedCodeIn(HOTEL);
    const { location } = await redeemAt(CAFE, code, CAFE_GUEST_MAC);
    expect(location).toContain(`code=${code}`);
  });
});
