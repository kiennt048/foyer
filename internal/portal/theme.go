package portal

import (
	"math"
	"regexp"
	"strconv"
)

// The venue's brand colour, as the shades actually painted.
//
// The twin of `accentTheme` / `accentCss` in `web/contract/src/design.ts`, and the
// two are held together by `web/portal/src/theme.vectors.json`, which both test
// suites read. An owner picks a colour, not a contrast ratio: a pale brand is
// darkened on the light page and a deep one lightened on the dark page until text
// in it and on it clears WCAG AA (4.5:1) against both backgrounds.
//
// ── why the arithmetic is spelled so carefully ───────────────────────────────
//
// The output is compared byte for byte with what JavaScript computes, so every
// step has to round the way `Math.round` does on the same IEEE doubles. Two traps:
//
//   - `Math.round` is half-up; Go's `math.Round` is half-away-from-zero. They agree
//     here only because every value is non-negative, which mix guarantees.
//   - Go may fuse `a*b + c` into one FMA instruction on arm64, which rounds once
//     where JavaScript rounds twice, and moves a channel exactly on a .5 boundary.
//     The explicit `float64(...)` conversions forbid the fusion: the Go
//     specification says an explicit conversion rounds, and names it as the way to
//     prevent exactly this.

var accentPattern = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// ValidAccent is the contract's pattern. The renderer re-checks it where the value
// becomes a stylesheet, as it re-checks a link where it becomes an `href`: the
// request validator refused anything else, and this is the last gate before bytes.
func ValidAccent(hex string) bool { return accentPattern.MatchString(hex) }

type rgb [3]float64

var (
	black = rgb{0, 0, 0}
	white = rgb{255, 255, 255}
)

const (
	lightBackground = "#fbf9f7"
	lightSurface    = "#ffffff"
	darkBackground  = "#16130f"
	darkSurface     = "#201c18"
)

func parseHex(hex string) rgb {
	var out rgb
	for i := 0; i < 3; i++ {
		n, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		out[i] = float64(n)
	}
	return out
}

func formatHex(c rgb) string {
	const digits = "0123456789abcdef"
	b := []byte{'#', 0, 0, 0, 0, 0, 0}
	for i, v := range c {
		n := int(v)
		b[1+2*i] = digits[n>>4]
		b[2+2*i] = digits[n&0xf]
	}
	return string(b)
}

func mix(a, b rgb, t float64) rgb {
	var out rgb
	for i := range a {
		out[i] = math.Round(float64(a[i]*(1-t)) + float64(b[i]*t))
	}
	return out
}

func luminance(c rgb) float64 {
	lin := func(v float64) float64 {
		s := v / 255
		if s <= 0.04045 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return float64(float64(0.2126*lin(c[0]))+float64(0.7152*lin(c[1]))) + float64(0.0722*lin(c[2]))
}

// contrastRatio is WCAG 2.x.
func contrastRatio(a, b rgb) float64 {
	la, lb := luminance(a), luminance(b)
	return (math.Max(la, lb) + 0.05) / (math.Min(la, lb) + 0.05)
}

// towards moves a colour one twentieth at a time towards black or white until it
// clears 4.5:1 against every background given. Step 20 is the target itself, which
// always passes against these backgrounds, so the loop cannot fall through in
// practice; the final return is the same answer the TypeScript gives if it did.
// towards holds the candidate against the page, the card and its own wash — the
// "Connected" badge is accent text on accent wash, and a café's amber that passed
// against the first two failed axe on the third. The wash is derived from the
// candidate, so it is recomputed at every step, exactly as the TypeScript does.
func towards(hex string, target rgb, wash func(rgb) rgb, against ...string) rgb {
	base := parseHex(hex)
	for step := 0; step <= 20; step++ {
		candidate := mix(base, target, float64(step)/20)
		ok := contrastRatio(candidate, wash(candidate)) >= 4.5
		for _, bg := range against {
			if !ok {
				break
			}
			if contrastRatio(candidate, parseHex(bg)) < 4.5 {
				ok = false
			}
		}
		if ok {
			return candidate
		}
	}
	return target
}

func lightWash(c rgb) rgb { return mix(c, parseHex(lightBackground), 0.9) }
func darkWash(c rgb) rgb  { return mix(c, parseHex(darkBackground), 0.8) }

// ThemeShades is one colour scheme's four custom properties.
type ThemeShades struct {
	Accent, Hover, Contrast, Wash string
}

// AccentTheme is `accentTheme`: the eight colours painted for one brand colour.
// The caller has already checked `ValidAccent`.
func AccentTheme(hex string) (light, dark ThemeShades) {
	l := towards(hex, black, lightWash, lightBackground, lightSurface)
	d := towards(hex, white, darkWash, darkBackground, darkSurface)
	light = ThemeShades{
		Accent:   formatHex(l),
		Hover:    formatHex(mix(l, black, 0.15)),
		Contrast: "#ffffff",
		Wash:     formatHex(lightWash(l)),
	}
	dark = ThemeShades{
		Accent:   formatHex(d),
		Hover:    formatHex(mix(d, white, 0.15)),
		Contrast: darkBackground,
		Wash:     formatHex(darkWash(d)),
	}
	return light, dark
}

// AccentCSS is `accentCss`: the text of the `<style>` both renderers emit. Every
// value is a formatted hex colour, so there is nothing in it to escape.
func AccentCSS(hex string) string {
	l, d := AccentTheme(hex)
	return ":root{--c-accent:" + l.Accent + ";--c-accent-hover:" + l.Hover +
		";--c-accent-contrast:" + l.Contrast + ";--c-accent-wash:" + l.Wash + "}" +
		"@media (prefers-color-scheme: dark){:root{--c-accent:" + d.Accent + ";--c-accent-hover:" + d.Hover +
		";--c-accent-contrast:" + d.Contrast + ";--c-accent-wash:" + d.Wash + "}}"
}
