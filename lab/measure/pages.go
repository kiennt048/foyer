package main

// Every page served here obeys the guest portal's own frozen constraints, because
// a probe page that breaks them measures a different environment than the one the
// portal will run in: no external resource of any kind, CSS and JavaScript inlined
// rather than linked, one HTTP request per document, and something useful on the
// screen with JavaScript disabled.
//
// Two of those are load-bearing for the measurement rather than for tidiness.
// A linked stylesheet would be a second request the CNA may or may not make, and a
// blank page would then be ambiguous between "the CNA refuses the certificate" and
// "the CNA fetched the document and not its stylesheet". And an external origin
// would have to be reachable, which on a captive network it is not.
//
// **No page contains an absolute URL.** That is asserted by a test, mirroring the
// portal's own build assertion. It has one consequence worth naming: the link that
// moves the reader between the HTTP and HTTPS listeners cannot be a link, because
// the other scheme is necessarily a different origin. It is a relative link to
// /switch, which answers 302 with the absolute address in a Location header — out
// of the document, where the rule is about.

import (
	"fmt"
	"html/template"
	"strings"
)

// theme is one block of role-named custom properties and their dark counterpart.
// The dark block is not decoration: whether the CNA honours prefers-color-scheme
// at all is one of the on-device unknowns docs/protocol-reality.md lists, so this
// stylesheet has to be a real theme for the answer to mean anything.
const themeCSS = `
:root{
  --bg:#f7f7f5; --fg:#16161a; --muted:#5b5b66; --line:#d6d6d0;
  --panel:#ffffff; --ok:#0f6d3d; --bad:#9a2515; --info:#1d4f8f; --mark:#8a5a00;
}
@media (prefers-color-scheme: dark){
  :root{
    --bg:#101014; --fg:#f2f2ef; --muted:#a2a2ad; --line:#33333c;
    --panel:#1a1a20; --ok:#5fd39a; --bad:#ff8b76; --info:#8ab6f0; --mark:#e0b64f;
  }
}
`

const baseCSS = themeCSS + `
*{box-sizing:border-box}
html{-webkit-text-size-adjust:100%}
body{
  margin:0;background:var(--bg);color:var(--fg);
  font:16px/1.35 -apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,sans-serif;
  padding:max(10px,env(safe-area-inset-top)) max(10px,env(safe-area-inset-right)) max(10px,env(safe-area-inset-bottom)) max(10px,env(safe-area-inset-left));
}
h1{font-size:1.5rem;margin:0 0 2px}
h2{font-size:.85rem;margin:14px 0 4px;color:var(--muted);text-transform:uppercase;letter-spacing:.06em}
p{margin:0 0 6px}
a{color:var(--info)}
.scheme{font-size:1.6rem;font-weight:700;letter-spacing:-.01em}
.scheme.tls{color:var(--ok)}
.scheme.plain{color:var(--mark)}
.hint{color:var(--muted);font-size:.8rem;margin-bottom:8px}
.digest{
  font:700 1.05rem/1.3 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
  background:var(--panel);border:2px solid var(--line);border-radius:6px;
  padding:8px;margin:0 0 10px;word-break:break-word;
}
ol.findings{list-style:none;margin:0;padding:0}
ol.findings li{border-top:1px solid var(--line);padding:7px 0}
.q{display:block;font-size:.78rem;color:var(--muted);text-transform:uppercase;letter-spacing:.05em}
.v{display:block;font:700 1.15rem/1.25 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;word-break:break-word}
.v.yes{color:var(--ok)}
.v.no{color:var(--bad)}
.v.info{color:var(--info)}
.sub{display:block;font-size:.78rem;color:var(--muted)}
.actions{display:flex;flex-wrap:wrap;gap:8px;margin:12px 0}
.actions form{margin:0}
button,.btn{
  font:600 1rem/1 inherit;min-height:44px;padding:0 14px;border-radius:6px;
  border:2px solid var(--line);background:var(--panel);color:var(--fg);
  display:inline-flex;align-items:center;text-decoration:none;
}
/* The scrollbar is kept on screen deliberately, the same trick and the same
   reason as the portal: in a window the reader cannot resize, a page that looks
   complete and is not costs a measurement. */
::-webkit-scrollbar{width:10px;height:10px;-webkit-appearance:none}
::-webkit-scrollbar-thumb{background:var(--muted);border-radius:5px}
.insetbar{display:block;background:var(--mark);width:100%}
.insetbar.top{height:env(safe-area-inset-top,0px)}
.insetbar.bottom{height:env(safe-area-inset-bottom,0px)}
#insetprobe{
  position:fixed;left:0;top:0;width:0;height:0;visibility:hidden;
  padding-top:env(safe-area-inset-top,0px);padding-right:env(safe-area-inset-right,0px);
  padding-bottom:env(safe-area-inset-bottom,0px);padding-left:env(safe-area-inset-left,0px);
}
/* The webfont test is CSS only on purpose, so it still answers with JavaScript
   off. The literal text is the failure message; a row of triangles is the pass. */
@font-face{font-family:ProbeTTF;src:url("/probe-font.ttf") format("truetype");font-display:block}
@font-face{font-family:ProbeWOFF;src:url("/probe-font.woff") format("woff");font-display:block}
.ttf{font-family:ProbeTTF,ui-monospace,monospace;font-size:1.3rem}
.woff{font-family:ProbeWOFF,ui-monospace,monospace;font-size:1.3rem}
.dark-only,.light-only{display:none}
@media (prefers-color-scheme: dark){.dark-only{display:inline}}
@media (prefers-color-scheme: light){.light-only{display:inline}}
.bucket{display:none}
.table-scroll{overflow-x:auto}
table{border-collapse:collapse;width:100%;min-width:34em;font:12px/1.3 ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}
td,th{border-bottom:1px solid var(--line);padding:3px 4px;text-align:left;vertical-align:top}
th{color:var(--muted);font-weight:600}
td.ua{color:var(--muted);word-break:break-all}
tr.tls-error td{color:var(--bad)}
tr.hello td{color:var(--info)}
`

