#!/usr/bin/env bash
#
# First boot: generate every secret this deployment needs, then start it.
#
# `docs/rules/security.md` §7 requires first-boot generation of the database
# password, the signing keys and the RADIUS secrets, and **no static credential
# anywhere in the repository**. `docs/rules/deployment.md` requires every change to
# the host to ship as code. Those two rules meet here,
# and this file is the shape of the answer: **the mechanism is committed, the
# values are not.** Nothing this script generates exists in any repository, and
# nothing it generates is printed except the one value a human must type.
#
# Idempotent. A secret that already exists is left alone — re-running this never
# rotates a key, because rotating FOYER_KEK without re-sealing the rows under it
# turns every enveloped column into ciphertext nobody can open.
#
#   sudo bash deploy/first-boot.sh                        # generate, then start
#   sudo bash deploy/first-boot.sh --secrets-only         # generate and stop
#   sudo bash deploy/first-boot.sh --promote-logins       # switch a staged login (logins.sh)
#   sudo FOYER_PORTAL_URL=http://wifi.example/en/ bash deploy/first-boot.sh
#
#   # The optional HTTPS portal is set in the admin console (Network → Portal
#   # certificate): upload the pair and the https:// address and it is served
#   # from the next handshake, with no re-run and no restart. A bootstrap
#   # fallback also works — a pair in /opt/foyer/secrets/portal-tls as
#   # portal.crt (leaf first, then the chain) and portal.key, then
#   sudo FOYER_PORTAL_URL=https://wifi.example/en/ bash deploy/first-boot.sh
#   # — and a certificate uploaded in the console wins over it.
#   # docs/portal-https.md says which certificate, and what the walled garden needs.
#
set -euo pipefail

FOYER_ROOT="${FOYER_ROOT:-/opt/foyer}"
# The account that owns the deployment, derived exactly as install.sh derives it
# (install.sh exports it): explicit FOYER_DEPLOY_USER, then the owner of an
# existing ${FOYER_ROOT}, then whoever invoked sudo, then whoever is running this.
# The owner of ${FOYER_ROOT} comes before SUDO_USER so that `upgrade.sh` run as
# root, or a re-run by a second administrator, does not re-own the secrets.
DEPLOY_USER="${FOYER_DEPLOY_USER:-${DEPLOY_USER:-}}"
if [[ -z "${DEPLOY_USER}" ]]; then
  DEPLOY_USER="$(stat -c %U "${FOYER_ROOT}" 2>/dev/null || true)"
  [[ -n "${DEPLOY_USER}" && "${DEPLOY_USER}" != "root" && "${DEPLOY_USER}" != "UNKNOWN" ]] \
    || DEPLOY_USER="${SUDO_USER:-$(id -un)}"
fi
SRC="${FOYER_ROOT}/src"
SECRETS="${FOYER_ROOT}/secrets"
STATE="${FOYER_ROOT}/state"
TLS="${SECRETS}/policy-tls"
ADMIN_TLS="${SECRETS}/admin-tls"
PORTAL_TLS="${SECRETS}/portal-tls"
# The FreeRADIUS handoff directory: the control plane writes each generated tree
# in here and the supervisor in the FreeRADIUS image takes it up. Created below
# with the container's uid, and the reason it could not be is written there.
RADDB="${SECRETS}/raddb"
ENV_FILE="${SECRETS}/foyer.env"

say() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
note() { printf '    %s\n' "$*"; }
die() { printf 'first-boot: %s\n' "$*" >&2; exit 1; }

# The uid deploy/Dockerfile.foyer runs the control plane as. Every secret that
# container reads is chowned to it, because it is not root and a file it cannot
# read is a service that will not start.
#
# READ from the Dockerfile rather than restated here. A second copy of this number
# drifts the day somebody changes the image, silently, and the symptom is a
# permission-denied that reads like a bug in Docker.
FOYER_UID="$(sed -n 's/.*adduser .*-u \([0-9][0-9]*\) .*/\1/p' "${SRC}/deploy/Dockerfile.foyer" | head -1)"
[[ -n "${FOYER_UID}" ]] || die "could not read the runtime uid out of deploy/Dockerfile.foyer — it is the authority for it, and guessing produces a stack that starts and cannot read its own secrets"

secrets_only=0
[[ "${1:-}" == "--secrets-only" ]] && secrets_only=1


# shellcheck source=logins.sh
. "${SRC}/deploy/logins.sh"

# `--promote-logins`: the one step upgrade.sh takes after `migrate` has granted the
# login a staged `database_url` names. Nothing else; `deploy/logins.sh` says why the
# switch is a separate step at all.
if [[ "${1:-}" == "--promote-logins" ]]; then
  foyer_promote_logins "${SECRETS}"
  exit 0
