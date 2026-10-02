#!/usr/bin/env bash
#
# Take a backup of the Foyer database, keep a bounded number of them, and say
# what it kept.
#
#   deploy/backup.sh                 # one backup, then prune, then report
#   deploy/backup.sh --list          # what is in the backup directory, no dump
#   deploy/backup.sh --install       # install the systemd timer that runs it
#   FOYER_BACKUP_KEEP=14 deploy/backup.sh
#
# Run on the host, as the deploy user. It is what `backup.timer` runs, and what
# `upgrade.sh` calls instead of its own inline `pg_dump`.
#
# ── why this is a script and not a line in upgrade.sh ────────────────────────
#
# A `pg_dump` taken only before an upgrade has **no schedule, no retention and no
# pruning**, and nothing that can restore it. A backup nobody has taken is not a
# backup, and "a deployed Foyer can be restored by someone who did not build it"
# needs all four.
#
# So the dump happens on a **schedule** (a systemd timer
# installed by `--install`, because a schedule nobody installed is a comment), it
# **prunes by age** (an unbounded backup directory on a 40 GB disk is a disk
# full, which is a failed restore with extra steps), and it **prints what it
# kept** — a backup you cannot inventory is a backup you cannot restore.
#
# ── what a backup is, and what it is not ─────────────────────────────────────
#
# A `pg_dump` is a backup of the **database**. It is not a backup of the
# installation, and the difference is the whole reason `restore.sh` exists:
#
#   * `/opt/foyer/secrets` holds the KEK, the database passwords, the signing
#     keys and the TLS material. None of it is in the database, by design.
#   * The KEK unseals every RADIUS shared secret, every controller credential
#     and every NT hash. **`internal/store/envelope.go` prefixes each sealed
#     value with the magic `FKv` and the key id** and the rest is AES-GCM
#     ciphertext, so a database restored without the KEK is a table of bytes.
#
# So this script says, on every run, that the dump is ciphertext without the KEK,
# and it records **the KEK's SHA-256 fingerprint** in the manifest beside each
# dump. That is what lets `verify.sh` answer "is this restorable in principle"
# without a second database and without ever reading a secret: a dump whose
# manifest names a KEK fingerprint is a dump somebody can match a KEK against.
#
# **The KEK is not copied into the backup directory**, and the reasoning is in
# `restore.sh`'s header where the decision is made: a backup is the thing most
# likely to be copied somewhere less protected, and a copy that carries the key
# is a copy of every shared secret in the venue.
#
set -euo pipefail

FOYER_ROOT="${FOYER_ROOT:-/opt/foyer}"
SRC="${FOYER_ROOT}/src"
STATE="${FOYER_ROOT}/state"
SECRETS="${FOYER_ROOT}/secrets"
# The account the timer runs as (`--install`, under sudo), derived as install.sh
# derives it: an explicit FOYER_DEPLOY_USER, then the owner of an existing
# ${FOYER_ROOT}, then whoever invoked sudo, then whoever is running this.
DEPLOY_USER="${FOYER_DEPLOY_USER:-}"
if [[ -z "${DEPLOY_USER}" ]]; then
  DEPLOY_USER="$(stat -c %U "${FOYER_ROOT}" 2>/dev/null || true)"
  [[ -n "${DEPLOY_USER}" && "${DEPLOY_USER}" != "root" && "${DEPLOY_USER}" != "UNKNOWN" ]] \
    || DEPLOY_USER="${SUDO_USER:-$(id -un)}"
fi

# How many to keep. Fourteen daily ones is a fortnight of undo. It is not a disk
# figure — a dump of this schema is a few MB on a 40 GB disk — and that is the
# point: the constraint here is an operator forgetting that backups exist, not
# space.
KEEP="${FOYER_BACKUP_KEEP:-14}"

die()  { printf 'backup: %s\n' "$*" >&2; exit 1; }
say()  { printf '\n==> %s\n' "$*"; }
note() { printf '    %s\n' "$*"; }

# The naming, the fingerprint and the listing live in deploy/backups.sh because
# `restore.sh` and `verify.sh` need the same three and a fingerprint computed two
# ways is a fingerprint that eventually disagrees with itself.
# shellcheck source=backups.sh
. "${SRC}/deploy/backups.sh"

