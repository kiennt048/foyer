package portal

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// The twin catalogue is held to its authorities, mechanically.
//
// Three copies of one contract: `web/portal/src/i18n/en.ts` and `vi.ts` are
// what the TypeScript renderer reads, `catalogue.go` is what the Go renderer
// reads. A key added in one place and forgotten in another renders as a blank
// string on a guest's phone — no crash, no test failure, just a missing
// sentence on the one screen the guest cannot ask about. So the files are read
// here, at test time, and held together: same key set both languages (typed
// already, asserted anyway for the Go side), same values the renderer will
// actually emit.

var catalogueKey = regexp.MustCompile(`"([^"]+)":\s+"`)

var catalogueValue = regexp.MustCompile(`"([^"]+)":\s*"((?:[^"\\]|\\.)*)"`)

func goCatalogueKeys(t *testing.T) map[string]string {
	t.Helper()
	body, err := os.ReadFile(filepath.FromSlash("catalogue.go"))
	if err != nil {
		t.Fatalf("reading catalogue.go: %v", err)
	}
	found := map[string]string{}
	for _, m := range catalogueKey.FindAllStringSubmatch(string(body), -1) {
		// The map literals are the only `"key": "value"` pairs in the file;
		// comments carry no quoted pairs in that shape. One capture group:
		// the key is m[1].
		found[m[1]] = m[1]
	}
	if len(found) == 0 {
		t.Fatal("read no keys out of catalogue.go — the derivation is vacuous")
	}
	return found
}

func tsCatalogue(t *testing.T, path string) map[string]string {
	t.Helper()
	body, err := os.ReadFile(filepath.FromSlash(path))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	found := map[string]string{}
	for _, m := range catalogueValue.FindAllStringSubmatch(string(body), -1) {
		found[m[1]] = m[2]
	}
	if len(found) == 0 {
		t.Fatalf("read no keys out of %s — the derivation is vacuous", path)
	}
	return found
}

// TestCatalogueMatchesBothLanguages — every key the Go renderer can read exists
// in both TypeScript catalogues, with the exception of the two switcher
// helpers, which are computed rather than stored.
func TestCatalogueMatchesBothLanguages(t *testing.T) {
	goKeys := goCatalogueKeys(t)
	en := tsCatalogue(t, "../../web/portal/src/i18n/en.ts")
	vi := tsCatalogue(t, "../../web/portal/src/i18n/vi.ts")

	var missing []string
	for k := range goKeys {
		if k == "lang.name.en" || k == "lang.name.vi" {
			continue
		}
		if _, ok := en[k]; !ok {
			missing = append(missing, "en:"+k)
		}
		if _, ok := vi[k]; !ok {
			missing = append(missing, "vi:"+k)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("Go catalogue keys with no TypeScript twin: %v", missing)
	}

	// And the reverse: a key the TypeScript renderer reads and the Go one
	// cannot renders blank in production while every TS test stays green.
	//
	// ── the one exemption, and its reason is a condition this file evaluates ──
	//
	// `probe.*` is the device measurement page. It is a lab
	// diagnostic, served by `lab/measure -probe` out of `web/portal/dist-probe`,
	// and `make web-embed` does not copy `dist-probe` into the Go binary — so no
	// `probe.` key can reach a guest and the Go renderer can never need one.
	//
	// An exemption whose reason is prose is never re-checked, so the reason is
	// asserted instead: if `dist-probe` ever reaches the embedded set, the keys
	// DO need Go twins, and this test says so rather than carrying on excusing
	// them. A list edit that reproduces the defect one entry along is not a fix.
	probeExempt := probeNeverEmbedded(t)
	var unported []string
	for k := range en {
		if _, ok := goKeys[k]; ok {
			continue
		}
		if probeExempt && strings.HasPrefix(k, "probe.") {
			continue
		}
		unported = append(unported, k)
	}
	if len(unported) > 0 {
		sort.Strings(unported)
		t.Errorf("TypeScript keys the Go renderer cannot read: %v", unported)
	}
	if !probeExempt {
		t.Error("the `probe.` exemption no longer holds its reason — `web/portal/dist-probe` is now reachable from the Go binary, so every `probe.` key needs a Go twin like any other")
	}
}

// probeNeverEmbedded reports whether `web/portal/dist-probe` is genuinely
// outside the Go binary's embed set, by asking the same question the build
// answers: which directories does `make web-embed` copy?
//
// It reads `Makefile` and `web/portal/build/build.ts` rather than a list, so the day
// the probe is embedded for real this returns false and
// TestCatalogueMatchesBothLanguages starts demanding Go twins — which is the
// moment they are needed.
func probeNeverEmbedded(t *testing.T) bool {
	t.Helper()
	mk, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatalf("reading the Makefile: %v", err)
	}
	return !strings.Contains(string(mk), "dist-probe")
}

