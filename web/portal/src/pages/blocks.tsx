/** @jsxImportSource preact */
import { accentCss, isLink, resolveBlockText, resolveHeadline } from "@foyer/contract";
import type { PortalBlock, PortalBlockKind, PortalBlockPlacement } from "@foyer/contract";
import { LANGUAGES, type MessageKey } from "../i18n/index.js";
import type { PageContext } from "./types.js";

/**
 * The venue's own content: the brand colour, the headline, and the blocks.
 *
 * The markup is the spec's, character for character, because the Go renderer
 * emits the same structure from the same rules
 * (`docs/architecture.md`). Every string is
 * venue-authored and reaches a pre-authentication page, so everything here is
 * text: Preact escapes it, and a body's line breaks survive through
 * `white-space: pre-line` rather than through markup.
 */

/**
 * The brand colour, as the first thing in the body. Nothing when there is none.
 *
 * A text child rather than raw HTML: the stylesheet is built only from colours that
 * matched `#rrggbb` twice over (the contract's pattern and `safeDesign`), so it holds
 * no character an escaper would touch, and nothing venue-typed reaches it.
 */
export function Theme({ ctx }: { ctx: PageContext }) {
  const accent = ctx.design?.accent;
  if (!accent) return null;
  return <style>{accentCss(accent)}</style>;
}

/** The welcome heading: the venue's, in the fallback language if it must be. */
export function headline(ctx: PageContext): { text: string; lang: string | undefined } {
  const h = resolveHeadline(ctx.design, ctx.lang, LANGUAGES);
  if (!h) return { text: ctx.t("welcome.title"), lang: undefined };
  return { text: h.text, lang: h.lang === ctx.lang ? undefined : h.lang };
}

/**
 * The blocks for one place on one page, in design order, inside one container —
 * or nothing at all, so an empty design adds no bytes and no spacing.
 */
export function Blocks({
  ctx,
  placement,
  only,
  except,
  class: className = "blocks",
}: {
  ctx: PageContext;
  placement: PortalBlockPlacement;
  only?: PortalBlockKind;
  except?: PortalBlockKind;
  class?: string;
}) {
  const rendered = (ctx.design?.blocks ?? [])
    .filter((b) => b.placement === placement)
    .filter((b) => (only ? b.kind === only : true))
    .filter((b) => (except ? b.kind !== except : true))
    .map((b) => renderBlock(ctx, b))
    .filter((node) => node !== null);
  if (rendered.length === 0) return null;
  return <div class={className}>{rendered}</div>;
}

function renderBlock(ctx: PageContext, block: PortalBlock) {
  const resolved = resolveBlockText(block, ctx.lang, LANGUAGES);
  const lang = resolved && resolved.lang !== ctx.lang ? resolved.lang : undefined;
  const text = resolved?.text;
  const title = text?.title?.trim();
  const body = text?.body?.trim();
  const key = block.id;

  switch (block.kind) {
    case "text":
      if (!resolved) return null;
      return (
        <section key={key} class="block block-text" lang={lang}>
          {title ? <h2 class="block-title">{title}</h2> : null}
          {body ? <p class="block-body">{body}</p> : null}
        </section>
      );
    case "notice":
      if (!resolved) return null;
      return (
        <section key={key} class="block block-notice" lang={lang}>
          {title ? <p class="block-title">{title}</p> : null}
          {body ? <p class="block-body">{body}</p> : null}
        </section>
      );
    case "offer": {
      if (!resolved) return null;
      const code = block.code?.trim();
      return (
        <section key={key} class="block block-offer" lang={lang}>
          {title ? <h2 class="block-title">{title}</h2> : null}
          {body ? <p class="block-body">{body}</p> : null}
          {code ? (
            <p class="block-code">
              <span class="block-code-label">{ctx.t("block.code")}</span>
              <span class="block-code-value">{code}</span>
            </p>
          ) : null}
        </section>
      );
    }
    case "list": {
      if (!resolved) return null;
      const items = (text?.items ?? []).filter((i) => i.label.trim());
      return (
        <section key={key} class="block block-list" lang={lang}>
          {title ? <h2 class="block-title">{title}</h2> : null}
          {items.length > 0 ? (
            <ul class="block-items">
              {items.map((item, i) => (
                <li key={i}>
                  <span class="block-item-label">{item.label.trim()}</span>
                  {item.value?.trim() ? <span class="block-item-value">{item.value.trim()}</span> : null}
                </li>
              ))}
            </ul>
          ) : null}
        </section>
      );
    }
    case "button": {
      // A link is re-checked where it becomes an `href`, as `originalUrl` is: the
      // server refused anything else, and this is the last gate before the DOM.
      const label = text?.label?.trim() || title;
      if (!label || !isLink(block.url)) return null;
      return (
        <a key={key} class="block block-button" href={block.url!} rel="noopener noreferrer" lang={lang}>
          {label}
        </a>
      );
    }
    case "social": {
      const links = (block.links ?? []).filter((l) => isLink(l.url));
      if (links.length === 0) return null;
      return (
        <section key={key} class="block block-social" lang={lang}>
          {title ? <h2 class="block-title">{title}</h2> : null}
          <ul class="block-links">
            {links.map((l, i) => (
              <li key={i}>
                <a class="block-link" href={l.url} rel="noopener noreferrer">
                  {ctx.t(`social.${l.network}` as MessageKey)}
                </a>
              </li>
            ))}
          </ul>
        </section>
      );
    }
    default:
      return null;
  }
}
