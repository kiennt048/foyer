package portal

import (
	"html"
	"net/url"
	"slices"
	"strings"
)

// Server-side rendering of the guest documents.
//
// The TypeScript renderer (`web/portal/src/render.ts` + `pages/` + `document.ts`)
// is the authority for what a document says and how it is shaped. This file is
// its Go twin: the same five pages, the same fallbacks, the same allowlists,
// emitting the same structure — so a guest's phone gets a document carrying
// their venue's name, methods, terms and brand on first paint, with no client
// script and no second request.
//
// ── why a twin and not the artefact ──────────────────────────────────────────
//
// The embedded documents are pre-rendered with `context: null`, so every
// venue-named slot holds the catalogue default: "Foyer", both methods, catalogue
// terms. Serving them answers the wrong venue on every deployment that
// configured one. A twin that renders from `PortalSettings` answers the right
// one, per request, from the row the operator saved.
//
// ── what this deliberately does not do ───────────────────────────────────────
//
//   - No presence check and no driver call. Rendering answers what the venue
//     configured; `Look` answers whether this browser may act on it. The CNA
//     probes document URLs repeatedly while a guest reads, and a controller
//     round trip per probe is a denial of service the venue pays for.
//   - No grant creation and no credential check. Those are POST operations with
//     their own rate-limit bucket; a GET that could mint one would be a grant
//     for every prefetch, preview and crawler.
//   - No session minting. A document requested with no claim renders the venue's
//     defaults; minting a row for every such request would fill the session
//     table with browsers nobody sent.
//   - No stylesheet or script port. The envelope (doctype, head, inlined CSS,
//     enhancement script) stays the embedded artefact's; only the body slots
//     below are rendered here. The twin covers what varies per venue, not what
//     is identical on every deployment.
//
// ── the slots, and which page reads which ────────────────────────────────────
//
// Welcome reads everything: venue and network names, logo, photograph, methods,
// languages (switcher), terms. Voucher and account read venue, logo, languages,
// plus the failure echo (`?e=`, `?code=` on voucher only). Connected reads venue,
// logo, languages and the grant projection. Problem reads venue, logo, languages
// and the failure echo. A slot a page does not read is not rendered for it —
// terms never leave the welcome page, a plan never leaves the connected one.

// RenderInput is what one document needs. It mirrors the TypeScript
// `RenderInput`: the language from the path prefix, the page under it, the
// venue's settings narrowed to what a guest may see, the grant projection for
// the connected page, the failure echo from the last redirect, and the raw
// query carried verbatim onto the language links.
type RenderLang string

type RenderInput struct {
	Lang RenderLang
	Path string
	// Settings is nil where no row answers — an unconfigured site, or a
	// document requested with no claim behind it. Nil renders the catalogue
	// defaults, exactly as the embedded artefact does.
	Settings *RenderSettings
	Status   *RenderStatus
	Error    string
	Code     string
	Query    string
	// PhoneSent is `?sent=1` on the phone page: a code is on its way, so the code
	// form renders instead of the number form. It decides which form is shown and
	// nothing else — the code is checked against this browser's session.
	PhoneSent bool

	// Notice is the raw `?n=` — a success to read, such as a withdrawn marketing
	// consent. Allowlisted by RenderNotice before anything renders it.
	Notice string
}

// RenderSettings is the guest-visible narrowing of a site's portal settings.
// It carries no site id, no portal URL and no byte budgets — the operator's
// business, not the guest's — mirroring `portalContextFrom` in `internal/api`.
type RenderSettings struct {
	VenueName           string
	NetworkName         string
	LogoDataURI         string
	HeroImageURL        string
	Methods             []string
	Languages           []string
	Terms               map[string]string
	ClickThroughMinutes int
	// Design is the venue's page **already narrowed** by the caller to what a
	// guest may read today — hidden and out-of-date blocks are absent, not
	// flagged. Nil renders exactly the page Foyer rendered before designs existed.
	Design *Design
	// Data is what the sign-in forms ask for. Nil asks for nothing.
	Data *RenderData
	// PhoneCodeMinutes is how long this venue's phone codes work — the sentence
	// on the code form. Zero renders DefaultPhoneCodeMinutes.
	PhoneCodeMinutes int
}