fi

[[ $EUID -eq 0 ]] || die "run me with sudo — I create files owned by other users and chown a private key"
[[ -d "${FOYER_ROOT}" ]] || die "${FOYER_ROOT} does not exist. Install with: sudo bash deploy/install.sh"
command -v docker >/dev/null 2>&1 || die "docker is not installed. Install with: sudo bash deploy/install.sh"
command -v openssl >/dev/null 2>&1 || die "openssl is not installed."
id -u "${DEPLOY_USER}" >/dev/null 2>&1 || die "no such user ${DEPLOY_USER}. Set FOYER_DEPLOY_USER= to the account that owns ${FOYER_ROOT} and re-run."

install -d -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" -m 0700 "${SECRETS}"
install -d -o "${DEPLOY_USER}" -g "${DEPLOY_USER}" -m 0755 "${STATE}"
# Owned by the container uid, not the deploy user.
#
# The files inside were already chowned to ${FOYER_UID}; the DIRECTORY was not, and
# 0700 owned by somebody else means the process cannot traverse into it — the files'
# own permissions never get consulted. The symptom is a control plane that reads its
# secrets, connects to PostgreSQL, and then dies on
# "open /run/foyer/tls/policy.crt: permission denied" with a file it owns.
#
# ca.key lives here too and stays root-owned and 0400 (below), so widening the
# directory does not widen the key.
install -d -o "${FOYER_UID}" -g "${FOYER_UID}" -m 0700 "${TLS}"

# The console's certificate directory, and it differs from every other directory
# this script makes in one way: **the control plane WRITES into it.**
#
# The console is HTTPS with a self-generated certificate, and every parameter is
# generated by the application — so this script does not run openssl here. It
# creates an empty directory the process can write, and the process fills it on
# the first start that finds it empty and reads it back on every start after.
#
# Owned by ${FOYER_UID} for the policy directory's reason above, one step further
# along: there, a directory the container could not TRAVERSE stops it reading files
# it owns. Here it must also be able to CREATE, and a directory root makes for
# another user is a console that cannot start — with an error that reads as a TLS
# problem rather than as a permissions one.
install -d -o "${FOYER_UID}" -g "${FOYER_UID}" -m 0700 "${ADMIN_TLS}"
# The portal's optional pair. Supplied by the operator and never generated — see
# docs/portal-https.md for why the portal, unlike the console, makes no certificate
# of its own. Created empty so the read-only bind mount in compose.yaml has a
# source Docker will not create as root:root.
install -d -o "${FOYER_UID}" -g "${FOYER_UID}" -m 0700 "${PORTAL_TLS}"
# The FreeRADIUS handoff directory, and the fourth case of the same rule as the
# three above. If nothing here created it, Docker would create the bind mount's
# source as `root:root 0755` on the first `up.sh`, and every later enforcement of
# the right ownership would fail with
#
#     install: cannot change owner and permissions of '/opt/foyer/secrets/raddb':
#     Operation not permitted
#
# because `deploy/secretdirs.sh` runs as the deploy user and a non-root process
# may chown a file to the uid it **already** has and to no other. The repair path
# could never repair it, and the console's Apply would have nowhere to put a
# generated tree — which is the whole of what the FreeRADIUS supervisor exists to
# take up.
#
# It is here rather than in `secretdirs.sh` because this is the script that runs
# as root and therefore the only one that can create a directory owned by the
# container's uid at all. `secretdirs.sh` stays what its header says: enforce on
# every start, generate conditionally.
install -d -o "${FOYER_UID}" -g "${FOYER_UID}" -m 0700 "${RADDB}"

# ── Randomness ───────────────────────────────────────────────────────────────
#
# `openssl rand` reads the kernel CSPRNG — the same source crypto/rand uses, and
# docs/rules/security.md §9's rule is about the source rather than about the language. Never
# $RANDOM, never a timestamp, never a shuffled dictionary.
#
# The database password is hex because it is interpolated into a `postgres://`
# URL, and a base64 password containing `/` or `+` produces a URL that parses
# into a different password than the one that was generated. 24 bytes is 192
# bits either way.
gen_hex() { openssl rand -hex 24; }
gen_b64() { openssl rand -base64 32; }