// probeJS enhances; it never carries the page. Everything it writes has a
// server-rendered or CSS-only counterpart above it, because the one environment
// this page is built for is also the one where a script error is invisible.
const probeJS = `
(function () {
  function put(id, text, verdict) {
    var el = document.getElementById(id);
    if (!el) return;
    el.textContent = text;
    el.className = "v" + (verdict ? " " + verdict : "");
  }
  var digest = [];

  var vv = window.visualViewport;
  var vp = window.innerWidth + "x" + window.innerHeight;
  put("viewport", vp
    + "  dpr " + (window.devicePixelRatio || 1)
    + "  screen " + screen.width + "x" + screen.height
    + (vv ? "  visual " + Math.round(vv.width) + "x" + Math.round(vv.height) : ""));
  digest.push("VP " + vp);

  var orient = (screen.orientation && screen.orientation.type)
    || (typeof window.orientation !== "undefined" ? "angle " + window.orientation : "not reported");
  put("orientation", orient);

  var mq = window.matchMedia;
  var dark = !!(mq && mq("(prefers-color-scheme: dark)").matches);
  var light = !!(mq && mq("(prefers-color-scheme: light)").matches);
  put("dark",
    dark ? "dark query MATCHED" : (light ? "light query matched" : "neither query matched"),
    dark ? "yes" : (light ? "info" : "no"));
  digest.push("DARK " + (dark ? "yes" : light ? "light" : "neither"));

  var probe = document.getElementById("insetprobe");
  var cs = probe && getComputedStyle(probe);
  if (cs) {
    var ins = [cs.paddingTop, cs.paddingRight, cs.paddingBottom, cs.paddingLeft].map(function (s) {
      return Math.round(parseFloat(s) || 0);
    });
    put("insets", "top " + ins[0] + "  right " + ins[1] + "  bottom " + ins[2] + "  left " + ins[3],
      ins[0] + ins[1] + ins[2] + ins[3] > 0 ? "yes" : "no");
    digest.push("INSET " + ins.join("/"));
  }

  function storageTest(kind) {
    try {
      var s = window[kind];
      if (!s) return { text: "not defined", v: "no" };
      s.setItem("foyer-probe", "1");
      var back = s.getItem("foyer-probe") === "1";
      s.removeItem("foyer-probe");
      return { text: back ? "write and read back OK" : "write did nothing, no error", v: back ? "yes" : "no" };
    } catch (e) {
      return { text: "threw " + ((e && e.name) || "an error"), v: "no" };
    }
  }
  var ls = storageTest("localStorage");
  var ss = storageTest("sessionStorage");
  put("localstorage", ls.text, ls.v);
  put("sessionstorage", ss.text, ss.v);
  digest.push("LS " + ls.v + " SS " + ss.v);

  try {
    var mark = window.localStorage && window.localStorage.getItem("foyer-marker");
    put("persist",
      mark ? "a marker written at " + mark + " survived" : "no earlier marker; one is written now",
      mark ? "yes" : "info");
    if (window.localStorage) {
      window.localStorage.setItem("foyer-marker", new Date().toTimeString().slice(0, 8));
    }
  } catch (e) {
    put("persist", "cannot test: " + ((e && e.name) || "error"), "no");
  }

  function widthOf(family) {
    var s = document.createElement("span");
    s.setAttribute("aria-hidden", "true");
    s.style.cssText = "position:absolute;left:-9999px;top:0;white-space:pre;font-size:40px;font-family:" + family;
    s.textContent = "NOT LOADED";
    document.body.appendChild(s);
    var w = s.offsetWidth;
    document.body.removeChild(s);
    return w;
  }
  function reportFonts() {
    var base = widthOf("ui-monospace,monospace");
    var ttfWide = widthOf("ProbeTTF,ui-monospace,monospace") !== base;
    var woffWide = widthOf("ProbeWOFF,ui-monospace,monospace") !== base;
    var api = "no document.fonts";
    if (document.fonts && document.fonts.check) {
      api = "check() ttf " + document.fonts.check("40px ProbeTTF")
        + ", woff " + document.fonts.check("40px ProbeWOFF");
    }
    put("font",
      "ttf " + (ttfWide ? "APPLIED" : "not applied") + "  ·  woff " + (woffWide ? "APPLIED" : "not applied"),
      ttfWide || woffWide ? "yes" : "no");
    var sub = document.getElementById("fontapi");
    if (sub) sub.textContent = api + " — width test is the one that does not need the API";
    digest.push("FONT ttf " + (ttfWide ? "y" : "n") + " woff " + (woffWide ? "y" : "n"));
    var line = digest.join("  ·  ");
    document.getElementById("digest").textContent = line;

    // Send the digest back so the SERVER holds it.
    //
    // Every environment value — viewport, colour scheme, safe-area insets,
    // storage, fonts — is computed here and rendered, and without this the only
    // way it reaches anybody is a photograph of the screen. Photographs get
    // cropped, and an evidence chain that runs through an image is one nobody
    // can re-read. A measurement whose provenance cannot be checked is not a
    // measurement.
    //
    // Best-effort on purpose: it must never change what the page displays, and a
    // CNA that refuses the request still shows the reader the same screen.
    try {
      if (window.fetch) {
        fetch("/report", {method: "POST", headers: {"Content-Type": "text/plain"}, body: line});
      } else if (window.XMLHttpRequest) {
        var x = new XMLHttpRequest();
        x.open("POST", "/report", true);
        x.setRequestHeader("Content-Type", "text/plain");
        x.send(line);
      }
    } catch (e) {}
  }
  if (document.fonts && document.fonts.load) {
    Promise.all([document.fonts.load("40px ProbeTTF"), document.fonts.load("40px ProbeWOFF")])
      .then(reportFonts, reportFonts);
  } else {
    reportFonts();
  }

  put("ua-js", navigator.userAgent);

  var started = Date.now();
  var clock = document.getElementById("elapsed");
  if (clock) {
    setInterval(function () {
      clock.textContent = Math.round((Date.now() - started) / 1000) + " s on this document";
    }, 1000);
  }
})();
`

