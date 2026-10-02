package version_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A build naming its own revision has two halves, and this file is the other one.
//
// `internal/version` answers *what does the binary say about itself*. It cannot
// answer *where does the value come from*, and on a deployed instance that is the
// fragile half: `git rev-parse` inside `/opt/foyer/src`, a directory the tree was
// copied into with `--exclude=.git`, gets nothing — and a script that then wrote
// the literal string `unknown` would, when `deploy/sync.sh` had already put a
// correct revision there, **overwrite it**.
//
// These tests live beside the Go half rather than in a `deploy/` harness of their
// own because `go test ./...` is the gate this repository actually runs, and a
// branch nothing runs is a branch nobody checks. That is also why the resolution
// lives in `deploy/revision.sh` rather than in `install.sh`: `install.sh` needs
// root and Ubuntu 24.04 and cannot be driven here at all.

func revisionScript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(repoRoot(t), "deploy", "revision.sh")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// `deploy/sync.sh` ships `git archive`, which preserves the mode bit, and
	// `install.sh` executes this path directly. A file that lost +x would be a
	// deployment that dies at the revision step rather than one that misreports.
	if info.Mode()&0o111 == 0 {
		t.Fatalf("%s is mode %v and deploy/install.sh runs it directly", path, info.Mode())
	}
	return path
}

// run drives revision.sh and returns its source tag, revision, and whether it
// claimed to have found one.
func run(t *testing.T, checkout, shipped string) (string, string, bool) {
	t.Helper()
	out, err := exec.Command(revisionScript(t), checkout, shipped).Output()
	line := strings.TrimSpace(string(out))
	source, rev, ok := strings.Cut(line, "\t")
	if !ok {
		t.Fatalf("deploy/revision.sh printed %q, which is not `<source>\\t<revision>`", line)
	}
	return source, rev, err == nil
}

// TestTheRevisionComesFromTheCheckoutWhenThereIsOne is the branch a shipped tree
// never takes.
//
// It compares against `git rev-parse` rather than against a string typed here, so
// the assertion is about *the repository's answer* and not about a value the test
// and the script could be wrong about together.
func TestTheRevisionComesFromTheCheckoutWhenThereIsOne(t *testing.T) {
	root := repoRoot(t)
	head, err := exec.Command("git", "-C", root, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		t.Skipf("%s is not a repository, so there is no checkout answer to compare against: %v", root, err)
	}
	want := strings.TrimSpace(string(head))

	// A shipped tree carrying a *different* revision, so a pass cannot be the
	// fallback answering. The checkout must win over a note somebody left.
	shipped := t.TempDir()
	writeShippedRev(t, shipped, "0000000")

	source, rev, found := run(t, root, shipped)
	if !found || source != "checkout" || rev != want {
		t.Errorf("revision.sh(%s) = %q/%q found=%v; want checkout/%s. "+
			"The checkout is the authoritative answer and must beat a stale .shipped-rev",
			root, source, rev, found, want)
	}
}

// TestAShippedRevisionSurvivesACheckoutThatIsNotARepository is the regression.
//
// This is exactly what happened on a deployed instance: `deploy/sync.sh` had put a
// real revision in `/opt/foyer/src/.shipped-rev`, `install.sh` ran, could not read
// one, and wrote `unknown` over it. Nothing downstream could recover — the images
// were relabelled from the clobbered file on the next `up.sh`.
func TestAShippedRevisionSurvivesACheckoutThatIsNotARepository(t *testing.T) {
	shipped := t.TempDir()
	writeShippedRev(t, shipped, "abc1234")

	source, rev, found := run(t, notARepository(t), shipped)
	if !found || source != "shipped" || rev != "abc1234" {
		t.Errorf("revision.sh over a non-repository checkout = %q/%q found=%v; want shipped/abc1234. "+
			"An install that cannot read a revision must not destroy the one sync.sh established",
			source, rev, found)
	}
}

// TestUnknownIsNeverPresentedAsAnAnswer covers the two states that genuinely have
// no revision, and asserts the *exit status* as well as the text.
//
// The status is the load-bearing half. Ending this path with `|| echo "unknown"`
// would be a failure wearing the shape of a result — the caller could not tell
// them apart, so it would write the file either way.
func TestUnknownIsNeverPresentedAsAnAnswer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		shipped func(*testing.T) string
	}{
		{"no shipped tree at all", func(t *testing.T) string { return t.TempDir() }},
		{"a shipped tree that itself says unknown", func(t *testing.T) string {
			dir := t.TempDir()
			writeShippedRev(t, dir, "unknown")
			return dir
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, rev, found := run(t, notARepository(t), tc.shipped(t))
			if found {
				t.Error("revision.sh exited 0 with no revision to report; the caller cannot then " +
					"distinguish an answer from a failure, which is the whole defect")
			}
			if source != "none" || rev != "unknown" {
				t.Errorf("got %q/%q, want none/unknown", source, rev)
			}
		})
	}
}

