#!/usr/bin/env bash
#
# Resolve which images to run, and whether they have to be built here.
#
# Sourced, never executed. `${SRC}` must already be set.
#
# ── why this file exists ─────────────────────────────────────────────────────
#
# `first-boot.sh`, `up.sh` and `upgrade.sh` all need the same rule, and a script
# that carries only half of it fails with
#
#     required variable FOYER_IMAGE_FOYER is missing a value
#
# `images.conf` carries *empty* digests until somebody publishes to a registry, so
# sourcing it alone sets the variables to the empty string and `compose.yaml`'s
# `${FOYER_IMAGE_*:?}` guards fire — correctly, and about the wrong thing. The
# other half is the fallback: no digest means build here, under a name, and say so.
#
# A rule restated in several places drifts; one file, every caller reads it.
#
# ── what it decides ──────────────────────────────────────────────────────────
#
#   FOYER_IMAGE_FOYER / _FREERADIUS / _POSTGRES  what compose runs
#   FOYER_BUILD_LOCALLY=1                        no digest: build here
#   FOYER_COMPOSE_FILES                          compose.yaml, plus the build
#                                                overlay when building
#
# **Build and pull are never both**, and the difference is printed rather than
# inferred from whether a pull happened to succeed.

if [[ -z "${FOYER_IMAGE_FOYER:-}" ]]; then
  # shellcheck source=/dev/null
  . "${SRC}/deploy/images.conf"
  if [[ -z "${FOYER_IMAGE_FOYER}" ]]; then
    FOYER_IMAGE_FOYER="foyer:${FOYER_VERSION}"
    FOYER_IMAGE_FREERADIUS="foyer-freeradius:${FOYER_VERSION}"
    FOYER_BUILD_LOCALLY=1
  fi
fi
export FOYER_IMAGE_FOYER FOYER_IMAGE_FREERADIUS FOYER_IMAGE_POSTGRES

FOYER_COMPOSE_FILES=(-f "${SRC}/deploy/compose.yaml")
if [[ "${FOYER_BUILD_LOCALLY:-0}" == "1" ]]; then
  FOYER_COMPOSE_FILES+=(-f "${SRC}/deploy/compose.build.yaml")
  printf '    building images locally — this host will run something no one else can verify\n' >&2
else
  printf '    pulling published images, pinned by digest\n' >&2
fi