// DefaultPhoneCodeMinutes is the code lifetime a page states when it has no
// venue's to state: `otp.DefaultLimits.CodeLifetime`, which `internal/api`'s
// tests hold this equal to (this package imports nothing from `otp`).
const DefaultPhoneCodeMinutes = 5

// RenderStatus is the grant projection the connected page reads. It mirrors
// `PortalStatus`: the state, the plan, the venue name echoed from the grant's
// own site, and the return link.
type RenderStatus struct {
	State       string
	Plan        *RenderPlan
	VenueName   string
	OriginalURL string
}

// RenderPlan is what the venue granted — never what the guest has used, which
// accounting may not have observed at all.
type RenderPlan struct {
	Name            string
	NameLang        string
	DurationMinutes *int
	DataAllowanceMB *int
	EnforcedBy      string
}

// RenderedPage is one document's title and body. The caller assembles the
// envelope; the title names the venue so the tab and the history entry do.
type RenderedPage struct {
	Title string
	Body  string
}

// RenderPage renders one page, or nil where no page answers that path. It
// mirrors the TypeScript `renderPage`: unknown paths are not documents, and a
// caller that serves a fallback for one would turn every mistyped URL into a
// venue page.
func RenderPage(in RenderInput) *RenderedPage {
	lang := in.Lang
	if lang != "en" && lang != "vi" {
		lang = "en"
	}
	page, ok := pageFor(in.Path)
	if !ok {
		return nil
	}
	t := renderTranslator(lang)
	ctx := buildRenderContext(lang, in, t)
	var body string
	switch page {
	case PageWelcome:
		body = renderWelcome(ctx)
	case PageVoucher:
		body = renderVoucher(ctx)
	case PageAccount:
		body = renderAccount(ctx)
	case PageConnected:
		body = renderConnected(ctx)
	case PageProblem:
		body = renderProblem(ctx)
	case PagePhone:
		body = renderPhone(ctx)
	default:
		return nil
	}
	return &RenderedPage{Title: pageTitle(page, ctx), Body: body}
}

// pageFor resolves the path under the language prefix to a page. It mirrors
// the TypeScript `findPage`: the empty path is the welcome page, trailing
// slashes are tolerated, anything else is not a document.
//
// Two values because the welcome page IS the empty string: a single return
// cannot tell "the welcome page" from "no page", and the caller guessing
// refuses the welcome page itself — the defect this comment exists to record.
func pageFor(path string) (Page, bool) {
	normalised := strings.Trim(path, "/")
	switch normalised {
	case "":
		return PageWelcome, true
	case "voucher":
		return PageVoucher, true
	case "account":
		return PageAccount, true
	case "connected":
		return PageConnected, true
	case "problem":
		return PageProblem, true
	case "phone":
		return PagePhone, true
	default:
		return "", false
	}
}

type renderContext struct {
	lang          RenderLang
	t             func(string) string
	venueName     string
	networkName   string
	logo          string
	heroURL       string
	methods       []string
	languages     []RenderLang
	terms         resolvedTerms
	query         string
	error         string
	submittedCode string
	status        *RenderStatus
	design        *Design
	phoneSent     bool
	data          *RenderData
	notice        string
	// codeMinutes is the venue's phone-code lifetime, never zero.
	codeMinutes int
}

type resolvedTerms struct {
	summary string
	lang    RenderLang
}

