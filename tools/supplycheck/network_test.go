package supplycheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// network.sh needs a registry, so `make supply-verify` is where it really runs. What
// can be proven offline is its judgement: given what the registry answers, does it
// pass the pins that match, fail the one that does not, and — with --update —
// rewrite exactly that one. The registry is a stub `docker` that answers
// `buildx imagetools inspect` from a table.

// registryStub answers every tag with the digest pinned beside it in the tree, or
// with a different one for the tags listed in MOVED (space-separated), and with a
// linux/amd64 image unless NO_AMD64 names the tag.
const registryStub = `#!/bin/bash
ref="$4"
digest="$(grep -h -o "${ref}@sha256:[0-9a-f]\{64\}" "$TREE"/deploy/Dockerfile* "$TREE"/deploy/images.conf | head -1)"
digest="${digest#*@}"
for m in $MOVED; do [[ "$m" == "$ref" ]] && digest="sha256:$(printf '%064d' 7)"; done
arch=amd64
for m in $NO_AMD64; do [[ "$m" == "$ref" ]] && arch=arm64; done
[[ -n "$digest" ]] || { echo "not found: $ref" >&2; exit 1; }
cat <<EOF
{
  "schemaVersion": 2,
  "mediaType": "application/vnd.oci.image.index.v1+json",
  "digest": "${digest}",
  "manifests": [
    {
      "platform": {
        "architecture": "${arch}",
        "os": "linux"
      }
    }
  ]
}
EOF
`

