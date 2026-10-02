/**
 * The admin-configurability rule's messaging settings against the oracle — the same
 * writes `internal/api/messaging_settings_test.go` drives, and the same effect on
 * the next guest request: tuned limits and ranges, the deployment-wide cap, the
 * test message, the site's calendar, and the venue's guest-data setting applied to
 * phone sign-in.
 */
import { beforeEach, describe, expect, it } from "vitest";
import type { MarketingConsent, PhoneAbuseControls, PhoneTestMessageResult, SiteTimeZone } from "@foyer/contract";
import { app } from "./server.js";
import { PHONE_LIMIT_RANGES, SPEND_CAP_RANGE, phoneOutbox, resetPhone } from "./phone.js";
import { contractDocument } from "./validate.js";
import { resetSiteTimeZones, siteDay } from "./sitetime.js";
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
let macSeq = 0x60;

async function arrive(): Promise<string> {
  const mac = `02:6a:2b:3c:4d:${(macSeq++).toString(16).padStart(2, "0")}`;
  const nas = world.nas.find((n) => n.siteId === CAFE && Boolean(n.mac))!;
  const res = await send(`/portal/context?${new URLSearchParams({ id: mac, ap: normaliseMac(nas.mac!) })}`);
  expect(res.status).toBe(200);
  world.presentMacs.add(normaliseMac(mac));
  return cookieOf(res);
}

async function configure(dailyLimit = 50) {
  const s = world.portalSettings.find((p) => p.siteId === CAFE)!;
  s.methods = [...new Set([...s.methods, "phone" as const])];
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
}

const limits = (over: Record<string, number> = {}) => ({
  perNumberPerHour: 3,
  perNumberPerDay: 5,
  perDevicePerHour: 5,
  perAddressPerHour: 20,
  codeMinutes: 5,
  maxAttempts: 5,
  ...over,
});

const requestCode = async (cookie: string, phone: string) =>
  (await send("/portal/phone", form(cookie, { phone, lang: "en" }))).headers.get("location");

beforeEach(async () => {
  resetPhone();
  resetSiteTimeZones();
  await json("/phone-sign-in/spend-cap", "PUT", { dailyLimit: null });
});

