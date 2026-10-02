import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { portalGrants } from "../i18n/portalgrants.js";
import { GrantOutcome, grantFailureSentence } from "./PortalGrants.jsx";

/**
 * The portal-authorization list's decisions that need no server (review2:disconnect):
 * what a controller that did not confirm is said to have done. That the Vietnamese
 * copy covers every English key is `i18n/i18n.test.ts`'s, for every catalogue at once.
 */
describe("a portal guest's disconnect", () => {
  it("says the controller confirmed, only when it did", () => {
    const html = renderToStaticMarkup(
      <GrantOutcome result={{ disconnected: true, grantsRevoked: 1, voucherRevoked: false }} />,
    );
    expect(html).toContain(portalGrants["grants.done"]);
  });

  it("says the permission is withdrawn and the guest may still be on, with the cause", () => {
    const html = renderToStaticMarkup(
      <GrantOutcome
        result={{
          disconnected: false,
          grantsRevoked: 1,
          voucherRevoked: false,
          failure: { type: "/problems/session-end-failed", params: { cause: "no-answer", nasName: "UniFi" } },
        }}
      />,
    );
    // React escapes the apostrophe, so the verdict is matched past it.
    expect(html).toContain(portalGrants["grants.failed"].split("'").at(-1)!);
    expect(html).toContain(portalGrants["grants.cause.no-answer"]);
    expect(html).not.toContain(portalGrants["grants.done"]);
  });

  it("falls back to a generic cause rather than rendering a word it does not know", () => {
    expect(grantFailureSentence({ type: "/problems/session-end-failed", params: { cause: "quantum" } })).toBe(
      portalGrants["grants.cause.error"],
    );
  });
});