func buildRenderContext(lang RenderLang, in RenderInput, t func(string) string) renderContext {
	s := in.Settings
	ctx := renderContext{lang: lang, t: t, query: normaliseQuery(in.Query), codeMinutes: DefaultPhoneCodeMinutes}
	if s == nil {
		ctx.venueName = "Foyer"
		ctx.methods = []string{"voucher", "account"}
	} else {
		ctx.venueName = s.VenueName
		if ctx.venueName == "" {
			ctx.venueName = "Foyer"
		}
		ctx.networkName = s.NetworkName
		ctx.logo = safeLogo(s.LogoDataURI)
		ctx.heroURL = safeHeroURL(s.HeroImageURL)
		ctx.methods = append([]string(nil), s.Methods...)
		if len(ctx.methods) == 0 {
			ctx.methods = []string{"voucher", "account"}
		}
		ctx.design = s.Design
		ctx.data = s.Data
		if s.PhoneCodeMinutes > 0 {
			ctx.codeMinutes = s.PhoneCodeMinutes
		}
	}
	ctx.notice = RenderNotice(in.Notice)
	ctx.languages = resolveLanguages(lang, settingsLanguages(s))
	ctx.terms = resolveTerms(lang, t, settingsTerms(s))
	ctx.error = in.Error
	ctx.submittedCode = resolveSubmittedCode(in.Code)
	ctx.status = in.Status
	ctx.phoneSent = in.PhoneSent
	return ctx
}

func settingsLanguages(s *RenderSettings) []string {
	if s == nil {
		return nil
	}
	return s.Languages
}

func settingsTerms(s *RenderSettings) map[string]string {
	if s == nil {
		return nil
	}
	return s.Terms
}

// resolveLanguages mirrors the TypeScript `resolveLanguages`: intersect what
// the venue advertises with the catalogue, and always keep the language being
// read — the guest is looking at it, and a switcher implying otherwise lies
// about the page it is on. Order follows the catalogue, never the venue's
// array, so the control does not reshuffle between deployments.
func resolveLanguages(current RenderLang, advertised []string) []RenderLang {
	wanted := map[RenderLang]bool{}
	if len(advertised) == 0 {
		wanted["en"] = true
		wanted["vi"] = true
	} else {
		for _, l := range advertised {
			if l == "en" || l == "vi" {
				wanted[RenderLang(l)] = true
			}
		}
	}
	wanted[current] = true
	var out []RenderLang
	for _, l := range []RenderLang{"en", "vi"} {
		if wanted[l] {
			out = append(out, l)
		}
	}
	return out
}

// resolveTerms mirrors the TypeScript `resolveTerms` and the contract's
// resolution order: the venue's text for the language being read, else its text
// in any other language it supplied (carrying that language), else the
// catalogue default. Never a translation, never a merge, never empty.
func resolveTerms(lang RenderLang, t func(string) string, summaries map[string]string) resolvedTerms {
	order := []RenderLang{lang}
	for _, l := range []RenderLang{"en", "vi"} {
		if l != lang {
			order = append(order, l)
		}
	}
	for _, candidate := range order {
		if summary := strings.TrimSpace(summaries[string(candidate)]); summary != "" {
			return resolvedTerms{summary: summary, lang: candidate}
		}
	}
	return resolvedTerms{summary: t("welcome.termsSummary"), lang: lang}
}

// resolveSubmittedCode mirrors the TypeScript resolver: the rejected voucher
// code comes back normalised to the wire form and length-capped, so the worst
// a crafted link can do is pre-fill twelve characters of the voucher alphabet.
// Only the voucher code travels this way — a username in a URL is a username
// in every access log between here and the browser.
func resolveSubmittedCode(raw string) string {
	if raw == "" {
		return ""
	}
	clean := NormaliseCode(raw)
	if len(clean) > VoucherLength {
		clean = clean[:VoucherLength]
	}
	return clean
}

// normaliseQuery mirrors the TypeScript helper: `""` or `?…` and nothing in
// between. Relayed, never re-serialised — re-encoding would decode `+` as a
// space and corrupt a `url` parameter a NAS did not percent-encode.
func normaliseQuery(raw string) string {
	if raw == "" {
		return ""
	}
	trimmed := strings.TrimPrefix(raw, "?")
	if trimmed == "" {
		return ""
	}
	return "?" + trimmed
}

// safeLogo mirrors the TypeScript `safeLogo`: an inline raster image, or a
// same-origin path the portal serves itself — never a remote origin, never an
// SVG that can carry script onto a pre-authentication page.
func safeLogo(uri string) string {
	if uri == "" {
		return ""
	}
	if isDataLogo(uri) {
		return uri
	}
	if isSameOriginPath(uri) {
		return uri
	}
	return ""
}

