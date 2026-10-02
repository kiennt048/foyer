import { t } from "../i18n/index.js";

/**
 * The keyboard catalogue.
 *
 * **One declaration per shortcut, and everything reads from it** — the matcher that
 * decides whether a keypress fired, the caps drawn in the reference sheet, the row
 * in the command palette, and the hints printed under the table. A shortcut that
 * exists only inside a `keydown` handler is a shortcut nobody uses; a shortcut whose
 * documentation is written by hand beside it is a shortcut that is documented wrong
 * within a month. Both failures are removed by making this file the only source.
 *
 * Nothing here has a handler. Handlers are supplied at the point of use by
 * `useShortcut`, which is also what makes the sheet honest: it lists what the screen
 * you are on has actually registered, not what the product wishes it supported.
 */

/** A single keypress. `key` is canonical: single characters are lower-case. */
export interface Chord {
  key: string;
  /** ⌘ on macOS, Ctrl elsewhere. */
  mod?: true;
  shift?: true;
}

/** One or more chords pressed in order. `g` `d`, `f` `c`. */
export type Sequence = readonly Chord[];

export type BindingGroup = "global" | "navigation" | "list" | "detail" | "filter";

export interface BindingSpec {
  id: string;
  group: BindingGroup;
  label: string;
  /** Alternative ways to press it. `j` and `↓` are the same binding, not two. */
  sequences: readonly Sequence[];
  /** Offered in the command palette as well as the sheet. */
  command?: true;
}

/* ── matching ─────────────────────────────────────────────────────────────── */

/** The parts of a `KeyboardEvent` that decide a match. Plain data, so it is testable. */
export interface KeyEventLike {
  key: string;
  metaKey: boolean;
  ctrlKey: boolean;
  shiftKey: boolean;
  altKey: boolean;
}

/**
 * The chord a keypress represents.
 *
 * Single characters are lower-cased because a shifted `f` arrives as `F`: comparing
 * the raw value would make `Shift+F` match a binding declared as `F` and miss the
 * one declared as `shift + f`, which is the same class of bug as documenting a
 * shortcut by hand.
 */
export function chordOf(e: KeyEventLike): Chord {
  const key = e.key.length === 1 ? e.key.toLowerCase() : e.key;
  const chord: Chord = { key };
  if (e.metaKey || e.ctrlKey) chord.mod = true;
  if (e.shiftKey) chord.shift = true;
  return chord;
}

export function chordEquals(a: Chord, b: Chord): boolean {
  return (
    a.key === b.key && Boolean(a.mod) === Boolean(b.mod) && Boolean(a.shift) === Boolean(b.shift)
  );
}

export function sequenceEquals(a: Sequence, b: Sequence): boolean {
  return a.length === b.length && a.every((chord, i) => chordEquals(chord, b[i]!));
}

/** True when `pressed` is the opening of `seq` but not yet the whole of it. */
export function isSequencePrefix(seq: Sequence, pressed: readonly Chord[]): boolean {
  if (pressed.length >= seq.length) return false;
  return pressed.every((chord, i) => chordEquals(chord, seq[i]!));
}

/** The first binding whose sequence the pressed chords complete exactly. */
export function findExact<T extends { spec: BindingSpec }>(
  candidates: readonly T[],
  pressed: readonly Chord[],
): T | undefined {
  return candidates.find((c) => c.spec.sequences.some((seq) => sequenceEquals(seq, pressed)));
}

/** True when some binding is still waiting for another key. */
export function hasPending<T extends { spec: BindingSpec }>(
  candidates: readonly T[],
  pressed: readonly Chord[],
): boolean {
  return candidates.some((c) => c.spec.sequences.some((seq) => isSequencePrefix(seq, pressed)));
}

/* ── display ──────────────────────────────────────────────────────────────── */

const APPLE = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.userAgent);

/**
 * The caps to draw for one chord, in press order.
 *
 * Derived from the same `Chord` the matcher uses, so the sheet cannot drift from
 * the behaviour — the failure mode this whole module exists to remove.
 */
