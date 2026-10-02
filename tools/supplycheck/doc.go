// Package supplycheck holds the supply-chain floor of this repository to the tree,
// offline, inside `go test ./...`.
//
// It has no code of its own; everything is in its tests. They read go.mod, every
// Dockerfile under deploy/ and lab/, and the Makefile, and assert:
//
//   - go.mod's `go` line is at or above the Go release that fixed every
//     standard-library vulnerability govulncheck found reachable from this code
//     (15 advisories at go1.26.2, the last of them fixed in go1.26.6);
//   - the two modules govulncheck named are at or above their fixed versions,
//     on the require line and through any `replace` that applies to it (a replace
//     is what the build uses, so a patched require line over a replace-down is
//     the vulnerable release);
//   - every golang builder image names an exact Go release in its tag, at or above
//     go.mod's `go` line — those images set GOTOOLCHAIN=local, so a builder below
//     the line refuses to build, and one that says only `1.26` hides which it is;
//   - every alpine base names its alpine branch, and that branch is still inside
//     alpine's own support window (alpine 3.20's support ended 2026-04-01, so a
//     runtime stage on it fails);
//   - `make vulncheck` exists, runs a pinned govulncheck, and `make ci` runs it;
//   - `make supply-verify` exists, runs network.sh in full, and `make ci` runs it.
//
// What it deliberately does NOT do is run govulncheck. That needs the network and
// a vulnerability database that changes under a fixed tree, which is the reason
// it is a Makefile target in `make ci` and not a test here. This package is the
// half of the story that can be checked with no network: the floor. The Makefile
// target is the half that notices when the floor has to move.
//
// It also reads the images deploy/images.conf pins
// (PostgreSQL's tag now names its alpine branch, so the alpine gate covers it), and
// eol_test.go holds PostgreSQL's major, ubuntu's release and debian's release — or a
// dated sid snapshot's age — to each project's own end of support.
//
// What it still cannot do offline is tell whether the digest beside a tag IS that
// image. tools/supplycheck/network.sh does, as `make supply-verify` inside `make ci`:
// every pinned tag must name its pinned digest at the registry today, and
// govulncheck runs in -mode=binary over the binaries deploy/Dockerfile.foyer builds.
// network_test.go proves that script's judgement against a stub registry.
package supplycheck
