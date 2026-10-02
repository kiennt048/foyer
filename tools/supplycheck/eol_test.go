package supplycheck

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ── the other bases' end of support ──────────────────────────────────────────
//
// Q4f gated the alpine branch and said, in doc.go, what it did not see: the
// PostgreSQL image in deploy/images.conf (whose tag named no alpine branch), the
// ubuntu base FreeRADIUS runs on, and the debian base of the lab's FreeRADIUS. Each
// of those has its own end of support, published by its own project, and a base past
// it stops receiving fixes on a date nobody here would notice.
//
// The dates are the projects' own, read through endoflife.date's API
// (https://endoflife.date/api/{postgresql,ubuntu,debian}.json), which cites:
// https://www.postgresql.org/support/versioning/, https://ubuntu.com/about/release-cycle
// and https://wiki.debian.org/DebianReleases. As with alpineEOL, a release absent from
// a table FAILS: moving a base onto something new means adding its date here.

// postgresEOL is each PostgreSQL major's final release date — the community's own
// "five years after the initial release" rule.
var postgresEOL = map[string]string{
	"13": "2025-11-13",
	"14": "2026-11-12",
	"15": "2027-11-11",
	"16": "2028-11-09",
	"17": "2029-11-08",
	"18": "2030-11-14",
}

// ubuntuEOL is the end of STANDARD support, by codename and by version. Expanded
// Security Maintenance runs longer, but it needs an Ubuntu Pro subscription the
// official container image does not carry, so standard support is the date the
// image stops getting fixes.
var ubuntuEOL = map[string]string{
	"jammy": "2027-06-01", "22.04": "2027-06-01",
	"noble": "2029-05-31", "24.04": "2029-05-31",
	"resolute": "2031-05-29", "26.04": "2031-05-29",
}

// debianEOL is the end of Debian LTS for each stable codename: the last date the
// debian image of that release receives security fixes.
var debianEOL = map[string]string{
	"bullseye": "2026-08-31",
	"bookworm": "2028-06-30",
	"trixie":   "2030-06-30",
}

// sidMaxAge bounds a dated sid snapshot. Unstable has no security support and no end
// of it either — Debian's security team does not cover sid, and it is fixed only by
// moving forward — so the only honest gate on `debian:sid-YYYYMMDD` is how far behind
// the archive the snapshot is. Six months is two of this repository's release cycles.
// An undated `sid` or `unstable` is refused outright: it is a floating tag.
const sidMaxAge = 180 * 24 * time.Hour

func eolLeft(table map[string]string, key string, now time.Time) (time.Duration, string, error) {
	d, ok := table[key]
	if !ok {
		return 0, "", fmt.Errorf("%q is not in the table; add its end-of-support date from the project's own page", key)
	}
	eol, err := time.Parse("2006-01-02", d)
	if err != nil {
		return 0, "", fmt.Errorf("%q = %q: %v", key, d, err)
	}
	return eol.Sub(now), d, nil
}

// judge reports an end of support that has passed or is inside eolMargin.
func judge(what, key string, table map[string]string, now time.Time) error {
	left, date, err := eolLeft(table, key, now)
	switch {
	case err != nil:
		return fmt.Errorf("%s %s: %v", what, key, err)
	case left <= 0:
		return fmt.Errorf("%s %s reached end of support on %s", what, key, date)
	case left < eolMargin:
		return fmt.Errorf("%s %s reaches end of support on %s, in %d days — inside the %d-day margin",
			what, key, date, int(left.Hours()/24), int(eolMargin.Hours()/24))
	}
	return nil
}

var (
	pgMajorRE  = regexp.MustCompile(`^(\d+)(?:\.\d+)*(?:-|$)`)
	sidDatedRE = regexp.MustCompile(`^sid-(\d{8})(?:-slim)?$`)
	debStable  = regexp.MustCompile(`^([a-z]+)(?:-\d{8})?(?:-slim)?$`)
	ubuntuRE   = regexp.MustCompile(`^([a-z]+|\d{2}\.\d{2})(?:-\d{8})?$`)
)

