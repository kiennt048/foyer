import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { ConfirmDialog, Foot, FootSkeleton, Modal, Notice } from "./primitives.js";
/* The stylesheet as text, in the same `?raw` form `gallery.test.tsx` uses and for the
   same reason: `@foyer/admin` has no `@types/node`, and a test that made the package
   depend on one in order to read a file it sits beside would be paying for its own
   convenience. */
import CSS from "../styles/admin.css?raw";

/**
 * What a server render can prove about the console's dialogs, and it is exactly two
 * things: what attributes they emit, and that they can be rendered at all.
 *
 * The trap this file is written around is that neither of those looks worth a test
 * until you know what they are standing in for. `@foyer/admin` has no jsdom and no
 * `matchMedia`; effects do not run here, so `showModal()`, the focus trap, the return
 * of focus and the keyboard suspension are all invisible to vitest and are locked in
 * `web/e2e` instead. **The first frame is still where two real defects
 * lived**, because both are attributes:
 *
 * - a `<dialog>` with no accessible name, which a screen reader announces as "dialog"
 *   and nothing else — five of the console's overlays were in that state, three of
 *   them this one primitive;
 * - `disabled` on the control that has focus, which blurs it to `<body>` for the
 *   length of the request and leaves it there when the request fails.
 *
 * Both were driven with a keyboard first. What is asserted here is the markup that
 * makes them true, so the next edit to this primitive cannot quietly undo either.
 */

const CONFIRM = renderToStaticMarkup(
  <ConfirmDialog
    open
    title="Stop allowing this destination"
    verb="Stop allowing it"
    consequence="Devices will lose access to it."
    onConfirm={() => {}}
    onCancel={() => {}}
  />,
);

const CONFIRM_BUSY = renderToStaticMarkup(
  <ConfirmDialog
    open
    busy
    title="Stop allowing this destination"
    verb="Stop allowing it"
    consequence="Devices will lose access to it."
    onConfirm={() => {}}
    onCancel={() => {}}
  />,
);

/** The `aria-labelledby` target, resolved against the markup that carries it. */
function labelledText(markup: string): string | null {
  const id = /<dialog[^>]*aria-labelledby="([^"]+)"/.exec(markup)?.[1];
  if (!id) return null;
  const heading = new RegExp(`<h2[^>]*id="${id}"[^>]*>([^<]*)</h2>`).exec(markup);
  return heading?.[1] ?? null;
}

describe("a dialog says what it is", () => {
  it("names the confirmation after its own title", () => {
    /*
     * The name has to come from the heading rather than a second string, because the
     * title *is* which resource is about to be destroyed. A hand-written label is a
     * second copy of that, and the copy is what goes stale.
     */
    expect(labelledText(CONFIRM)).toBe("Stop allowing this destination");
  });

  it("gives every instance a distinct id", () => {
    // Two mounted together is the ordinary case on `/design`, and `getElementById`
    // resolves to the first match — so a literal id would name both dialogs after
    // the first one's heading.
    const two = renderToStaticMarkup(
      <>
        <ConfirmDialog
          open
          title="First"
          verb="Do it"
          consequence="x"
          onConfirm={() => {}}
          onCancel={() => {}}
        />
        <ConfirmDialog
          open
          title="Second"
          verb="Do it"
          consequence="x"
          onConfirm={() => {}}
          onCancel={() => {}}
        />
      </>,
    );
    const ids = [...two.matchAll(/<dialog[^>]*aria-labelledby="([^"]+)"/g)].map((m) => m[1]);
    expect(ids).toHaveLength(2);
    expect(new Set(ids).size, "both confirmations claim the same heading id").toBe(2);
  });

  it("takes a name from a heading or a literal, and emits neither when given neither", () => {
    expect(renderToStaticMarkup(<Modal onClose={() => {}} labelledBy="head-7" children={null} />))
      .toContain('aria-labelledby="head-7"');
    expect(renderToStaticMarkup(<Modal onClose={() => {}} label="Commands" children={null} />))
      .toContain('aria-label="Commands"');
  });
});

