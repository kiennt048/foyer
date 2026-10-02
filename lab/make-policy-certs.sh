#!/usr/bin/env bash
#
# The lab's own CA, Policy API server certificate, and the client certificate
# FreeRADIUS presents to it.
#
# ── why the lab generates a pair rather than lifting the requirement ─────────
#
# `lab/docker-compose.yml` sets FOYER_LAB_INSECURE, which lifts two startup
# refusals: a known-default database credential, and a plaintext Policy API on a
# non-loopback address. Making mTLS a third thing it lifts would be the cheap
# answer and it is refused, because of what the lab is *for*:
#
#   * `rlm_rest` is the boundary every authorization decision crosses, and
#     docs/rules/testing.md says the lab's job is to prevent regressions in
#     what hardware has already taught us. A lab that runs the boundary in
#     plaintext prevents regressions in a configuration no deployment uses.
#   * The item names and their placement inside `rlm_rest` are not guessable —
#     they were measured, and a wrong one (`verify_peer`) is discarded without a
#     warning while every test stays green. The only thing that can
#     tell us the generator still emits a tree FreeRADIUS understands is a real
#     FreeRADIUS reading it, and that is `make lab-up`.
#   * The lab is also where the *refusal* direction can be watched: stop
#     presenting a certificate and every Access-Request must fail. That is not an
#     assertion anybody can make about a plaintext lab.
#
# The price is this file, and a `make lab-up` that generates six files the first
# time. That is a smaller price than a boundary nothing exercises.
#
# NOTHING HERE IS A SECRET WORTH KEEPING and nothing here may ever be committed:
# `.gitignore` covers `/lab/.state/`, and `*.key` besides. A deployment's
# equivalents come from `deploy/first-boot.sh`, which generates a per-install CA
# on the machine — §7 forbids shipping a CA in an image, and that applies to a
# repository at least as much.
#
#   bash lab/make-policy-certs.sh              # generate if absent
#   bash lab/make-policy-certs.sh --force      # re-issue everything
#
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${FOYER_LAB_TLS_DIR:-${HERE}/.state/policy-tls}"

# The name FreeRADIUS dials. `check_cert_cn = yes` in the generated rest module
# means this has to be in the server certificate's subjectAltName or every call
# fails — which is the point: a certificate issued for the wrong name is refused
# here rather than accepted quietly.
POLICY_HOST="${FOYER_LAB_POLICY_HOST:-foyer-backend}"

force=0
[[ "${1:-}" == "--force" ]] && force=1

command -v openssl >/dev/null 2>&1 || { echo "make-policy-certs: openssl is not installed" >&2; exit 1; }

mkdir -p "${OUT}"

if [[ ${force} -eq 0 && -s "${OUT}/radius.crt" && -s "${OUT}/policy.crt" && -s "${OUT}/ca.crt" ]]; then
  echo "lab TLS material already in ${OUT} (--force to re-issue)"
  exit 0
fi

rm -f "${OUT}"/*.crt "${OUT}"/*.key "${OUT}"/*.csr "${OUT}"/*.srl

umask 077

# ── the lab's CA ─────────────────────────────────────────────────────────────
openssl req -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
  -keyout "${OUT}/ca.key" -out "${OUT}/ca.crt" \
  -subj "/CN=Foyer lab policy CA" \
  -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
  -addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1

# ── the Policy API's server certificate ──────────────────────────────────────
openssl req -newkey rsa:2048 -sha256 -nodes \
  -keyout "${OUT}/policy.key" -out "${OUT}/policy.csr" \
  -subj "/CN=${POLICY_HOST}" >/dev/null 2>&1
openssl x509 -req -in "${OUT}/policy.csr" -sha256 -days 825 \
  -CA "${OUT}/ca.crt" -CAkey "${OUT}/ca.key" -CAcreateserial \
  -out "${OUT}/policy.crt" \
  -extfile <(printf 'subjectAltName=DNS:%s,DNS:foyer-lab-backend,DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' "${POLICY_HOST}") \
  >/dev/null 2>&1

# ── the certificate FreeRADIUS presents ──────────────────────────────────────
#
# extendedKeyUsage=clientAuth, and Go's crypto/tls enforces it: a certificate
# issued for serverAuth is refused by RequireAndVerifyClientCert. Issuing one
# certificate for both ends would have hidden that, and hidden the day somebody
# pointed the Policy API's own certificate at the rest module.
openssl req -newkey rsa:2048 -sha256 -nodes \
  -keyout "${OUT}/radius.key" -out "${OUT}/radius.csr" \
  -subj "/CN=freeradius" >/dev/null 2>&1
openssl x509 -req -in "${OUT}/radius.csr" -sha256 -days 825 \
  -CA "${OUT}/ca.crt" -CAkey "${OUT}/ca.key" -CAcreateserial \
  -out "${OUT}/radius.crt" \
  -extfile <(printf 'extendedKeyUsage=clientAuth\n') \
  >/dev/null 2>&1

rm -f "${OUT}"/*.csr
chmod 0644 "${OUT}"/*.crt
chmod 0600 "${OUT}"/*.key

echo "lab TLS material in ${OUT}:"
echo "  ca.crt      the lab CA, trusted by both ends"
echo "  policy.crt  the Policy API's server certificate (SAN ${POLICY_HOST})"
echo "  radius.crt  what FreeRADIUS presents to it"
