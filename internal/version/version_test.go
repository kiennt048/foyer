package version_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/kiennt048/foyer/internal/version"
)

// The three tests here answer three different questions and none of them
// substitutes for another:
//
//	1. does a stamped binary actually carry the value?          (the mechanism)
//	2. does the image build stamp the symbol that exists today?  (the wiring)
//	3. does the binary agree with the tag the image will carry?  (the two copies)
//
// (1) is the one that would have caught a build that cannot name itself. `verify.sh`
// could already report `revision=unknown` — it was right, and being right about a
// build that cannot name itself is not the same as a build that can.

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's working directory")
		}
		dir = parent
	}
}

func modulePath(t *testing.T, root string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	t.Fatal("go.mod declares no module path")
	return ""
}

// stampTarget derives the `-X` symbol from the source rather than restating it.
//
// **The name is found by shape, not by spelling.** `version.go` has exactly one
// package-level `string` variable with no initialiser — that is what a link-time
// stamp is — so renaming it here turns `deploy/Dockerfile.foyer` red instead of
// leaving a `-X` flag pointed at a symbol that no longer exists. The linker does
// not complain about that: it stamps nothing and exits 0, which is the same silent
// shape as a build that cannot name itself.
func stampTarget(t *testing.T, root, module string) string {
	t.Helper()
	file := filepath.Join(root, "internal", "version", "version.go")
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	var names []string
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Values) != 0 {
				continue
			}
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "string" {
				continue
			}
			for _, n := range vs.Names {
				names = append(names, n.Name)
			}
		}
	}
	if len(names) != 1 {
		t.Fatalf("internal/version declares %d uninitialised package-level string variables (%v); "+
			"exactly one is the link-time stamp, and this test cannot tell which. "+
			"Either there is a second one — give it an initialiser — or the stamp was deleted.",
			len(names), names)
	}
	return module + "/internal/version." + names[0]
}

