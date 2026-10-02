import { afterEach, describe, expect, it } from "vitest";
import { normaliseSetupCode } from "./auth.js";
import { DEFAULT_CEILINGS, DEFAULT_THROTTLES, bootstrapPortalUrl, saveTransport } from "./network.js";
import { app } from "./server.js";
import { DEV_PASSWORD } from "./state.js";

/**
 * The mock's copy of the console's sign-in defences (`auth.ts`), driven through the
 * routes, and **following the console's settings**: the throttle is the one the
 * console saved, not a constant, and the body ceilings apply.
 */

const setSettings = (patch: {
  consoleAccount?: { burst: number; intervalMs: number };
  anonymousBytes?: number;
  portalBytes?: number;
}) =>
  saveTransport(
    {
      portalUrl: null,
      venueUrl: null,
      throttles: { ...DEFAULT_THROTTLES, ...(patch.consoleAccount ? { consoleAccount: patch.consoleAccount } : {}) },
      bodyCeilings: {
        ...DEFAULT_CEILINGS,
        ...(patch.anonymousBytes ? { anonymousBytes: patch.anonymousBytes } : {}),
        ...(patch.portalBytes ? { portalBytes: patch.portalBytes } : {}),
      },
    },
    "test",
  );

afterEach(() => setSettings({}));

const signIn = (username: string, password: string, cookie?: string) =>
  app.request("/api/v0/auth/session", {
    method: "POST",
    headers: { "content-type": "application/json", ...(cookie ? { cookie } : {}) },
    body: JSON.stringify({ username, password }),
  });

const deviceCookie = (res: Response) =>
  (res.headers.get("set-cookie") ?? "")
    .split(/,(?=\s*[\w-]+=)/)
    .map((c) => c.trim().split(";")[0]!)
    .find((c) => c.startsWith("foyer_device="));

describe("the sign-in throttle", () => {
  it("follows the console's per-account setting", async () => {
    setSettings({ consoleAccount: { burst: 2, intervalMs: 60_000 } });
    const user = "throttle.setting.drive";
    for (let i = 0; i < 2; i++) expect((await signIn(user, "x")).status).toBe(401);
    const third = await signIn(user, "x");
    expect(third.status, "the setting's burst of two did not bind").toBe(429);
    expect(Number(third.headers.get("retry-after"))).toBeGreaterThanOrEqual(1);
  });

  it("does not let a stranger keep the account's holder out of a device they signed in from", async () => {
    const holder = "dang.quoc.viet";
    const first = await signIn(holder, DEV_PASSWORD);
    expect(first.status).toBe(200);
    const device = deviceCookie(first);
    expect(device, "a successful sign-in set no trusted-device cookie").toBeTruthy();

    for (let i = 0; i < 5; i++) await signIn(holder, "a-guess");
    expect((await signIn(holder, DEV_PASSWORD)).status, "the shared bucket is not empty").toBe(429);
    expect((await signIn(holder, DEV_PASSWORD, device)).status).toBe(200);
    /* A forged cookie is a stranger's. */
    expect((await signIn(holder, DEV_PASSWORD, `${device}x`)).status).toBe(429);
  });
});

describe("the body ceilings", () => {
  it("refuses a public operation over the anonymous ceiling, and follows the setting", async () => {
    const body = JSON.stringify({ username: "u".repeat(10 << 10), password: "p" });
    expect((await signIn("u".repeat(10 << 10), "p")).status, "10 KiB is under the 16 KiB default").not.toBe(413);
    setSettings({ anonymousBytes: 8 << 10 });
    const res = await app.request("/api/v0/auth/session", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body,
    });
    expect(res.status).toBe(413);
    const problem = (await res.json()) as {
      type: string;
      params: { fields: Record<string, string>; fieldParams: { body: { limitBytes: number } } };
    };
    expect(problem.type).toBe("/problems/too-large");
    expect(problem.params.fields.body).toBe("too-large");
    expect(problem.params.fieldParams.body.limitBytes).toBe(8 << 10);
  });

  it("holds the guest portal to its own ceiling", async () => {
    setSettings({ portalBytes: 8 << 10 });
    const res = await app.request("/api/v0/portal/voucher", {
      method: "POST",
      headers: { "content-type": "application/x-www-form-urlencoded" },
      body: `code=${"A".repeat(9 << 10)}&lang=en`,
    });
    expect(res.status).toBe(413);
    expect(bootstrapPortalUrl()).toBeTruthy();
  });
});

describe("the setup code", () => {
  it("reads a code the way a person retypes it", () => {
    expect(normaliseSetupCode("abcd efgh-jkmn pq0O")).toBe("ABCDEFGHJKMNPQ00");
    expect(normaliseSetupCode("0000-0000-0000-000U")).toBe("");
  });
});
