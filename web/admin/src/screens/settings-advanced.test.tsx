import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { NetworkSettings, PhoneAbuseControls, RadiusServerSettings } from "@foyer/contract";
import { AuthProvider } from "../auth.js";
import { settings as settingsCopy } from "../i18n/settings.js";
import { boundHint, parsed, shown, UNITS } from "../ui/settings.js";
import { NetworkScreen, throttleBound } from "./Network.js";
import { PhoneAbusePanel } from "./portal/PhoneAbuse.js";

/**
 * The settings screens must not be a flat wall of technical inputs in milliseconds
 * and bytes. What these tests hold:
 *
 *   - what a venue sets (the portal's address, its certificate) is up front, and
 *     **every** technical knob is inside one `Advanced` section, collapsed;
 *   - each knob is in the unit a person reads — seconds, KB, MB — with the range
 *     and default **the server sent**, and the input's `min`/`max` in that unit;
 *   - "reset to default" appears on a knob that has moved, and resets it to *that
 *     knob's* default, which for the four throttles is four different figures.
 *
 * The ranges below are deliberately not the shipped ones: a screen that compiled
 * its own would still render the shipped figures and fail here.
 */

const settings: NetworkSettings = {
  portalUrl: { value: "http://10.0.0.2:8081/en/", source: "derived" },
  venueUrl: { value: null, source: "unset" },
  throttles: {
    consoleAddress: { burst: 10, intervalMs: 6000 },
    consoleAccount: { burst: 2, intervalMs: 30000 },
    portalAddress: { burst: 30, intervalMs: 1500 },
    portalAccount: { burst: 5, intervalMs: 30000 },
  },
  bodyCeilings: { portalBytes: 16384, consoleBytes: 2 << 20, anonymousBytes: 16384, headerBytes: 65536 },
  bounds: {
    throttleBurst: { minimum: 2, maximum: 700, default: 30 },
    throttleIntervalMs: { minimum: 250, maximum: 90_000, default: 1000 },
    portalBytes: { minimum: 8 << 10, maximum: 96 << 10, default: 16 << 10 },
    consoleBytes: { minimum: 1 << 20, maximum: 6 << 20, default: 1 << 20 },
    anonymousBytes: { minimum: 8 << 10, maximum: 96 << 10, default: 16 << 10 },
    headerBytes: { minimum: 16 << 10, maximum: 96 << 10, default: 64 << 10 },
  },
  defaults: {
    throttles: {
      consoleAddress: { burst: 10, intervalMs: 6000 },
      consoleAccount: { burst: 5, intervalMs: 30000 },
      portalAddress: { burst: 30, intervalMs: 1000 },
      portalAccount: { burst: 5, intervalMs: 30000 },
    },
    bodyCeilings: { portalBytes: 16384, consoleBytes: 1 << 20, anonymousBytes: 16384, headerBytes: 65536 },
  },
  portalCertificate: { state: "none", source: "none", keySet: false },
  updatedAt: null,
  updatedBy: null,
};

const radius: RadiusServerSettings = {
  maxServers: 30,
  maxServersBound: { minimum: 4, maximum: 64, default: 30 },
  eapTlsEnabled: false,
  eapCertificate: { keySet: false },
  pendingApply: false,
  applyAvailable: true,
} as RadiusServerSettings;

