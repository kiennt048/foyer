/** @jsxImportSource preact */
import type { ComponentChildren, VNode } from "preact";
import type { MessageKey, Translate } from "../i18n/index.js";
import { LANGUAGES } from "../i18n/index.js";

/**
 * The measurement probe — the page that three phone measurements (storage,
 * layout and webfonts in the captive-network sheet) are lost without. It reports what the environment *does*, in the portal's own languages,
 * on the portal's own stylesheet, and POSTs the digest to `/report`.
 *
 * ── why it is not a `PageDefinition` ─────────────────────────────────────────
 *
 * **It is not in `PAGES`, and that is the whole of the containment.** `PAGES` is
 * the guest route table: `build/build.ts` emits a document per entry, `make
 * web-embed` copies that tree into the Go binary, and `internal/webui` serves it
 * on the listener anybody within radio range can reach. One entry here would put
 * a diagnostic on the guest surface, and the byte budget would carry it to every
 * guest to serve one afternoon of measurement. So this module exports no
 * `PageDefinition`, registers nothing, and `probe.test.ts` holds both facts.
 *
 * The output is `dist-probe/`, which nothing embeds and
 * `web/tools/check-budget.mjs` does not walk.
 *
 * ── who serves it ───────────────────────────────────────────────────────────
 *
 * `lab/measure`, at `/{lang}/probe/`, with `-probe web/portal/dist-probe`. That
 * is the host that already owns `POST /report` (`lab/measure/server.go`), and
 * choosing it is what makes the digest land in a log rather than in a
 * photograph: values that reach a report only as cropped screenshots cannot be
 * compared or checked.
 *
 * It is the *portal's* probe rather than a second copy of `lab/measure`'s,
 * because the layout measurement's consequence is about the portal's own
 * vertical rhythm: the `--v` ladder in `styles/portal.css` is keyed on
 * `max-height`, and only a page wearing that stylesheet can say which step an
 * iPhone CNA lands on.
 *
 * ── the two webfont paths ───────────────────────────────────────────────────
 *
 * `lab/measure` generates and serves `/probe-font.ttf` and `/probe-font.woff`
 * (`font.go`: every printable character is one filled triangle, so the literal
 * string `NOT LOADED` is the failure message and a row of triangles is the
 * pass). Those two paths are the webfont measurement, and they are named here as
 * constants so the coupling to that host is one grep away rather than a literal
 * buried in a stylesheet.
 */
export const PROBE_PATH = "probe/";

/** The two same-origin font files, as `lab/measure` serves them today. */
export const PROBE_FONT_TTF = "/probe-font.ttf";
export const PROBE_FONT_WOFF = "/probe-font.woff";

/**
 * The probe's own rules, appended to the portal's stylesheet.
 *
 * Colours are the portal's tokens and nothing else — `web/tools/check-tokens.mjs`
 * holds this file to it, and a raw hex on a page that
 * exists to measure the dark theme would be the one place its own answer could not
 * be trusted.
 *
 * Three of these rules answer a question **with scripting off**, which is a real
 * environment for this page: the safe-area frame, the dark/light marker and the
 * `--v` bucket ladder are all pure CSS, so a WebView that refuses the script
 * still reports them. Everything else needs the script and says "not measured
 * yet" without it.
 */
