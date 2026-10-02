package supplycheck

import (
	"fmt"
	"go/version"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const repoRoot = "../.."

// ── the floors ────────────────────────────────────────────────────────────────
//
// Each of these is a FLOOR and not the pin: the pin lives in go.mod and the
// Dockerfiles, and these say how low the pin may go before a known, reachable
// vulnerability is back in the binary. They move up when `make vulncheck` goes red
// and the fix is a release; they never move down.

// patchedGoFloor is the lowest Go release with no known standard-library
// vulnerability reachable from this code, as govulncheck v1.8.0 measured it. At
// go1.26.2 it reported 15, fixed across go1.26.3 .. go1.26.6; the
// last ones (GO-2026-5026, -5972, -6088, -6089, -6090, -6091, -6218) are fixed in
// go1.26.6. https://go.dev/doc/devel/release#go1.26.minor
const patchedGoFloor = "go1.26.6"

// patchedModuleFloors are the non-standard-library findings of the same scan.
var patchedModuleFloors = map[string]string{
	// GO-2026-6354, GO-2026-6355.
	"golang.org/x/crypto": "v0.56.0",
	// GO-2026-6253, path traversal in archive extraction; reached through
	// testcontainers-go, which the integration tests drive.
	"github.com/moby/go-archive": "v0.3.0",
}

// alpineEOL is alpine's own end-of-support date for each release branch, from
// https://alpinelinux.org/releases/ (the machine-readable copy is
// https://alpinelinux.org/releases.json, field `eol_date`).
//
// A branch that is not in this table fails the test rather than passing it: a
// newer branch has to be added here with its date, from that page, by whoever
// moves a Dockerfile onto it. An unknown branch passing silently would be a check
// that looks at nothing.
var alpineEOL = map[string]string{
	"3.14": "2023-05-01",
	"3.15": "2023-11-01",
	"3.16": "2024-05-23",
	"3.17": "2024-11-22",
	"3.18": "2025-05-09",
	"3.19": "2025-11-01",
	"3.20": "2026-04-01",
	"3.21": "2026-11-01",
	"3.22": "2027-05-01",
	"3.23": "2027-11-01",
	"3.24": "2028-06-01",
}

// eolMargin is how long before alpine's own end-of-support date this test turns
// red. Red on the day support ends is red on the day the base stops receiving
// fixes, which is too late to be the first notice; ninety days is one release
// cycle of this repository's size to move the image and re-run deploy/verify.sh.
const eolMargin = 90 * 24 * time.Hour

// govulncheckModule is what `make vulncheck` must run.
const govulncheckModule = "golang.org/x/vuln/cmd/govulncheck"

// ── reading the tree ──────────────────────────────────────────────────────────

func read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(b)
}

// dockerfiles returns every Dockerfile under deploy/ and lab/, relative to the
// repository root. Globbed rather than listed, so a third Dockerfile is held to
// the same floors the day it is added rather than the day somebody remembers.
func dockerfiles(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{"deploy", "lab"} {
		m, err := filepath.Glob(filepath.Join(repoRoot, dir, "Dockerfile*"))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range m {
			rel, _ := filepath.Rel(repoRoot, p)
			out = append(out, filepath.ToSlash(rel))
		}
	}
	sort.Strings(out)
	if len(out) < 4 {
		// deploy/Dockerfile.{foyer,freeradius}, lab/Dockerfile.{backend,freeradius}.
		// Fewer means the glob is looking in the wrong place, and every assertion
		// below would pass over nothing.
		t.Fatalf("found %d Dockerfiles under deploy/ and lab/ (%v); expected at least 4", len(out), out)
	}
	return out
}

// baseImage is one FROM line.
type baseImage struct {
	file, line string
	lineNo     int
	name       string // docker.io/library/ stripped: "golang", "alpine", "node"
	tag        string // "" when absent
	digest     string // "" when absent
}

var fromRE = regexp.MustCompile(`(?i)^FROM\s+(?:--platform=\S+\s+)?(\S+)(?:\s+AS\s+(\S+))?\s*$`)

