import { useCallback, useEffect, useMemo, useState } from "react";
import {
  EMPTY_SELECTION,
  clear,
  extend,
  reconcile,
  toggle,
  toggleAll,
  type Selection,
} from "./selection.js";

/**
 * `selection.ts` wired to a screen, once, so four screens cannot drift apart.
 *
 * The module below this one is a pure value on purpose and stays that way. What was
 * duplicated was the *wiring*: a piece of state, the reconcile effect, a `Set` for
 * the table, and four handlers — and every screen that copies that is a screen that
 * can forget the effect. The rule the whole thing exists for is one an operator never
 * sees working and only ever sees broken: **never act on rows you cannot see.**
 *
 * `keys` is the **selectable** keys, not every row's. A row that cannot be acted on
 * gets no checkbox (`TableSelection.selectable`), and passing the full set here would
 * let `Shift`-extend and select-all put it in the batch anyway — the checkbox gone
 * from the screen and the row still in the count.
 */
export interface SelectionHandle {
  selection: Selection;
  checked: ReadonlySet<string>;
  count: number;
  onToggle: (key: string, extendFromAnchor: boolean) => void;
  /**
   * `Shift`+arrow, where the span starts at the cursor rather than at the anchor.
   *
   * Separate from `onToggle(key, true)` because a shift-*click* has no cursor to
   * measure from and a shift-*arrow* has no anchor until the first one is set. Both
   * end in `extend`; only the origin differs.
   */
  onExtend: (from: string, to: string) => void;
  onToggleAll: () => void;
  clear: () => void;
  /**
   * Keep exactly these and drop the rest.
   *
   * How a partial failure ends: the rows that went through leave the selection, the
   * rows that did not stay in it, and the same press retries exactly them.
   */
  keep: (keys: readonly string[]) => void;
}

/**
 * @param scope  Everything that decides *which rows are on screen*, from
 *               `scopeToken`. Built at the call site rather than in here, because the
 *               list of inputs is the part a reader has to be able to check.
 * @param keys   The selectable row keys, in display order. Memoise it.
 * @param ready  False while the first load is in flight — an empty `keys` then is not
 *               evidence that the selected rows have gone.
 */
export function useSelection(
  scope: string,
  keys: readonly string[],
  ready: boolean,
): SelectionHandle {
  const [selection, setSelection] = useState<Selection>(EMPTY_SELECTION);

  useEffect(() => {
    if (!ready) return;
    setSelection((prev) => reconcile(prev, scope, keys));
  }, [scope, keys, ready]);

  const checked = useMemo(() => new Set(selection.keys), [selection.keys]);

  const onToggle = useCallback(
    (key: string, extendFromAnchor: boolean) =>
      setSelection((s) =>
        extendFromAnchor && s.anchor ? extend(s, keys, s.anchor, key) : toggle(s, key, keys),
      ),
    [keys],
  );
  const onExtend = useCallback(
    (from: string, to: string) => setSelection((s) => extend(s, keys, from, to)),
    [keys],
  );
  const onToggleAll = useCallback(() => setSelection((s) => toggleAll(s, keys)), [keys]);
  const clearAll = useCallback(() => setSelection((s) => clear(s)), []);
  const keep = useCallback(
    (kept: readonly string[]) =>
      setSelection((s) => ({
        scope: s.scope,
        keys: s.keys.filter((k) => kept.includes(k)),
        anchor: null,
      })),
    [],
  );

  return {
    selection,
    checked,
    count: selection.keys.length,
    onToggle,
    onExtend,
    onToggleAll,
    clear: clearAll,
    keep,
  };
}
