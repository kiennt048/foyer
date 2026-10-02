import type { AuditEntry, AuditKind } from "@foyer/contract";
import { t } from "./i18n/audit.js";

/**
 * Audit copy, resolved from the kind the server sent.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * The third catalogue in this console, after `alarms.ts` and `problems.ts`, and the
 * one with the strongest case: **a log is read long after the fact, by somebody who
 * was not there, and a confidently phrased sentence in the wrong language is
 * indistinguishable from a correct one.** The client owns this copy because a server
 * composes text in its own language, not the reader's; on a dashboard that is noticed
 * at once, and in a log it would not be noticed for months.
 *
 * So the server sends `kind`, `params` and `subject`, and every sentence is here.
 * `Record<AuditKind, …>` over the contract's closed enum, so a kind added there does
 * not build until it has copy — a row an operator cannot read is worse in a log than
 * anywhere else, because there is nothing else on the page to infer it from.
 *
 * **Deliberately not imported by anything eager.** `screens/Audit.tsx` is the only
 * consumer and is lazily loaded, so this code travels in that chunk — and so do
 * its *strings*, which is why `t` here comes from `i18n/audit.js`
 * rather than `i18n/index.js`. The catalogue is nine namespaces, and the
 * compile-time guarantee is narrowed to each, which `i18n/core.ts` explains.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **`title` and `detail` are the fallback and they are deliberately last**, exactly
 * as in `alarms.ts`. They survive in the contract for one case — a kind a newer
 * server raises and this console predates — and `web/mock` is asserted to send
 * neither, because a server that always sends prose is a server whose clients never
 * implement the kind.
 */

type Params = Record<string, unknown>;

const text = (v: unknown): string => (typeof v === "string" ? v : "");
const count = (v: unknown): number => (typeof v === "number" && Number.isFinite(v) ? v : 0);
/** `["heroImage","venueName"]` → `heroImage, venueName`. Values, not a sentence. */
const list = (v: unknown): string => (Array.isArray(v) ? v.map(String).join(", ") : "");
/** A range bound as its UTC date: the export's filename names days, so the log does. */
const time = (v: unknown): string => (typeof v === "string" ? v.slice(0, 10) : "");
/**
 * The field names out of a `{"changed": {field: {"from":…,"to":…}}}` entry.
 *
 * One expression for every kind that has it, so the four readers cannot drift
 * apart into four shapes — the values themselves stay in `params` and are not
 * rendered here, because an entry is a list of what moved, not a transcript of
 * the page.
 */
const changedKeys = (p: Params): string[] =>
  p.changed && typeof p.changed === "object" && !Array.isArray(p.changed) ? Object.keys(p.changed) : [];

export interface AuditCopy {
  /** The change, as a short phrase. Never contains the subject's name. */
  what: string;
  /** What else the entry knows, from `params`. Empty where there is nothing to add. */
  detail: string;
  /** True when the catalogue had nothing and the server's own text is showing. */
  fromServer: boolean;
}

/**
 * The change itself, keyed on the kind.
 *
 * The subject's name is **not** in these sentences: it is rendered from
 * `subject.name`, in its own line, for every kind. That is not a layout preference —
 * the subject often no longer exists (`nas-tang-2` was registered and deleted, and
 * both of its entries are still in the log), so the name has to come from the entry
 * rather than from a lookup, and a sentence that interpolated it would make every
 * kind in this catalogue responsible for remembering that.
 */
