#!/usr/bin/env bash
#
# `docker compose` for this deployment, with the image pins already loaded.
#
#   deploy/compose.sh ps
#   deploy/compose.sh exec -T foyer foyerctl sync-nas
#   deploy/compose.sh logs foyer
#
# WHY THIS EXISTS: compose.yaml names its images through ${FOYER_IMAGE_*}, which
# come from deploy/images.conf. A bare `docker compose -f deploy/compose.yaml …`
# therefore fails with "required variable FOYER_IMAGE_FOYER is missing a value",
# including for the follow-up commands first-boot.sh prints.
#
# The obvious alternative is a `.env` beside compose.yaml, which compose reads
# automatically. It is refused on purpose: verify.sh treats ANY *.env inside the
# shipped tree as drift, because that is how a secret ends up committed, and an
# exception carved into that rule is worth less than this six-line script.
set -euo pipefail

SRC="${FOYER_ROOT:-/opt/foyer}/src"
[[ -f "${SRC}/deploy/compose.yaml" ]] || SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# shellcheck source=/dev/null
. "${SRC}/deploy/images.conf"
if [[ -z "${FOYER_IMAGE_FOYER}" ]]; then
  FOYER_IMAGE_FOYER="foyer:${FOYER_VERSION}"
  FOYER_IMAGE_FREERADIUS="foyer-freeradius:${FOYER_VERSION}"
fi
export FOYER_IMAGE_FOYER FOYER_IMAGE_FREERADIUS FOYER_IMAGE_POSTGRES
FOYER_REV="$(cat "${SRC}/.shipped-rev" 2>/dev/null || echo unknown)"
export FOYER_REV

# The console's certificate names, for the same reason the image pins are here and
# found the same way: compose.yaml requires FOYER_ADMIN_TLS_NAMES, so a bare
# `docker compose` fails without it. **The symptom is worse than the image one**
# — `verify.sh` calls this script with stderr discarded, so an unset variable does
# not print "required variable … is missing a value", it prints nothing and yields
# an empty container id, and verify.sh reports `the foyer service is not running`
# about containers that are up.
# shellcheck source=/dev/null
. "${SRC}/deploy/consolenames.sh"

exec docker compose -f "${SRC}/deploy/compose.yaml" "$@"