# ── the dump, through compose ────────────────────────────────────────────────
#
# `compose.sh` rather than a bare `docker compose`, for the reason that file
# exists: compose.yaml's image pins and FOYER_ADMIN_TLS_NAMES come from there,
# and a bare call fails with "required variable … is missing a value" — or, with
# stderr discarded, prints nothing at all.
#
# The superuser login, because a `pg_dump` must read every table including the
# ones `foyer_app` is deliberately refused (migration 00019). The
# password goes in as PGPASSWORD through `docker compose exec -e`, never on a
# command line, because `ps` shows those.
#
# `--format=custom` rather than plain SQL: it is compressed, `pg_restore` can
# restore it selectively, and — the reason that matters here — **`pg_restore
# --list` reads its table of contents without a database**, which is what makes
# "is this dump restorable in principle" answerable at all.
#
# **`--no-acl` is deliberately NOT passed.** A dump taken with `--no-acl`
# carries no GRANT statements, so a restored database has the schema and none of
# the privileges on it — and the control plane then dies at start with
#
#     reading the network settings: ERROR: permission denied for table
#     network_settings (SQLSTATE 42501)
#
# which reads like a broken image and is not one. Nothing puts the grants back:
# migration 00019 is the statement that made them, goose records it as applied,
# and the dump restored that record, so `foyerctl migrate` correctly declines to
# re-run it. **The privileges are part of the schema, not part of the data, and
# `--no-acl` throws away half of what a restore needs.**
#
# `--no-owner` stays: the objects belong to `foyer` on both sides, and carrying
# an owner list would make a restore depend on a role name matching.
#
# One wrapper for every call, so "through compose.sh, with the password in the
# environment" is stated once rather than at each of the two call sites. PGPASS is
# set below, after the guards.
compose() { PGPASSWORD="${PGPASS}" "${SRC}/deploy/compose.sh" "$@"; }

pg_dump_to() {
  local out="$1"
  compose exec -T -e PGPASSWORD="${PGPASS}" \
    postgres pg_dump -U foyer --format=custom --no-owner foyer > "${out}"
}

# ── --list ───────────────────────────────────────────────────────────────────
if [[ "${1:-}" == "--list" ]]; then
  [[ -d "${BACKUPS}" ]] || die "${BACKUPS} does not exist; nothing has been backed up"
  printf 'backup\tage\tkek fingerprint\n'
  foyer_backup_list
  exit 0
fi

# ── --install: the schedule ──────────────────────────────────────────────────
#
# A systemd timer rather than a line in root's crontab, for two reasons that are
# both about this directory's rules. A timer is a **file in
# /etc/systemd/system that this repository generates**, so `verify.sh` can assert
# it exists; a crontab line is a thing a human typed, which
# `docs/rules/deployment.md` calls a defect precisely because it is absent from the
# repository. And a timer survives a reboot on its own — on a box that is off the
# network for days, that is the whole question.
if [[ "${1:-}" == "--install" ]]; then
  [[ $EUID -eq 0 ]] || die "--install writes /etc/systemd/system; run it with sudo"
  cat > /etc/systemd/system/foyer-backup.service <<UNIT
[Unit]
Description=Foyer database backup
# After the deployment, because a dump needs the postgres container to exist.
Requires=docker.service
After=docker.service

[Service]
Type=oneshot
# The deploy user, not root. A backup is a read of the database and a write into
# state/; neither needs more, and root would make the timer the operator's most
# privileged unattended thing, which is not what a backup is.
User=${DEPLOY_USER}
WorkingDirectory=${FOYER_ROOT}
ExecStart=${SRC}/deploy/backup.sh
# A dump that takes longer than this has met a database that is not answering,
# and a hung backup nobody notices is the same as no backup.
TimeoutStartSec=600
UNIT
  cat > /etc/systemd/system/foyer-backup.timer <<UNIT
[Unit]
Description=Run the Foyer database backup daily

[Timer]
# 03:17 rather than 03:00, and that is the entire reasoning: every deployment
# ever written puts its own jobs on the hour, so an install on this LAN with a
# 03:00 timer competes for the same PostgreSQL.
OnCalendar=*-*-* 03:17:00
# Persistent, so a backup missed while the machine was off runs at the next boot.
Persistent=true
# Jitter, so a fleet of installs does not converge on one instant.
RandomizedDelaySec=900

