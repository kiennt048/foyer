#!/usr/bin/env bash
#
# Install Foyer on a clean Ubuntu 24.04 host. One script, start to finish.
#
#   sudo bash deploy/install.sh
#
# It is the three lower-level scripts — bootstrap, sync, first-boot — with the
# seam between them removed: "a stranger installs it and a phone gets online" has
# to be one command, and three scripts in two places with an scp between them is
# not a thing a stranger completes.
#
# What it does, in order, and every step is idempotent:
#
#   1. refuses anything that is not Ubuntu 24.04, and refuses to run unprivileged
#   2. records the package baseline verify.sh compares against — BEFORE installing
#   3. installs Docker from Docker's own repository
#   4. creates /opt/foyer, its secrets directory, and the deploy user's access
#   5. generates every secret on this host, once, and never rotates one that exists
#   6. pulls the published images, pinned by digest
#   7. migrates, generates the FreeRADIUS tree, starts the stack
#   8. opens the console's first-access setup and prints its one-time code once
#
# It does NOT configure a venue, an access point or a certificate. Those are the
# operator's, and step 8 prints what is left.
#
# RE-RUNNABLE. Run it again after an upgrade, or to repair permissions: it
# regenerates nothing and enforces everything.
#
# To place a new release without starting it — the first half of an upgrade —
# run it from the new release's checkout with --secrets-only, then upgrade.sh:
#
#   sudo bash deploy/install.sh --secrets-only
#   bash /opt/foyer/src/deploy/upgrade.sh
set -euo pipefail

FOYER_ROOT="${FOYER_ROOT:-/opt/foyer}"
# The account that owns the deployment. An explicit FOYER_DEPLOY_USER wins; then
# the owner of an existing ${FOYER_ROOT}, so a re-run by a different administrator
# does not re-own the install; then whoever invoked sudo; then whoever is running
# this. first-boot.sh, bootstrap.sh and backup.sh derive it the same way, and it is
# exported below so first-boot.sh is told rather than left to guess.
DEPLOY_USER="${FOYER_DEPLOY_USER:-}"
if [[ -z "${DEPLOY_USER}" ]]; then
  DEPLOY_USER="$(stat -c %U "${FOYER_ROOT}" 2>/dev/null || true)"
  [[ -n "${DEPLOY_USER}" && "${DEPLOY_USER}" != "root" && "${DEPLOY_USER}" != "UNKNOWN" ]] \
    || DEPLOY_USER="${SUDO_USER:-$(id -un)}"
fi
SRC="${FOYER_ROOT}/src"
# Where the integrity manifest lives — outside the tree it describes. See the
# manifest block below, and `sync.sh`, which writes the same file.
STATE="${FOYER_ROOT}/state"

say()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die()  { printf '\033[31minstall: %s\033[0m\n' "$*" >&2; exit 1; }

# ── 0. Where am I, and may I ────────────────────────────────────────────────
[[ $EUID -eq 0 ]] || die "run me with sudo — I install packages, create users and chown private keys"

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
[[ -f "${here}/deploy/compose.yaml" ]] || die "run me from a Foyer checkout: I need deploy/compose.yaml beside me"

say "Preflight"
. /etc/os-release
if [[ "${ID:-}" != "ubuntu" || "${VERSION_ID:-}" != "24.04" ]]; then
  die "this expects Ubuntu 24.04; found ${PRETTY_NAME:-unknown}.
    Foyer is container-only and the pinned FreeRADIUS package comes from
    NetworkRADIUS's noble repository. Another release may work and has not been
    tested, and a security product should not guess about its own base."
fi
note "${PRETTY_NAME}, $(dpkg --print-architecture)"

