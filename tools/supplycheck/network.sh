#!/usr/bin/env bash
#
# The networked half of the supply-chain check: the BYTES, not the tag.
#
# tools/supplycheck's tests run offline inside `go test ./...` and read the tree:
# they can say a base is `golang:1.26.8-alpine3.24` and that 1.26.8 is above the
# patched floor. They cannot say the digest pinned beside that tag is that image. A
# digest swapped for an older golang under an unchanged tag passes every one of them,
# because Docker ignores the tag when a digest is present and so does the offline
# check. Only the registry and the binary know.
#
#   bash tools/supplycheck/network.sh            # check: `make supply-verify`
#   bash tools/supplycheck/network.sh --images   # the digest half only
#   bash tools/supplycheck/network.sh --update   # rewrite moved pins: `make supply-pin`
#
# ── 1. digest → tag ─────────────────────────────────────────────────────────
#
# Every `name:tag@sha256:…` in deploy/'s Dockerfiles and deploy/images.conf is
# resolved with `docker buildx imagetools inspect name:tag`, and the index digest the
# registry names for the tag TODAY must be the pinned one, with a linux/amd64 image
# in it (deploy/ targets an amd64/x86_64 host). Anything else fails:
#
#   * the pin was edited and names some other image than its tag says — the attack;
#   * or the tag moved, which is what an upstream rebuild with fixes looks like.
#
# The second is a failure too, on purpose, and it is Renovate's `pinDigests` model
# with the bot replaced by a make target: a pin behind its tag is a base missing the
# fixes the tag now carries, and the remedy is `make supply-pin`, a reviewed diff and
# a commit. It goes red with no change to the tree, exactly as `make vulncheck` does
# the day an advisory lands, for the same reason.
#
# ── 2. govulncheck on the built binary ──────────────────────────────────────
#
# `make vulncheck` scans the SOURCE against the local toolchain. What ships is the
# binary deploy/Dockerfile.foyer compiles with the pinned golang image, and that
# image's Go is written into the binary's build info. So the final stage is exported
# (`--output type=local`, no image is loaded or pushed) and govulncheck reads the two
# binaries in `-mode=binary`: an older builder is an older standard library, and
# this is the check that sees it.
#
# Docker Hub rate-limits anonymous requests. A 429 is retried with backoff; one that
# persists is a failure that says so, never a pass.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
GOVULNCHECK_VERSION="${GOVULNCHECK_VERSION:-v1.8.0}"
mode=check
case "${1:-}" in
  "") ;;
  --images) mode=images ;;
  --update) mode=update ;;
  *) echo "usage: $0 [--images|--update]" >&2; exit 2 ;;
esac

fail=0
ok()   { printf '  ok    %s\n' "$*"; }
bad()  { printf '  FAIL  %s\n' "$*"; fail=1; }

# pinned_refs prints "<file> <name:tag@sha256:digest>" for every digest pin.
pinned_refs() {
  local f ref
  for f in "${ROOT}"/deploy/Dockerfile*; do
    while read -r ref; do
      printf '%s %s\n' "${f#"${ROOT}/"}" "${ref}"
    done < <(sed -nE 's/^[[:space:]]*FROM[[:space:]]+(--platform=[^[:space:]]+[[:space:]]+)?([^[:space:]]+@sha256:[0-9a-f]{64}).*/\2/p' "${f}")
  done
  while read -r ref; do
    printf 'deploy/images.conf %s\n' "${ref}"
  done < <(sed -nE 's/^FOYER_IMAGE_[A-Z_]+=([^[:space:]]+@sha256:[0-9a-f]{64})[[:space:]]*$/\1/p' "${ROOT}/deploy/images.conf")
}

# index_of <ref> prints the registry's image index (JSON) for a tag or digest.
index_of() {
  local ref="$1" out attempt
  for attempt in 1 2 3 4; do
    if out="$(docker buildx imagetools inspect "${ref}" --format '{{json .Manifest}}' 2>&1)"; then
      printf '%s\n' "${out}"
      return 0
    fi
    case "${out}" in
      *"429"*|*"Too Many Requests"*|*"toomanyrequests"*)
        sleep $((attempt * 15)) ;;
      *) printf '%s\n' "${out}" >&2; return 1 ;;
    esac
  done
  printf 'the registry kept answering 429 Too Many Requests for %s\n' "${ref}" >&2
  return 1
}

# The top-level "digest" of the index, which is the one a pin names.
index_digest() { sed -nE 's/^  "digest": "(sha256:[0-9a-f]{64})".*/\1/p' | head -1; }
has_amd64() { tr -d ' \n' | grep -q '"platform":{"architecture":"amd64","os":"linux"'; }

check_images() {
  local file ref tag pin index now
  echo "digest → tag"
  while read -r file ref; do
    tag="${ref%@*}"
    pin="${ref#*@}"
    if ! index="$(index_of "${tag}")"; then
      bad "${file}: ${tag} could not be resolved at the registry"
      continue
    fi
    now="$(index_digest <<< "${index}")"
    if [[ "${now}" == "${pin}" ]]; then
      if has_amd64 <<< "${index}"; then
        ok "${file}: ${tag} is ${pin:0:19}…, with a linux/amd64 image"
      else
        bad "${file}: ${tag}@${pin:0:19}… has no linux/amd64 image, and deploy/ targets an amd64/x86_64 host"
      fi
    elif [[ "${mode}" == update ]]; then
      if ! has_amd64 <<< "${index}"; then
        bad "${file}: ${tag} now names ${now:0:19}…, which has no linux/amd64 image; not pinned"
        continue
      fi
      sed -i.bak "s|${tag}@${pin}|${tag}@${now}|g" "${ROOT}/${file}" && rm -f "${ROOT}/${file}.bak"
      ok "${file}: ${tag} re-pinned ${pin:0:19}… → ${now:0:19}…"
    else
      bad "${file}: ${tag} names ${now:0:19}… at the registry, and the pin is ${pin:0:19}…
        Either the tag moved (an upstream rebuild, usually with fixes): run \`make supply-pin\`,
        review the diff, commit it. Or the pin was edited and does not name the image its tag says."
    fi
  done < <(pinned_refs)
}

check_binaries() {
  local out
  echo "govulncheck -mode=binary on what deploy/Dockerfile.foyer builds"
  out="$(mktemp -d)"
  trap 'rm -rf "${out}"' RETURN
  if ! docker buildx build -f "${ROOT}/deploy/Dockerfile.foyer" \
      --build-arg REVISION="$(git -C "${ROOT}" rev-parse --short HEAD 2>/dev/null || echo unknown)" \
      --output "type=local,dest=${out}/rootfs" "${ROOT}" >"${out}/build.log" 2>&1; then
    tail -20 "${out}/build.log" >&2
    bad "deploy/Dockerfile.foyer did not build, so the shipped binary could not be scanned"
    return
  fi
  local b
  for b in foyer foyerctl; do
    if go run "golang.org/x/vuln/cmd/govulncheck@${GOVULNCHECK_VERSION}" -mode=binary "${out}/rootfs/usr/local/bin/${b}"; then
      ok "usr/local/bin/${b}: no known vulnerability in the binary the image ships"
    else
      bad "usr/local/bin/${b}: govulncheck -mode=binary found a vulnerability (above), in the bytes deploy/Dockerfile.foyer ships"
    fi
  done
}

check_images
[[ "${mode}" == check ]] && check_binaries
if [[ ${fail} -ne 0 ]]; then
  echo "supply-verify: FAILED"
  exit 1
fi
echo "supply-verify: ok"
