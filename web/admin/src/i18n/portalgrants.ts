/**
 * Copy for the sessions screen's portal-authorization list, loaded with it. English;
 * the Vietnamese is `vi/portalgrants.ts`, fetched only when the console speaks it.
 *
 * The list is `lazy()` out of the sessions screen, so these sentences travel in its
 * chunk and the entry pays only for the import edge. `core.ts` explains the rule.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const portalGrants = {
  "grants.title": "Signed in through the portal",
  "grants.body":
    "Guests the portal let on whose access has not ended. A network whose controller authorizes guests without RADIUS accounting has no session rows, so a guest can be online here and absent above.",
  "grants.empty": "No guest is signed in through the portal at this venue.",
  "grants.col.device": "Device",
  "grants.col.method": "Signed in with",
  "grants.col.granted": "Since",
  "grants.col.expires": "Until",
  "grants.col.via": "Through",
  "grants.method.voucher": "Voucher",
  "grants.method.account": "Account",
  "grants.method.click-through": "Terms only",
  "grants.method.phone": "Phone",
  "grants.noExpiry": "No limit",
  "grants.disconnect": "Disconnect",
  "grants.cannot": "End this guest's session from the list above, or on the device.",
  "grants.confirm.title": "Disconnect this guest?",
  "grants.confirm.consequence": (mac: string) =>
    `Foyer withdraws the portal's permission for ${mac} and asks the controller to unauthorize the device. The guest drops off the network.`,
  "grants.confirm.recovery": "The guest can sign in again at the portal unless the voucher is revoked too.",
  "grants.option.voucher": "Also revoke the voucher this guest used",
  "grants.done": "Guest disconnected. The controller confirmed.",
  "grants.failed": "The portal's permission is withdrawn, and the controller did not confirm the guest is off.",
  "grants.cause.refused": "The controller refused.",
  "grants.cause.no-answer": "The controller did not answer.",
  "grants.cause.bad-secret": "The controller rejected Foyer's credential.",
  "grants.cause.error": "Foyer could not complete the request to the controller.",
};

void loadLocale(() => import("./vi/portalgrants.js"));

export const t = makeT({ ...core, ...portalGrants });
