package portal

import (
	"strings"
	"testing"
)

// The venue's page, rendered by the Go twin. The markup assertions are the page
// builder's (`web/portal/src/pages/blocks.tsx`), because the TypeScript renderer
// emits the same structure and a class name that differs between them is a page
// styled on one path and bare on the other.

func designSettings(d *Design) *RenderSettings {
	return &RenderSettings{
		VenueName: "Cà Phê Số 7", Methods: []string{"voucher"},
		Languages: []string{"en", "vi"}, Terms: map[string]string{}, Design: d,
	}
}

func connectedStatus() *RenderStatus {
	return &RenderStatus{State: string(StateConnected), OriginalURL: "http://example.test/news"}
}

func TestThemeIsEmittedOnlyWithAnAccentAndOnEveryPage(t *testing.T) {
	plain := RenderPage(RenderInput{Lang: "en", Path: "", Settings: designSettings(&Design{})})
	if strings.Contains(plain.Body, "<style>") {
		t.Error("a design with no accent emitted a stylesheet — Foyer's own colour needs none")
	}
	none := RenderPage(RenderInput{Lang: "en", Path: ""})
	if strings.Contains(none.Body, "<style>") {
		t.Error("no settings emitted a stylesheet")
	}

	want := `<style>` + AccentCSS("#c8873a") + `</style><div class="shell">`
	for _, path := range []string{"", "voucher/", "account/", "connected/", "problem/"} {
		p := RenderPage(RenderInput{Lang: "en", Path: path, Settings: designSettings(&Design{Accent: "#c8873a"})})
		if !strings.HasPrefix(p.Body, want) {
			t.Errorf("/%s: the theme is not the first thing before the shell:\n%.200s", path, p.Body)
		}
	}
	// Connected, signed in — a different branch from the not-connected shape above.
	p := RenderPage(RenderInput{Lang: "en", Path: "connected/", Status: connectedStatus(),
		Settings: designSettings(&Design{Accent: "#c8873a"})})
	if !strings.HasPrefix(p.Body, want) {
		t.Error("the connected page carries no theme")
	}

	// A value that is not the contract's pattern never reaches a stylesheet, even
	// though the request validator already refused it: this is where it becomes CSS.
	evil := RenderPage(RenderInput{Lang: "en", Path: "", Settings: designSettings(&Design{Accent: "red}body{display:none"})})
	if strings.Contains(evil.Body, "<style>") {
		t.Error("an accent outside #rrggbb was written into a stylesheet")
	}
}

func TestHeadlineFallsBackAcrossLanguagesAndSaysSo(t *testing.T) {
	d := &Design{Headline: map[string]string{"vi": "Chào mừng đến Cà phê Số 7", "en": "  "}}
	p := RenderPage(RenderInput{Lang: "en", Path: "", Settings: designSettings(d)})
	if !strings.Contains(p.Body, `<h1 lang="vi">Chào mừng đến Cà phê Số 7</h1>`) {
		t.Errorf("a blank English headline did not fall back to the Vietnamese one, marked:\n%s", p.Body)
	}
	if !strings.HasPrefix(p.Title, "Get online") {
		t.Errorf("the document title changed with the headline: %q", p.Title)
	}

	own := RenderPage(RenderInput{Lang: "vi", Path: "", Settings: designSettings(d)})
	if !strings.Contains(own.Body, `<h1>Chào mừng đến Cà phê Số 7</h1>`) {
		t.Error("the headline in its own language carries a lang attribute or is missing")
	}

	empty := RenderPage(RenderInput{Lang: "en", Path: "", Settings: designSettings(&Design{})})
	if !strings.Contains(empty.Body, `<h1>Get online</h1>`) {
		t.Error("no headline did not fall back to the catalogue's")
	}
}

func TestNoticeSitsAboveTheWaysInAndEverythingElseBelow(t *testing.T) {
	d := &Design{Blocks: []Block{
		{ID: "t", Kind: "text", Placement: "welcome", Content: map[string]BlockText{"en": {Title: "Our story", Body: "Since 1999"}}},
		{ID: "n", Kind: "notice", Placement: "welcome", Content: map[string]BlockText{"en": {Title: "Closed Monday"}}},
		{ID: "o", Kind: "offer", Placement: "welcome", Code: "CAFE7", Content: map[string]BlockText{"en": {Title: "10% off"}}},
	}}
	body := RenderPage(RenderInput{Lang: "en", Path: "", Settings: designSettings(d)}).Body

	notice := strings.Index(body, `<div class="blocks blocks-notice"><section class="block block-notice"><p class="block-title">Closed Monday</p></section></div>`)
	label := strings.Index(body, `<p class="section-label">`)
	methods := strings.Index(body, `<div class="methods">`)
	rest := strings.Index(body, `<div class="blocks"><section class="block block-text"><h2 class="block-title">Our story</h2><p class="block-body">Since 1999</p></section>`+
		`<section class="block block-offer"><h2 class="block-title">10% off</h2><p class="block-code"><span class="block-code-label">Code</span><span class="block-code-value">CAFE7</span></p></section></div>`)
	terms := strings.Index(body, `<section class="terms">`)
	if notice < 0 || rest < 0 {
		t.Fatalf("containers missing or not the spec's markup:\n%s", body)
	}
	if !(notice < label && label < methods && methods < rest && rest < terms) {
		t.Errorf("order is notice=%d label=%d methods=%d rest=%d terms=%d; want notice, label, methods, rest, terms",
			notice, label, methods, rest, terms)
	}

	// A container with nothing in it is not rendered at all.
	bare := RenderPage(RenderInput{Lang: "en", Path: "", Settings: designSettings(&Design{})}).Body
	if strings.Contains(bare, `class="blocks`) {
		t.Error("an empty design rendered an empty container")
	}
}