# keep <path> <mode> <owner> <generator…>  — generates only if absent, but
# ENFORCES mode and owner on every run.
#
# The split matters. If "idempotent" meant "do nothing when the file exists", a
# release that changes the required ownership of an existing install's secrets
# could not be applied by re-running this script — the obvious remedy, and the one
# the README tells you to reach for — and the control plane would stay in a restart
# loop saying `permission denied` on a file this script knows how to fix.
#
# Generating is what must not repeat, because it would rotate a live key.
# Correcting mode and owner must repeat, because that is how a fix reaches a host
# that already exists.
keep() {
  local path="$1" mode="$2" owner="$3"
  shift 3
  if [[ -s "${path}" ]]; then
    note "$(basename "${path}") already exists, leaving its contents alone"
  else
    ( umask 077; "$@" > "${path}" )
    note "generated $(basename "${path}")"
  fi
  chmod "${mode}" "${path}"
  chown "${owner}" "${path}"
}

# ── The console certificate's fingerprint ────────────────────────────────────
#
# Printed where the operator is standing, because an operator who is told "the
# browser will warn once" has no way to tell that warning from an attacker's — it
# looks identical either way, and *click through it* is precisely the habit a man
# in the middle relies on. A fingerprint is something they can compare against
# what the browser shows, which turns the warning into a check rather than a
# shrug. `internal/nas` pins a controller certificate on first use for the same
# reason, pointed the other way.
#
# This script does not GENERATE the certificate — the control plane does, at the
# paths compose.yaml names, on its first start. So this reads a file that may not
# be there yet, and when it is not it says which condition failed rather than
# printing nothing.
console_fingerprint() {
  local crt="${ADMIN_TLS}/admin.crt" fp until_ sans
  if [[ ! -s "${crt}" ]]; then
    note "No console certificate at ${crt}."
    note "The control plane writes it on the first start that finds the directory"
    note "empty. If it is still absent once the stack is healthy, this build is not"
    note "serving the console over TLS. Ask the container what it was told:"
    note "  ${SRC}/deploy/compose.sh exec -T foyer printenv FOYER_ADMIN_TLS_CERT"
    return
  fi
  fp="$(openssl x509 -in "${crt}" -noout -fingerprint -sha256 2>/dev/null | sed 's/^.*=//')"
  until_="$(openssl x509 -in "${crt}" -noout -enddate 2>/dev/null | sed 's/^notAfter=//')"
  # The names, because a browser refusing the NAME and one refusing the AUTHORITY
  # produce warnings that look alike and mean opposite things. The authority
  # warning is expected and click-through. A name error means this certificate is
  # for a machine that is not the one you dialled, and the answer is to fix the
  # deployment rather than to accept it.
  sans="$(openssl x509 -in "${crt}" -noout -ext subjectAltName 2>/dev/null \
    | tail -n +2 | tr -d ' ' | sed 's/DNS://g; s/IPAddress://g')"
  printf '\n    ────────────────────────────────────────────────────────────────────────\n'
  printf '    The console is HTTPS with a certificate this install generated for\n'
  printf '    itself. **Your browser will warn you, once, and that is expected.**\n'
  printf '    It warns because nothing outside this machine vouches for the\n'
  printf '    certificate — not because anything is wrong with the install.\n'
  printf '\n'
  printf '    Before you click through it, check that the browser is showing YOU:\n'
  printf '\n'
  printf '      SHA-256  %s\n' "${fp:-could not be read; is openssl installed?}"
  printf '      valid to %s\n' "${until_:-unknown}"
  printf '      the names it is valid FOR:\n'
  printf '        %s\n' "${sans:-could not be read}"
  printf '\n'
  printf '    A warning for a DIFFERENT fingerprint is not this certificate, and\n'
  printf '    clicking through that one is the whole attack. Nothing here is a\n'
  printf '    public certificate authority and nothing here should be trusted by\n'
  printf '    anything but the operator who reads this line.\n'
  printf '\n'
  printf '    This value is NOT permanent. The certificate renews when it is close\n'
  printf '    to expiring, and a renewal changes the fingerprint — the control\n'
  printf '    plane says so in its startup log when it happens, so a warning you\n'
  printf '    did not expect is answerable rather than alarming. Read the current\n'
  printf '    one back at any time with:\n'
  printf '      openssl x509 -in %s -noout -fingerprint -sha256\n' "${crt}"
  printf '    ────────────────────────────────────────────────────────────────────────\n'
}

say "Secrets"
keep "${SECRETS}/postgres_password" 0600 "${DEPLOY_USER}:${DEPLOY_USER}" gen_hex