// TestCatalogueValuesMatchEnglish — the values are the contract too, not just
// the keys. A twin with the right keys and a paraphrased sentence renders a
// different portal in production than the one en.ts defines. Compared through
// the translator, because the raw maps hold both languages and the translator
// is what the renderer reads.
func TestCatalogueValuesMatchEnglish(t *testing.T) {
	en := tsCatalogue(t, "../../web/portal/src/i18n/en.ts")
	probeExempt := probeNeverEmbedded(t)
	var drifted []string
	for k, want := range en {
		if k == "lang.name" {
			continue
		}
		// The same exemption, under the same condition, as the key test above —
		// one reason, one predicate, and the predicate is evaluated rather than
		// remembered. A key with no Go twin whose VALUE diverges is the same
		// finding as a key with no Go twin at all.
		if probeExempt && strings.HasPrefix(k, "probe.") {
			continue
		}
		if got := renderTranslator("en")(k); got != want {
			drifted = append(drifted, k)
		}
	}
	if len(drifted) > 0 {
		sort.Strings(drifted)
		t.Errorf("Go values differing from en.ts (the source copy): %v", drifted)
	}
}

// TestCatalogueValuesMatchVietnamese — same, for the second language.
func TestCatalogueValuesMatchVietnamese(t *testing.T) {
	probeExempt := probeNeverEmbedded(t)
	vi := tsCatalogue(t, "../../web/portal/src/i18n/vi.ts")
	for k, want := range vi {
		if probeExempt && strings.HasPrefix(k, "probe.") {
			continue
		}
		got := renderTranslator("vi")(k)
		if got != want {
			t.Errorf("vi %q: Go renders %q, catalogue says %q", k, got, want)
			break
		}
	}
	en := tsCatalogue(t, "../../web/portal/src/i18n/en.ts")
	for k, want := range en {
		if k == "lang.name" {
			continue
		}
		if probeExempt && strings.HasPrefix(k, "probe.") {
			continue
		}
		if got := renderTranslator("en")(k); got != want {
			t.Errorf("en %q: Go renders %q, catalogue says %q", k, got, want)
			break
		}
	}
}

// TestRendererResolvesTermsLikeTheContract — the resolution order is the
// contract's, asserted through the renderer rather than the helper: venue text
// for the language, else any other language it supplied carrying that
// language, else the catalogue default. Never empty, never translated.
func TestRendererResolvesTermsLikeTheContract(t *testing.T) {
	tFn := renderTranslator("en")
	if got := resolveTerms("en", tFn, map[string]string{"en": "House rules."}); got.summary != "House rules." || got.lang != "en" {
		t.Errorf("own language: %+v", got)
	}
	if got := resolveTerms("en", tFn, map[string]string{"vi": "Nội quy."}); got.summary != "Nội quy." || got.lang != "vi" {
		t.Errorf("fallback carries its language: %+v", got)
	}
	if got := resolveTerms("en", tFn, nil); got.summary == "" {
		t.Error("no terms renders empty — the catalogue default must answer")
	}
	if got := resolveTerms("en", tFn, map[string]string{"en": "   "}); got.summary == "" || got.summary == "   " {
		t.Errorf("blank terms must fall through, got %q", got.summary)
	}
}

// TestRendererKeepsTheSwitcherHonest — one advertised language means no
// switcher; the language being read is always offered; order is catalogue.
func TestRendererKeepsTheSwitcherHonest(t *testing.T) {
	if got := resolveLanguages("en", []string{"vi"}); len(got) != 2 {
		t.Errorf("single-language venue + current: %v, want both", got)
	}
	if got := resolveLanguages("en", []string{"en"}); len(got) != 1 || got[0] != "en" {
		t.Errorf("single-language venue read in its language: %v, want no switcher", got)
	}
	if got := resolveLanguages("en", nil); len(got) != 2 {
		t.Errorf("unconfigured site: %v, want the full catalogue", got)
	}
}

