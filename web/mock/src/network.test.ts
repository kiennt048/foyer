import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import type { NasCoaTiming, NetworkSettings, PortalCertificateStatus, RadiusServerSettings } from "@foyer/contract";
import { app } from "./server.js";
import { DEV_PASSWORD, world } from "./state.js";

/**
 * The `network` tag in the oracle, driven through
 * the real routes. `internal/api/network_test.go` holds the Go server to the same
 * sentences; these are what `make diff-api` compares it against.
 */

let cookieValue = "";
async function cookie(): Promise<string> {
  if (cookieValue) return cookieValue;
  const res = await app.request("/api/v0/auth/session", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ username: "dang.quoc.viet", password: DEV_PASSWORD }),
  });
  expect(res.status).toBe(200);
  cookieValue = res.headers.get("set-cookie")?.split(";")[0] ?? "";
  return cookieValue;
}

async function call(method: string, path: string, body?: unknown) {
  return app.request(`/api/v0${path}`, {
    method,
    headers: { "content-type": "application/json", cookie: await cookie() },
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  });
}

async function settings(): Promise<NetworkSettings> {
  return (await (await call("GET", "/network/settings")).json()) as NetworkSettings;
}

const fault = (p: { params?: { fields?: Record<string, string>; fieldParams?: Record<string, Record<string, unknown>> } }, f: string) => ({
  fault: p.params?.fields?.[f],
  params: p.params?.fieldParams?.[f],
});

describe("network settings", () => {
  it("answers the deployment's own address until the console saves one, then that one", async () => {
    const before = await settings();
    expect(before.portalUrl.source).toBe("derived");
    const res = await call("PUT", "/network/settings", {
      portalUrl: "http://wifi.venue.test/en/",
      venueUrl: null,
      throttles: before.throttles,
      bodyCeilings: before.bodyCeilings,
    });
    expect(res.status).toBe(200);
    const after = (await res.json()) as NetworkSettings;
    expect(after.portalUrl).toEqual({ value: "http://wifi.venue.test/en/", source: "console" });
    expect(after.updatedBy).toBe("dang.quoc.viet");

    const back = await call("PUT", "/network/settings", { ...after, portalUrl: null, venueUrl: null });
    expect(((await back.json()) as NetworkSettings).portalUrl.source).toBe("derived");
  });

  it("bounds every number with the figure, from the bounds it reports", async () => {
    const s = await settings();
    const res = await call("PUT", "/network/settings", {
      portalUrl: null,
      venueUrl: null,
      throttles: { ...s.throttles, portalAddress: { burst: s.bounds.throttleBurst.maximum + 1, intervalMs: 50 } },
      bodyCeilings: { ...s.bodyCeilings, consoleBytes: s.bounds.consoleBytes.minimum - 1 },
    });
    expect(res.status).toBe(422);
    const p = await res.json();
    expect(fault(p, "throttles.portalAddress.burst")).toEqual({ fault: "above-maximum", params: { maximumCount: 1000 } });
    expect(fault(p, "throttles.portalAddress.intervalMs")).toEqual({ fault: "below-minimum", params: { minimumCount: 100 } });
    expect(fault(p, "bodyCeilings.consoleBytes").fault).toBe("below-minimum");
  });
});

describe("the portal certificate", () => {
  /* A pair generated for this run and never written into the repository
     (`docs/rules/security.md` §7). The drive needs `openssl`; without it the
     positive path is the Go suite's alone and this says so. */
  function pair(): { cert: string; key: string } | null {
    try {
      const dir = mkdtempSync(join(tmpdir(), "foyer-net-"));
      execFileSync("openssl", [
        "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:prime256v1", "-nodes",
        "-keyout", join(dir, "k.pem"), "-out", join(dir, "c.pem"), "-days", "30",
        "-subj", "/CN=wifi.venue.test", "-addext", "subjectAltName=DNS:wifi.venue.test",
      ], { stdio: "ignore" });
      return { cert: readFileSync(join(dir, "c.pem"), "utf8"), key: readFileSync(join(dir, "k.pem"), "utf8") };
    } catch {
      return null;
    }
  }

  it("is checked, served at a name it covers, and never answered with its key", async () => {
    const p = pair();
    if (!p) {
      console.warn("openssl is not on PATH: the mock's positive certificate path was not driven");
      return;
    }
    const other = pair()!;
    const refused = await call("PUT", "/network/portal-certificate", {
      certificatePem: p.cert, privateKeyPem: other.key, portalUrl: "https://wifi.venue.test/en/",
    });
    expect(fault(await refused.json(), "privateKeyPem")).toEqual({ fault: "certificate-refused", params: { reason: "key-mismatch" } });

    const wrongName = await call("PUT", "/network/portal-certificate", {
      certificatePem: p.cert, privateKeyPem: p.key, portalUrl: "https://elsewhere.test/en/",
    });
    expect(fault(await wrongName.json(), "portalUrl").params).toEqual({ reason: "name-not-covered" });

    const res = await call("PUT", "/network/portal-certificate", {
      certificatePem: p.cert, privateKeyPem: p.key, portalUrl: "https://wifi.venue.test/en/",
    });
    const text = await res.text();
    expect(res.status).toBe(200);
    expect(text).not.toMatch(/PRIVATE KEY/);
    const st = JSON.parse(text) as PortalCertificateStatus;
    expect(st).toMatchObject({ state: "served", source: "console", keySet: true, dnsNames: ["wifi.venue.test"] });
    expect(JSON.stringify(await settings())).not.toMatch(/PRIVATE KEY/);

    /* With a certificate served, the address may not move off it. */
    const s = await settings();
    const moved = await call("PUT", "/network/settings", { ...s, portalUrl: "https://elsewhere.test/en/", venueUrl: null });
    expect(fault(await moved.json(), "portalUrl").params).toEqual({ reason: "name-not-covered" });

    expect((await (await call("DELETE", "/network/portal-certificate")).json()).state).toBe("none");
    await call("PUT", "/network/settings", { ...s, portalUrl: null, venueUrl: null });
  });
});

