package portal

import (
	"regexp"
	"strings"
	"unicode"
)

// The venue's own page content, as the renderer reads it.
//
// These are the renderer's types, not the contract's: this package is the twin of
// the TypeScript renderer and does not import the generated HTTP types. The API
// converts `gen.PortalDesign` into a `Design` **after narrowing it** — hidden and
// out-of-date blocks never reach this package — so nothing here reads a date or a
// visibility switch. The rules live in `web/contract/src/design.ts`; the
// TypeScript implementation is `web/portal/src/pages/blocks.tsx` over
// `@foyer/contract`'s `resolveBlockText` and `resolveHeadline`.
//
// **Every string in a design is venue-authored and reaches a pre-authentication
// page**, so every string is text: escaped where it is written, and a body keeps
// its line breaks through CSS (`white-space: pre-line`) rather than markup.

// Design is a narrowed `PortalDesign`.
type Design struct {
	// Accent is `#rrggbb` or empty for Foyer's own colour.
	Accent string
	// Headline is per language, `en`/`vi`.
	Headline map[string]string
	Blocks   []Block
}

// Block is one content block. Kind and Placement carry the contract's enum values.
type Block struct {
	ID        string
	Kind      string
	Placement string
	Code      string
	URL       string
	Links     []SocialLink
	Content   map[string]BlockText
}

type SocialLink struct {
	Network string
	URL     string
}

type BlockText struct {
	Title, Body, Label string
	Items              []BlockItem
}

type BlockItem struct {
	Label, Value string
}

// TrimText is JavaScript's `String.prototype.trim`, because "blank" has to mean the
// same thing on both sides of the language seam. Go's `strings.TrimSpace` differs
// at exactly two code points: it trims U+0085 (NEL), which JavaScript keeps, and
// keeps U+FEFF (BOM), which JavaScript trims. A title of one BOM would be text to
// one renderer and nothing to the other.
func TrimText(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		if r == '\uFEFF' {
			return true
		}
		if r == '\u0085' {
			return false
		}
		return unicode.IsSpace(r)
	})
}

// hasText mirrors `hasText`: any of title, body or label non-blank, or an item
// with a non-blank label.
func hasText(t BlockText) bool {
	if TrimText(t.Title) != "" || TrimText(t.Body) != "" || TrimText(t.Label) != "" {
		return true
	}
	for _, it := range t.Items {
		if TrimText(it.Label) != "" {
			return true
		}
	}
	return false
}

// languageOrder is the language being read, then the rest in catalogue order —
// the terms resolution order, applied to marketing text.
func languageOrder(lang RenderLang) []RenderLang {
	order := []RenderLang{lang}
	for _, l := range []RenderLang{"en", "vi"} {
		if l != lang {
			order = append(order, l)
		}
	}
	return order
}

// resolveBlockText mirrors `resolveBlockText`: the block's words in the language
// being read if it has any, else the other language's, carrying that language.
// Never a translation and never a merge of the two.
func resolveBlockText(b Block, lang RenderLang) (BlockText, RenderLang, bool) {
	for _, candidate := range languageOrder(lang) {
		if t, ok := b.Content[string(candidate)]; ok && hasText(t) {
			return t, candidate, true
		}
	}
	return BlockText{}, "", false
}

// resolveHeadline mirrors `resolveHeadline`; ok=false falls back to the
// catalogue's `welcome.title`.
func resolveHeadline(d *Design, lang RenderLang) (string, RenderLang, bool) {
	if d == nil {
		return "", "", false
	}
	for _, candidate := range languageOrder(lang) {
		if text := TrimText(d.Headline[string(candidate)]); text != "" {
			return text, candidate, true
		}
	}
	return "", "", false
}

// A link, per the spec: http(s) with a host and no userinfo — the same pattern as
// `PortalStatus.originalUrl` — or a `tel:` number. Nothing else: every one of these
// lands in an `href` on a page a guest has just been asked to trust, and
// `javascript:` parses as a URL. `mailto:` is excluded because
// Android's captive-portal WebView hard-blocks it (`docs/rules/frontend.md`), so
// an "email us" link would be a button that does nothing on half the phones.
//
// `ws` is JavaScript's `\s`, spelled out: RE2's `\s` is ASCII only, so a no-break
// space inside a URL would pass here and be refused by the mock and the console.
// The patterns are otherwise the spec's character for character.
const ws = `\t\n\x0B\f\r\p{Z}\x{FEFF}`

var (
	httpLink = regexp.MustCompile(`^https?://[^/?#@` + ws + `]+(?:[/?#][^` + ws + `]*)?$`)
	telLink  = regexp.MustCompile(`^tel:\+?[0-9][0-9 .()-]{2,19}$`)
)

// IsLink is `isLink` in `@foyer/contract`. The API refuses a design carrying
// anything else (`not-a-link`); the renderer re-checks where the value becomes an
// `href`, as `SafeReturnURL` does for `originalUrl` — the last gate before the DOM.
func IsLink(u string) bool {
	return u != "" && (httpLink.MatchString(u) || telLink.MatchString(u))
}