// TestRootIsAllowedToReadTheRevisionOfSomebodyElsesCheckout is a spelling check and
// says so, because the condition it stands for cannot be created here.
//
// `install.sh` runs under sudo over a checkout owned by the invoking user. Since
// CVE-2022-24765 git refuses a repository it does not own, and it refuses by
// exiting non-zero — straight into the `unknown` branch, with the repair looking
// finished. `-c safe.directory` is what prevents that, and creating a
// differently-owned directory needs a second uid this test does not have. Recorded
// as reasoned from git's documented behaviour, not measured.
func TestRootIsAllowedToReadTheRevisionOfSomebodyElsesCheckout(t *testing.T) {
	body, err := os.ReadFile(revisionScript(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `safe.directory=`) {
		t.Error("deploy/revision.sh no longer passes -c safe.directory. Under sudo, git refuses " +
			"a repository owned by another user and exits non-zero, which this script reads as " +
			"`no revision here` — the failure looks exactly like an ordinary tarball install")
	}
}

func writeShippedRev(t *testing.T, dir, rev string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".shipped-rev"), []byte(rev+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// notARepository returns a directory with no repository above it.
//
// `t.TempDir()` is not enough on its own to *prove* that — a temporary directory
// under a checkout would have one — so the result is asserted rather than assumed.
// Without this the two negative tests could pass for the wrong reason on a machine
// whose temp directory sits inside a repository.
func notARepository(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Run(); err == nil {
		t.Skipf("%s is inside a repository, so it cannot stand in for a tree with no revision", dir)
	}
	return dir
}

// TestUpRunsFarEnoughToReachItsOwnRefusals is the third half, and it exists because
// a use-before-assignment in `deploy/up.sh` is invisible **until the script is run
// on a deployed instance.**
//
// A block that sources `deploy/images.conf` placed above the line that assigns
// `SRC` is, under `set -u`, not a degradation but an abort: `up.sh: line 25: SRC:
// unbound variable`, on every invocation.
//
// **shellcheck does not catch it, measured.** Both the broken and the correct file
// are clean at `-S warning`: `SC2154` fires for a variable assigned *nowhere*, and
// `SRC` is assigned — eight lines later. Reaching for the linter that looks like it
// covers this is the wrong instinct and it is written down here so the next person
// does not spend the afternoon on it.
//
// What catches it is running the thing. The assertion is deliberately not *"up.sh
// succeeds"* — it cannot, off a real host with no compose file and no secrets. It is
// that the script reaches **its own** first refusal, the one its author wrote, rather
// than dying in the shell before any of its guards can speak. That distinguishes the
// two failures cleanly, which is the whole requirement:
//
//	broken → up.sh: line 25: SRC: unbound variable
//	fixed  → up: …/src/deploy/compose.yaml is not there. Run deploy/sync.sh …
func TestUpRunsFarEnoughToReachItsOwnRefusals(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "deploy", "up.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("deploy/up.sh is not there: %v", err)
	}
	cmd := exec.Command("bash", script)
	cmd.Dir = root
	// A FOYER_ROOT with nothing under it. Every guard in the script fails against
	// it, which is the point: the first one to speak says whether the script got
	// that far under its own control.
	cmd.Env = append(os.Environ(), "FOYER_ROOT="+t.TempDir())
	out, err := cmd.CombinedOutput()
	got := strings.TrimSpace(string(out))
	if err == nil {
		t.Fatalf("up.sh succeeded against an empty FOYER_ROOT, so this assertion has lost "+
			"its subject: every guard in the script should have refused.\n%s", got)
	}
	if !strings.HasPrefix(got, "up: ") {
		t.Errorf("up.sh died before any of its own guards could speak:\n\t%s\n\n"+
			"\tA line starting `up: ` is the script refusing; anything else is bash "+
			"refusing on its behalf, which under `set -u` means a variable is used above "+
			"the line that assigns it. shellcheck does not see this — measured, both the "+
			"broken and the fixed file are clean — so running it is the only check there is.",
			got)
	}
}