const WHAT: Record<AuditKind, string> = {
  get "site-created"() {
    return t("audit.site-created");
  },
  get "nas-secret-rotated"() {
    return t("audit.nas-secret-rotated");
  },
  get "nas-registered"() {
    return t("audit.nas-registered");
  },
  get "nas-deleted"() {
    return t("audit.nas-deleted");
  },
  get "controller-registered"() {
    return t("audit.controller-registered");
  },
  get "controller-updated"() {
    return t("audit.controller-updated");
  },
  get "controller-discovery-run"() {
    return t("audit.controller-discovery-run");
  },
  get "controller-deleted"() {
    return t("audit.controller-deleted");
  },
  get "garden-entry-added"() {
    return t("audit.garden-entry-added");
  },
  get "garden-entry-removed"() {
    return t("audit.garden-entry-removed");
  },
  get "portal-settings-changed"() {
    return t("audit.portal-settings-changed");
  },
  get "portal-terms-changed"() {
    return t("audit.portal-terms-changed");
  },
  get "operator-created"() {
    return t("audit.operator-created");
  },
  get "operator-updated"() {
    return t("audit.operator-updated");
  },
  get "operator-deleted"() {
    return t("audit.operator-deleted");
  },
  get "operator-password-changed"() {
    return t("audit.operator-password-changed");
  },
  get "voucher-batch-created"() {
    return t("audit.voucher-batch-created");
  },
  get "voucher-revoked"() {
    return t("audit.voucher-revoked");
  },
  get "retention-policy-changed"() {
    return t("audit.retention-policy-changed");
  },
  get "data-subject-erased"() {
    return t("audit.data-subject-erased");
  },
  get "data-exported"() {
    return t("audit.data-exported");
  },
  get "session-disconnected"() {
    return t("audit.session-disconnected");
  },
  get "guest-data-settings-changed"() {
    return t("audit.guest-data-settings-changed");
  },
  get "marketing-consent-withdrawn"() {
    return t("audit.marketing-consent-withdrawn");
  },
  get "network-settings-changed"() {
    return t("audit.network-settings-changed");
  },
  get "portal-certificate-changed"() {
    return t("audit.portal-certificate-changed");
  },
  get "radius-clients-synced"() {
    return t("audit.radius-clients-synced");
  },
  get "radius-configuration-applied"() {
    return t("audit.radius-configuration-applied");
  },
  get "site-time-zone-changed"() {
    return t("audit.site-time-zone-changed");
  },
  get "phone-spend-cap-changed"() {
    return t("audit.phone-spend-cap-changed");
  },
  get "maintenance-settings-changed"() {
    return t("audit.maintenance-settings-changed");
  },
  get "account-credentials-changed"() {
    return t("audit.account-credentials-changed");
  },
};

/**
 * What `params` adds, keyed on the kind — and **most kinds add nothing**.
 *
 * The empty ones are empty because the change plus its subject is the whole fact:
 * *deleted* an access point named in the row beside it needs no elaboration. Where a
 * value does exist it is the one an operator reads the log *for*: which class of
 * change the secret rotation was, how many vouchers the batch held, which fields of
 * the portal settings moved.
 */
