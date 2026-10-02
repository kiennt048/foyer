/**
 * Copy for `/attempts`, loaded with the screen.
 *
 * Nothing imports this but the modules behind that lazy boundary, so the bundler
 * puts these sentences in their chunk and an operator who never opens the screen
 * never downloads them. `core.ts` explains why that matters and what the rule is.
 */
import { loadLocale, makeT } from "./catalogue.js";
import { core } from "./core.js";

export const attempts = {
  "attempts.search": "Search MAC, username, access point…",
  "attempts.searchLabel": "Search sign-in attempts",

  "attempts.filter.rejected": "Rejected",
  "attempts.filter.accepted": "Accepted",
  "attempts.filter.source.radius": "From RADIUS",
  "attempts.filter.source.portal": "From the portal",
  "attempts.filter.source.driver": "From the access point",

  "attempts.col.time": "Attempt time",
  "attempts.col.outcome": "Outcome",
  "attempts.col.what": "What happened",
  "attempts.col.mac": "MAC address",
  "attempts.col.identity": "Identity",
  "attempts.col.nas": "Access point",

  "attempts.outcome.accepted": "Accepted",
  "attempts.outcome.rejected": "Rejected",

  "attempts.source.radius": "RADIUS",
  "attempts.source.portal": "Portal",
  "attempts.source.driver": "Access point",

  // One line per kind. The server sends the kind; this catalogue is the only place
  // the sentence exists, so a Vietnamese string can never reach the English console.
  "attempts.kind.radius-accept": "Accepted by RADIUS",
  "attempts.kind.radius-reject": "Rejected by RADIUS",
  "attempts.kind.voucher-unknown": "No such voucher",
  "attempts.kind.voucher-expired": "Voucher expired",
  "attempts.kind.voucher-exhausted": "Voucher already used up",
  "attempts.kind.voucher-revoked": "Voucher revoked",
  "attempts.kind.account-unknown": "No such account",
  "attempts.kind.account-disabled": "Account disabled",
  "attempts.kind.account-bad-password": "Wrong password",
  "attempts.kind.rate-limited": "Too many attempts",
  "attempts.kind.presence-unverified": "Device was not on the network",
  "attempts.kind.authorize-failed": "The access point refused the authorization",
  "attempts.kind.grant-created": "Signed in",

  // Why, not what. Each of these is a fact an operator would otherwise learn from a
  // support call, and three of them change what they should do next.
  "attempts.why.radius-reject":
    "RADIUS records that a request was refused and never records why — the reason is decided inside the authentication exchange and no column holds it.",
  "attempts.why.presence-unverified":
    "The portal was called with a MAC address that was not associated with any access point. This is what a forged portal link looks like, not a guest making a mistake.",
  "attempts.why.authorize-failed":
    "The guest passed every check Foyer makes. The access point or its controller refused the call that would have let them through, so nothing on the sessions screen will show this.",
  "attempts.why.rate-limited":
    "Nothing is wrong with the credential. The attempt was refused for coming too fast.",

  "attempts.noMac": "Not recorded",
  "attempts.noMacWhy":
    "This attempt reached Foyer without a MAC address, so a search by MAC will never find it.",
  "attempts.noNas": "Unmatched",
  "attempts.noNasWhy":
    "The request came from an address that matches no registered access point. On a controller deployment this is what a newly adopted access point looks like before it is registered.",

  "attempts.coverage.notRecorded.title": "MAC addresses are not being recorded.",
  "attempts.coverage.notRecorded.body":
    "Searching by MAC will find nothing here, whatever the guest did. The generated FreeRADIUS configuration is not populating the client address column.",
  "attempts.coverage.unknown.title": "Whether MAC addresses are recorded is not established.",
  "attempts.coverage.unknown.body":
    "An empty search result may mean the guest never tried, or may mean the column is never written.",

  "attempts.empty.title": "No attempts recorded.",
  "attempts.empty.description": "Attempts appear as guests try to sign in.",

  "attempts.blind.title": "What this screen cannot see",
  "attempts.blind.lede":
    "These produce no row here at all. If a guest is not online and there is no attempt for them, one of these is the likeliest reason.",
  "attempts.blind.shared-secret-mismatch":
    "The shared secret does not match. FreeRADIUS discards the request before anything is written, so a whole site can be offline with nothing on this screen.",
  "attempts.blind.unknown-radius-client":
    "The request came from an address in no client entry. Common right after an access point is added to a controller.",
  "attempts.blind.never-associated":
    "The guest never reached the access point — out of range, wrong network, or the radio is down.",
  /*
   * Three sentences for one kind, and the split is the point.
   *
   * A single sentence such as *"This deployment authorizes through a controller
   * API"* would assert an architecture, even for a site whose device list is
   * empty. `AuthBlindSpot.kind` has no member for *no devices*, so
   * the count arrives in `params` and decides which of these an operator reads.
   *
   * The `noSecret` one deliberately does not pick between the two readings of its
   * own fact: a device with no shared secret is either a controller-API deployment
   * that meant it, or the most common real fault in the product. Nothing on this
   * screen can tell those apart, and a sentence that chose one would be the same
   * defect one size smaller. The bare key is the fallback for a server that sends no
   * count — it states what is known and stops.
   */
  "attempts.blind.no-radius-configured":
    "No RADIUS client is configured for this site, so nothing writes to this stream.",
  "attempts.blind.no-radius-configured.noDevices":
    "No access point is registered in this site, so there is nothing to send an authentication request. Register one and attempts appear here as guests try to sign in.",
  "attempts.blind.no-radius-configured.noSecret":
    "Every access point registered in this site is without a RADIUS shared secret, so nothing reaches FreeRADIUS. On a deployment that authorizes through a controller API that is by design; on any other it is the reason nobody can get online.",
  "attempts.blind.before-retention": (days: number) =>
    `Older than ${days} days. Attempts are not kept indefinitely.`,
};

/** The shell's copy plus this screen's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/attempts.js"));

export const t = makeT({ ...core, ...attempts });