// TestAStampedBinaryNamesItsCommitAndAnUnstampedOneSaysUnknown builds foyerctl three
// ways and reads what it says about itself.
//
// The middle build is the finding. `-buildvcs=false` is not an invented condition —
// it is exactly what `deploy/Dockerfile.foyer` gets for free, because `.dockerignore`
// excludes `.git` and `deploy/sync.sh` ships a tree rather than a repository. That
// is the build a deployment ran before the stamp existed, and it is why the label
// and the process both said `unknown`.
func TestAStampedBinaryNamesItsCommitAndAnUnstampedOneSaysUnknown(t *testing.T) {
	root := repoRoot(t)
	target := stampTarget(t, root, modulePath(t, root))
	dir := t.TempDir()

	build := func(name string, flags ...string) string {
		bin := filepath.Join(dir, name)
		args := append([]string{"build"}, flags...)
		args = append(args, "-o", bin, "./cmd/foyerctl")
		cmd := exec.Command("go", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		out, err := exec.Command(bin, "version").CombinedOutput()
		if err != nil {
			t.Fatalf("%s version: %v\n%s", bin, err, out)
		}
		return strings.TrimSpace(string(out))
	}

	// 1. The mechanism. A value nobody could have got from a repository, so a
	//    green result cannot be the fallback quietly answering instead.
	if got := build("stamped", "-ldflags", "-X "+target+"=d15ea5e"); !strings.Contains(got, "(revision d15ea5e)") {
		t.Errorf("a binary built with -X %s=d15ea5e says %q; the stamp did not reach it, "+
			"which is what deploy/Dockerfile.foyer relies on", target, got)
	}

	// 2. The defect, reproduced. Nothing stamped and no repository to read.
	if got := build("blind", "-buildvcs=false"); !strings.Contains(got, "(revision unknown)") {
		t.Errorf("a binary built with no stamp and no repository says %q, expected `unknown`; "+
			"if it found a revision, something other than these two sources is answering", got)
	}

	// 3. The workstation fallback, compared against **what the toolchain actually
	//    stamped** rather than against `git rev-parse HEAD`.
	//
	//    That distinction is the whole of case 4 below. Compared against HEAD,
	//    this assertion goes red in a linked worktree — not because the fallback
	//    is broken, but because `vcs.revision` and HEAD are **different facts**
	//    there. Comparing against HEAD restates a rule the toolchain owns; this
	//    reads the toolchain's own answer out of the binary, which is what the
	//    package's fallback is a view of.
	//
	//    Skipped on a condition a run can evaluate — an extracted tarball is not
	//    a repository — and never on a phase.
	if _, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output(); err != nil {
		t.Skipf("%s is not a repository, so the toolchain has nothing to stamp and there is "+
			"nothing for this case to compare against: %v", root, err)
	}
	plain := filepath.Join(t.TempDir(), "plain-probe")
	cmd := exec.Command("go", "build", "-o", plain, "./cmd/foyerctl")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	stamped := toolchainRevision(t, plain)
	if stamped == "" {
		t.Skip("the toolchain recorded no vcs.revision for a plain build, so the fallback has " +
			"no source and there is nothing here to compare against")
	}
	got := build("plain")
	// `+dirty` is tolerated and not required: measured, `go build` in a linked
	// worktree reports `vcs.modified=false` over modified tracked files.
	// Requiring the suffix would be asserting something the toolchain does not
	// reliably say.
	if !regexp.MustCompile(`\(revision ` + stamped[:7] + `(\+dirty)?\)`).MatchString(got) {
		t.Errorf("a plain `go build` in this repository says %q; the toolchain stamped %s into "+
			"the same binary, and the build-info fallback is the only thing a developer's "+
			"binary has", got, stamped[:7])
	}

	// 4. **The fallback is not trustworthy in a git worktree, and this says so out
	//    loud rather than letting case 3 discover it as noise.**
	//
	//    Measured in a linked worktree: the toolchain stamps the
	//    **main repository's** HEAD, not the worktree's, and reports
	//    `vcs.modified=false` while tracked files are modified. So a binary built
	//    in a worktree names a commit it was not built from, confidently — which
	//    is worse than `unknown`, because `unknown` is a fact and this is a wrong
	//    answer that reads like a right one.
	//
	//    It is not an assertion because it is not a defect this repository can
	//    fix; it is a property of the toolchain that every deployment path
	//    already bypasses (`-ldflags -X`, case 1). It is a `t.Log` so that the
	//    next person to read `Revision`'s doc comment finds the measurement
	//    behind it instead of rediscovering it.
	head, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err == nil && !strings.HasPrefix(strings.TrimSpace(string(head)), stamped) {
		t.Logf("the toolchain stamped %s and HEAD here is %s — a linked git worktree, where "+
			"`vcs.revision` names the MAIN repository's HEAD. A developer's binary built here "+
			"names a commit it was not built from. The deployment path does not rely on this "+
			"(deploy/Dockerfile.foyer passes -ldflags -X); a developer reading `foyerctl "+
			"version` in a worktree does.",
			stamped[:7], strings.TrimSpace(string(head))[:7])
	}
}

// toolchainRevision reads `vcs.revision` back out of a built binary.
//
// The authority for what the build-info fallback should answer is the build info
// itself, and `go version -m` is how it is read without linking the binary's own
// packages into this test.
func toolchainRevision(t *testing.T, bin string) string {
	t.Helper()
	out, err := exec.Command("go", "version", "-m", bin).Output()
	if err != nil {
		t.Fatalf("go version -m %s: %v", bin, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if _, rev, ok := strings.Cut(strings.TrimSpace(line), "vcs.revision="); ok {
			return rev
		}
	}
	return ""
}

// TestTheImageBuildStampsTheSymbolThatExists reads deploy/Dockerfile.foyer.
//
// It is a string comparison and it is not a substitute for the test above — it
// cannot see whether the flag works, only whether it names something real. The two
// together are what say the deployment path is wired: one proves `-X` reaches the
// binary, the other proves the Dockerfile passes the `-X` this package answers to.
func TestTheImageBuildStampsTheSymbolThatExists(t *testing.T) {
	root := repoRoot(t)
	target := stampTarget(t, root, modulePath(t, root))

	path := filepath.Join(root, "deploy", "Dockerfile.foyer")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	// Both binaries, because a support bundle collected with an unstamped foyerctl
	// is as blind as a control plane that cannot log its own commit.
	for _, binary := range []string{"./cmd/foyer", "./cmd/foyerctl"} {
		if !strings.Contains(text, binary) {
			t.Errorf("deploy/Dockerfile.foyer no longer builds %s; this test is checking a build that has moved", binary)
		}
	}

	want := "-X " + target + "=${REVISION}"
	if strings.Count(text, want) != 2 {
		t.Errorf("deploy/Dockerfile.foyer contains %q %d times, expected 2 (one per binary).\n"+
			"The symbol is derived from internal/version/version.go, so if that variable was "+
			"renamed the Dockerfile has to follow — a -X at a name that does not exist stamps "+
			"nothing and the linker says nothing.", want, strings.Count(text, want))
	}

	// The build argument has to be declared in the builder stage as well as the
	// runtime one: an ARG is scoped to the stage that names it, so a Dockerfile
	// that declares it only where the LABEL is written compiles the binaries with
	// an empty `${REVISION}` and labels the image correctly. That is worse than
	// both being unknown, because the label then vouches for a binary that has
	// nothing in it.
	if !strings.Contains(text, "ARG REVISION") {
		t.Error("deploy/Dockerfile.foyer declares no ARG REVISION")
	}
	if n := strings.Count(text, "ARG REVISION"); n < 2 {
		t.Errorf("deploy/Dockerfile.foyer declares ARG REVISION %d time(s); a build argument is "+
			"scoped per stage, and both the builder and the runtime stage need it", n)
	}
}

// TestTheReleaseVersionMatchesTheImageTag holds the two copies of the release
// string together.
//
// `deploy/images.conf` is what the image is tagged and pulled by; `Version` is what
// the binary, the support bundle and the log line report. They are two copies of one
// fact, which `docs/rules/database.md` has a section about, and the failure they
// produce is the same species as an unstamped build: an artefact that misreports
// which Foyer it is.
func TestTheReleaseVersionMatchesTheImageTag(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "deploy", "images.conf"))
	if err != nil {
		t.Fatal(err)
	}
	tagged := ""
	for _, line := range strings.Split(string(body), "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "FOYER_VERSION="); ok {
			tagged = strings.Trim(strings.TrimSpace(rest), `"'`)
		}
	}
	if tagged == "" {
		t.Fatal("deploy/images.conf declares no FOYER_VERSION, so nothing says what the image is tagged")
	}
	if tagged != version.Version {
		t.Errorf("deploy/images.conf tags the image %q and internal/version reports %q; "+
			"an operator pulling one and reading the other is told two different things", tagged, version.Version)
	}
}