const DETAIL: Record<AuditKind, (p: Params) => string> = {
  /* Nothing to add, and deliberately: the venue's name is rendered from
     `subject.name` like every other subject's, and `SiteCreate` accepts no other
     field, so a detail here could only repeat the name beside it or state an
     `environment` this operation cannot vary. */
  "site-created": () => "",
  /* `restart` versus `sql-only` is the difference between a rotation that dropped
     every in-flight EAP conversation on site and one that did not. It is the single
     most useful thing this log records. */
  "nas-secret-rotated": (p) => text(p.changeClass),
  "nas-registered": (p) => text(p.driver),
  "nas-deleted": () => "",
  "controller-registered": (p) => text(p.vendor),
  /* Which members moved, the same shape as `operator-updated` and for the same
     reason: an entry recording that *something* changed is the shape of a log nobody
     trusts. **Values never travel** — `apiKey` and `password` are write-only in the
     contract — so the field name is the whole of what there is to say. */
  "controller-updated": (p) => list(p.changed),
  "controller-discovery-run": (p) =>
    t("audit.detail.discovery", count(p.adopted), count(p.updated), count(p.missing)),
  "controller-deleted": (p) =>
    count(p.orphanedNas) > 0 ? t("audit.detail.orphanedNas", count(p.orphanedNas)) : "",
  "garden-entry-added": (p) => text(p.kind),
  "garden-entry-removed": (p) => text(p.kind),
  /* Which members moved, and the same expression `network-settings-changed` uses.
     These two kinds are the ones the backend writes `changed` on; without it there
     are no `fields` or `languages` either, and this renders an empty detail line. */
  "portal-settings-changed": (p) => list(changedKeys(p)),
  "portal-terms-changed": (p) => list(changedKeys(p)),
  "operator-created": (p) => text(p.role),
  /* Which members moved. An `operator-updated` with no fields named is an entry that
     records that *something* changed, which is the shape of a log nobody trusts. */
  "operator-updated": (p) => list(p.changed),
  "operator-deleted": () => "",
  "operator-password-changed": () => "",
  "voucher-batch-created": (p) => t("audit.detail.vouchers", count(p.count)),
  "voucher-revoked": () => "",
  /* Which categories moved. The old and new periods are in `params` for anybody
     reading the raw entry; the column names what changed, like `operator-updated`. */
  "retention-policy-changed": (p) =>
    list(changedKeys(p)),
  "data-subject-erased": (p) => t("audit.detail.erased", count(p.total)),
  /* Which table and which days — the two facts an administrator reads an export
     entry for. The rows themselves never travel in an entry. */
  "data-exported": (p) => t("audit.detail.exported", text(p.dataset), time(p.from), time(p.to)),
  /* What went with the session, and whether the device confirmed. The second is the
     one read later: an entry for a disconnect the device refused is the record that
     the guest was still online after somebody tried. */
  "session-disconnected": (p) =>
    t("audit.detail.disconnect", p.disconnected === true, p.voucherRevoked === true, count(p.grantsRevoked)),
  "guest-data-settings-changed": (p) =>
    t("audit.detail.guestData", list(Array.isArray(p.fields) ? p.fields : []), p.marketingConsent === true),
  "marketing-consent-withdrawn": () => "",
  /* Which values moved — a throttle raised from 30 to 300 is what an administrator
     reads this entry for. A CoA timing change names the device as its subject. */
  "network-settings-changed": (p) =>
    p.changed && typeof p.changed === "object"
      ? list(changedKeys(p))
      : p.eapCertificate
        ? t("audit.detail.eapCertificate")
        : typeof p.timeoutMs === "number"
          ? `${p.timeoutMs} ms × ${count(p.retries) + 1}`
          : "",
  "portal-certificate-changed": (p) =>
    p.removed === true ? t("audit.detail.certificateRemoved") : t("audit.detail.certificate", text(p.fingerprintSha256), time(p.notAfter)),
  "radius-clients-synced": (p) => t("audit.detail.sync", text(p.trigger), text(p.outcome), count(p.removed)),
  "radius-configuration-applied": (p) => text(p.outcome),
  /* From and to — the one fact about a calendar change an operator reads it for,
     because every daily figure before it counted the other day. */
  "site-time-zone-changed": (p) => t("audit.detail.change", text(p.from), text(p.to)),
  "phone-spend-cap-changed": (p) =>
    t(
      "audit.detail.change",
      p.from == null ? t("audit.detail.noCap") : String(p.from),
      p.to == null ? t("audit.detail.noCap") : String(p.to),
    ),
  /* Which values moved, like `retention-policy-changed`; the figures are in `params`. */
  "maintenance-settings-changed": (p) =>
    list(changedKeys(p)),
  "account-credentials-changed": (p) =>
    t("audit.detail.credentials", p.passwordChanged === true, p.eapTtlsPap === true),
};

export function auditCopy(entry: AuditEntry): AuditCopy {
  const what = WHAT[entry.kind as AuditKind];
  if (what) {
    return { what, detail: DETAIL[entry.kind as AuditKind](entry.params ?? {}), fromServer: false };
  }
  /* A kind newer than this console. The server's sentence is better than nothing and
     worse than the catalogue, which is exactly the order it is used in. */
  if (entry.title) return { what: entry.title, detail: entry.detail ?? "", fromServer: true };
  return { what: t("audit.unknown.what"), detail: t("audit.unknown.detail"), fromServer: true };
}
