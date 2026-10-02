import { Fragment, useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import {
  absoluteTime,
  bytes as fmtBytes,
  relativeTime,
  splitForTruncation,
} from "../format.js";
import { t } from "../i18n/index.js";
import { errorCopy } from "../problems.js";
import { useNarrow } from "../layout.js";
import { Alert, ChevronDown, Filter } from "./icons.js";
import { CopyButton, Empty, SkeletonRows } from "./primitives.js";

/* ── cell semantics ───────────────────────────────────────────────────────── */

/**
 * Cell *types*, not per-column styling.
 *
 * Monospace, tabular figures, alignment and truncation strategy are properties of
 * what a value **is** — an identifier, a quantity, a timestamp — so they are
 * defined once here and inherited everywhere. The consequence is that no screen
 * can forget to monospace a MAC address or right-align a byte count, because no
 * screen makes that decision.
 */

/** An absent value is a hyphen at a lighter tier. Never a blank cell. */
export function Absent({ hint }: { hint?: string }) {
  return (
    <span className="empty-value" title={hint}>
      –
    </span>
  );
}

/**
 * Every character that a mono value uses to separate its own parts.
 *
 * A hostname's dots and hyphens, a MAC's colons, a CIDR's slash, an account's `@`
 * and `_`. Split *after* the separator so a fragment ends with the mark that says it
 * continues — `portal.` rather than `portal` — which is the whole reason a break here
 * is legible and a break one character later is not.
 */
const MONO_SEPARATORS = /(?<=[.:\-/@_])/;

/**
 * The value's own break opportunities, and no others.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * At 390px the narrow table's `overflow-wrap: anywhere` broke
 * mono values mid-token on four routes — `portal.foye`/`r.local`, `104.16.0.0/`/`13`,
 * a MAC in three pieces, an operator's name mid-syllable — and `admin.css` already
 * records why that is not a cosmetic fault: a wrapped `164/500` reading `164/5` over
 * `00` *"is not a clipped number — it is a different, smaller, entirely plausible
 * one."* Two plausible hostnames is the same defect on the column that identifies
 * the row.
 *
 * **The obvious fix could not be used**, and the measurement is why: `overflow-wrap:
 * normal` alone raises the cell's min-content to the entire string and puts
 * `/garden`'s scroller 26px past its own port at 390 — which is precisely what
 * `narrow-columns-fit` asserts against. So instead the wrap rule goes to `normal`
 * (`admin.css`, `.mono`) and the break opportunities come back one at a time, at the
 * separators the value already contains. min-content falls to the longest run between
 * two of them. Measured over all nine narrow tables, fine and coarse pointer: zero
 * mid-token breaks, zero added overflow.
 *
 * `<wbr>` rather than a zero-width space, because a ZWSP is a *character* — it would
 * land in `textContent`, in what an operator copies, and in every assertion that reads
 * a cell.
 */
function withBreaks(value: string): ReactNode {
  const parts = value.split(MONO_SEPARATORS);
  if (parts.length < 2) return value;
  const out: ReactNode[] = [];
  parts.forEach((part, i) => {
    out.push(part);
    if (i < parts.length - 1) out.push(<wbr key={i} />);
  });
  return out;
}

export function Mono({ children }: { children: ReactNode }) {
  return (
    <span className="mono">{typeof children === "string" ? withBreaks(children) : children}</span>
  );
}

/**
 * An identifier: monospace, middle-truncated so the tail survives, with
 * filter-by-this-value and copy-this-value revealed on hover or focus.
 *
 * The tail matters because MACs and `Acct-Session-Id`s carry their discriminating
 * characters at the end — truncating from the right hides exactly the part that
 * tells two rows apart. The two actions matter because filtering by an identifier
 * you are looking at is the single most common thing an operator does with one.
 */
export function IdCell({
  value,
  subject,
  onFilter,
  keepTail = 8,
  suffix,
}: {
  value: string;
  subject: string;
  onFilter?: (value: string) => void;
  keepTail?: number;
  suffix?: ReactNode;
}) {
  const { head, tail } = splitForTruncation(value, keepTail);
  return (
    <span className="id-cell">
      <span className="mono truncate-tail" title={value}>
        {head ? <span className="head">{head}</span> : null}
        <span className="tail">{tail}</span>
      </span>
      {suffix}
      <span className="id-actions">
        {onFilter ? (
          <button
            type="button"
            className="id-action"
            title={t("common.filterBy", subject)}
            aria-label={t("common.filterBy", subject)}
            onClick={(e) => {
              e.stopPropagation();
              onFilter(value);
            }}
          >
            <Filter size={13} />
          </button>
        ) : null}
        <CopyButton value={value} subject={subject} />
      </span>
    </span>
  );
}

/** Relative in the cell, absolute with UTC offset in the tooltip. */
export function TimeCell({ iso, absent }: { iso: string | null | undefined; absent?: string }) {
  const rel = relativeTime(iso);
  const abs = absoluteTime(iso);
  if (!rel || !iso) return <Absent hint={absent} />;
  return (
    <time dateTime={iso} title={abs ?? undefined}>
      {rel}
    </time>
  );
}

/**
 * A quantity. Right-aligned by the column, tabular figures here.
 *
 * **`wrapped` is not decoration.** `Acct-Input-Octets` and `Acct-Output-Octets` are
 * 32-bit (RFC 2866 §5.3–5.4), and a NAS that omits Gigawords (RFC 2869 §5.1–5.2)
 * reports a total that drops by 4 GiB every 4 GiB — so a session runs *backwards*,
 * 3.81 GB → 14 MB across a single tick, and a console without this flag renders that
 * as a plain fact on a healthy access point.
 *
 * When the flag is set the cell shows the reconstructed total and says it is a
 * **floor**: two wraps between two accounting records are indistinguishable from
 * one, and at the 300-second interim floor 4 GiB in one interval is 114 Mbps, which
 * a hotel's gigabit uplink reaches. A lower bound drawn as a measurement is the same
 * lie in a smaller font, so the qualifier is on the value and not in a tooltip.
 *
 * The reported counter is never corrected in place — it is the evidence, and it
 * stays reachable in the tooltip beside the arithmetic that produced the total.
 */
export function BytesCell({
  value,
  total,
  wrapped,
}: {
  value: number | null | undefined;
  /** `value` plus 4 GiB per observed wrap. Ignored unless `wrapped`. */
  total?: number | null;
  /** `Session.countersWrapped`. Sticky for the life of the session. */
  wrapped?: boolean;
}) {
  const text = fmtBytes(value);
  if (text === null) return <Absent hint={t("freshness.noAccounting")} />;
  if (!wrapped) return <span className="num">{text}</span>;

  const floor = fmtBytes(total ?? value);
  return (
    <span
      className="num num-anomaly"
      title={t("sessions.wrapped.why", text)}
      data-testid="counter-wrapped"
    >
      {t("bytes.atLeast", floor ?? text)}
    </span>
  );
}

/**
 * A primary label with a secondary line beneath it.
 *
 * The absent case gets its own tier rather than a blank line — an account with no
 * display name is the normal case, not a rendering failure, and it should look like
 * it.
 */
export function Stack({
  primary,
  secondary,
  absentText,
}: {
  primary: ReactNode;
  secondary?: ReactNode;
  absentText?: string;
}) {
  return (
    <span className="stack">
      <span>{primary}</span>
      {secondary ? (
        <span className="stack-sub">{secondary}</span>
      ) : absentText ? (
        <span className="stack-sub stack-sub-absent">{absentText}</span>
      ) : null}
    </span>
  );
}

/* ── table ────────────────────────────────────────────────────────────────── */

export interface Column<T> {
  key: string;
  header: string;
  /**
   * Quantitative numerics only — bytes, durations, counts. Categorical numerics
   * such as VLAN IDs and port numbers stay left, because they are labels.
   */
  align?: "end";
  width?: string;
  /**
   * Whether the column survives the narrow layout, where there is room for two or
   * three of them.
   *
   * **Required, because either default is a silent wrong answer.** `keep` lets a
   * phone quietly grow a tenth column; `drop` lets the column that identifies the
   * row quietly vanish. Both are invisible in the browser anyone reviews in, so the
   * decision is made where the column is declared or it is not made at all — the
   * same mechanism the copy catalogues use to turn a forgotten string into a compile
   * error rather than a blank one.
   *
   * Two rules the type cannot state and this comment therefore must:
   *
   * - **A column that renders a control is always `keep`.** Hiding a *value* defers
   *   it to one tap; hiding a *control* makes the action impossible. Revoke on a
   *   voucher is, by its own comment, the only way to put a live guest off the
   *   network. `Table` enforces the mechanical half of this — see the assertion at
   *   the top of it — and cannot enforce the rest.
   * - **Never the column carrying the row's identity.** Two kept columns that
   *   between them name nothing leave a phone looking at rows it cannot tell apart.
   */
  narrow: "keep" | "drop";
  cell: (row: T) => ReactNode;
}

/**
 * Multi-row selection, supplied by the screen because the screen owns the rule that
 * makes it safe: a selection never outlives the filters it was made under. See
 * `list/selection.ts`.
 */
export interface TableSelection<T> {
  checked: ReadonlySet<string>;
  /** `extend` is true for a shift-click — the row range from the anchor. */
  onToggle: (key: string, extend: boolean) => void;
  /** Selects every visible row, or clears when they are all already selected. */
  onToggleAll: () => void;
  /** Names the row, so the checkbox does not announce as "checkbox, blank". */
  rowLabel: (row: T) => string;
  allLabel: string;
  /**
   * Whether this row can be acted on at all. Defaults to every row.
   *
   * The same rule as the actions column, one control along: a required walled-garden
   * entry has no delete button and a revoked voucher has no revoke button, so a
   * checkbox that puts either into a batch is a control that does nothing — the thing
   * *hide, never disable* exists to remove. It also decides what select-all means: a
   * header box that ticks rows no action can reach reports a count the operator
   * cannot act on.
   *
   * The screen must pass the same predicate to `useSelection`'s `keys`, or
   * `Shift`-extend puts back what the missing checkbox left out.
   */
  selectable?: (row: T) => boolean;
}

export interface TableProps<T> {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T) => string;
  isLoading?: boolean;
  /**
   * What the list query threw.
   *
   * `unknown`, not `Error`, because nothing here renders `.message`: that is
   * `ApiError`'s `problem.title` — a short label the *server* wrote, in the server's
   * language — and putting it under the table put a server-written sentence on every
   * list screen in the console. The sentence comes from the catalogue.
   */
  error?: unknown;
  /** Shown when the collection has never had data. */
  empty?: ReactNode;
  /** True when rows exist but the current filters exclude them all. */
  filtered?: boolean;
  onClearFilters?: () => void;
  onRowClick?: (row: T) => void;
  /** The cursor row: focused, highlighted, and what the detail panel is about. */
  selectedKey?: string | null;
  selection?: TableSelection<T>;
  /**
   * Names one row, for the narrow layout's disclosure button.
   *
   * "Show more" repeated down a list is one accessible name announced fifteen times,
   * and a screen-reader user tabbing through it learns nothing about which row they
   * are on. Falls back to `rowKey`, which is an opaque id and still identifies the
   * row — so the fallback is honest rather than good, and every list that has a
   * better answer passes one.
   */
  rowLabel?: (row: T) => string;
  /** Rendered inside <thead> during a refetch, so rows never vanish. */
  refreshing?: boolean;
  /**
   * `dataUpdatedAt` from the query — when these rows were last successfully
   * fetched. Half of one rule, not a nicety: rows stay on screen through a refetch
   * *because* the operator is told how old they are, and shipping the first half
   * without the second is worse than shipping neither, since a failing refresh then
   * leaves stale rows with nothing saying so.
   */
  updatedAt?: number | null;
}

