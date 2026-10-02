/**
 * The guest surface, and four refusals the oracle has to be able to say.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * `web/mock` is the reference every screen in this project is checked against: the
 * E2E suite and `tools/codegen/differ` both read it as the statement of what the
 * product does. **Where it cannot represent a refusal, nothing in this project can
 * see the refusal.** On the guest surface there are four that are easy to lose:
 *
 *  - **the NAS redirect** — if no redirect parameter were read at all, `portalClient`
 *    would mint a client from nothing, so an access point no deployment had ever
 *    registered would be answered `200` with a venue's name, its languages and its
 *    terms, while the Go server answers `403 /problems/device-not-verified`.
 *  - **the guest's password and venue** — a `loginWithAccount` that accepted any
 *    non-empty password, or searched `world.accounts` unscoped, would let a café's
 *    portal sign in a hotel's guest. The console's site scoping one surface further
 *    out, and this is the half within radio range.
 *  - **the original URL** — were `originalUrl` a fixture constant such as
 *    `http://vnexpress.net/`, the allowlist would be applied to a value that could
 *    never violate it. An oracle that answers a constant cannot disagree with an
 *    implementation that fails to validate, on this project's prime open-redirect
 *    target.
 *  - **click-through's venue** — were `/portal/agree` to read
 *    `settingsFor("site-hcm")`, a literal, whether click-through was offered and for
 *    how long would be answered from a site the guest may not be standing in.
 *
 * Each case below is written against one of those, and records what makes it fail.
 *
 * ── the shape these cases are built to avoid ─────────────────────────────────
 *
 * `docs/rules/testing.md` records **an input the harness cannot be wrong about**:
 * a fixture world, a fake driver or a seeded row that produces confident output in
 * either direction. Two defences run through this file:
 *
 *  - **Nothing here is a typed-in expectation.** Venue names, languages, durations
 *    and usernames are read out of `world`, so a fixture edit moves the expectation
 *    with the world rather than turning a true test red or a false one green.
 *  - **Every claim about a *difference* is asserted as a difference.** "The hotel's
 *    page is not the café's" is checked by comparing the two answers, so a world in
 *    which the two venues were configured identically fails here rather than
 *    silently making the scope assertions vacuous.
 */
import { describe, expect, it } from "vitest";
import { PASSWORD_MIN_LENGTH } from "@foyer/contract";
import type { PortalContext, PortalStatus } from "@foyer/contract";
import { UNREGISTERED_AP_MAC } from "@foyer/fixtures";
import { app } from "./server.js";
import { DEV_PASSWORD, normaliseMac, world } from "./state.js";

const send = (path: string, init?: RequestInit) => app.request(`/api/v0${path}`, init);
const json = (path: string, init?: RequestInit) =>
  send(path, { ...init, headers: { "content-type": "application/json", ...(init?.headers ?? {}) } });

/** A phone's address on a captive-portal SSID: locally administered, per `security.md` §6. */
const GUEST_MAC = "02:1a:2b:3c:4d:5e";
const OTHER_GUEST_MAC = "02:aa:bb:cc:dd:ee";

/**
 * The access point a guest claims to be standing at, chosen **by property**.
 *
 * Never by id: a hardcoded `nas-hcm-01` breaks the day somebody renames a fixture
 * and — worse — passes silently if the row is removed. The same rule the differential
 * harness's `harnessGuestAP` follows, and for the same reason.
 */
function apMacIn(siteId: string): string {
  const nas = world.nas.find((n) => n.siteId === siteId && Boolean(n.mac));
  expect(nas, `no access point with a MAC in ${siteId}, so no guest can arrive there`).toBeDefined();
  return normaliseMac(nas!.mac!);
}

/** The two populated venues, and the devices that place a guest in each. */
const CAFE = "site-hcm";
const HOTEL = "site-hanoi";

function settings(siteId: string) {
  const s = world.portalSettings.find((p) => p.siteId === siteId);
  expect(s, `no portal settings for ${siteId}`).toBeDefined();
  return s!;
}

