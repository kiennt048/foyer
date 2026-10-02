package deploycheck

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The rule: an operator changes every setting in the admin console, and the FOYER_*
// variables that remain are install-time only. docs/operator/configuration.md is
// where that is written down for each variable. Two things keep it true, and both
// are asserted here:
//
//   - every FOYER_* name the binaries read is classified on that page, so a new
//     variable cannot arrive without somebody saying whether an operator is meant
//     to touch it;
//   - no living operator document tells an operator to SET a variable whose value
//     is the console's.

var envName = regexp.MustCompile(`FOYER_[A-Z0-9_]*[A-Z0-9]`)

// readByBinaries is every FOYER_* string literal in non-test Go under cmd/ and internal/.
func readByBinaries(t *testing.T) map[string]string {
	t.Helper()
	lit := regexp.MustCompile(`"(FOYER_[A-Z0-9_]*[A-Z0-9])"`)
	out := map[string]string{}
	for _, root := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, root), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for _, m := range lit.FindAllStringSubmatch(string(b), -1) {
				rel, _ := filepath.Rel(repoRoot, p)
				out[m[1]] = rel
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(out) < 20 {
		t.Fatalf("found %d FOYER_* names in cmd/ and internal/; there are more than twenty, so this is looking in the wrong place", len(out))
	}
	return out
}

// classified is every name the page mentions, with its two shorthands expanded:
// "`FOYER_X_CERT`/`_KEY`" names FOYER_X_KEY too, and "`FOYER_X_*`" names every
// FOYER_X_… .
func classified(t *testing.T) (names map[string]bool, prefixes []string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot, "docs", "operator", "configuration.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	names = map[string]bool{}
	for _, n := range envName.FindAllString(doc, -1) {
		names[n] = true
	}
	for _, m := range regexp.MustCompile("`(FOYER_[A-Z0-9_]+)_[A-Z0-9]+`/`(_[A-Z0-9]+)`").FindAllStringSubmatch(doc, -1) {
		names[m[1]+m[2]] = true
	}
	for _, m := range regexp.MustCompile("`(FOYER_[A-Z0-9_]+_)\\*`").FindAllStringSubmatch(doc, -1) {
		prefixes = append(prefixes, m[1])
	}
	return names, prefixes
}

func TestEveryVariableTheBinariesReadIsClassified(t *testing.T) {
	names, prefixes := classified(t)
	for name, where := range readByBinaries(t) {
		if names[name] {
			continue
		}
		covered := false
		for _, p := range prefixes {
			covered = covered || strings.HasPrefix(name, p)
		}
		if !covered {
			t.Errorf("%s is read by %s and docs/operator/configuration.md does not say what it is: an install-time value an operator never sets, or a console setting it should not be", name, where)
		}
	}
}

// The variables whose value is the console's now. Setting one is still possible —
// they are the installation's bootstrap values — but telling an operator to is the
// defect.
var consoleOwned = regexp.MustCompile(`(?m)(?:^|sudo\s+|export\s+|\s)FOYER_(PORTAL_URL|VENUE_URL|PORTAL_TLS_CERT|PORTAL_TLS_KEY|MAINTENANCE_INTERVAL)=`)

func TestNoOperatorDocumentSetsAConsoleSetting(t *testing.T) {
	docs := []string{"README.md", "CONTRIBUTING.md", "deploy/README.md", "lab/README.md", "docs/portal-https.md"}
	m, _ := filepath.Glob(filepath.Join(repoRoot, "docs", "operator", "*.md"))
	for _, p := range m {
		rel, _ := filepath.Rel(repoRoot, p)
		docs = append(docs, rel)
	}
	for _, d := range docs {
		b, err := os.ReadFile(filepath.Join(repoRoot, d))
		if err != nil {
			t.Fatal(err)
		}
		for _, hit := range consoleOwned.FindAllString(string(b), -1) {
			t.Errorf("%s tells an operator to set %s — that value is set in the admin console now (docs/operator/configuration.md)", d, strings.TrimSpace(hit))
		}
	}
}