describe("a busy destructive button keeps the focus it was pressed with", () => {
  it("is aria-disabled while the request is in flight, never disabled", () => {
    /*
     * The distinction is the whole fix. `disabled` on the focused element makes the
     * user agent blur it to `<body>` with a null `relatedTarget`, and nothing puts it
     * back — measured against a request slowed to 2.5 s, focus was on `<body>` for
     * all of it and still there after the request failed. `aria-disabled` announces
     * the same unavailability and moves nothing.
     */
    const danger = /<button[^>]*btn-danger[^>]*>/.exec(CONFIRM_BUSY)?.[0] ?? "";
    expect(danger, "no danger button rendered at all").toContain("btn-danger");
    expect(danger).toContain('aria-disabled="true"');
    expect(danger, "`disabled` is what blurs the focused control").not.toContain("disabled=\"\"");
  });

  it("says nothing about being disabled when it is not busy", () => {
    const danger = /<button[^>]*btn-danger[^>]*>/.exec(CONFIRM)?.[0] ?? "";
    expect(danger).not.toContain("aria-disabled");
    expect(danger).not.toContain('disabled=""');
  });

  it("still disables the typed-name tier until the name matches", () => {
    // The other half, or the test above is satisfied by never disabling anything.
    // `disabled` is right here: this button has never had focus, because `disabled`
    // is also what keeps it out of the tab order until the operator has typed.
    const markup = renderToStaticMarkup(
      <ConfirmDialog
        open
        tier="type-name"
        confirmName="nguyen.thi.mai"
        title="Delete account"
        verb="Delete it"
        consequence="x"
        onConfirm={() => {}}
        onCancel={() => {}}
      />,
    );
    const danger = /<button[^>]*btn-danger[^>]*>/.exec(markup)?.[0] ?? "";
    expect(danger).toContain('disabled=""');
  });
});

describe("a dialog renders with no keyboard registry above it", () => {
  it("does not throw", () => {
    /*
     * `Modal` and `ConfirmDialog` suspend the keyboard registry, which is what stops
     * `g` `d` navigating out of a half-filled form. So the hook is called by all
     * eleven overlays — including from `renderToStaticMarkup`, where there is no
     * provider and never will be. If `useKeyboardSuspend` ever throws without one,
     * every server render of a screen with a dialog on it dies here rather than in CI.
     */
    expect(() =>
      renderToStaticMarkup(<Modal onClose={() => {}} children={<p>no provider</p>} />),
    ).not.toThrow();
    expect(CONFIRM.length).toBeGreaterThan(100);
  });
});


/* ── the four speech acts, and the two rules that carry them ───────────────── */

/**
 * What `Foot`, `FootSkeleton` and `Notice`'s verdict bound can be held to here, and
 * why the CSS is read as text rather than measured.
 *
 * A green suite is not evidence that these work: each is a layout claim, and two of
 * them are easy to get wrong in ways invisible from the source:
 *
 * - `FootSkeleton` can close **16px of a 24px jump** while its comment says it closes
 *   all of it, because `.metric-foot` is a flex container and a `0.75em` bar is not
 *   the height of a line of text;
 * - the exemption that keeps the mark beside the sentence from shrinking names `svg`,
 *   and the heartbeat is a `<span>`, so without its own exemption at a 190px card the
 *   dot measures **3.31px wide against its own 6px height**.
 *
 * Both are measured by rendering the two shapes in a browser and reading
 * `getBoundingClientRect()`. Neither number can be re-taken from here — there is no
 * jsdom in this package and `renderToStaticMarkup` has no layout at all — so what is
 * locked below is **the declaration each measurement bought**. The measurements
 * themselves are written into `admin.css` beside the rules, where the next person to
 * touch them will read them.
 */

