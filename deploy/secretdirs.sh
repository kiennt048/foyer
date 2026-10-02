#!/usr/bin/env bash
#
# The directories the control plane must be able to traverse and write.
#
# Sourced, never executed. `${SRC}` and `${FOYER_ROOT}` must already be set.
#
# ── why this file exists ─────────────────────────────────────────────────────
#
# If the rule lived in `first-boot.sh` alone, the upgrade path — which runs
# `up.sh`, not first-boot — would never apply it: compose would be handed a bind
# mount whose source does not exist, Docker would create it as `root:root 0755`,
# and the control plane would crash-loop on
#
#     tlsca: writing /run/foyer/admin-tls/admin.crt: permission denied
#
# taking the guest portal down with it, because both listeners are one process.
#
# `deploy/images.sh` exists for exactly this shape: a rule in one script and half
# of it in another. **A deployment made of cooperating scripts needs a single home
# for each rule.**
#
# ── the invariant ────────────────────────────────────────────────────────────
#
# A directory the container cannot traverse stops it reading files it owns; a
# directory it cannot write stops it creating the ones it is expected to make. Both
# failures surface as something else — a TLS error, a missing secret — which is why
# this runs on every start rather than once.

# The runtime uid, read out of the image that declares it. Guessing produces a
# stack that starts and cannot read its own secrets.
FOYER_UID="$(sed -n 's/.*adduser .*-u \([0-9][0-9]*\) .*/\1/p' "${SRC}/deploy/Dockerfile.foyer" | head -1)"
[[ -n "${FOYER_UID}" ]] || { printf 'secretdirs: could not read the runtime uid out of %s/deploy/Dockerfile.foyer\n' "${SRC}" >&2; exit 1; }

# Idempotent, and deliberately unconditional: `install -d` on an existing directory
# still applies the ownership and mode, which is the half `first-boot.sh`'s
# `keep()` exists to remember — generate conditionally, enforce every time.
# `portal-tls` is the operator's optional portal pair, mounted read-only; empty is
# the default plain-HTTP portal, and a root-owned source would be a pair the
# container cannot read the day one is put there.
#
# `raddb` is the FreeRADIUS handoff directory: the control plane
# writes each generated tree into it and FreeRADIUS's supervisor, running as root
# in the other container, reads it and answers in `status`. 0700 for the control
# plane's uid, because a generated tree carries FreeRADIUS's database password and,
# with EAP-TLS on, the EAP private key. A root-owned source here is a console whose
# Apply cannot write, which is exactly the failure this file exists to prevent.
for d in "${FOYER_ROOT}/secrets/policy-tls" "${FOYER_ROOT}/secrets/admin-tls" "${FOYER_ROOT}/secrets/portal-tls" \
         "${FOYER_ROOT}/secrets/raddb"; do
  install -d -o "${FOYER_UID}" -g "${FOYER_UID}" -m 0700 "${d}"
done
