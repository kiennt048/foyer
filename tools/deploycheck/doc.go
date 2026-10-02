// Package deploycheck runs the deployment's shell scripts against a sandbox and
// asserts what they DO, not what they say.
//
// It has no code of its own; everything is in its tests. deploy/ is the host as
// code (`docs/rules/deployment.md`), and a script nothing executes outside the host
// itself can read `${SRC}` a line before it sets it — a script that `set -u` stops at
// its first expansion and that no review of the prose notices.
//
// The sandbox is a temporary FOYER_ROOT with the real script under test copied into
// `src/deploy/`, its collaborators replaced by stubs that append one line each to a
// log, and `docker` and `sudo` on PATH doing the same. The assertions read the log:
// the ORDER in which an upgrade does things is its safety argument, so the order is
// what is tested.
//
// What it cannot see: anything Docker, PostgreSQL or the host would do. Those are
// `deploy/verify.sh`'s, against a running machine.
package deploycheck
