import { describe, expect, it } from "vitest";
import {
  ALL_BINDINGS,
  BINDINGS,
  chordCaps,
  chordOf,
  filterSections,
  findExact,
  hasPending,
  shortcutSections,
  type BindingSpec,
  type Chord,
  type KeyEventLike,
} from "./keys/bindings.js";
import { CLOSED, panelIsOpen, panelReducer, step } from "./list/detail.js";
import {
  EMPTY_SELECTION,
  extend,
  reconcile,
  scopeToken,
  toggle,
  toggleAll,
} from "./list/selection.js";

/**
 * The named tests for list interaction. Each guards a rule that is stated in the
 * design and is invisible once broken — a selection that survives a filter change
 * looks fine on screen right up to the moment someone acts on it.
 */

/* ── bulk selection ───────────────────────────────────────────────────────── */

const ROWS = ["a", "b", "c", "d"];

describe("selection can never hold a row the operator cannot see", () => {
  const scopeA = scopeToken(["site-hcm", "online", "", ""]);
  const scopeB = scopeToken(["site-hcm", "online,ended", "", ""]);

  it("resets when filters change", () => {
    // The rule bulk actions are not safe without. A selection made under one filter
    // and carried into another is a batch bar counting rows that are off screen.
    const picked = toggle(toggle({ ...EMPTY_SELECTION, scope: scopeA }, "a", ROWS), "c", ROWS);
    expect(picked.keys).toEqual(["a", "c"]);

    const afterFilterChange = reconcile(picked, scopeB, ROWS);
    expect(afterFilterChange.keys).toEqual([]);
    expect(afterFilterChange.scope).toBe(scopeB);
  });

  it("resets when the site, the query or the pinned set changes", () => {
    // Every input that decides which rows are on screen is part of the scope. If one
    // is ever left out, this is the test that says so.
    const picked = toggle({ ...EMPTY_SELECTION, scope: scopeA }, "a", ROWS);
    const variants = [
      scopeToken(["site-hanoi", "online", "", ""]),
      scopeToken(["site-hcm", "online", "iphone", ""]),
      scopeToken(["site-hcm", "online", "", "3c:22:fb:06:0b:e5"]),
    ];
    for (const scope of variants) {
      expect(reconcile(picked, scope, ROWS).keys, scope).toEqual([]);
    }
  });

  it("drops rows that have gone away under a refetch", () => {
    // The list polls every fifteen seconds. A session that ends between two polls
    // must not stay selected and countable.
    const picked = toggle(toggle({ ...EMPTY_SELECTION, scope: scopeA }, "a", ROWS), "d", ROWS);
    const next = reconcile(picked, scopeA, ["a", "b", "c"]);
    expect(next.keys).toEqual(["a"]);
  });

  it("returns the same object when nothing changed", () => {
    // So the result can be fed straight back into setState without looping.
    const picked = toggle({ ...EMPTY_SELECTION, scope: scopeA }, "b", ROWS);
    expect(reconcile(picked, scopeA, ROWS)).toBe(picked);
  });

  it("keeps selected keys in display order, not click order", () => {
    const picked = toggle(toggle({ ...EMPTY_SELECTION, scope: scopeA }, "d", ROWS), "b", ROWS);
    expect(picked.keys).toEqual(["b", "d"]);
  });

  it("extends from the anchor in both directions", () => {
    const anchored = toggle({ ...EMPTY_SELECTION, scope: scopeA }, "c", ROWS);
    expect(extend(anchored, ROWS, "c", "a").keys).toEqual(["a", "b", "c"]);
    expect(extend(anchored, ROWS, "c", "d").keys).toEqual(["c", "d"]);
  });

  it("select-all covers exactly the visible rows, and toggles off", () => {
    const all = toggleAll({ ...EMPTY_SELECTION, scope: scopeA }, ROWS);
    expect(all.keys).toEqual(ROWS);
    expect(toggleAll(all, ROWS).keys).toEqual([]);
  });
});

/* ── the detail panel ─────────────────────────────────────────────────────── */

