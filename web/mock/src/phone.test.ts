/**
 * Phone sign-in against the oracle: the same order, limits and refusals the Go
 * server has (`internal/api/phone_test.go`), so a screen driven here fails the way
 * the product fails.
 */
import { beforeEach, describe, expect, it } from "vitest";
import type { PhoneSignIn, PortalStatus } from "@foyer/contract";
import { app } from "./server.js";
import { normaliseVN, phoneOutbox, resetPhone, PHONE_LIMITS } from "./phone.js";
import { normaliseMac, world } from "./state.js";

const send = (path: string, init?: RequestInit) => app.request(`/api/v0${path}`, init);
const json = (path: string, method: string, body: unknown) =>
  send(path, { method, headers: { "content-type": "application/json" }, body: JSON.stringify(body) });
const cookieOf = (res: Response) => res.headers.get("set-cookie")?.split(";")[0] ?? "";
const form = (cookie: string, body: Record<string, string>) => ({
  method: "POST",
  headers: { "content-type": "application/x-www-form-urlencoded", cookie },
  body: new URLSearchParams(body).toString(),
});

const CAFE = "site-hcm";
let macSeq = 0x10;

function apMacIn(siteId: string): string {
  const nas = world.nas.find((n) => n.siteId === siteId && Boolean(n.mac));
  expect(nas).toBeDefined();
  return normaliseMac(nas!.mac!);
}

/** A fresh guest each time, so the per-device limit does not leak between cases. */
async function arrive(): Promise<string> {
  const mac = `02:1a:2b:3c:4d:${(macSeq++).toString(16).padStart(2, "0")}`;
  const q = new URLSearchParams({ id: mac, ap: apMacIn(CAFE) });
  const res = await send(`/portal/context?${q}`);
  expect(res.status).toBe(200);
  return cookieOf(res);
}

function offerPhone(on: boolean) {
  const s = world.portalSettings.find((p) => p.siteId === CAFE)!;
  s.methods = on ? [...new Set([...s.methods, "phone" as const])] : s.methods.filter((m) => m !== "phone");
}

async function configure(dailyLimit = 50) {
  const res = await json(`/sites/${CAFE}/portal/phone`, "PUT", {
    provider: "esms-sms",
    brandname: "FOYER",
    dailyLimit,
    sessionMinutes: 90,
    messageTemplate: "{code} la ma Wi-Fi",
    apiKey: "mock-api-key",
    secretKey: "mock-secret-key",
  });
  expect(res.status).toBe(200);
  return (await res.json()) as PhoneSignIn;
}

beforeEach(() => {
  resetPhone();
  offerPhone(false);
});

