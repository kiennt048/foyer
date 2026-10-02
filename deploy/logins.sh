# shellcheck shell=bash
#
# The control plane's database login, switched only once the database accepts it.
#
# Sourced by first-boot.sh, which writes the connection strings, and by the three
# scripts that start the stack — first-boot.sh itself, up.sh and upgrade.sh — which
# promote a staged one. Not executable on its own.
#
# ── the window this closes ───────────────────────────────────────────────────
#
# `database_url` is the one secret file a RUNNING control plane reads: compose
# bind-mounts it, and `restart: unless-stopped` re-reads it on every restart. If
# `first-boot.sh --secrets-only` rewrote it in place, then on an install from
# before migration 00019 it would turn `postgres://foyer:…` (the superuser) into
# `postgres://foyer_app:…` — a role that has no LOGIN and no password until
# `foyerctl migrate` runs, which upgrade.sh does three steps later, after the build.
# A control plane that restarted in between (an OOM kill, a host reboot, a crash)
# would come back on a login the database refuses, and stay in a restart loop until
# the upgrade finished — or for good, if the build failed and the upgrade stopped
# there. The same happens on a rotation: a new password in `database_url` before
# the migrate that sets it.
#
# The field's answer is AWS Secrets Manager's four-step rotation: the new value is
# created as AWSPENDING (createSecret), set in the database (setSecret), tested, and
# only then labelled AWSCURRENT (finishSecret) — clients never read a credential the
# database does not yet accept. Vault's database engine creates the role before it
# hands out the lease, for the same reason. So here: first-boot.sh
# STAGES a changed `database_url` as `database_url.next`, the running process keeps
# the file it was started with, and whoever starts the new containers promotes the
# staged file — upgrade.sh after migrate has granted the login, up.sh and first-boot
# immediately before `docker compose up`, which runs migrate before it recreates
# the control plane (`depends_on: service_completed_successfully`).
#
# A fresh install has no `database_url`, so nothing is staged and the file is simply
# written. An unchanged one is left alone.

# foyer_write_secret_file <path> <owner uid:gid> <content>
#
# Writes <content> to <path>, 0400 and owned by <owner>, atomically: a temporary
# file in the same directory, renamed over the old one. A half-written connection
# string is a control plane that fails to parse its own login.
foyer_write_secret_file() {
  local path="$1" owner="$2" content="$3" tmp
  tmp="$(umask 077 && mktemp "${path}.XXXXXX")"
  printf '%s\n' "${content}" > "${tmp}"
  chmod 0400 "${tmp}"
  chown "${owner}" "${tmp}"
  mv -f "${tmp}" "${path}"
}

# foyer_stage_login_url <path> <owner uid:gid> <content>
#
# Prints what it did as one word — `written`, `unchanged` or `staged` — so the
# caller can say so in its own voice.
foyer_stage_login_url() {
  local path="$1" owner="$2" content="$3"
  if [[ ! -e "${path}" ]]; then
    foyer_write_secret_file "${path}" "${owner}" "${content}"
    rm -f "${path}.next"
    echo written
  elif [[ "$(cat "${path}")" == "${content}" ]]; then
    # Correct the mode and owner on every run, for first-boot.sh's `keep` reason.
    chmod 0400 "${path}"
    chown "${owner}" "${path}"
    rm -f "${path}.next"
    echo unchanged
  else
    foyer_write_secret_file "${path}.next" "${owner}" "${content}"
    echo staged
  fi
}

# foyer_promote_logins <secrets dir>
#
# Renames every staged login into place. A rename within one directory is atomic,
# and the staged file already has its final mode and owner, so there is no moment
# at which the file a container is about to read is half one thing.
foyer_promote_logins() {
  local dir="$1"
  if [[ -e "${dir}/database_url.next" ]]; then
    mv -f "${dir}/database_url.next" "${dir}/database_url"
    printf '    promoted database_url: the control plane logs in with it from its next start\n'
  else
    printf '    no staged database login to promote\n'
  fi
}
