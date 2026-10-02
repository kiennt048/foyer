package portal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The colour derivation is held to the TypeScript one by shared vectors, read
// from the file the portal's own suite reads. Two implementations of arithmetic
// that must agree to the bit are only in agreement where something compares them;
// `#777777` and `#767676` are in the file because they sit either side of 4.5:1 on
// white, where a rounding difference would change the answer rather than a digit.

type themeVector struct {
	Accent string `json:"accent"`
	Light  struct {
		Accent, Hover, Contrast, Wash string
	} `json:"light"`
	Dark struct {
		Accent, Hover, Contrast, Wash string
	} `json:"dark"`
	CSS string `json:"css"`
}

func TestAccentThemeMatchesTheSharedVectors(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash("../../web/portal/src/theme.vectors.json"))
	if err != nil {
		t.Fatalf("reading the shared vectors: %v", err)
	}
	var vectors []themeVector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatalf("decoding the shared vectors: %v", err)
	}
	if len(vectors) == 0 {
		t.Fatal("no vectors — the comparison is vacuous")
	}
	for _, v := range vectors {
		if !ValidAccent(v.Accent) {
			t.Errorf("%s: a vector the contract's pattern refuses", v.Accent)
			continue
		}
		l, d := AccentTheme(v.Accent)
		want := [][2]string{
			{l.Accent, v.Light.Accent}, {l.Hover, v.Light.Hover}, {l.Contrast, v.Light.Contrast}, {l.Wash, v.Light.Wash},
			{d.Accent, v.Dark.Accent}, {d.Hover, v.Dark.Hover}, {d.Contrast, v.Dark.Contrast}, {d.Wash, v.Dark.Wash},
		}
		names := []string{"light.accent", "light.hover", "light.contrast", "light.wash",
			"dark.accent", "dark.hover", "dark.contrast", "dark.wash"}
		for i, pair := range want {
			if pair[0] != pair[1] {
				t.Errorf("%s %s: Go %s, TypeScript %s", v.Accent, names[i], pair[0], pair[1])
			}
		}
		if got := AccentCSS(v.Accent); got != v.CSS {
			t.Errorf("%s css:\n Go %s\n TS %s", v.Accent, got, v.CSS)
		}
	}
}

// The derivation's promise, checked independently of the vectors: whatever an
// owner picks, the painted accent clears 4.5:1 on both surfaces of its scheme.
func TestAccentThemeAlwaysClearsAA(t *testing.T) {
	for _, hex := range []string{"#ffffff", "#000000", "#ffff00", "#00ffff", "#777777", "#0000ff", "#c8873a"} {
		l, d := AccentTheme(hex)
		for _, bg := range []string{lightBackground, lightSurface} {
			if r := contrastRatio(parseHex(l.Accent), parseHex(bg)); r < 4.5 {
				t.Errorf("%s light accent %s on %s: %.3f", hex, l.Accent, bg, r)
			}
		}
		for _, bg := range []string{darkBackground, darkSurface} {
			if r := contrastRatio(parseHex(d.Accent), parseHex(bg)); r < 4.5 {
				t.Errorf("%s dark accent %s on %s: %.3f", hex, d.Accent, bg, r)
			}
		}
	}
}
