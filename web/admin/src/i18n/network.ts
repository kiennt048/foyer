/**
 * Copy for `/network`, loaded with the screen.
 *
 * Nothing imports this but the modules behind that lazy boundary, so the bundler
 * puts these sentences in their chunk and an operator who never opens the screen
 * never downloads them. `core.ts` explains why that matters and what the rule is.
 *
 * English, the source of truth. The Vietnamese is `vi/network.ts`, typed as a total
 * translation of these keys and fetched only when the console speaks Vietnamese
 * (`lang.ts`); keeping it in this file would put both languages in the screen's
 * chunk for every reader.
 */
import { loadLocale, makeT, plural } from "./catalogue.js";
import { core } from "./core.js";

export const network = {
  "network.readOnly": "You can see these settings. Changing them needs an administrator.",
  "network.source.console": "Set here",
  "network.source.environment": "From the installation",
  "network.source.derived": "Worked out from this server's address",
  "network.source.unset": "Not set",
  "network.saved": "Saved. In force from the next request; nothing restarted.",
  "network.failed": "Not saved",

  /* ── the portal's address ─────────────────────────────────────────────────── */
  "network.url.title": "Portal address",
  "network.url.body":
    "The address a guest's phone is sent to sign in. The operating system is handed it as it is (RFC 8908), so it must be one guests can reach — and, with a certificate, a name the certificate covers.",
  "network.url.portal": "Portal address",
  "network.url.portalHint": "For example https://wifi.example.vn/en/. Leave empty to use the installation's own.",
  "network.url.venue": "Venue information page",
  "network.url.venueHint": "Optional. A page about the venue that phones may offer beside the sign-in page.",

  /* ── sign-in limits and request sizes ─────────────────────────────────────── */
  "network.limits.title": "Sign-in limits",
  "network.limits.body":
    "How many sign-in attempts are allowed back to back, and how quickly the allowance refills. Every attempt costs the server a slow password check, so these protect it as well as your accounts.",
  "network.limits.consoleAddress": "Console, per address",
  "network.limits.consoleAccount": "Console, per account",
  "network.limits.portalAddress": "Guest portal, per address",
  "network.limits.portalAccount": "Guest portal, per account at each address",
  "network.limits.natHint":
    "If your guest network puts every guest behind one address (NAT), raise this: otherwise one busy hour looks like one guest guessing.",
  "network.limits.burst": "Attempts in a row",
  "network.limits.interval": "Then one more every",
  "network.ceilings.title": "Request sizes",
  "network.ceilings.body": "The most the server reads of one request. Larger values admit larger uploads and cost more memory per request.",
  "network.ceilings.portalBytes": "Guest portal request",
  "network.ceilings.consoleBytes": "Console request, signed in",
  "network.ceilings.anonymousBytes": "Console request, not signed in",
  "network.ceilings.headerBytes": "Request headers",
  "network.settings.save": "Save limits and sizes",
  "network.url.save": "Save addresses",
  "network.advanced.lede":
    "Sign-in limits, request sizes, FreeRADIUS and disconnect timing. Each ships with a safe default that suits almost every venue; change one when support or a measurement says to.",

  /* ── the portal certificate ───────────────────────────────────────────────── */
  "network.cert.title": "Portal certificate",
  "network.cert.body":
    "With a certificate the portal is served over HTTPS and the plain-HTTP address sends guests there. Without one it is plain HTTP, which is the default. The private key is sealed when it is saved and is never shown again.",
  "network.cert.none": "No certificate. The portal is served over plain HTTP.",
  "network.cert.served": "Served to guests.",
  "network.cert.notServed": "Stored, and not served:",
  "network.cert.reason.portal-url-not-https": "the portal address is not https://.",
  "network.cert.reason.name-not-covered": "it does not name the portal address.",
  "network.cert.reason.expired": "it has expired.",
  "network.cert.reason.not-yet-valid": "it is not valid yet; it is served from the moment it is.",
  "network.cert.reason.listener-failed": "the HTTPS listener could not start; the reason is in the server's log.",
  "network.cert.fromEnvironment": "This certificate came with the installation. One uploaded here replaces it.",
  "network.cert.names": "Names",
  "network.cert.until": "Valid until",
  "network.cert.fingerprint": "SHA-256",
  "network.cert.key": "Private key",
  "network.cert.keySet": "Set, and never shown",
  "network.cert.upload": "Upload a certificate",
  "network.cert.certificatePem": "Certificate (PEM)",
  "network.cert.certificateHint": "The certificate first, then any intermediate certificates.",
  "network.cert.privateKeyPem": "Private key (PEM)",
  "network.cert.privateKeyHint": "Not encrypted with a passphrase. Sent once, sealed, and never shown again.",
  "network.cert.portalUrl": "Portal address to serve it at",
  "network.cert.portalUrlHint": "Saved with the certificate, because the two have to agree. Leave empty to keep the current one.",
  "network.cert.fromFile": "Read from a file",
  "network.cert.save": "Upload certificate",
  "network.cert.saved": "Certificate saved and in force.",
  "network.cert.remove": "Remove certificate",
  "network.cert.removeTitle": "Remove the portal certificate?",
  "network.cert.removeConsequence":
    "The portal goes back to plain HTTP from the next request, unless the installation supplies a certificate of its own.",
  "network.cert.refused.not-pem": "This is not a PEM certificate or key.",
  "network.cert.refused.key-mismatch": "This key does not belong to this certificate.",
  "network.cert.refused.expired": "This certificate has expired.",
  "network.cert.refused.not-yet-valid": "This certificate is not valid yet.",
  "network.cert.refused.chain-broken": "The certificates are not in order: each must be signed by the next.",
  "network.cert.refused.name-not-covered": "The certificate does not name this address.",
  "network.cert.refused.portal-url-not-https": "With a certificate, the portal address must start https://.",

  /* ── FreeRADIUS ───────────────────────────────────────────────────────────── */
  "network.radius.title": "FreeRADIUS",
  "network.radius.body":
    "Saved here and given to FreeRADIUS when you apply. FreeRADIUS fixes these when it starts, so applying restarts it.",
  "network.radius.maxServers": "Worker threads",
  "network.radius.maxServersHint":
    "How many sign-ins FreeRADIUS answers at once. Each worker can hold a database connection, so raising this raises what the database must admit.",
  "network.radius.eap": "Offer EAP-TLS (certificate sign-in for 802.1X)",
  "network.radius.eapHint": "Needs the server certificate and the CA that issued your devices' certificates.",
  "network.radius.eapCert": "EAP-TLS certificate",
  "network.radius.eapNone": "None uploaded.",
  "network.radius.caPem": "Client CA (PEM)",
  "network.radius.eapSave": "Upload EAP-TLS certificate",
  "network.radius.save": "Save FreeRADIUS settings",
  "network.radius.pending": "Saved and not yet applied: FreeRADIUS is still running the previous settings.",
  "network.radius.apply": "Apply and restart FreeRADIUS",
  "network.radius.applyTitle": "Restart FreeRADIUS?",
  "network.radius.applyConsequence":
    "Every 802.1X device on site signs in again. Guests on the portal notice only the seconds it is down.",
  "network.radius.applyUnavailable":
    "FreeRADIUS is not taking configuration from here yet: either this installation writes none, or FreeRADIUS's container is not running. Settings are kept, and applied when it starts.",
  "network.radius.applied.applied": "Applied, and FreeRADIUS restarted.",
  "network.radius.applied.unchanged": "Applied: FreeRADIUS was already running this configuration.",
  "network.radius.applied.restart-unavailable": "Written, and nothing has taken it up yet: FreeRADIUS's container is not running.",
  "network.radius.applied.not-configured": "Nothing applied: this installation does not write FreeRADIUS's configuration from here.",
  "network.radius.applied.failed": "The apply failed.",
  "network.radius.lastApply": (when: string) => `Last applied ${when}.`,

  /* ── the client table ─────────────────────────────────────────────────────── */
  "network.sync.title": "RADIUS clients",
  "network.sync.body":
    "Every change to an access point or controller is written to the client list FreeRADIUS reads straight away. Nothing needs to be run by hand.",
  "network.sync.never": "Not synced since this server started keeping a record.",
  "network.sync.last": (when: string) => `Last synced ${when}.`,
  "network.sync.outcome.synced": "Synced",
  "network.sync.outcome.refused": "Refused, nothing written",
  "network.sync.outcome.failed": "Failed, nothing written",
  "network.sync.counts": (added: number, changed: number, removed: number) =>
    `${added} added, ${changed} changed, ${plural(removed, "client", "clients")} removed.`,
  "network.sync.skipped": (n: number) => `${plural(n, "client", "clients")} could not be listed:`,
  "network.sync.restart": "FreeRADIUS needs a restart to see this change.",
  "network.sync.now": "Sync now",

  /* ── CoA timing ───────────────────────────────────────────────────────────── */
  "network.coa.title": "Disconnect timing",
  "network.coa.body":
    "How long Foyer waits for a router to answer a disconnect or change request, and how often it asks again. Raise it for a router that is slow or far away.",
  "network.coa.empty.title": "No routers to time.",
  "network.coa.empty.description": "Only devices Foyer disconnects directly are listed here.",
  "network.coa.device": "Device",
  "network.coa.timeout": "Wait, in seconds",
  "network.coa.retries": "Retries",
  "network.coa.default": "Default",
  "network.coa.save": "Save",
  "network.coa.reset": "Use defaults",
};

void loadLocale(() => import("./vi/network.js"));

/** The shell's copy plus this screen's, in the console's language. */
export const t = makeT({ ...core, ...network });
