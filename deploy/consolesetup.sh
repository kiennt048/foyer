#!/usr/bin/env bash
#
# The console's first-access setup: the one line of installer output an operator
# needs. Sourced, never executed. Defines open_console_setup.
#
# ── why a code and not a password ────────────────────────────────────────────
#
# Foyer ships no credential (`docs/rules/security.md` §7), so a fresh install has
# no operator at all. This opens a one-time setup with a random code, and the
# console's own screen asks for that code, a username and a password of the
# operator's choosing — once. `internal/api/handlers_setup.go` has the survey
# (authentik, UniFi, Omada, Grafana, Jenkins, GitLab, Keycloak, OWASP) and why a
# code and not a first-come page: the console is published on every interface.
#
# ── why it is its own file ───────────────────────────────────────────────────
#
# `first-boot.sh` runs it on a fresh install and `upgrade.sh` on every upgrade:
# migration 00040 deletes an operator still holding a formerly shipped default
# password, so an upgraded install that never changed it has no administrator
# afterwards and needs the same code a fresh one does. A rule in only one of the
# two is a rule the other path lacks (`secretdirs.sh`, `images.sh`,
# `consolenames.sh` — same shape).
#
# ── idempotent, and the recovery for a lost code ─────────────────────────────
#
# `foyerctl open-setup` opens nothing when an enabled administrator exists, and
# prints nothing. While the setup is open, every run prints a NEW code and retires
# the last, so an operator who scrolled past it re-runs the installer — the one
# command they already know — rather than learning another. The code is never
# written to disk on either side; the database holds its SHA-256.

# open_console_setup <console-url> <docker compose argument...>
open_console_setup() {
  local url="$1"
  shift
  local code
  if ! code="$(docker compose "$@" exec -T foyer foyerctl open-setup)"; then
    printf 'console setup: foyerctl open-setup failed; the stack is up but the console cannot be set up\n' >&2
    return 1
  fi
  if [[ -z "${code}" ]]; then
    printf '    An administrator exists; the console needs no setup.\n'
    return 0
  fi
  printf '\n    ────────────────────────────────────────────────────────────────────────\n'
  printf '    Console setup code:  %s\n' "${code}"
  printf '\n    Open %s and enter this code to create the first administrator —\n' "${url}"
  printf '    a username and a password of your own. It works ONCE, and until it is\n'
  printf '    used, it is the only way in: Foyer ships no password.\n'
  printf '\n    Lost it? Run this installer again; it prints a new code and the old one\n'
  printf '    stops working.\n'
  printf '    ────────────────────────────────────────────────────────────────────────\n\n'
}
