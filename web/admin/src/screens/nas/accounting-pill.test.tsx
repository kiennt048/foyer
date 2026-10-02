import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Nas } from "@foyer/contract";
import { t } from "../../i18n/index.js";
import { AccountingPill } from "./bits.js";

/**
 * **The unmeasured accounting state reaching the screen.** `accountingConfigured`
 * has three states and this column rendered two.
 *
 * The record's own definition is the authority: the property is optional in
 * `Nas`, and `store.RadiusClient.AccountingConfigured` is a `*bool` whose comment
 * says *"Nil is not measured"* and, in as many words, that **the console may not
 * render it as missing**. A pill of `n.accountingConfigured ? configured : missing`
 * would report a device nobody had probed as one from which no session record will
 * ever arrive — a measurement produced by the absence of one, on the
 * screen whose premise is that these things are measured rather than assumed.
 *
 * The two are different afternoons: *Not attached* sends an operator to the
 * controller to add an accounting profile, *Not measured* sends them to the Probe
 * button. That is the whole cost of collapsing them.
 *
 * ── WHAT RED LOOKS LIKE ─────────────────────────────────────────────────────
 *
 * `AccountingPill` in a two-branch form — `nas
 * .accountingConfigured ? <positive/> : <caution-minor Not attached/>`:
 *
 *   × an unprobed device is not reported as one that will never account
 *       → a device nothing has probed is reported "Not attached":
 *         expected 'Not attached' not to contain 'Not attached'
 *   × the sentence is the pill's own text, not a tooltip nobody can read
 *       → Not measured is not the pill's text: expected 'Not attached' to
 *         contain 'Not measured'
 *
 * The two measured cases stay green throughout, which is the half that says the
 * answer is a third branch rather than a softened second one.
 *
 * And the call-site guard, against `NasDetail.tsx`'s notice written as
 * `{!nas.accountingConfigured ? (`:
 *
 *   × no screen asks this three-valued field a two-valued question
 *       → expected [ "./NasDetail.tsx: {!nas.accountingConfigured ? (" ] to
 *         deeply equal []
 *
 * Its **derived** half, with `AccessPoints.tsx` rendering an inline
 * `n.accountingConfigured ? … : …` and the `AccountingPill` import dropped — the
 * regression a hand-written file list would stand for:
 *
 *   × no screen asks this three-valued field a two-valued question
 *       → no scanned screen delegates to `AccountingPill` — either the glob no
 *         longer reaches the screens, or a screen has gone back to rendering
 *         this field itself: expected false to be true
 *
 * ── AND WHY IT ASSERTS ON THE TEXT AND ON THE ATTRIBUTE ─────────────────────
 *
 * `docs/rules/testing.md` records a green-either-way test on a panel very like
 * this one: the sentence lived in a `title` attribute, a whole-page `toContainText`
 * could neither find it nor fail to find it, and *fixing the defect changed nothing
 * about the result*. `Pill` takes a `title`, so that failure is available here by
 * construction. Every case below therefore asserts the label as the element's own
 * text **and** forbids a `title` from being the only place it appears.
 */

const device = (over: Partial<Nas> = {}): Nas =>
  ({
    id: "nas-under-test",
    siteId: "site-hcm",
    name: "Under test",
    driver: "mikrotik",
    source: "manual",
    capabilities: { walledGarden: "none" },
    reachability: "unknown",
    enabled: true,
    secretSet: true,
    messageAuthenticator: "unknown",
    ...over,
  }) as Nas;

const markup = (nas: Nas) => renderToStaticMarkup(<AccountingPill nas={nas} />);

