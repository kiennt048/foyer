import { useEffect, useMemo, useState } from "react";
import type { Language, PortalBlockPlacement, PortalContext } from "@foyer/contract";
import { budgetBytes } from "../../format.js";
import { t } from "../../i18n/portal.js";
import { Pill } from "../../ui/primitives.js";
import { LANGUAGE_NAME } from "./PageBuilder.js";
import { gzipBytes, previewDocument, weighedDocument } from "./preview.js";

type Frame = "phone" | "window";

/**
 * The draft, as a guest will see it, beside the form that makes it.
 *
 * Rendered from what is typed — not from what is saved — by the portal's own
 * renderer (`preview.ts` says why that is the only honest source). Two sizes, the
 * ones that matter: a phone, and the iOS/macOS sign-in window that cannot be
 * resized. The page shown follows the one being edited, so an owner working on
 * the page after sign-in sees the page after sign-in.
 *
 * The weight under it is **measured** — the whole document, stylesheet included,
 * gzipped in the browser — because the budget is a gzip figure and every guest pays
 * it on every launch.
 */
export function LivePreview({
  context,
  languages,
  lang,
  onLang,
  page,
  onPage,
  base,
  liveUrl,
  budget,
  dirty,
  today,
}: {
  context: PortalContext;
  languages: Language[];
  /** The guest language shown. Follows the language being written in, and can be changed here. */
  lang: Language;
  onLang: (l: Language) => void;
  page: PortalBlockPlacement;
  onPage: (p: PortalBlockPlacement) => void;
  base: string | null;
  liveUrl: string | null;
  budget: number;
  dirty: boolean;
  /** The site's calendar day, which decides whether a dated block is shown. */
  today?: string;
}) {
  const [frame, setFrame] = useState<Frame>("phone");
  const shownLang = languages.includes(lang) ? lang : (languages[0] ?? "en");

  const doc = useMemo(
    () => previewDocument({ context, lang: shownLang, page, base, today }),
    [context, shownLang, page, base, today],
  );

  /* The heaviest page a guest can be sent is the welcome page — every guest gets it,
     and it carries the terms. Weighed in the language being previewed. */
  const [bytes, setBytes] = useState<number | null>(null);
  useEffect(() => {
    let live = true;
    const handle = setTimeout(() => {
      void gzipBytes(weighedDocument({ context, lang: shownLang, page: "welcome", base: null })).then((n) => {
        if (live) setBytes(n);
      });
    }, 250);
    return () => {
      live = false;
      clearTimeout(handle);
    };
  }, [context, shownLang]);

  return (
    <section className="card portal-section live-preview" aria-label={t("portal.previewing")}>
      <div className="preview-head">
        <h2 className="section-title">{t("portal.previewing")}</h2>
        {dirty ? <Pill level="caution-minor" label={t("portal.previewing.unsaved")} /> : null}
      </div>
      <p className="section-lede">{t("portal.previewing.draft")}</p>

      <div className="preview-controls">
        <div className="segmented segmented-sm" role="group" aria-label={t("portal.previewing.page")}>
          {(["welcome", "connected"] as const).map((p) => (
            <button key={p} type="button" className="segment" aria-pressed={page === p} onClick={() => onPage(p)}>
              {t(p === "welcome" ? "portal.place.welcome" : "portal.place.connected")}
            </button>
          ))}
        </div>
        <div className="segmented segmented-sm" role="group" aria-label={t("portal.previewing")}>
          {(["phone", "window"] as const).map((f) => (
            <button key={f} type="button" className="segment" aria-pressed={frame === f} onClick={() => setFrame(f)}>
              {t(f === "phone" ? "portal.previewing.phone" : "portal.previewing.window")}
            </button>
          ))}
        </div>
        {/* A venue with one language has no second one to look at — the same rule the
            portal follows when it renders no switcher. */}
        {languages.length > 1 ? (
          <div className="segmented segmented-sm" role="group" aria-label={t("portal.previewing.language")}>
            {languages.map((l) => (
              <button key={l} type="button" className="segment" aria-pressed={shownLang === l} onClick={() => onLang(l)}>
                {LANGUAGE_NAME[l]}
              </button>
            ))}
          </div>
        ) : null}
      </div>

      <div className="preview-stage" data-frame={frame}>
        <div className="preview-device">
          <iframe
            title={t("portal.preview.label")}
            sandbox=""
            srcDoc={doc}
            tabIndex={-1}
            loading="lazy"
          />
        </div>
      </div>

      <div className="weight-row">
        {bytes !== null ? (
          <Pill
            level={bytes > budget ? "urgent" : "neutral"}
            label={t("portal.weight.page", budgetBytes(bytes), budgetBytes(budget))}
          />
        ) : null}
      </div>
      {bytes !== null && bytes > budget ? (
        <p className="field-error" role="alert">
          {t("portal.weight.over")}
        </p>
      ) : null}

      {liveUrl ? (
        <a className="btn btn-sm" href={liveUrl} target="_blank" rel="noreferrer">
          {t("portal.previewing.live")}
        </a>
      ) : null}
    </section>
  );
}
