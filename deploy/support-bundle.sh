#!/usr/bin/env bash
#
# Collect this host's diagnostics into one redacted archive.
#
# This is the "send us your logs" button, and the reason it is a script rather
# than an instruction is `docs/rules/security.md` §7: **`radiusd -X` output
# contains every RADIUS shared secret in cleartext**, the generated
# mods-available/sql carries the database password, and an operator who is asked
# to "send the FreeRADIUS log" will send exactly that.
#
# Nothing here does the redacting. `foyerctl support-bundle` does, and every byte
# goes through it — this script's whole job is to gather host-side output that a
# container cannot see (docker's own logs, the compose configuration, what is
# listening) and hand it over.
#
#   deploy/support-bundle.sh [--out /path/foyer-support.tar.gz]
#
set -euo pipefail

FOYER_ROOT="${FOYER_ROOT:-/opt/foyer}"
SRC="${FOYER_ROOT}/src"
COMPOSE="${SRC}/deploy/compose.yaml"
OUT="${FOYER_ROOT}/state/foyer-support-$(date -u +%Y%m%dT%H%M%SZ).tar.gz"
if [[ "${1:-}" == "--out" ]]; then
  OUT="${2:?--out needs a path}"
fi

die() { printf 'support-bundle: %s\n' "$*" >&2; exit 1; }
[[ -f "${COMPOSE}" ]] || die "${COMPOSE} is not there"

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT
chmod 0700 "${work}"

echo "==> gathering"

# Host facts that no container can see.
{
  echo "# uname"; uname -a
  echo; echo "# os-release"; cat /etc/os-release 2>/dev/null
  echo; echo "# memory"; free -m 2>/dev/null
  echo; echo "# disk"; df -h / 2>/dev/null
  echo; echo "# uptime"; uptime
  echo; echo "# docker"; docker --version; docker compose version
  echo; echo "# shipped revision"; cat "${SRC}/.shipped-rev" 2>/dev/null || echo "(none)"
} > "${work}/host.txt" 2>&1 || true

# What is listening, which is how a "the portal does not open" report gets
# answered without a second round trip.
ss -lntup > "${work}/listeners.txt" 2>&1 || true

# The compose configuration AS RESOLVED, not as written: it is the interpolated
# one that says what is actually running. It carries FOYER_DATABASE_URL, so it is
# a prime example of why nothing goes into the archive unredacted.
docker compose -f "${COMPOSE}" config > "${work}/compose-resolved.yaml" 2>&1 || true
docker compose -f "${COMPOSE}" ps --all > "${work}/compose-ps.txt" 2>&1 || true

# Container logs. `radiusd -f -l stdout` means FreeRADIUS's output is here, and
# on a server started with -X that output is full of shared secrets.
for svc in postgres migrate foyer freeradius; do
  docker compose -f "${COMPOSE}" logs --no-color --tail 2000 "${svc}" \
    > "${work}/logs-${svc}.txt" 2>&1 || true
done

# The generated FreeRADIUS tree, which is the first thing to look at when RADIUS
# answers wrongly, and which holds the database password in mods-available/sql.
# Read from the running server — /etc/raddb is the colour its supervisor last
# started — rather than regenerated, because what is running is the question.
docker compose -f "${COMPOSE}" exec -T freeradius \
  tar -c -C /etc/raddb/ . 2>/dev/null | tar -x -C "${work}" --one-top-level=raddb 2>/dev/null || \
  echo "(the generated raddb tree could not be read)" > "${work}/raddb-missing.txt"

echo "==> redacting"

# foyerctl runs inside the image, so the work directory is mounted in. The
# environment of THAT container is not the deployment's — `--no-env` says so
# rather than shipping a misleading one — and the values that must be scrubbed
# from the host-side output are passed as literals instead.
#
# Reading them here is the one place this script touches a secret value, and it
# hands them to the redactor as things to remove rather than to record.
lits=()
if [[ -s "${FOYER_ROOT}/secrets/foyer.env" ]]; then
  while IFS='=' read -r _ value; do
    [[ -n "${value}" ]] && lits+=(--redact-value "${value}")
  done < <(grep -E '^[A-Z_]+=' "${FOYER_ROOT}/secrets/foyer.env")
fi
# Every database password, not only the superuser's. The two login passwords
# (migration 00019) are the ones that actually cross the wire: FreeRADIUS's is in cleartext in
# the generated mods-available/sql, and the application's is inside database_url.
# `-r` and not only `-s`: the two login files are owned by the container's uid, so
# run without sudo they exist and cannot be read, and `$(cat …)` would hand the
# redactor an empty literal instead of saying nothing.
# The maintenance roles' (migration 00021) are inside reaper_ and retention_database_url.
for f in postgres_password app_db_password radius_db_password reaper_db_password retention_db_password maintenance_token; do
  if [[ -s "${FOYER_ROOT}/secrets/${f}" && -r "${FOYER_ROOT}/secrets/${f}" ]]; then
    lits+=(--redact-value "$(cat "${FOYER_ROOT}/secrets/${f}")")
  fi
done

docker compose -f "${COMPOSE}" run --rm --no-deps \
  -v "${work}:/collect:ro" -v "$(dirname "${OUT}")":/out \
  --entrypoint foyerctl foyer \
  support-bundle --no-env --force \
  --out "/out/$(basename "${OUT}")" \
  --add "host=/collect" \
  ${lits[@]+"${lits[@]}"}

echo
echo "    ${OUT}"
echo
echo "    Read manifest.json before sending it. It reports how many redactions each"
echo "    rule made, and a rule that fired zero times against a source you expected"
echo "    it to catch is the interesting number."
