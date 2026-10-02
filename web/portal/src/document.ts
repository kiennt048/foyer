import type { Language } from "@foyer/contract";

/**
 * Assembles one self-contained HTML document.
 *
 * CSS and JS are inlined rather than linked. That looks like a regression until you
 * account for the environment: Android calls `clearCache(true)` on every portal
 * launch and the CNA has no persistent store at all, so a linked asset is
 * re-fetched every single time and a cache-busting hash buys nothing. Inlining
 * makes the whole portal exactly one HTTP request over the worst link the guest
 * will use that day.
 */
export function renderDocument(opts: {
  lang: Language;
  title: string;
  body: string;
  css: string;
  /** Inlined at build time; a module URL in dev so HMR works. */
  script?: { inline?: string; src?: string };
}): string {
  const { lang, title, body, css, script } = opts;

  const scriptTag = script?.inline
    ? `<script>${script.inline}</script>`
    : script?.src
      ? `<script type="module" src="${script.src}"></script>`
      : "";

  return `<!doctype html>
<html lang="${lang}">
<head>
<meta charset="utf-8">
<title>${escapeHtml(title)}</title>
<!--
  Mandatory. Android sets setUseWideViewPort(true) and setLoadWithOverviewMode(true)
  on the captive-portal WebView, so without this the page is laid out at ~980px and
  scaled to fit. viewport-fit=cover is what makes env(safe-area-inset-*) meaningful.
-->
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<!--
  Android never auto-darkens the captive-portal WebView: it does not call
  setForceDark or setAlgorithmicDarkeningAllowed. This plus a real dark stylesheet
  is the only reason dark mode works at all.
-->
<meta name="color-scheme" content="dark light">
<meta name="robots" content="noindex,nofollow">
<meta name="referrer" content="no-referrer">
<style>${css}</style>
</head>
<body>
${body}
${scriptTag}
</body>
</html>
`;
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;");
}
