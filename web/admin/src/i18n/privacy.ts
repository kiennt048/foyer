/**
 * Copy for `/privacy`, loaded with the screen.
 *
 * Nothing imports this but the modules behind that lazy boundary, so the bundler
 * puts these sentences in their chunk and an operator who never opens the screen
 * never downloads them. `core.ts` explains why that matters and what the rule is.
 */
import { loadLocale, makeT, plural } from "./catalogue.js";
import { core } from "./core.js";

export const privacy = {
  /* ── retention ──────────────────────────────────────────────────────────── */
  "privacy.retention.title": "How long it is kept",
  "privacy.retention.body":
    "Foyer removes each kind of guest data once it is older than its period, in a sweep that runs every few minutes. A period you shorten takes effect on the next sweep, and what it removes cannot be brought back.",
  "privacy.retention.days": "Days",
  "privacy.retention.keep": "0 keeps it until you erase or delete it.",
  "privacy.retention.bounds": (min: number, max: number) => `Between ${min} and ${max} days.`,
  "privacy.retention.default": (days: number) =>
    days === 0 ? "Default: kept until deleted." : `Default: ${plural(days, "day", "days")}.`,
  "privacy.retention.tables": "Tables",
  "privacy.retention.neverSwept": "Not swept yet.",
  "privacy.retention.swept": (when: string, removed: number) =>
    `Last swept ${when}; ${plural(removed, "row", "rows")} removed.`,
  "privacy.retention.sweepFailed": (when: string) =>
    `The last sweep, ${when}, failed part-way. The reason is in the control plane's log.`,
  "privacy.retention.restore": "Restore default",
  "privacy.retention.save": "Save periods",
  "privacy.retention.saved": "Retention periods saved.",
  "privacy.retention.failed": "The periods were not saved",
  "privacy.retention.unchanged": "Nothing has changed.",

  // One line per category, saying what the data is and what dates a row. The
  // contract's `RetentionCategoryId` describes the same five.
  "privacy.category.accounting": "Session history",
  "privacy.category.accounting.what": "Each guest session — device, access point, address, data used. Dated by when it ended; an open session is never removed.",
  "privacy.category.authAttempts": "Sign-in attempts",
  "privacy.category.authAttempts.what": "Every accepted and refused sign-in, from the network and from the portal.",
  "privacy.category.portalGrants": "Portal grants",
  "privacy.category.portalGrants.what": "What the portal let each device do, dated by when it ended.",
  "privacy.category.audit": "Audit log",
  "privacy.category.audit.what": "Operators' changes. Kept longer than guest data, and never removed one by one.",
  "privacy.category.guestAccounts": "Guest accounts",
  "privacy.category.guestAccounts.what": "Accounts you created, removed once unused for the period.",
  "privacy.category.marketingConsents": "Marketing consents",
  "privacy.category.marketingConsents.what":
    "Consents guests ticked on the portal, dated by when they were withdrawn, or given for one still active.",

  /* ── erasure ────────────────────────────────────────────────────────────── */
  "privacy.erase.title": "Erase one person's data",
  "privacy.erase.body":
    "When a guest asks to be forgotten, remove every record Foyer holds about them. Ask for the MAC address their phone shows for this network — phones use a different private address on each network.",
  "privacy.erase.kind": "Identify them by",
  "privacy.erase.kind.mac": "Device MAC address",
  "privacy.erase.kind.account": "Guest account",
  "privacy.erase.kind.phone": "Phone number",
  "privacy.erase.kind.email": "E-mail address",
  "privacy.erase.mac": "MAC address",
  "privacy.erase.macHint": "Any spelling: AA-BB-CC-00-11-22, aa:bb:cc:00:11:22, aabb.cc00.1122.",
  "privacy.erase.account": "Account id",
  "privacy.erase.accountHint": "From the Accounts screen.",
  "privacy.erase.phone": "Phone number",
  "privacy.erase.phoneHint":
    "Any spelling: 0912 345 678, +84 912 345 678. Erases the codes sent to it, the sign-ins it was verified for and every sign-in form that gave it, at every venue.",
  "privacy.erase.email": "E-mail address",
  "privacy.erase.emailHint":
    "The address they gave on your sign-in form or with a marketing consent. Upper or lower case does not matter.",
  "privacy.erase.submit": "Erase…",
  "privacy.erase.confirmTitle": "Erase this person's data?",
  "privacy.erase.confirmVerb": "Erase",
  "privacy.erase.consequence":
    "Every session, sign-in attempt, portal grant and portal session naming them is deleted, and for an account the account itself. This cannot be undone.",
  "privacy.erase.recovery":
    "The audit log records that you did it, with the address masked, and its earlier entries about this person keep the event with the address replaced by a pseudonym. If the record cannot be written, nothing is erased. Backups and your controller's own history are not reached.",
  "privacy.erase.failed": "Nothing was reported erased",
  "privacy.erase.done": (masked: string, total: number) =>
    `${masked}: ${plural(total, "record", "records")} erased.`,
  "privacy.erase.table": "Table",
  "privacy.erase.rows": "Removed",
  "privacy.erase.pseudonymised": (n: number) =>
    `${plural(n, "audit log entry", "audit log entries")} kept, with the identifier replaced by a pseudonym.`,
};

/** The shell's copy plus this screen's. A key from another screen does not compile. */
void loadLocale(() => import("./vi/privacy.js"));

export const t = makeT({ ...core, ...privacy });
