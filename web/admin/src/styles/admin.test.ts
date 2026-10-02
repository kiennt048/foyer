import { describe, expect, it } from "vitest";
/* The stylesheet as text, in the `?raw` form `gallery.test.tsx` and `layout.test.ts`
   already use. `vite.config.ts` sets `css: true` so this is the file rather than the
   empty string vitest stubs CSS to by default — without it every assertion here would
   pass vacuously, which is worse than failing. */
import CSS from "./admin.css?raw";

/**
 * Rules that were bought with a browser measurement, locked where the rule lives.
 *
 * `@foyer/admin` has no jsdom, so nothing in this package has layout: every number
 * below was taken against the running console with `getBoundingClientRect()`, and
 * none of them can be re-taken from here. What a text assertion *can* do is stop the
 * declaration being deleted by someone who reads it as tidying — which is exactly how
 * `.metric-foot` lost its height and `.notice-body` gained an opacity.
 *
 * The measurements themselves are written into `admin.css` beside each rule. If one
 * of these fails, the comment above the rule is what says what it cost.
 */

/** One rule's declarations, by exact selector, with comments stripped. */
function rule(selector: string, within = CSS): string {
  const plain = within.replace(/\/\*[\s\S]*?\*\//g, "");
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return new RegExp(`(?:^|[}{;])\\s*${escaped}\\s*\\{([^}]*)\\}`).exec(plain)?.[1] ?? "";
}

/** The `@media (max-width: 1100px)` block, which is where the two-column grid ends. */
const NARROW = (() => {
  const start = CSS.indexOf("@media (max-width: 1100px)");
  return start === -1 ? "" : CSS.slice(start, CSS.indexOf("\n}", CSS.indexOf(".portal-aside", start)) + 2);
})();

describe("the portal editor's preview is reachable", () => {
  it("finds the block this file is about", () => {
    // Guards every assertion below: a selector that stopped existing would make an
    // empty string satisfy every `not.toContain` here.
    expect(rule(".portal-aside"), "no sticky .portal-aside rule at all").toContain("position");
    expect(NARROW, "no narrow block containing .portal-aside").toContain(".portal-aside");
  });

  it("gives the sticky aside a scroll region of its own", () => {
    /*
     * Measured at 1440x900: the aside is 977px in a 900px viewport, so while it is
     * stuck at `top: 16px` its bottom 93px is past the fold — across scrollY 96 to
     * 768, which is 70% of the page's 955px of scroll. It comes back only in the last
     * 4%, and only by taking its own top 98px out of view instead. **There is no
     * scroll position at which the whole aside is visible**, and the aside is the
     * rendered portal the form is editing.
     *
     * With `max-height` and `overflow-y` the same twenty-sample sweep reports 0px
     * hidden above and 0px hidden below at every position from scrollY 96 on.
     */
    const sticky = rule(".portal-aside");
    expect(sticky).toContain("position: sticky");
    expect(sticky, "a sticky box taller than the viewport hides its own bottom").toContain(
      "max-height",
    );
    expect(sticky, "a max-height with no overflow rule clips the preview instead").toContain(
      "overflow-y",
    );
  });

  it("does not build a second scroller in the single-column layout", () => {
    /*
     * The other half, and it is not symmetry for its own sake: below 1100px the aside
     * is `position: static` and the page is the only scroller, so the rule above has
     * nothing to hold open. Measured at 390 without the reset, a 1015px aside sat
     * inside an 812px box — the bottom of the preview lost a second time, in the
     * layout where it was already furthest from the field it previews.
     */
    const narrow = rule(".portal-aside", NARROW);
    expect(narrow).toContain("position: static");
    expect(narrow, "the sticky scroll region survives into the single column").toContain(
      "max-height: none",
    );
    expect(narrow).toContain("overflow-y: visible");
  });
});