/** The NAS redirect, spelled the way UniFi spells it. */
function entry(opts: { mac?: string; ap?: string; ssid?: string; url?: string }): string {
  const q = new URLSearchParams();
  if (opts.mac !== undefined) q.set("id", opts.mac);
  if (opts.ap !== undefined) q.set("ap", opts.ap);
  if (opts.ssid !== undefined) q.set("ssid", opts.ssid);
  if (opts.url !== undefined) q.set("url", opts.url);
  return `/portal/context?${q}`;
}

const cookieOf = (res: Response) => res.headers.get("set-cookie")?.split(";")[0] ?? "";

const form = (cookie: string, body: string) => ({
  method: "POST",
  headers: { "content-type": "application/x-www-form-urlencoded", cookie },
  body,
});

// ── the redirect is read, and the refusal exists ─────────────────────────────

describe("the NAS redirect is read, and an access point nobody registered is refused", () => {
  /**
   * Non-vacuity first, and it is the whole reason `UNREGISTERED_AP_MAC` is a
   * documented address rather than a number somebody picked.
   *
   * Every case below turns on that address resolving to nothing. If a fixture row
   * ever carried it, those cases would go on passing while testing a *registered*
   * access point, which is a mechanism that looks like a check and is not — an
   * input the harness cannot be wrong about.
   */
  it("uses an address the fixture world really does not carry", () => {
    const collision = world.nas.find(
      (n) => Boolean(n.mac) && normaliseMac(n.mac!) === UNREGISTERED_AP_MAC,
    );
    expect(
      collision,
      `A fixture access point now carries ${UNREGISTERED_AP_MAC}, the address every ` +
        "refusal case in this file uses as *the AP nothing registered*. Those cases " +
        "would keep passing against a device that resolves. Move the row off the " +
        "IANA documentation range (RFC 7042 §2.1.2) rather than changing the constant.",
    ).toBeUndefined();
  });

  it("refuses a redirect naming an access point this deployment has never registered", async () => {
    const res = await send(entry({ mac: GUEST_MAC, ap: UNREGISTERED_AP_MAC }));

    expect(
      res.status,
      "An access point no deployment has ever registered was answered with a venue's " +
        "page. `security.md` §4: the NAS redirect is a claim, and a portal that fell " +
        "back to some site for an AP it has never heard of is a portal authorizing " +
        "devices on a network it does not manage.",
    ).toBe(403);
    const body = (await res.json()) as { type: string };
    expect(body.type).toBe("/problems/device-not-verified");
  });

  /**
   * And it leaves nothing behind, which is the half that is easy to get wrong.
   *
   * The product mints no session row for an access point it cannot place, so there
   * is no cookie either. A refusal that wrote one would bind this browser to it, and
   * the guest's next redirect — the well-formed one, from the AP they are actually
   * standing under — would be answered out of the poisoned binding.
   */
  it("mints no session for a redirect it refused, so a good one still works", async () => {
    const refused = await send(entry({ mac: GUEST_MAC, ap: UNREGISTERED_AP_MAC }));
    expect(
      refused.headers.get("set-cookie"),
      "A refused entry set a portal session cookie. The next well-formed redirect " +
        "would then be answered out of a binding that failed its presence check.",
    ).toBeNull();

    const ok = await send(entry({ mac: GUEST_MAC, ap: apMacIn(CAFE) }));
    expect(ok.status).toBe(200);
  });

  /**
   * Both halves of the claim are required, and each refuses on its own — the same
   * two gates `internal/portal` applies. A redirect naming a client and no access
   * point cannot be placed; one naming an access point and no client has nothing to
   * verify.
   */
  it("refuses a half-formed claim in either direction", async () => {
    expect((await send(entry({ mac: GUEST_MAC }))).status).toBe(403);
    expect((await send(entry({ ap: apMacIn(CAFE) }))).status).toBe(403);
  });

  /** Vendors spell an address three ways in one product. All three are one AP. */
  it("resolves an access point however the redirect spells its address", async () => {
    const mac = apMacIn(CAFE);
    for (const spelling of [
      mac.toUpperCase(),
      mac.replace(/:/g, "-"),
      mac.replace(/:/g, ""),
      `${mac.replace(/:/g, "").slice(0, 4)}.${mac.replace(/:/g, "").slice(4, 8)}.${mac.replace(/:/g, "").slice(8)}`,
    ]) {
      const res = await send(entry({ mac: GUEST_MAC, ap: spelling }));
      expect(res.status, `the redirect spelled the AP as ${spelling} and was refused`).toBe(200);
    }
  });

  /**
   * **The case that says a correct implementation and a leaking one differ.**
   *
   * The refusal above can be satisfied by a lookup that resolves the AP and then
   * throws the answer away: a `portalContextFor` that read `settingsFor("site-hcm")`
   * whatever the guest's placement was would emit, for a portal that resolved the
   * venue correctly and one that ignored the access point entirely, **identical
   * bytes** for every guest in a single-venue world.
   *
   * The two venues are compared against each other rather than against typed-in
   * copy, so a world in which the hotel were configured like the café fails here
   * instead of making this vacuous.
   */
  it("answers each venue's own page, and the two venues differ", async () => {
    const cafe = (await (await send(entry({ mac: GUEST_MAC, ap: apMacIn(CAFE) }))).json()) as PortalContext;
    const hotel = (await (
      await send(entry({ mac: OTHER_GUEST_MAC, ap: apMacIn(HOTEL) }))
    ).json()) as PortalContext;

    expect(
      hotel.venueName,
      "The two populated venues answer the same name, so this file cannot tell a " +
        "portal that reads the guest's placement from one that hardcodes a site. " +
        "Give the second site its own venue name in `web/fixtures`.",
    ).not.toBe(cafe.venueName);

    expect(cafe.venueName).toBe(settings(CAFE).venueName);
    expect(hotel.venueName).toBe(settings(HOTEL).venueName);
    expect(hotel.languages).toEqual(settings(HOTEL).languages);
  });
});

