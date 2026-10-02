/**
 * **The shape of a portal `303`, driven on every outcome that produces one.**
 *
 * Were `PortalRedirect.Location` typed a bare `type: string`, an absolute URL at
 * `PORTAL_ORIGIN` and `internal/portal`'s root-relative path would *both conform*
 * and the document could not say which was right. It matters more than a
 * type: a captive portal is reached over plain HTTP, on an SSID the guest has no
 * route off yet, so an absolute `Location` does not carry a scheme and a host — it
 * **picks** them, on the one screen where the guest has just typed a credential.
 *
 * The rule is a `pattern` in the contract, and **this file reads it out of the
 * document rather than restating it**. A second copy of a constraint the contract
 * owns is `docs/rules/database.md`'s restated-constraint defect in another
 * language: it drifts silently, still passing, still printing a reason, and by then
 * describing a rule nothing enforces.
 *
 * Two halves, because a pattern that matches everything would satisfy the first one
 * alone:
 *
 *   1. every `Location` this server writes matches it, on all seven outcomes;
 *   2. the pattern **refuses** the shapes this file is about — an absolute URL at
 *      this server's own origin, protocol-relative `//host`, the `/\host`
 *      spelling several browsers treat as protocol-relative, and a value carrying a
 *      line terminator.
 */
import { describe, expect, it } from "vitest";
import { app } from "./server.js";
import { world } from "./state.js";
import { contractDocument } from "./validate.js";

/** The normative statement, read from the document that makes it. */
function locationPattern(): string {
  const responses = (
    contractDocument().components as unknown as {
      responses: Record<
        string,
        { headers?: Record<string, { schema?: { pattern?: string } }> }
      >;
    }
  ).responses;
  const pattern = responses?.PortalRedirect?.headers?.Location?.schema?.pattern;
  expect(
    pattern,
    "`PortalRedirect.headers.Location` declares no `pattern`, so this whole file " +
      "compares every value against nothing",
  ).toBeTruthy();
  return pattern!;
}

const form = (cookie: string, body: string, path: string) =>
  app.request(`/api/v0${path}`, {
    method: "POST",
    headers: { "content-type": "application/x-www-form-urlencoded", cookie },
    body,
  });

/**
 * A browser, from the entry the NAS redirects to.
 *
 * The status is deliberately not asserted: `?present=0` is the forged-redirect case
 * and answers `403`, and it still mints the session — `portalClient` creates and
 * sets the cookie before the verification check, which is what makes the refusing
 * branch of `acceptTerms` drivable at all. The cookie is what this needs, so the
 * cookie is what it asserts.
 */
const newClient = async (query = "") => {
  const jar = await app.request(`/api/v0/portal/context${query}`);
  const cookie = jar.headers.get("set-cookie")?.split(";")[0] ?? "";
  expect(cookie, "no portal session was minted, so there is no browser to redirect").toBeTruthy();
  return cookie;
};

const patchPortal = (body: unknown) =>
  app.request("/api/v0/sites/site-hcm/portal", {
    method: "PATCH",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
  });