mem_mb=$(( $(awk '/MemTotal/ {print $2}' /proc/meminfo) / 1024 ))
swap_mb=$(( $(awk '/SwapTotal/ {print $2}' /proc/meminfo) / 1024 ))
note "RAM ${mem_mb} MB, swap ${swap_mb} MB"
(( mem_mb >= 1500 )) || note "WARNING: under 1.5 GB. PostgreSQL and FreeRADIUS will be tight."
(( swap_mb > 0 ))    || note "WARNING: no swap. An OOM kill here takes the RADIUS server with it."

# ── 1. The images, and whether they are pinned ──────────────────────────────
say "Images"
# shellcheck source=/dev/null
. "${here}/deploy/images.conf"
: "${FOYER_IMAGE_REPO:?deploy/images.conf is missing FOYER_IMAGE_REPO}"
: "${FOYER_VERSION:?deploy/images.conf is missing FOYER_VERSION}"

build_locally=0
if [[ -z "${FOYER_IMAGE_FOYER}" || -z "${FOYER_IMAGE_FREERADIUS}" ]]; then
  if [[ "${FOYER_ALLOW_UNPINNED:-0}" == "1" ]]; then
    note "images.conf carries no digest — building locally because FOYER_ALLOW_UNPINNED=1"
    build_locally=1
    FOYER_IMAGE_FOYER="foyer:${FOYER_VERSION}"
    FOYER_IMAGE_FREERADIUS="foyer-freeradius:${FOYER_VERSION}"
  else
    die "deploy/images.conf names no published image digest.
    Nothing has been pushed for ${FOYER_VERSION} yet, so there is nothing to pull.
    Either publish first (deploy/publish.sh), or build on this host with:

        sudo FOYER_ALLOW_UNPINNED=1 bash deploy/install.sh

    That second form is a development convenience. It produces an image nobody
    else can verify and that verify.sh has no published digest to compare with,
    and it is refused by default rather than taken quietly."
  fi
else
  note "foyer       ${FOYER_IMAGE_FOYER}"
  note "freeradius  ${FOYER_IMAGE_FREERADIUS}"
fi

# ── 2. Baseline, before anything is installed ───────────────────────────────
say "Baseline"
install -d -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" -m 0755 "${FOYER_ROOT}/state"
if [[ -s "${FOYER_ROOT}/state/packages.baseline" ]]; then
  note "packages.baseline exists — it records this host before Foyer, leaving it alone"
else
  apt-mark showmanual 2>/dev/null | sort -u > "${FOYER_ROOT}/state/packages.baseline"
  chown "${DEPLOY_USER}:${DEPLOY_USER}" "${FOYER_ROOT}/state/packages.baseline"
  note "recorded $(wc -l < "${FOYER_ROOT}/state/packages.baseline") pre-existing packages"
fi

# ── 3. Docker ───────────────────────────────────────────────────────────────
say "Docker"
if command -v docker >/dev/null && docker compose version >/dev/null 2>&1; then
  note "already present: $(docker --version)"
else
  install -m 0755 -d /etc/apt/keyrings
  if [[ ! -s /etc/apt/keyrings/docker.asc ]]; then
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
  fi
  cat > /etc/apt/sources.list.d/docker.list <<EOF
deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${VERSION_CODENAME} stable
EOF
  apt-get update -qq
  DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
  note "installed $(docker --version)"
fi
getent group docker >/dev/null && id -nG "${DEPLOY_USER}" | grep -qw docker || {
  usermod -aG docker "${DEPLOY_USER}"
  note "added ${DEPLOY_USER} to the docker group — a new login is needed for it to take effect"
}

# ── 4. Layout, and the tree this host runs ──────────────────────────────────
say "Layout"

install -d -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" -m 0755 "${FOYER_ROOT}" "${SRC}"
if [[ "$(cd "${here}" && pwd)" != "${SRC}" ]]; then
  # Running from a checkout somewhere else: copy it into place so the host runs
  # what verify.sh will later measure, rather than whatever directory happened to
  # be current when somebody typed the command.
  tar -C "${here}" -cf - --exclude=.git . | tar -C "${SRC}" -xf -
  chown -R "${DEPLOY_USER}:${DEPLOY_USER}" "${SRC}"
  note "copied the tree into ${SRC}"