describe("the detail panel", () => {
  it("is closed on load and opens on a selection", () => {
    expect(panelIsOpen(CLOSED)).toBe(false);
    expect(panelIsOpen(panelReducer(CLOSED, { type: "open", key: "a" }))).toBe(true);
  });

  it("stays closed once dismissed, however much the selection changes", () => {
    // The operator has said they do not want it. Re-opening on the next arrow key
    // overrules them, and is what makes an otherwise good panel feel like it is
    // fighting you.
    let state = panelReducer(CLOSED, { type: "open", key: "a" });
    state = panelReducer(state, { type: "dismiss" });
    expect(panelIsOpen(state)).toBe(false);

    for (const key of ["b", "c", "d"]) {
      state = panelReducer(state, { type: "move", key });
      expect(panelIsOpen(state), `moving to ${key} re-opened a dismissed panel`).toBe(false);
      expect(state.cursor).toBe(key);
    }
  });

  it("re-opens only on a deliberate open", () => {
    let state = panelReducer(panelReducer(CLOSED, { type: "open", key: "a" }), { type: "dismiss" });
    state = panelReducer(state, { type: "move", key: "b" });
    expect(panelIsOpen(state)).toBe(false);
    state = panelReducer(state, { type: "open", key: "b" });
    expect(panelIsOpen(state)).toBe(true);
  });

  it("records whether the open was asked for, which is what decides focus", () => {
    /*
     * The panel focuses itself when it opens, and that is right for `Enter` and
     * wrong for `J` — measured, the first `ArrowDown` moved focus off the list and
     * the second moved it back. The distinction this file already draws between a
     * move and an open is the one the panel needs; it simply was not carried.
     */
    expect(panelReducer(CLOSED, { type: "move", key: "a" }).deliberate).toBe(false);
    expect(panelReducer(CLOSED, { type: "open", key: "a" }).deliberate).toBe(true);

    /* And it goes back. Without this, every arrow key after one `Enter` would take
       focus off the list again — the bug, one keystroke later. */
    const opened = panelReducer(CLOSED, { type: "open", key: "a" });
    expect(panelReducer(opened, { type: "move", key: "b" }).deliberate).toBe(false);

    /* Dismissal is `Escape`, not a change of mind about what opened the panel. */
    expect(panelReducer(opened, { type: "dismiss" }).deliberate).toBe(true);
  });

  it("holds the cursor at the ends rather than wrapping", () => {
    // Wrapping from the last row to the first is how an operator loses their place
    // in a list they are working down.
    expect(step(ROWS, "d", 1)).toBe("d");
    expect(step(ROWS, "a", -1)).toBe("a");
    expect(step(ROWS, null, 1)).toBe("a");
    // A cursor on a row that has gone away restarts at the top rather than vanishing.
    expect(step(ROWS, "gone", 1)).toBe("a");
    expect(step([], "a", 1)).toBeNull();
  });
});

/* ── the keyboard registry ────────────────────────────────────────────────── */

const press = (key: string, mods: Partial<KeyEventLike> = {}): Chord =>
  chordOf({ key, metaKey: false, ctrlKey: false, shiftKey: false, altKey: false, ...mods });

const registered = (specs: readonly BindingSpec[]) =>
  specs.map((spec) => ({ spec, enabled: true, run: () => {} }));

