import { describe, expect, it } from "vitest";
import { LANGUAGES, translator } from "./i18n/index.js";
import { PAGES, findPage } from "./pages/index.js";
import { PROBE_FONT_TTF, PROBE_FONT_WOFF, PROBE_LANG_PATHS, PROBE_PATH, PROBE_ROWS } from "./pages/probe.js";
import { renderProbeDocument } from "./probe.js";
import { renderPage } from "./render.js";

/**
 * The measurement probe.
 *
 * Two things are held here, and the second is the one that would be easy to lose.
 *
 * **It answers every value the measurement plan lists.** That plan is a table, and
 * a probe that quietly dropped a row would still render, still report and still photograph
 * well; the measurement would only be found missing afterwards, at a phone, on the
 * one afternoon it was for.
 *
 * **It is not a guest page.** `PAGES` is what `build/build.ts` emits, what
 * `make web-embed` copies into the Go binary and what the guest listener serves,
 * so membership in it is membership in every guest's page view. That is asserted
 * here rather than left to the code that happens not to add it.
 */

const CSS = ":root{--v:1}";

/** Every language the portal carries, because a measurement in one is half of one. */
const documents = LANGUAGES.map((lang) => [lang, renderProbeDocument({ lang, css: CSS })] as const);

const english = documents[0]![1];

describe("the probe is not a guest page", () => {
  it("is not in PAGES, so no build walks it and no guest downloads it", () => {
    for (const page of PAGES) {
      expect(page.path.replace(/\/+$/, "").split("/"), page.path).not.toContain("probe");
    }
  });

  it("is not addressable by a guest, in any shape findPage tolerates", () => {
    for (const path of [PROBE_PATH, `/${PROBE_PATH}`, "probe", "probe/"]) {
      expect(findPage(path), path).toBeUndefined();
      expect(renderPage({ lang: "en", path, context: null, status: null, error: null }), path).toBeNull();
    }
  });

  it("is built under a language prefix like every other document, into a tree of its own", () => {
    // `dist/` is what `make web-embed` copies and what check-budget.mjs walks;
    // `dist-probe/` is what `lab/measure -probe` is pointed at. One is reachable
    // by a guest and one is not, and only the build can put a document in either.
    expect(PROBE_LANG_PATHS).toEqual(LANGUAGES.map((l) => `${l}/${PROBE_PATH}`));
  });
});