func parseBases(t *testing.T, file, src string) []baseImage {
	t.Helper()
	var out []baseImage
	stages := map[string]bool{}
	for i, raw := range strings.Split(src, "\n") {
		line := strings.TrimSpace(raw)
		m := fromRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		ref := m[1]
		if m[2] != "" {
			stages[strings.ToLower(m[2])] = true
		}
		if stages[strings.ToLower(ref)] && !strings.ContainsAny(ref, ":@/") {
			continue // FROM <earlier stage>
		}
		b := baseImage{file: file, line: line, lineNo: i + 1}
		if at := strings.Index(ref, "@"); at >= 0 {
			b.digest = ref[at+1:]
			ref = ref[:at]
		}
		// A tag is the part after the last colon that is not part of a registry
		// host:port — i.e. after the last slash.
		if c := strings.LastIndex(ref, ":"); c > strings.LastIndex(ref, "/") {
			b.tag = ref[c+1:]
			ref = ref[:c]
		}
		b.name = strings.TrimPrefix(strings.TrimPrefix(ref, "docker.io/"), "library/")
		out = append(out, b)
	}
	return out
}

func (b baseImage) String() string { return fmt.Sprintf("%s:%d  %s", b.file, b.lineNo, b.line) }

func allBases(t *testing.T) []baseImage {
	t.Helper()
	var out []baseImage
	for _, f := range dockerfiles(t) {
		out = append(out, parseBases(t, f, read(t, f))...)
	}
	return out
}

var goDirectiveRE = regexp.MustCompile(`(?m)^go\s+(\d+\.\d+(?:\.\d+)?)\s*$`)
var toolchainRE = regexp.MustCompile(`(?m)^toolchain\s+(go\S+)\s*$`)

// goModFloor is the lowest Go release that will build this module: the `go` line,
// or the `toolchain` line when that is higher. (A `toolchain` line only ever
// raises it — https://go.dev/doc/toolchain.)
func goModFloor(t *testing.T) (goLine, effective string) {
	t.Helper()
	src := read(t, "go.mod")
	m := goDirectiveRE.FindAllStringSubmatch(src, -1)
	if len(m) != 1 {
		t.Fatalf("go.mod must carry exactly one `go` line; found %d", len(m))
	}
	goLine = "go" + m[0][1]
	effective = goLine
	if tc := toolchainRE.FindStringSubmatch(src); tc != nil && version.Compare(tc[1], effective) > 0 {
		effective = tc[1]
	}
	return goLine, effective
}

// ── the Go release and the modules ───────────────────────────────────────────

// TestGoModIsAtOrAbovePatchedRelease is the proving test for the toolchain half.
// With go.mod saying `go 1.26.2`, a local go1.26.2 builds the binary with fifteen
// reachable vulnerabilities and no complaint, because 1.26.2 satisfies the line.
//
// It judges the `go` line and not only the `toolchain` line on purpose. A
// `toolchain` line is a preference that GOTOOLCHAIN=local ignores, and the golang
// images set exactly that; the `go` line is a requirement every toolchain honours
// by refusing to build.
func TestGoModIsAtOrAbovePatchedRelease(t *testing.T) {
	goLine, _ := goModFloor(t)
	if !version.IsValid(goLine) {
		t.Fatalf("go.mod `go` line %q is not a Go version", goLine)
	}
	if version.Compare(goLine, patchedGoFloor) < 0 {
		t.Errorf("go.mod says `go %s`, below %s — the lowest release with no known standard-library\n"+
			"vulnerability reachable from this code (govulncheck). A toolchain that satisfies\n"+
			"this line builds the vulnerable standard library without a word. Raise the `go` line.",
			strings.TrimPrefix(goLine, "go"), patchedGoFloor)
	}
}

// modDirective is one `require` or `replace` line of go.mod, in either the
// single-line or the block form.
type modDirective struct {
	verb      string // "require" or "replace"
	path, ver string // the left-hand side; ver is "" on a replace with no version
	// newPath and newVer are the right-hand side of a replace. newVer is "" when
	// the replacement is a directory, which has no version at all.
	newPath, newVer string
}