// ── the URL the NAS sent, allowlisted ────────────────────────────────────────

describe("the return link is the URL the NAS sent, allowlisted", () => {
  async function statusAfterAgreeing(query: string): Promise<PortalStatus> {
    const jar = await send(query);
    expect(jar.status).toBe(200);
    const cookie = cookieOf(jar);
    const agreed = await send("/portal/agree", form(cookie, "lang=en"));
    expect(agreed.status, "the guest did not get on, so there is no status to read").toBe(303);
    const res = await send("/portal/status", { headers: { cookie } });
    expect(res.status).toBe(200);
    return (await res.json()) as PortalStatus;
  }

  it("carries the guest's own destination through to the connected page", async () => {
    const url = "http://tin-tuc.example/bai-viet?id=7";
    const status = await statusAfterAgreeing(entry({ mac: GUEST_MAC, ap: apMacIn(CAFE), url }));
    expect(
      status.originalUrl,
      "The connected page offers a link the NAS never sent. It is the one value on " +
        "that screen that comes from the guest's own browsing, and a portal that " +
        "renders a constant is rendering somebody else's destination.",
    ).toBe(url);
  });

  /**
   * The allowlist, put to values it can actually refuse.
   *
   * Applied to a constant and to nothing else, it could not fail — the check would
   * be real and the input could not be wrong. `security.md` §4 is
   * explicit about why this one matters more than it reads: the victim is on an
   * untrusted network, *expects* to be redirected, and has just typed a credential.
   */
  it.each([
    ["javascript:alert(1)", "a scheme that is not a destination"],
    ["data:text/html,<script>alert(1)</script>", "the other one"],
    ["//evil.example/", "protocol-relative — no scheme at all, so it inherits the portal's"],
    ["http://portal.example@evil.example/", "userinfo: the half a guest reads is not the host"],
    ["http://", "a scheme with no authority"],
    [`https://${"a".repeat(2100)}.example/`, "longer than the contract's 2048"],
  ])("renders no link at all for %s (%s)", async (hostile) => {
    const status = await statusAfterAgreeing(
      entry({ mac: GUEST_MAC, ap: apMacIn(CAFE), url: hostile }),
    );
    expect(
      status.originalUrl,
      `\`${hostile}\` reached the connected page as a link. Silence is the right ` +
        "failure here: a guest who does not get their original page back is mildly " +
        "inconvenienced, and one who gets somebody else's has been phished on a " +
        "network the venue vouched for.",
    ).toBeNull();
  });
});

