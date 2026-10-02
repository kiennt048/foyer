/**
 * The portal's open authorizations, and ending one on its controller.
 *
 * On a UniFi SSID with no RADIUS accounting profile a guest has no `radacct` row, so
 * there is no session for the console to disconnect. The grant is listed and ended
 * instead — UniFi's Hotspot Manager and Omada's portal list work the same way. Driven
 * through the real routes: a guest arrives at a UniFi access point and redeems a code,
 * and the console lists and disconnects them.
 */
import { describe, expect, it } from "vitest";
import type { PortalAuthorization, SessionDisconnectResult } from "@foyer/contract";
import { app } from "./server.js";
import { DEV_PASSWORD, normaliseMac, world } from "./state.js";

const send = (path: string, init?: RequestInit) => app.request(`/api/v0${path}`, init);

async function operatorCookie(): Promise<string> {
  const res = await send("/auth/session", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ username: "dang.quoc.viet", password: DEV_PASSWORD }),
  });
  expect(res.status).toBe(200);
  return res.headers.get("set-cookie")?.split(";")[0] ?? "";
}

describe("a portal guest with no session", () => {
  it("is listed, and disconnected on the controller that authorized them", async () => {
    const ap = world.nas.find((n) => n.driver === "unifi" && n.capabilities.canDeauthGuest && n.reachability === "ok" && n.mac);
    expect(ap, "the world has a reachable UniFi access point a guest can arrive at").toBeDefined();
    const sold = new Set(world.batches.filter((b) => b.siteId === ap!.siteId).map((b) => b.id));
    const voucher = world.vouchers.find((v) => v.state === "unused" && sold.has(v.batchId));
    expect(voucher, "that venue has an unused code").toBeDefined();

    const mac = "02:7e:57:00:00:01";
    const arrive = await send(`/portal/context?${new URLSearchParams({ id: mac, ap: normaliseMac(ap!.mac!) })}`);
    expect(arrive.status).toBe(200);
    const guest = arrive.headers.get("set-cookie")?.split(";")[0] ?? "";
    const redeemed = await send("/portal/voucher", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded", cookie: guest },
      body: `lang=en&code=${encodeURIComponent(voucher!.code)}`,
    });
    expect(redeemed.status).toBe(303);

    const cookie = await operatorCookie();
    const list = async () =>
      ((await (await send(`/portal-grants?siteId=${ap!.siteId}`, { headers: { cookie } })).json()) as {
        items: PortalAuthorization[];
      }).items;
    const mine = (await list()).find((g) => normaliseMac(g.clientMac) === normaliseMac(mac));
    expect(mine, "the redeemed guest is listed as an open authorization").toBeDefined();
    expect(mine!.canDisconnect).toBe(true);
    expect(mine!.method).toBe("voucher");

    const res = await send(`/portal-grants/${mine!.id}/disconnect`, {
      method: "POST",
      headers: { "content-type": "application/json", cookie },
      body: JSON.stringify({ revokeVoucher: true }),
    });
    expect(res.status).toBe(200);
    const result = (await res.json()) as SessionDisconnectResult;
    expect(result).toMatchObject({ disconnected: true, grantsRevoked: 1, voucherRevoked: true });
    expect((await list()).some((g) => g.id === mine!.id), "a disconnected guest is no longer open").toBe(false);

    const again = await send(`/portal-grants/${mine!.id}/disconnect`, {
      method: "POST",
      headers: { "content-type": "application/json", cookie },
      body: "{}",
    });
    expect(again.status).toBe(409);
    expect(((await again.json()) as { type: string }).type).toBe("/problems/session-not-live");
  });

  it("answers 404 for a grant that does not exist", async () => {
    const res = await send("/portal-grants/grant-does-not-exist/disconnect", {
      method: "POST",
      headers: { "content-type": "application/json", cookie: await operatorCookie() },
      body: "{}",
    });
    expect(res.status).toBe(404);
  });
});