else
  note "already running from ${SRC}"
fi

# **Which commit is this?** It is asked of the checkout — `${here}` — not of
# `${SRC}`, which the copy above strips `.git` out of and which `sync.sh` never had
# one in. deploy/revision.sh carries the three branches and the reason each
# exists; the only thing decided here is what to say about the answer.
#
# `.shipped-rev` is not part of the shipped tree, so an extraction over the top
# leaves an existing one for revision.sh's second branch to find — which is what
# stops this run from overwriting a good revision with `unknown`.
# **The integrity manifest verify.sh compares the tree against.**
#
# `sync.sh` writes it too. Without it here, a host installed the documented way —
# one script, start to finish — would have `verify.sh` report on every run that
# `src.sha256` is missing and the host "was populated some other way", when the
# other way is the entry point the README gives.
#
# Computed from `${SRC}` rather than from `${here}`, because `${SRC}` is what
# `verify.sh` measures and what the container runs — the two must be the same
# question. The manifest lives in `state/` for the reason `sync.sh` gives: one
# inside the tree it describes would have to exclude itself.
#
# Written **before** `.shipped-rev`, so the file the next line creates is not in
# the manifest — the same asymmetry `sync.sh` has, and for the same reason:
# `.shipped-rev` is not part of the shipped tree.
install -d -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" -m 0755 "${STATE}"
( cd "${SRC}" && find . -type f -print0 | sort -z | xargs -0 sha256sum ) > "${STATE}/src.sha256"
chown "${DEPLOY_USER}:${DEPLOY_USER}" "${STATE}/src.sha256"
note "manifested $(wc -l < "${STATE}/src.sha256" | tr -d ' ') files under ${SRC}"

rev_answer="$("${SRC}/deploy/revision.sh" "${here}" "${SRC}")" && rev_found=1 || rev_found=0
# Tab-separated, and written as $'\t' rather than a literal so a later edit
# cannot silently turn the separator into spaces.
rev_source="${rev_answer%%$'\t'*}"
rev="${rev_answer##*$'\t'}"
if [[ "${rev_found}" -eq 1 ]]; then
  printf '%s\n' "${rev}" > "${SRC}/.shipped-rev"
  case "${rev_source}" in
    checkout) note "revision ${rev} (read from the checkout at ${here})" ;;
    *)        note "revision ${rev} (kept — deploy/sync.sh shipped it, and ${here} is not a repository)" ;;
  esac
else
  printf 'unknown\n' > "${SRC}/.shipped-rev"
  note "revision unknown — ${here} is not a repository and nothing here has shipped one."
  note "  Every image built below is labelled and stamped \`unknown\`, and deploy/verify.sh"
  note "  will report that as drift, correctly: nothing will be able to say which commit"
  note "  is serving guests. Install from a git checkout of the release, or ship with"
  note "  deploy/sync.sh from a workstation, to get an answer."
fi
chown "${DEPLOY_USER}:${DEPLOY_USER}" "${SRC}/.shipped-rev" 2>/dev/null || true

# ── 5–8. Secrets, images, migration, start, first administrator ─────────────
#
# first-boot.sh owns all of it and is not duplicated here. This script's job is to
# make sure it is reachable in one command from a clean machine, not to re-implement
# what it already does correctly.
export FOYER_IMAGE_FOYER FOYER_IMAGE_FREERADIUS FOYER_IMAGE_POSTGRES FOYER_VERSION
export FOYER_BUILD_LOCALLY="${build_locally}"
# first-boot.sh owns the secrets and chowns them to this account, so it is told
# which one rather than left to a default of its own.
export FOYER_ROOT
export FOYER_DEPLOY_USER="${DEPLOY_USER}"
exec bash "${SRC}/deploy/first-boot.sh" "$@"