[Install]
WantedBy=timers.target
UNIT
  systemctl daemon-reload
  systemctl enable --now foyer-backup.timer
  say "Installed"
  note "daily at 03:17, persistent, up to 15 minutes of jitter"
  note "check it:  systemctl list-timers foyer-backup.timer"
  note "run one now: ${SRC}/deploy/backup.sh"
  exit 0
fi

# ── guards ───────────────────────────────────────────────────────────────────
[[ -f "${SRC}/deploy/compose.sh" ]] || die "${SRC}/deploy/compose.sh is not there. Install with: sudo bash deploy/install.sh, from a checkout of the release — it places the tree in ${SRC}."
[[ -s "${SECRETS}/postgres_password" ]] || die "${SECRETS}/postgres_password is missing. Run: sudo bash ${SRC}/deploy/first-boot.sh --secrets-only"

# flock, because this is a timer **and** something an operator runs by hand, and
# two dumps racing for one filename produce a truncated file that looks fine —
# which is the worst shape a backup failure can take, because it is found during
# a restore rather than during the backup.
#
# `flock(1)` is util-linux and this host has it. A host without it is a
# workstation rehearsal rather than a deployment, and it is **said so and
# skipped** rather than refused: refusing would make the script untestable off
# the host, and this file's whole design is that the same code runs in both
# places. What is given up — mutual exclusion against a concurrent run — is
# named in the output so nobody mistakes the absence for the guarantee.
if command -v flock >/dev/null 2>&1; then
  exec 9>"${STATE}/backup.lock"
  flock -n 9 || die "another backup is already running (${STATE}/backup.lock is held)"
else
  note "flock(1) is not on this machine, so this run is not mutually excluded from another."
  note "That is fine for a rehearsal and is not fine for a timer. Ubuntu has flock."
fi

install -d -m 0700 "${BACKUPS}"
PGPASS="$(cat "${SECRETS}/postgres_password")"

# ── the dump ────────────────────────────────────────────────────────────────
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
dump="${BACKUPS}/foyer-${stamp}.dump"
manifest="${BACKUPS}/foyer-${stamp}.manifest"
partial="${BACKUPS}/foyer-${stamp}.partial"

say "Dumping"
# To a `.partial`, renamed on success. A name that says "not a backup yet" is
# what stops a killed backup from leaving something `restore.sh` would pick up
# and `verify.sh` would count.
if ! pg_dump_to "${partial}"; then
  rm -f "${partial}"
  die "pg_dump failed; nothing was written to ${BACKUPS}"
fi
if [[ ! -s "${partial}" ]]; then
  rm -f "${partial}"
  die "pg_dump produced an empty file; treating that as a failure rather than a backup"
fi
mv "${partial}" "${dump}"
chmod 0600 "${dump}"

# ── the manifest ────────────────────────────────────────────────────────────
# **There is no `|| true` here, and its absence is deliberate.** If the
# fingerprint function cannot read the KEK, `|| true` would turn that into an
# empty string, and the empty string's SHA-256 is a perfectly ordinary-looking
# digest that would go into the manifest as if it were an answer: a real backup,
# a zero exit, and a fingerprint of nothing. A backup whose readability nobody can
# determine is worse than no backup, so this refuses and names the file.
if ! fingerprint="$(foyer_kek_fingerprint "${SECRETS}/kek")"; then
  die "cannot read ${SECRETS}/kek, so this dump would carry no KEK fingerprint and nobody could later tell whether it is readable. Run this as a user that can read it, or fix its mode. Refusing rather than writing a manifest with an empty value in it."
fi

# The goose version, read over the **same socket the dump was just taken
# through**, in the same second, rather than from a `foyerctl migrate --status`
# in a container. Two reasons and the second is the one that matters: the local
# socket needs no password and no image, and a dump and the version that
# describes it read over two different connections are two facts that can
# disagree — a migration landing between them produces a manifest that
# misdescribes the file beside it.
#
# `unknown`, never empty. An empty `schema:` reads as "recorded, and the schema
# is blank", which is a different and more reassuring claim than "this script
# could not find out", and `restore.sh` prints this field in exactly the message
# an operator reads when a restore has already failed.
schema="$(compose exec -T postgres \
            psql -U foyer -d foyer -tAc \
              "SELECT COALESCE(max(version_id)::text,'unknown') FROM goose_db_version WHERE is_applied" \
              2>/dev/null | tr -d '[:space:]' || true)"
