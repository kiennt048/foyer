#!/usr/bin/env bash
#
# Upgrade the deployment to the tree that has already been placed in
# ${FOYER_ROOT}/src.
#
# **The first person to install must be able to upgrade.** This is what an
# upgrade is, in order, with the reason each step is where it is:
#
#   0. place the tree   from a checkout of the new release on the host:
#                         sudo bash deploy/install.sh --secrets-only
#                       (copies it into ${FOYER_ROOT}/src, records its revision
#                       and manifest, starts nothing) — or deploy/sync.sh from a
#                       workstation
#   1. back up          deploy/backup.sh, before anything moves — the same
#                       script the daily timer runs, so an upgrade's backup is
#                       an ordinary one that deploy/restore.sh can read
#   2. secrets          anything the new code needs and this install lacks;
#                       a changed database login is STAGED, not switched
#   3. images           pull the pinned ones, or build locally, while the old
#                       ones are still serving
#   4. stop the app     foyer and freeradius only; PostgreSQL stays up
#   5. migrate          one-shot, must exit 0; it grants the new login
#   5b. switch login    promote the staged database_url (deploy/logins.sh)
#   6. start            the new images
#   7. verify           deploy/verify.sh
#
# **The database is migrated with the application stopped, and that ordering is
# the whole safety argument.** Run in the other order and the old binary is
# serving live guests against a schema it was not built for — which is a
# different failure on every request and looks like flakiness rather than like an
# upgrade.
#
# The portal and RADIUS are down between steps 4 and 6: a container restart plus
# however long the migrations take.
#
# ── When a migration fails part way ──────────────────────────────────────────
#
# goose wraps each migration in a transaction, and no migration in
# internal/store/migrations declares `-- +goose NO TRANSACTION`. So a failure
# leaves the database at the **last version that fully applied** — never half of
# one. Concretely:
#
#   * Step 5 exits non-zero and this script stops. Nothing is started, so nothing
#     is serving guests against a schema it does not understand.
#   * `foyerctl migrate --status` names which migrations applied and which did
#     not. That is the first thing to run, and it is read-only.
#   * To go back: `foyerctl migrate --to <version>` runs the down migrations, and
#     then place the previous release (step 0) and run this again.
#     Every migration 00001–00011 has a `-- +goose Down`; a future one that does
#     not is a migration with no way back, and that is worth knowing before it
#     ships rather than during an outage.
#   * The backup from step 1 is the answer when the down migrations are not
#     enough — a `DROP COLUMN` reverses cleanly in schema and not in data. It is
#     a dump `deploy/backup.sh` wrote, so `deploy/restore.sh` reads it: **and it
#     needs `${FOYER_ROOT}/secrets/kek`**, which is not in the dump and never
#     will be. That script refuses by name without it rather than quietly
#     producing an installation nobody can decrypt.
#
set -euo pipefail

FOYER_ROOT="${FOYER_ROOT:-/opt/foyer}"
SRC="${FOYER_ROOT}/src"
COMPOSE="${SRC}/deploy/compose.yaml"
BACKUPS="${FOYER_ROOT}/state/backups"

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die() { printf 'upgrade: %s\n' "$*" >&2; exit 1; }

[[ -f "${COMPOSE}" ]] || die "${COMPOSE} is not there. Place the release first: from a checkout of it, sudo bash deploy/install.sh --secrets-only"

# first-boot.sh as root, which it has to be: it chowns secrets to the container's uid.
as_root() {
  if [[ $EUID -eq 0 ]]; then
    bash "$@"
  else
    sudo bash "$@"
  fi
}

# The images, the compose files and the console's names, from the files up.sh and
# first-boot.sh read them from. Below the line that sets SRC, which `set -u` would
# otherwise turn into "SRC: unbound variable"; and with the build overlay when
# images.sh says to build, because compose.yaml alone has no build section and an
# install whose images.conf names no published image could not be upgraded.
# shellcheck source=images.sh
. "${SRC}/deploy/images.sh"
# shellcheck source=consolenames.sh
. "${SRC}/deploy/consolenames.sh"
COMPOSE_FILES=("${FOYER_COMPOSE_FILES[@]}")
FOYER_REV="$(cat "${SRC}/.shipped-rev" 2>/dev/null || true)"
[[ -n "${FOYER_REV}" ]] || die "${SRC}/.shipped-rev is missing; place the release with sudo bash deploy/install.sh --secrets-only from its checkout, which writes it"
export FOYER_REV

set -a
# shellcheck disable=SC1091 # generated on the host, deliberately not in the repo
. "${FOYER_ROOT}/secrets/foyer.env"
set +a

say "Upgrading to ${FOYER_REV}"

# ── 1. Back up ───────────────────────────────────────────────────────────────
#
# Before the build, not after: a build that fills the disk is a thing that has
# happened, and the backup is worth more than the build.
#
# **Through `deploy/backup.sh`, not an inline `pg_dump`.** An inline dump of its
# own would carry no manifest and no KEK fingerprint, so nothing could say
# afterwards whether it was restorable, and `deploy/restore.sh` — which needs both
# — could not read it.
#
# One backup, one definition, one set of refusals. `backup.sh` refuses to
# continue on a failed dump, and it prunes by age and prints the inventory, so
# upgrades do not fill the disk with dumps nobody will ever prune.
say "Backup"
if ! bash "${SRC}/deploy/backup.sh"; then
  die "the backup failed; refusing to migrate without one"