/** The markup as an operator reads it: tags out, and with them every attribute. */
const reads = (nas: Nas) =>
  markup(nas)
    .replace(/<[^>]+>/g, " ")
    .replace(/&#x27;/g, "'")
    .replace(/&quot;/g, '"')
    .replace(/&amp;/g, "&");

/** What a `title` attribute carries, which is exactly what `reads` cannot see. */
const titles = (nas: Nas) => [...markup(nas).matchAll(/title="([^"]*)"/g)].map((m) => m[1] ?? "");

describe("accounting is rendered in the three states the record has", () => {
  it("an unprobed device is not reported as one that will never account", () => {
    const out = reads(device({ accountingConfigured: undefined }));

    expect(out, 'a device nothing has probed is reported "Not attached"').not.toContain(
      t("nas.accounting.missing"),
    );
    expect(out, "an unmeasured device is claimed to be accounting").not.toContain(
      t("nas.accounting.configured"),
    );
  });

  it("the sentence is the pill's own text, not a tooltip nobody can read", () => {
    const nas = device({ accountingConfigured: undefined });
    expect(reads(nas), "Not measured is not the pill's text").toContain(
      t("nas.accounting.notMeasured"),
    );
    /* The trap, closed from the other side. A label that reached the screen only as
       a `title` would satisfy a whole-markup `toContain` and be invisible to a
       keyboard, a screen reader and a screenshot. */
    expect(titles(nas), "the label is a tooltip rather than the pill").not.toContain(
      t("nas.accounting.notMeasured"),
    );
    /* The explanation may be a `title` — it is the one thing a tooltip is for — but
       it must not be doing the label's job. */
    expect(titles(nas)).toContain(t("nas.accounting.notMeasuredWhy"));
  });

  it("still says what it means when something did measure it", () => {
    /*
     * The half that keeps the repair from being "say less". `false` is a real
     * measurement and the most consequential one on this screen: every session
     * figure for the device is absent forever. It has to keep arriving, at a level
     * that is not green.
     */
    const silent = device({ accountingConfigured: false });
    expect(reads(silent)).toContain(t("nas.accounting.missing"));
    expect(markup(silent), "a measured silence rendered as an achievement").not.toContain(
      'data-level="positive"',
    );

    const accounting = device({ accountingConfigured: true });
    expect(reads(accounting)).toContain(t("nas.accounting.configured"));
    expect(markup(accounting)).toContain('data-level="positive"');
  });

  /**
   * The call-site half, in the shape `screens.test.ts` already uses for the three
   * dialog invariants — and for the same reason it uses it.
   *
   * `@foyer/admin` has no jsdom, and the second surface this defect lived on is a
   * notice inside `NasDetail`, a screen that needs a router and a query client to
   * render at all. What *is* observable here is the shape of the test, and the shape
   * is what regresses: `!nas.accountingConfigured` reads as *not accounting* in
   * English and means *absent or false* in TypeScript, so the next screen to ask
   * this question the obvious way asks it wrongly again.
   *
   * `bits.tsx` is the exemption rather than an oversight: the three-valued question
   * is answered in exactly one place and every screen goes through it.
   *
   * ── WHAT THIS DOES NOT REACH, AND WHY IT IS NOT WIDENED ─────────────────────
   *
   * Stated because the boundary is easy to mistake for coverage. This scans `.tsx`
   * under `admin/src/screens` and **nothing else** — not `web/fixtures`, not
   * `web/mock`, not this package's own `.ts`. The question was asked directly and
   * answered by enumerating every non-comment read of the property in
   * `web/`: **six outside tests, and four of them are guarded truthiness reads that
   * are not defects.**
   *
   *   - `findings.ts` ×3 — `nas.accountingConfigured ? …`, behind
   *     `!measured ? unmeasured(…) : …` where `measured = probe.reachability === "ok"`.
   *   - `mock/src/state.ts` ×2 — behind `reachable`, two statements after the probe
   *     wrote a boolean into the record.
   *   - `fixtures/src/index.ts` `deriveAlarms` — unguarded. **This was a live defect**
   *     and is fixed; the alarm now compares `=== false`.
   *   - `mock/src/server.ts` — `devices.some((n) => n.accountingConfigured)` deciding
   *     `foyer-sampled` against `unenforced`. Unguarded, and reported rather than
   *     fixed here.
   *
   * **A line-level text rule cannot tell those apart.** The guard that makes four of
   * them correct is a different statement, sometimes thirty lines away, and no regex
   * over a line can see it. So a workspace-wide version of this scan would need an
   * exemption list holding `findings.ts`, `state.ts` **and `server.ts`** — and the
   * last of those is a suspected instance of the very defect. A guard whose
   * exemption list contains a suspected defect holds that defect in place, the same
   * way a test that *asserts* a vulnerable header does.
   *
   * The instrument that can see the guard is the type system, not a scan:
   * `accountingConfigured?: boolean` is `boolean | undefined` and
   * `@typescript-eslint/strict-boolean-expressions` refuses exactly this shape at
   * every call site in every package. There is no linter in this workspace, so that
   * is a dependency decision and a report — not a quietly longer list here.
   */
  it("no screen asks this three-valued field a two-valued question", () => {
    const sources = import.meta.glob("../**/*.tsx", {
      query: "?raw",
      eager: true,
      import: "default",
    }) as Record<string, string>;

    /*
     * Tests are excluded by name, and this is not tidiness. Vite drops the globbing
     * file from its own results, so a guard that relied on that would be silently
     * scanning a different set from the one it names — and every test file here
     * builds records with this property set, which is what a test is for.
     *
     * `bits.tsx` is the exemption named above: one place answers the three-valued
     * question and every screen goes through it.
     */
    const files = Object.entries(sources).filter(
      ([path]) => !path.endsWith(".test.tsx") && !/(^|\/)bits\.tsx$/.test(path),
    );

    /* Comments out first. Every one of these files explains this field in prose, and
       a guard that could not tell a sentence from an expression would be pinning the
       documentation. */
    const code = (src: string) =>
      src.replace(/\/\*[\s\S]*?\*\//g, " ").replace(/(^|[^:])\/\/[^\n]*/g, "$1 ");

    /* A type position names the property in a string; an expression compares it.
       Anything else is reading a three-valued field for its truthiness. */
    const offends = (line: string) =>
      line.includes("accountingConfigured") &&
      !/Pick<[^>]*"accountingConfigured"/.test(line) &&
      !/accountingConfigured\s*(===|!==)/.test(line);

    /*
     * ── the two ways this could pass while measuring nothing ──────────────────
     *
     * **The matcher, self-tested.** A rule expressed as a regex can stop matching
     * without stopping running — one edit to the character class and every line is
     * innocent. So it is shown flagging the defect and clearing the repair, here,
     * on every run, rather than trusted because it once worked.
     */
    expect(offends("{!nas.accountingConfigured ? ("), "the matcher stopped seeing the defect")
      .toBe(true);
    expect(offends("{nas.accountingConfigured === false ? ("), "the matcher flags the repair")
      .toBe(false);

    /*
     * **The corpus, derived rather than listed.** This named `NasDetail.tsx` and
     * `AccessPoints.tsx` by hand, which is a list of where the bug happened to be
     * and not a rule — a moved directory or a renamed screen would empty the glob
     * and leave everything below green over nothing.
     *
     * The property that replaces them: whatever screen renders the accounting column
     * must be inside the scanned set, found by looking for the delegate rather than
     * by naming the file. `AccountingPill` is a symbol the compiler owns, so renaming
     * it breaks the import above and this search together; deleting the delegation
     * and going back to an inline branch turns this red, which is the regression the
     * list was really guarding against.
     */
    expect(files.length, "the glob matched nothing, so this guard asks nothing").toBeGreaterThan(0);
    expect(
      files.some(([, src]) => code(src).includes("AccountingPill")),
      "no scanned screen delegates to `AccountingPill` — either the glob no longer " +
        "reaches the screens, or a screen has gone back to rendering this field itself",
    ).toBe(true);

    const offenders: string[] = [];
    for (const [path, src] of files) {
      for (const line of code(src).split("\n")) {
        if (offends(line)) offenders.push(`${path}: ${line.trim()}`);
      }
    }
    expect(
      offenders,
      "`accountingConfigured` is absent on a device nothing has probed, so a truthiness " +
        "test on it reports an unmeasured radio as one that will never account. Compare " +
        "it explicitly, or render it with `AccountingPill`.",
    ).toEqual([]);
  });

  it("the three states are three renderings, not two spellings of one", () => {
    /* Cheap, and it is what stops a future repair from mapping two states onto one
       pill and leaving every assertion above green. */
    const drawn = new Set(
      [undefined, true, false].map((v) => markup(device({ accountingConfigured: v }))),
    );
    expect(drawn.size, "two of the three states render identically").toBe(3);
  });
});
