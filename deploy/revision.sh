#!/usr/bin/env bash
#
# Which commit is this tree? One question, one answer, one place.
#
#   deploy/revision.sh <checkout-dir> [<shipped-tree-dir>]
#
# Prints `<source><TAB><revision>` and exits 0 when it found one, or
# `none<TAB>unknown` and exits 1 when nothing on this machine can say.
#
# ── why this is a file and not four lines inside install.sh ──────────────────
#
# The obvious four lines inside install.sh are wrong:
#
#     ( cd "${SRC}" && git rev-parse --short HEAD 2>/dev/null || echo "unknown" ) \
#       > "${SRC}/.shipped-rev"
#
# run *after* install.sh has copied the tree into ${SRC} with `--exclude=.git`, they
# ask a tree that by construction is not a repository, and `|| echo "unknown"`
# turns the failure into something that reads like an answer: `.shipped-rev` holds
# the literal string `unknown`, every locally built image is labelled
# `revision=unknown`, and `verify.sh` reports a true drift nobody can clear. Worse,
# when `deploy/sync.sh` has already written a correct revision, they **overwrite
# it**.
#
# Separate because it is the one piece of that script with more than one branch,
# and a branch nothing can run is a branch nobody checks. `internal/version`'s test
# drives all three of them.
#
# ── the two traps ────────────────────────────────────────────────────────────
#
#   * **git need not be installed.** `deploy/packages.txt` does not list it, and
#     `deploy/sync.sh` ships `git archive` — a tree, not a repository. Its absence
#     is a fallback, never a failure.
#   * **install.sh runs under sudo and the checkout belongs to somebody else.**
#     Since CVE-2022-24765 git refuses a repository it does not own — *detected
#     dubious ownership* — and it refuses by exiting non-zero, straight back into
#     the `unknown` branch. `-c safe.directory` is what lets root read the revision
#     of one directory without declaring every repository on the box safe forever.
#
set -uo pipefail

checkout="${1:?usage: revision.sh <checkout-dir> [<shipped-tree-dir>]}"
shipped_dir="${2:-}"

# 1. This tree, if it is a repository. The most authoritative answer there is: it
#    was read from the thing being installed rather than from a note about it.
if command -v git >/dev/null 2>&1; then
  rev="$(git -c "safe.directory=${checkout}" -C "${checkout}" rev-parse --short HEAD 2>/dev/null || true)"
  if [[ -n "${rev}" ]]; then
    printf 'checkout\t%s\n' "${rev}"
    exit 0
  fi
fi

# 2. What a previous run established. `deploy/sync.sh` writes `.shipped-rev` from a
#    workstation, where the repository is, and it is not part of the shipped tree —
#    so an extraction over the top leaves it alone. **`unknown` is not an answer**
#    and must never displace one: that asymmetry is the regression this file exists
#    to prevent, and it is a separate case in the test.
if [[ -n "${shipped_dir}" && -s "${shipped_dir}/.shipped-rev" ]]; then
  prev="$(tr -d '[:space:]' < "${shipped_dir}/.shipped-rev")"
  if [[ -n "${prev}" && "${prev}" != "unknown" ]]; then
    printf 'shipped\t%s\n' "${prev}"
    exit 0
  fi
fi

# 3. Nothing. Said out loud, and non-zero, because a build that cannot name itself
#    is a real state an operator has to be told about rather than a default.
printf 'none\tunknown\n'
exit 1
