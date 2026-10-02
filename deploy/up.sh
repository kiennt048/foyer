#!/usr/bin/env bash
#
# Start (or restart) the deployment from the shipped tree.
#
# It exists for one reason that is not convenience: **`FOYER_REV` has to be in
# the environment when compose builds.** It becomes the image tag and the
# `org.opencontainers.image.revision` label, and the revision linked into the
# binaries themselves, so a support bundle and a log line can answer the
# question too rather than only `docker inspect`. A plain `docker compose up`
# builds `foyer:unknown`, and verify.sh reports that as drift — correctly,
# because a host that cannot name its own revision has drifted from a repository
# whether or not the bytes match.
#
#   deploy/up.sh
#
# Run as the deploy user, after deploy/first-boot.sh has generated the secrets.
#
set -euo pipefail

FOYER_ROOT="${FOYER_ROOT:-/opt/foyer}"

SRC="${FOYER_ROOT}/src"
COMPOSE="${SRC}/deploy/compose.yaml"


die() { printf 'up: %s\n' "$*" >&2; exit 1; }

[[ -f "${COMPOSE}" ]] || die "${COMPOSE} is not there. Install with: sudo bash deploy/install.sh, from a checkout of the release — it places the tree in ${SRC}."
[[ -s "${FOYER_ROOT}/secrets/foyer.env" ]] || die "${FOYER_ROOT}/secrets/foyer.env is missing. Run: sudo bash ${SRC}/deploy/first-boot.sh --secrets-only"
[[ -s "${FOYER_ROOT}/secrets/postgres_password" ]] || die "${FOYER_ROOT}/secrets/postgres_password is missing. Run first-boot.sh --secrets-only."
# The least-privileged logins (migration 00019). An install from before them
# has neither file, and compose cannot start a service whose secret is missing —
# nor would cmd/foyer start on the old database_url, which logs in as the
# superuser. `up.sh` does not run first-boot.sh, so it says so instead.
# Since migration 00021 the maintenance jobs log in as their own roles too.
for f in owner_database_url app_db_password radius_db_password reaper_db_password retention_db_password reaper_database_url retention_database_url maintenance_token; do
  [[ -e "${FOYER_ROOT}/secrets/${f}" ]] || die "${FOYER_ROOT}/secrets/${f} is missing: this install predates the separate database logins. Run: sudo bash ${SRC}/deploy/first-boot.sh --secrets-only"
done

# The directories the control plane writes into, enforced on every start rather
# than once at first boot. `up.sh` does not run `first-boot.sh`, so a rule that
# lives only there is a rule the upgrade path does not have, and the symptom is a
# control plane crash-looping on a console certificate it cannot write. See
# `deploy/secretdirs.sh`.
# shellcheck source=/dev/null
. "${SRC}/deploy/secretdirs.sh"

# The images, and whether they must be built here. One file decides it and
# `deploy/first-boot.sh` reads the same one — see its header for why.
#
# **Below the guards, not above them.** Sourcing it first would make this script
# die on a missing file instead of on `${COMPOSE} is not there`, which is a worse
# sentence about the same situation.
# shellcheck source=/dev/null
. "${SRC}/deploy/images.sh"

# The names the console's certificate must cover, computed from this host on every
# start. Third file in the same pattern and for the same reason as the two above:
# `up.sh` does not run `first-boot.sh`, so a rule that lives only there is a rule
# the upgrade path does not have.
#
# This is what makes an existing install pick up a changed address:
# `internal/tlsca` replaces a stored certificate that does not cover a declared
# name, so the box regenerates once, by itself, and then leaves it alone.
# shellcheck source=/dev/null
. "${SRC}/deploy/consolenames.sh"

FOYER_REV="$(cat "${SRC}/.shipped-rev" 2>/dev/null || true)"
[[ -n "${FOYER_REV}" ]] || die "${SRC}/.shipped-rev is missing or empty; deploy/install.sh and deploy/sync.sh write it. Without it the image cannot say which commit it is."
export FOYER_REV

# FOYER_PORTAL_URL is in foyer.env for the container, and compose also
# interpolates it into this file's own `environment:` block; sourcing it here
# keeps the two from disagreeing.
set -a
# shellcheck disable=SC1091 # generated on the host, deliberately not in the repo
. "${FOYER_ROOT}/secrets/foyer.env"
set +a

echo "==> bringing up ${FOYER_REV}"
# A database login first-boot.sh staged (deploy/logins.sh) is switched here, after
# the pull and immediately before `up` — which runs migrate, and so grants it,
# before it recreates the control plane that reads it.
# shellcheck source=logins.sh
. "${SRC}/deploy/logins.sh"
if [[ "${FOYER_BUILD_LOCALLY:-0}" == "1" ]]; then
  foyer_promote_logins "${FOYER_ROOT}/secrets"
  docker compose "${FOYER_COMPOSE_FILES[@]}" up -d --build
else
  docker compose "${FOYER_COMPOSE_FILES[@]}" pull --quiet
  foyer_promote_logins "${FOYER_ROOT}/secrets"
  docker compose "${FOYER_COMPOSE_FILES[@]}" up -d
fi
docker compose "${FOYER_COMPOSE_FILES[@]}" ps
