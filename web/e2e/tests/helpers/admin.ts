import { expect, type Locator, type Page } from "@playwright/test";

/*
 * Admin selectors.
 *
 * Same discipline as the portal helpers: structure and `data-*`, never copy. The
 * admin is being converted from Vietnamese to English right now; asserting on a
 * label would make this suite fail on a translation instead of on a regression.
 *
 * `data-level` on a status pill is the interesting one. It is a *semantic*
 * attribute — `positive`, `negative`, `caution-minor`, `urgent` — so a test can
 * assert what a row means without reading a word of it.
 */
export const admin = {
  sidebar: "aside.sidebar",
  navItems: "nav.nav a.nav-item",
  pageTitle: ".page-title",
  cards: ".card-grid .card",
  metricValue: ".metric-value",
  metricFoot: ".metric-foot",
  table: "table",
  tbody: "tbody",
  /* Real rows only: skeletons and the empty/zero-result rows are not data. */
  rows: "tbody tr:not(.empty-row)",
  chips: ".chip-group .chip",
  /* Bulk selection. A selection control changes nothing about a
     session — it decides which rows the operator is looking at. */
  rowSelect: 'input[data-role="row-select"]',
  batchBar: ".batch-bar",
  /* Session detail. A dock, not a page: the operator is triaging a list,
     so the list stays on screen. Located by its landmark role rather than by the
     class, because the role is the part a screen reader depends on. */
  detailDock: '.page > .detail-dock[role="complementary"]',
  detailPanel: ".detail-dock .detail-panel",
  search: ".toolbar .search input",
  pill: ".pill",
  notice: ".notice",
  toasts: '[aria-live="polite"]',
  sections: "section.section",
  keyValues: "dl.kv",
  /* The capability-gated action buttons on the access-point detail screen. They
     live in the one `section.section` that is a direct child of `.page`. */
  /*
   * The capability-gated controls on the access-point screen.
   *
   * `button, a.btn` because two of them are links: they lead to where the setting
   * actually lives (a voucher batch's rate, the site's garden) rather than being
   * buttons with no handler. A selector matching only `button` would silently count
   * zero and the suite would go green while measuring nothing.
   */
  gatedControls: ".page > section.section .card button, .page > section.section .card a.btn",
} as const;

/**
 * `/sessions`, holding the rows the narrow work was measured against.
 *
 * `Orphaned`, `Unknown` and `Ended` carry a freshness qualifier and take the Status
 * column to 202px; a healthy row says `Online` and takes it to 85px. Only the
 * authored rows carry the wide labels, and that 117px is the whole of the difference
 * between `/sessions` measuring 189px over its port and measuring 72.
 *
 * **A server-side filter, deliberately.** `?only=` pins rows by id but only narrows
 * what was already fetched, so it cannot reach a row the page never carried — which
 * is the first thing anyone will reach for here. Selecting on `state` re-asks the
 * mock, so no quantity of healthy rows in front of the fixtures can empty it.
 *
 * **This is not a second copy of `LIVE_SESSION_WINDOW`'s argument.** That bound
 * (`web/mock/src/state.ts`) stops portal arrivals burying the authored rows, and it
 * is the reason the fit numbers are stable today. But volume was one reason page one
 * can lose the anomalies and never the only one: a fixture edit, a default filter, a
 * site switch or a stray `q=` empties it just as well, and a fit assertion measuring
 * a page of healthy rows goes green and says nothing. `restsOn` in
 * `narrow-columns-fit` detects such an absence; selecting the view means the fit
 * assertions never rest on page one's composition in the first place. They compose.
 *
 * Here for the same reason `NARROW_COLUMNS_REVERTED` is: two files need it, and a
 * second copy would drift.
 */
export const SESSIONS_WIDEST_ROWS = "/sessions?state=orphaned,unknown,ended";