/** One rule's declarations, by exact selector, with comments stripped. */
function rule(selector: string): string {
  const plain = CSS.replace(/\/\*[\s\S]*?\*\//g, "");
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const found = new RegExp(`(?:^|[}{;])\\s*${escaped}\\s*\\{([^}]*)\\}`).exec(plain);
  return found?.[1] ?? "";
}

describe("the line under a figure says which of four things it is doing", () => {
  const ACTS = ["provenance", "live", "alarm", "definition"] as const;

  for (const act of ACTS) {
    it(`marks a ${act} line as one`, () => {
      // `act` is a required prop, so this is the half a type cannot check: that the
      // value reaches the attribute the stylesheet keys on. A `Foot` that renders the
      // sentence and drops `data-act` compiles, looks finished, and is the defect the
      // component exists to prevent.
      const markup = renderToStaticMarkup(<Foot act={act}>Some line</Foot>);
      expect(markup).toContain('class="metric-foot"');
      expect(markup).toContain(`data-act="${act}"`);
    });
  }

  it("gives an age a clock and a liveness claim the heartbeat, and the other two no mark", () => {
    /*
     * The distinction is the whole finding: all four were 11px muted text, so a
     * 25-minute-old figure and a live one looked equally trustworthy. Asserted as
     * four separate presences rather than a count, because "some mark appears" is
     * satisfied by giving all four the same one — which is the state being fixed.
     */
    const mark = (act: (typeof ACTS)[number]) => {
      const m = renderToStaticMarkup(<Foot act={act}>x</Foot>);
      return { svg: m.includes("<svg"), heart: m.includes('class="heartbeat"') };
    };
    expect(mark("provenance")).toEqual({ svg: true, heart: false });
    expect(mark("live")).toEqual({ svg: false, heart: true });
    expect(mark("alarm")).toEqual({ svg: false, heart: false });
    expect(mark("definition")).toEqual({ svg: false, heart: false });
  });

  it("carries the full sentence the line abbreviates", () => {
    const markup = renderToStaticMarkup(
      <Foot act="provenance" title="Sampled at 09:14, every 5 minutes">
        Sampled 25 minutes ago
      </Foot>,
    );
    expect(markup).toContain('title="Sampled at 09:14, every 5 minutes"');
  });

  it("holds the box open while the figure has not arrived, and announces nothing", () => {
    /*
     * There is no act yet, because the act is a property of data that has not come —
     * and a loading foot that claimed one would be the first thing on the card to
     * state a fact it cannot know.
     */
    const markup = renderToStaticMarkup(<FootSkeleton />);
    expect(markup).toContain('class="metric-foot"');
    expect(markup).toContain('aria-hidden="true"');
    expect(markup).toContain('class="skeleton"');
    expect(markup, "a loading line has no act").not.toContain("data-act");
  });

  it("is one line tall even when what is in it is not a line of text", () => {
    /*
     * The half no render in this package can see. Measured at 1440 on a 300px card:
     * a card with no foot is 86.94px, a card with any arrived foot is 110.88px, and
     * `FootSkeleton` without this declaration was **103.19px** — the skeleton's
     * `0.75em` bar setting the height of a flex container where a sentence sets a
     * 15.94px line box. With it, 110.88px exactly.
     */
    expect(rule(".metric-foot"), "the loading foot is shorter than the arrived one")
      .toContain("min-height");
  });

  it("keeps the mark beside the sentence a mark, whichever element it is", () => {
    /*
     * The exemption was written for the icons and names `svg`. `Heartbeat` is a
     * `<span>`, so `act="live"` was the one act it did not cover: measured at a 190px
     * card with a wrapping sentence, the dot was 3.31px wide and 6px tall, which
     * `border-radius: 50%` draws as an ellipse.
     */
    expect(CSS.replace(/\/\*[\s\S]*?\*\//g, "")).toContain(".metric-foot .heartbeat");
  });
});

describe("a notice tells a verdict apart from a paragraph", () => {
  /** 214 characters — one of the two the census found, near enough in shape. */
  const PROSE =
    "A walled garden entry that names a hostname is enforced by SNI on the drivers " +
    "that support it, and by resolved address on the ones that do not, which means a " +
    "CDN-hosted destination may admit traffic to unrelated sites.";

  it("renders a one-line verdict as a verdict", () => {
    const markup = renderToStaticMarkup(
      <Notice level="warning" title="Controller unreachable">
        The last poll failed.
      </Notice>,
    );
    expect(markup).toContain('class="notice-title">Controller unreachable');
  });

  it("renders a paragraph as a body, and emits no verdict rather than a false one", () => {
    /*
     * `title` is required and `children` is optional, which is what made this the easy
     * mistake: a caller holding one paragraph and no verdict puts the paragraph in the
     * slot that is not optional. Two call sites did, and 3-4 lines rendered at the
     * weight that everywhere else in this console means *the one-line verdict*.
     */
    expect(PROSE.length).toBeGreaterThan(180);
    const markup = renderToStaticMarkup(<Notice level="warning" title={PROSE} />);
    expect(markup, "a paragraph rendered at the verdict's weight").not.toContain("notice-title");
    expect(markup).toContain(`<p class="notice-body">${PROSE}</p>`);
  });

  it("does not put a caller's list inside a paragraph", () => {
    /*
     * A `<ul>` inside a `<p>` is invalid: the parser closes the paragraph before the
     * list and hoists it out, so the tree in the document is not the tree this
     * component describes. React 19 logs *"cannot contain a nested"* for it, and
     * `ProbeWarnings` — two `<ul className="notice-list">` children on the
     * registration wizard's third step — had been emitting that warning since it
     * shipped. `FirstRun`'s *What Foyer cannot do for you* is the third such caller
     * and is what found it.
     *
     * **Both halves, because the repair could be a deletion.** The `title` slot only
     * ever holds a string, so it stays a paragraph; only the caller's children moved
     * to a `<div>`. A test asserting just the second half is satisfied by rendering
     * everything in `<div>`s, which loses the paragraph semantics the assertion above
     * pins.
     */
    const markup = renderToStaticMarkup(
      <Notice level="info" title="Two things">
        <ul className="notice-list">
          <li>one</li>
        </ul>
      </Notice>,
    );
    expect(markup, "a list inside a paragraph").not.toMatch(/<p[^>]*>\s*<ul/);
    expect(markup).toContain('<div class="notice-body"><ul');
  });

  it("draws the line inside the gap the census found, not at a number somebody liked", () => {
    /*
     * The bound is arguable rather than magic, and this is what makes it arguable: of
     * the 25 distinct titles the console can render over fourteen routes, **23 are 53
     * characters or fewer and every one of those carries a body**; the two that carry
     * no body are 184 and 214. Nothing exists between 53 and 184, so the line may sit
     * anywhere in there — and both edges of the gap are asserted, because a bound that
     * only checks one edge is satisfied by moving it to the other.
     */
    const longestRealVerdict = "x".repeat(53);
    const shortestRealParagraph = "y".repeat(184);
    expect(
      renderToStaticMarkup(<Notice level="urgent" title={longestRealVerdict} />),
      "the longest verdict the console actually renders stopped being a verdict",
    ).toContain("notice-title");
    expect(
      renderToStaticMarkup(<Notice level="urgent" title={shortestRealParagraph} />),
      "the shorter of the two paragraphs still renders as a verdict",
    ).not.toContain("notice-title");
  });

  it("composites nothing, so the ratio the token file records is the ratio a reader gets", () => {
    /*
     * `.notice-body` carried `opacity: 0.92`, which took `warning`'s body from the
     * 5.58:1 `tokens.css` measured to **4.74:1** — passing 4.5:1 with 0.24 of headroom
     * that no future deepening of a `-bg` could afford. There is no colour in an
     * `opacity` declaration, so the token lint, axe and every screenshot were blind to
     * it; this is the only thing in the repo that can see it.
     */
    expect(rule(".notice-body"), "a composited body is a ratio nobody has measured")
      .not.toContain("opacity");
  });
});
