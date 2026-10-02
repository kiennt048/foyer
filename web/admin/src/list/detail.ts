/**
 * Whether the detail panel is open — as a state machine, not a boolean nobody owns.
 *
 * The rule that is easy to state and easy to get wrong: the panel opens when a row
 * is selected, and **once the operator closes it, it stays closed as the selection
 * keeps changing.** They have told you they do not want it; re-opening it on the
 * next arrow key overrules them, and it is precisely the behaviour that makes an
 * otherwise good panel feel like it is fighting you.
 *
 * So dismissal is sticky, and only a deliberate gesture clears it. Moving the cursor
 * is not deliberate — it happens on every `J`. Pressing `Enter`, or clicking a row,
 * is.
 */
export interface PanelState {
  /** The row the panel is about. Mirrored into the URL, so a session is a link. */
  cursor: string | null;
  /** Set by the operator closing the panel. Cleared only by an explicit open. */
  dismissed: boolean;
  /**
   * Whether the open the panel is currently showing was *asked for*.
   *
   * The panel focuses itself when it opens, and the reason is good — the first thing
   * a screen reader should read there is the record, not the way out of it. But
   * `panelIsOpen` is true as soon as the cursor moves, so the first `ArrowDown` on a
   * list threw focus out of the list being traversed and the second one brought it
   * back: measured, and the pair reads as a broken screen.
   *
   * Both halves were already right and only the *distinction between them* was not
   * reaching the panel. It is the same one this file already draws two paragraphs
   * above — `move` is not deliberate, `open` is — carried rather than reinvented.
   */
  deliberate: boolean;
}

export type PanelAction =
  /** The cursor moved — arrow keys, `J`/`K`, or a row disappearing under a refetch. */
  | { type: "move"; key: string | null }
  /** A deliberate open: `Enter`, `Space`, or a click on the row. */
  | { type: "open"; key: string }
  /** The operator closed it: `Esc`, or the close button. */
  | { type: "dismiss" };

export const CLOSED: PanelState = { cursor: null, dismissed: false, deliberate: false };

/**
 * Open when there is something to show and the operator has not said otherwise.
 *
 * Note what this gives for free: with no selection at all the panel is closed, which
 * is the page-load state, and a link carrying a selection opens it — the same rule,
 * not two.
 */
export function panelIsOpen(state: PanelState): boolean {
  return state.cursor !== null && !state.dismissed;
}

export function panelReducer(state: PanelState, action: PanelAction): PanelState {
  switch (action.type) {
    case "move":
      // Selection changes do not resurrect a dismissed panel, and they do not make
      // an open a deliberate one — this is the `J` that happens fifty times a minute.
      if (state.cursor === action.key) return state;
      return { cursor: action.key, dismissed: state.dismissed, deliberate: false };
    case "open":
      return { cursor: action.key, dismissed: false, deliberate: true };
    case "dismiss":
      if (state.dismissed) return state;
      return { ...state, dismissed: true };
    default:
      return state;
  }
}

/** The row after `key`, staying put at the end rather than wrapping. */
export function step(visible: readonly string[], key: string | null, delta: 1 | -1): string | null {
  if (visible.length === 0) return null;
  if (key === null) return delta === 1 ? visible[0]! : visible[visible.length - 1]!;
  const i = visible.indexOf(key);
  // A cursor on a row that has gone away restarts at the top rather than vanishing.
  if (i === -1) return visible[0]!;
  const next = i + delta;
  if (next < 0 || next >= visible.length) return key;
  return visible[next]!;
}
