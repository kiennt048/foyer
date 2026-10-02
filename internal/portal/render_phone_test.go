package portal

import (
	"regexp"
	"strings"
	"testing"
)

// The code form, as the Go twin renders it. The browser behaviour — typing and pasting into the enhanced field
// and reaching the connected page — is `web/e2e/tests/portal/phone-code.spec.ts`;
// what is here is the markup that behaviour depends on.

func codeForm(t *testing.T, lang RenderLang, set *RenderSettings) string {
	t.Helper()
	p := RenderPage(RenderInput{Lang: lang, Path: "phone/", Settings: set, PhoneSent: true})
	if p == nil {
		t.Fatal("no phone page")
	}
	return p.Body
}

var otpInputTag = regexp.MustCompile(`<input id="otp"[^>]*>`)

// TestTheCodeFieldIsNotTheVoucherField: `code-input` is the class the enhancement
// script groups into `XXXX-XXXX-XXXX`, and on the code field that grouping is what
// made the browser refuse a correct code.
func TestTheCodeFieldIsNotTheVoucherField(t *testing.T) {
	tag := otpInputTag.FindString(codeForm(t, "en", nil))
	if tag == "" {
		t.Fatal("no code field")
	}
	if strings.Contains(tag, "code-input") {
		t.Fatalf("the code field carries the voucher class, so the enhancer groups it as a voucher: %s", tag)
	}
	if !strings.Contains(tag, `class="otp-input"`) {
		t.Fatalf("the code field lost its own class, so the enhancer cannot find it: %s", tag)
	}
}

// TestTheCodeFieldAdmitsWhatTheServerAccepts: with JavaScript off the browser's
// own `pattern` check is the only thing between a pasted code and the server, so
// everything `otp.NormaliseCode` takes must pass it — and `maxlength` must not cut
// a pasted code short first.
func TestTheCodeFieldAdmitsWhatTheServerAccepts(t *testing.T) {
	tag := otpInputTag.FindString(codeForm(t, "en", nil))
	m := regexp.MustCompile(`pattern="([^"]*)"`).FindStringSubmatch(tag)
	if m == nil {
		t.Fatalf("no pattern: %s", tag)
	}
	// HTML anchors a pattern at both ends, and compiles it as an ECMAScript
	// regular expression, whose `\s` includes the no-break space that RE2's does
	// not — so that one difference is spelled out for Go.
	re := regexp.MustCompile(`^(?:` + strings.ReplaceAll(m[1], `\s`, `\s\x{00a0}`) + `)$`)
	for _, typed := range []string{"231086", "231 086", "231-086", "231 086", " 231 086 "} {
		if !re.MatchString(typed) {
			t.Errorf("the pattern %q refuses %q, which the server accepts", m[1], typed)
		}
	}
	for _, bad := range []string{"23108a", "2310-86x"} {
		if re.MatchString(bad) {
			t.Errorf("the pattern %q admits %q", m[1], bad)
		}
	}
	if !strings.Contains(tag, `maxlength="12"`) {
		t.Errorf("maxlength leaves no room for a pasted, spaced code: %s", tag)
	}
}

// TestTheCodeFormStatesTheVenuesLifetime: the sentence said five minutes in both
// languages while an administrator could set anything from one to ten.
func TestTheCodeFormStatesTheVenuesLifetime(t *testing.T) {
	set := &RenderSettings{VenueName: "Cà phê", Methods: []string{"phone"}, PhoneCodeMinutes: 2}
	if en := codeForm(t, "en", set); !strings.Contains(en, "It works for 2 min.") || strings.Contains(en, "5 min") {
		t.Errorf("en code form does not say the venue's two minutes:\n%s", en)
	}
	if vi := codeForm(t, "vi", set); !strings.Contains(vi, "hiệu lực trong 2 phút") || strings.Contains(vi, "5 phút") {
		t.Errorf("vi code form does not say the venue's two minutes:\n%s", vi)
	}
	if def := codeForm(t, "en", nil); !strings.Contains(def, "It works for 5 min.") {
		t.Errorf("with no venue the product default is not stated:\n%s", def)
	}
	if strings.Contains(codeForm(t, "en", set), "{minutes}") {
		t.Error("the placeholder reached the page")
	}
}
