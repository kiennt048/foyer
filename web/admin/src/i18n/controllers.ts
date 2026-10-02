/**
 * Copy for `/controllers`, loaded with the screen.
 *
 * Nothing imports this but the modules behind that lazy boundary, so the bundler
 * puts these sentences in their chunk and an operator who never opens the screen
 * never downloads them. `core.ts` explains why that matters and what the rule is.
 */
import { loadLocale, makeT, plural } from "./catalogue.js";
import { core } from "./core.js";

export const controllers = {
  "controllers.add": "Add controller",
  "controllers.empty.title": "No controllers.",
  "controllers.empty.description": "Add the controller your access points are adopted by.",

  "controllers.col.name": "Name",
  "controllers.col.address": "Address",
  "controllers.col.status": "Status",
  "controllers.col.version": "Version",
  "controllers.col.sites": "Sites",
  "controllers.col.accessPoints": "Access points",
  "controllers.col.lastContact": "Last contact time",

  "controllers.vendor.unifi": "UniFi",
  "controllers.vendor.omada": "Omada",
  "controllers.vendor.ruckus": "Ruckus SmartZone",

  "controllers.status.ok": "Reachable",
  "controllers.status.unreachable": "Unreachable",
  "controllers.status.credentials-expired": "Credential rejected",
  /* The version verdict is a *status*, not a footnote in the Version column — see
     `StatusPill`. The target version is not named: only
     `ControllerProbe` carries `minimumVersion`, and the probe dialog states it. */
  "controllers.status.versionUnsupported": "Version not supported",
  "controllers.status.unknown": "Not probed",
  "controllers.status.unknownWhy":
    "Nothing has reached this controller yet. Probe it — that is what establishes the version, the sites and whether the credential works.",

  "controllers.version.unknown": "Not established",
  "controllers.version.tooOld": (minimum: string) => `Needs ${minimum} or newer`,
  "controllers.sites.none": "None read yet",
  "controllers.sites.bound": (bound: number, total: number) => `${bound} of ${total} adopted`,
  /* Not a count. A controller whose sites have been read and adopted by nobody is
     one step short of working, and it read `0 of 1 adopted` beside a green
     *Reachable* — a figure where a state belongs. See `controllerAttachment`. */
  "controllers.sites.unattached": "Not attached",
  "controllers.sites.unattachedWhy":
    "This controller has been read and none of its sites is adopted into this venue yet. Attach it — discovering its access points is what adopts one.",

  "controllers.create.title": "Add controller",
  "controllers.create.vendor": "Vendor",
  "controllers.create.name": "Name",
  "controllers.create.nameHint": "What staff will call it, for example “Cloud Key — main office”.",
  "controllers.create.baseUrl": "Address",
  "controllers.create.baseUrlHint":
    "Scheme, host and port, as you would type it in a browser. Whether it is a UniFi OS console or a standalone controller is probed, not asked.",
  "controllers.create.controllerId": "Controller ID",
  "controllers.create.controllerIdHint":
    "Omada only. The 32 characters between the port and /login in your controller's own address bar. It cannot be guessed, which is why it is asked for.",
  "controllers.create.authMode": "Credential",
  "controllers.create.authMode.api-key": "API key",
  "controllers.create.authMode.local-admin": "Local administrator",
  "controllers.create.authMode.hotspot-operator": "Hotspot operator",
  "controllers.create.apiKey": "API key",
  "controllers.create.apiKeyHint":
    "UniFi Network → Control Plane → Integrations. Needs Network Application 9.1.105 or newer; older consoles have no such screen and you want a local administrator instead.",
  "controllers.create.username": "Username",
  "controllers.create.password": "Password",
  // The single most common first-attempt failure on Omada, and it is invisible:
  // the controller answers, and refuses.
  "controllers.create.hotspotOperatorHint":
    "Not your controller login. This is an operator account created in the controller's Hotspot Manager — TP-Link is explicit that the two are different, and using the wrong one fails without saying why.",
  "controllers.create.localAdminHint":
    "A local administrator on the controller, not a cloud account. Two-factor on the account will stop this working.",
  "controllers.create.tlsMode": "Certificate",
  "controllers.create.tls.verify": "Verify against a public authority",
  "controllers.create.tls.trust-on-first-use": "Trust this certificate and pin it",
  "controllers.create.tlsHint":
    "Controllers usually ship a self-signed certificate. Pinning accepts the one it presents now and refuses a change later — Foyer will not offer to ignore certificates entirely, which is what most integrations do.",
  "controllers.create.submit": "Add controller",
  "controllers.create.failed.title": "Controller not added",
  "controllers.create.credentialWhy":
    "Stored write-only. No screen and no API call ever returns it — it is a password to your network.",

  "controllers.probe": "Probe",
  "controllers.probing": "Probing…",
  "controllers.probe.failed.title": "Probe failed",
  "controllers.probe.check.reachable": "Reached the controller",
  "controllers.probe.check.tls": "Certificate",
  "controllers.probe.check.credential": "Credential accepted",
  "controllers.probe.check.version": "Software version",
  "controllers.probe.check.sites": "Read its site list",
  "controllers.probe.check.guest-authorization": "Can authorize a guest",
  "controllers.probe.check.presence-check": "Can confirm a device is really present",
  // The check that decides whether a driver may ship at all, said plainly.
  "controllers.probe.presenceWhy":
    "Foyer will not create a grant for a device it cannot confirm is associated — the portal is called with a MAC address anyone can supply. A controller that cannot answer that question cannot be used to let guests on.",
  "controllers.probe.flavour": (what: string) => `Detected as ${what}`,

  /* The same dialog as *Probe*, named for what it is about to do. A controller that
     answered and is adopted by nobody has one step left, and *Probe* is not it. */
  "controllers.attach": "Attach to this venue",
  "controllers.attachWhy":
    "Read its access points and adopt one of its sites into this venue.",

  "controllers.discover": "Discover access points",
  "controllers.discovering": "Reading the access-point list…",
  "controllers.discover.failed.title": "Discovery failed",
  "controllers.discover.result": (adopted: number, updated: number) =>
    `${plural(adopted, "access point", "access points")} adopted, ${updated} already known`,
  "controllers.discover.why":
    "Access points are read from the controller because the redirect a guest arrives with identifies them by MAC address and nothing else. Typing twelve by hand also means typing the thirteenth.",
  "controllers.discover.secret":
    "Every access point here presents one shared secret, because that is where the secret lives — in a RADIUS profile on the controller, not on each device.",
  /* Said before the button is pressed, because binding is a side effect of reading
     access points and nothing in this dialog had ever named it. */
  "controllers.discover.attaches": (site: string) =>
    `This controller is not attached to a venue yet. Discovering attaches it to ${site}, and adopts its access points into it.`,
  /* The same sentence while the venue's own name is still loading. It says the same
     thing and names nothing it does not know. */
  "controllers.discover.attachesHere":
    "This controller is not attached to a venue yet. Discovering attaches it to the venue you are looking at, and adopts its access points into it.",
  "controllers.discover.vendorSite": "Which of its sites",

  "controllers.delete.title": "Forget this controller",
  "controllers.delete.verb": "Forget controller",
  "controllers.delete.consequence": (name: string) =>
    `Foyer stops talking to ${name}. Guests already online stay online.`,
  "controllers.delete.recovery":
    "It can be added again, and the credential will have to be entered again. Access points adopted from it must be removed first.",
};

/** The shell's copy plus this screen's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/controllers.js"));

export const t = makeT({ ...core, ...controllers });
