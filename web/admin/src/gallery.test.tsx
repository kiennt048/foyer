import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { STATUS_LEVELS } from "./format.js";
import { problemCopy } from "./problems.js";
import { DesignGallery } from "./screens/Gallery.js";
/* The stylesheets as text. `?raw` rather than `node:fs`, because the admin has no
   `@types/node` and a test that made the package depend on one in order to read a
   file it sits next to would be paying for its own convenience. */
import CSS from "./styles/admin.css?raw";
import GALLERY_SOURCE from "./screens/Gallery.tsx?raw";
import TOKENS from "./styles/tokens.css?raw";

/**
 * The design-system gallery is the review surface *and* the regression surface, and
 * both of those claims are only true while it is complete.
 *
 * The failure this file exists to prevent is quiet: someone adds a ninth status
 * level, wires it into the alarm mapping, and the gallery — which is where the level
 * would have been looked at beside the other eight — simply does not show it. The
 * page still renders, still looks finished, and is now lying about the size of the
 * vocabulary. Every assertion below is a way for that to be loud instead.
 *
 * `renderToStaticMarkup` rather than a DOM: the gallery holds no data, makes no
 * request and reads nothing from the browser, which is a property worth keeping and
 * is exactly what makes a server render sufficient. Effects do not run, so what is
 * asserted is the first frame — which is also what a screenshot captures.
 */

const MARKUP = renderToStaticMarkup(<DesignGallery />);


describe("the gallery renders every status level the type declares", () => {
  it("renders something substantial at all", () => {
    // Guards every assertion below: a component that threw and rendered an empty
    // string would satisfy a `not.toContain` check and fail nothing.
    expect(MARKUP.length).toBeGreaterThan(20_000);
    expect(STATUS_LEVELS.length).toBe(8);
  });

  for (const level of STATUS_LEVELS) {
    it(`shows ${level} as a pill and as a notice, in both themes`, () => {
      /*
       * Counted, not merely present. The gallery renders one panel per theme, so a
       * level that appears once has been dropped from one of them — which is the
       * shape a "just add it to the light one" mistake takes, and the shape that a
       * single-theme screenshot cannot catch.
       */
      const pills = MARKUP.split(`class="pill" data-level="${level}"`).length - 1;
      expect(pills, `${level} is missing from a theme panel's pill row`).toBeGreaterThanOrEqual(2);

      const notices = MARKUP.split(`class="notice" data-level="${level}"`).length - 1;
      expect(notices, `${level} is missing from a theme panel's notice stack`).toBe(2);
    });

    it(`has colours and rules for ${level}`, () => {
      /*
       * The other half, and the half a render cannot see: a level with no tint paints
       * a transparent block with body text on it and looks like a rendering failure.
       * `positive` was in exactly that state — legal, compiled, used by nothing, and
       * broken — until all eight were put side by side here.
       */
      expect(TOKENS, `--st-${level}-bg is not defined`).toContain(`--st-${level}-bg:`);
      expect(TOKENS, `--st-${level}-fg is not defined`).toContain(`--st-${level}-fg:`);
      expect(CSS, `no pill rule for ${level}`).toContain(`.pill[data-level="${level}"]`);
      expect(CSS, `no notice rule for ${level}`).toContain(`.notice[data-level="${level}"]`);
    });
  }

  it("enumerates the levels rather than listing them", () => {
    /*
     * The rule that makes all of the above maintainable instead of another list to
     * keep in step: the pill row and the notice stack are both `STATUS_LEVELS.map`.
     * Individual levels do appear elsewhere in the file — a `positive` pill labelled
     * "Secret set" is an example of a level, not an enumeration of them — so this
     * checks the two places that must be generated, not the absence of the names.
     */
    expect(GALLERY_SOURCE.split("STATUS_LEVELS.map").length - 1).toBeGreaterThanOrEqual(2);
  });
});

