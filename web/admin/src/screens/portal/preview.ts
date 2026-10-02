/**
 * The guest's page, rendered in the console by the portal's own renderer.
 *
 * ─────────────────────────────────────────────────────────────────────────────
 *
 * **Why the console imports the portal rather than asking it.** An iframe of the
 * *live* portal can only show what has already been saved — "Save first", the screen
 * would say, to an owner trying to find out whether a change looks right before a
 * guest sees it. The field's page builders all preview the
 * draft (Omada, Tanaza, Smart WiFi Platform), and an owner who cannot see the result
 * of a keystroke is not using an editor.
 *
 * So the draft goes through `renderPage` — the same function that produces the
 * built portal, and the authority the Go renderer is a twin of — and the result is
 * shown in a sandboxed `srcdoc` frame. Nothing is sent anywhere, and what the frame
 * shows is the markup a guest's phone would receive, not a React imitation of it.
 *
 * The frame is `sandbox=""`: no script, no navigation out, no same-origin access.
 * Venue text is escaped by the renderer; the sandbox is there so that the one place
 * the console renders guest markup cannot be the place that finds out otherwise.
 */
import { localDate } from "@foyer/contract";
import type { Language, PortalContext, PortalStatus } from "@foyer/contract";
import { renderDocument } from "@foyer/portal/src/document.js";
import { renderPage } from "@foyer/portal/src/render.js";
import portalCss from "@foyer/portal/src/styles/portal.css?raw";

export type PreviewPage = "welcome" | "connected";

/*
 * Links do nothing in a preview. A tap on "I have a voucher" or on the venue's
 * Facebook link would navigate the frame somewhere it cannot follow, and the owner
 * would be looking at a broken frame instead of their page.
 */
const PREVIEW_CSS = "a,button,input{pointer-events:none}";

/** A guest who has just signed in, so the connected page has something to say. */
const SAMPLE_STATUS = (venueName: string): PortalStatus => ({
  state: "connected",
  venueName,
  plan: { name: "1 h", durationMinutes: 60, enforcedBy: "nas" } as PortalStatus["plan"],
});

export function previewDocument(opts: {
  context: PortalContext;
  lang: Language;
  page: PreviewPage;
  /** Where relative asset paths — the photograph — resolve. The portal's origin. */
  base: string | null;
  today?: string;
}): string {
  const { context, lang, page } = opts;
  const rendered = renderPage({
    lang,
    path: page === "welcome" ? "" : "connected/",
    context,
    status: page === "connected" ? SAMPLE_STATUS(context.venueName) : null,
    error: null,
    today: opts.today ?? localDate(),
  });
  if (!rendered) return "";
  const doc = renderDocument({ lang, title: rendered.title, body: rendered.body, css: portalCss + PREVIEW_CSS });
  return opts.base ? doc.replace("<head>", `<head>\n<base href="${escapeAttr(opts.base)}/">`) : doc;
}

/**
 * The document's weight as a guest's phone receives it: gzipped, stylesheet
 * included. Measured, not estimated — the budget is a gzip figure and the only
 * honest way to compare against it is to compress. `null` where the platform has
 * no `CompressionStream`, and the screen then shows nothing rather than a guess.
 */
export async function gzipBytes(text: string): Promise<number | null> {
  if (typeof CompressionStream === "undefined") return null;
  const stream = new Blob([text]).stream().pipeThrough(new CompressionStream("gzip"));
  const buf = await new Response(stream).arrayBuffer();
  return buf.byteLength;
}

/** The weight of the page without the preview's own additions. */
export function weighedDocument(opts: Parameters<typeof previewDocument>[0]): string {
  return previewDocument({ ...opts, base: null }).replace(PREVIEW_CSS, "");
}

function escapeAttr(s: string): string {
  return s.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;");
}
