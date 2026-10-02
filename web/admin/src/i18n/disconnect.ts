/**
 * Copy for the session dock's disconnect control, loaded with it.
 *
 * The control is `lazy()` out of the session dock, so these sentences travel in its
 * chunk and the entry pays only for the import edge. `core.ts` explains the rule.
 */
import { loadLocale, makeT, plural } from "./catalogue.js";
import { core } from "./core.js";

export const disconnect = {
  "disconnect.button": "Disconnect guest",
  "disconnect.confirm.title": "Disconnect this guest?",
  "disconnect.confirm.verb": "Disconnect",
  // Names the consequence and the recovery, the `confirm` tier's rule: a disconnect
  // on its own is recoverable — the guest can sign in again.
  "disconnect.confirm.consequence": (mac: string) =>
    `Foyer asks the device to end the session for ${mac}. The guest drops off the network.`,
  "disconnect.confirm.recovery":
    "On its own this is recoverable: the guest can sign in again with the same voucher or account. Tick below to stop that.",
  "disconnect.option.voucher": "Also revoke the voucher this session used",
  "disconnect.option.grant": "Also revoke the portal grant for this device",

  // A device whose driver cannot end a session. Said beside the missing control,
  // never left as an absence the operator has to interpret.
  "disconnect.cannot.title": "Foyer cannot disconnect guests on this device",
  "disconnect.cannot.ruckus":
    "Ruckus SmartZone ends captive sessions through its Northbound API, which Foyer does not drive; RADIUS Disconnect fails there. Disconnect the guest in SmartZone.",
  "disconnect.cannot.omada":
    "Foyer has no Omada driver yet. Disconnect the guest in the Omada controller.",
  "disconnect.cannot.unprobed":
    "This device has not reported that it can end a session. Probe it from Access points; until then, disconnect the guest on the device itself.",

  "disconnect.done.title": "Guest disconnected",
  "disconnect.done.body":
    "The device confirmed. The row reads ended once its accounting stop arrives.",
  "disconnect.failed.title": "The guest is still online",
  "disconnect.cause.refused": (errorCause: number | null) =>
    errorCause === 503
      ? "The device refused: it has no such session (Error-Cause 503)."
      : errorCause
        ? `The device refused (Error-Cause ${errorCause}).`
        : "The device refused, and gave no reason.",
  "disconnect.cause.no-answer":
    "The device did not answer. It may be unreachable, a firewall may drop the request, or its CoA port may be wrong.",
  "disconnect.cause.bad-secret":
    "Something answered, but the shared secret did not match the one Foyer holds.",
  "disconnect.cause.error": "Foyer could not complete the request to the device.",
  // What went with it, stated whether or not the device confirmed — a revocation that
  // happened is a fact on its own.
  "disconnect.withdrawn": (voucher: boolean, grants: number) =>
    [
      voucher ? "The voucher is revoked." : "",
      grants > 0 ? `${plural(grants, "portal grant", "portal grants")} revoked.` : "",
    ]
      .filter(Boolean)
      .join(" "),
};

/** The shell's copy plus this control's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/disconnect.js"));

export const t = makeT({ ...core, ...disconnect });
