/**
 * The admin-configurability rule's operations against the oracle: a write is what
 * the next read answers, and the guest Wi-Fi password comes back only from the
 * voucher slip's read — the same guarantees `internal/api/settings_test.go` proves
 * of the Go server.
 */
import { beforeEach, describe, expect, it } from "vitest";
import type { Account, MaintenanceSettings } from "@foyer/contract";
import { app } from "./server.js";
import { resetSettings } from "./settings.js";

const send = (path: string, init?: RequestInit) => app.request(`/api/v0${path}`, init);
const json = (path: string, method: string, body: unknown) =>
  send(path, { method, headers: { "content-type": "application/json" }, body: JSON.stringify(body) });

beforeEach(() => resetSettings());

describe("maintenance settings", () => {
  it("answers the saved values on the next read", async () => {
    const fresh = (await (await send("/privacy/maintenance")).json()) as MaintenanceSettings;
    expect(fresh.isDefault).toBe(true);
    expect(fresh.intervalSeconds).toBe(300);
    const body = { intervalSeconds: 900, staleAfterMissedUpdates: 6, staleCeilingPerPass: 50, retentionBatchSize: 100 };
    expect((await json("/privacy/maintenance", "PUT", body)).status).toBe(200);
    const after = (await (await send("/privacy/maintenance")).json()) as MaintenanceSettings;
    expect(after).toMatchObject({ ...body, isDefault: false });
  });
});

describe("the guest Wi-Fi password", () => {
  it("is write-only on the settings operations and read by the slip", async () => {
    const secret = "Pho-Bo-2026!";
    const put = await json("/sites/site-hanoi/guest-wifi-key", "PUT", { passphrase: secret });
    const status = await (await send("/sites/site-hanoi/guest-wifi-key")).text();
    expect(put.status).toBe(200);
    expect(await put.text()).not.toContain(secret);
    expect(status).not.toContain(secret);
    expect(JSON.parse(status).set).toBe(true);
    const slip = await (await send("/sites/site-hanoi/voucher-slip-wifi")).json();
    expect(slip).toEqual({ passphrase: secret });
  });
});

describe("an account's 802.1X opt-in", () => {
  it("needs the password in the same request, and then reaches credentialKind", async () => {
    const created = (await (
      await json("/accounts", "POST", {
        siteId: "site-hanoi",
        username: "mock.dot1x",
        password: "a-long-enough-password",
      })
    ).json()) as Account;
    const refused = await json(`/accounts/${created.id}`, "PATCH", { eapTtlsPap: true });
    expect(refused.status).toBe(422);
    const on = (await (
      await json(`/accounts/${created.id}`, "PATCH", { eapTtlsPap: true, password: "another-long-password" })
    ).json()) as Account;
    expect(on.credentialKind).toBe("argon2id+bcrypt");
    await send(`/accounts/${created.id}`, { method: "DELETE" });
  });
});
