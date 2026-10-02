import { describe, expect, it } from "vitest";
import { NARROW } from "./layout.js";
/* The stylesheet as text, `?raw`, exactly as `gallery.test.tsx` reads it — and it
   only works because `vite.config.ts` sets `test: { css: true }`. Stubbed to `""`,
   which is vitest's default, every assertion below would pass vacuously. */
import CSS from "./styles/admin.css?raw";

/**
 * The breakpoint is one string, and this is what stops it becoming two.
 *
 * The shell's narrow layout is half CSS and half JavaScript: the stylesheet turns
 * the sidebar into a header and the panel into a drawer, and `useNarrow` decides
 * whether the button that opens that drawer is rendered at all. Transcribing the
 * width into both is the sort of duplication that looks harmless because it is
 * *correct* at almost every width — a drawer whose CSS says 900 and whose JS says
 * 899 is a console that behaves perfectly except in a one-pixel band nobody resizes
 * to on purpose.
 *
 * Same mechanism as `STATUS_LEVELS` generating the gallery: one declaration, and a
 * test that fails when a second one appears.
 */

interface Query {
  prelude: string;
  body: string;
}

/** Every `@media` in the file, with its prelude and its braces-balanced body. */
const MEDIA: Query[] = [...CSS.matchAll(/@media([^{]+)\{/g)].map((match) => {
  const open = CSS.indexOf("{", match.index);
  let depth = 0;
  for (let i = open; i < CSS.length; i++) {
    if (CSS[i] === "{") depth++;
    else if (CSS[i] === "}" && --depth === 0) {
      return { prelude: (match[1] ?? "").trim(), body: CSS.slice(open, i + 1) };
    }
  }
  throw new Error(`unbalanced braces after @media${match[1]}`);
});

const containing = (selector: string) => MEDIA.filter((q) => q.body.includes(selector));

describe("the narrow breakpoint is declared once", () => {
  it("reads a stylesheet at all", () => {
    // Guards everything below. A `?raw` import that resolved to an empty string
    // would satisfy every `not.toContain` in this file and fail nothing.
    expect(CSS.length).toBeGreaterThan(20_000);
    expect(MEDIA.length).toBeGreaterThan(2);
  });

  it("is the query the drawer is defined under", () => {
    /*
     * Keyed on the drawer's own rule rather than on the text of the query, so the
     * test finds the block by what it *does*. Renaming the constant, or reformatting
     * the query, cannot make this pass by accident.
     */
    expect(containing('.sidebar-panel[data-open="true"]').map((q) => q.prelude)).toEqual([NARROW]);
    expect(containing(".menu-button").map((q) => q.prelude)).toEqual([NARROW]);
  });

  it("is written nowhere else in the stylesheet", () => {
    /*
     * `admin.css` has a second width query and it is deliberately not a failure:
     * `@media (max-width: 1100px)` collapses the portal-settings editor from two
     * columns to one. That is a question about one screen's form, asked at the width
     * that form stops fitting — nothing in JavaScript needs to know it, and forcing
     * it onto the shell's number would move a breakpoint for a reason that has
     * nothing to do with the screen it governs.
     *
     * What must not exist is a *second* spelling of the shell's own width. This is
     * the assertion that fails when someone writes `900px` into a new block instead
     * of extending this one, which is the transcription `layout.ts` exists to
     * prevent.
     */
    const width = NARROW.replace(/.*:\s*/, "").replace(/\).*/, ""); // "900px"
    const spellings = MEDIA.filter((q) => q.prelude.replace(/\s+/g, "").includes(width));
    expect(spellings.map((q) => q.prelude)).toEqual([NARROW]);
  });
});

describe("keyboard affordances hide on input capability, not on width", () => {
  const COARSE = "(hover: none) and (pointer: coarse)";

  it("asks whether there is a keyboard, in its own query", () => {
    expect(MEDIA.map((q) => q.prelude)).toContain(COARSE);
  });

  it("hides the shortcut hints and reveals the identifier actions there", () => {
    expect(containing(".kbd-hint-button").map((q) => q.prelude)).toEqual([COARSE]);
    expect(containing(".id-actions").map((q) => q.prelude)).toEqual([COARSE]);
  });

  it("does not answer the question with the width query's evidence", () => {
    /*
     * The assertion that carries the design's input-capability decision rather than
     * restating it. A
     * tablet at 880px with a keyboard attached must still be told its shortcuts
     * exist, and the cheapest way to break that is to move one declaration up into
     * the width block — where, on a phone, it would look identical.
     */
    const narrow = MEDIA.find((q) => q.prelude === NARROW);
    expect(narrow?.body).not.toContain(".kbd-hint");
    expect(narrow?.body).not.toContain(".id-actions");
  });
});
