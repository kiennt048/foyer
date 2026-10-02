#!/usr/bin/env bash
#
# Bring a clean Ubuntu 24.04 host to the point where Foyer can be deployed on it.
#
# This is the ONE step a human performs, and it is performed with sudo. Everything
# after it runs as the deploy user without privilege, because the last thing this
# script does is put that user in the docker group. A provisioning system that needs
# a human for every step is not a provisioning system; one that needs a human for
# none has been given a password it should not hold.
#
# Idempotent: run it as many times as you like. It installs nothing that is already
# installed and changes nothing that is already correct.
#
# It does NOT install Foyer. It prepares the substrate — Docker, the deploy user,
# the layout, and the package baseline verify.sh compares against — and stops there.
# `deploy/install.sh` does this and the rest in one command, and is the supported
# path. This script is for an operator who ships the tree from a workstation:
#
#   sudo bash deploy/bootstrap.sh        # here: substrate
#   deploy/sync.sh                       # from a workstation: the committed tree
#   sudo bash deploy/first-boot.sh       # on the host: secrets, then start
#
set -euo pipefail

FOYER_ROOT="${FOYER_ROOT:-/opt/foyer}"
# The account that owns the deployment, derived as install.sh derives it: an
# explicit FOYER_DEPLOY_USER, then the owner of an existing ${FOYER_ROOT}, then
# whoever invoked sudo, then whoever is running this.
DEPLOY_USER="${FOYER_DEPLOY_USER:-${DEPLOY_USER:-}}"
if [[ -z "${DEPLOY_USER}" ]]; then
  DEPLOY_USER="$(stat -c %U "${FOYER_ROOT}" 2>/dev/null || true)"
  [[ -n "${DEPLOY_USER}" && "${DEPLOY_USER}" != "root" && "${DEPLOY_USER}" != "UNKNOWN" ]] \
    || DEPLOY_USER="${SUDO_USER:-$(id -un)}"
fi

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }

if [[ $EUID -ne 0 ]]; then
  echo "bootstrap: run me with sudo — I install packages and create ${FOYER_ROOT}" >&2
  exit 1
fi

# ── 1. Refuse a host this was not written for ────────────────────────────────
say "Checking the host"

. /etc/os-release
if [[ "${ID:-}" != "ubuntu" || "${VERSION_ID:-}" != "24.04" ]]; then
  echo "bootstrap: this expects Ubuntu 24.04; found ${PRETTY_NAME:-unknown}." >&2
  echo "           Refusing rather than guessing: the Docker repository line and the" >&2
  echo "           package names below are release-specific." >&2
  exit 1
fi
note "${PRETTY_NAME} on $(uname -m)"

if ! id -u "${DEPLOY_USER}" >/dev/null 2>&1; then
  echo "bootstrap: no such user ${DEPLOY_USER}. Set FOYER_DEPLOY_USER= and re-run." >&2
  exit 1
fi

# ── 2. Say the memory out loud ───────────────────────────────────────────────
#
# 2 GB shared by PostgreSQL, FreeRADIUS and the control plane. This is not a
# failure condition and it is not a detail either: FreeRADIUS sizes a FIXED thread
# pool with max_servers, and docs/rules/security.md §2 records that exhausting it blocks
# Status-Server too — so the health check dies with the thing it checks. Printing
# the number here means nobody has to rediscover it during an incident.
say "Memory"
mem_mb=$(( $(awk '/MemTotal/ {print $2}' /proc/meminfo) / 1024 ))
swap_mb=$(( $(awk '/SwapTotal/ {print $2}' /proc/meminfo) / 1024 ))
note "RAM ${mem_mb} MB, swap ${swap_mb} MB"
if (( mem_mb < 1800 )); then
  note "WARNING: under 1.8 GB. PostgreSQL and FreeRADIUS will contend."
fi
if (( swap_mb == 0 )); then
  note "WARNING: no swap. An OOM kill here takes the RADIUS server with it."
fi

# ── 2b. Record what was here before we touched anything ──────────────────────
#
# `deploy/verify.sh` has to be able to say "no package outside the manifest", and
# that sentence needs a baseline: Ubuntu 24.04 marks a few hundred packages as
# manually installed before Foyer arrives, so comparing against
# deploy/packages.txt alone would report the distribution as drift.
#
# **Captured before the installs below, and never overwritten.** A baseline taken
# afterwards would bake in whatever had already been done by hand, which is the
# one thing it exists to detect.
say "Baseline"
install -d -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" -m 0755 "${FOYER_ROOT}/state"
if [[ -s "${FOYER_ROOT}/state/packages.baseline" ]]; then
  note "packages.baseline already exists, leaving it alone — it records this host before Foyer"