[[ -n "${schema}" ]] || schema="unknown"

{
  printf 'taken: %s\n'    "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  printf 'revision: %s\n' "$(cat "${SRC}/.shipped-rev" 2>/dev/null || echo unknown)"
  printf 'schema: %s\n'   "${schema}"
  printf 'bytes: %s\n'    "$(wc -c < "${dump}" | tr -d ' ')"
  printf 'sha256: %s\n'   "$(foyer_sha256 "${dump}")"
  if [[ -n "${fingerprint}" ]]; then
    printf 'kek-sha256: %s\n' "${fingerprint}"
    printf 'kek-source: %s\n' "${SECRETS}/kek — the KEK is NOT in this dump and must be kept off this host"
  else
    # `none` rather than an empty value: an empty field here reads as
    # "fingerprinted, and the fingerprint is nothing", which is a different and
    # more reassuring claim than "this dump's key is not on this machine".
    printf 'kek-sha256: none\n'
    printf 'kek-source: the KEK is not on this host, so this dump is ciphertext nobody here can read\n'
  fi
} > "${manifest}"
chmod 0600 "${manifest}"

note "$(du -h "${dump}" | cut -f1) → $(basename "${dump}")"

# ── the sentence that has to be here ────────────────────────────────────────
#
# `upgrade.sh` printed this beside its dump and README.md prints it beside the
# KEK. It is repeated on **every scheduled run** because a scheduled run has no
# operator reading it, and this is the one fact about the file on disk that
# nothing else will ever tell them.
if [[ -n "${fingerprint}" ]]; then
  note "This dump holds every shared secret, credential and NT hash as CIPHERTEXT."
  note "It cannot be read without ${SECRETS}/kek, which is not in it."
  note "KEK sha256 ${fingerprint} — keep a copy of that file somewhere this"
  note "host's disk failure cannot reach, or this backup restores nothing usable."
else
  note "WARNING: ${SECRETS}/kek is not on this host."
  note "This dump is ciphertext and nothing on this machine can open it."
fi

# ── prune ───────────────────────────────────────────────────────────────────
#
# Newest first, then drop everything past KEEP. The newest is kept, and a KEEP
# that would not keep it is **refused before the dump is taken** — see the guard
# below. A pruning rule that can delete every backup is worse than no pruning,
# because the operator finds out at the moment they need one.
#
# `FOYER_BACKUP_KEEP` is validated here rather than clamped. Clamping is the
# worse of the two: an operator who set KEEP=0 and got 1 would not know their
# setting was ignored, and the next thing they set would be ignored too.
case "${KEEP}" in
  ''|*[!0-9]*) die "FOYER_BACKUP_KEEP=${KEEP} is not a whole number of backups" ;;
esac
if (( KEEP < 1 )); then
  die "FOYER_BACKUP_KEEP=${KEEP} would delete every backup including the one just taken.
         A backup script that can empty its own directory is a backup you find out
         about during a restore. Set FOYER_BACKUP_KEEP to at least 1. Nothing was written."
fi

say "Pruning"
kept=0
removed=0
while IFS= read -r f; do
  [[ -n "${f}" ]] || continue
  kept=$((kept + 1))
  if (( kept > KEEP )); then
    rm -f "${f}" "${f%.dump}.manifest"
    removed=$((removed + 1))
    note "removed $(basename "${f}")"
  fi
done < <(foyer_backups_newest_first)

note "kept ${kept}, removed ${removed} (KEEP=${KEEP})"

# ── what it kept ────────────────────────────────────────────────────────────
#
# The inventory, on every run. `--list` and this block are the *same function*,
# so the schedule and the operator cannot be told two different things.
say "Backups in ${BACKUPS}"
while IFS=$'\t' read -r name age fp; do
  [[ -n "${name}" ]] || continue
  printf '    %-32s %5sh  %s\n' "${name}" "${age}" "${fp}"
done < <(foyer_backup_list)