/**
 * The select-all box, which is three-state.
 *
 * `indeterminate` cannot be expressed in JSX — it is a property, not an attribute —
 * so it is set through a ref. Without it a partially-selected table shows an empty
 * box, and clicking it looks like "select all" while meaning "clear".
 */
function TriCheckbox({
  checked,
  indeterminate,
  label,
  onToggle,
}: {
  checked: boolean;
  indeterminate?: boolean;
  label: string;
  onToggle: (extend: boolean) => void;
}) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (ref.current) ref.current.indeterminate = Boolean(indeterminate);
  }, [indeterminate]);
  return (
    <input
      ref={ref}
      type="checkbox"
      className="row-check"
      data-role="row-select"
      checked={checked}
      aria-label={label}
      onClick={(e) => e.stopPropagation()}
      onChange={(e) => {
        // Shift-click extends. The native event behind the change is the click, so
        // the modifier is still here; a keyboard toggle has none, which is correct.
        const native = e.nativeEvent as Partial<MouseEvent>;
        onToggle(Boolean(native.shiftKey));
      }}
    />
  );
}

/**
 * `Enter` and `Space` on a row, for the rows that are tab stops.
 *
 * `onClick` on a `<tr>` does not fire for either key — a row is not a button — so on
 * every table with `onRowClick` a keyboard user could Tab to a row, see the pointer
 * cursor, press the key that means *activate*, and have nothing at all happen.
 * Measured on `/vouchers` and `/access-points`; WCAG 2.1.1, and invisible to axe
 * because the row is genuinely focusable and genuinely has a name.
 *
 * It lives beside the `tabIndex` that keys on the same `onRowClick` so the two cannot
 * drift: whatever makes a row a tab stop is what makes it activatable, in one place.
 *
 * Three details that are load-bearing rather than defensive:
 *
 * - **Only when the row itself has focus.** Every control inside a cell already
 *   answers `Enter`/`Space`, and the keydown bubbles up here afterwards. Without the
 *   target check, copying a MAC or opening a disclosure would also open the record
 *   behind it — the same mistake `stopPropagation` on those clicks exists to avoid.
 * - **`preventDefault`, because `Space` scrolls.** A list that jumps a screenful
 *   every time a row is opened is worse than one that ignores the key.
 * - **`stopPropagation`, because the registry listens at the window.** Its
 *   `Enter`/`Space` binding opens *the cursor's* row, which is not always the row
 *   holding focus; letting both run means two opens per press and the second one wins.
 */