// parseGoMod reads the require and replace directives of go.mod. It knows which
// block a line is in, because a module path followed by a version is a
// requirement inside `require (…)`, an exclusion inside `exclude (…)` and a
// retraction inside `retract (…)` — and reading them all as requirements would let
// an `exclude` line stand in for the version the build actually uses.
func parseGoMod(src string) []modDirective {
	var out []modDirective
	block := ""
	for _, raw := range strings.Split(src, "\n") {
		line := raw
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		verb := block
		switch {
		case block != "" && f[0] == ")":
			block = ""
			continue
		case block == "" && len(f) == 2 && f[1] == "(":
			block = f[0]
			continue
		case block == "":
			verb, f = f[0], f[1:]
		}
		switch verb {
		case "require":
			if len(f) >= 2 {
				out = append(out, modDirective{verb: verb, path: f[0], ver: f[1]})
			}
		case "replace":
			arrow := -1
			for i, w := range f {
				if w == "=>" {
					arrow = i
				}
			}
			if arrow < 1 || arrow == len(f)-1 {
				continue
			}
			d := modDirective{verb: verb, path: f[0], newPath: f[arrow+1]}
			if arrow == 2 {
				d.ver = f[1]
			}
			if len(f) > arrow+2 {
				d.newVer = f[arrow+2]
			}
			out = append(out, d)
		}
	}
	return out
}

// moduleVersionProblems is the judgement of TestVulnerableModulesAreAtOrAboveTheirFix,
// separated from the file so its edge cases can be pinned below.
//
// A `replace` is where a floor on the require line stops meaning anything:
// `replace golang.org/x/crypto => golang.org/x/crypto v0.55.0` builds the vulnerable
// release while the require line still says v0.57.0, and a replacement by a
// directory has no version to judge at all.
func moduleVersionProblems(src string, floors map[string]string) []string {
	have := map[string]string{}
	var repl []modDirective
	for _, d := range parseGoMod(src) {
		switch d.verb {
		case "require":
			have[d.path] = d.ver
		case "replace":
			repl = append(repl, d)
		}
	}
	var out []string
	mods := make([]string, 0, len(floors))
	for mod := range floors {
		mods = append(mods, mod)
	}
	sort.Strings(mods)
	for _, mod := range mods {
		floor := floors[mod]
		v, ok := have[mod]
		if !ok {
			// Gone from the graph entirely is fine for the vulnerability and wrong
			// for this table, which would then be asserting about nothing.
			out = append(out, fmt.Sprintf("%s is no longer in go.mod; remove it from patchedModuleFloors rather than leave\n"+
				"a floor that checks an absent module", mod))
			continue
		}
		if semverLess(v, floor) {
			out = append(out, fmt.Sprintf("go.mod requires %s %s; the fix is %s", mod, v, floor))
		}
		for _, r := range repl {
			if r.path != mod || (r.ver != "" && r.ver != v) {
				continue // a replace of some other version does not apply to this build
			}
			switch {
			case r.newVer == "":
				out = append(out, fmt.Sprintf("go.mod replaces %s with the directory %s, which has no version to hold\n"+
					"to the fix %s", mod, r.newPath, floor))
			case semverLess(r.newVer, floor):
				out = append(out, fmt.Sprintf("go.mod replaces %s with %s %s; the fix is %s — the require line\n"+
					"is not what the build uses", mod, r.newPath, r.newVer, floor))
			}
		}
	}
	return out
}

// TestVulnerableModulesAreAtOrAboveTheirFix is the proving test for the module half.
func TestVulnerableModulesAreAtOrAboveTheirFix(t *testing.T) {
	for _, p := range moduleVersionProblems(read(t, "go.mod"), patchedModuleFloors) {
		t.Error(p)
	}
}