export const PROBE_CSS = `
@font-face{font-family:FoyerProbeTTF;src:url("${PROBE_FONT_TTF}") format("truetype");font-display:block}
@font-face{font-family:FoyerProbeWOFF;src:url("${PROBE_FONT_WOFF}") format("woff");font-display:block}
.probe-h{font-size:.95rem;line-height:1.2;margin:0 0 calc(2px * var(--v))}
.probe-lede{font-size:.68rem;line-height:1.25;color:var(--c-text-muted);margin:0 0 calc(5px * var(--v))}
/* Two columns, and the label column is as narrow as the shortest labels allow.
   The plan requires every value visible without scrolling in a 550 pt window, and at
   a phone width the label column is most of the page: a wide one buys a value
   column that wraps the user agent across eight lines and pushes the safe-area
   row off the bottom. The labels are short because of that, not for taste. */
/* The rows stack; each row is the two-column grid. Putting the columns on the
   list instead stacked the label above its own value — a dt and a dd as block
   children of a grid *item* — leaving every row two lines tall and 969 px of
   content where the plan wants it inside 550. */
.probe-list{margin:0;display:grid;gap:calc(2px * var(--v))}
.probe-row{display:grid;grid-template-columns:auto 1fr;gap:0 calc(8px * var(--v));align-items:baseline;min-width:0}
.probe-k{font-size:.6rem;font-weight:700;letter-spacing:.06em;text-transform:uppercase;color:var(--c-text-muted);white-space:nowrap}
.probe-v{font:600 12.5px/1.25 var(--font-mono);color:var(--c-text);word-break:break-word;min-width:0}
.probe-v[data-verdict="yes"]{color:var(--c-accent)}
.probe-v[data-verdict="no"]{color:var(--c-danger)}
.probe-v[data-verdict="info"]{color:var(--c-text)}
.probe-sub{display:block;font:400 10px/1.25 var(--font);color:var(--c-text-faint);word-break:break-word}
/* The two long values — the user agent and the digest — take the full width and
   the smallest type on the page. Neither is read across a room: one is context
   for the numbers above it and the other is a verbatim copy of all of them, in
   the log and on the wire. Both are still legible in a photograph at dpr 3. */
/* Side by side after all: a one-column wide row spent a whole line on the label
   under a value that is context rather than a number, and at 550 pt every line
   is one somebody has to scroll to reach. */
.probe-wide{grid-template-columns:auto 1fr}
.probe-wide .probe-v{font-size:10.5px;line-height:1.3;color:var(--c-text-muted);word-break:break-all}
/* The four env(safe-area-inset-*) values, as the drawn edges of one box. Visible
   with scripting off, and read by the script as this element's border widths — one
   element answers the question either way, and a second hidden probe for the same
   value would be a second thing that could disagree. */
.probe-insetframe{display:block;border-style:solid;border-color:var(--c-accent-wash);
  border-width:env(safe-area-inset-top,0px) env(safe-area-inset-right,0px) env(safe-area-inset-bottom,0px) env(safe-area-inset-left,0px);
  margin:calc(1px * var(--v)) 0}
/* Webfont. The literal text is the failure message; a row of triangles is the pass.
   Side by side, because the two lines are a comparison and a comparison split
   across a folded page is not one. */
.probe-fontline{font:400 15px/1.15 var(--font-mono);color:var(--c-text)}
.probe-fontline.ttf{font-family:FoyerProbeTTF,var(--font-mono)}
.probe-fontline.woff{font-family:FoyerProbeWOFF,var(--font-mono)}
.probe-fonts{display:flex;gap:calc(10px * var(--v));flex-wrap:wrap}
/* The --v ladder, cross-checked in CSS. The script reports the value it computed
   the root token to; this reports which of the three max-height bands the
   stylesheet itself selected. A disagreement means something is scaling the page,
   which is itself the finding. */
.probe-bucket{display:none}
@media (max-height:620px){.probe-b0{display:inline}}
@media (min-height:621px) and (max-height:720px){.probe-b1{display:inline}}
@media (min-height:721px){.probe-b2{display:inline}}
.probe-dark{display:none}
@media (prefers-color-scheme:dark){.probe-dark{display:inline}}
.probe-light{display:inline}
@media (prefers-color-scheme:dark){.probe-light{display:none}}
`;

/**
 * One labelled row: a value, optionally a machine sub-line under it, optionally
 * server-rendered markers that need no script at all.
 *
 * The `id` is the seam between the markup and `probeScript` — exactly one `put()`
 * writes `probe-<id>`, and `probe.test.ts` holds that pairing in both directions.
 * A row whose writer is deleted would otherwise read "not measured yet" on the one
 * afternoon it was needed, and look like an environment that measures nothing.
 *
 * `wide` takes the whole row rather than the value column, for the two values
 * that are long enough to wrap the grid into uselessness: the user agent and the
 * digest. Both are context for the numbers above them rather than numbers, and
 * The plan's "visible without scrolling in a 550 pt window" is a requirement this
 * page either meets or reports it failed to meet.
 */