type bucket struct {
	Class string
	Label string
}

// bucketLadder turns a list of breakpoints into one span per range plus the media
// queries that reveal exactly one of them. It exists so the viewport is legible
// with JavaScript off — and, with JavaScript on, as an independent cross-check:
// if the reported innerHeight and the CSS bucket disagree, something is scaling
// the page and that is itself worth knowing.
func bucketLadder(prefix, dimension string, edges []int) ([]bucket, string) {
	var buckets []bucket
	var css strings.Builder
	css.WriteString("." + prefix + "{display:none}\n")

	for i := 0; i <= len(edges); i++ {
		class := fmt.Sprintf("%s-%d", prefix, i)
		var label, query string
		switch {
		case i == 0:
			label = fmt.Sprintf("under %d", edges[0])
			query = fmt.Sprintf("(max-%s:%dpx)", dimension, edges[0]-1)
		case i == len(edges):
			label = fmt.Sprintf("%d or more", edges[i-1])
			query = fmt.Sprintf("(min-%s:%dpx)", dimension, edges[i-1])
		default:
			label = fmt.Sprintf("%d to %d", edges[i-1], edges[i]-1)
			query = fmt.Sprintf("(min-%s:%dpx) and (max-%s:%dpx)", dimension, edges[i-1], dimension, edges[i]-1)
		}
		buckets = append(buckets, bucket{Class: prefix + " " + class, Label: label})
		fmt.Fprintf(&css, "@media %s{.%s{display:inline}}\n", query, class)
	}
	return buckets, css.String()
}

