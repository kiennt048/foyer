/**
 * Copy for `/audit` and for `audit.ts`, which renders one entry's change.
 *
 * Nothing imports this but the modules behind that lazy boundary, so the bundler
 * puts these sentences in their chunk and an operator who never opens the screen
 * never downloads them. `core.ts` explains why that matters and what the rule is.
 */
import { loadLocale, makeT, plural } from "./catalogue.js";
import { core } from "./core.js";

export const audit = {
  /*
   * Kind plus params, never a sentence from the server. A log is read long after
   * the fact by somebody who was not there, and a confidently phrased sentence in
   * the wrong language is indistinguishable from a correct one.
   *
   * Each is a short phrase and **never contains the subject's name**:
   * the name is rendered from `subject.name`, because the thing it names has often
   * been deleted and there is nothing left to look up.
   */

  "audit.empty.title": "No changes recorded.",
  "audit.empty.description": "Configuration changes appear here as they are made.",

  "audit.col.when": "When",
  "audit.col.actor": "Operator",
  "audit.col.change": "Change",
  "audit.col.site": "Site",
  "audit.noSite": "Not a site's — operators are deployment-wide.",

  "audit.readOnly.title": "Append-only",
  "audit.readOnly.body":
    "Entries cannot be edited or removed one by one, and an operator's entries stay after their account is deleted. A wrong entry is corrected by a later one describing the correction. Entries older than the audit retention period, under Privacy, are removed by age.",

  "audit.filter.actor": "Operator",
  "audit.filter.anyActor": "Any operator",
  "audit.filter.from": "From",
  "audit.filter.to": "To",

  // The first entry in any deployment's log, because nothing else can be done before
  // it. "Site" and not "Venue": it is the noun the switcher and the column header
  // beside this row already use, and a log that renamed the thing halfway down is a
  // log an operator has to translate while reading.
  "audit.site-created": "Site created",
  "audit.nas-secret-rotated": "Shared secret rotated",
  "audit.nas-registered": "Access point registered",
  "audit.nas-deleted": "Access point deleted",
  "audit.controller-registered": "Controller registered",
  // Not "Controller edited". The word an operator scanning this column needs is the
  // one that says a stored credential may have moved — the change this kind was added
  // to record. Which field it was is the detail beside it.
  "audit.controller-updated": "Controller changed",
  "audit.controller-discovery-run": "Discovery run",
  "audit.controller-deleted": "Controller deleted",
  "audit.garden-entry-added": "Walled-garden entry added",
  "audit.garden-entry-removed": "Walled-garden entry removed",
  "audit.portal-settings-changed": "Portal settings changed",
  "audit.portal-terms-changed": "Portal terms changed",
  "audit.operator-created": "Operator created",
  "audit.operator-updated": "Operator changed",
  "audit.operator-deleted": "Operator deleted",
  "audit.operator-password-changed": "Password changed",
  "audit.voucher-batch-created": "Voucher batch created",
  "audit.voucher-revoked": "Voucher revoked",
  "audit.retention-policy-changed": "Retention periods changed",
  // The subject beside it is masked on purpose: the log keeps who and when, never the
  // address it records erasing.
  "audit.data-subject-erased": "Personal data erased",
  "audit.data-exported": "Data exported",
  // The withdrawal is named by the consent's id, never the guest's details.
  "audit.guest-data-settings-changed": "Guest data collection changed",
  "audit.marketing-consent-withdrawn": "Marketing consent withdrawn",
  "audit.site-time-zone-changed": "Site time zone changed",
  "audit.phone-spend-cap-changed": "Deployment SMS cap changed",
  "audit.detail.change": (from: string, to: string) => `${from} → ${to}`,
  "audit.detail.noCap": "no cap",
  "audit.maintenance-settings-changed": "Maintenance schedule changed",
  // Whether the password changed and whether 802.1X is now on. Never a value.
  "audit.account-credentials-changed": "Account credentials changed",
  "audit.detail.credentials": (password: boolean, dot1x: boolean) =>
    `${password ? "Password changed; " : ""}802.1X ${dot1x ? "on" : "off"}`,
  "audit.detail.guestData": (fields: string, consent: boolean) =>
    `${fields || "No fields"}; marketing consent ${consent ? "asked" : "not asked"}`,
  "audit.detail.exported": (dataset: string, from: string, to: string) => `${dataset}, ${from} to ${to}`,
  "audit.session-disconnected": "Guest disconnected",
  // The network screen. A certificate entry names the fingerprint and the
  // expiry; no entry ever carries a key.
  "audit.network-settings-changed": "Network settings changed",
  "audit.portal-certificate-changed": "Portal certificate changed",
  "audit.radius-clients-synced": "RADIUS clients synced",
  "audit.radius-configuration-applied": "FreeRADIUS configuration applied",
  "audit.detail.certificateRemoved": "Removed",
  "audit.detail.eapCertificate": "EAP-TLS certificate uploaded",
  "audit.detail.certificate": (fingerprint: string, until: string) =>
    `SHA-256 ${String(fingerprint).slice(0, 16)}…, valid until ${until}`,
  "audit.detail.sync": (trigger: string, outcome: string, removed: number) =>
    `${trigger}: ${outcome}${removed > 0 ? `, ${plural(removed, "client", "clients")} removed` : ""}`,

  "audit.detail.vouchers": (n: number) => plural(n, "voucher", "vouchers"),
  "audit.detail.disconnect": (confirmed: boolean, voucher: boolean, grants: number) =>
    [
      confirmed ? "device confirmed" : "device did not confirm",
      voucher ? "voucher revoked" : "",
      grants > 0 ? `${plural(grants, "portal grant", "portal grants")} revoked` : "",
    ]
      .filter(Boolean)
      .join(", "),
  /* Three counts in one phrase, because they are read together: an operator checking
     a discovery run wants *what came in, what was already there, and what stopped
     answering*, and the third is the one nothing else on any screen records. `missing`
     is spelled out rather than left as a bare number for exactly that reason — a
     device that stopped being reported is marked, never deleted, so this line is the
     only place the fact appears. */
  "audit.detail.discovery": (adopted: number, updated: number, missing: number) =>
    missing > 0
      ? `${adopted} adopted, ${updated} re-read, ${plural(missing, "device", "devices")} no longer reported`
      : `${adopted} adopted, ${updated} re-read`,
  "audit.detail.erased": (n: number) => `${plural(n, "record", "records")} removed`,
  "audit.detail.orphanedNas": (n: number) =>
    `${plural(n, "access point", "access points")} left without a controller`,

  // A kind this console predates. Same shape as `alarm.unknown` and `problem.unknown`,
  // and for the same reason: never blank, never a raw enum value.
  "audit.unknown.what": "A change this console does not recognise",
  "audit.unknown.detail":
    "A newer version of Foyer recorded it. The operator, the time and the subject beside it are still exact.",
};

/** The shell's copy plus this screen's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/audit.js"));

export const t = makeT({ ...core, ...audit });