function Row({
  t,
  id,
  label,
  withSub = false,
  wide = false,
  children,
}: {
  t: Translate;
  id: string;
  label: MessageKey;
  withSub?: boolean;
  wide?: boolean;
  children?: ComponentChildren;
}) {
  return (
    <div class={wide ? "probe-row probe-wide" : "probe-row"}>
      <dt class="probe-k">{t(label)}</dt>
      <dd>
        <span class="probe-v" id={`probe-${id}`} data-verdict="info">
          {t("probe.pending")}
        </span>
        {withSub ? <span class="probe-sub" id={`probe-${id}-sub`} /> : null}
        {children}
      </dd>
    </div>
  );
}

/**
 * The page body, in one language.
 *
 * It wears the portal's own `.shell`, on purpose: the safe-area padding and the
 * `--v` ladder the browser resolves for it are the portal's, so the insets row and
 * the "does this page fit" row answer the layout question about the product rather than about a
 * diagnostic written for the occasion.
 */
export function probeBody(t: Translate): VNode {
  return (
    <div class="shell probe">
      <h1 class="probe-h">{t("probe.heading")}</h1>
      <p class="probe-lede">{t("probe.lede")}</p>

      <dl class="probe-list">
        {/* Layout — the iOS CNA viewport, unknown, and the 900 × 550 figure is macOS. */}
        <Row t={t} id="viewport" label="probe.viewport" withSub />
        <Row t={t} id="orientation" label="probe.orientation" />
        <Row t={t} id="ladder" label="probe.ladder">
          <span class="probe-sub">
            <b class="probe-b0">h&lt;=620</b>
            <b class="probe-b1">h&lt;=720</b>
            <b class="probe-b2">h&gt;720</b>
          </span>
        </Row>
        {/* Layout — whether a dark stylesheet is honoured at all. The marker beside the
            value is CSS, so it answers even where the script did not run. */}
        <Row t={t} id="dark" label="probe.dark">
          <span class="probe-sub">
            <b class="probe-dark">the stylesheet is showing its dark rules</b>
            <b class="probe-light">the stylesheet is showing its light rules</b>
          </span>
        </Row>
        {/* Layout — whether a bottom CTA sits under the gesture bar, and what the
            portal's own column therefore resolves to. */}
        <Row t={t} id="insets" label="probe.insets" withSub>
          <span class="probe-insetframe" id="probe-insetframe" aria-hidden="true" />
        </Row>

        {/* Storage — the three meaningfully different outcomes: writes throw, writes work
            in-session and are destroyed, and writes persist across a rejoin. */}
        <Row t={t} id="localstorage" label="probe.localstorage" />
        <Row t={t} id="sessionstorage" label="probe.sessionstorage" />
        <Row t={t} id="persist" label="probe.persist" />
        {/* Not a layout, storage or font question, and it belongs here: the whole sign-in flow is a client
            cookie, so a CNA that reports `cookieEnabled` and then refuses the
            cookie is a finding this page is the only cheap place to take. */}
        <Row t={t} id="cookie" label="probe.cookie" />

        {/* Webfont — does a same-origin @font-face load in the CNA. */}
        <Row t={t} id="font" label="probe.font" withSub>
          <span class="probe-sub">{t("probe.fontnote")}</span>
          <span class="probe-fonts">
            <span class="probe-fontline ttf">NOT LOADED</span>
            <span class="probe-fontline woff">NOT LOADED</span>
          </span>
        </Row>

        {/* `wide`: the user agent is a hundred characters and the value column is
            a third of a phone. Wrapped in the grid it is eight lines, and the plan
            says there may be no scrolling. */}
        <Row t={t} id="ua" label="probe.ua" withSub wide />
        {/* The plan's own requirement: "everything on it must be visible without
            scrolling in a 550 pt-tall window". Rather than assert that, the page
            measures it and reports the overflow in pixels. */}
        <Row t={t} id="fits" label="probe.fits" />
        <Row t={t} id="report" label="probe.report" withSub wide />
      </dl>
    </div>
  );
}