func TestLinkBlocksRenderOnlyOnTheConnectedPage(t *testing.T) {
	d := &Design{Blocks: []Block{
		{ID: "b", Kind: "button", Placement: "connected", URL: "https://facebook.com/cafe7",
			Content: map[string]BlockText{"en": {Label: "Follow us"}}},
		{ID: "s", Kind: "social", Placement: "connected", Links: []SocialLink{
			{Network: "google-review", URL: "https://g.page/r/cafe7"},
			{Network: "phone", URL: "tel:+84 28 1234 5678"},
			{Network: "facebook", URL: "javascript:alert(1)"},
		}},
		{ID: "l", Kind: "list", Placement: "connected", Content: map[string]BlockText{"vi": {
			Title: "Giờ mở cửa", Items: []BlockItem{{Label: "T2–T6", Value: "7:00–22:00"}, {Label: " "}, {Label: "CN"}},
		}}},
	}}
	welcome := RenderPage(RenderInput{Lang: "en", Path: "", Settings: designSettings(d)}).Body
	if strings.Contains(welcome, "facebook.com") || strings.Contains(welcome, `class="blocks"`) {
		t.Error("connected blocks rendered on the welcome page")
	}
	notConnected := RenderPage(RenderInput{Lang: "en", Path: "connected/", Settings: designSettings(d)}).Body
	if strings.Contains(notConnected, "facebook.com") {
		t.Error("connected blocks rendered for a browser that is not connected")
	}

	body := RenderPage(RenderInput{Lang: "en", Path: "connected/", Status: connectedStatus(), Settings: designSettings(d)}).Body
	for _, want := range []string{
		`<a class="block block-button" href="https://facebook.com/cafe7" rel="noopener noreferrer">Follow us</a>`,
		`<section class="block block-social"><ul class="block-links">` +
			`<li><a class="block-link" href="https://g.page/r/cafe7" rel="noopener noreferrer">Review us on Google</a></li>` +
			`<li><a class="block-link" href="tel:+84 28 1234 5678" rel="noopener noreferrer">Call us</a></li></ul></section>`,
		`<section class="block block-list" lang="vi"><h2 class="block-title">Giờ mở cửa</h2><ul class="block-items">` +
			`<li><span class="block-item-label">T2–T6</span><span class="block-item-value">7:00–22:00</span></li>` +
			`<li><span class="block-item-label">CN</span></li></ul></section>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("connected page lacks\n%s\nin\n%s", want, body)
		}
	}
	if strings.Contains(body, "javascript:") {
		t.Error("a link outside the allowlist reached an href")
	}
	blocks := strings.Index(body, `<div class="blocks">`)
	cont := strings.Index(body, `<a class="continue"`)
	if blocks < 0 || cont < 0 || blocks > cont {
		t.Errorf("connected blocks must come before the continue link: blocks=%d continue=%d", blocks, cont)
	}

	// The labels are the catalogue's, in the language being read.
	vi := RenderPage(RenderInput{Lang: "vi", Path: "connected/", Status: connectedStatus(), Settings: designSettings(d)}).Body
	if !strings.Contains(vi, ">Đánh giá trên Google</a>") || !strings.Contains(vi, ">Gọi cho chúng tôi</a>") {
		t.Error("social labels are not the Vietnamese catalogue's")
	}
}

func TestVenueTextIsEscapedEverywhere(t *testing.T) {
	x := `<script>alert(1)</script>`
	d := &Design{
		Headline: map[string]string{"en": x},
		Blocks: []Block{
			{ID: "n", Kind: "notice", Placement: "welcome", Content: map[string]BlockText{"en": {Title: x, Body: x}}},
			{ID: "o", Kind: "offer", Placement: "welcome", Code: x, Content: map[string]BlockText{"en": {Title: x}}},
			{ID: "l", Kind: "list", Placement: "connected", Content: map[string]BlockText{"en": {Title: x, Items: []BlockItem{{Label: x, Value: x}}}}},
			{ID: "b", Kind: "button", Placement: "connected", URL: `https://a.example/"><script>`, Content: map[string]BlockText{"en": {Label: x}}},
		},
	}
	for _, in := range []RenderInput{
		{Lang: "en", Path: "", Settings: designSettings(d)},
		{Lang: "en", Path: "connected/", Status: connectedStatus(), Settings: designSettings(d)},
	} {
		body := RenderPage(in).Body
		if strings.Contains(body, "<script>") {
			t.Errorf("/%s: venue text reached the page as markup:\n%s", in.Path, body)
		}
		if !strings.Contains(body, "&lt;script&gt;") {
			t.Errorf("/%s: the escaped text is missing — the assertion above proves nothing", in.Path)
		}
	}
}