describe("messaging settings", () => {
  it("holds the next code to the venue's tuned limits and ranges, and refuses what is out of bounds", async () => {
    await configure();
    const path = `/sites/${CAFE}/portal/phone/abuse-controls`;
    const before = (await (await send(path)).json()) as PhoneAbuseControls;
    expect(before.customised).toBe(false);
    expect(before.mobilePrefixes).toEqual(["03", "05", "07", "08", "09"]);

    const low = await json(path, "PUT", { limits: limits({ perNumberPerDay: 2 }), mobilePrefixes: ["09"] });
    expect(low.status).toBe(422);
    expect(JSON.stringify(await low.json())).toContain('"limits.perNumberPerDay":"below-minimum"');
    expect((await json(path, "PUT", { limits: limits({ codeMinutes: 11 }), mobilePrefixes: ["09"] })).status).toBe(422);

    const saved = await json(path, "PUT", { limits: limits({ perDevicePerHour: 1 }), mobilePrefixes: ["09"] });
    expect(saved.status).toBe(200);
    const phone = (await (await send(`/sites/${CAFE}/portal/phone`)).json()) as { limits: { perDevicePerHour: number } };
    expect(phone.limits.perDevicePerHour).toBe(1);

    const cookie = await arrive();
    expect(await requestCode(cookie, "0332345678")).toContain("e=phone-invalid");
    expect(await requestCode(cookie, "0912345678")).toBe("/en/phone/?sent=1");
    expect(await requestCode(cookie, "0912345678")).toContain("e=code-limit");
  });

  it("refuses past the deployment-wide cap, and logs it as the deployment's", async () => {
    await configure();
    expect((await json("/phone-sign-in/spend-cap", "PUT", { dailyLimit: 0 })).status).toBe(422);
    expect((await json("/phone-sign-in/spend-cap", "PUT", { dailyLimit: 1 })).status).toBe(200);
    expect(await requestCode(await arrive(), "0912345678")).toBe("/en/phone/?sent=1");
    expect(await requestCode(await arrive(), "0987654321")).toContain("e=code-unavailable");
    await json("/phone-sign-in/spend-cap", "PUT", { dailyLimit: null });
    expect(await requestCode(await arrive(), "0987654321")).toBe("/en/phone/?sent=1");
  });

  it("sends a counted test message and names the outcome, never the number", async () => {
    const path = `/sites/${CAFE}/portal/phone/test-message`;
    const before = (await (await json(path, "POST", { phone: "0912345678" })).json()) as PhoneTestMessageResult;
    expect(before.outcome).toBe("not-configured");
    await configure();
    const res = (await (await json(path, "POST", { phone: "0912 345 678" })).json()) as PhoneTestMessageResult;
    expect(res).toMatchObject({ outcome: "sent", sentToday: 1 });
    expect(phoneOutbox().at(-1)?.to).toBe("+84912345678");
    const bad = (await (await json(path, "POST", { phone: "0241234567" })).json()) as PhoneTestMessageResult;
    expect(bad.outcome).toBe("phone-invalid");
  });

  it("moves the site's calendar, and refuses a zone nobody has heard of", async () => {
    const path = `/sites/${CAFE}/time-zone`;
    expect(((await (await send(path)).json()) as SiteTimeZone).timeZone).toBe("Asia/Ho_Chi_Minh");
    // `updatedAt` is when the zone changed: nobody has, and storing the default is no change.
    expect(((await (await send(path)).json()) as SiteTimeZone).updatedAt).toBeNull();
    const same = (await (await json(path, "PUT", { timeZone: "Asia/Ho_Chi_Minh" })).json()) as SiteTimeZone;
    expect(same.updatedAt).toBeNull();
    const bad = await json(path, "PUT", { timeZone: "Local" });
    expect(bad.status).toBe(422);
    expect(JSON.stringify(await bad.json())).toContain('"timeZone":"not-a-time-zone"');
    expect((await json(path, "PUT", { timeZone: "Pacific/Kiritimati" })).status).toBe(200);
    const kiritimati = siteDay(CAFE, Date.now());
    expect((await json(path, "PUT", { timeZone: "Pacific/Pago_Pago" })).status).toBe(200);
    // Twenty-five hours apart: never the same date.
    expect(siteDay(CAFE, Date.now())).not.toBe(kiritimati);
  });

  it("applies the venue's guest-data setting to phone sign-in, with the verified number as the phone", async () => {
    await configure();
    const text = "Gửi ưu đãi cho tôi qua SMS.";
    const set = await json(`/sites/${CAFE}/guest-data`, "PUT", {
      fields: [
        { field: "name", required: true },
        { field: "phone", required: true },
      ],
      marketingConsent: { enabled: true, text: { vi: text } },
    });
    expect(set.status).toBe(200);
    const version = ((await set.json()) as { marketingConsent: { versions: { vi: string } } }).marketingConsent.versions.vi;
    const cookie = await arrive();
    expect(await requestCode(cookie, "0912345678")).toBe("/en/phone/?sent=1");
    const code = phoneOutbox().at(-1)!.code;

    const blank = await send("/portal/phone/code", form(cookie, { code, lang: "en" }));
    expect(blank.headers.get("location")).toBe("/en/phone/?e=details-required&sent=1");
    const ok = await send(
      "/portal/phone/code",
      form(cookie, { code, lang: "en", name: "Lan", marketingConsent: "yes", consentVersion: version }),
    );
    expect(ok.headers.get("location")).toBe("/en/connected/");
    const list = (await (await send(`/sites/${CAFE}/marketing-consents`)).json()) as { items: MarketingConsent[] };
    const mine = list.items.find((c) => c.consentVersion === version);
    expect(mine).toMatchObject({ phone: "+84912345678", name: "Lan" });

    // Erased by the number, written the way the guest would say it.
    const bad = await json("/privacy/erasures", "POST", { subject: { kind: "phone", phone: "lan@example.com" } });
    expect(JSON.stringify(await bad.json())).toContain('"subject.phone":"not-a-phone-number"');
    const erased = await json("/privacy/erasures", "POST", { subject: { kind: "phone", phone: "091 234 5678" } });
    expect(erased.status).toBe(200);
    const receipt = (await erased.json()) as { subject: { masked: string }; removed: { table: string; rows: number }[] };
    expect(receipt.subject.masked).toBe("••••••••678");
    const rows = Object.fromEntries(receipt.removed.map((r) => [r.table, r.rows]));
    expect(rows.phone_codes).toBeGreaterThan(0);
    expect(rows.portal_grants).toBeGreaterThan(0);
    expect(rows.marketing_consents).toBeGreaterThan(0);
    const after = (await (await send(`/sites/${CAFE}/marketing-consents`)).json()) as { items: MarketingConsent[] };
    expect(after.items.some((c) => c.phone === "+84912345678")).toBe(false);
  });
});

describe("the ranges the console's Advanced section states", () => {
  type Schema = { properties: Record<string, { minimum?: number; maximum?: number }> };
  const schemas = (contractDocument() as unknown as { components: { schemas: Record<string, Schema> } }).components
    .schemas;

  it("are the contract's own, limit by limit", async () => {
    const declared = schemas.PhoneAbuseLimitsUpdate!.properties;
    const c = (await (await send(`/sites/${CAFE}/portal/phone/abuse-controls`)).json()) as PhoneAbuseControls;
    for (const [k, [min, max]] of Object.entries(PHONE_LIMIT_RANGES)) {
      expect([declared[k]?.minimum, declared[k]?.maximum], k).toEqual([min, max]);
      const b = c.bounds[k as keyof PhoneAbuseControls["bounds"]];
      expect([b.minimum, b.maximum], k).toEqual([min, max]);
      expect(b.default, k).toBe(c.limits[k as keyof PhoneAbuseControls["limits"]]);
    }
    const cap = schemas.PhoneSpendCapUpdate!.properties.dailyLimit!;
    expect([cap.minimum, cap.maximum]).toEqual([SPEND_CAP_RANGE.minimum, SPEND_CAP_RANGE.maximum]);
    const read = (await (await send("/phone-sign-in/spend-cap")).json()) as { bounds: unknown };
    expect(read.bounds).toEqual({ ...SPEND_CAP_RANGE });
  });
});