# The two LOGIN passwords (migration 00019). The superuser's password above is
# used by the `migrate` one-shot and nothing else; the control plane logs in as
# foyer_app and FreeRADIUS as foyer_radius, neither a superuser, so a RESET ROLE
# from either gains nothing. Generated here, independently, like every
# other secret on this box; `foyerctl migrate` sets them in PostgreSQL on every
# run, so a value rotated in one of these files reaches the database on the next
# `up`. Hex, for the URL reason gen_hex gives.
#
# 0400 and owned by the container's uid, for database_url's reason below: the
# migrate one-shot runs as ${FOYER_UID} and reads them as bind-mounted secrets.
keep "${SECRETS}/app_db_password" 0400 "${FOYER_UID}:${FOYER_UID}" gen_hex
keep "${SECRETS}/radius_db_password" 0400 "${FOYER_UID}:${FOYER_UID}" gen_hex
# And the two maintenance roles' (migration 00021):
# the control plane's stale-session reaper runs as foyer_reaper and its retention
# sweep as foyer_retention, each on a pool of its own, because foyer_app may not
# SET ROLE into either. Same generation, same ownership, same rotation.
keep "${SECRETS}/reaper_db_password" 0400 "${FOYER_UID}:${FOYER_UID}" gen_hex
keep "${SECRETS}/retention_db_password" 0400 "${FOYER_UID}:${FOYER_UID}" gen_hex
# The maintenance door's token. The two logins above are the `maintenance`
# service's, not the control plane's, and the control plane asks that service for
# the retention half of an erasure over the internal network, presenting this
# (internal/jobs/remote.go). 48 hex characters; the door
# refuses fewer than 32. Both containers read it as the image's uid.
keep "${SECRETS}/maintenance_token" 0400 "${FOYER_UID}:${FOYER_UID}" gen_hex

# FOYER_KEK is `id:base64key`, active key first (internal/store/envelope.go). The
# id is dated so that a later rotation can say which key sealed which row without
# guessing — a bare key would work today and make the second key ambiguous.
gen_kek() { printf '%s:%s\n' "$(date -u +%Y-%m)" "$(gen_b64)"; }
keep "${SECRETS}/kek" 0400 "${FOYER_UID}:${FOYER_UID}" gen_kek

# ── The Policy API's TLS material ────────────────────────────────────────────
#
# docs/rules/security.md §2 wants mTLS between FreeRADIUS and the Policy API. §7 says never
# ship a CA in an image, because a fixed key means every deployment on earth
# shares one private key — so this install generates its own, here, once.
#
# A CA plus a server certificate rather than one self-signed certificate, because
# internal/raddb's rest module also presents a CLIENT certificate, issued from this
# CA a few lines below. One CA means one trust root for both directions.
say "Policy API certificate"
if [[ -s "${TLS}/policy.crt" ]]; then
  note "policy.crt already exists, leaving it alone"
else
  ( umask 077
    openssl req -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
      -keyout "${TLS}/ca.key" -out "${TLS}/ca.crt" \
      -subj "/CN=Foyer policy CA (${HOSTNAME:-foyer})" \
      -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
      -addext "keyUsage=critical,keyCertSign,cRLSign" >/dev/null 2>&1

    openssl req -newkey rsa:2048 -sha256 -nodes \
      -keyout "${TLS}/policy.key" -out "${TLS}/policy.csr" \
      -subj "/CN=foyer" >/dev/null 2>&1

    # SAN `foyer` because that is the compose service name the rest module
    # connects to. Loopback is there for a probe run inside the container.
    openssl x509 -req -in "${TLS}/policy.csr" -sha256 -days 825 \
      -CA "${TLS}/ca.crt" -CAkey "${TLS}/ca.key" -CAcreateserial \
      -out "${TLS}/policy.crt" \
      -extfile <(printf 'subjectAltName=DNS:foyer,DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n') \
      >/dev/null 2>&1
    rm -f "${TLS}/policy.csr"
  )
  chmod 0644 "${TLS}/policy.crt" "${TLS}/ca.crt"
  chmod 0400 "${TLS}/policy.key" "${TLS}/ca.key"
  # The server key is readable by the control-plane container's uid and by
  # nobody else. The CA key stays with root: nothing running needs it, and it is
  # what would let an attacker mint a client certificate.
  chown "${FOYER_UID}:${FOYER_UID}" "${TLS}/policy.key" "${TLS}/policy.crt"
  chown "root:root" "${TLS}/ca.key" "${TLS}/ca.crt"
  chmod 0644 "${TLS}/ca.crt"
  note "generated a per-install CA and a policy server certificate"
fi