// TestModuleVersionJudgement pins the judgement to fixed go.mod texts, including
// the two ways a require line can look patched while the build is not.
func TestModuleVersionJudgement(t *testing.T) {
	floors := map[string]string{"golang.org/x/crypto": "v0.56.0"}
	cases := []struct {
		name, src string
		red       bool
	}{
		{"patched, block form", "module m\n\nrequire (\n\tgolang.org/x/crypto v0.57.0\n)\n", false},
		{"patched, single line", "module m\nrequire golang.org/x/crypto v0.57.0 // indirect\n", false},
		{"vulnerable require", "module m\nrequire (\n\tgolang.org/x/crypto v0.55.0\n)\n", true},
		{"absent", "module m\n", true},
		{"replaced down", "module m\nrequire golang.org/x/crypto v0.57.0\nreplace golang.org/x/crypto => golang.org/x/crypto v0.55.0\n", true},
		{"replaced down, versioned block", "module m\nrequire golang.org/x/crypto v0.57.0\nreplace (\n\tgolang.org/x/crypto v0.57.0 => example.com/fork v0.1.0\n)\n", true},
		{"replaced by a directory", "module m\nrequire golang.org/x/crypto v0.57.0\nreplace golang.org/x/crypto => ../crypto\n", true},
		{"replaced up", "module m\nrequire golang.org/x/crypto v0.57.0\nreplace golang.org/x/crypto => golang.org/x/crypto v0.58.0\n", false},
		{"replace of another version", "module m\nrequire golang.org/x/crypto v0.57.0\nreplace golang.org/x/crypto v0.50.0 => ../crypto\n", false},
		{"an exclude is not a requirement", "module m\nrequire golang.org/x/crypto v0.55.0\nexclude (\n\tgolang.org/x/crypto v0.57.0\n)\n", true},
	}
	for _, c := range cases {
		got := moduleVersionProblems(c.src, floors)
		if (len(got) > 0) != c.red {
			t.Errorf("%s: problems = %q; want red=%v", c.name, got, c.red)
		}
	}
}

// TestBuilderImagesNameAPatchedGoRelease holds every `FROM golang:` to go.mod.
//
// The tag must begin with an exact release (1.26.8, not 1.26): a digest pins the
// bytes, but a reviewer reads the tag, and a `1.26-alpine@sha256:…` could be
// go1.26.7 while saying nothing of the kind. The release must be at or above
// go.mod's effective floor, because the image sets GOTOOLCHAIN=local and a builder
// below it does not download a newer one — it refuses.
func TestBuilderImagesNameAPatchedGoRelease(t *testing.T) {
	_, floor := goModFloor(t)
	exact := regexp.MustCompile(`^(\d+\.\d+\.\d+)(?:-|$)`)
	n := 0
	for _, b := range allBases(t) {
		if b.name != "golang" {
			continue
		}
		n++
		m := exact.FindStringSubmatch(b.tag)
		if m == nil {
			t.Errorf("%s\n  the golang tag %q does not name an exact Go release; write e.g. golang:1.26.8-alpine3.24", b, b.tag)
			continue
		}
		rel := "go" + m[1]
		if version.Compare(rel, patchedGoFloor) < 0 {
			t.Errorf("%s\n  builds with %s, below the patched floor %s", b, rel, patchedGoFloor)
		}
		if version.Compare(rel, floor) < 0 {
			t.Errorf("%s\n  builds with %s, below go.mod's %s; with GOTOOLCHAIN=local this image cannot build the module", b, rel, floor)
		}
	}
	if n < 2 {
		t.Fatalf("found %d `FROM golang:` lines; deploy/Dockerfile.foyer and lab/Dockerfile.backend both build Go", n)
	}
}

// TestDeployBasesArePinnedByDigest restates docs/rules/deployment.md for the
// build stages: an image the deployment is built FROM is pinned the way an image
// it runs is. A base moved to a new branch is exactly when a digest gets dropped "for now".
func TestDeployBasesArePinnedByDigest(t *testing.T) {
	digest := regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	for _, b := range allImages(t) {
		if !strings.HasPrefix(b.file, "deploy/") {
			continue // lab/Dockerfile.backend is unpinned on purpose; its header says why.
		}
		if !digest.MatchString(b.digest) {
			t.Errorf("%s\n  a deployment base must be pinned by digest (docs/rules/deployment.md)", b)
		}
	}
}

// ── Q4f: the alpine branch ────────────────────────────────────────────────────

