import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Account, GuestWifiKeyStatus, MaintenanceSettings } from "@foyer/contract";
import { maintenance } from "../i18n/maintenance.js";
import { credentials } from "../i18n/credentials.js";
import { wifiKey } from "../i18n/wifikey.js";
import { GuestWifiKey } from "../print/GuestWifiKey.js";
import { AccountCredentialsDialog } from "./AccountCredentials.js";
import { Maintenance } from "./Maintenance.js";

/**
 * Three surfaces that make operator settings configurable in the console, at the
 * component: each renders what the server said is in force, and the write-only
 * one has nothing to render a secret from. The writes themselves are proven against
 * both servers — `internal/api/settings_test.go` and `web/mock/src/settings.test.ts`.
 */

function withData(entries: [unknown[], unknown][], node: ReactNode) {
  const qc = new QueryClient();
  for (const [key, value] of entries) qc.setQueryData(key, value);
  return renderToStaticMarkup(<QueryClientProvider client={qc}>{node}</QueryClientProvider>);
}

const limits = {
  intervalSeconds: { minimum: 60, maximum: 86400, default: 300 },
  staleAfterMissedUpdates: { minimum: 3, maximum: 48, default: 4 },
  staleCeilingPerPass: { minimum: 1, maximum: 100000, default: 200 },
  retentionBatchSize: { minimum: 50, maximum: 10000, default: 500 },
};

describe("the maintenance section", () => {
  it("shows the values in force, with the server's bounds on each input", () => {
    const s: MaintenanceSettings = {
      intervalSeconds: 900,
      staleAfterMissedUpdates: 6,
      staleCeilingPerPass: 50,
      retentionBatchSize: 100,
      isDefault: false,
      updatedAt: "2026-09-23T08:00:00Z",
      updatedBy: "kim.administrator",
      limits,
    };
    const html = withData([[["privacy", "maintenance"], s]], <Maintenance />);
    // Held in seconds, read in minutes: 900 s is 15, the 60 s – 24 h range 1 – 1440.
    expect(html).toContain('value="15"');
    expect(html).toContain('min="1"');
    expect(html).toContain('max="1440"');
    // All four are technical, so the section is Advanced and starts collapsed.
    expect(html).toMatch(/^<details class="advanced">/);
    expect(html).toContain('value="6"');
    // Six missed updates at the five-minute floor is half an hour, said in minutes.
    expect(html).toContain(maintenance["maintenance.stale.hint"](30));
    expect(html).toContain("kim.administrator");
    expect(html).not.toContain(maintenance["maintenance.default"]);
  });
});

describe("the credentials dialog", () => {
  const account = (kind: Account["credentialKind"]): Account => ({
    id: "acc-x",
    siteId: "site-hcm",
    username: "lan.nguyen",
    displayName: null,
    enabled: true,
    createdAt: "2026-09-01T00:00:00Z",
    lastLoginAt: null,
    credentialKind: kind,
  });
  const render = (kind: Account["credentialKind"]) =>
    withData([], <AccountCredentialsDialog account={account(kind)} onClose={() => {}} onSaved={() => {}} />);

  it("starts the 802.1X box at the account's own state, read from credentialKind", () => {
    expect(render("argon2id+bcrypt")).toMatch(/<input type="checkbox"[^>]*checked=""/);
    expect(render("argon2id")).not.toMatch(/<input type="checkbox"[^>]*checked=""/);
    expect(render("argon2id")).toContain(credentials["credentials.dot1xHint"]);
  });
});

describe("the guest Wi-Fi password", () => {
  it("says a password is set and has nothing to show it from", () => {
    const status: GuestWifiKeyStatus = { set: true, updatedAt: "2026-09-23T08:00:00Z", updatedBy: "kim.administrator" };
    const html = withData([[["guest-wifi-key", "site-hcm"], status]], <GuestWifiKey siteId="site-hcm" canManage />);
    expect(html).toContain("It is not shown again.");
    expect(html).toMatch(/<input[^>]*type="password"[^>]*value=""/);
    expect(html).toContain(wifiKey["wifiKey.replace"]);
  });

  it("says slips join an open network when none is set, and offers no removal", () => {
    const status: GuestWifiKeyStatus = { set: false, updatedAt: null, updatedBy: null };
    const html = withData([[["guest-wifi-key", "site-hcm"], status]], <GuestWifiKey siteId="site-hcm" canManage />);
    expect(html).toContain(wifiKey["wifiKey.unset"]);
    expect(html).not.toContain(wifiKey["wifiKey.clear"]);
  });
});
