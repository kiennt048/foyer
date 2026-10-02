#!/usr/bin/env bash
#
# Issue FreeRADIUS the client certificate it presents to the Policy API.
#
# Its own file because it has two callers and must not be two copies:
# `first-boot.sh` runs it on a fresh install, and `upgrade.sh` runs it on every
# upgrade — because an older install may have the CA and the server certificate
# and **no client certificate**, and the generated tree requires one. An upgrade that skipped this would produce a
# FreeRADIUS that does not start and a control plane that refuses to.
#
# Idempotent: an existing radius.crt is left alone. Re-running never rotates.
#
#   sudo bash deploy/issue-radius-cert.sh
#
set -euo pipefail

FOYER_ROOT="${FOYER_ROOT:-/opt/foyer}"
SECRETS="${FOYER_ROOT}/secrets"
TLS="${SECRETS}/policy-tls"

# A separate directory from ${TLS}, and that is the point rather than tidiness:
# ${TLS} holds ca.key, and the FreeRADIUS container starts as root. Mounting ${TLS}
# into it would hand the AAA data plane the key that mints client certificates
# for its own Policy API. This directory holds a copy of ca.crt and one pair.
RADIUS_TLS="${SECRETS}/radius-tls"

note() { printf '    %s\n' "$*"; }
die() { printf 'issue-radius-cert: %s\n' "$*" >&2; exit 1; }

[[ $EUID -eq 0 ]] || die "run me with sudo — I write a private key into ${SECRETS}"
command -v openssl >/dev/null 2>&1 || die "openssl is not installed."

# radiusd does not run as root: it switches to `freerad` after reading its tree
# and BEFORE rlm_rest opens this key. The image puts freerad in the group
# foyer-radius-tls at the fixed gid 1812 (deploy/Dockerfile.freeradius,
# internal/raddb.TLSReadGID), so this directory and key are granted to that gid --
# root-owned, group-readable, nothing for anybody else. Applied on EVERY run,
# before the early exit below, because an older install may have root:root
# 0700/0400 here and an upgrade that did not fix it would produce a FreeRADIUS
# that cannot read its own key.
RADIUS_TLS_GID=1812

reown() {
  chown root:"${RADIUS_TLS_GID}" "${RADIUS_TLS}"
  chmod 0750 "${RADIUS_TLS}"
  if [[ -e "${RADIUS_TLS}/radius.key" ]]; then
    chown root:"${RADIUS_TLS_GID}" "${RADIUS_TLS}/radius.key"
    chmod 0440 "${RADIUS_TLS}/radius.key"
  fi
  for f in radius.crt ca.crt; do
    if [[ -e "${RADIUS_TLS}/${f}" ]]; then
      chown root:root "${RADIUS_TLS}/${f}"
      chmod 0644 "${RADIUS_TLS}/${f}"
    fi
  done
}

install -d -o root -g "${RADIUS_TLS_GID}" -m 0750 "${RADIUS_TLS}"

if [[ -s "${RADIUS_TLS}/radius.crt" ]]; then
  reown
  note "radius.crt already exists, leaving it alone (ownership: root:${RADIUS_TLS_GID})"
  exit 0
fi

[[ -s "${TLS}/ca.key" ]] || die "${TLS}/ca.key is missing, so nothing can issue FreeRADIUS a certificate. Run deploy/first-boot.sh --secrets-only first."

( umask 077
  openssl req -newkey rsa:2048 -sha256 -nodes \
    -keyout "${RADIUS_TLS}/radius.key" -out "${RADIUS_TLS}/radius.csr" \
    -subj "/CN=freeradius" >/dev/null 2>&1

  # extendedKeyUsage=clientAuth, and Go's crypto/tls enforces it: a certificate
  # issued for serverAuth is refused by RequireAndVerifyClientCert. Issuing one
  # certificate for both ends of this boundary would work until somebody looked.
  openssl x509 -req -in "${RADIUS_TLS}/radius.csr" -sha256 -days 825 \
    -CA "${TLS}/ca.crt" -CAkey "${TLS}/ca.key" -CAcreateserial \
    -out "${RADIUS_TLS}/radius.crt" \
    -extfile <(printf 'extendedKeyUsage=clientAuth\n') >/dev/null 2>&1
  rm -f "${RADIUS_TLS}/radius.csr"
  cp "${TLS}/ca.crt" "${RADIUS_TLS}/ca.crt" )

reown
note "issued a client certificate for FreeRADIUS from this install's CA"
note "825 days. There is no renewal mechanism yet — that is a gap, not a decision,"
note "and rlm_rest reports an expired one as \"Failed sending data to the peer\"."
