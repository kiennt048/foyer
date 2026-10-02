import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Capabilities, Nas } from "@foyer/contract";
import { DisconnectPanel, disconnectOffer, failureSentence } from "./SessionDisconnect.js";

/**
 * The disconnect control's gate, proved against the capability that decides it.
 *
 * `docs/rules/frontend.md` asks for capability gating to be proven against a
 * control **with behaviour**, and the lesson behind that is written into this repo:
 * a gate proven only against controls that did nothing was a gate that proved
 * nothing. The behaviour — a click that issues exactly one `POST
 * /sessions/{id}/disconnect` — is asserted in `web/e2e` (`@foyer/admin` has no
 * jsdom). What is asserted here is the decision and the markup it produces.
 */

const caps = (canDeauthGuest: boolean): Capabilities => ({
  liveReauthorize: false,
  walledGarden: "none",
  hardQuota: false,
  bandwidthLimits: false,
  dynamicVlan: false,
  rotateSecret: false,
  canDeauthGuest,
});
const device = (driver: Nas["driver"], can: boolean) => ({ driver, capabilities: caps(can) });

describe("disconnectOffer", () => {
  it("offers the control where the driver can end a session", () => {
    for (const d of ["unifi", "mikrotik", "coa"] as const) {
      expect(disconnectOffer({ state: "online" }, device(d, true), true)).toEqual({ kind: "offer" });
    }
    // Orphaned and unknown may still have a guest on them; only a recorded stop is over.
    expect(disconnectOffer({ state: "orphaned" }, device("unifi", true), true).kind).toBe("offer");
    expect(disconnectOffer({ state: "unknown" }, device("unifi", true), true).kind).toBe("offer");
  });

  it("says why where the driver cannot, per vendor", () => {
    expect(disconnectOffer({ state: "online" }, device("ruckus", false), true)).toEqual({
      kind: "cannot",
      reason: "ruckus",
    });
    expect(disconnectOffer({ state: "online" }, device("omada", false), true)).toEqual({
      kind: "cannot",
      reason: "omada",
    });
    expect(disconnectOffer({ state: "online" }, device("unifi", false), true)).toEqual({
      kind: "cannot",
      reason: "unprobed",
    });
  });

  it("offers nothing on an ended session, without the permission, or for an unknown device", () => {
    expect(disconnectOffer({ state: "ended" }, device("unifi", true), true).kind).toBe("hidden");
    expect(disconnectOffer({ state: "online" }, device("unifi", true), false).kind).toBe("hidden");
    expect(disconnectOffer({ state: "online" }, null, true).kind).toBe("hidden");
  });
});

describe("DisconnectPanel", () => {
  const render = (offer: Parameters<typeof DisconnectPanel>[0]["offer"], result = null) =>
    renderToStaticMarkup(<DisconnectPanel offer={offer} result={result} onOpen={() => {}} />);

  it("renders the button, and nothing that enters data, where it can", () => {
    const html = render({ kind: "offer" });
    expect(html).toContain("Disconnect guest");
    expect(html).toContain("<button");
    // The dock is asserted in web/e2e to contain no data-entry control; the options
    // live in the portalled dialog.
    expect(html).not.toMatch(/<input|<select|<textarea/);
  });

  it("renders a reason and no button where it cannot", () => {
    const html = render({ kind: "cannot", reason: "ruckus" });
    expect(html).not.toContain("<button");
    expect(html).toContain("Northbound API");
  });

  it("says the guest is still online when the device did not confirm", () => {
    const html = renderToStaticMarkup(
      <DisconnectPanel
        offer={{ kind: "offer" }}
        result={{
          disconnected: false,
          grantsRevoked: 1,
          voucherRevoked: true,
          failure: { type: "/problems/session-end-failed", params: { cause: "refused", errorCause: 503 } },
        }}
        onOpen={() => {}}
      />,
    );
    expect(html).toContain("still online");
    expect(html).toContain("Error-Cause 503");
    expect(html).toContain("voucher is revoked");
    expect(html).toContain("<button"); // may try again
  });
});

describe("failureSentence", () => {
  it("has a sentence for every cause the contract names", () => {
    const seen = new Set(
      ["refused", "no-answer", "bad-secret", "error", undefined].map((cause) =>
        failureSentence({ type: "/problems/session-end-failed", params: cause ? { cause } : {} }),
      ),
    );
    // `error` and a missing cause share the fallback; the other three are distinct.
    expect(seen.size).toBe(4);
  });
});