/**
 * The narrow column rules, put back the way they were.
 *
 * Below 900px the kept columns share the scroll port instead of claiming their
 * content width: values wrap, secondary lines wrap, pills wrap between and inside
 * their segments, and `IdCell`'s head takes a zero flex basis so its middle
 * truncation is reachable. Reverting the group restores the state the narrow layout
 * exists to prevent — a table wider than the port it lives in.
 *
 * **It lives here because two files need it and a second copy would drift.**
 * `narrow-columns-fit` injects it to prove its own lock can go red;
 * `narrow-values-reachable` injects it because its geometry evidence needs a table
 * that is wider than the port, and with the shared-port rules in place no table is.
 *
 * The recorded figures with this injected, at 390×844 under a coarse pointer, against
 * a 356px port:
 *
 *   /sessions     545px (189 over)    /accounts  415px (59 over)
 *   /controllers  473px (117 over)    /garden    407px (51 over)
 *
 * ── `wbr { display: none }`, AND WHY A REVERT HAS TO OWN IT ─────────────────
 *
 * `Mono` (`admin/src/ui/table.tsx`) splits a mono value on `MONO_SEPARATORS` and puts
 * a `<wbr>` after each one, so an IP or an `Acct-Session-Id` breaks at its own dots
 * and colons instead of being a single unbreakable token. `overflow-wrap: normal` does
 * not undo it: a `<wbr>` is an explicit break opportunity in the markup, not a
 * wrapping policy, so reverting the policy leaves the breaks in place, and the revert
 * would not be the exact inverse of the fix for the two tables that carry them.
 *
 * The neutralisation run — same stylesheet, plus `.table-scroll wbr { display: none }`
 * — is why this line is here rather than a baseline being re-recorded:
 *
 *   route         revert   + wbr:none   <wbr> in the table
 *   /attempts        45          80     105     ← back to its recorded figure exactly
 *   /garden          25          79      19
 *   /sessions       176         176       0
 *   /controllers    133         133       0
 *   /accounts        98          98       0
 *   voucher codes     0           0      80     ← carries them and fits either way
 *
 * **Only the tables that actually contain a `<wbr>` move**, which is what makes this
 * a repair to the sabotage rather than a wider stylesheet — the property the whole
 * file rests on. `/attempts` returning to exactly 80 is the evidence, not the intent.
 *
 * It also says what *not* to conclude. `/sessions`, `/controllers` and `/accounts`
 * contain **no `<wbr>` at all** — `IdCell` builds its own `.mono` span and never calls
 * `Mono` — so whatever makes them measure 176/133/98 against their recorded 189/117/59
 * is **not** this, and it has not been established. All three still clear
 * their `/2` floor, so nothing is re-recorded on an untested cause. The one measured
 * fact worth starting from is that `/sessions`'s identity column is 254px on the
 * landing view and 202.4px on a filtered one **with identical per-row content**.
 *
 * Equal specificity to the rules it reverts, so it wins on source order. If it ever
 * stops overflowing, something else is holding those tables and the numbers above
 * need taking again — do not soften the assertions that depend on it.
 */
export const NARROW_COLUMNS_REVERTED = `
  @media (max-width: 900px) {
    .table-scroll th { white-space: nowrap; }
    .table-scroll td, .table-scroll th { overflow-wrap: normal; }
    .table-scroll .pill { flex-wrap: nowrap; }
    .table-scroll .pill-seg { white-space: nowrap; }
    .table-scroll .stack-sub { white-space: nowrap; }
    .table-scroll .id-cell { flex-wrap: nowrap; }
    .table-scroll .truncate-tail { min-width: 0; }
    .table-scroll .truncate-tail > .head { flex: 0 1 auto; }
    .table-scroll wbr { display: none; }
    .row-actions { flex-wrap: nowrap; }
  }`;

/** Waits until the table has finished its first load and is showing real rows. */
export async function tableReady(page: Page): Promise<Locator> {
  const tbody = page.locator(admin.tbody).first();
  await expect(tbody).toHaveAttribute("aria-busy", "false");
  return page.locator(admin.rows);
}

/** `ABCDEFGHJKMN` → `ABCD-EFGH-JKMN`, the grouping the admin renders codes in. */
export function groupCode(code: string): string {
  return (code.match(/.{1,4}/g) ?? [code]).join("-");
}

/**
 * Every visible action control on the page that is not part of the page header and
 * not inside a dialog.
 *
 * A coarse backstop for the capability assertions: even if the precise gated-control
 * locator drifts, a low-capability NAS must still offer strictly fewer things to do.
 */
export async function actionControlCount(page: Page): Promise<number> {
  /*
   * `button, a.btn` — an action, not an element name.
   *
   * This counted only `<button>` until two capability-gated controls became links to
   * where their setting actually lives. Nothing about the page got worse and the
   * backstop stopped seeing them, which is the failure mode a *coarse* check is
   * supposed to be immune to: it exists to survive the precise locator drifting, and
   * it was itself pinned to a tag.
   *
   * A link styled as a button and rendered by the same capability gate is an action
   * control. Counting one and not the other measured the markup rather than the
   * product.
   */
  return page
    .locator("main button:visible, main a.btn:visible")
    .evaluateAll(
      (els) => els.filter((el) => !el.closest(".page-head") && !el.closest("dialog")).length,
    );
}