# ── The certificate FreeRADIUS presents ──────────────────────────────────────
#
# docs/rules/security.md §2 requires **mTLS** on the rlm_rest boundary. The encrypting half
# alone — a Policy API with a certificate, and nothing checking who is calling it —
# is, on an internal bridge, an endpoint answering "is this guest allowed on" to
# any container that can route to it.
#
# Issued from the CA above, so no second trust root exists. Separate directory,
# because ${TLS} holds the CA key. Root-owned and readable by gid 1812 only --
# radiusd reads it as `freerad` through that group.
#
# **The order matters and it is why this is above the compose up.** rlm_rest opens
# ca_info_file at INSTANTIATION, not on the first request — measured on 3.2.10:
# with the file absent the server says "Failed parsing configuration item" and
# refuses to start. A tree that requires a certificate, generated before one
# exists, is a data plane that never comes up.
say "FreeRADIUS client certificate"
# One authority, two callers: `upgrade.sh` runs the same script, because an
# older install may have the CA and the server certificate and no client
# certificate, and the tree generated now requires one.
bash "$(dirname "${BASH_SOURCE[0]}")/issue-radius-cert.sh"

# ── The environment file ─────────────────────────────────────────────────────
say "Environment"

# An existing value is kept. This script is idempotent and `upgrade.sh` calls it,
# so re-deriving the portal URL from `ip route` would silently overwrite an
# operator's deliberate FOYER_PORTAL_URL on every upgrade — and a wrong value
# there is a portal that never opens on a phone.
portal_url="${FOYER_PORTAL_URL:-}"
if [[ -z "${portal_url}" && -s "${ENV_FILE}" ]]; then
  portal_url="$(sed -n 's/^FOYER_PORTAL_URL=//p' "${ENV_FILE}" | tail -1)"
  [[ -n "${portal_url}" ]] && note "keeping the FOYER_PORTAL_URL already in ${ENV_FILE}: ${portal_url}"
fi
if [[ -z "${portal_url}" ]]; then
  host_ip="$(ip route get 1.1.1.1 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}')"
  [[ -n "${host_ip}" ]] || die "could not work out this host's address; set FOYER_PORTAL_URL and re-run"
  portal_url="http://${host_ip}/en/"
  note "FOYER_PORTAL_URL was not set, so it is ${portal_url}"
  note "This is what RFC 8908 hands the guest's operating system. If guests reach"
  note "the portal by a name rather than by this address, set FOYER_PORTAL_URL and"
  note "re-run — a wrong value here is a portal that never opens on a phone."
fi