func isSafeLink(u string) bool { return IsLink(u) }

// ── markup ───────────────────────────────────────────────────────────────────

// langAttr is ` lang="xx"` when the text came from the fallback language.
func langAttr(from, reading RenderLang) string {
	if from == "" || from == reading {
		return ""
	}
	return ` lang="` + string(from) + `"`
}

// renderBlocks is the container for one place on one page: the blocks with that
// placement, filtered by kind, in design order — or nothing at all, so an empty
// design adds no bytes and no spacing.
func renderBlocks(ctx renderContext, placement, class string, keep func(kind string) bool) string {
	if ctx.design == nil {
		return ""
	}
	var inner strings.Builder
	for _, b := range ctx.design.Blocks {
		if b.Placement != placement || !keep(b.Kind) {
			continue
		}
		inner.WriteString(renderBlock(ctx, b))
	}
	if inner.Len() == 0 {
		return ""
	}
	return `<div class="` + class + `">` + inner.String() + `</div>`
}

func renderBlock(ctx renderContext, b Block) string {
	text, from, resolved := resolveBlockText(b, ctx.lang)
	lang := langAttr(from, ctx.lang)
	title := TrimText(text.Title)
	body := TrimText(text.Body)

	part := func(tag, class, value string) string {
		if value == "" {
			return ""
		}
		return `<` + tag + ` class="` + class + `">` + esc(value) + `</` + tag + `>`
	}

	switch b.Kind {
	case "text":
		if !resolved {
			return ""
		}
		return `<section class="block block-text"` + lang + `>` +
			part("h2", "block-title", title) + part("p", "block-body", body) + `</section>`
	case "notice":
		if !resolved {
			return ""
		}
		return `<section class="block block-notice"` + lang + `>` +
			part("p", "block-title", title) + part("p", "block-body", body) + `</section>`
	case "offer":
		if !resolved {
			return ""
		}
		out := `<section class="block block-offer"` + lang + `>` +
			part("h2", "block-title", title) + part("p", "block-body", body)
		if code := TrimText(b.Code); code != "" {
			out += `<p class="block-code"><span class="block-code-label">` + esc(ctx.t("block.code")) +
				`</span><span class="block-code-value">` + esc(code) + `</span></p>`
		}
		return out + `</section>`
	case "list":
		if !resolved {
			return ""
		}
		var items strings.Builder
		for _, it := range text.Items {
			label := TrimText(it.Label)
			if label == "" {
				continue
			}
			items.WriteString(`<li><span class="block-item-label">` + esc(label) + `</span>`)
			if value := TrimText(it.Value); value != "" {
				items.WriteString(`<span class="block-item-value">` + esc(value) + `</span>`)
			}
			items.WriteString(`</li>`)
		}
		out := `<section class="block block-list"` + lang + `>` + part("h2", "block-title", title)
		if items.Len() > 0 {
			out += `<ul class="block-items">` + items.String() + `</ul>`
		}
		return out + `</section>`
	case "button":
		// A label, else the title; and a link that passes the allowlist here,
		// where it becomes an `href`. Either missing, and there is no button.
		label := TrimText(text.Label)
		if label == "" {
			label = title
		}
		if label == "" || !isSafeLink(b.URL) {
			return ""
		}
		return `<a class="block block-button" href="` + esc(b.URL) + `" rel="noopener noreferrer"` + lang + `>` +
			esc(label) + `</a>`
	case "social":
		// Needs at least one link; its title is optional. The labels are the
		// catalogue's, never the venue's, so a network always reads the same.
		var links strings.Builder
		for _, l := range b.Links {
			if !isSafeLink(l.URL) {
				continue
			}
			links.WriteString(`<li><a class="block-link" href="` + esc(l.URL) + `" rel="noopener noreferrer">` +
				esc(ctx.t("social."+l.Network)) + `</a></li>`)
		}
		if links.Len() == 0 {
			return ""
		}
		return `<section class="block block-social"` + lang + `>` + part("h2", "block-title", title) +
			`<ul class="block-links">` + links.String() + `</ul></section>`
	default:
		return ""
	}
}

// themeStyle is the accent stylesheet, the first thing in every page's body, or
// nothing where the venue chose no colour.
func themeStyle(ctx renderContext) string {
	if ctx.design == nil || !ValidAccent(ctx.design.Accent) {
		return ""
	}
	return `<style>` + AccentCSS(ctx.design.Accent) + `</style>`
}

// headlineH1 is the welcome page's heading: the venue's, carrying its language
// when it is the fallback's, else the catalogue's.
func headlineH1(ctx renderContext) string {
	if text, from, ok := resolveHeadline(ctx.design, ctx.lang); ok {
		return `<h1` + langAttr(from, ctx.lang) + `>` + esc(text) + `</h1>`
	}
	return `<h1>` + esc(ctx.t("welcome.title")) + `</h1>`
}