describe("phone sign-in", () => {
  it("is not offered until configured and shown", async () => {
    const cookie = await arrive();
    const res = await send("/portal/phone", form(cookie, { phone: "0912345678", lang: "en" }));
    expect(res.status).toBe(404);
    expect(((await res.json()) as { type: string }).type).toBe("/problems/method-not-offered");
    await configure();
    // Configured, button hidden: still 404.
    expect((await send("/portal/phone", form(cookie, { phone: "0912345678", lang: "en" }))).status).toBe(404);
  });

  it("sends a code, never echoes the number, and grants the venue's duration", async () => {
    await configure();
    offerPhone(true);
    const cookie = await arrive();
    const sent = await send("/portal/phone", form(cookie, { phone: "0912 345 678", lang: "vi" }));
    expect(sent.status).toBe(303);
    expect(sent.headers.get("location")).toBe("/vi/phone/?sent=1");
    const msg = phoneOutbox().at(-1)!;
    expect(msg.to).toBe("+84912345678");
    expect(msg.text).toBe(`${msg.code} la ma Wi-Fi`);

    const wrong = msg.code === "000000" ? "111111" : "000000";
    const bad = await send("/portal/phone/code", form(cookie, { code: wrong, lang: "vi" }));
    expect(bad.headers.get("location")).toBe("/vi/phone/?e=code-wrong&sent=1");

    const ok = await send("/portal/phone/code", form(cookie, { code: msg.code, lang: "vi" }));
    expect(ok.status).toBe(303);
    expect(ok.headers.get("location")).toBe("/vi/connected/");
    const status = (await (await send("/portal/status", { headers: { cookie } })).json()) as PortalStatus;
    expect(status.state).toBe("connected");
    expect(status.plan.durationMinutes).toBe(90);

    // Single use.
    const again = await send("/portal/phone/code", form(cookie, { code: msg.code, lang: "vi" }));
    expect(again.headers.get("location")).toBe("/vi/phone/?e=code-expired");
  });

  it("refuses a number codes cannot be sent to, without sending", async () => {
    await configure();
    offerPhone(true);
    const cookie = await arrive();
    for (const phone of ["+12025550123", "0241234567", "1900 1234"]) {
      const res = await send("/portal/phone", form(cookie, { phone, lang: "en" }));
      expect(res.headers.get("location")).toBe("/en/phone/?e=phone-invalid");
      expect(res.headers.get("location")).not.toContain("202555");
    }
    expect(phoneOutbox()).toHaveLength(0);
  });

  it("limits codes per number, and stops at the daily ceiling", async () => {
    await configure(4);
    offerPhone(true);
    for (let i = 0; i < PHONE_LIMITS.perNumberPerHour; i++) {
      const res = await send("/portal/phone", form(await arrive(), { phone: "0912345678", lang: "en" }));
      expect(res.headers.get("location")).toBe("/en/phone/?sent=1");
    }
    const fourth = await send("/portal/phone", form(await arrive(), { phone: "0912345678", lang: "en" }));
    expect(fourth.headers.get("location")).toBe("/en/phone/?e=code-limit");

    // One more to another number reaches the ceiling of four; the fifth is refused.
    expect((await send("/portal/phone", form(await arrive(), { phone: "0987654321", lang: "en" }))).headers.get("location"))
      .toBe("/en/phone/?sent=1");
    const past = await send("/portal/phone", form(await arrive(), { phone: "0977654321", lang: "en" }));
    expect(past.headers.get("location")).toBe("/en/phone/?e=code-unavailable");
    expect(phoneOutbox()).toHaveLength(4);
  });

  it("kills a code after the attempts run out", async () => {
    await configure();
    offerPhone(true);
    const cookie = await arrive();
    await send("/portal/phone", form(cookie, { phone: "0912345678", lang: "en" }));
    const right = phoneOutbox().at(-1)!.code;
    const wrong = right === "000000" ? "111111" : "000000";
    for (let i = 1; i < PHONE_LIMITS.maxAttempts; i++) {
      await send("/portal/phone/code", form(cookie, { code: wrong, lang: "en" }));
    }
    const last = await send("/portal/phone/code", form(cookie, { code: wrong, lang: "en" }));
    expect(last.headers.get("location")).toBe("/en/phone/?e=code-expired");
    const late = await send("/portal/phone/code", form(cookie, { code: right, lang: "en" }));
    expect(late.headers.get("location")).toBe("/en/phone/?e=code-expired");
  });

  it("never returns the keys, and refuses a template without the code", async () => {
    const saved = await configure();
    expect(JSON.stringify(saved)).not.toContain("mock-api-key");
    expect(saved.credentialsSet).toBe(true);
    const bad = await json(`/sites/${CAFE}/portal/phone`, "PUT", {
      provider: "esms-sms", brandname: "FOYER", dailyLimit: 5, sessionMinutes: 5, messageTemplate: "no code here",
    });
    expect(bad.status).toBe(422);
    expect(JSON.stringify(await bad.json())).toContain("missing-code-placeholder");
  });

  it("refuses offering phone before it is configured", async () => {
    const res = await json(`/sites/${CAFE}/portal`, "PATCH", { methods: ["voucher", "phone"] });
    expect(res.status).toBe(422);
    expect(JSON.stringify(await res.json())).toContain("needs-phone-provider");
  });

  it("normalises the spellings a guest types, and nothing else", () => {
    expect(normaliseVN("+84 (0) 912 345 678")).toBe("+84912345678");
    expect(normaliseVN("0084912345678")).toBe("+84912345678");
    expect(normaliseVN("0112345678")).toBeNull();
  });
});