else
  apt-mark showmanual 2>/dev/null | sort -u > "${FOYER_ROOT}/state/packages.baseline"
  chown "${DEPLOY_USER}:${DEPLOY_USER}" "${FOYER_ROOT}/state/packages.baseline"
  note "recorded $(wc -l < "${FOYER_ROOT}/state/packages.baseline") pre-existing packages"
fi

# ── 3. Docker, from Docker's own repository ──────────────────────────────────
#
# Not the distribution's docker.io: the compose plugin is what lab/ and deploy/
# both drive, and it is packaged separately and older there.
say "Docker"

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  note "already present: $(docker --version), $(docker compose version --short)"
else
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y -qq ca-certificates curl gnupg >/dev/null

  install -m 0755 -d /etc/apt/keyrings
  if [[ ! -f /etc/apt/keyrings/docker.asc ]]; then
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
  fi

  cat > /etc/apt/sources.list.d/docker.list <<EOF
deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${VERSION_CODENAME} stable
EOF

  apt-get update -qq
  apt-get install -y -qq \
    docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
  note "installed $(docker --version)"
fi

systemctl enable --now docker >/dev/null 2>&1 || true

# ── 4. The deploy user operates Docker without sudo ──────────────────────────
#
# This is the step that ends the need for a human. Membership in the docker group
# is root-equivalent on this host, which is a real statement and not a footnote:
# it is acceptable here because this box exists to run Foyer and nothing else, and
# because the alternative is a password living somewhere it can be read.
say "Deploy user"
if id -nG "${DEPLOY_USER}" | tr ' ' '\n' | grep -qx docker; then
  note "${DEPLOY_USER} is already in the docker group"
else
  usermod -aG docker "${DEPLOY_USER}"
  note "${DEPLOY_USER} added to the docker group — it takes effect on the next login"
fi

# ── 4b. Unattended sudo for the deploy user, if asked for ────────────────────
#
# Off by default; SUDO_NOPASSWD=1 turns it on. It exists because the alternative
# people reach for is worse: a password written into a file, or into a repository,
# where it is permanent and readable. This grants nothing new — step 4 already put
# this user in the docker group, which is root-equivalent on this host — it only
# stops a human being needed for the operations that are not Docker.
#
# Shipped as a file under deploy/, so who may do this is reviewable in a diff.
if [[ "${SUDO_NOPASSWD:-0}" == "1" ]]; then
  say "Unattended sudo"
  printf '%s ALL=(ALL) NOPASSWD:ALL\n' "${DEPLOY_USER}" > /etc/sudoers.d/60-foyer-deploy
  chmod 0440 /etc/sudoers.d/60-foyer-deploy
  # visudo -c refuses to leave a syntactically broken sudoers behind, which would
  # lock every administrator out of the host, including the one running this.
  if ! visudo -c -q; then
    rm -f /etc/sudoers.d/60-foyer-deploy
    echo "bootstrap: the sudoers drop-in did not validate; removed it and stopping." >&2
    exit 1
  fi
  note "${DEPLOY_USER} may now sudo without a password — /etc/sudoers.d/60-foyer-deploy"
  note "Remove that file to revoke it."
fi

# ── 5. Where the deployment lives ────────────────────────────────────────────
say "Layout"
install -d -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" -m 0755 "${FOYER_ROOT}"
install -d -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" -m 0755 "${FOYER_ROOT}/src"
# 0700: this is where first-boot generated secrets will live. docs/rules/security.md §7
# requires them generated on the host and present in no repository, so the
# directory that will hold them is created with the right mode before anything
# can be written into it with the wrong one.
install -d -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" -m 0700 "${FOYER_ROOT}/secrets"
note "${FOYER_ROOT}/src       the deployed tree, replaced whole by deploy/sync.sh"
note "${FOYER_ROOT}/secrets   mode 0700, generated on this host, in no repository"
note "${FOYER_ROOT}/state     what this host knows about itself: the package baseline,"
note "                        the shipped-file manifest, backups. Not shipped, not secret."

say "Done"
note "Nothing of Foyer is installed yet."
note "Next: either sudo bash deploy/install.sh from a checkout of the release on this host,"
note "      or FOYER_HOST=<this host> deploy/sync.sh from a workstation and then"
note "      sudo bash ${FOYER_ROOT}/src/deploy/first-boot.sh here."
