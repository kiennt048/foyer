/**
 * Bulk selection, as a pure value.
 *
 * The rule the whole module exists for: **an operator must never act on rows they
 * cannot see.** A selection made under one set of filters and carried into another
 * is exactly that failure — the batch bar keeps saying "12 selected" while the
 * twelve rows are off screen, and the operator acts on a set they are no longer
 * looking at.
 *
 * So a selection carries the scope it was made in. Change the filters, the sort or
 * the page and the scope token changes with them; the selection does not survive it.
 * That is enforced here rather than in the screen, because a screen that has to
 * remember to reset is a screen that will forget when the fourth filter is added.
 */
export interface Selection {
  /** Identifies the row set this selection was made against. */
  scope: string;
  /** Selected row keys, in the order the rows are displayed. */
  keys: readonly string[];
  /** Where a `Shift`-extend measures from. */
  anchor: string | null;
}

export const EMPTY_SELECTION: Selection = { scope: "", keys: [], anchor: null };

/**
 * The identity of a row set.
 *
 * Every input that changes *which rows are on screen* belongs in here — filters,
 * the site, the free-text query, and the page cursor once pagination exists.
 * Anything left out is a way for a stale selection to survive.
 */
export function scopeToken(parts: readonly (string | number | null | undefined)[]): string {
  return parts.map((p) => (p == null ? "" : String(p))).join("");
}

/**
 * Bring a selection up to date with what is on screen.
 *
 * Two rules, in order: a scope change empties it, and rows that have gone away are
 * dropped. The second matters because this table refetches every fifteen seconds —
 * a session that ends between two polls must not stay selected and countable.
 *
 * Returns the *same object* when nothing changed, so it is safe to feed straight
 * back into `setState` without looping.
 */
export function reconcile(
  selection: Selection,
  scope: string,
  visible: readonly string[],
): Selection {
  if (selection.scope !== scope) {
    return selection.keys.length === 0 && selection.anchor === null && selection.scope === scope
      ? selection
      : { scope, keys: [], anchor: null };
  }

  const present = new Set(visible);
  const keys = selection.keys.filter((k) => present.has(k));
  const anchor = selection.anchor && present.has(selection.anchor) ? selection.anchor : null;
  if (keys.length === selection.keys.length && anchor === selection.anchor) return selection;
  return { scope, keys, anchor };
}

/** Order a key set the way the rows are ordered, so "copy" reads down the screen. */
function ordered(keys: Iterable<string>, visible: readonly string[]): string[] {
  const set = new Set(keys);
  return visible.filter((k) => set.has(k));
}

export function toggle(selection: Selection, key: string, visible: readonly string[]): Selection {
  const has = selection.keys.includes(key);
  const next = has
    ? selection.keys.filter((k) => k !== key)
    : ordered([...selection.keys, key], visible);
  return { scope: selection.scope, keys: next, anchor: has ? selection.anchor : key };
}

/**
 * `Shift`+arrow: extend from the anchor to `to`, inclusive.
 *
 * With no anchor the extension starts where the cursor is, which is what makes
 * `Shift+↓` work as the first keystroke rather than needing an `X` first.
 */
export function extend(
  selection: Selection,
  visible: readonly string[],
  from: string,
  to: string,
): Selection {
  const anchor = selection.anchor ?? from;
  const a = visible.indexOf(anchor);
  const b = visible.indexOf(to);
  if (a === -1 || b === -1) return selection;
  const span = visible.slice(Math.min(a, b), Math.max(a, b) + 1);
  return {
    scope: selection.scope,
    keys: ordered([...selection.keys, ...span], visible),
    anchor,
  };
}

/** Select every visible row, or clear when they are all already selected. */
export function toggleAll(selection: Selection, visible: readonly string[]): Selection {
  const all = visible.length > 0 && visible.every((k) => selection.keys.includes(k));
  return {
    scope: selection.scope,
    keys: all ? [] : [...visible],
    anchor: null,
  };
}

export function clear(selection: Selection): Selection {
  if (selection.keys.length === 0 && selection.anchor === null) return selection;
  return { scope: selection.scope, keys: [], anchor: null };
}