// TestTheLabImageStampsTheSymbolThatExists is the Dockerfile test above for
// lab/Dockerfile.backend, and the two places that feed it the value.
//
// An earlier lab image passed no `-X` at all. Its context carries no `.git`
// (.dockerignore), so every lab binary said `revision unknown` — the unstamped-build
// defect, alive in the one place nobody deploys from and everybody tests in.
func TestTheLabImageStampsTheSymbolThatExists(t *testing.T) {
	root := repoRoot(t)
	target := stampTarget(t, root, modulePath(t, root))
	read := func(rel string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	docker := read("lab/Dockerfile.backend")
	if want := "-X " + target + "=${REVISION}"; strings.Count(docker, want) != 2 {
		t.Errorf("lab/Dockerfile.backend contains %q %d times, expected 2 (one per binary)", want, strings.Count(docker, want))
	}
	if !strings.Contains(docker, "ARG REVISION") {
		t.Error("lab/Dockerfile.backend declares no ARG REVISION, so ${REVISION} is empty in the build")
	}
	if compose := read("lab/docker-compose.yml"); !strings.Contains(compose, "REVISION: ${FOYER_REV") {
		t.Error("lab/docker-compose.yml passes no REVISION build argument")
	}
	// The property, not the spelling. This asserted one literal line, so a later
	// change — which put `FOYER_KEK` on the same command and wrapped it across two lines — turned it red with a correct Makefile
	// underneath. `FOYER_REV` reaching compose is the guarantee; where the
	// backslash lands is a formatting decision, and a test that reads formatting
	// has stopped reading the guarantee.
	if mk := read("Makefile"); !strings.Contains(mk, `FOYER_REV="$(REVISION)"`) ||
		!strings.Contains(mk, "docker compose -f lab/docker-compose.yml up -d --build") {
		t.Error("make lab-up does not hand the lab build its revision")
	}
}

// TestMakeStampsTheCommitOfTheCheckoutItRunsIn — `make build-go` names this
// checkout's commit, including in a linked worktree, where the toolchain's own
// `vcs.revision` names the main repository's HEAD instead (invariant 4 in
// CONTRACT.md). It reads the command make would run rather
// than building, and compares with git asked in the same directory.
func TestMakeStampsTheCommitOfTheCheckoutItRunsIn(t *testing.T) {
	root := repoRoot(t)
	if _, err := exec.LookPath("make"); err != nil {
		t.Skip("make is not installed")
	}
	head, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		t.Skipf("%s is not a repository, so there is no commit to stamp", root)
	}
	want := strings.TrimSpace(string(head))
	if exec.Command("git", "-C", root, "diff", "--quiet", "HEAD", "--").Run() != nil {
		want += "+dirty"
	}
	cmd := exec.Command("make", "-n", "build-go")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("make -n build-go: %v\n%s", err, out)
	}
	stamp := "-X " + stampTarget(t, root, modulePath(t, root)) + "=" + want + `"`
	for _, binary := range []string{"./cmd/foyer", "./cmd/foyerctl"} {
		found := false
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, binary) && strings.Contains(line, stamp) {
				found = true
			}
		}
		if !found {
			t.Errorf("make build-go does not build %s with %s:\n%s", binary, stamp, out)
		}
	}
}