function activateRow(event: KeyboardEvent<HTMLTableRowElement>, activate: () => void): void {
  if (event.key !== "Enter" && event.key !== " ") return;
  if (event.target !== event.currentTarget) return;
  if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
  /* Held keys autorepeat; the registry ignores repeats for the same reason. */
  if (event.repeat) return;
  event.preventDefault();
  event.stopPropagation();
  activate();
}

/**
 * The table.
 *
 * Four fetch states in a fixed order — loading, error, empty, data — never nested
 * ternaries. Loading is skeleton rows rather than a spinner, so the column layout
 * survives and the table does not collapse and re-expand while someone is reading
 * it. A refetch keeps the existing rows on screen and puts the progress indicator
 * in the header: an operator watching a live sessions list must never have rows
 * disappear under the cursor.
 */
export function Table<T>({
  columns,
  rows,
  rowKey,
  isLoading,
  error,
  empty,
  filtered,
  onClearFilters,
  onRowClick,
  selectedKey,
  selection,
  rowLabel,
  refreshing,
  updatedAt,
}: TableProps<T>) {
  /*
   * Which columns are on screen, and the second non-`Column` cell.
   *
   * The dropped set is computed rather than hidden in CSS: `display: none` would
   * leave every dropped cell in the DOM, rendered, and then announce it a second
   * time from inside the disclosure list. `useNarrow` is a subscription, so a
   * rotation re-renders this.
   */
  const narrow = useNarrow();
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set());
  const dropped = narrow ? columns.filter((c) => c.narrow === "drop") : [];
  const discloses = dropped.length > 0;
  const shown = discloses ? columns.filter((c) => c.narrow === "keep") : columns;

  /*
   * The pairing `Column.narrow`'s comment cannot express in the type.
   *
   * Four columns in this console carry `header: ""` and exist only to hold controls.
   * Dropping one puts `<dt></dt>` in the disclosure list — an unlabelled term, which
   * is both an accessibility defect and a control the operator can no longer reach.
   * Checked at every width, because the browser a developer uses is the wide one and
   * a narrow-only assertion would fire for nobody.
   *
   * Development only: this is a mistake in the source, not in the data, so it cannot
   * survive a build that anybody ran once. A rule that is only written down is a rule
   * the fifth actions column will not have read.
   */
  if (import.meta.env.DEV) {
    const unlabelled = columns.find((c) => c.narrow === "drop" && c.header === "");
    if (unlabelled) {
      throw new Error(
        `Column "${unlabelled.key}" has an empty header and narrow: "drop". A column ` +
          `with no header is a column of controls, and a control that is dropped is an ` +
          `action the narrow layout cannot perform — mark it narrow: "keep".`,
      );
    }
  }

  /*
   * ───────────────────────────────────────────────────────────────────────────
   *  A query that has never answered is not an empty collection
   * ───────────────────────────────────────────────────────────────────────────
   *
   * `isEmpty` was `!isLoading && !error && rows.length === 0`, and React Query's
   * `isLoading` is `isPending && isFetching`. So a query at `fetchStatus: "paused"`
   * — the browser is offline, the request is queued and has never run — is not
   * loading, has no error and has no rows, and every list screen in this console
   * answered that by rendering its never-had-data state. Reproduced offline on
   * `/sessions`, `/accounts` and `/vouchers`: *"No sessions."*, *"No accounts."* with
   * an **Add account** button under it, *"No voucher batches."* with **Create batch**
   * — three invitations to write to a backend nobody can reach, and `aria-busy` false
   * over all of them. Found while building the auth screens, where it reads
   * *"No operators. Nobody can sign in to this console."*
   *
   * It is the product's own thesis failing again — absence is not zero — and it is
   * fixed here rather than at eleven call sites for the reason `Column.narrow` gives:
   * a rule that has to be remembered per screen is a rule the twelfth screen will
   * not have read.
   *
   * **`updatedAt` is the signal, and it is one the screens already pass.**
   * `dataUpdatedAt` is 0 until a query has succeeded once, so `> 0` means *this
   * collection has answered at least once and what you see is its answer*. The
   * alternative — a new `isPending` prop — would be optional, per-call-site and
   * unenforced, which is precisely how an optional `Pill.icon` leaves chips glyphless.
   *
   * `updatedAt == null` keeps the static behaviour for a caller that passes none: the
   * gallery renders these states deliberately with static arrays and must keep being
   * able to. Every one of the eleven real lists passes it.
   *
   * An error still wins. A failed query has also never answered, and the error row is
   * the more specific truth.
   */
  const answered = updatedAt == null || updatedAt > 0;
  const waiting = Boolean(isLoading) || (!answered && !error);
  const isEmpty = !waiting && !error && rows.length === 0;

  /* The selection checkbox is not a `Column` and neither is the disclosure. Both
     have to be counted here or the empty, error and skeleton rows span short. */
  const span = shown.length + (selection ? 1 : 0) + (discloses ? 1 : 0);

  const keys = rows.map(rowKey);
  /* Select-all is over the rows that *have* a checkbox. Counting the rest makes the
     header box permanently indeterminate on `/garden`, where two of seven entries are
     required and can never be ticked — a three-state control stuck in its third
     state says "partly selected" about a set that is entirely selected. */
  const pickable = selection?.selectable ? rows.filter(selection.selectable).map(rowKey) : keys;
  const checkedCount = selection ? pickable.filter((k) => selection.checked.has(k)).length : 0;

  /*
   * Roving tabindex. Exactly one row is reachable with Tab — the cursor if it is on
   * screen, otherwise the first row — and the arrow keys move between them. A table
   * where every row is a tab stop makes Tab useless for leaving the table, which is
   * the most common thing a keyboard user wants to do with a long list.
   */
  const tabKey = selectedKey && keys.includes(selectedKey) ? selectedKey : (keys[0] ?? null);

  /*
   * The row set changing under the operator, said once.
   *
   * Filtering, clearing filters and a refetch that adds or removes rows all rewrite
   * the table with no announcement of any kind: the visible count is the only signal,
   * and it is not a signal for someone who is not looking at it. This is the one
   * live region the table owns — the *Last updated* line deliberately stays out of it
   * (see below), because that is a fact to be looked up rather than an interruption.
   *
   * **Silent when the count is unchanged**, which is most polls. These lists refetch
   * every fifteen seconds against a mock on a compressed clock; a region that speaks
   * on every tick is a region an operator switches off, and then it is worse than
   * nothing because the console believes it has one.
   *
   * The first settled render only records the count. Arriving on a screen is not a
   * change, and the page announces itself well enough without the table shouting a
   * number over the top of it.
   *
   * The message is wrapped in a keyed element rather than set as text. Filter, clear,
   * filter again produces the same sentence twice, React writes no DOM for a string
   * identical to the last one, and a live region with no mutation announces nothing —
   * the second identical count would be the one that went missing.
   */
  /*
   * ───────────────────────────────────────────────────────────────────────────
   *  The columns that are past the right-hand edge, named
   * ───────────────────────────────────────────────────────────────────────────
   *
   * `.table-scroll` absorbs a table wider than its port, which is what keeps the
   * *page* from scrolling — and it is also why nothing on screen said that two of
   * `/sessions`'s nine columns were not there. Measured at 1440 × 1000: the table
   * needs **1376px in a 1150px scroller**, `document.scrollWidth` is 1440, and
   * macOS overlay scrollbars are invisible until a gesture.
   *
   * The classic scrollbar in `admin.css` says *there is more to the right*. This
   * says **what**, which is the half that matters here: the columns that fall off
   * are the ones at the end of the declaration order, which on a table declared
   * carelessly is the column dating every byte figure on the row.
   *
   * ── WHAT IS MEASURED, AND WHY IT IS NOT SCROLL-DEPENDENT ───────────────────
   *
   * Each header's offset **within the table**, against the port's width. That is a
   * property of the layout, not of where the operator has scrolled to, so the line
   * does not rewrite itself while the table is being scrolled — which would make it
   * a distraction rather than a fact. It re-measures when the port resizes and when
   * the rendered column set or the row count changes, because a table's column
   * widths are its content's widths: **the same table fits all nine columns at 1440
   * with the backend down**, and that is exactly the trap this line exists to close.
   * A cue that only appears once the data is ugly is correct; a cue that was never
   * written because the developer's screen was empty is the defect.
   */
  const scrollRef = useRef<HTMLDivElement>(null);
  const [hiddenCols, setHiddenCols] = useState<string[]>([]);
  const shownKey = shown.map((c) => c.key).join("|");
  useEffect(() => {
    const port = scrollRef.current;
    if (!port) return;
    const measure = () => {
      const table = port.querySelector("table");
      if (!table) return;
      const base = table.getBoundingClientRect().left;
      const limit = port.clientWidth;
      const past: string[] = [];
      for (const th of port.querySelectorAll<HTMLElement>("thead tr:first-child th")) {
        const box = th.getBoundingClientRect();
        /* Any part past the edge counts. A column half on screen is a column an
           operator has to scroll to read, and the header is the half that is cut. */
        if (box.left - base + box.width > limit + 0.5 && th.textContent?.trim()) {
          past.push(th.textContent.trim());
        }
      }
      setHiddenCols((prev) =>
        prev.length === past.length && prev.every((v, i) => v === past[i]) ? prev : past,
      );
    };
    measure();
    /*
     * **Both the port and the table**, and the second one is not belt-and-braces.
     *
     * A column's width is its content's width, so the table can outgrow the port
     * without the port changing size at all — a poll bringing a longer access-point
     * name does it, and so does the world going from empty to ugly, which is the
     * sharpest half of the problem. Observing only the container fails the case that
     * widens a column from the test: the table goes 1150 → 1501 and the console goes
     * on saying nothing.
     */
    const observer = new ResizeObserver(measure);
    observer.observe(port);
    const table = port.querySelector("table");
    if (table) observer.observe(table);
    return () => observer.disconnect();
  }, [shownKey, rows.length, isLoading, error]);

  /**
   * The refusal, resolved once — and **discriminated on the type, not on the table**.
   *
   * There were two defects here. The second is this one: the headline
   * read *"Could not load this data. Reload the page to try again."* for every failure
   * this table can meet, and a staff session on `/operators` is refused **permanently**
   * — reloading asks the same server the same question about the same role and gets
   * the same `403`. Only the sentence beneath it, which is the catalogue's, corrected
   * the advice.
   *
   * `problems.ts` already carries the discrimination, so the headline is the type's own
   * label wherever the catalogue recognises it: *Not permitted for this role*, *The
   * server failed*, *No answer from the server*, *Not found*. `table.error` survives as
   * the fallback for a type this console predates, where `fromServer` is true and the
   * only honest headline is a generic one — which is also the only case where *reload
   * and try again* is still reasonable advice.
   *
   * **Not** a sentence about `403` written into every table's error row, which is the
   * repair that would have been one line shorter and wrong on eight other screens.
   */
  const failure = error ? errorCopy(error) : null;

  const [announcement, setAnnouncement] = useState<{ text: string; seq: number } | null>(null);
  const countedRows = useRef<number | null>(null);
  useEffect(() => {
    if (isLoading) return;
    const count = rows.length;
    if (countedRows.current === null) {
      countedRows.current = count;
      return;
    }
    if (countedRows.current === count) return;
    countedRows.current = count;
    setAnnouncement((prev) => ({ text: t("table.rowsShown", count), seq: (prev?.seq ?? 0) + 1 }));
  }, [rows.length, isLoading]);

  return (
    <div className="table-wrap" data-empty={isEmpty}>
      {/* Present from the first render and empty until there is news: a live region
          added to the document at the same moment as its text is a live region that
          announces nothing. `role="status"` and `aria-live` say the same thing, and
          both are written so that grepping either finds this decision. */}
      <p className="visually-hidden" role="status" aria-live="polite" data-role="row-count-live">
        {announcement ? <span key={announcement.seq}>{announcement.text}</span> : null}
      </p>

      <div className="table-scroll" ref={scrollRef}>
        <table>
          <thead>
            <tr>
              {selection ? (
                <th className="check-col">
                  <TriCheckbox
                    checked={checkedCount > 0 && checkedCount === pickable.length}
                    indeterminate={checkedCount > 0 && checkedCount < pickable.length}
                    label={selection.allLabel}
                    onToggle={() => selection.onToggleAll()}
                  />
                </th>
              ) : null}
              {discloses ? <th className="disclose-col" /> : null}
              {/* `data-col` is the column's own key, so a test can ask *which* column
                  without reading a word of the copy — the header is the only other
                  handle and it is prose that is still being rewritten. */}
              {shown.map((c) => (
                <th
                  key={c.key}
                  data-col={c.key}
                  data-align={c.align}
                  style={c.width ? { width: c.width } : undefined}
                >
                  {c.header}
                </th>
              ))}
            </tr>
            {refreshing ? (
              <tr aria-hidden="true">
                <td colSpan={span} style={{ height: 2, padding: 0, border: "none" }}>
                  <span className="skeleton" style={{ height: 2, width: "100%" }} />
                </td>
              </tr>
            ) : null}
          </thead>
          <tbody aria-busy={waiting}>
            {/* `stackAt` is the first cell that holds a value rather than a checkbox, which
                is the cell every one of these tables gives to a two-line identity
                stack — so it is the one the skeleton has to draw two bars in. See
                `SkeletonRows`. */}
            {waiting ? (
              <SkeletonRows rows={6} cols={span} stackAt={selection ? 1 : 0} />
            ) : null}

            {/*
              Only when there is nothing to show. A refetch that fails while rows are
              on screen must not replace them — "refresh never destroys the view" —
              so that case is carried by the *Last updated* line beneath the table,
              which is the same line that makes keeping the rows honest.
            */}
            {failure && rows.length === 0 ? (
              <tr className="empty-row">
                <td colSpan={span}>
                  {/*
                    `role="alert"`, and it is the whole of the first defect's fix.

                    A staff session refused on `/operators` was told **nothing**: the
                    refusal rendered here with no role and no live region, *after*
                    `aria-busy` had already flipped to `false`, with focus on `<body>`.
                    The only thing announced was that the table had finished loading —
                    on a screen whose entire content is a sentence explaining that it
                    never will.

                    **axe reports zero violations on exactly that state**, in both
                    schemes and at both widths. It is the clearest instance in this
                    repository of what the accessibility gate cannot see, and the
                    reason the announcement is the fix rather than the copy.

                    On this node rather than defaulted somewhere shared: it mounts with
                    its text, which is what makes an alert announce at all. A
                    `role="alert"` that is present and empty and later filled says
                    nothing in most screen readers — the same rule `Notice` is written
                    on, and the reason the row-count region below is not this.
                  */}
                  <div role="alert" style={{ display: "grid", placeItems: "center", gap: "0.5rem" }}>
                    <Alert size={20} />
                    <div>
                      <strong>{failure.fromServer ? t("table.error") : failure.title}</strong>
                    </div>
                    <div>{failure.body}</div>
                  </div>
                </td>
              </tr>
            ) : null}

            {/* Zero results is a different state from never-had-data: it keeps the
                table shape, dulls the header, and offers to clear the filters
                rather than offering to create the first item. */}
            {isEmpty && filtered ? (
              <tr className="empty-row">
                <td colSpan={span}>
                  {t("table.noMatches")}{" "}
                  {onClearFilters ? (
                    <button type="button" className="btn btn-sm btn-ghost" onClick={onClearFilters}>
                      {t("table.clearFilters")}
                    </button>
                  ) : null}
                </td>
              </tr>
            ) : null}

            {isEmpty && !filtered ? (
              <tr className="empty-row">
                <td colSpan={span}>{empty ?? <Empty title={t("table.empty.title")} />}</td>
              </tr>
            ) : null}

            {rows.map((row) => {
              const key = rowKey(row);
              const checked = selection?.checked.has(key) ?? false;
              const open = expanded.has(key);
              return (
                <Fragment key={key}>
                  <tr
                    data-row-key={key}
                    data-selected={selectedKey === key}
                    data-checked={checked}
                    tabIndex={onRowClick ? (key === tabKey ? 0 : -1) : undefined}
                    onClick={onRowClick ? () => onRowClick(row) : undefined}
                    onKeyDown={onRowClick ? (e) => activateRow(e, () => onRowClick(row)) : undefined}
                    style={onRowClick ? { cursor: "pointer" } : undefined}
                  >
                    {selection ? (
                      <td className="check-col">
                        {/* Empty, not a disabled box: this row has no action behind
                            it, so the control would advertise one. The cell stays
                            because the column does — the rows around it are ticked
                            and a missing cell would shift them. */}
                        {selection.selectable && !selection.selectable(row) ? null : (
                          <TriCheckbox
                            checked={checked}
                            label={selection.rowLabel(row)}
                            onToggle={(extend) => selection.onToggle(key, extend)}
                          />
                        )}
                      </td>
                    ) : null}
                    {/*
                      Leading, beside the checkbox, and that is a measurement rather
                      than a taste. Two of these tables are still wider than a 390px
                      viewport once their kept columns have their content in them —
                      `.table-scroll` is what absorbs that, and it is why the *page*
                      does not scroll. A trailing control therefore starts life off
                      the right-hand edge of a table the operator has not scrolled
                      yet: measured at 418px against a 356px container on `/garden`.
                      A control that has to be found before it can be pressed is the
                      `.id-actions` mistake again — the thing existed, and the device
                      it is for could not reach it.
                    */}
                    {discloses ? (
                      <td className="disclose-col">
                        <button
                          type="button"
                          className="row-disclosure"
                          data-role="row-disclose"
                          aria-expanded={open}
                          aria-label={t(
                            "table.moreFields",
                            dropped.length,
                            (rowLabel ?? rowKey)(row),
                          )}
                          onClick={(e) => {
                            // Exactly as `TriCheckbox` and the `IdCell` actions do.
                            // Without it, opening the disclosure on a sessions row
                            // also opens the detail dock over the thing being read.
                            e.stopPropagation();
                            setExpanded((prev) => {
                              const next = new Set(prev);
                              if (!next.delete(key)) next.add(key);
                              return next;
                            });
                          }}
                        >
                          <ChevronDown size={16} />
                        </button>
                      </td>
                    ) : null}
                    {shown.map((c) => (
                      <td key={c.key} data-align={c.align}>
                        {c.cell(row)}
                      </td>
                    ))}
                  </tr>

                  {/* The rest of this row, not a row of its own: no cursor, no
                      `data-row-key`, and nothing the roving tabindex can land on. */}
                  {discloses && open ? (
                    <tr className="row-detail" data-row-detail={key}>
                      <td colSpan={span}>
                        <dl className="row-detail-list">
                          {dropped.map((c) => (
                            <div key={c.key}>
                              <dt>{c.header}</dt>
                              <dd>{c.cell(row)}</dd>
                            </div>
                          ))}
                        </dl>
                      </td>
                    </tr>
                  ) : null}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      </div>

      {/*
        Absolute, with the UTC offset, and outside the scroll container so it does
        not travel sideways with a wide table. `aria-live` is deliberately absent —
        this is a fact to be looked up, not an announcement to interrupt someone
        reading a row with.
      */}
      {hiddenCols.length > 0 ? (
        <p className="table-hidden-cols" data-role="hidden-columns">
          <span>{t("table.hiddenColumns", hiddenCols.length)}</span>
          <b>{hiddenCols.join(", ")}</b>
        </p>
      ) : null}

      {updatedAt ? (
        <p className="table-updated" data-stale={Boolean(error)}>
          <time dateTime={new Date(updatedAt).toISOString()}>
            {t("table.lastUpdated", absoluteTime(new Date(updatedAt).toISOString()) ?? "")}
          </time>
          {error ? <span className="table-updated-fail">{t("table.refreshFailed")}</span> : null}
        </p>
      ) : null}
    </div>
  );
}