describe("the shortcut reference is generated from the registry", () => {
  it("lists every registered binding", () => {
    // The failure this replaces: a hand-written sheet that drifts from the handlers.
    // Nothing may be registered that the sheet omits.
    const sections = shortcutSections(ALL_BINDINGS);
    const listed = sections.flatMap((s) => s.items.map((i) => i.id));
    expect(listed.sort()).toEqual(ALL_BINDINGS.map((b) => b.id).sort());
  });

  it("lists only what is registered, so it shrinks with the screen", () => {
    const listSpecs = ALL_BINDINGS.filter((b) => b.group === "list");
    const listed = shortcutSections(listSpecs).flatMap((s) => s.items.map((i) => i.id));
    expect(listed.sort()).toEqual(listSpecs.map((b) => b.id).sort());
    expect(listed).not.toContain(BINDINGS.goDashboard.id);
  });

  it("gives every binding a label and at least one drawable chord", () => {
    for (const spec of ALL_BINDINGS) {
      expect(spec.label.trim(), spec.id).not.toBe("");
      expect(spec.sequences.length, spec.id).toBeGreaterThan(0);
      for (const sequence of spec.sequences) {
        expect(sequence.length, spec.id).toBeGreaterThan(0);
        for (const chord of sequence) {
          expect(chordCaps(chord).join(""), spec.id).not.toBe("");
        }
      }
    }
  });

  it("draws the chord it actually matches", () => {
    /*
     * The whole reason the catalogue exists. A sheet that can disagree with the
     * bindings is worse than no sheet, so this synthesises a keypress from each
     * declared sequence and asserts the matcher fires on it — one declaration
     * driving both the caps and the behaviour.
     */
    const all = registered(ALL_BINDINGS);
    for (const spec of ALL_BINDINGS) {
      for (const sequence of spec.sequences) {
        const pressed = sequence.map((c) =>
          press(c.key, { metaKey: Boolean(c.mod), shiftKey: Boolean(c.shift) }),
        );
        // A multi-key sequence must be pending after its first key and only then fire.
        for (let i = 1; i < pressed.length; i++) {
          const prefix = pressed.slice(0, i);
          expect(findExact(all, prefix), `${spec.id} fired early`).toBeUndefined();
          expect(hasPending(all, prefix), `${spec.id} lost its prefix`).toBe(true);
        }
        expect(findExact(all, pressed)?.spec.id, spec.id).toBe(spec.id);
      }
    }
  });

  it("does not confuse a shifted letter with the unshifted one", () => {
    // `Shift+F` arrives as `F`. Comparing the raw value would match the wrong
    // binding and miss the right one.
    const all = registered(ALL_BINDINGS);
    expect(findExact(all, [press("F", { shiftKey: true })])?.spec.id).toBe(
      BINDINGS.focusSearch.id,
    );
    expect(findExact(all, [press("f")])).toBeUndefined();
    expect(hasPending(all, [press("f")]), "F must still open the F-C sequence").toBe(true);
  });

  it("is searchable by label and by key", () => {
    const sections = shortcutSections(ALL_BINDINGS);
    const ids = (q: string) =>
      filterSections(sections, q).flatMap((s) => s.items.map((i) => i.id));

    expect(ids("close")).toContain(BINDINGS.detailClose.id);
    // Searching by the cap an operator half-remembers pressing.
    expect(ids("esc")).toContain(BINDINGS.detailClose.id);
    expect(ids("nothing here matches")).toEqual([]);
    expect(ids("")).toEqual(ALL_BINDINGS.map((b) => b.id));
  });

  it("has a handler for every binding it declares", () => {
    /*
     * The other direction, and the one that keeps the sheet honest: a binding in the
     * catalogue that nothing registers would be printed on screen as a key that does
     * nothing. Handlers are attached by name through `useShortcut`, so the source is
     * where that is checked.
     */
    const sources = import.meta.glob("./**/*.{ts,tsx}", {
      query: "?raw",
      eager: true,
      import: "default",
    }) as Record<string, string>;

    const body = Object.entries(sources)
      .filter(([path]) => !path.endsWith(".test.ts"))
      .map(([, text]) => text)
      .join("\n");

    expect(body.length).toBeGreaterThan(1000);

    for (const name of Object.keys(BINDINGS)) {
      expect(
        body.includes(`useShortcut(BINDINGS.${name}`) || body.includes(`useShortcut(\n    BINDINGS.${name}`),
        `BINDINGS.${name} is declared and drawn but nothing registers a handler for it`,
      ).toBe(true);
    }
  });

  it("declares no two bindings on the same sequence", () => {
    // Two handlers on one chord means one of them silently never runs.
    const seen = new Map<string, string>();
    for (const spec of ALL_BINDINGS) {
      for (const sequence of spec.sequences) {
        const key = sequence.map((c) => `${c.mod ? "M" : ""}${c.shift ? "S" : ""}${c.key}`).join(">");
        expect(seen.get(key), `${key} is claimed by ${seen.get(key)} and ${spec.id}`).toBeUndefined();
        seen.set(key, spec.id);
      }
    }
  });
});