// alpineBranch reports whether an image is alpine-based, and which branch its tag
// names ("" when it is alpine-based and does not say).
func alpineBranch(name, tag string) (isAlpine bool, branch string) {
	if name == "alpine" {
		if m := regexp.MustCompile(`^(\d+\.\d+)(?:\.\d+)?$`).FindStringSubmatch(tag); m != nil {
			return true, m[1]
		}
		return true, "" // alpine, alpine:latest, alpine:edge
	}
	if !strings.Contains(tag, "alpine") {
		return false, ""
	}
	if m := regexp.MustCompile(`alpine(\d+\.\d+)(?:\b|$)`).FindStringSubmatch(tag); m != nil {
		return true, m[1]
	}
	return true, ""
}

func TestAlpineBranchClassifier(t *testing.T) {
	cases := []struct {
		name, tag string
		alpine    bool
		branch    string
	}{
		{"alpine", "3.20", true, "3.20"},
		{"alpine", "3.24.2", true, "3.24"},
		{"alpine", "", true, ""},
		{"alpine", "latest", true, ""},
		{"golang", "1.26-alpine", true, ""},
		{"golang", "1.26.8-alpine3.24", true, "3.24"},
		{"node", "24-alpine3.24", true, "3.24"},
		{"node", "24-alpine", true, ""},
		{"postgres", "16-alpine3.21", true, "3.21"},
		{"debian", "sid-20260824-slim", false, ""},
		{"ubuntu", "noble", false, ""},
	}
	for _, c := range cases {
		a, br := alpineBranch(c.name, c.tag)
		if a != c.alpine || br != c.branch {
			t.Errorf("alpineBranch(%q, %q) = (%v, %q); want (%v, %q)", c.name, c.tag, a, br, c.alpine, c.branch)
		}
	}
}

// supportedFor is how long a branch has left, judged against `now`.
func supportedFor(branch string, now time.Time) (time.Duration, error) {
	d, ok := alpineEOL[branch]
	if !ok {
		return 0, fmt.Errorf("alpine %s is not in alpineEOL; add its end-of-support date from https://alpinelinux.org/releases/", branch)
	}
	eol, err := time.Parse("2006-01-02", d)
	if err != nil {
		return 0, fmt.Errorf("alpineEOL[%q] = %q: %v", branch, d, err)
	}
	return eol.Sub(now), nil
}

// TestAlpineEOLJudgement pins the judgement itself to fixed dates, so the proving
// test below cannot pass because the arithmetic is wrong.
func TestAlpineEOLJudgement(t *testing.T) {
	asOf := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	if left, err := supportedFor("3.20", asOf); err != nil || left >= 0 {
		t.Errorf("alpine 3.20 on the reference date: left=%v err=%v; want already past end of support", left, err)
	}
	if left, err := supportedFor("3.21", asOf); err != nil || left <= 0 || left >= eolMargin {
		t.Errorf("alpine 3.21 on the reference date: left=%v err=%v; want supported but inside the %v margin", left, err, eolMargin)
	}
	if left, err := supportedFor("3.24", asOf); err != nil || left < eolMargin {
		t.Errorf("alpine 3.24 on the reference date: left=%v err=%v; want comfortably supported", left, err)
	}
	if _, err := supportedFor("3.99", asOf); err == nil {
		t.Error("an alpine branch absent from the table must be an error, not a pass")
	}
}

// TestAlpineBasesAreSupported fails a base like `FROM alpine:3.20` once it is past
// its end of support, and a node or golang stage that names no branch at all.
//
// It is judged against the wall clock deliberately. End of support is a date, and
// a test that only knew the date it was written would go on passing in 2028 for a branch
// that died in 2027. The consequence is stated: this test can go red on a tree
// nobody touched, and when it does, the fix is to move the base.
func TestAlpineBasesAreSupported(t *testing.T) {
	now := time.Now()
	n := 0
	// allImages, not allBases: the PostgreSQL image deploy/images.conf pins runs on
	// alpine too.
	for _, b := range allImages(t) {
		isAlpine, branch := alpineBranch(b.name, b.tag)
		if !isAlpine {
			continue
		}
		n++
		if branch == "" {
			t.Errorf("%s\n  an alpine base whose tag does not name its branch cannot be judged against end of\n"+
				"  support; name it (e.g. alpine:3.24.2, node:24-alpine3.24, golang:1.26.8-alpine3.24)", b)
			continue
		}
		left, err := supportedFor(branch, now)
		if err != nil {
			t.Errorf("%s\n  %v", b, err)
			continue
		}
		switch {
		case left <= 0:
			t.Errorf("%s\n  alpine %s reached end of support on %s (https://alpinelinux.org/releases/)", b, branch, alpineEOL[branch])
		case left < eolMargin:
			t.Errorf("%s\n  alpine %s reaches end of support on %s, in %d days — inside the %d-day margin",
				b, branch, alpineEOL[branch], int(left.Hours()/24), int(eolMargin.Hours()/24))
		}
	}
	if n < 3 {
		t.Fatalf("found %d alpine bases; deploy/Dockerfile.foyer alone has three", n)
	}
}