describe("FreeRADIUS", () => {
  it("syncs the client table by itself after a RADIUS-client write", async () => {
    const nas = world.nas.find((n) => n.enabled)!;
    const res = await call("PATCH", `/nas/${nas.id}`, { name: nas.name });
    expect(res.status).toBe(200);
    const r = (await (await call("GET", "/network/radius")).json()) as RadiusServerSettings;
    expect(r.lastSync?.trigger).toBe("nas-updated");
    expect(r.lastSync?.outcome).toBe("synced");
  });

  it("keeps a generator setting pending until an apply restarts FreeRADIUS on it", async () => {
    const bad = await call("PUT", "/network/radius", { maxServers: 2, eapTlsEnabled: false });
    expect(fault(await bad.json(), "maxServers")).toEqual({ fault: "below-minimum", params: { minimumCount: 4 } });
    const noCert = await call("PUT", "/network/radius", { maxServers: 30, eapTlsEnabled: true });
    expect(fault(await noCert.json(), "eapTlsEnabled").fault).toBe("required");
    // 45 is past the PostgreSQL budget the Go side derives its ceiling from.
    const tooMany = await call("PUT", "/network/radius", { maxServers: 45, eapTlsEnabled: false });
    expect(fault(await tooMany.json(), "maxServers").fault).toBe("above-maximum");

    const ok = (await (await call("PUT", "/network/radius", { maxServers: 40, eapTlsEnabled: false })).json()) as RadiusServerSettings;
    expect(ok).toMatchObject({ maxServers: 40, pendingApply: true, applyAvailable: true });
    const applied = await (await call("POST", "/network/radius/apply")).json();
    expect(applied).toMatchObject({ outcome: "applied", restarted: true });
    expect(((await (await call("GET", "/network/radius")).json()) as RadiusServerSettings).pendingApply).toBe(false);
    await call("PUT", "/network/radius", { maxServers: 30, eapTlsEnabled: false });
  });

  it("stores CoA timing per device, bounded, and refuses a device that does not send CoA", async () => {
    const coa = world.nas.find((n) => n.driver === "mikrotik" || n.driver === "coa");
    const unifi = world.nas.find((n) => n.driver === "unifi")!;
    expect(coa, "the fixture world has a device that speaks RFC 5176 itself").toBeTruthy();
    const bad = await call("PUT", `/nas/${coa!.id}/coa-timing`, { timeoutMs: 50, retries: 9 });
    const p = await bad.json();
    expect(fault(p, "timeoutMs").fault).toBe("below-minimum");
    expect(fault(p, "retries").fault).toBe("above-maximum");

    const ok = (await (await call("PUT", `/nas/${coa!.id}/coa-timing`, { timeoutMs: 800, retries: 2 })).json()) as NasCoaTiming;
    expect(ok).toMatchObject({ timeoutMs: 800, retries: 2, source: "console" });
    const list = await (await call("GET", "/network/coa-timing")).json();
    expect(list.items.find((i: NasCoaTiming) => i.nasId === coa!.id)?.source).toBe("console");
    expect(list.items.some((i: NasCoaTiming) => i.nasId === unifi.id)).toBe(false);

    const refused = await call("PUT", `/nas/${unifi.id}/coa-timing`, { timeoutMs: 800, retries: 2 });
    expect(fault(await refused.json(), "nasId").fault).toBe("incorrect");
    await call("PUT", `/nas/${coa!.id}/coa-timing`, { timeoutMs: null, retries: null });
  });
});

describe("the figures the console's Advanced section needs", () => {
  it("sends each throttle's own default, not one for all four", async () => {
    const s = await settings();
    expect(s.defaults.throttles.consoleAccount).not.toEqual(s.defaults.throttles.portalAddress);
    expect(s.defaults.bodyCeilings.consoleBytes).toBe(s.bounds.consoleBytes.default);
  });

  it("sends the CoA range it enforces, and one past it is refused", async () => {
    const list = (await (await call("GET", "/network/coa-timing")).json()) as {
      items: NasCoaTiming[];
      bounds: { timeoutMs: { minimum: number; maximum: number; default: number } };
    };
    const b = list.bounds.timeoutMs;
    const nasId = list.items[0]!.nasId;
    const over = await call("PUT", `/nas/${nasId}/coa-timing`, { timeoutMs: b.maximum + 1, retries: 0 });
    expect(over.status).toBe(422);
    const at = await call("PUT", `/nas/${nasId}/coa-timing`, { timeoutMs: b.maximum, retries: 0 });
    expect(at.status).toBe(200);
    await call("PUT", `/nas/${nasId}/coa-timing`, { timeoutMs: null, retries: null });
  });
});
