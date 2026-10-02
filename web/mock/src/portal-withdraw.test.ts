/**
 * The guest's own consent withdrawal, against the oracle — the twin of
 * `TestAStrangerCannotWithdrawAnOnlineGuestsConsent` in `internal/api`: only the
 * portal session that holds a
 * grant withdraws anything, never one that merely claims an online guest's MAC.
 */
import { describe, expect, it } from "vitest";
import { app } from "./server.js";
import { consentVersion, keepGuestInput, marketingConsents } from "./guestdata.js";
import { normaliseMac, world } from "./state.js";

const send = (path: string, init?: RequestInit) => app.request(`/api/v0${path}`, init);
const cookieOf = (res: Response) => res.headers.get("set-cookie")?.split(";")[0] ?? "";
const SITE = "site-hcm";

describe("withdrawOwnMarketingConsent", () => {
  it("is refused to a session that claims an online guest's MAC but holds no grant", async () => {
    const mac = "02:6b:2b:3c:4d:77";
    const nas = world.nas.find((n) => n.siteId === SITE && Boolean(n.mac))!;
    const text = `Offers ${Date.now()}`;
    expect(
      (await send(`/sites/${SITE}/guest-data`, {
        method: "PUT",
        headers: { "content-type": "application/json" },
        body: JSON.stringify({ fields: [{ field: "email", required: false }], marketingConsent: { enabled: true, text: { en: text } } }),
      })).status,
    ).toBe(200);
    keepGuestInput(SITE, normaliseMac(mac), null, {
      details: { email: "guest@example.test" },
      consentVersion: consentVersion(text),
    });
    const mine = () => marketingConsents(SITE).filter((c) => c.clientMac === normaliseMac(mac));
    expect(mine()).toHaveLength(1);

    // A stranger's fresh session, claiming the guest's MAC — which is present.
    const arrived = await send(`/portal/context?${new URLSearchParams({ id: mac, ap: normaliseMac(nas.mac!) })}`);
    expect(arrived.status).toBe(200);
    const res = await send("/portal/marketing-consent/withdraw", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded", cookie: cookieOf(arrived) },
      body: "lang=en",
    });
    expect(res.status).toBe(303);
    expect(res.headers.get("location")).toBe("/en/connected/?n=consent-withdrawn");
    expect(mine()[0]!.state).toBe("active");
  });
});