fi
# The newest dump, for the two places below that name it. Read back rather than
# reconstructed, so the path in an error message is the path that was written.
backup="$(ls -1t "${BACKUPS}"/*.dump 2>/dev/null | head -1 || true)"
[[ -n "${backup}" ]] || die "backup.sh reported success but ${BACKUPS} holds no dump; refusing to migrate"

# ── 2. Secrets the new code needs and this install may not have ──────────────
#
# `first-boot.sh --secrets-only` is idempotent and never rotates: it generates
# what is absent and leaves what is present alone. Things an older install may
# not have, and the upgrade fails without:
#
#   * **a client certificate for FreeRADIUS.** The rlm_rest boundary is mutual
#     TLS, and rlm_rest opens the CA at instantiation — so without this,
#     FreeRADIUS does not start and the control plane refuses to.
#   * **${FOYER_ROOT}/secrets/database_url.** FOYER_DATABASE_URL and FOYER_KEK
#     are docker secrets rather than environment variables, and compose cannot
#     start a service whose secret file is missing.
#   * **maintenance_token**: the maintenance jobs run in their own `maintenance`
#     service, which the control plane reaches with this token; compose cannot
#     start either without the file.
#   * **app_db_password, radius_db_password, owner_database_url** (migration
#     00019). The control plane and FreeRADIUS do not log in as the superuser.
#     first-boot.sh generates the two passwords and STAGES a database_url that
#     logs in as foyer_app, as database_url.next; step 5's migrate grants that
#     login and sets its password, and step 5b switches to it. cmd/foyer refuses
#     the old URL, but the old binary still running until step 4 does not — so
#     the file is staged rather than rewritten here, or an old control plane that
#     restarted during the build would come back on a login that does not exist.
#
# It also rewrites foyer.env, which is why first-boot.sh reads an existing
# FOYER_PORTAL_URL back out of it: re-deriving that from `ip route` on every
# upgrade would silently replace an operator's deliberate value with a guess.
say "Secrets"
as_root "${SRC}/deploy/first-boot.sh" --secrets-only

# foyer.env changed under us; re-read it.
set -a
# shellcheck disable=SC1091 # generated on the host, deliberately not in the repo
. "${FOYER_ROOT}/secrets/foyer.env"
set +a

# ── 3. Images, while the old ones are still serving ──────────────────────────
say "Images"
if [[ "${FOYER_BUILD_LOCALLY:-0}" == "1" ]]; then
  docker compose "${COMPOSE_FILES[@]}" build
else
  docker compose "${COMPOSE_FILES[@]}" pull --quiet
fi

# ── 4. Stop the application, keep the database ───────────────────────────────
say "Stopping the application"
note "The portal and RADIUS are down from here until the last step."
docker compose "${COMPOSE_FILES[@]}" stop freeradius foyer maintenance

# ── 5. Migrate ───────────────────────────────────────────────────────────────
say "Migrations"
docker compose "${COMPOSE_FILES[@]}" run --rm migrate foyerctl migrate --status || true
if ! docker compose "${COMPOSE_FILES[@]}" run --rm migrate; then
  printf '\n' >&2
  echo "upgrade: the migration failed. NOTHING HAS BEEN STARTED." >&2
  echo "         goose runs each migration in a transaction, so the database is at the" >&2
  echo "         last version that fully applied — not half of one." >&2
  echo "         The control plane's login was NOT switched: a staged database_url.next" >&2
  echo "         is still staged, so the previous image starts on the login it had." >&2
  echo "" >&2
  echo "         Look:     ${SRC}/deploy/compose.sh run --rm migrate foyerctl migrate --status" >&2
  echo "         Go back:  ${SRC}/deploy/compose.sh run --rm migrate foyerctl migrate --to <version>" >&2
  echo "                   then place the previous release (sudo bash deploy/install.sh" >&2
  echo "                   --secrets-only from its checkout) and run this again." >&2
  echo "         Restore:  ${SRC}/deploy/restore.sh ${backup}" >&2
  echo "                   — it needs ${FOYER_ROOT}/secrets/kek, and says so by name if it is absent" >&2
  exit 1
fi

# ── 5b. Switch the control plane's login ─────────────────────────────────────
#
# Only now, with the login granted and its password set, and with the control plane
# stopped: nothing can have read the new file before the database accepted it.
say "Database login"
as_root "${SRC}/deploy/first-boot.sh" --promote-logins

# ── 6. Start ─────────────────────────────────────────────────────────────────
say "Starting"
docker compose "${COMPOSE_FILES[@]}" up -d

ready=0
for _ in $(seq 1 60); do
  if docker compose "${COMPOSE_FILES[@]}" exec -T foyer wget --no-check-certificate -q -O- https://127.0.0.1:8443/healthz >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done
[[ ${ready} -eq 1 ]] || die "the control plane did not become healthy after the upgrade. ${SRC}/deploy/compose.sh logs foyer"

# ── 7. Verify ────────────────────────────────────────────────────────────────
# An install upgraded past migration 00040 whose administrator still held the
# old shipped password has none now (the migration deleted it, because that
# password is published). It gets the same first-access setup a fresh install
# does; on every other install this prints one line and opens nothing.
say "Administrator"
. "${SRC}/deploy/consolesetup.sh"
open_console_setup "https://$(hostname -I 2>/dev/null | awk '{print $1}'):8443" -f "${COMPOSE}" \
  || die "opening the console setup failed; the stack is up but nobody may be able to administer it"

say "Verifying"
bash "${SRC}/deploy/verify.sh"

say "Done"
note "Keep a record of this upgrade: a host whose history has a gap is a machine that drifted on a date nobody knows."