export function chordCaps(chord: Chord): string[] {
  const caps: string[] = [];
  if (chord.mod) caps.push(APPLE ? "⌘" : "Ctrl");
  if (chord.shift) caps.push(APPLE ? "⇧" : "Shift");
  caps.push(keyCap(chord.key));
  return caps;
}

export function keyCap(key: string): string {
  switch (key) {
    case " ":
      return t("keys.cap.space");
    case "Enter":
      return t("keys.cap.enter");
    case "Escape":
      return t("keys.cap.esc");
    case "ArrowUp":
      return "↑";
    case "ArrowDown":
      return "↓";
    case "ArrowLeft":
      return "←";
    case "ArrowRight":
      return "→";
    default:
      return key.length === 1 ? key.toUpperCase() : key;
  }
}

/* ── the catalogue ────────────────────────────────────────────────────────── */

const chord = (key: string, mods: { mod?: true; shift?: true } = {}): Chord => ({ key, ...mods });

export const BINDINGS = {
  commandPalette: {
    id: "command-palette",
    group: "global",
    get label() {
    return t("shell.commands");
  },
    sequences: [[chord("k", { mod: true })]],
  },
  shortcutSheet: {
    id: "shortcut-sheet",
    group: "global",
    get label() {
    return t("keys.sheet.title");
  },
    sequences: [[chord("/", { mod: true })]],
  },

  goDashboard: {
    id: "go-dashboard",
    group: "navigation",
    get label() {
    return t("nav.dashboard");
  },
    sequences: [[chord("g"), chord("d")]],
  },
  goSessions: {
    id: "go-sessions",
    group: "navigation",
    get label() {
    return t("nav.sessions");
  },
    sequences: [[chord("g"), chord("s")]],
  },
  goAccessPoints: {
    id: "go-access-points",
    group: "navigation",
    get label() {
    return t("nav.accessPoints");
  },
    sequences: [[chord("g"), chord("p")]],
  },
  goVouchers: {
    id: "go-vouchers",
    group: "navigation",
    get label() {
    return t("nav.vouchers");
  },
    sequences: [[chord("g"), chord("v")]],
  },
  goAccounts: {
    id: "go-accounts",
    group: "navigation",
    get label() {
    return t("nav.accounts");
  },
    sequences: [[chord("g"), chord("a")]],
  },
  goControllers: {
    id: "go-controllers",
    group: "navigation",
    get label() {
    return t("nav.controllers");
  },
    sequences: [[chord("g"), chord("c")]],
  },
  goPortal: {
    id: "go-portal",
    group: "navigation",
    get label() {
    return t("nav.portal");
  },
    sequences: [[chord("g"), chord("o")]],
  },
  goAttempts: {
    id: "go-attempts",
    group: "navigation",
    get label() {
    return t("nav.attempts");
  },
    // `g t` — for *attempts*. `g a` is taken by Accounts and the mnemonic an
    // operator reaches for under pressure is the last letter they remember.
    sequences: [[chord("g"), chord("t")]],
  },
  goGarden: {
    id: "go-garden",
    group: "navigation",
    get label() {
    return t("nav.garden");
  },
    sequences: [[chord("g"), chord("w")]],
  },
  goOperators: {
    id: "go-operators",
    group: "navigation",
    get label() {
    return t("nav.operators");
  },
    // `g u` — the people who *use* this console. `g o` is Portal and `g p` is Access
    // Points, so the noun's own first two letters are both spoken for, and the letter
    // that survives is the one naming the thing rather than spelling it.
    sequences: [[chord("g"), chord("u")]],
  },
  goAudit: {
    id: "go-audit",
    group: "navigation",
    get label() {
    return t("nav.audit");
  },
    // `g l` — the *log*. `g a` is Accounts.
    sequences: [[chord("g"), chord("l")]],
  },
  goGuestData: {
    id: "go-guest-data",
    group: "navigation",
    get label() {
    return t("nav.guestData");
  },
    // `g m` — *marketing*. `g g` would read as a stutter, and `g d` is the dashboard.
    sequences: [[chord("g"), chord("m")]],
  },
  goPrivacy: {
    id: "go-privacy",
    group: "navigation",
    get label() {
    return t("nav.privacy");
  },
    // `g r` — *retention*. `g p` is Access Points.
    sequences: [[chord("g"), chord("r")]],
  },
  goNetwork: {
    id: "go-network",
    group: "navigation",
    get label() {
    return t("nav.network");
  },
    sequences: [[chord("g"), chord("n")]],
  },

  listNext: {
    id: "list-next",
    group: "list",
    get label() {
    return t("keys.listNext");
  },
    sequences: [[chord("j")], [chord("ArrowDown")]],
  },
  listPrevious: {
    id: "list-previous",
    group: "list",
    get label() {
    return t("keys.listPrevious");
  },
    sequences: [[chord("k")], [chord("ArrowUp")]],
  },
  listOpen: {
    id: "list-open",
    group: "list",
    get label() {
    return t("keys.listOpen");
  },
    sequences: [[chord("Enter")], [chord(" ")]],
  },
  listToggle: {
    id: "list-toggle",
    group: "list",
    get label() {
    return t("keys.listToggle");
  },
    sequences: [[chord("x")]],
  },
  listExtendDown: {
    id: "list-extend-down",
    group: "list",
    get label() {
    return t("keys.listExtendDown");
  },
    sequences: [[chord("ArrowDown", { shift: true })]],
  },
  listExtendUp: {
    id: "list-extend-up",
    group: "list",
    get label() {
    return t("keys.listExtendUp");
  },
    sequences: [[chord("ArrowUp", { shift: true })]],
  },

  detailClose: {
    id: "detail-close",
    group: "detail",
    get label() {
    return t("keys.detailClose");
  },
    sequences: [[chord("Escape")]],
  },

  focusSearch: {
    id: "focus-search",
    group: "filter",
    get label() {
    return t("keys.focusSearch");
  },
    sequences: [[chord("f", { shift: true })]],
  },
  clearFilters: {
    id: "clear-filters",
    group: "filter",
    get label() {
    return t("table.clearFilters");
  },
    sequences: [[chord("f"), chord("c")]],
  },
} as const satisfies Record<string, BindingSpec>;