func TestABlockWithNoWordsIsNotRendered(t *testing.T) {
	d := &Design{Blocks: []Block{
		{ID: "t", Kind: "text", Placement: "welcome", Content: map[string]BlockText{"en": {Title: "  "}, "vi": {}}},
		{ID: "b", Kind: "button", Placement: "connected", URL: "https://a.example"},
		{ID: "s", Kind: "social", Placement: "connected"},
	}}
	welcome := RenderPage(RenderInput{Lang: "en", Path: "", Settings: designSettings(d)}).Body
	connected := RenderPage(RenderInput{Lang: "en", Path: "connected/", Status: connectedStatus(), Settings: designSettings(d)}).Body
	if strings.Contains(welcome, "block-") || strings.Contains(connected, "block-") {
		t.Error("a block with no text, a button with no label, or a social block with no links rendered")
	}
}

func TestIsLinkIsTheSpecsAllowlist(t *testing.T) {
	for _, ok := range []string{"https://facebook.com/cafe7", "http://a.example", "https://a.example/x?y#z", "tel:+84 28 1234 5678", "tel:0901234567"} {
		if !IsLink(ok) {
			t.Errorf("IsLink(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "javascript:alert(1)", "mailto:hi@cafe.example", "https://user@a.example",
		"//a.example", "https://", "https://a.example/ x", "https://a\u00a0b.example", "tel:12", "ftp://a.example", "HTTPS://A.EXAMPLE"} {
		if IsLink(bad) {
			t.Errorf("IsLink(%q) = true", bad)
		}
	}
}

func TestTrimTextIsJavaScriptsTrim(t *testing.T) {
	if TrimText("\uFEFF") != "" {
		t.Error("a BOM is blank to JavaScript and must be blank here")
	}
	if TrimText("\u0085") == "" {
		t.Error("NEL is text to JavaScript and must be text here")
	}
	if TrimText(" \u00a0x\u3000") != "x" {
		t.Error("Unicode spaces are not trimmed")
	}
}

// The connected page's order with a real plan on it: facts, then the qualifier,
// then the venue's blocks, then the way back. A plan is on it because the blocks
// sit between the plan's markup and the continue link, and a test with no plan
// cannot tell "after the qualifier" from "anywhere before the link" — and because
// rendering a plan's figures exercises `itoa`, which, if it never divided, would
// loop forever on any duration or allowance, on the one page a guest sees once
// online.
func TestConnectedBlocksSitAfterThePlanAndBeforeTheWayBack(t *testing.T) {
	thirty, allowance := 30, 1500
	st := connectedStatus()
	st.Plan = &RenderPlan{Name: "Café", DurationMinutes: &thirty, DataAllowanceMB: &allowance, EnforcedBy: "unenforced"}
	d := &Design{Blocks: []Block{{ID: "t", Kind: "text", Placement: "connected",
		Content: map[string]BlockText{"en": {Title: "Thanks for visiting"}}}}}
	body := RenderPage(RenderInput{Lang: "en", Path: "connected/", Status: st, Settings: designSettings(d)}).Body

	facts := strings.Index(body, `<div class="facts">`)
	qualifier := strings.Index(body, `<p class="qualifier">`)
	blocks := strings.Index(body, `<div class="blocks"><section class="block block-text"><h2 class="block-title">Thanks for visiting</h2></section></div>`)
	cont := strings.Index(body, `<a class="continue"`)
	if facts < 0 || qualifier < 0 || blocks < 0 || cont < 0 || !(facts < qualifier && qualifier < blocks && blocks < cont) {
		t.Errorf("order facts=%d qualifier=%d blocks=%d continue=%d\n%s", facts, qualifier, blocks, cont, body)
	}
	if !strings.Contains(body, ">30 min<") || !strings.Contains(body, ">1.5 GB<") {
		t.Errorf("the plan's figures are wrong:\n%s", body)
	}
}