describe("every portal 303", () => {
  /**
   * Six of the seven are three operations with a success and a failure each. The
   * seventh is the one no operation of the three can reach: **a browser with no
   * placeable access point**, which on `POST /portal/agree` takes a branch of its
   * own, where a hand-built absolute `Location` would go unmeasured by the other six.
   * `?present=0` cannot stand in for it — that query mints the session and then
   * refuses the *grant*, which is the present case one route later.
   *
   * So it is driven with **no cookie at all**: `portalClient` cannot place an access
   * point out of nothing, mints no session row and writes no cookie
   * (`server.ts`, the `!created` branch), which is the product's own reason the
   * branch exists. A query parameter that manufactures the state would prove only
   * the parameter.
   */
  it("writes a Location the contract's own pattern accepts", async () => {
    const re = new RegExp(locationPattern());
    await patchPortal({ methods: ["voucher", "account", "click-through"], clickThroughMinutes: 30 });

    const account = world.accounts.find((a) => a.enabled)!;
    const voucher = world.vouchers.find((v) => v.state === "unused")!;

    const drives: { what: string; res: Response }[] = [
      {
        what: "redeemVoucher, refused",
        res: await form(await newClient(), "lang=en&code=NOSUCHCODE99", "/portal/voucher"),
      },
      {
        what: "redeemVoucher, granted",
        res: await form(await newClient(), `lang=en&code=${voucher.code}`, "/portal/voucher"),
      },
      {
        what: "acceptTerms, granted",
        res: await form(await newClient(), "lang=vi", "/portal/agree"),
      },
      {
        what: "loginWithAccount, refused",
        res: await form(
          await newClient(),
          "lang=en&username=nobody.at.all&password=x",
          "/portal/account",
        ),
      },
      {
        what: "loginWithAccount, granted",
        res: await form(
          await newClient(),
          `lang=vi&username=${encodeURIComponent(account.username)}&password=anything`,
          "/portal/account",
        ),
      },
      {
        /*
         * `acceptTerms`, refused — and the refusal has to be one that still
         * redirects. A venue that does not offer click-through answers `404`, which
         * is a different response entirely; the redirecting failure is a browser the
         * driver cannot confirm is present, which is `security.md` §4's own case.
         */
        what: "acceptTerms, refused",
        res: await form(await newClient("?present=0"), "lang=en", "/portal/agree"),
      },
    ];

    /**
     * The seventh outcome, and the one no knob reaches.
     *
     * A world with **no placeable access point at all**: `world.nas` is emptied for
     * the duration — the fresh-deployment world `first-run.test.ts` already drives —
     * and the form is posted with no cookie, so `newPortalClient` finds no
     * placement, mints no session row and writes no cookie. `portalClient` answers
     * `null`, and the route takes the branch that could otherwise write an absolute
     * `Location` from `PORTAL_ORIGIN`.
     *
     * A query parameter cannot stand in for it. `?present=0` mints a client and
     * refuses the *grant* — the present case, one route down — and manufacturing the
     * state under test would prove only the manufacturing.
     */
    const devices = world.nas.splice(0, world.nas.length);
    try {
      expectRedirect(
        "agree, no placeable access point",
        await form("", "lang=en", "/portal/agree"),
        re,
      );
    } finally {
      world.nas.push(...devices);
    }

    for (const { what, res } of drives) {
      await expectRedirect(what, res, re);
    }
  });

  /** Every property of a portal `303`, read off one response. */
  async function expectRedirect(what: string, res: Response, re: RegExp) {
    expect(res.status, `${what}: not a 303, so the CNA never re-checks captivity`).toBe(303);
    const location = res.headers.get("location") ?? "";
    expect(location, `${what}: no Location at all`).toBeTruthy();
    expect(
      re.test(location),
      `${what}: Location ${JSON.stringify(location)} does not match the contract's ` +
        `pattern ${locationPattern()} — an absolute or protocol-relative value picks ` +
        `a scheme and a host for a guest who has no route off this SSID`,
    ).toBe(true);
    expect(res.headers.get("cache-control"), `${what}: cacheable`).toBe("no-store");
    /* No body. A `303` a Captive Network Assistant might render is a `303` that
       has stopped being a navigation, and the contract declares no `content`. */
    expect(await res.text(), `${what}: the redirect carries a body`).toBe("");
  }

  /**
   * **The pattern can refuse**, which is the half that makes the half above mean
   * something.
   *
   * `^.*$` would accept every value driven up there. Each rejected string below is a
   * real shape rather than a decoration: the first is an absolute URL at this
   * server's own origin, the next two pick a host without naming a scheme, and the last
   * is a header with a second header in it.
   */
  it("refuses the shapes that pick a scheme and a host", () => {
    const re = new RegExp(locationPattern());
    for (const value of [
      "http://localhost:5171/en/connected/",
      "https://evil.example/en/connected/",
      "//evil.example/en/connected/",
      "/\\evil.example/en/connected/",
      "/en/connected/\r\nSet-Cookie: a=b",
      "en/connected/",
      "",
    ]) {
      expect(re.test(value), `the pattern accepts ${JSON.stringify(value)}`).toBe(false);
    }
    /* And it accepts the shape the product writes, including the root itself and a
       failure path carrying a query — so it is a rule rather than a refusal of
       everything. */
    for (const value of ["/", "/en/connected/", "/vi/voucher/?code=ABC&e=already-used"]) {
      expect(re.test(value), `the pattern refuses ${JSON.stringify(value)}`).toBe(true);
    }
  });
});