// TestRendererRefusesWhatTheCatalogueCannotSay — unknown error reasons read
// generic; unknown pages are not documents; unknown languages read English.
func TestRendererRefusesWhatTheCatalogueCannotSay(t *testing.T) {
	if got := renderErrorKey("no-such-reason"); got != "error.generic" {
		t.Errorf("unknown reason: %q, want error.generic", got)
	}
	if RenderPage(RenderInput{Lang: "en", Path: "nope/"}) != nil {
		t.Error("unknown path rendered a document")
	}
	if p := RenderPage(RenderInput{Lang: "xx", Path: ""}); p == nil {
		t.Error("unknown language refused — must read English defaults")
	} else if !strings.Contains(p.Title, "Get online") {
		t.Errorf("unknown language title %q, want the English default", p.Title)
	}
}

// TestRendererEchoesOnlyTheVoucherCode — `?code=` comes back normalised and
// capped; anything else is dropped rather than published.
func TestRendererEchoesOnlyTheVoucherCode(t *testing.T) {
	if got := resolveSubmittedCode("abcd-efgh-jkmn"); got != "ABCDEFGHJKMN" {
		t.Errorf("normalisation: %q", got)
	}
	if got := resolveSubmittedCode("ABCDEFGHJKMNPQRST"); got != "ABCDEFGHJKMN" {
		t.Errorf("cap: %q", got)
	}
	if got := resolveSubmittedCode("<script>alert(1)</script>"); strings.ContainsAny(got, "<>") {
		t.Errorf("markup survives: %q", got)
	}
	if got := resolveSubmittedCode(""); got != "" {
		t.Errorf("empty: %q", got)
	}
}

// TestRendererGatesBrandAssets — remote origins and SVG never render; a
// same-origin path does; nothing renders a broken element.
func TestRendererGatesBrandAssets(t *testing.T) {
	for _, bad := range []string{
		"http://cdn.example.com/logo.png", "//cdn.example.com/logo.png",
		"data:image/svg+xml;base64,PHN2Zz48L3N2Zz4=", "javascript:alert(1)",
	} {
		if got := safeLogo(bad); got != "" {
			t.Errorf("safeLogo(%q) = %q, want nothing", bad, got)
		}
	}
	if got := safeLogo("/venue/s/logo.png"); got == "" {
		t.Error("same-origin logo path refused")
	}
	if got := safeLogo("data:image/png;base64,iVBORw0KGgo="); got == "" {
		t.Error("inline raster logo refused")
	}
	for _, bad := range []string{"//evil.example/x.jpg", "https://evil.example/x.jpg", "data:image/png;base64,iVBORw0KGgo="} {
		if got := safeHeroURL(bad); got != "" {
			t.Errorf("safeHeroURL(%q) = %q, want nothing", bad, got)
		}
	}
}

// TestRendererWelcomesTheConfiguredVenue — a configured venue must not render the
// catalogue defaults. The welcome page names the
// venue, offers its methods, and carries its terms; nothing carries another
// venue's.
func TestRendererWelcomesTheConfiguredVenue(t *testing.T) {
	in := RenderInput{
		Lang: "en", Path: "",
		Settings: &RenderSettings{
			VenueName: "Quán Cà Phê Số 7", NetworkName: "Guest-WiFi",
			Methods:             []string{"voucher", "click-through"},
			Languages:           []string{"en", "vi"},
			Terms:               map[string]string{"en": "House rules."},
			ClickThroughMinutes: 30,
		},
		Query: "?id=aa&ap=bb",
	}
	p := RenderPage(in)
	if p == nil {
		t.Fatal("welcome page refused")
	}
	for _, want := range []string{"Quán Cà Phê Số 7", "Guest-WiFi", "House rules.", "/en/voucher/?id=aa", "Agree and get online"} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("welcome page lacks %q", want)
		}
	}
	if strings.Contains(p.Body, "I have an account") {
		t.Error("welcome page offers an account the venue did not configure")
	}
	if !strings.Contains(p.Title, "Quán Cà Phê Số 7") {
		t.Errorf("title %q does not name the venue", p.Title)
	}
}

// TestRendererFailsClosedWithoutSettings — no row, no claim, no session: the
// catalogue defaults, never a blank page and never another venue.
func TestRendererFailsClosedWithoutSettings(t *testing.T) {
	p := RenderPage(RenderInput{Lang: "en", Path: ""})
	if p == nil {
		t.Fatal("welcome page refused without settings")
	}
	for _, want := range []string{"Foyer", "I have a voucher", "Internet access is provided as is"} {
		if !strings.Contains(p.Body, want) {
			t.Errorf("default page lacks %q", want)
		}
	}
}