describe("the gallery covers the states that are expensive to reach by hand", () => {
  it("renders all four table fetch states", () => {
    // Four different designs, and the difference is the point. Each is identified by
    // something only that state produces.
    expect(MARKUP).toContain('aria-busy="true"'); // 1 · loading
    // Computed from the catalogue rather than typed: a hand-written expectation here
    // passes for a gallery showing a sentence the product does not produce.
    expect(MARKUP).toContain(
      problemCopy({
        type: "/problems/controller-unreachable",
        params: { name: "UniFi — Cloud Key Gen2", reachability: "unreachable" },
      }).body,
    ); // 2 · error
    expect(MARKUP).toContain("No access points."); // 3 · never had data
    expect(MARKUP).toContain("Clear filters"); // 4 · filters exclude everything
  });

  it("renders the heartbeat in both of its states", () => {
    // The signal the whole freshness design rests on. Still and beating must both be
    // on the page or anyone reviewing the gallery is looking at half of it.
    expect(MARKUP).toContain('class="heartbeat" data-still="false"');
    expect(MARKUP).toContain('class="heartbeat" data-still="true"');
  });

  it("renders the qualifier segment, not a second hue", () => {
    expect(MARKUP).toContain("pill-seg pill-seg-qualifier");
  });

  it("renders both destructive tiers, and the high tier renders no Cancel", () => {
    expect(MARKUP).toContain("Regenerate secret");
    expect(MARKUP).toContain("Delete site");
    // Two `confirm`-tier dialogs per panel — the inline card and the modal — so four
    // Cancel buttons across two panels. A fifth would mean the typed-name tier grew
    // one, which is the thing that tier is defined by not having.
    expect(MARKUP.split(">Cancel<").length - 1).toBe(4);
  });

  it("renders every icon the module exports", async () => {
    // Enumerated from the namespace, so a new icon appears without anyone
    // remembering. This asserts the enumeration actually reached the markup.
    const icons = await import("./ui/icons.js");
    const names = Object.keys(icons);
    expect(names.length).toBeGreaterThan(15);
    for (const name of names) {
      expect(MARKUP, `${name} is exported but not drawn in the gallery`).toContain(
        `>${name}</span>`,
      );
    }
  });

  it("renders both themes, from one set of token values", () => {
    expect(MARKUP).toContain('data-scheme="light"');
    expect(MARKUP).toContain('data-scheme="dark"');
    // The mechanism, asserted where it would be quietly undone: a media query cannot
    // be scoped to a subtree, so re-introducing one here would mean a second copy of
    // every dark value, which is a second place to forget one. (Prose about the
    // media query is fine and there is plenty of it, so comments are stripped.)
    expect(TOKENS.replace(/\/\*[\s\S]*?\*\//g, "")).not.toContain("prefers-color-scheme");
    expect(TOKENS).toContain("light-dark(");
  });
});

describe("motion that survives prefers-reduced-motion is motion that means something", () => {
  const reduced = CSS.slice(CSS.indexOf("@media (prefers-reduced-motion: reduce)"));

  it("keeps the skeleton shimmering", () => {
    // A frozen shimmer is not "no motion", it is a hung page — on the one state whose
    // whole job is to say the request is still in flight.
    expect(reduced).toMatch(/\.skeleton\s*\{[^}]*animation-iteration-count:\s*infinite/);
  });

  it("keeps the heartbeat beating, without moving anything", () => {
    /*
     * The signal is "interim updates are arriving", and stopping it makes a pulsing
     * dot identical to a dead one — the preference would invert the meaning rather
     * than reduce it. The scale is what is dropped; the breathing is what is kept.
     */
    expect(reduced).toMatch(/\.heartbeat\s*\{[^}]*animation-name:\s*pulse-reduced/);
    expect(reduced).toMatch(/\.heartbeat\s*\{[^}]*animation-iteration-count:\s*infinite/);
    const frames = CSS.slice(CSS.indexOf("@keyframes pulse-reduced"));
    expect(frames.slice(0, frames.indexOf("}\n}") + 3)).not.toContain("transform");
  });

  it("leaves no transition in the stylesheet at all", () => {
    /*
     * The motion budget, stated as a number so it cannot drift.
     *
     * Hover reveals are the natural candidates for a transition — the nav shortcut
     * chip, the row tint, the row checkbox, the identifier cell's actions. Hover
     * answers exactly one question in a nine-column table (*which row is under my
     * cursor*), and an operator sweeps thirty rows in a second, so a 100–120ms fade
     * leaves five rows part-tinted and blurs the one thing the tint is for.
     *
     * The only `transition-*` left in the file is the reduced-motion reset, which
     * exists to switch transitions off. Adding a new one is a design argument, and
     * this is where it has to be made.
     */
    const withoutComments = CSS.replace(/\/\*[\s\S]*?\*\//g, "");
    expect(withoutComments).not.toMatch(/(^|[\s;{])transition\s*:/);
    expect(withoutComments.split("transition-duration").length - 1).toBe(1);
  });

  it("keeps exactly four animations, and each one carries information", () => {
    /*
     * The whole motion inventory, pinned. `pulse` and `shimmer` say something is
     * ongoing; `dock-in` and `batch-in` say where a fixed surface came from, which a
     * surface that simply appears over the page cannot. `pulse-reduced` is `pulse`
     * with the movement taken out. A fifth name here means something started moving
     * that has not been argued for.
     */
    const names = [...CSS.matchAll(/@keyframes\s+([\w-]+)/g)].map((m) => m[1]).sort();
    expect(names).toEqual(["batch-in", "dock-in", "pulse", "pulse-reduced", "shimmer"]);
  });
});
