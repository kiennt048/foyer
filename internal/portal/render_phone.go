package portal

import (
	"strconv"
	"strings"
)

// renderPhone is the twin of the TypeScript `phone/` page: the number form, or —
// with `?sent=1` — the code form.
//
// Two forms on one document rather than two documents, because the second step is
// meaningless without the first and a guest who reloads the code page, or opens it
// in a fresh CNA, must be able to get back to the number without guessing a URL.
//
// Neither form echoes anything. The number is not put back in its field after a
// refusal (it is personal data and would travel in the query string), and the
// code is a credential.
func renderPhone(ctx renderContext) string {
	var b strings.Builder
	b.WriteString(themeStyle(ctx) + `<div class="shell">`)
	b.WriteString(masthead(ctx, "phone/"))
	if ctx.phoneSent {
		b.WriteString(`<h1>` + esc(ctx.t("phone.codeTitle")) + `</h1>`)
		b.WriteString(`<p class="lede">` + esc(sentLede(ctx)) + `</p>`)
		b.WriteString(`<form method="post" action="/api/v0/portal/phone/code" data-portal-form>` +
			`<input type="hidden" name="lang" value="` + string(ctx.lang) + `" />` +
			`<div class="field"><label for="otp">` + esc(ctx.t("phone.codeLabel")) + `</label>`)
		// `autocomplete="one-time-code"` is what lets iOS offer the code from the
		// message above the keyboard. Whether the captive-network sheet honours it
		// is UNVERIFIED.
		//
		// **`otp-input`, never `code-input`.** `code-input` is the voucher field's
		// class and the enhancement script groups whatever carries it into
		// `XXXX-XXXX-XXXX`; on this field that would turn `231086` into `2310-86`,
		// which a digits-only pattern refuses, so the browser would block the
		// submit before anything reached the server — phone sign-in would work
		// only with JavaScript off. The pattern
		// admits what `otp.NormaliseCode` admits (digits, spaces, hyphens, a
		// no-break space), so a pasted `231 086` or `231-086` submits with
		// JavaScript off too, and `maxlength` leaves room for it.
		b.WriteString(`<input id="otp" name="code" type="text" class="otp-input" required maxlength="12" ` +
			`inputmode="numeric" pattern="` + OTPPattern + `" autocomplete="one-time-code" enterkeyhint="go" autofocus` +
			phoneAria(ctx, "otp-error") + ` />`)
		b.WriteString(errorNote(ctx, "otp-error"))
		// The venue's guest-data fields and its marketing checkbox, less `phone`:
		// the number this code was sent to is the phone the venue collects.
		b.WriteString(`</div>` + guestFieldsExcept(ctx, "phone") +
			`<button class="btn" type="submit">` + esc(ctx.t("phone.verify")) + `</button></form>`)
		b.WriteString(`<a class="back" href="` + esc(queryFor("", string(ctx.lang), "phone/")) + `">` + esc(ctx.t("phone.again")) + `</a>`)
		b.WriteString(`</div>`)
		return b.String()
	}
	b.WriteString(`<h1>` + esc(ctx.t("phone.title")) + `</h1>`)
	b.WriteString(`<form method="post" action="/api/v0/portal/phone" data-portal-form>` +
		`<input type="hidden" name="lang" value="` + string(ctx.lang) + `" />` +
		`<div class="field"><label for="phone">` + esc(ctx.t("phone.label")) + `</label>` +
		`<input id="phone" name="phone" type="tel" required maxlength="32" autocomplete="tel-national" ` +
		`inputmode="tel" enterkeyhint="send" autofocus` + phoneAria(ctx, "phone-help phone-error") + ` />` +
		`<p class="help" id="phone-help">` + esc(ctx.t("phone.help")) + `</p>`)
	b.WriteString(errorNote(ctx, "phone-error"))
	b.WriteString(`</div><button class="btn" type="submit">` + esc(ctx.t("phone.send")) + `</button></form>`)
	b.WriteString(`<a class="back" href="` + esc(queryFor(ctx.query, string(ctx.lang), "")) + `">` + esc(ctx.t("phone.back")) + `</a>`)
	b.WriteString(`</div>`)
	return b.String()
}

// OTPPattern is the code field's `pattern`: digits and the separators a guest may
// paste around them. It must admit everything `otp.NormaliseCode` accepts, or the
// browser refuses a code the server would have taken.
const OTPPattern = `[0-9\s\-]*`

// sentLede is the code form's opening sentence with the venue's code lifetime in
// it. The catalogue carries `{minutes}`; a lifetime an administrator set to two
// minutes must not be announced as five.
func sentLede(ctx renderContext) string {
	return strings.ReplaceAll(ctx.t("phone.sentLede"), "{minutes}", strconv.Itoa(ctx.codeMinutes))
}

func phoneAria(ctx renderContext, withError string) string {
	if ctx.error == "" {
		if strings.HasPrefix(withError, "phone-help") {
			return ` aria-describedby="phone-help"`
		}
		return ""
	}
	return ` aria-invalid="true" aria-describedby="` + withError + `"`
}