/**
 * The enhancement, as a string, with the three report sentences inlined.
 *
 * Written the way `lab/measure`'s own probe script is: ES5, no template literals,
 * no arrow functions, no `const`. The one environment this runs in is a phone's
 * WebView inside a Captive Network Assistant, and a syntax error in it is
 * invisible — the page would render "not measured yet" in every row and look like
 * an environment that measures nothing.
 *
 * **The values are machine tokens, not sentences.** The same line is POSTed to
 * `/report` verbatim and is meant to be grepped out of the log hours later, with
 * the device and the date beside it. Prose would be the wrong shape for that; the
 * three places a person needs a sentence get one from the catalogue.
 *
 * **Best-effort by design.** Nothing here may change what the page shows: a CNA
 * that refuses the POST still leaves every value on the screen, which is the
 * photograph this page was built to stop needing.
 */
export function probeScript(t: Translate): string {
  return `(function () {
  var d = [];
  function put(id, text, verdict) {
    var el = document.getElementById("probe-" + id);
    if (!el) return;
    el.textContent = text;
    el.setAttribute("data-verdict", verdict || "info");
  }
  function sub(id, text) {
    var el = document.getElementById("probe-" + id + "-sub");
    if (el) el.textContent = text;
  }
  /* One row, one or more entries, in the order the rows are shown. */
  function row(id, value) { d.push(id + "=" + value); }
  /* Errors and user agents carry spaces; the digest is space-separated. */
  function slug(s) { return String(s).replace(/\\s+/g, "_").slice(0, 80); }
  var computed = window.getComputedStyle ? window.getComputedStyle.bind(window) : null;
  /* A CSS resolved length to a whole pixel, with the NaN the rules above would
     otherwise produce collapsed to 0. Nine call sites, one definition. */
  function px(v) { return Math.round(parseFloat(v) || 0); }

  /* ── layout · viewport ────────────────────────────────────────────────── */
  var vw = window.innerWidth, vh = window.innerHeight;
  var dpr = window.devicePixelRatio || 1;
  var sw = window.screen ? window.screen.width : 0;
  var sh = window.screen ? window.screen.height : 0;
  var vv = window.visualViewport;
  var vp = vw + "x" + vh;
  put("viewport", vp + "  dpr " + dpr + "  screen " + sw + "x" + sh
      + (vv ? "  visual " + Math.round(vv.width) + "x" + Math.round(vv.height) : ""));
  row("vp", vp); row("dpr", dpr); row("screen", sw + "x" + sh);
  if (vv) row("visual", Math.round(vv.width) + "x" + Math.round(vv.height) + "@" + vv.scale);

  /* ── layout · orientation ─────────────────────────────────────────────────── */
  var o = (window.screen && window.screen.orientation && window.screen.orientation.type)
      || (typeof window.orientation === "number" ? "angle " + window.orientation : "unknown");
  put("orientation", o);
  row("orient", slug(o));

  /* ── layout · the --v ladder, from the portal's own stylesheet ────────────── */
  var v = computed ? slug(computed(document.documentElement).getPropertyValue("--v")) : "unknown";
  var mq = window.matchMedia;
  var band = !mq ? "no-matchMedia"
      : mq("(max-height: 620px)").matches ? "h<=620"
      : mq("(max-height: 720px)").matches ? "h<=720"
      : "h>720";
  put("ladder", v + "   band " + band);
  row("v", v + ":" + band);

  /* ── layout · prefers-color-scheme ────────────────────────────────────────── */
  var dark = !!(mq && mq("(prefers-color-scheme: dark)").matches);
  var light = !!(mq && mq("(prefers-color-scheme: light)").matches);
  var scheme = dark ? "dark" : (light ? "light" : (mq ? "no-preference" : "no-matchMedia"));
  put("dark", scheme, dark ? "yes" : (light ? "info" : "no"));
  row("dark", dark ? "dark" : (light ? "light" : "none"));

  /* ── layout · safe-area insets, and what the portal's column resolves to ──── */
  var frame = document.getElementById("probe-insetframe");
  var ins = [0, 0, 0, 0];
  if (frame && computed) {
    var cs = computed(frame);
    ins = [px(cs.borderTopWidth), px(cs.borderRightWidth), px(cs.borderBottomWidth), px(cs.borderLeftWidth)];
  }
  put("insets", "top " + ins[0] + "  right " + ins[1] + "  bottom " + ins[2] + "  left " + ins[3],
      (ins[0] + ins[1] + ins[2] + ins[3]) > 0 ? "yes" : "no");
  row("insets", ins.join("/"));
  var shell = document.querySelector(".shell");
  if (shell && computed) {
    var sp = computed(shell);
    var pad = [px(sp.paddingTop), px(sp.paddingRight), px(sp.paddingBottom), px(sp.paddingLeft)];
    sub("insets", "page padding resolved: " + pad.join("/") + "  t/r/b/l");
    row("shellpad", pad.join("/"));
  }

  /* ── storage ─────────────────────────────────────────────────────────── */
  function store(kind) {
    try {
      var s = window[kind];
      if (!s) return "absent";
      s.setItem("foyer-probe", "1");
      var back = s.getItem("foyer-probe") === "1";
      s.removeItem("foyer-probe");
      return back ? "ok" : "write-only";
    } catch (e) {
      return "threw:" + slug((e && e.name) || "Error");
    }
  }
  var ls = store("localStorage");
  var ss = store("sessionStorage");
  put("localstorage", ls, ls === "ok" ? "yes" : "no");
  put("sessionstorage", ss, ss === "ok" ? "yes" : "no");
  row("ls", ls); row("ss", ss);

  /* Storage's third outcome, which the write-then-read above cannot reach: a marker left
     across a reload. "survived:<ts>" on a second visit is a persistent data store
     in the CNA — the one thing the design says nothing depends on, so it is
     measured rather than assumed either way. */
  var persist = "unavailable";
  try {
    if (window.localStorage) {
      var mark = window.localStorage.getItem("foyer-probe-marker");
      persist = mark ? "survived:" + mark : "first-visit";
      window.localStorage.setItem("foyer-probe-marker", String(Date.now()));
    }
  } catch (e) {
    persist = "threw:" + slug((e && e.name) || "Error");
  }
  put("persist", persist, persist.indexOf("survived") === 0 ? "yes" : "info");
  row("persist", slug(persist));

  /* Not in the plan: the portal's whole sign-in flow is a client cookie. */
  var cookie = "cookieEnabled=" + (navigator.cookieEnabled === true ? "true"
      : (navigator.cookieEnabled === false ? "false" : "unknown")) + "  write=";
  try {
    document.cookie = "foyer-probe=1; path=/; SameSite=Lax";
    cookie += /foyer-probe=1/.test(document.cookie) ? "ok" : "not-retained";
    document.cookie = "foyer-probe=; path=/; max-age=0; SameSite=Lax";
  } catch (e) {
    cookie += "threw:" + slug((e && e.name) || "Error");
  }
  put("cookie", cookie, /write=ok/.test(cookie) ? "yes" : "no");
  row("cookie", slug(cookie));

  /* ── webfont · two ways ───────────────────────────────────────── */
  function widthOf(family) {
    var el = document.createElement("span");
    el.setAttribute("aria-hidden", "true");
    el.style.cssText = "position:absolute;left:-9999px;top:0;white-space:pre;font-size:40px;font-family:" + family;
    el.textContent = "NOT LOADED";
    document.body.appendChild(el);
    var w = el.offsetWidth;
    document.body.removeChild(el);
    return w;
  }
  function fonts() {
    /* The width test needs no API, and a CNA whose document.fonts is absent or
       lying must still be measured — so the width decides and check() is reported
       beside it, never instead of it. */
    var base = widthOf("ui-monospace, monospace");
    var ttf = widthOf("FoyerProbeTTF, ui-monospace, monospace") !== base;
    var woff = widthOf("FoyerProbeWOFF, ui-monospace, monospace") !== base;
    var check = "no document.fonts";
    if (document.fonts && document.fonts.check) {
      check = "check() ttf=" + document.fonts.check("40px FoyerProbeTTF")
          + " woff=" + document.fonts.check("40px FoyerProbeWOFF");
    }
    put("font", "ttf " + (ttf ? "applied" : "not-applied") + "   woff " + (woff ? "applied" : "not-applied"),
        (ttf || woff) ? "yes" : "no");
    sub("font", check);
    row("font", "ttf=" + (ttf ? "applied" : "not-applied") + "_woff=" + (woff ? "applied" : "not-applied")
        + "_" + slug(check));
  }
  if (document.fonts && document.fonts.load && window.Promise && window.Promise.all) {
    window.Promise.all([
      document.fonts.load("40px FoyerProbeTTF"),
      document.fonts.load("40px FoyerProbeWOFF")
    ]).then(fonts, fonts);
  } else {
    fonts();
  }

  /* ── context for all of the above ─────────────────────────────────────── */
  var ua = navigator.userAgent || "unknown";
  put("ua", ua + "   [" + (navigator.language || "?")
      + " touch " + (navigator.maxTouchPoints || 0)
      + " pointer " + ((mq && mq("(pointer: coarse)").matches) ? "coarse" : "fine") + "]");
  row("ua", slug(ua));

  /* ── the digest, the plan's own requirement, then the report ────────────── */
  //
  // Two things about this ordering, both learned by watching the row disagree
  // with the browser.
  //
  // **The report row is the outcome in the value and the digest in the sub-line,
  // never the other way round.** With the digest in the value, the row measures
  // 49 px short while the sub-line is empty and then 130 px over once the POST
  // resolves and the value collapses from four lines to one sentence. A
  // number describing a layout nobody will ever look at is not a measurement.
  //
  // **The fit is measured on the next frame, after the digest has been laid
  // out.** The digest is what the page is 150 px taller with than an empty
  // sub-line, and measuring before it is written is measuring a page that is
  // about to change. The rewrite afterwards adds one token, not a line.
  var state = document.getElementById("probe-report");
  var why = document.getElementById("probe-report-sub");
  if (why) why.textContent = d.join("  ");
  function settle() {
    var scroll = Math.max(document.documentElement.scrollHeight,
        document.body ? document.body.scrollHeight : 0);
    var over = scroll > window.innerHeight;
    put("fits", (over ? "no" : "yes") + "   " + scroll + " / " + window.innerHeight + "px",
        over ? "no" : "yes");
    row("fits", (over ? "no:" : "yes:") + scroll + "/" + window.innerHeight);
    var line = d.join("  ");
    if (why) why.textContent = line;
    send(line);
  }
  if (window.requestAnimationFrame) window.requestAnimationFrame(settle); else settle();

  function done(ok, detail, line) {
    if (state) {
      state.textContent = ok ? ${JSON.stringify(t("probe.sent"))}
          : (detail === "blocked" ? ${JSON.stringify(t("probe.blocked"))} : ${JSON.stringify(t("probe.refused"))});
      state.setAttribute("data-verdict", ok ? "yes" : "no");
    }
    if (why) why.textContent = line + "   — " + detail;
  }
  /* The route is the one that already exists: lab/measure's /report, which logs the
     body. Not a second endpoint and not a contract operation — this page is served
     by that host, so "same origin" is the whole of the routing. */
  function send(line) {
    try {
      if (window.fetch) {
        window.fetch("/report", { method: "POST", headers: { "Content-Type": "text/plain" },
                                   body: line, keepalive: true })
          .then(function (r) { done(r.ok, "http " + r.status, line); },
                function () { done(false, "blocked", line); });
      } else if (window.XMLHttpRequest) {
        var x = new XMLHttpRequest();
        x.open("POST", "/report", true);
        x.setRequestHeader("Content-Type", "text/plain");
        x.onreadystatechange = function () {
          if (x.readyState !== 4) return;
          done(x.status >= 200 && x.status < 300, "http " + x.status, line);
        };
        x.send(line);
      } else {
        done(false, "no-transport", line);
      }
    } catch (e) {
      done(false, "blocked", line);
    }
  }
})();`;
}

/** Every row on the page, and the `id` its `put()` writes. One list, walked by
 *  `probe.test.ts` in both directions: a row with no writer, or a writer with no
 *  row, is a measurement that reads "not measured yet" on the day it was needed. */
export const PROBE_ROWS = [
  "viewport",
  "orientation",
  "ladder",
  "dark",
  "insets",
  "localstorage",
  "sessionstorage",
  "persist",
  "cookie",
  "font",
  "ua",
  "fits",
  "report",
] as const;

/** Where the probe document goes, for every language the portal carries. */
export const PROBE_LANG_PATHS: readonly string[] = LANGUAGES.map((l) => `${l}/${PROBE_PATH}`);