# ── The optional HTTPS portal ────────────────────────────────────────────────
#
# **A bootstrap fallback.** An operator never edits a file or re-runs this script
# to change a setting: the portal certificate and the portal's address are
# uploaded and set in the admin console (Network), stored in the database with the
# key sealed under the KEK, and served without a restart — cmd/foyer binds the
# HTTPS listener (published on 443/8843 already) the moment the first one is
# uploaded. Nothing below is required for that.
# What stays is the path for an install that already put a pair here: it keeps
# working, and a pair uploaded in the console wins over it.
#
# Off unless the operator put a pair in ${PORTAL_TLS}. Both files or neither: one
# alone is a half-done install, and cmd/foyer refuses a half-set pair anyway —
# refusing here says which file is missing before anything starts.
#
# With a pair, FOYER_PORTAL_URL must be https and must name what the certificate
# covers. cmd/foyer checks the second half itself (it refuses a certificate that
# does not cover the URL's host); the scheme is checked here because a derived
# http:// address is exactly what the branch above produces when nothing was set.
portal_tls=0
if [[ -e "${PORTAL_TLS}/portal.crt" || -e "${PORTAL_TLS}/portal.key" ]]; then
  [[ -s "${PORTAL_TLS}/portal.crt" && -s "${PORTAL_TLS}/portal.key" ]] \
    || die "${PORTAL_TLS} holds one of portal.crt / portal.key and not the other; supply both, or remove both for the plain-HTTP portal"
  [[ "${portal_url}" == https://* ]] \
    || die "a portal certificate is in ${PORTAL_TLS}, so FOYER_PORTAL_URL must be the https:// address it covers (got ${portal_url}); re-run with FOYER_PORTAL_URL=https://<name>/en/"
  # The container reads them as ${FOYER_UID}. The key is nobody else's business.
  chown "${FOYER_UID}:${FOYER_UID}" "${PORTAL_TLS}/portal.crt" "${PORTAL_TLS}/portal.key"
  chmod 0444 "${PORTAL_TLS}/portal.crt"
  chmod 0400 "${PORTAL_TLS}/portal.key"
  portal_tls=1
  note "portal certificate found in ${PORTAL_TLS}: the portal will serve HTTPS at ${portal_url}"
elif [[ "${portal_url}" == https://* ]]; then
  # The pair was removed but the URL kept (it is kept across re-runs, above). Not
  # refused: a TLS-terminating proxy in front of the portal is a deliberate shape.
  # But without one this is RFC 8908 handing every guest an address nothing on
  # this host answers.
  note "WARNING: FOYER_PORTAL_URL is ${portal_url} but ${PORTAL_TLS} holds no certificate."
  note "         Unless a certificate is uploaded in the console (Network → Portal"
  note "         certificate) or something in front of this host terminates TLS, the"
  note "         portal is served over plain HTTP only; the portal's address can be"
  note "         changed in the console too."
fi

# Where an operator opens the console. Derived from the same address the portal
# URL is, because it is the same machine and the operator is on the same LAN —
# and printed rather than stored, since it is a sentence in the output and not a
# setting anything reads back.
console_host="$(ip route get 1.1.1.1 2>/dev/null | awk '{for (i=1;i<=NF;i++) if ($i=="src") {print $(i+1); exit}}')"
console_url="https://${console_host:-127.0.0.1}:8443"

pg_password="$(cat "${SECRETS}/postgres_password")"
app_db_password="$(cat "${SECRETS}/app_db_password")"
reaper_db_password="$(cat "${SECRETS}/reaper_db_password")"
retention_db_password="$(cat "${SECRETS}/retention_db_password")"

# ── the connection strings, as files rather than as variables ────────────────
#
# They carry a database password inline, and an environment variable is readable
# through `docker inspect` and `/proc/<pid>/environ` by anything that can reach
# the daemon or the namespace. PostgreSQL itself avoids this with
# POSTGRES_PASSWORD_FILE; cmd/foyer reads FOYER_DATABASE_URL_FILE and
# FOYER_KEK_FILE the same way, and compose.yaml passes them as docker secrets.
#
# **Two of them.** `owner_database_url` is the superuser's and only the `migrate`
# one-shot is given it. `database_url` is the control plane's and logs in as
# foyer_app — were it the superuser, `SET ROLE` would be the only thing between
# one SQL injection and the whole cluster. cmd/foyer refuses to start on any login
# that holds more than foyer_app.
#
# `sslmode=disable` is honest: PostgreSQL is on an internal Docker network with
# no gateway, reachable by two containers and nothing else, and a TLS certificate
# for it would be a second per-install CA for no attacker this removes.
#
# **`database_url` is staged, not overwritten, when it changes** (deploy/logins.sh).
# It is the one file a running control plane re-reads on a restart, and the login it
# names may not exist yet: on an install from before 00019 it switches from the
# superuser to foyer_app, which `foyerctl migrate` only grants later. The other
# three are read by the migrate one-shot or by code the new image brings, so they
# are written in place.
foyer_write_secret_file "${SECRETS}/owner_database_url" "${FOYER_UID}:${FOYER_UID}" \
  "postgres://foyer:${pg_password}@postgres:5432/foyer?sslmode=disable"
case "$(foyer_stage_login_url "${SECRETS}/database_url" "${FOYER_UID}:${FOYER_UID}" \
  "postgres://foyer_app:${app_db_password}@postgres:5432/foyer?sslmode=disable")" in
  staged)
    note "database_url changes, so the new one is STAGED as database_url.next and the"
    note "running control plane keeps its login. upgrade.sh promotes it after migrate"
    note "has granted it; up.sh and this script, just before they start the stack." ;;
  *) ;;
esac
# The maintenance jobs' own logins (migration 00021). cmd/foyer reads them as
# FOYER_REAPER_DATABASE_URL_FILE / FOYER_RETENTION_DATABASE_URL_FILE; without
# them the two jobs fail every cycle, because foyer_app cannot SET ROLE.
foyer_write_secret_file "${SECRETS}/reaper_database_url" "${FOYER_UID}:${FOYER_UID}" \
  "postgres://foyer_reaper:${reaper_db_password}@postgres:5432/foyer?sslmode=disable"
foyer_write_secret_file "${SECRETS}/retention_database_url" "${FOYER_UID}:${FOYER_UID}" \
  "postgres://foyer_retention:${retention_db_password}@postgres:5432/foyer?sslmode=disable"
# 0400 and owned by the container's uid, not the deploy user.
#
# Compose bind-mounts a secret with the host's ownership, the control plane runs as
# ${FOYER_UID}, and 0600-root-of-the-deploy-user means every service that reads
# this file dies with "permission denied" before it does anything. The directory
# above stays 0700, so nothing else on the host can reach in.
for f in owner_database_url database_url reaper_database_url retention_database_url; do
  note "${SECRETS}/${f} is 0400, uid ${FOYER_UID} — the container reads it"
done

# Written whole each time, because it is derived from the files above rather than
# edited.
#
# **What is NOT in here:** FOYER_DATABASE_URL, FOYER_KEK and any database
# password. An env_file is read by every service including the control plane, so
# a password in it is everybody's. FreeRADIUS's own password is in
# radius_db_password and is handed to the freeradius service alone: the tree the
# control plane generates names it as $ENV{FOYER_RADIUS_DB_PASSWORD}, which
# FreeRADIUS's supervisor exports from that secret.
( umask 077
  cat > "${ENV_FILE}" <<EOF
# Generated by deploy/first-boot.sh. NOT IN ANY REPOSITORY, and it must never be.
# Regenerating this file is safe; regenerating the values it reads is not.
#
# Every secret is a file in ${SECRETS}, passed to the containers as a docker
# secret, so none of them is readable through docker inspect.
FOYER_PORTAL_URL=${portal_url}
EOF
  # Paths inside the container (compose.yaml mounts ${PORTAL_TLS} there, :ro).
  if [[ ${portal_tls} -eq 1 ]]; then
    cat >> "${ENV_FILE}" <<EOF
FOYER_PORTAL_TLS_CERT=/run/foyer/portal-tls/portal.crt
FOYER_PORTAL_TLS_KEY=/run/foyer/portal-tls/portal.key
EOF
  fi
)
chmod 0600 "${ENV_FILE}"
chown "${DEPLOY_USER}:${DEPLOY_USER}" "${ENV_FILE}"
note "wrote ${ENV_FILE} (0600, ${DEPLOY_USER})"

cat <<'WARN'

    ────────────────────────────────────────────────────────────────────────
    FOYER_KEK is in /opt/foyer/secrets/kek and is not printed here.

    It is the key-encryption key for every RADIUS shared secret, every
    controller credential and every NT hash in this deployment. LOSING IT
    MAKES ALL OF THEM PERMANENTLY UNREADABLE — there is no recovery path,
    by design: the key is deliberately not in the database it protects.

    Back up /opt/foyer/secrets somewhere that is not this machine, and not
    this repository. A backup of the database without it is a backup of
    ciphertext.
    ────────────────────────────────────────────────────────────────────────
WARN

if [[ ${secrets_only} -eq 1 ]]; then
  say "Done (--secrets-only)"
  # An existing install upgrading through `upgrade.sh` arrives here, and it has a
  # console certificate already — printing it is how the fingerprint stays
  # available to somebody who has lost the first-boot output. A fresh install has
  # none yet, because nothing has started, and the function says so.
  console_fingerprint
  note "Nothing was started. Bring it up with: deploy/up.sh"
  exit 0
fi

# ── Start it ─────────────────────────────────────────────────────────────────
say "Starting"
[[ -f "${SRC}/deploy/compose.yaml" ]] || die "${SRC}/deploy/compose.yaml is not there. Install with: sudo bash deploy/install.sh, from a checkout of the release — it places the tree in ${SRC}."

# FOYER_REV tags the images and becomes org.opencontainers.image.revision, which
# is how verify.sh asks a running container which commit it is. Without it there
# is no answer, so `unknown` is treated as drift rather than as a default.
FOYER_REV="$(cat "${SRC}/.shipped-rev" 2>/dev/null || echo unknown)"
export FOYER_REV

# The images, and whether they must be built here. `deploy/up.sh` reads the same
# file, so the two cannot disagree. See `deploy/images.sh`.
# shellcheck source=/dev/null
. "${SRC}/deploy/images.sh"

# The names the console's certificate must cover. `deploy/up.sh` sources the same
# file, for the same reason `images.sh` is a file at all.
# shellcheck source=/dev/null
. "${SRC}/deploy/consolenames.sh"

COMPOSE_FILES=("${FOYER_COMPOSE_FILES[@]}")
export FOYER_PORTAL_URL="${portal_url}"

# Promoted after the pull and immediately before `up`, which runs migrate before it
# recreates the control plane — so the login is granted before anything new reads it.
if [[ "${FOYER_BUILD_LOCALLY:-0}" == "1" ]]; then
  foyer_promote_logins "${SECRETS}"
  docker compose "${COMPOSE_FILES[@]}" up -d --build
else
  docker compose "${COMPOSE_FILES[@]}" pull --quiet
  foyer_promote_logins "${SECRETS}"
  docker compose "${COMPOSE_FILES[@]}" up -d
fi

say "Waiting for the control plane"
# HTTPS, and `--no-check-certificate` because the certificate is self-signed by
# construction and this is the container talking to its own loopback. In the
# image deploy/Dockerfile.foyer pins, BusyBox's wget does speak TLS
# (through /usr/bin/ssl_client), the flag is required against a self-signed
# certificate, and without TLS on the other end the request fails rather than
# succeeding quietly. compose.yaml's healthcheck carries the same measurement.
#
# **`/readyz` and not `/healthz`, and this is the same distinction
# `deploy/compose.yaml`'s healthcheck makes.** An install that reports success
# against a liveness-only probe has proved the process started, which is not the
# question: the control plane refuses to start without its database, and what
# matters here is whether it can *serve*. `/readyz` answers 503 when PostgreSQL is
# unreachable, so this loop now fails with a one-line diagnosis instead of timing
# out and sending an operator to the logs.
ready=0
for _ in $(seq 1 60); do
  if docker compose "${COMPOSE_FILES[@]}" exec -T foyer \
      wget --no-check-certificate -q -O- https://127.0.0.1:8443/readyz >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 2
done
if [[ ${ready} -ne 1 ]]; then
  # Two different failures answer this loop identically, and telling them apart is
  # the difference between "look at the logs" and a one-line diagnosis. So ASK.
  #
  # The condition is evaluable on every run: an image that serves the console over
  # TLS never reaches this branch, so nothing has to remember to delete it.
  if docker compose "${COMPOSE_FILES[@]}" exec -T foyer \
      wget -q -O- http://127.0.0.1:8443/healthz >/dev/null 2>&1; then
    die "the console answered /healthz over PLAINTEXT http on 8443, so the control
     plane is running and is not serving TLS. compose.yaml sets
     FOYER_ADMIN_TLS_CERT and this image ignored it: it predates console
     certificate support. Rebuild from a current release — sudo bash
     deploy/install.sh from its checkout — rather than reverting this file,
     because a console serving plaintext on a machine that configures RADIUS is
     the defect."
  fi
  die "the control plane did not become ready — its database is unreachable, or the
     stored network settings could not be read. ${SRC}/deploy/compose.sh logs foyer"
fi
note "healthy"

# ── The one value a human has to be told ─────────────────────────────────────
#
# **No password.** Foyer ships no credential (`docs/rules/security.md` §7). This
# opens the console's first-access setup and prints its one-time code, and the
# operator creates the first administrator in the console with a username and a
# password of their own. `deploy/consolesetup.sh` has why, and why a code rather
# than a page anybody on the LAN could reach first.
say "Administrator"
. "${SRC}/deploy/consolesetup.sh"
open_console_setup "${console_url}" "${COMPOSE_FILES[@]}" \
  || die "opening the console setup failed; the stack is up but nobody can administer it"

say "Done"
if [[ ${portal_tls} -eq 1 ]]; then
  note "Portal:  ${portal_url}    — HTTPS on the operator's certificate (published on"
  note "         443 and 8843). Plain HTTP on 80/8880 still receives the NAS redirect"
  note "         and sends every guest there. docs/portal-https.md: walled garden."
else
  note "Portal:  ${portal_url}    — HTTP, the default. For HTTPS, upload the"
  note "         certificate in the console (Network); docs/portal-https.md."
fi
note "Console: ${console_url}   — HTTPS, on every interface."
note "         Open it from any browser on this network; compare the fingerprint"
note "         below before accepting the warning. A tunnel still works and is"
note "         still the safer habit from off-site:"
note "         ssh -L 8443:127.0.0.1:8443 ${DEPLOY_USER}@\$(hostname -I | awk '{print \$1}')"
note "Then:    deploy/verify.sh   — asserts this machine matches the repository."
console_fingerprint

# ── What this script deliberately did not create ─────────────────────────────
#
# **This deployment has no site, and one is not seeded.** A site is a venue, so
# its name is the one fact about an install that only the operator has — a
# migration that inserted a row would be a schema file pretending to be a fixture,
# and every install would then be stuck renaming a placeholder on the first screen
# it shows. `createSite` in the contract is what makes one.
#
# It is said here because the ordering is not guessable and the failure is silent
# in the wrong direction: registering a device requires a `siteId`. The operator's
# next steps start the moment this script exits, so they belong in its output
# rather than only in `README.md`.
say "First run"
note "Open ${console_url} and accept the warning once — the fingerprint above"
note "is what you are accepting. Enter the setup code printed above to create the"
note "first administrator, or sign in if you already have."
note ""
note "This deployment has no site yet, and one is not seeded — a site is a venue and"
note "its name is yours. In the console, create it, then:"
note "  1. register the access point (it is refused without a site)"
note "  2. ${SRC}/deploy/compose.sh exec -T foyer foyerctl sync-nas"
note "     — writes the \`clients.conf\` projection FreeRADIUS reads. Until it runs,"
note "     the server has no client for the device and answers its packets with"
note "     silence rather than with a reject."
note "  3. point the access point at this host and watch \`foyerctl doctor\`."
