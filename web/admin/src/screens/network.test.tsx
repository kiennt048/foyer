import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { PortalCertificateStatus, Problem } from "@foyer/contract";
import { ApiError } from "../api.js";
import { network } from "../i18n/network.js";
import { ApplyOutcome, CertificateFacts, appliedKey, networkFieldMessages, updateFromDraft } from "./Network.jsx";

/**
 * `/network`. The screen's decisions that do not
 * need a server: which sentence a refused certificate gets, what an emptied address
 * means, what a stored certificate is described as. That the Vietnamese copy covers
 * every English key is `i18n/i18n.test.ts`'s, for every catalogue at once.
 */

const refusal = (fields: Record<string, string>, fieldParams: Record<string, Record<string, unknown>>) =>
  new ApiError(422, {
    type: "/problems/validation",
    title: "That cannot be saved",
    status: 422,
    errors: Object.fromEntries(Object.keys(fields).map((f) => [f, "Refused."])),
    params: { fields, fieldParams },
  } as Problem);

describe("a refused certificate", () => {
  it("is described by its reason, on the field the server named", () => {
    const m = networkFieldMessages(
      refusal(
        { privateKeyPem: "certificate-refused", portalUrl: "certificate-refused" },
        { privateKeyPem: { reason: "key-mismatch" }, portalUrl: { reason: "name-not-covered" } },
      ),
    );
    expect(m.privateKeyPem).toBe(network["network.cert.refused.key-mismatch"]);
    expect(m.portalUrl).toBe(network["network.cert.refused.name-not-covered"]);
  });

  it("falls back to the console's sentence for a reason this screen predates", () => {
    const m = networkFieldMessages(refusal({ certificatePem: "certificate-refused" }, { certificatePem: { reason: "quantum" } }));
    expect(m.certificatePem).toBeTruthy();
    expect(m.certificatePem).not.toBe("quantum");
  });
});

describe("an apply's answer", () => {
  it("shows the supervisor's reason for a refusal, not a pointer to a log", () => {
    const html = renderToStaticMarkup(
      <ApplyOutcome
        applied={{
          at: "2026-09-23T10:00:00Z",
          outcome: "failed",
          restarted: false,
          detail: "radiusd -C refused the configuration: mods-enabled/eap[12]: Failed reading private key",
        }}
      />,
    );
    expect(html).toContain(network["network.radius.applied.failed"]);
    expect(html).toContain("Failed reading private key");
  });

  it("does not say FreeRADIUS restarted when it was already running the tree", () => {
    expect(appliedKey({ at: "2026-09-23T10:00:00Z", outcome: "applied", restarted: false })).toBe(
      "network.radius.applied.unchanged",
    );
    expect(appliedKey({ at: "2026-09-23T10:00:00Z", outcome: "applied", restarted: true })).toBe(
      "network.radius.applied.applied",
    );
  });
});

describe("the settings form", () => {
  it("sends an emptied address as null, which is the installation's own", () => {
    const u = updateFromDraft({
      portalUrl: "  ",
      venueUrl: " https://venue.example/about ",
      throttles: {
        consoleAddress: { burst: 10, intervalMs: 6000 },
        consoleAccount: { burst: 5, intervalMs: 30000 },
        portalAddress: { burst: 300, intervalMs: 200 },
        portalAccount: { burst: 5, intervalMs: 30000 },
      },
      bodyCeilings: { portalBytes: 16384, consoleBytes: 1048576, anonymousBytes: 16384, headerBytes: 65536 },
    });
    expect(u.portalUrl).toBeNull();
    expect(u.venueUrl).toBe("https://venue.example/about");
    expect(u.throttles.portalAddress.burst).toBe(300);
  });
});

describe("the certificate's description", () => {
  const served: PortalCertificateStatus = {
    state: "served",
    source: "console",
    keySet: true,
    dnsNames: ["wifi.venue.test"],
    ipAddresses: [],
    notAfter: "2026-12-01T00:00:00Z",
    fingerprintSha256: "ab".repeat(32),
  };

  it("names what is served and says only that a key is set", () => {
    const html = renderToStaticMarkup(<CertificateFacts st={served} />);
    expect(html).toContain("wifi.venue.test");
    expect(html).toContain("ab".repeat(32));
    expect(html).toContain(network["network.cert.keySet"]);
    expect(html).not.toMatch(/PRIVATE KEY/);
  });

  it("says why a stored certificate is not served", () => {
    const html = renderToStaticMarkup(
      <CertificateFacts st={{ ...served, state: "not-served", notServedReason: "name-not-covered" }} />,
    );
    expect(html).toContain(network["network.cert.reason.name-not-covered"]);
  });
});
