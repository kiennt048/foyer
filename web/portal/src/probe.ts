import type { Language } from "@foyer/contract";
import { translator } from "./i18n/index.js";
import { renderDocument } from "./document.js";
import { PROBE_CSS, probeBody, probeScript } from "./pages/probe.js";
import render from "preact-render-to-string";

// The build walks these and writes one document per entry, so the list lives
// with the page rather than being re-derived from `LANGUAGES` a second time in
// `build/build.ts` — one list, and the host is pointed at the same one.
export { PROBE_LANG_PATHS } from "./pages/probe.js";

/**
 * The probe page as one complete document per language — the unit
 * `build/build.ts` writes to `dist-probe/`.
 *
 * **This module is deliberately not `render.ts`.** `renderPage` takes a
 * `PortalContext`, resolves a venue and is what `PAGES` drives; a probe has no
 * venue, no grant and no guest, and putting it there would have made it one
 * `PageDefinition` among six — the shape `build.ts` and `make web-embed` treat
 * as "ship to every guest". Keeping the entry point separate is what makes that
 * impossible rather than merely discouraged.
 *
 * `css` is the portal's own stylesheet, minified exactly as the guest documents
 * get it, and the probe's own rules ride on the end. That is not a shortcut: the layout
 * measurement's consequence is about *this* stylesheet's `--v` ladder, so a page wearing a
 * different one would measure the wrong environment.
 */
export function renderProbeDocument(opts: { lang: Language; css: string }): string {
  const t = translator(opts.lang);
  return renderDocument({
    lang: opts.lang,
    title: t("probe.title"),
    body: render(probeBody(t)),
    css: `${opts.css}${PROBE_CSS}`,
    script: { inline: probeScript(t) },
  });
}