func isDataLogo(uri string) bool {
	if !strings.HasPrefix(uri, "data:image/") {
		return false
	}
	comma := strings.Index(uri, ",")
	if comma < 0 {
		return false
	}
	meta, payload := uri[len("data:"):comma], uri[comma+1:]
	kind := strings.SplitN(meta, ";", 2)[0]
	switch kind {
	case "image/png", "image/jpeg", "image/webp", "image/gif":
	default:
		return false
	}
	if !strings.Contains(meta, ";base64") {
		return false
	}
	for _, r := range payload {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '+', r == '/', r == '=':
		default:
			return false
		}
	}
	return len(payload) > 0
}

// isSameOriginPath mirrors the TypeScript `safeHeroUrl`: root-relative, never
// protocol-relative, nothing that breaks out of the attribute it lands in.
func isSameOriginPath(v string) bool {
	if len(v) < 2 || v[0] != '/' || v[1] == '/' {
		return false
	}
	return !strings.ContainsAny(v, " \t\r\n\"'<>")
}

// safeHeroURL mirrors the TypeScript `safeHeroUrl`.
func safeHeroURL(raw string) string {
	if isSameOriginPath(raw) {
		return raw
	}
	return ""
}

// errorKey maps a failure reason to the catalogue key rendering it. Unknown
// reasons read `error.generic` — a reason with no copy must not blank the
// page, and the mapping is held to the catalogue by test, not by comment.
func renderErrorKey(reason string) string {
	// `Reasons()` rather than a second list: a reason added there and forgotten
	// here rendered `error.generic`, which is the drift `reason_test.go` exists for.
	if slices.Contains(Reasons(), Reason(reason)) {
		return "error." + reason
	}
	return "error.generic"
}

// pageTitle mirrors the TypeScript titles: the page, then the venue.
func pageTitle(page Page, ctx renderContext) string {
	switch page {
	case PageVoucher:
		return ctx.t("voucher.title") + " · " + ctx.venueName
	case PageAccount:
		return ctx.t("account.title") + " · " + ctx.venueName
	case PagePhone:
		return ctx.t("phone.title") + " · " + ctx.venueName
	case PageConnected:
		if ctx.status != nil && ctx.status.State == string(StateConnected) {
			return ctx.t("connected.title") + " · " + ctx.venueName
		}
		return ctx.t("problem.title") + " · " + ctx.venueName
	case PageProblem:
		return ctx.t("problem.title") + " · " + ctx.venueName
	default:
		return ctx.t("welcome.title") + " · " + ctx.venueName
	}
}

// queryFor carries the request's query onto a same-page link, so switching
// language keeps the NAS parameters, the return URL and a rejected voucher's
// `?e=`/`?code=`. Relayed verbatim — unsigned and attacker-controlled, still
// untrusted at the far end — onto a path built here, so it cannot change the
// origin. The renderer escapes the attribute.
func queryFor(query, lang, path string) string {
	return "/" + lang + "/" + path + query
}

func esc(s string) string { return html.EscapeString(s) }

func langLink(lang RenderLang, path, query string, t func(string) string) string {
	other := RenderLang("vi")
	if lang == "vi" {
		other = "en"
	}
	return `<div class="langs"><a class="lang-switch" href="` + esc(queryFor(query, string(other), path)) +
		`" lang="` + string(other) + `" hreflang="` + string(other) + `">` + esc(t("lang.name."+string(other))) + `</a></div>`
}

func masthead(ctx renderContext, path string) string {
	var b strings.Builder
	b.WriteString(`<header class="masthead"><div class="venue">`)
	if ctx.logo != "" {
		b.WriteString(`<img class="venue-mark" src="` + esc(ctx.logo) + `" alt="" aria-hidden="true" />`)
	} else {
		b.WriteString(markSVG)
	}
	b.WriteString(`<span class="venue-name">` + esc(ctx.venueName) + `</span></div>`)
	others := 0
	for _, l := range ctx.languages {
		if l != ctx.lang {
			others++
		}
	}
	if others > 0 {
		b.WriteString(langLink(ctx.lang, path, ctx.query, ctx.t))
	}
	b.WriteString(`</header>`)
	return b.String()
}

