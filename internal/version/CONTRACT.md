# internal/version — CONTRACT

Which release, and which commit, a running binary is. Imports only the standard
library; the package comment says why the revision is stamped rather than discovered.

## Invariants

1. **`Version` is the release, and it equals `deploy/images.conf`'s `FOYER_VERSION`.**
   `TestTheReleaseVersionMatchesTheImageTag` reads that file rather than restating
   it. `cmd/foyerctl`'s `Version` is an alias of this constant, not a copy.
2. **`Revision()` has three sources, in this order:** the link-time stamp
   (`-X github.com/kiennt048/foyer/internal/version.revision=…`); else the
   toolchain's `vcs.revision`, cut to seven characters, with `+dirty` when
   `vcs.modified=true`; else `Unknown`.
3. **`Unknown` (`"unknown"`) is an answer, not a placeholder.** A build that cannot
   name itself says so; `deploy/verify.sh` treats it as drift.
4. **`+dirty` is a courtesy, not a guarantee.** Its absence is not evidence of a clean
   tree: in a linked git worktree with modified files, the toolchain reports
   `vcs.modified=false` and stamps the main repository's HEAD rather than the
   worktree's. So every build path in the repository stamps explicitly: `deploy/Dockerfile.foyer`, `lab/Dockerfile.backend`
   and `make build-go`, which asks `git rev-parse --short HEAD` in the checkout it
   runs in and appends `+dirty` itself when a tracked file differs from HEAD. Only a
   bare `go build` still gets the toolchain's answer.
5. **`revision` is the one package-level string variable with no initialiser.**
   `TestTheImageBuildStampsTheSymbolThatExists` finds it by that shape and asserts
   `deploy/Dockerfile.foyer` stamps it exactly twice (both binaries) and declares
   `ARG REVISION` in both stages; `TestTheLabImageStampsTheSymbolThatExists` does the
   same for `lab/Dockerfile.backend`, its compose build argument and `make lab-up`;
   `TestMakeStampsTheCommitOfTheCheckoutItRunsIn` reads `make -n build-go` and
   compares with git asked in the same directory. A `-X` at a name that does not
   exist is silent to the linker; these tests are what make a rename red.
6. **`String()` is the one format:** `<Version> (revision <Revision()>)`.
   `TestAStampedBinaryNamesItsCommitAndAnUnstampedOneSaysUnknown` builds `foyerctl`
   stamped, with `-buildvcs=false`, and plain, and reads what each prints.

`revision_script_test.go` lives here too and drives `deploy/revision.sh` (where the
stamp comes from) and `deploy/up.sh`, because `go test ./...` is the gate that runs.

## What this package will not do

- Discover a revision at run time. There is no repository in a container.
- Read the image label. That is `deploy/verify.sh`'s second surface, compared with this one.
