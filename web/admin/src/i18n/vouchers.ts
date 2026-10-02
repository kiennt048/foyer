/**
 * Copy for `/vouchers`, its batch pages and the print sheet's operator half, loaded
 * with that screen.
 *
 * `/vouchers` is a lazy route for the reason `sessions.ts` gives.
 * The guest-facing slip copy, in both languages, is `print.ts` and still travels in
 * the print kit's own chunk; this is only what the operator reads around the sheet.
 *
 * English, the source of truth; the Vietnamese is `vi/vouchers.ts`. Moved, not written.
 */
import { loadLocale, makeT, plural } from "./catalogue.js";
import { core } from "./core.js";

export const vouchers = {
  "bulk.selectVoucher": (code: string) => `Select voucher ${code}`,
  "bulk.vouchers.revoke": (n: number) => (n === 1 ? "Revoke 1 code" : `Revoke ${n} codes`),
  "bulk.vouchers.confirm.title": "Revoke the selected codes?",
  "bulk.vouchers.confirm.verb": "Revoke them",
  "bulk.vouchers.confirm.consequence": (n: number) =>
    n === 1
      ? "1 code stops working and any session it is carrying ends."
      : `${n} codes stop working and any sessions they are carrying end.`,
  "bulk.vouchers.confirm.recovery":
    "There is no un-revoke. Printed slips for these codes become waste paper.",
  "bulk.vouchers.done": (n: number) => (n === 1 ? "1 code revoked" : `${n} codes revoked`),
  "bulk.noted.title": (n: number) =>
    n === 1 ? "1 code was revoked and its session was not ended" : `${n} codes were revoked and their sessions were not ended`,
  "vouchers.create": "Create batch",
  "vouchers.empty.title": "No voucher batches.",
  "vouchers.empty.description": "Create the first batch to print codes for guests.",
  "vouchers.col.batch": "Batch",
  "vouchers.col.used": "Used",
  "vouchers.col.expiry": "Expiry",
  "vouchers.col.created": "Created",
  "vouchers.col.createdBy": "Created by",
  "vouchers.revokedCount": (n: number) => `${n} revoked`,
  // Stated in words, never as a bare date: "valid until 1 September" and "valid for
  // 24 hours from first use" are different products, and a guest holding a printed
  // code cannot tell which one they have.
  // Takes the span already formatted, not a number of hours. `Math.round(minutes /
  // 60)` turned 90 minutes into "2 hours" and needed a `?? 1` at the call site that
  // invented an hour out of nothing.
  "vouchers.expiry.fromFirstUse": (span: string) => `${span} from first use`,
  "vouchers.expiry.fromFirstUseUnknown": "From first use",
  // Tensed, because the cell beside it is a relative time: "Expires in 24 days",
  // "Expired 160 days ago". "Until in 24 days" is what the untensed version reads
  // like, and it is the kind of thing a reader blames on the whole product.
  "vouchers.expiry.expires": "Expires",
  "vouchers.expiry.expired": "Expired",
  "vouchers.batch.fallbackTitle": "Voucher batch",
  // The plan name is appended by the caller, inside its own `lang` — venue text in
  // an English console, marked rather than translated.
  "vouchers.batch.summary": (count: number) => plural(count, "voucher", "vouchers"),
  "vouchers.batch.print": (n: number) => `Print ${plural(n, "voucher", "vouchers")}`,
  "vouchers.batch.printIdle": "Print",
  "vouchers.batch.printCounting": "Counting the vouchers this will print…",
  "vouchers.batch.printPreparing": "Preparing the print sheet…",
  "vouchers.batch.printSheetFailed": "The print sheet did not load. Reload the page to try again.",
  "vouchers.batch.printNothing":
    "Every voucher in this batch has been used, has expired or was revoked. There is nothing to print.",
  "vouchers.batch.printWhat":
    "Prints every unused voucher in this batch. Used, expired and revoked codes are left out — a printed slip that cannot be redeemed is worse than no slip.",
  "vouchers.batch.printTooMany":
    "This batch is larger than the print sheet can gather. Split it into smaller batches.",
  "vouchers.batch.printScope.title": (n: number) =>
    `Printing produces ${plural(n, "slip", "slips")}, not what is listed below`,
  "vouchers.batch.printScope.body":
    "Print covers every unused voucher in this batch. The search and the status filters change the table only — they never change what comes out of the printer.",
  "vouchers.batch.listTruncated": (n: number) => `First ${n} — more match than are listed`,
  "vouchers.batch.expired.title": "Batch expired",
  "vouchers.batch.expired.body":
    "Every voucher in this batch expired on the batch's own date, used or not.",
  "vouchers.batch.search": "Search voucher codes…",
  "vouchers.batch.searchLabel": "Search voucher codes",
  "vouchers.batch.empty.title": "No vouchers in this batch.",
  "vouchers.col.code": "Code",
  "vouchers.col.state": "Status",
  "vouchers.col.firstUsed": "First used",
  "vouchers.col.expires": "Expires",
  "vouchers.firstUsed.absent": "Not used.",
  "vouchers.expires.absent": "Not set.",
  "vouchers.state.unused": "Unused",
  "vouchers.state.inUse": "In use",
  "vouchers.state.exhausted": "Used up",
  "vouchers.state.expired": "Expired",
  "vouchers.state.revoked": "Revoked",
  "vouchers.revoke": "Revoke",
  // The toast reports what happened, not what was intended: revoking a voucher does
  // not by itself end a live session — that needs a driver call, which can fail.
  "vouchers.revoked": "Voucher revoked.",
  "vouchers.revokedAndEnded": "Voucher revoked and the live session ended.",
  "vouchers.createDialog.title": "Create voucher batch",
  "vouchers.createDialog.label": "Label",
  "vouchers.createDialog.labelHint": "Staff-only, for example “Daily print — counter”.",
  "vouchers.createDialog.count": "Count",
  "vouchers.createDialog.hours": "Hours per voucher",
  "vouchers.createDialog.expiry": "Expiry",
  "vouchers.createDialog.fromFirstUse": "From first use",
  "vouchers.createDialog.fromCreation": "From creation",
  "vouchers.createDialog.expiryHint":
    "The two produce very different guest experiences, and a guest holding a printed code cannot tell them apart. The printed voucher states which one in words.",
  "vouchers.createDialog.data": "Data allowance",
  "vouchers.createDialog.dataUnit": "MB, or blank for none",
  "vouchers.createDialog.rateDown": "Download limit",
  "vouchers.createDialog.rateUp": "Upload limit",
  "vouchers.createDialog.rateUnit": "kbps, or blank for none",
  // A batch is redeemable on any access point in the site, so "who applies this" has
  // no single answer — the weakest device decides what a guest actually gets.
  "vouchers.createDialog.rateIgnored": (names: string) =>
    `${names} cannot apply a rate limit. A guest who lands there gets no limit at all.`,
  "vouchers.createDialog.dataSampled": (names: string) =>
    `${names} cannot enforce a data cap. There it is measured from accounting instead — sampled every five minutes at best, so a guest can overshoot it.`,
  "vouchers.createDialog.rateNowhere":
    "No access point in this site can apply a rate limit. Setting one here states a cap that nothing enforces.",
  "vouchers.createDialog.noDevices":
    "This site has no access points, so nothing will enforce anything on this batch.",
  "vouchers.createDialog.submit": (n: number) => `Create ${plural(n, "voucher", "vouchers")}`,
  "vouchers.createDialog.planFallback": "Voucher",
  "vouchers.createDialog.failed.title": "Batch not created",
};

void loadLocale(() => import("./vi/vouchers.js"));

export const t = makeT({ ...core, ...vouchers });