// ── the guest's password, and the guest's venue ──────────────────────────────

describe("the portal verifies the password and scopes it to the venue", () => {
  const enabledIn = (siteId: string) => {
    const a = world.accounts.find((x) => x.siteId === siteId && x.enabled);
    expect(a, `no enabled account in ${siteId}`).toBeDefined();
    return a!;
  };

  async function signIn(
    ap: string,
    username: string,
    password: string,
    mac = GUEST_MAC,
  ): Promise<{ location: string; cookie: string }> {
    const jar = await send(entry({ mac, ap }));
    expect(jar.status).toBe(200);
    const cookie = cookieOf(jar);
    const res = await send(
      "/portal/account",
      form(
        cookie,
        `lang=en&username=${encodeURIComponent(username)}&password=${encodeURIComponent(password)}`,
      ),
    );
    expect(res.status, "every outcome of this operation is a 303").toBe(303);
    return { location: res.headers.get("location") ?? "", cookie };
  }

  it("refuses a wrong password, and mints no grant while doing it", async () => {
    const account = enabledIn(CAFE);
    const { location, cookie } = await signIn(apMacIn(CAFE), account.username, "not-the-password");

    expect(
      location,
      "Any non-empty password signed this guest in. The console verifies its own " +
        "operators' passwords; this is the half reachable by anyone within " +
        "radio range.",
    ).toContain("/account/?e=bad-credentials");

    /* And nothing was granted — a redirect to the form is not evidence on its own. */
    const status = await send("/portal/status", { headers: { cookie } });
    expect(status.status, "a refused sign-in left a grant behind").toBe(404);
  });

  it("lets the right password through", async () => {
    const account = enabledIn(CAFE);
    const { location } = await signIn(apMacIn(CAFE), account.username, DEV_PASSWORD);
    expect(location).toContain("/connected/");
  });

  /**
   * **The site scope, and it is asserted as a pair.**
   *
   * One half alone proves nothing: "the hotel's username is refused at the café"
   * passes just as well against a portal where account login is simply broken. The
   * second half — the same username and the same password, at the hotel's own access
   * point — is what says the refusal was about the venue.
   *
   * The username is chosen for a reason `web/fixtures` records: *Nguyễn* is the
   * surname of roughly two Vietnamese people in five, so this is not a synthetic
   * collision. A café's portal signing in a hotel's guest is a real support call.
   */
  it("refuses another venue's guest, and admits them at their own venue", async () => {
    const hotelGuest = enabledIn(HOTEL);
    expect(
      world.accounts.some((a) => a.siteId === CAFE && a.username === hotelGuest.username),
      "The two venues share this username, so refusing it at the café would be a " +
        "statement about the account rather than about the site.",
    ).toBe(false);

    const atTheCafe = await signIn(apMacIn(CAFE), hotelGuest.username, DEV_PASSWORD);
    expect(
      atTheCafe.location,
      "A café's portal signed in a hotel's guest. `world.accounts.find` searched the " +
        "whole deployment; `accounts` is UNIQUE (tenant_id, site_id, username) and " +
        "`internal/api/memory.AuthenticateGuest` scopes on exactly that pair.",
    ).toContain("/account/?e=bad-credentials");

    const atTheHotel = await signIn(
      apMacIn(HOTEL),
      hotelGuest.username,
      DEV_PASSWORD,
      OTHER_GUEST_MAC,
    );
    expect(
      atTheHotel.location,
      "The same guest cannot get on at their own venue either, so the case above was " +
        "not measuring the site scope.",
    ).toContain("/connected/");
  });

  /**
   * A disabled account with the **right** password is `disabled`; with a wrong one it
   * is `bad-credentials`, exactly like an unknown username.
   *
   * That order is `AuthenticateGuest`'s and it is not decoration: answering
   * `disabled` to a wrong password would make this endpoint a place to ask whether an
   * account exists, on a surface anybody within radio range can reach.
   */
  it("distinguishes disabled from wrong only for somebody who has the password", async () => {
    const off = world.accounts.find((a) => a.siteId === CAFE && !a.enabled);
    expect(off, "no disabled account in the café, so this case cannot be driven").toBeDefined();

    const right = await signIn(apMacIn(CAFE), off!.username, DEV_PASSWORD);
    expect(right.location).toContain("/account/?e=disabled");

    const wrong = await signIn(apMacIn(CAFE), off!.username, "not-the-password");
    expect(
      wrong.location,
      "A wrong password on a disabled account was answered `disabled`, which publishes " +
        "that the account exists.",
    ).toContain("/account/?e=bad-credentials");
  });

  /**
   * The password an operator types when creating an account has to go somewhere.
   *
   * If it went nowhere, a created account could not sign in on the portal — and that
   * would be invisible for as long as *any* password worked. `createOperator` has the
   * same obligation, on the other population.
   */
  it("lets an account created through the console sign in on the portal", async () => {
    const password = "a-created-account-password";
    const created = await json("/accounts", {
      method: "POST",
      body: JSON.stringify({ siteId: CAFE, username: "moi.tao.tai.khoan", password }),
    });
    expect(created.status).toBe(201);

    const { location } = await signIn(apMacIn(CAFE), "moi.tao.tai.khoan", password);
    expect(
      location,
      "The password `createAccount` was given reached nothing, so the account it " +
        "created cannot be used.",
    ).toContain("/connected/");
  });

  /** And the duplicate check is the database's rule: a username belongs to a venue. */
  it("lets two venues have a guest of the same name", async () => {
    const taken = enabledIn(CAFE).username;
    const res = await json("/accounts", {
      method: "POST",
      body: JSON.stringify({ siteId: HOTEL, username: taken, password: "another-venue-password" }),
    });
    expect(
      res.status,
      "The hotel was refused a username because the café already had one. " +
        "`accounts` is UNIQUE (tenant_id, site_id, username) — a username is a " +
        "venue's, not a deployment's.",
    ).toBe(201);

    /* Still refused inside one venue, or the check has simply been removed. */
    const again = await json("/accounts", {
      method: "POST",
      body: JSON.stringify({ siteId: CAFE, username: taken, password: "a-third-password" }),
    });
    expect(again.status).toBe(422);
  });

  /**
   * The password floor, which Go applies and the oracle has to apply too.
   *
   * `internal/api`'s `createAccount` refuses below `passwordMinimum` — twelve, shared
   * with `createOperator` and `changeOwnPassword`. A mock that checked only that a
   * password was **present** would accept an eleven-character one here while Go
   * refused it there, and nothing would go red: `make diff-api` posts a 35-character
   * constant, so the harness cannot be wrong about the input and both servers answer
   * 201. `docs/rules/testing.md`'s *input the harness cannot be wrong about*, in the
   * direction that stays quiet.
   *
   * **Both branches, and neither length is typed in.** `PASSWORD_MIN_LENGTH` is the
   * authority the console already renders its own rule from, so a floor that moves
   * moves this case with it; and locking only the refusal would be satisfied by a
   * mock that refuses every password.
   */
  it("refuses a password below the floor Go applies, and accepts one at it", async () => {
    const shortOne = "a".repeat(PASSWORD_MIN_LENGTH - 1);
    const before = world.accounts.length;
    const refused = await json("/accounts", {
      method: "POST",
      body: JSON.stringify({ siteId: CAFE, username: "qua.ngan.mat.khau", password: shortOne }),
    });
    expect(
      refused.status,
      `A ${shortOne.length}-character password was accepted. \`internal/api\` refuses ` +
        `it at ${PASSWORD_MIN_LENGTH}, so a console built against this oracle offers ` +
        "an operator a credential the server will not take.",
    ).toBe(422);
    const problem = (await refused.json()) as {
      params?: { fields?: Record<string, string>; fieldParams?: Record<string, { minimumCount?: number }> };
    };
    expect(problem.params?.fields?.password, "the refusal must name why, not only that").toBe(
      "below-minimum",
    );
    expect(
      problem.params?.fieldParams?.password?.minimumCount,
      "`below-minimum` without its figure leaves the console inventing the number",
    ).toBe(PASSWORD_MIN_LENGTH);
    expect(world.accounts.length, "a refused creation still created the account").toBe(before);

    /* One character longer, and the same request is accepted — so this cannot be
       satisfied by refusing everything. */
    const accepted = await json("/accounts", {
      method: "POST",
      body: JSON.stringify({
        siteId: CAFE,
        username: "vua.du.mat.khau",
        password: "a".repeat(PASSWORD_MIN_LENGTH),
      }),
    });
    expect(
      accepted.status,
      `A password of exactly ${PASSWORD_MIN_LENGTH} characters was refused: the floor ` +
        "is off by one, or it is not a floor.",
    ).toBe(201);
  });
});

