import { useCallback, type RefObject } from "react";
import { BINDINGS } from "./bindings.js";
import { useShortcut } from "./registry.js";
import { step } from "../list/detail.js";

/**
 * The keyboard layer for a list.
 *
 * Every binding here comes from the catalogue and is registered rather than
 * listened for, so the reference sheet lists exactly what this list supports —
 * including the bindings that are registered but momentarily inert, such as
 * *Close detail* while nothing is open.
 *
 * Focus follows the cursor. That is the decision the rest of the behaviour hangs
 * off: the cursor is not a second, invisible notion of "current row" that a screen
 * reader cannot see, it is which row has focus.
 */
export interface ListKeyboard {
  /** Put DOM focus on a row. The panel's Escape uses it to hand focus back. */
  focusRow: (key: string | null) => void;
}

export interface ListKeyboardOptions {
  /** Contains the rows. Each row carries `data-row-key`. */
  rootRef: RefObject<HTMLElement | null>;
  /** Row keys, in display order. */
  keys: readonly string[];
  cursor: string | null;
  onMove: (key: string) => void;
  onOpen: (key: string) => void;
  onClose: () => void;
  /** False when nothing is open — the binding stays listed, and does nothing. */
  canClose: boolean;
  onToggle: (key: string) => void;
  onExtend: (from: string, to: string) => void;
  onFocusSearch: () => void;
  onClearFilters: () => void;
  canClearFilters: boolean;
}

export function useListKeyboard(options: ListKeyboardOptions): ListKeyboard {
  const { rootRef, keys, cursor } = options;

  const focusRow = useCallback(
    (key: string | null) => {
      if (!key) return;
      const root = rootRef.current;
      const row = root?.querySelector<HTMLElement>(`[data-row-key="${CSS.escape(key)}"]`);
      row?.focus();
      // `nearest` rather than `center`: a row two pixels under the sticky header
      // should slide into view, and a row already visible should not move at all.
      row?.scrollIntoView({ block: "nearest" });
    },
    [rootRef],
  );

  /** Where a movement starts. With no cursor yet, the top of the list. */
  const move = (delta: 1 | -1) => {
    const next = step(keys, cursor, delta);
    if (!next) return;
    options.onMove(next);
    focusRow(next);
  };

  const extend = (delta: 1 | -1) => {
    const from = cursor ?? keys[0] ?? null;
    if (!from) return;
    const to = step(keys, cursor, delta);
    if (!to) return;
    options.onExtend(from, to);
    options.onMove(to);
    focusRow(to);
  };

  const hasRows = keys.length > 0;

  useShortcut(BINDINGS.listNext, () => move(1), hasRows);
  useShortcut(BINDINGS.listPrevious, () => move(-1), hasRows);
  useShortcut(BINDINGS.listExtendDown, () => extend(1), hasRows);
  useShortcut(BINDINGS.listExtendUp, () => extend(-1), hasRows);

  useShortcut(
    BINDINGS.listOpen,
    () => {
      const key = cursor ?? keys[0];
      if (key) options.onOpen(key);
    },
    hasRows,
  );

  useShortcut(
    BINDINGS.listToggle,
    () => {
      const key = cursor ?? keys[0];
      if (key) options.onToggle(key);
    },
    hasRows,
  );

  useShortcut(
    BINDINGS.detailClose,
    () => {
      options.onClose();
      // Focus and escape-to-blur are a pair: closing hands focus back to the row the
      // panel was about, never to the top of the document.
      focusRow(cursor);
    },
    options.canClose,
  );
  useShortcut(BINDINGS.focusSearch, () => options.onFocusSearch());
  useShortcut(BINDINGS.clearFilters, () => options.onClearFilters(), options.canClearFilters);

  return { focusRow };
}