// ── the scan itself ───────────────────────────────────────────────────────────

// TestVulncheckRunsInCI asserts the Makefile half: a `vulncheck` target that runs
// a PINNED govulncheck over the whole module, and a `ci` that calls it before
// `ci-integration` (which is red today and would stop `make` before a later step).
//
// This is text over the Makefile, which is weaker than running it — running it
// needs the network. What it catches is the shapes that make a scan look present
// and not be: the target deleted, `@latest` (a different scanner every run), a
// package pattern narrower than ./..., or `ci` no longer calling it.
func TestVulncheckRunsInCI(t *testing.T) {
	mk := read(t, "Makefile")

	ver := regexp.MustCompile(`(?m)^GOVULNCHECK_VERSION\s*\??=\s*(\S+)\s*$`).FindStringSubmatch(mk)
	if ver == nil {
		t.Fatal("Makefile has no GOVULNCHECK_VERSION; the scanner must be pinned")
	}
	if !regexp.MustCompile(`^v\d+\.\d+\.\d+$`).MatchString(ver[1]) {
		t.Errorf("GOVULNCHECK_VERSION = %q; pin an exact release, not a moving name", ver[1])
	}

	recipe := func(target string) string {
		m := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(target) + `:[^\n]*\n((?:\t[^\n]*\n)+)`).FindStringSubmatch(mk)
		if m == nil {
			return ""
		}
		return m[1]
	}

	vc := recipe("vulncheck")
	if vc == "" {
		t.Fatal("Makefile has no `vulncheck` target")
	}
	want := "go run " + govulncheckModule + "@$(GOVULNCHECK_VERSION) ./..."
	if !strings.Contains(vc, want) {
		t.Errorf("`vulncheck` must run %q; it runs:\n%s", want, vc)
	}

	ci := recipe("ci")
	iv, ii := strings.Index(ci, "$(MAKE) vulncheck"), strings.Index(ci, "$(MAKE) ci-integration")
	switch {
	case iv < 0:
		t.Errorf("`make ci` does not run vulncheck; its recipe is:\n%s", ci)
	case ii >= 0 && iv > ii:
		t.Errorf("`make ci` runs vulncheck after ci-integration, which is red today, so it would never run")
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// semverLess compares vMAJOR.MINOR.PATCH, ignoring any pre-release or build
// suffix — enough for the floors above, which are all plain releases.
func semverLess(a, b string) bool {
	pa, pb := semverParts(a), semverParts(b)
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] < pb[i]
		}
	}
	return false
}

func semverParts(v string) [3]int {
	v = strings.TrimPrefix(v, "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		out[i], _ = strconv.Atoi(p)
	}
	return out
}

func TestSemverLess(t *testing.T) {
	for _, c := range []struct {
		a, b string
		less bool
	}{
		{"v0.55.0", "v0.56.0", true},
		{"v0.57.0", "v0.56.0", false},
		{"v0.56.0", "v0.56.0", false},
		{"v0.2.0", "v0.3.0", true},
		{"v0.3.3", "v0.3.0", false},
		{"v0.10.0", "v0.9.0", false},
		{"v1.0.0-rc1", "v0.9.9", false},
	} {
		if got := semverLess(c.a, c.b); got != c.less {
			t.Errorf("semverLess(%s, %s) = %v; want %v", c.a, c.b, got, c.less)
		}
	}
}