// ── click-through reads the guest's venue ────────────────────────────────────

describe("click-through is the guest's venue's, not a literal site's", () => {
  /**
   * An `/portal/agree` that read `settingsFor("site-hcm")` would grant a guest
   * standing in the hotel the café's duration — and, at a venue that had not enabled
   * the mode at all, would let them on by the café's setting.
   *
   * The two durations are compared against each other first, for the same reason as
   * the venue names above: if both venues offered the same number of minutes, this
   * case could not tell a portal that reads the placement from one that does not.
   */
  it("grants each venue its own duration", async () => {
    const cafeMinutes = settings(CAFE).clickThroughMinutes;
    const hotelMinutes = settings(HOTEL).clickThroughMinutes;
    expect(cafeMinutes, "the café does not offer click-through").toBeTruthy();
    expect(hotelMinutes, "the hotel does not offer click-through").toBeTruthy();
    expect(
      hotelMinutes,
      "Both venues grant the same number of minutes, so this case cannot tell a " +
        "portal that reads the guest's venue from one that reads a literal.",
    ).not.toBe(cafeMinutes);

    const granted = async (ap: string, mac: string): Promise<number> => {
      const jar = await send(entry({ mac, ap }));
      expect(jar.status).toBe(200);
      const cookie = cookieOf(jar);
      expect((await send("/portal/agree", form(cookie, "lang=en"))).status).toBe(303);
      const res = await send("/portal/status", { headers: { cookie } });
      expect(res.status).toBe(200);
      const s = (await res.json()) as PortalStatus;
      expect(s.expiresAt, "an unmetered click-through grant is not a thing a venue chose").toBeTruthy();
      return Math.round(
        (Date.parse(s.expiresAt!) - Date.parse(s.grantedAt!)) / 60_000,
      );
    };

    expect(await granted(apMacIn(CAFE), GUEST_MAC)).toBe(cafeMinutes);
    expect(
      await granted(apMacIn(HOTEL), OTHER_GUEST_MAC),
      "A guest standing in the hotel was granted the café's duration, which is what " +
        "a hardcoded `settingsFor(\"site-hcm\")` does.",
    ).toBe(hotelMinutes);
  });
});