// baseSupport judges one base against the three tables. Bases of other images, and
// alpine (TestAlpineBasesAreSupported), are not its business: nil.
func baseSupport(b baseImage, now time.Time) error {
	switch b.name {
	case "postgres":
		m := pgMajorRE.FindStringSubmatch(b.tag)
		if m == nil {
			return fmt.Errorf("postgres:%s names no major version, so it cannot be judged", b.tag)
		}
		return judge("PostgreSQL", m[1], postgresEOL, now)
	case "ubuntu":
		m := ubuntuRE.FindStringSubmatch(b.tag)
		if m == nil {
			return fmt.Errorf("ubuntu:%s names no release (use a codename or YY.MM)", b.tag)
		}
		return judge("Ubuntu", m[1], ubuntuEOL, now)
	case "debian":
		if m := sidDatedRE.FindStringSubmatch(b.tag); m != nil {
			snap, err := time.Parse("20060102", m[1])
			if err != nil {
				return fmt.Errorf("debian:%s: %v", b.tag, err)
			}
			if age := now.Sub(snap); age > sidMaxAge {
				return fmt.Errorf("debian:%s is a sid snapshot %d days old; unstable is fixed only by moving forward, and the limit is %d days",
					b.tag, int(age.Hours()/24), int(sidMaxAge.Hours()/24))
			}
			return nil
		}
		m := debStable.FindStringSubmatch(b.tag)
		if m == nil || m[1] == "sid" || m[1] == "unstable" || m[1] == "testing" || m[1] == "stable" {
			return fmt.Errorf("debian:%s is a floating or unknown release; pin a stable codename or a dated sid snapshot", b.tag)
		}
		return judge("Debian", m[1], debianEOL, now)
	}
	return nil
}

// TestBaseSupportJudgement pins the judgement to fixed dates, so the proving test
// below cannot pass because the arithmetic or the parsing is wrong.
func TestBaseSupportJudgement(t *testing.T) {
	asOf := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	img := func(name, tag string) baseImage { return baseImage{file: "x", name: name, tag: tag} }
	pass := []baseImage{
		img("postgres", "16.15-alpine3.24"), img("postgres", "16-alpine"), img("postgres", "17"),
		img("ubuntu", "noble"), img("ubuntu", "24.04"), img("ubuntu", "noble-20260910"),
		img("debian", "sid-20260824-slim"), img("debian", "bookworm-slim"), img("debian", "trixie-20260901"),
		img("golang", "1.26.8-alpine3.24"),
	}
	for _, b := range pass {
		if err := baseSupport(b, asOf); err != nil {
			t.Errorf("%s:%s on the reference date: %v; want supported", b.name, b.tag, err)
		}
	}
	fail := []baseImage{
		img("postgres", "13-alpine"),        // ended 2025-11-13
		img("postgres", "14.19-alpine3.22"), // 2026-11-12: inside the margin
		img("postgres", "9.6"),              // not in the table
		img("postgres", "alpine"),           // names no major
		img("ubuntu", "focal"),              // not in the table
		img("ubuntu", "latest"),             // not a release
		img("debian", "sid"),                // floating
		img("debian", "unstable-slim"),      // floating
		img("debian", "sid-20260301-slim"),  // 206 days old
		img("debian", "bullseye-slim"),      // 2026-08-31, passed
		img("debian", "buster"),             // not in the table
	}
	for _, b := range fail {
		if err := baseSupport(b, asOf); err == nil {
			t.Errorf("%s:%s on the reference date passed; want a refusal", b.name, b.tag)
		}
	}
}

// TestBasesAreInsideTheirSupportWindow is the proving test: every FROM line under
// deploy/ and lab/, and every image deploy/images.conf pins, judged against the wall
// clock for TestAlpineBasesAreSupported's reason — support ends on a date, and a test
// that only knew today would pass in 2029 for a base that died in 2028.
func TestBasesAreInsideTheirSupportWindow(t *testing.T) {
	now := time.Now()
	seen := map[string]bool{}
	for _, b := range allImages(t) {
		if err := baseSupport(b, now); err != nil {
			t.Errorf("%s\n  %v", b, err)
		}
		seen[b.name] = true
	}
	// Each of the three must actually have been looked at, or this passed over nothing.
	for _, name := range []string{"postgres", "ubuntu", "debian"} {
		if !seen[name] {
			t.Errorf("no %s image found under deploy/, lab/ or in deploy/images.conf; this test is looking in the wrong place", name)
		}
	}
}

// imagesConfBases are the images deploy/images.conf pins, as bases: the ones the
// deployment RUNS rather than builds from. An empty FOYER_IMAGE_* (not yet
// published) is skipped.
func imagesConfBases(t *testing.T) []baseImage {
	t.Helper()
	var out []baseImage
	for i, raw := range strings.Split(read(t, "deploy/images.conf"), "\n") {
		line := strings.TrimSpace(raw)
		if !strings.HasPrefix(line, "FOYER_IMAGE_") || !strings.Contains(line, "=") {
			continue
		}
		ref := line[strings.Index(line, "=")+1:]
		if !strings.ContainsAny(ref, ":@") {
			continue // empty (not yet published), or FOYER_IMAGE_REPO, which is not an image
		}
		bs := parseBases(t, "deploy/images.conf", "FROM "+ref)
		for j := range bs {
			bs[j].lineNo, bs[j].line = i+1, line
		}
		out = append(out, bs...)
	}
	return out
}

// allImages is every Dockerfile base plus every image images.conf pins.
func allImages(t *testing.T) []baseImage {
	return append(allBases(t), imagesConfBases(t)...)
}