function render(node: ReactNode, data: [unknown[], unknown][]) {
  const qc = new QueryClient();
  qc.setQueryData(["auth"], null);
  for (const [key, value] of data) qc.setQueryData(key, value);
  return renderToStaticMarkup(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={["/network"]}>
        <AuthProvider>{node}</AuthProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const network = () =>
  render(<NetworkScreen />, [
    [["network", "settings"], settings],
    [["network", "radius"], radius],
    [["network", "coa"], {
      items: [],
      defaults: { timeoutMs: 2000, retries: 0 },
      bounds: { timeoutMs: { minimum: 200, maximum: 10000, default: 2000 }, retries: { minimum: 0, maximum: 5, default: 0 } },
    }],
  ]);

/** The markup before the `Advanced` section and inside it. */
const split = (html: string) => {
  const at = html.indexOf('<details class="advanced"');
  expect(at, "the screen has an Advanced section").toBeGreaterThan(0);
  return { front: html.slice(0, at), advanced: html.slice(at) };
};

describe("/network, reorganised", () => {
  it("puts what a venue sets up front and every technical knob in one collapsed section", () => {
    const html = network();
    const { front, advanced } = split(html);
    expect(front).toContain('type="url"');
    expect(front).toContain('id="network-cert"');
    // Not one number input before the fold: the wall of technical inputs is behind it.
    expect(front).not.toMatch(/type="number"/);
    expect(advanced.match(/type="number"/g)?.length).toBeGreaterThanOrEqual(13);
    // Collapsed by default: `<details>` with no `open`.
    expect(html).toMatch(/<details class="advanced">/);
    expect(html).not.toMatch(/<details class="advanced" open/);
    expect(advanced).toContain('id="network-radius"');
    expect(advanced).toContain('id="network-coa"');
  });

  it("speaks seconds, KB and MB, with the server's range in those units", () => {
    const { advanced } = split(network());
    // 1 500 ms, shown as 1.5 seconds; the range 250 ms – 90 s as 0.25 – 90.
    expect(advanced).toContain('value="1.5"');
    expect(advanced).toContain('min="0.25"');
    expect(advanced).toContain('max="90"');
    expect(advanced).toContain(boundHint(settings.bounds.throttleIntervalMs, UNITS.msAsSeconds).split(" Default")[0]!);
    // 2 MiB of console body as 2 MB, and 16 KiB of portal body as 16 KB.
    expect(advanced).toMatch(/value="2"[^>]*>[^<]*<span class="setting-unit">MB/);
    expect(advanced).toMatch(/value="16"[^>]*>[^<]*<span class="setting-unit">KB/);
    // No milliseconds and no bytes anywhere an operator reads.
    expect(advanced).not.toMatch(/\(ms\)|Bytes,/);
  });

  it("offers a reset on exactly the knobs that have moved from their own default", () => {
    const { advanced } = split(network());
    const resets = advanced.match(new RegExp(settingsCopy["settings.reset"], "g"))?.length ?? 0;
    /* Moved: consoleAccount's burst (2, default 5), portalAddress's interval (1 500,
       default 1 000) and the console body (2 MB, default 1 MB). Everything else sits
       at its default — and against the one shared `bounds.throttleBurst.default`
       (30) the console buckets would all have looked moved. */
    expect(resets).toBe(3);
  });

  it("resets a throttle to that throttle's default, not the shared one", () => {
    expect(throttleBound(settings, "consoleAccount", "burst").default).toBe(5);
    expect(throttleBound(settings, "portalAddress", "burst").default).toBe(30);
    expect(throttleBound(settings, "consoleAddress", "intervalMs").default).toBe(6000);
    expect(throttleBound(settings, "portalAddress", "intervalMs")).toMatchObject({ minimum: 250, maximum: 90_000 });
  });
});

describe("a setting's unit", () => {
  it("round-trips what a person types into what the server holds", () => {
    expect(parsed("0.5", UNITS.msAsSeconds)).toBe(500);
    expect(parsed("0,5", UNITS.msAsSeconds)).toBe(500);
    expect(parsed("15", UNITS.secondsAsMinutes)).toBe(900);
    expect(parsed("2", UNITS.bytesAsMb)).toBe(2 << 20);
    expect(Number.isNaN(parsed("", UNITS.count))).toBe(true);
    expect(shown(900, UNITS.secondsAsMinutes)).toBe("15");
    expect(shown(100, UNITS.msAsSeconds)).toBe("0.1");
  });
});

describe("phone sign-in's limits", () => {
  const controls: PhoneAbuseControls = {
    siteId: "site-hcm",
    limits: { perNumberPerHour: 3, perNumberPerDay: 5, perDevicePerHour: 5, perAddressPerHour: 90, codeMinutes: 5, maxAttempts: 5 },
    bounds: {
      perNumberPerHour: { minimum: 1, maximum: 7, default: 3 },
      perNumberPerDay: { minimum: 1, maximum: 13, default: 5 },
      perDevicePerHour: { minimum: 1, maximum: 17, default: 5 },
      perAddressPerHour: { minimum: 1, maximum: 1234, default: 60 },
      codeMinutes: { minimum: 1, maximum: 9, default: 5 },
      maxAttempts: { minimum: 1, maximum: 8, default: 5 },
    },
    mobilePrefixes: ["03", "09"],
    country: "VN",
    customised: true,
    updatedAt: null,
  };

  it("are Advanced, collapsed, with the server's ranges and a reset where one moved", () => {
    const html = render(<PhoneAbusePanel siteId="site-hcm" canManage />, [
      [["phone-abuse", "site-hcm"], controls],
      [["phone-spend-cap"], { dailyLimit: null, updatedAt: null, bounds: { minimum: 1, maximum: 4321 } }],
    ]);
    const { front, advanced } = split(html);
    expect(html).not.toMatch(/<details class="advanced" open/);
    for (const max of ["7", "13", "17", "1234", "9", "8"]) expect(advanced).toContain(`max="${max}"`);
    expect(advanced.match(new RegExp(settingsCopy["settings.reset"], "g"))?.length).toBe(1);
    // The deployment's cap is money, not a protocol figure: it stays in view, bounded by the server.
    expect(front + advanced.slice(advanced.indexOf("</details>"))).toContain('max="4321"');
  });
});