// networkTree copies network.sh and the files it reads into a temporary tree, so
// --update can rewrite them without touching the repository.
func networkTree(t *testing.T) string {
	t.Helper()
	tree := t.TempDir()
	files := []string{"tools/supplycheck/network.sh", "deploy/images.conf"}
	m, _ := filepath.Glob(filepath.Join(repoRoot, "deploy", "Dockerfile*"))
	for _, p := range m {
		rel, _ := filepath.Rel(repoRoot, p)
		files = append(files, rel)
	}
	for _, f := range files {
		b := []byte(read(t, f))
		dst := filepath.Join(tree, f)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(tree, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(registryStub), 0o755); err != nil {
		t.Fatal(err)
	}
	return tree
}

func runNetwork(t *testing.T, tree string, env []string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(tree, "tools/supplycheck/network.sh")}, args...)...)
	cmd.Env = append(os.Environ(), append(env,
		"PATH="+filepath.Join(tree, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TREE="+tree)...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// pins returns every name:tag@digest the tree pins, from the same sources network.sh reads.
func pins(t *testing.T, tree string) []string {
	t.Helper()
	re := regexp.MustCompile(`[a-z0-9./-]+:[A-Za-z0-9._-]+@sha256:[0-9a-f]{64}`)
	var out []string
	m, _ := filepath.Glob(filepath.Join(tree, "deploy", "Dockerfile*"))
	for _, p := range append(m, filepath.Join(tree, "deploy", "images.conf")) {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, re.FindAllString(string(b), -1)...)
	}
	if len(out) < 4 {
		t.Fatalf("found %d digest pins; deploy/ has at least four, so this is looking in the wrong place", len(out))
	}
	return out
}

func TestSupplyVerifyPassesPinsTheRegistryAgreesWith(t *testing.T) {
	tree := networkTree(t)
	out, err := runNetwork(t, tree, nil, "--images")
	if err != nil {
		t.Fatalf("every pin matches its tag and network.sh failed: %v\n%s", err, out)
	}
	if n := strings.Count(out, "  ok    "); n != len(pins(t, tree)) {
		t.Errorf("%d ok lines for %d pins:\n%s", n, len(pins(t, tree)), out)
	}
}

// The attack and the upstream rebuild look the same from here, and both must fail.
func TestSupplyVerifyFailsAPinItsTagDoesNotName(t *testing.T) {
	tree := networkTree(t)
	moved := strings.SplitN(pins(t, tree)[0], "@", 2)[0]
	out, err := runNetwork(t, tree, []string{"MOVED=" + moved}, "--images")
	if err == nil {
		t.Fatalf("%s names another digest at the registry and network.sh passed:\n%s", moved, out)
	}
	if !strings.Contains(out, "FAIL  ") || !strings.Contains(out, moved+" names sha256:0000") {
		t.Errorf("the failure does not name %s:\n%s", moved, out)
	}
	if !strings.Contains(out, "make supply-pin") {
		t.Errorf("the failure does not say how to re-pin:\n%s", out)
	}
}

func TestSupplyVerifyFailsAPinWithNoAmd64Image(t *testing.T) {
	tree := networkTree(t)
	tag := strings.SplitN(pins(t, tree)[0], "@", 2)[0]
	out, err := runNetwork(t, tree, []string{"NO_AMD64=" + tag}, "--images")
	if err == nil || !strings.Contains(out, "has no linux/amd64 image") {
		t.Fatalf("a pin with no linux/amd64 image passed (err %v):\n%s", err, out)
	}
}

// --update rewrites the moved pin and only that one, and the result then verifies.
func TestSupplyPinRewritesOnlyTheMovedPin(t *testing.T) {
	tree := networkTree(t)
	before := pins(t, tree)
	moved := strings.SplitN(before[0], "@", 2)[0]
	if out, err := runNetwork(t, tree, []string{"MOVED=" + moved}, "--update"); err != nil {
		t.Fatalf("--update failed: %v\n%s", err, out)
	}
	after := pins(t, tree)
	for i := range before {
		switch {
		case strings.HasPrefix(before[i], moved+"@"):
			if after[i] != moved+"@sha256:"+strings.Repeat("0", 63)+"7" {
				t.Errorf("the moved pin became %s", after[i])
			}
		case after[i] != before[i]:
			t.Errorf("--update rewrote a pin that had not moved: %s → %s", before[i], after[i])
		}
	}
}

// TestSupplyVerifyRunsInCI is TestVulncheckRunsInCI's shape for the networked half:
// the target exists, runs network.sh in its checking mode, and `make ci` calls it
// before ci-integration, which is red today and would stop make before it.
func TestSupplyVerifyRunsInCI(t *testing.T) {
	mk := read(t, "Makefile")
	recipe := func(target string) string {
		m := regexp.MustCompile(`(?ms)^` + regexp.QuoteMeta(target) + `:[^\n]*\n((?:\t[^\n]*\n)+)`).FindStringSubmatch(mk)
		if m == nil {
			return ""
		}
		return m[1]
	}
	sv := recipe("supply-verify")
	if !strings.Contains(sv, "bash tools/supplycheck/network.sh") || strings.Contains(sv, "--images") || strings.Contains(sv, "--update") {
		t.Errorf("`supply-verify` must run tools/supplycheck/network.sh with no flag (both halves); it runs:\n%s", sv)
	}
	if !strings.Contains(sv, "GOVULNCHECK_VERSION=$(GOVULNCHECK_VERSION)") {
		t.Errorf("`supply-verify` must hand network.sh the pinned GOVULNCHECK_VERSION; it runs:\n%s", sv)
	}
	if !strings.Contains(recipe("supply-pin"), "network.sh --update") {
		t.Error("`supply-pin` must run network.sh --update")
	}
	ci := recipe("ci")
	is, ii := strings.Index(ci, "$(MAKE) supply-verify"), strings.Index(ci, "$(MAKE) ci-integration")
	switch {
	case is < 0:
		t.Errorf("`make ci` does not run supply-verify; its recipe is:\n%s", ci)
	case ii >= 0 && is > ii:
		t.Error("`make ci` runs supply-verify after ci-integration, so a red ci-integration would stop it ever running")
	}
	if !strings.Contains(recipe("check-all"), "go mod tidy -diff") {
		t.Error("`check-all` no longer runs `go mod tidy -diff`; without it go.mod can drift untidy with nothing asking")
	}
	src := read(t, "tools/supplycheck/network.sh")
	for _, want := range []string{"docker buildx imagetools inspect", "-mode=binary", "--output \"type=local"} {
		if !strings.Contains(src, want) {
			t.Errorf("network.sh no longer contains %q", want)
		}
	}
}