var (
	widthEdges  = []int{320, 360, 375, 390, 412, 430, 480, 600, 768, 900, 1024}
	heightEdges = []int{400, 450, 500, 512, 550, 600, 620, 660, 700, 720, 800, 900}

	widthBuckets, widthCSS   = bucketLadder("wb", "width", widthEdges)
	heightBuckets, heightCSS = bucketLadder("hb", "height", heightEdges)
)

// probeData is everything the server can answer without JavaScript. Each field is
// on the page in its own right, not only inside the script's digest.
type probeData struct {
	Scheme          string
	SchemeClass     string
	SchemeNote      string
	Host            string
	Loopback        bool
	UA              string
	CookieVerdict   string
	CookieClass     string
	Captive         bool
	CaptiveJSON     string
	ServerTime      string
	TLSDetail       string
	CertFingerprint string
	CertNotAfter    string
	WidthBuckets    []bucket
	HeightBuckets   []bucket
}

var probeTmpl = template.Must(template.New("probe").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<!-- viewport-fit=cover is required, not cosmetic: without it iOS reports every
     env(safe-area-inset-*) as zero and the inset measurement is a false negative. -->
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="light dark">
<title>Foyer device probe</title>
<style>` + baseCSS + widthCSS + heightCSS + `</style>
</head>
<body>
<span id="insetprobe"></span>
<span class="insetbar top"></span>

<h1 class="scheme {{.SchemeClass}}">{{.Scheme}}</h1>
<p class="hint">{{.SchemeNote}} &middot; host {{.Host}} &middot; {{.ServerTime}}</p>
{{if .Loopback}}<p class="hint"><strong>This is the laptop, not a phone.</strong> Every number below describes this browser and settles nothing.</p>{{end}}
<p class="hint">Photograph this screen, then scroll and photograph the rest.</p>

<p class="digest" id="digest">JavaScript is off or has not run &mdash; the lines below still answer</p>

<ol class="findings">

<li>
  <span class="q">1 &middot; viewport</span>
  <span class="v" id="viewport">needs JavaScript</span>
  <span class="sub">CSS alone says width {{range .WidthBuckets}}<span class="{{.Class}}">{{.Label}}</span>{{end}}, height {{range .HeightBuckets}}<span class="{{.Class}}">{{.Label}}</span>{{end}}</span>
</li>

<li>
  <span class="q">2 &middot; orientation</span>
  <span class="v" id="orientation">needs JavaScript</span>
</li>

<li>
  <span class="q">3 &middot; prefers-color-scheme</span>
  <span class="v" id="dark">needs JavaScript</span>
  <span class="sub">CSS alone says <span class="dark-only">dark matched</span><span class="light-only">light matched</span></span>
</li>

<li>
  <span class="q">4 &middot; safe-area insets</span>
  <span class="v" id="insets">needs JavaScript</span>
  <span class="sub">the coloured strips at the very top and bottom of this page are the top and bottom insets; no strip means zero</span>
</li>

<li>
  <span class="q">5 &middot; localStorage</span>
  <span class="v" id="localstorage">needs JavaScript</span>
</li>

<li>
  <span class="q">6 &middot; sessionStorage</span>
  <span class="v" id="sessionstorage">needs JavaScript</span>
</li>

<li>
  <span class="q">7 &middot; did storage survive an earlier visit</span>
  <span class="v" id="persist">needs JavaScript</span>
</li>

<li>
  <span class="q">8 &middot; cookie round trip &mdash; answered by the server</span>
  <span class="v {{.CookieClass}}">{{.CookieVerdict}}</span>
  <span class="sub">the portal binds its session to a cookie, so this one is not a curiosity</span>
</li>

<li>
  <span class="q">9 &middot; same-origin webfont</span>
  <span class="ttf">NOT LOADED</span> <span class="sub">truetype &mdash; triangles mean it loaded, the words mean it did not</span>
  <span class="woff">NOT LOADED</span> <span class="sub">woff &mdash; same reading</span>
  <span class="v" id="font">needs JavaScript for the width test</span>
  <span class="sub" id="fontapi"></span>
</li>

<li>
  <span class="q">10 &middot; RFC 8908 answer right now</span>
  <span class="v {{if .Captive}}info{{else}}yes{{end}}">{{.CaptiveJSON}}</span>
  <span class="sub">served at /captive-portal/api as application/captive+json</span>
</li>

<li>
  <span class="q">11 &middot; user agent as the server saw it</span>
  <span class="v">{{.UA}}</span>
</li>

<li>
  <span class="q">12 &middot; user agent as the page sees it</span>
  <span class="v" id="ua-js">needs JavaScript</span>
</li>

<li>
  <span class="q">13 &middot; transport</span>
  <span class="v">{{.TLSDetail}}</span>
  <span class="sub">certificate {{.CertFingerprint}}, expires {{.CertNotAfter}}</span>
</li>

<li>
  <span class="q">14 &middot; time on this document</span>
  <span class="v" id="elapsed">needs JavaScript</span>
</li>

</ol>

<h2>actions</h2>
<div class="actions">
  <form method="post" action="/captive"><input type="hidden" name="state" value="false"><button type="submit">answer captive:false</button></form>
  <form method="post" action="/captive"><input type="hidden" name="state" value="true"><button type="submit">answer captive:true</button></form>
  <a class="btn" href="/log">request log</a>
  <a class="btn" href="/switch">same page, other scheme</a>
  <a class="btn" href="/ca.crt">install root certificate</a>
  <a class="btn" href="/captive-portal/api">raw RFC 8908 answer</a>
</div>
<p class="hint">Every one of those is a full document navigation, which is the only
thing that makes the CNA re-check captivity. That is deliberate.</p>

<span class="insetbar bottom"></span>
<script>` + probeJS + `</script>
</body>
</html>
`))

type logData struct {
	Events    []event
	Count     int
	Captive   bool
	ServerNow string
}

var logTmpl = template.Must(template.New("log").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="light dark">
<title>Foyer device probe &mdash; request log</title>
<style>` + baseCSS + `</style>
</head>
<body>
<h1>request log</h1>
<p class="hint">{{.Count}} events, newest first &middot; {{.ServerNow}} &middot; captive: {{.Captive}}</p>
<p class="hint">A <code>hello</code> with no <code>http</code> after it is a client that
started TLS and walked away. No <code>hello</code> at all is a client that never
arrived. Those are different answers to the HTTPS question.</p>
<p class="hint">This page does not refresh itself: an automatic reload is a navigation,
and a navigation is the thing that makes the CNA re-check. It would corrupt the time-on-document reading.</p>
<div class="actions"><a class="btn" href="/log">reload</a><a class="btn" href="/probe">back to the probe</a></div>
<div class="table-scroll">
<table>
<tr><th>time</th><th>kind</th><th>peer</th><th>detail</th><th>user agent</th></tr>
{{range .Events}}<tr class="{{.Kind}}"><td>{{.When.Format "15:04:05.000"}}</td><td>{{.Kind}}</td><td>{{.Peer}}</td><td>{{.Detail}}</td><td class="ua">{{.UA}}</td></tr>
{{end}}
</table>
</div>
</body>
</html>
`))

// notFoundTmpl says what does exist. A 404 on a phone with no console is
// otherwise indistinguishable from the failure this whole kit is measuring.
var notFoundTmpl = template.Must(template.New("404").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="color-scheme" content="light dark">
<title>Foyer device probe &mdash; no such page</title>
<style>` + baseCSS + `</style>
</head>
<body>
<h1>no such page</h1>
<p class="hint">This host reached you, which is itself worth knowing. It serves:</p>
<div class="actions">
  <a class="btn" href="/probe">/probe</a>
  <a class="btn" href="/log">/log</a>
  <a class="btn" href="/captive-portal/api">/captive-portal/api</a>
  <a class="btn" href="/ca.crt">/ca.crt</a>
</div>
{{if .}}<p class="hint">{{.}}</p>{{end}}
</body>
</html>
`))