func errorNote(ctx renderContext, id string) string {
	if ctx.error == "" {
		return ""
	}
	msg := ctx.t(renderErrorKey(ctx.error))
	if msg == "" {
		msg = ctx.t("error.generic")
	}
	forAttr := ""
	if id != "" {
		forAttr = ` id="` + id + `"`
	}
	return `<p class="error" role="alert"` + forAttr + `><span>` + esc(msg) + `</span></p>`
}

func renderWelcome(ctx renderContext) string {
	var b strings.Builder
	b.WriteString(themeStyle(ctx) + `<div class="shell">`)
	b.WriteString(masthead(ctx, ""))
	b.WriteString(headlineH1(ctx))
	if ctx.networkName != "" {
		b.WriteString(`<p class="lede">` + esc(ctx.t("welcome.network")) + ` · ` + esc(ctx.networkName) + `</p>`)
	}
	// The TypeScript welcome page has always rendered its failure here; the twin
	// had nothing to render until a detail could be refused on this page.
	b.WriteString(errorNote(ctx, ""))
	if ctx.heroURL != "" {
		b.WriteString(`<img class="venue-hero" src="` + esc(ctx.heroURL) + `" alt="" width="900" height="200" decoding="async" />`)
	}
	// A notice is the one block above the ways in: one short line, the kind a
	// venue puts on the door. Everything longer waits below them, because a guest
	// who cannot find how to get online reads nothing else.
	b.WriteString(renderBlocks(ctx, "welcome", "blocks blocks-notice", func(k string) bool { return k == "notice" }))
	b.WriteString(`<p class="section-label">` + esc(ctx.t("welcome.choose")) + `</p><div class="methods">`)
	if hasMethod(ctx.methods, "voucher") {
		b.WriteString(`<a class="method" href="` + esc(queryFor(ctx.query, string(ctx.lang), "voucher/")) + `">` +
			`<span class="method-body"><span class="method-title">` + esc(ctx.t("welcome.voucher")) + `</span>` +
			`<span class="method-hint">` + esc(ctx.t("welcome.voucherHint")) + `</span></span></a>`)
	}
	if hasMethod(ctx.methods, "account") {
		b.WriteString(`<a class="method" href="` + esc(queryFor(ctx.query, string(ctx.lang), "account/")) + `">` +
			`<span class="method-body"><span class="method-title">` + esc(ctx.t("welcome.account")) + `</span>` +
			`<span class="method-hint">` + esc(ctx.t("welcome.accountHint")) + `</span></span></a>`)
	}
	if hasMethod(ctx.methods, "phone") {
		b.WriteString(`<a class="method" href="` + esc(queryFor(ctx.query, string(ctx.lang), "phone/")) + `">` +
			`<span class="method-body"><span class="method-title">` + esc(ctx.t("welcome.phone")) + `</span>` +
			`<span class="method-hint">` + esc(ctx.t("welcome.phoneHint")) + `</span></span></a>`)
	}
	b.WriteString(`</div>`)
	b.WriteString(renderBlocks(ctx, "welcome", "blocks", func(k string) bool { return k != "notice" }))
	b.WriteString(`<section class="terms"><h2>` + esc(ctx.t("welcome.termsHeading")) + `</h2>`)
	if ctx.terms.lang == ctx.lang {
		b.WriteString(`<p>` + esc(ctx.terms.summary) + `</p>`)
	} else {
		b.WriteString(`<p lang="` + string(ctx.terms.lang) + `">` + esc(ctx.terms.summary) + `</p>`)
	}
	b.WriteString(`<p class="boundary"><span>` + esc(ctx.t("welcome.boundary")) + `</span></p>`)
	if hasMethod(ctx.methods, "click-through") {
		b.WriteString(`<form class="agree" method="post" action="/api/v0/portal/agree" data-portal-form>` +
			`<input type="hidden" name="lang" value="` + string(ctx.lang) + `" />` + guestFields(ctx) +
			`<button type="submit" class="btn">` + esc(ctx.t("welcome.agree")) + `</button></form>`)
	}
	b.WriteString(`</section></div>`)
	return b.String()
}