describe("the probe answers every value the measurement plan lists", () => {
  // The table, row for row. Each is a thing the plan names, not a thing this page
  // found interesting later.
  const REQUIRED: [string, RegExp][] = [
    // "innerWidth × innerHeight, screen.width × screen.height, devicePixelRatio"
    ["viewport", /window\.innerWidth, vh = window\.innerHeight/],
    ["screen", /window\.screen \? window\.screen\.width : 0/],
    ["dpr", /window\.devicePixelRatio/],
    // "The four env(safe-area-inset-*) values, resolved to pixels"
    ["insets", /border-width:env\(safe-area-inset-top,0px\) env\(safe-area-inset-right,0px\)/],
    // "a try/catch write-then-read of localStorage and sessionStorage"
    ["localstorage", /store\("localStorage"\)/],
    ["sessionstorage", /store\("sessionStorage"\)/],
    // "matchMedia('(prefers-color-scheme: dark)').matches"
    ["dark", /mq\("\(prefers-color-scheme: dark\)"\)\.matches/],
    ["font", /@font-face\{font-family:FoyerProbeTTF/],
    ["font", /document\.fonts\.check\("40px FoyerProbeTTF"\)/],
    // "navigator.userAgent — context for all of them"
    ["ua", /navigator\.userAgent/],
  ];

  for (const [row, pattern] of REQUIRED) {
    it(`${row}: ${pattern.source}`, () => {
      expect(english, `${row} is not measured`).toMatch(pattern);
    });
  }

  it("renders every row it has a writer for, and exactly one writer for every row", () => {
    for (const row of PROBE_ROWS) {
      expect(english, `no markup for ${row}`).toContain(`id="probe-${row}"`);
      // One writer each: a value written from two branches would leave the
      // digest with no single source for its key. The report row is the one
      // exception — it is the outcome, written by `done()` and addressed by id
      // rather than by `put()`, because its value is a sentence and its sub-line
      // is the digest.
      const writers =
        english.split(`put("${row}"`).length - 1 + english.split(`getElementById("probe-${row}")`).length - 1;
      expect(writers, `writers for ${row}`).toBe(1);
    }
  });

  it("shows the webfont line twice, so the answer is legible without script", () => {
    // `lab/measure`'s font draws every printable character as one filled
    // triangle, so the literal text IS the failure message: letters mean the
    // same-origin @font-face did not load, in the CNA or anywhere else.
    for (const [lang, html] of documents) {
      expect(html, lang).toMatch(/class="probe-fontline ttf">NOT LOADED/);
      expect(html, lang).toMatch(/class="probe-fontline woff">NOT LOADED/);
    }
    // And the width test measures that same string, off-screen, with no API.
    expect(english).toMatch(/el\.textContent = "NOT LOADED"/);
  });

  it("reports the layout about the portal's own stylesheet, not about a stylesheet of its own", () => {
    // The `--v` ladder, read off the portal's root token, and the two font paths
    // the host that serves this page actually serves.
    expect(english).toContain('getPropertyValue("--v")');
    expect(english).toContain(PROBE_FONT_TTF);
    expect(english).toContain(PROBE_FONT_WOFF);
    // It wears the portal's column, which is where its safe-area padding is —
    // and the resolved padding is reported beside the raw insets, because "is the
    // bottom button under the gesture bar" is a question about the first.
    expect(english).toContain('class="shell probe"');
    expect(english).toContain('querySelector(".shell")');
    expect(english).toContain('row("shellpad"');
  });

  it("measures the plan's own requirement — visible without scrolling — rather than asserting it", () => {
    expect(english).toMatch(/document\.documentElement\.scrollHeight/);
    expect(english).toMatch(/row\("fits"/);
  });

  it("makes exactly the two requests the measurement needs, and no others", () => {
    // The page's own rules may only name the two font files the host serves. A
    // third `url()` here would be a request the CNA may not make, and the row
    // depending on it would be measuring a fetch rather than a font.
    for (const [lang, html] of documents) {
      const urls = html.match(/url\(([^)]*)\)/g) ?? [];
      expect(urls.sort(), lang).toEqual([`url("${PROBE_FONT_TTF}")`, `url("${PROBE_FONT_WOFF}")`]);
    }
  });
});

describe("the probe reports itself to /report", () => {
  it("POSTs the digest it assembles to the route that already exists, same-origin", () => {
    for (const [lang, html] of documents) {
      expect(html, lang).toContain('fetch("/report"');
      expect(html, lang).toContain('x.open("POST", "/report", true)');
      // `id=value`, from the row ids — the same names the markup uses, so a key
      // in the log and a row on the screen are the same word.
      expect(html, lang).toContain('d.push(id + "=" + value)');
      expect(html, lang).toContain('row("vp", vp)');
      expect(html, lang).toMatch(/var line = d\.join\("  "\)/);
      expect(html, lang).toMatch(/body: line/);
    }
  });

  it("sends machine tokens, and only the outcome in words", () => {
    // The digest is grepped out of a log hours later, with the device and the
    // date beside it. Prose would be the wrong shape for that.
    expect(english).toMatch(/row\("ls", ls\)/);
    expect(english).toMatch(/"ok" : "write-only"/);
    expect(english).toContain("Sent to the measurement host");
  });

  it("never changes what the page shows when the report fails", () => {
    // The screen is the fallback and the POST is the improvement: a CNA that
    // refuses the request must still leave every value legible, which is the
    // photograph this page exists to stop needing.
    expect(english).toMatch(/done\(false, "blocked", line\)/);
    expect(english).toMatch(/done\(false, "no-transport", line\)/);
    expect(english).toContain("The host refused the report. The values above are still on this screen.");
  });

  it("obeys the portal's frozen rules: one request, no absolute URL", () => {
    // `lab/measure/pages_test.go` asserts the same of its own page for the same
    // reason: a second request is one the CNA may not make, and an external
    // origin is unreachable on a captive network.
    for (const [lang, html] of documents) {
      const urls = html.match(/(?:src|href)="https?:\/\/[^"]*"/g) ?? [];
      expect(urls, lang).toEqual([]);
      expect(html, lang).not.toMatch(/<link\b/);
    }
  });
});

describe("the probe is localised, in every language the portal carries", () => {
  it("labels the page in that language", () => {
    // Distinct per language, so a hardcoded English label cannot pass by being
    // the only one there is.
    const ledes = documents.map(([lang, html]) => {
      const lede = /<p class="probe-lede">([^<]*)</.exec(html)?.[1] ?? "";
      expect(lede, lang).not.toBe("");
      return lede;
    });
    expect(new Set(ledes).size).toBe(documents.length);
  });

  it("embeds only this language's report sentences in the script", () => {
    for (const [lang, html] of documents) {
      const other = lang === "en" ? "vi" : "en";
      expect(html, lang).toContain(JSON.stringify(translator(lang)("probe.sent")));
      expect(html, lang).toContain(JSON.stringify(translator(lang)("probe.refused")));
      expect(html, lang).not.toContain(JSON.stringify(translator(other)("probe.sent")));
    }
  });
});
