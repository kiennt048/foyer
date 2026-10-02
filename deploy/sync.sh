#!/usr/bin/env bash
#
# Ship the committed tree to a deployment host, from a workstation.
#
# It ships `git archive HEAD`, and that choice is the point rather than a
# convenience: **you cannot deploy uncommitted work.** A file that is only in
# somebody's working tree cannot reach the host, so the host can never hold a change
# that is not in the repository — which is the whole of the rule in
# docs/rules/deployment.md, enforced by the transport instead of by intention.
#
# It refuses on a dirty tree for the same reason. A dirty tree means the thing you
# are about to ship is not the thing you are looking at.
#
#   FOYER_HOST=foyer-host deploy/sync.sh                 # ship HEAD
#   FOYER_HOST=foyer-host deploy/sync.sh --allow-dirty   # ship HEAD anyway, and keep a record that you did
#
#   FOYER_HOST     the host to ship to (required)
#   FOYER_USER     the SSH login on it (default: your local user name)
#   FOYER_SSH_KEY  an identity file to pass to ssh -i (default: ssh's own choice)
#   FOYER_ROOT     the deployment root on the host (default: /opt/foyer)
#
set -euo pipefail

HOST="${FOYER_HOST:?set FOYER_HOST to the host to deploy to, e.g. FOYER_HOST=foyer-host deploy/sync.sh}"
USER_AT="${FOYER_USER:-$(id -un)}"
DEST="${FOYER_ROOT:-/opt/foyer}/src"
SSH=(ssh -o BatchMode=yes)
[[ -n "${FOYER_SSH_KEY:-}" ]] && SSH+=(-i "${FOYER_SSH_KEY}")

allow_dirty=0
[[ "${1:-}" == "--allow-dirty" ]] && allow_dirty=1

cd "$(git rev-parse --show-toplevel)"

if [[ $allow_dirty -eq 0 ]] && ! git diff-index --quiet HEAD --; then
  echo "sync: the working tree is dirty." >&2
  echo "      git archive ships HEAD, so what you are looking at is not what would" >&2
  echo "      land on ${HOST}. Commit first, or pass --allow-dirty and keep a record" >&2
  echo "      that you did." >&2
  git status --short >&2
  exit 1
fi

rev="$(git rev-parse --short HEAD)"
echo "==> shipping ${rev} to ${USER_AT}@${HOST}:${DEST}"

"${SSH[@]}" "${USER_AT}@${HOST}" "mkdir -p '${DEST}'"

# --delete semantics without rsync: the destination is emptied of tracked content
# first, so a file removed in a commit is removed on the host. Anything the host
# generated for itself lives outside src/ — secrets/ is a sibling, deliberately.
git archive --format=tar HEAD \
  | "${SSH[@]}" "${USER_AT}@${HOST}" \
      "rm -rf '${DEST}'/* '${DEST}'/.[!.]* 2>/dev/null; tar -x -C '${DEST}'"

"${SSH[@]}" "${USER_AT}@${HOST}" \
  "printf '%s\n' '${rev}' > '${DEST}/.shipped-rev'"

# ── The manifest verify.sh checks against ────────────────────────────────────
#
# "No file changed under the managed paths" needs something to compare with, and
# it is computed HERE rather than on the host: hashing what landed would record
# whatever landed, which is a manifest that agrees with a hand-edit made during
# the copy. This hashes the same `git archive HEAD` the host is receiving, from
# the repository, on this machine.
#
# It lives in state/ and not in src/, because a manifest inside the tree it
# describes is a file that has to exclude itself, and because sync empties src/
# on every run.
STATE="${FOYER_ROOT:-/opt/foyer}/state"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
git archive --format=tar HEAD | tar -x -C "${tmp}"

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    xargs -0 sha256sum
  else
    xargs -0 shasum -a 256    # macOS; same `<hash>  <path>` form sha256sum -c reads
  fi
}
( cd "${tmp}" && find . -type f -print0 | sort -z | sha256 ) > "${tmp}.sha256"

"${SSH[@]}" "${USER_AT}@${HOST}" \
  "mkdir -p '${STATE}' && cat > '${STATE}/src.sha256'" < "${tmp}.sha256"
manifested="$(wc -l < "${tmp}.sha256" | tr -d ' ')"
rm -f "${tmp}.sha256"

echo "==> ${DEST} now holds ${rev} (${manifested} files manifested)"
echo "    Next on the host: deploy/upgrade.sh, or sudo bash deploy/first-boot.sh the first time."
echo "    Keep a record of what this changed."