func renderVoucher(ctx renderContext) string {
	var b strings.Builder
	b.WriteString(themeStyle(ctx) + `<div class="shell">`)
	b.WriteString(masthead(ctx, "voucher/"))
	b.WriteString(`<h1>` + esc(ctx.t("voucher.title")) + `</h1>`)
	b.WriteString(`<form method="post" action="/api/v0/portal/voucher" data-portal-form>` +
		`<input type="hidden" name="lang" value="` + string(ctx.lang) + `" />` +
		`<div class="field"><label for="code">` + esc(ctx.t("voucher.label")) + `</label>`)
	value := ""
	if ctx.submittedCode != "" {
		value = ` value="` + esc(groupCode(ctx.submittedCode)) + `"`
	}
	described := "code-help"
	if ctx.error != "" {
		described = "code-help code-error"
		b.WriteString(`<input id="code" name="code" type="text" class="code-input" required maxlength="14" autocomplete="off" autocapitalize="characters" autocorrect="off" spellcheck="false" inputmode="text" enterkeyhint="go" autofocus` + value + ` aria-invalid="true" aria-describedby="` + described + `" />`)
	} else {
		b.WriteString(`<input id="code" name="code" type="text" class="code-input" required maxlength="14" autocomplete="off" autocapitalize="characters" autocorrect="off" spellcheck="false" inputmode="text" enterkeyhint="go" autofocus` + value + ` aria-describedby="` + described + `" />`)
	}
	b.WriteString(`<p class="help" id="code-help">` + esc(ctx.t("voucher.help")) + `</p>`)
	b.WriteString(errorNote(ctx, "code-error"))
	b.WriteString(`</div>` + guestFields(ctx) + `<button class="btn" type="submit">` + esc(ctx.t("voucher.submit")) + `</button></form>`)
	b.WriteString(`<a class="back" href="` + esc(queryFor(ctx.query, string(ctx.lang), "")) + `">` + esc(ctx.t("voucher.back")) + `</a>`)
	b.WriteString(`</div>`)
	return b.String()
}

func renderAccount(ctx renderContext) string {
	var b strings.Builder
	b.WriteString(themeStyle(ctx) + `<div class="shell">`)
	b.WriteString(masthead(ctx, "account/"))
	b.WriteString(`<h1>` + esc(ctx.t("account.title")) + `</h1>`)
	b.WriteString(`<form method="post" action="/api/v0/portal/account" data-portal-form>` +
		`<input type="hidden" name="lang" value="` + string(ctx.lang) + `" />` +
		`<div class="field"><label for="username">` + esc(ctx.t("account.username")) + `</label>` +
		`<input id="username" name="username" type="text" required autocomplete="username" autocapitalize="none" autocorrect="off" spellcheck="false" enterkeyhint="next" autofocus` +
		ariaDescribed(ctx) + ` /></div>` +
		`<div class="field"><label for="password">` + esc(ctx.t("account.password")) + `</label>` +
		`<input id="password" name="password" type="password" required autocomplete="current-password" enterkeyhint="go"` +
		ariaDescribed(ctx) + ` />` +
		errorNote(ctx, "account-error") + `</div>` + guestFields(ctx) +
		`<button class="btn" type="submit">` + esc(ctx.t("account.submit")) + `</button></form>`)
	b.WriteString(`<a class="back" href="` + esc(queryFor(ctx.query, string(ctx.lang), "")) + `">` + esc(ctx.t("account.back")) + `</a>`)
	b.WriteString(`</div>`)
	return b.String()
}

func ariaDescribed(ctx renderContext) string {
	if ctx.error == "" {
		return ""
	}
	return ` aria-invalid="true" aria-describedby="account-error"`
}

