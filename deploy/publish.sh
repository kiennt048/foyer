#!/usr/bin/env bash
#
# Build the two Foyer images and push them, then write the DIGESTS back into
# deploy/images.conf.
#
#   docker login
#   deploy/publish.sh                 # publishes FOYER_VERSION from images.conf
#   deploy/publish.sh 0.2.0           # publishes that version instead
#
# The maintainer's script, not an operator's. An operator runs deploy/install.sh
# and never builds anything.
#
# WHY IT REWRITES images.conf RATHER THAN LEAVING YOU TO: the digest is the whole
# point of the file, and a digest a human copies is a digest a human can mistype.
# `docker buildx imagetools inspect` is asked for it after the push, so the file
# records what the registry actually holds rather than what this script intended
# to put there.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${here}"

say()  { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die()  { printf '\033[31mpublish: %s\033[0m\n' "$*" >&2; exit 1; }

# shellcheck source=/dev/null
. deploy/images.conf
VERSION="${1:-${FOYER_VERSION}}"
[[ -n "${FOYER_IMAGE_REPO}" ]] || die "deploy/images.conf has no FOYER_IMAGE_REPO"

# A dirty tree produces an image whose revision label is a lie.
if [[ -n "$(git status --porcelain)" ]]; then
  die "the working tree is dirty.
    Every image carries org.opencontainers.image.revision, verify.sh treats
    revision=unknown as drift, and an image labelled with a commit that does not
    describe its contents is worse than one labelled unknown. Commit first."
fi
REV="$(git rev-parse --short HEAD)"

say "Publishing ${VERSION} (${REV}) to ${FOYER_IMAGE_REPO}"
docker buildx version >/dev/null 2>&1 || die "docker buildx is required to push a multi-arch manifest"

# amd64 only, and it is not an oversight.
#
# deploy/Dockerfile.freeradius installs freeradius 3.2.10-1 from
# packages.networkradius.com, whose index declares `all amd64` and answers 404 for
# arm64. A multi-arch manifest would therefore have to either
# drop that pin on arm64 — shipping a different FreeRADIUS than the one the dialect
# contract was tested against — or fail half-built. It ships one architecture and
# says so.
PLATFORM="linux/amd64"

for img in foyer foyer-freeradius; do
  case "${img}" in
    foyer)            dockerfile=deploy/Dockerfile.foyer ;;
    foyer-freeradius) dockerfile=deploy/Dockerfile.freeradius ;;
  esac
  ref="${FOYER_IMAGE_REPO}/${img}:${VERSION}"
  say "${img}"
  docker buildx build \
    --platform "${PLATFORM}" \
    --build-arg "REVISION=${REV}" \
    -f "${dockerfile}" \
    -t "${ref}" \
    --push \
    .
  digest="$(docker buildx imagetools inspect "${ref}" --format '{{json .Manifest.Digest}}' | tr -d '"')"
  [[ -n "${digest}" ]] || die "${ref} was pushed and the registry reported no digest for it"
  note "${ref}@${digest}"

  case "${img}" in
    foyer)            key=FOYER_IMAGE_FOYER ;;
    foyer-freeradius) key=FOYER_IMAGE_FREERADIUS ;;
  esac
  # Rewrite in place. The whole line, so a stale digest cannot survive beside a new one.
  tmp="$(mktemp)"
  awk -v k="${key}" -v v="${ref}@${digest}" \
    '$0 ~ "^"k"=" {print k"="v; next} {print}' deploy/images.conf > "${tmp}"
  mv "${tmp}" deploy/images.conf
done

# The version line too, when it was overridden on the command line.
if [[ "${VERSION}" != "${FOYER_VERSION}" ]]; then
  tmp="$(mktemp)"
  awk -v v="${VERSION}" '/^FOYER_VERSION=/ {print "FOYER_VERSION="v; next} {print}' deploy/images.conf > "${tmp}"
  mv "${tmp}" deploy/images.conf
fi

say "Done"
note "deploy/images.conf now pins what the registry holds. Commit it —"
note "it is the only record of which bytes a deployment will run."
git --no-pager diff --stat deploy/images.conf
