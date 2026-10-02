import type { ReactNode, RefObject } from "react";
import { t } from "../i18n/index.js";
import { Notice } from "../ui/primitives.js";
import { groupedNotes, type BatchOutcome } from "./batch.js";

/**
 * The bar that appears when rows are ticked, and the shell every screen shares.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **What goes in it is a per-screen judgement and it is deliberately not one answer.**
 * The temptation with a shared bar is to put selection on every table because the
 * component now exists. A checkbox column on a table with nothing to do to a set of
 * rows is furniture — it costs a column, a tab stop and an announcement per row, and
 * it pays nothing. `/attempts` and `/audit` are records: there is no operation on a
 * row of either, so they have no selection and no bar.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **An empty bar is worse than no bar.** Where every action in it is gated away from
 * this operator the screen must render nothing at all — a toolbar offering *Clear
 * selection* and nothing else advertises a capability by its own outline. That is the
 * screen's decision because the screen knows which permissions it asked about, so
 * this component is never mounted defensively: a caller with no permitted action does
 * not render the selection column either.
 *
 * Floating, and the reason is in `admin.css`: an operator who has scrolled forty rows
 * to finish a selection must not scroll back up to act on it.
 */
export function BatchBar({
  count,
  children,
  onClear,
  barRef,
}: {
  count: number;
  /** The screen's own actions. One primary verb, not a menu. */
  children: ReactNode;
  onClear: () => void;
  /**
   * The pill, for a screen whose verb can remove itself under the operator's finger.
   * See `focusFirstAction` — `/accounts` is the one that needs it.
   */
  barRef?: RefObject<HTMLDivElement | null>;
}) {
  return (
    <div className="batch-bar">
      <div className="batch-pill" ref={barRef}>
        <span className="batch-count">{t("bulk.selected", count)}</span>
        {/* The reset rule, said where the selection is. Without it an operator who
            types in the search box and loses twelve ticks has met a bug. */}
        <span className="batch-note">{t("bulk.resetNote")}</span>
        {children}
        <button type="button" className="btn btn-sm btn-ghost" onClick={onClear}>
          {t("bulk.clear")}
        </button>
      </div>
    </div>
  );
}

/**
 * What a batch of ten where three failed says about the three.
 *
 * Two notices rather than one, because they ask different things of the operator. A
 * **failure** is retryable and those rows are still ticked, so the sentence points at
 * the control that retries them. A **note** is a write that succeeded and a consequence
 * that did not — retrying it does nothing, so it says what is now true instead.
 *
 * `role="alert"`: both mount with their text, at the moment something failed, which is
 * the one shape a live region actually announces.
 */
export function BatchReport({
  outcome,
  notedTitle,
}: {
  outcome: BatchOutcome | null;
  /**
   * What *the write succeeded and its consequence did not* means on this screen, since
   * only the screen knows: revoking a code has a session to end, and nothing else in
   * the contract answers `200` with a `Failure` yet. Naming it here rather than in
   * the shared copy keeps the first operation that joins from inheriting a sentence
   * about voucher codes. With none given the catalogue's own title for the problem
   * type stands in — a real fallback rather than a plausible-sounding one.
   */
  notedTitle?: (n: number) => string;
}) {
  if (!outcome || (outcome.failed.length === 0 && outcome.noted.length === 0)) return null;
  return (
    <div className="stack-sm">
      {outcome.failed.length > 0 ? (
        <Notice
          level="warning"
          role="alert"
          title={t("bulk.failed.title", outcome.failed.length, outcome.total)}
        >
          {t("bulk.failed.body")} {groupedNotes(outcome.failed)}
        </Notice>
      ) : null}
      {outcome.noted.length > 0 ? (
        <Notice
          level="warning"
          role="alert"
          title={
            notedTitle?.(outcome.noted.length) ?? outcome.noted[0]?.copy.title ?? ""
          }
        >
          {t("bulk.noted.body", outcome.noted.length)} {groupedNotes(outcome.noted)}
        </Notice>
      ) : null}
    </div>
  );
}
