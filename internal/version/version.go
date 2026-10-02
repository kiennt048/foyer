// Package version answers a question a running Foyer could not answer: **which
// commit is serving guests?**
//
// It existed nowhere. `deploy/verify.sh` asked the OCI label on the image, which is
// the right question addressed to the wrong object — the label is a property of a
// tarball the daemon happens to be holding, so answering it needs the Docker socket,
// it says nothing inside a support bundle or a log line, and a container started
// from a re-tagged image answers whatever the tag's builder passed. The binary
// itself said only `0.1.0-alpha`, which is the same string for every build ever
// made of this release.
//
// ── why the revision has to be stamped in, and cannot be discovered ──────────
//
// Go stamps `vcs.revision` into `debug.BuildInfo` by itself whenever the toolchain
// can see a repository, and that is exactly what a developer's `go build` gets — so
// the fallback below is not a courtesy, it is the whole answer on a workstation.
//
// **It is the wrong answer in a linked git worktree.** The toolchain stamps the
// *main repository's* HEAD there, not the worktree's, and reports
// `vcs.modified=false` either way — so a binary built in a worktree names a
// plausible short SHA that is not its own, and nothing flags it. That is worse than
// [Unknown], which at least `deploy/verify.sh` treats as drift. It reaches no
// deployment, because every deployment path uses the stamp below; it reaches every
// developer who runs `foyerctl version` from a worktree.
//
// So `make build-go` stamps the revision itself, from `git rev-parse` in the
// checkout it runs in; a bare `go build` still gets the toolchain's answer.
// `make lab-up` stamps the lab image the same way.
//
// It is never the answer in a container. `deploy/Dockerfile.foyer` builds from a
// context that carries no `.git` (`.dockerignore` excludes it), and on a
// deployed instance there is no repository to exclude in the first place:
// `deploy/sync.sh` ships `git archive`, which is a tree. The toolchain therefore
// has nothing to read, records no `vcs.*` settings, and — because `-buildvcs` is
// `auto` — does not complain about it either. **The silence is the failure mode**:
// the build succeeds, the binary is correct, and it cannot name itself.
//
// So the revision arrives the only way it can, as a value the builder is told:
// `-ldflags -X` in the image build, fed the same `REVISION` build argument that
// becomes `org.opencontainers.image.revision`. One value, two surfaces, and
// `deploy/verify.sh` now asks both — because a label and a binary that disagree is
// a re-tagged image, which is precisely the thing a digest pin exists to prevent
// and the unpinned install path cannot.
package version

import (
	"runtime/debug"
	"strings"
)

// Version is the release this tree describes.
//
// `deploy/images.conf` carries the same string as `FOYER_VERSION`, because it is
// what the image is tagged with, and `TestTheReleaseVersionMatchesTheImageTag`
// reads that file rather than restating it. Two copies of a release number that
// drift are how a container tagged 0.2 reports itself as 0.1.
const Version = "0.1.0-alpha"

// Unknown is what every surface says when nothing stamped a revision in and there
// is no repository to fall back on.
//
// It is a real answer and not a placeholder to be tidied away: `deploy/verify.sh`
// treats it as drift. A build that cannot name itself must say so, because the
// alternative — printing the release version and stopping — reads like an answer.
const Unknown = "unknown"

// revision is stamped at link time by the image build. It is the only package-level
// string variable here with no initialiser, and `TestTheImageBuildStampsTheSymbolThatExists`
// finds it by that shape rather than by name, so renaming it turns the Dockerfile's
// `-X` flag red instead of silent.
var revision string

// Revision returns the commit this binary was built from, or [Unknown].
//
// Three sources, in the order of how much they can be trusted:
//
//  1. the link-time stamp, which is what a container carries;
//  2. `vcs.revision` from the build info, which is what a workstation build
//     carries and which the toolchain records without being asked;
//  3. [Unknown].
//
// `+dirty` is appended when the toolchain says the tree was modified, and the
// narrow claim is the true one: **its absence is not evidence of a clean tree.**
// In a linked worktree with modified tracked files, `go version -m` reports
// `vcs.modified=false`. So read `+dirty` as *the toolchain
// noticed*, never as *this is exactly that commit*. A deployment gets its revision
// stamped by `deploy/sync.sh`, which refuses a dirty tree itself; this suffix is a
// courtesy on a workstation and not a guarantee anywhere.
func Revision() string {
	if r := strings.TrimSpace(revision); r != "" {
		return r
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Unknown
	}
	var rev string
	var dirty bool
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return Unknown
	}
	if len(rev) > 7 {
		rev = rev[:7]
	}
	if dirty {
		return rev + "+dirty"
	}
	return rev
}

// String is what every surface prints: the release, and which build of it.
//
// One function rather than a format string per call site. `foyerctl version` and
// the control plane's first log line print exactly this, and `deploy/verify.sh`
// parses `(revision …)` out of the former, so the shape is load-bearing. The
// support bundle's manifest carries the same two facts as separate fields —
// `version` is [Version] and `revision` is [Revision] — so an operator reading a
// bundle and an operator reading a log are comparing like with like.
func String() string { return Version + " (revision " + Revision() + ")" }