func renderConnected(ctx renderContext) string {
	if ctx.status == nil || ctx.status.State != string(StateConnected) {
		return renderNotConnected(ctx, notConnectedMessage(ctx))
	}
	st := ctx.status
	var b strings.Builder
	b.WriteString(themeStyle(ctx) + `<div class="shell">`)
	b.WriteString(masthead(ctx, "connected/"))
	b.WriteString(`<p class="badge-ok">` + esc(ctx.t("connected.title")) + `</p>`)
	b.WriteString(`<h1>` + esc(ctx.t("connected.lede")) + `</h1>`)
	b.WriteString(noticeNote(ctx))
	if st.Plan != nil {
		b.WriteString(`<div class="facts">`)
		if st.Plan.Name != "" {
			if st.Plan.NameLang != "" && st.Plan.NameLang != string(ctx.lang) {
				b.WriteString(`<div class="fact"><span class="fact-label">` + esc(ctx.t("connected.plan")) + `</span><span class="fact-value"><span lang="` + esc(st.Plan.NameLang) + `">` + esc(st.Plan.Name) + `</span></span></div>`)
			} else {
				b.WriteString(`<div class="fact"><span class="fact-label">` + esc(ctx.t("connected.plan")) + `</span><span class="fact-value">` + esc(st.Plan.Name) + `</span></span></div>`)
			}
		}
		b.WriteString(`<div class="fact"><span class="fact-label">` + esc(ctx.t("connected.duration")) + `</span><span class="fact-value">` + esc(durationText(ctx, st.Plan.DurationMinutes)) + `</span></div>`)
		if st.Plan.DataAllowanceMB != nil {
			b.WriteString(`<div class="fact"><span class="fact-label">` + esc(ctx.t("connected.allowance")) + `</span><span class="fact-value">` + esc(allowanceText(*st.Plan.DataAllowanceMB)) + `</span></div>`)
		}
		b.WriteString(`</div>`)
		var notes []string
		if st.Plan.DataAllowanceMB != nil {
			notes = append(notes, ctx.t("connected.allowanceNote"))
		}
		switch st.Plan.EnforcedBy {
		case "unenforced":
			notes = append(notes, ctx.t("connected.unenforcedNote"))
		case "foyer-sampled":
			notes = append(notes, ctx.t("connected.sampledNote"))
		}
		if len(notes) > 0 {
			b.WriteString(`<p class="qualifier"><span>` + esc(strings.Join(notes, " ")) + `</span></p>`)
		}
	}
	// The venue's page after sign-in — the one place a link is a link, because the
	// guest has internet now. Before the way back to where they were going, which
	// is the guest's own intent and stays last.
	b.WriteString(renderBlocks(ctx, "connected", "blocks", func(string) bool { return true }))
	if st.OriginalURL != "" {
		if safe := SafeReturnURL(st.OriginalURL); safe != "" {
			u, err := url.Parse(safe)
			host := safe
			if err == nil {
				host = u.Host
			}
			b.WriteString(`<a class="continue" href="` + esc(safe) + `" rel="noopener noreferrer"><span><span class="continue-label">` +
				esc(ctx.t("connected.continue")) + `</span><br /><span class="continue-url">` + esc(host) + `</span></span></a>`)
		}
	}
	b.WriteString(`<p class="close-hint">` + esc(ctx.t("connected.closeHint")) + `</p>`)
	b.WriteString(withdrawForm(ctx))
	b.WriteString(`</div>`)
	return b.String()
}

func notConnectedMessage(ctx renderContext) string {
	if ctx.status == nil {
		return "connected.unknown"
	}
	switch ctx.status.State {
	case string(StateExpired):
		return "connected.expired"
	case string(StateRevoked):
		return "connected.revoked"
	case string(StateNotOnNetwork):
		return "connected.notOnNetwork"
	default:
		return "connected.unknown"
	}
}

func renderNotConnected(ctx renderContext, message string) string {
	var b strings.Builder
	b.WriteString(themeStyle(ctx) + `<div class="shell">`)
	b.WriteString(masthead(ctx, "connected/"))
	b.WriteString(`<h1>` + esc(ctx.t("problem.title")) + `</h1>`)
	b.WriteString(noticeNote(ctx))
	b.WriteString(`<p class="error" role="alert"><span>` + esc(ctx.t(message)) + `</span></p>`)
	b.WriteString(`<a class="back" href="` + esc(queryFor(ctx.query, string(ctx.lang), "")) + `">` + esc(ctx.t("connected.signIn")) + `</a>`)
	b.WriteString(`</div>`)
	return b.String()
}