/** Every shortcut the product declares. The sheet lists a subset — what is live. */
export const ALL_BINDINGS: readonly BindingSpec[] = Object.values(BINDINGS);

/* ── the reference sheet's model ──────────────────────────────────────────── */

/** Groups in reading order: what works everywhere, then what works here. */
const GROUP_ORDER: BindingGroup[] = ["global", "navigation", "list", "detail", "filter"];

const GROUP_LABEL: Record<BindingGroup, string> = {
  get global() {
    return t("keys.group.global");
  },
  get navigation() {
    return t("keys.group.navigation");
  },
  get list() {
    return t("keys.group.list");
  },
  get detail() {
    return t("keys.group.detail");
  },
  get filter() {
    return t("keys.group.filter");
  },
};

export interface ShortcutSection {
  group: BindingGroup;
  label: string;
  items: BindingSpec[];
}

/**
 * The sheet, generated from whatever is registered.
 *
 * Written as a pure function of the binding list precisely so the sheet cannot be
 * hand-maintained: there is nowhere to write a row that no handler backs, and no way
 * to register a handler the sheet will not show.
 */
export function shortcutSections(specs: readonly BindingSpec[]): ShortcutSection[] {
  const seen = new Set<string>();
  const unique = specs.filter((s) => (seen.has(s.id) ? false : (seen.add(s.id), true)));
  return GROUP_ORDER.map((group) => ({
    group,
    label: GROUP_LABEL[group],
    items: unique.filter((s) => s.group === group),
  })).filter((section) => section.items.length > 0);
}

/** Searchable by what it does *and* by the keys it uses — `esc` finds Close detail. */
export function shortcutMatches(spec: BindingSpec, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (!q) return true;
  if (spec.label.toLowerCase().includes(q)) return true;
  return spec.sequences.some((seq) =>
    seq
      .flatMap(chordCaps)
      .join(" ")
      .toLowerCase()
      .includes(q),
  );
}

export function filterSections(sections: ShortcutSection[], query: string): ShortcutSection[] {
  return sections
    .map((s) => ({ ...s, items: s.items.filter((i) => shortcutMatches(i, query)) }))
    .filter((s) => s.items.length > 0);
}
