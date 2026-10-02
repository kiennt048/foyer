package portal

import "strings"

// What the sign-in forms ask for, rendered. The twin of
// `web/portal/src/pages/details.tsx`, and the same rules:
//
//   - **Only the fields the venue asked for**, in its order, each marked optional
//     unless the venue made it required — and a required one carries `required`, so
//     a browser stops an empty submit before the server has to.
//   - **The marketing checkbox is never pre-ticked** and there is no input that
//     could make it so. Its sentence is the venue's own, resolved like the terms;
//     with no sentence in any language there is no checkbox at all. The hidden
//     `consentVersion` beside it is the SHA-256 of exactly the sentence rendered, so
//     the record names what the guest read.
//   - **Nothing a guest typed is rendered back.** A refused form comes back empty
//     with a reason; the values would have had to travel in the address.

// RenderData is the venue's `PortalDataCollection`, for the renderer.
type RenderData struct {
	Fields []RenderField
	// ConsentText and ConsentVersions are per language, and empty unless the venue
	// is asking for marketing consent.
	ConsentText     map[string]string
	ConsentVersions map[string]string
}

// RenderField is one field and whether it is required.
type RenderField struct {
	Field    string
	Required bool
}

// ConsentAsked says whether the venue asks for marketing consent at all, which is
// what puts the withdrawal button on the connected page.
func (d *RenderData) consentAsked() bool {
	return d != nil && len(d.ConsentText) > 0
}

type resolvedConsent struct {
	text, version string
	lang          RenderLang
}

// resolveConsent mirrors `resolveTerms`, with no catalogue fallback: there is no
// default promise the portal could make on the venue's behalf.
func resolveConsent(lang RenderLang, d *RenderData) (resolvedConsent, bool) {
	if d == nil {
		return resolvedConsent{}, false
	}
	order := []RenderLang{lang}
	for _, l := range []RenderLang{"en", "vi"} {
		if l != lang {
			order = append(order, l)
		}
	}
	for _, l := range order {
		text := strings.TrimSpace(d.ConsentText[string(l)])
		version := d.ConsentVersions[string(l)]
		if text != "" && version != "" {
			return resolvedConsent{text: text, version: version, lang: l}, true
		}
	}
	return resolvedConsent{}, false
}

// guestFields is the markup every authenticating form carries before its button.
func guestFields(ctx renderContext) string { return guestFieldsExcept(ctx, "") }

// guestFieldsExcept is guestFields less one field the form already has — phone
// sign-in's code form, whose number was just proved.
func guestFieldsExcept(ctx renderContext, skip string) string {
	d := ctx.data
	if d == nil {
		return ""
	}
	var b strings.Builder
	for _, f := range d.Fields {
		if f.Field == skip {
			continue
		}
		var attrs string
		switch f.Field {
		case "name":
			attrs = `type="text" autocomplete="name" maxlength="120"`
		case "phone":
			attrs = `type="tel" autocomplete="tel" inputmode="tel" maxlength="32"`
		case "email":
			attrs = `type="email" autocomplete="email" inputmode="email" maxlength="254" autocapitalize="none" spellcheck="false"`
		default:
			continue
		}
		id := "g-" + f.Field
		b.WriteString(`<div class="field"><label for="` + id + `">` + esc(ctx.t("details."+f.Field)))
		if !f.Required {
			b.WriteString(` <span class="optional">(` + esc(ctx.t("details.optional")) + `)</span>`)
		}
		b.WriteString(`</label><input id="` + id + `" name="` + f.Field + `" ` + attrs)
		if f.Required {
			b.WriteString(` required`)
		}
		b.WriteString(` /></div>`)
	}
	if c, ok := resolveConsent(ctx.lang, d); ok {
		b.WriteString(`<div class="consent"><input type="checkbox" id="g-consent" name="marketingConsent" value="yes" />`)
		if c.lang == ctx.lang {
			b.WriteString(`<label for="g-consent">`)
		} else {
			b.WriteString(`<label for="g-consent" lang="` + string(c.lang) + `">`)
		}
		b.WriteString(esc(c.text) + `</label><input type="hidden" name="consentVersion" value="` + esc(c.version) + `" /></div>`)
	}
	return b.String()
}

// noticeNote is a success carried through a `303`, as `?n=`.
func noticeNote(ctx renderContext) string {
	if ctx.notice == "" {
		return ""
	}
	return `<p class="notice" role="status"><span>` + esc(ctx.t("notice."+ctx.notice)) + `</span></p>`
}

// withdrawForm is the guest's own way out, on the connected page, wherever the
// venue asks for marketing consent. Shown whether or not this device consented:
// the page does not know, and must not be the thing that tells a stranger.
func withdrawForm(ctx renderContext) string {
	if !ctx.data.consentAsked() {
		return ""
	}
	return `<form class="withdraw" method="post" action="/api/v0/portal/marketing-consent/withdraw" data-portal-form>` +
		`<input type="hidden" name="lang" value="` + string(ctx.lang) + `" />` +
		`<p class="help">` + esc(ctx.t("connected.withdrawHint")) + `</p>` +
		`<button type="submit" class="btn-quiet">` + esc(ctx.t("connected.withdraw")) + `</button></form>`
}

// RenderNotice allowlists `?n=`: only notices the catalogue can say reach a page.
func RenderNotice(raw string) string {
	for _, n := range Notices() {
		if string(n) == raw {
			return raw
		}
	}
	return ""
}