func renderProblem(ctx renderContext) string {
	var b strings.Builder
	b.WriteString(themeStyle(ctx) + `<div class="shell">`)
	b.WriteString(masthead(ctx, "problem/"))
	b.WriteString(`<h1>` + esc(ctx.t("problem.title")) + `</h1>`)
	msg := ctx.t("error.generic")
	if ctx.error != "" {
		if m := ctx.t(renderErrorKey(ctx.error)); m != "" {
			msg = m
		}
	}
	b.WriteString(`<p class="error" role="alert"><span>` + esc(msg) + `</span></p>`)
	b.WriteString(`<a class="back" href="` + esc(queryFor(ctx.query, string(ctx.lang), "")) + `">` + esc(ctx.t("problem.back")) + `</a>`)
	b.WriteString(`</div>`)
	return b.String()
}

func hasMethod(methods []string, want string) bool {
	for _, m := range methods {
		if m == want {
			return true
		}
	}
	return false
}

func groupCode(code string) string {
	clean := NormaliseCode(code)
	var groups []string
	for i := 0; i < len(clean); i += 4 {
		end := i + 4
		if end > len(clean) {
			end = len(clean)
		}
		groups = append(groups, clean[i:end])
	}
	return strings.Join(groups, "-")
}

func durationText(ctx renderContext, minutes *int) string {
	if minutes == nil {
		return ctx.t("unit.unlimited")
	}
	if *minutes < 60 {
		return strings.TrimSpace(strings.Join([]string{itoa(*minutes), ctx.t("unit.minutes")}, " "))
	}
	h := float64(*minutes) / 60
	intH := int(h)
	var hs string
	if float64(intH) == h {
		hs = itoa(intH)
	} else {
		hs = strings.TrimRight(strings.TrimRight(floatToString(h), "0"), ".")
	}
	if intH == 1 && float64(intH) == h {
		return hs + " " + ctx.t("unit.hour")
	}
	return hs + " " + ctx.t("unit.hours")
}

func allowanceText(mb int) string {
	if mb >= 1000 {
		gb := float64(mb) / 1000
		intGB := int(gb)
		if float64(intGB) == gb {
			return itoa(intGB) + " GB"
		}
		s := floatToString(gb)
		if strings.Contains(s, ".") {
			s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
			if !strings.Contains(s, ".") {
				parts := strings.SplitN(floatToString(gb), ".", 2)
				if len(parts) == 2 {
					s = parts[0] + "." + string(parts[1][0])
				}
			} else {
				parts := strings.SplitN(s, ".", 2)
				s = parts[0] + "." + string(parts[1][0])
			}
		}
		return s + " GB"
	}
	return itoa(mb) + " MB"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		// Without this the loop never ends, and it was reached from every
		// connected page whose plan has a time limit or an allowance.
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

func floatToString(f float64) string {
	s := strings.TrimRight(strings.TrimRight(floatToStringPrec(f), "0"), ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

func floatToStringPrec(f float64) string {
	// One decimal place is all any duration or allowance ever needs.
	whole := int(f)
	frac := int(f*10) % 10
	if frac < 0 {
		frac = -frac
	}
	if frac == 0 {
		return itoa(whole)
	}
	return itoa(whole) + "." + itoa(frac)
}

// markSVG is Foyer's own mark: a doorway — the room passed through on the way
// in. Inlined here rather than read from the artefact so a venue with no logo
// renders identically whether or not any document was embedded.
const markSVG = `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" class="venue-mark"><path d="M4 21V5.5A1.5 1.5 0 0 1 5.5 4h8A1.5 1.5 0 0 1 15 5.5V21"></path><path d="M2.5 21h19"></path><path d="M15 9h3.5A1.5 1.5 0 0 1 20 10.5V21"></path><circle cx="11.75" cy="12.75" r="0.9" fill="currentColor" stroke="none"></circle></svg>`
