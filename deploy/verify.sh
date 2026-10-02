#!/usr/bin/env bash
#
# Assert that a deployment host matches this repository.
#
# `docs/rules/deployment.md`: a rule against hand-editing cannot be enforced by
# intention; what makes it real is that the machine is rebuildable. This file is
# the other half of that — the thing that notices when the machine and the
# repository have stopped agreeing, on a day when nobody is looking for it.
#
# **Every failure names the object**, as docs/rules/database.md requires of the
# `radacct` checks: a refusal that says "permission denied for
# sequence radacct_radacctid_seq" is not evidence that the table is protected, and
# a drift report that says "something changed" is not something an operator can
# act on. Each line below says which file, which package, which image.
#
#   deploy/verify.sh                               on the host
#   FOYER_HOST=foyer-host deploy/verify.sh --remote  from a workstation; adds the
#                                                    checks that need the git
#                                                    object database
#
# --remote reads FOYER_HOST (required), FOYER_USER (default: your local user
# name) and FOYER_SSH_KEY (optional; passed to ssh -i only when set).
#
# Exit status: 0 if the machine matches, non-zero and a count if it does not.
#
set -uo pipefail   # deliberately not -e: every check runs, then the total is reported

FOYER_ROOT="${FOYER_ROOT:-/opt/foyer}"
SRC="${FOYER_ROOT}/src"
STATE="${FOYER_ROOT}/state"
SECRETS="${FOYER_ROOT}/secrets"
COMPOSE="${SRC}/deploy/compose.yaml"

drifts=0
checks=0

ok()    { checks=$((checks+1)); printf '  ok    %s\n' "$*"; }
drift() { checks=$((checks+1)); drifts=$((drifts+1)); printf '  DRIFT %s\n' "$*"; }
skip()  { printf '  --    %s\n' "$*"; }
head_() { printf '\n%s\n' "$*"; }
# bounded <command…> — at most five seconds, where coreutils' timeout exists. A probe
# of a published port whose container side is not listening must fail, not hang.
bounded() {
  if command -v timeout >/dev/null 2>&1; then timeout 5 "$@"; else "$@"; fi
}

# ── --remote: the half that needs the repository ─────────────────────────────
#
# The host has no *repository*: sync.sh ships `git archive` and install.sh copies
# without `.git`, so what is there is a tree. (The git binary may well be
# installed, and that is not the same thing.) So "the .shipped-rev names a commit
# that exists" cannot be answered there, only here, where the object database is.
if [[ "${1:-}" == "--remote" ]]; then
  HOST="${FOYER_HOST:?set FOYER_HOST to the host to verify, e.g. FOYER_HOST=foyer-host deploy/verify.sh --remote}"
  USER_AT="${FOYER_USER:-$(id -un)}"
  SSH=(ssh -o BatchMode=yes)
  [[ -n "${FOYER_SSH_KEY:-}" ]] && SSH+=(-i "${FOYER_SSH_KEY}")

  self="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/$(basename "${BASH_SOURCE[0]}")"
  echo "==> verifying ${USER_AT}@${HOST}"
  rev="$("${SSH[@]}" "${USER_AT}@${HOST}" "cat ${SRC}/.shipped-rev 2>/dev/null || true")"
  rc=0
  "${SSH[@]}" "${USER_AT}@${HOST}" "bash -s" < "${self}" || rc=$?

  printf '\nRevision\n'
  if [[ -z "${rev}" ]]; then
    printf '  DRIFT the host has no .shipped-rev, so nothing says which commit it is running\n'
    rc=1
  elif git -C "$(dirname "${self}")/.." cat-file -e "${rev}^{commit}" 2>/dev/null; then
    printf '  ok    %s is a commit in this repository\n' "${rev}"
  else
    printf '  DRIFT the host is running %s, which is not a commit in this repository\n' "${rev}"
    rc=1
  fi
  exit "${rc}"
fi

echo "verify: ${FOYER_ROOT} against the shipped tree"

# ── 1. Which revision does this host think it is? ────────────────────────────
head_ "Revision"
REV=""
if [[ -s "${SRC}/.shipped-rev" ]]; then
  REV="$(cat "${SRC}/.shipped-rev")"
  ok ".shipped-rev says ${REV}"
else
  drift "${SRC}/.shipped-rev is missing or empty — nothing on this host says which commit it is running"
fi

# ── 2. Has anything under the managed tree been edited in place? ─────────────
#
# What this actually detects: a change made AFTER the sync that put the manifest
# there. It cannot detect a change made in the seconds between `tar -x` and the
# manifest being written, and it says nothing about whether the shipped tree
# matched the repository — that is `--remote`'s job and sync.sh's refusal to ship
# a dirty tree.
head_ "Managed files"
if ! command -v sha256sum >/dev/null 2>&1; then
  drift "sha256sum is not installed, so the managed tree cannot be checked at all"
elif [[ ! -s "${STATE}/src.sha256" ]]; then
  drift "${STATE}/src.sha256 is missing — deploy/install.sh and deploy/sync.sh write it, so this host was populated some other way"
else
  if bad="$(cd "${SRC}" && sha256sum -c --quiet "${STATE}/src.sha256" 2>&1)"; then
    ok "$(wc -l < "${STATE}/src.sha256" | tr -d ' ') files under ${SRC} are byte-for-byte what was shipped"
  else
    while IFS= read -r line; do
      # sha256sum's own summary line ("WARNING: 1 computed checksum did NOT
      # match") names nothing, and a drift report that names nothing is the one
      # thing this file is not allowed to produce.
      [[ -z "${line}" || "${line}" == sha256sum:* ]] && continue
      drift "${line}"
    done <<< "${bad}"
  fi

  # A checksum list only speaks about the files it lists. **A file DROPPED into
  # the managed tree is invisible to it** — appending a line to a path the
  # manifest does not know about passes the check. So the set is compared as well
  # as the contents. `.shipped-rev` is written by sync.sh after the extraction
  # and is expected to be there.
  extra="$(comm -23 \
    <(cd "${SRC}" && find . -type f | sort) \
    <(sed 's/^[0-9a-f]\{64\}  //' "${STATE}/src.sha256" | sort))"
  extra="$(printf '%s\n' "${extra}" | grep -vx './.shipped-rev' || true)"
  if [[ -z "${extra}" ]]; then
    ok "no file under ${SRC} that the manifest does not name"
  else
    while IFS= read -r f; do
      [[ -z "${f}" ]] && continue
      drift "${SRC}/${f#./} exists and was not shipped — something wrote into the managed tree"
    done <<< "${extra}"
  fi
fi

# ── 3. Packages ──────────────────────────────────────────────────────────────
#
# "No package outside the manifest" has to mean something checkable. Ubuntu ships
# several hundred packages, so the comparison is against two things: the baseline
# bootstrap.sh recorded BEFORE it installed anything, and deploy/packages.txt,
# which is what Foyer adds. Anything manually installed that is in neither was
# put there by a human, by hand, and is exactly what docs/rules/deployment.md
# forbids.
head_ "Packages"
if ! command -v apt-mark >/dev/null 2>&1; then
  skip "apt-mark is not available; this check only means something on Ubuntu"
elif [[ ! -s "${STATE}/packages.baseline" ]]; then
  drift "${STATE}/packages.baseline is missing — install.sh and bootstrap.sh record it before they install anything, so this host was prepared by something else or by hand"
else
  manifest="${SRC}/deploy/packages.txt"
  if [[ ! -s "${manifest}" ]]; then
    drift "${manifest} is missing from the shipped tree"
  else
    extra="$(comm -23 \
      <(apt-mark showmanual 2>/dev/null | sort -u) \
      <(cat "${STATE}/packages.baseline" <(grep -vE '^\s*(#|$)' "${manifest}") | sort -u))"
    if [[ -z "${extra}" ]]; then
      ok "no manually installed package outside deploy/packages.txt"
    else
      while IFS= read -r p; do
        [[ -z "${p}" ]] && continue
        drift "package ${p} is installed and is in neither the baseline nor deploy/packages.txt"
      done <<< "${extra}"
    fi
  fi
fi

# ── 4. Images ────────────────────────────────────────────────────────────────
head_ "Images"
if ! command -v docker >/dev/null 2>&1; then
  drift "docker is not installed, so nothing this repository describes is running"
else
  # 4a. Every digest pinned in compose.yaml resolves to an image that is present,
  #     and the container using it is running that exact image and not a
  #     same-named one that was pulled since.
  # The pins live in images.conf, so that one file names what a deployment runs
  # and compose.yaml describes only shape.
  pinned="$(grep -oE '[A-Za-z0-9./_-]+(:[A-Za-z0-9._-]+)?@sha256:[0-9a-f]{64}' "${SRC}/deploy/images.conf" 2>/dev/null | sort -u)"
  if [[ -z "${pinned}" ]]; then
    drift "deploy/images.conf pins no image by digest; a deployment requires digests, never floating tags.
        An unpinned install is what FOYER_ALLOW_UNPINNED=1 produces, and it runs an
        image nobody else can verify — which is exactly what this check is for."
  else
    while IFS= read -r ref; do
      [[ -z "${ref}" ]] && continue
      if id="$(docker image inspect -f '{{.Id}}' "${ref}" 2>/dev/null)"; then
        ok "${ref%%@*} is the pinned digest (${id:7:12})"
      else
        drift "image ${ref} is pinned in deploy/images.conf and is not present on this host"
      fi
    done <<< "${pinned}"
  fi

  # 4b. The control plane's own image cannot be digest-pinned — it is built here,
  #     from the shipped tree. Its equivalent is the revision label, and a
  #     container that answers `unknown` is one that was started without
  #     FOYER_REV: it is running something, and nothing says what.
  # **Ask compose why, do not assume.** `compose.sh ps -q` answers an empty string
  # for two entirely different situations — the container is not up, and compose
  # could not evaluate compose.yaml at all — and discarding stderr makes the second
  # read as the first: a required variable in compose.yaml that compose.sh does not
  # load produces `the foyer service is not running` for containers that are up
  # and healthy, with the real message on stderr.
  #
  # A verification tool that reports the wrong outage is worse than one that
  # reports none: it sends an operator to restart a stack that is fine.
  compose_err=""
  if ! cid="$("${SRC}/deploy/compose.sh" ps -q foyer 2>/tmp/foyer-verify-compose.err)"; then
    compose_err="$(tr '\n' ' ' < /tmp/foyer-verify-compose.err | sed 's/  */ /g; s/ *$//')"
  fi
  rm -f /tmp/foyer-verify-compose.err
  if [[ -n "${compose_err}" ]]; then
    drift "docker compose could not read ${COMPOSE}, so NOTHING below about running services means anything: ${compose_err}"
    cid=""
  fi
  if [[ -n "${compose_err}" ]]; then
    : # already reported; do not also claim the service is down
  elif [[ -z "${cid}" ]]; then
    drift "the foyer service is not running"
  else
    label="$(docker inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "${cid}" 2>/dev/null)"
    case "${label}" in
      "")        drift "the running foyer image carries no org.opencontainers.image.revision label" ;;
      unknown)   drift "the running foyer image says revision=unknown; it was built without FOYER_REV, so nothing can say which commit is serving guests" ;;
      "${REV}")  ok "the running foyer image was built from ${REV}" ;;
      *)         drift "the running foyer image was built from ${label}, but ${SRC}/.shipped-rev says ${REV}" ;;
    esac

    # 4b-i. Ask the BINARY, not the wrapper around it.
    #
    # The label above is metadata on the image: `docker tag` copies it, a rebuild
    # from a different tree can carry the old one, and reading it needs the daemon.
    # None of that is true of a string linked into the executable. So the same
    # value is asserted at a second, independent surface — and the two disagreeing
    # is itself a finding, because it means the running image is not the image its
    # label describes.
    #
    # This is also what keeps the check from being satisfied by its own input:
    # `verify.sh` is handed FOYER_REV by compose.sh, and a check that only compares
    # a label built from that value against that value is a harness that cannot be
    # wrong about itself.
    stamped="$(docker exec "${cid}" foyerctl version 2>/dev/null \
      | sed -n 's/.*(revision \(.*\))/\1/p' | head -1)"
    case "${stamped}" in
      "")        drift "the running control plane's \`foyerctl version\` printed no revision at all — this binary predates internal/version, or the command failed" ;;
      unknown)   drift "the control plane BINARY reports revision=unknown: the image was built without --build-arg REVISION, so the process serving guests cannot name its own commit even though the image label can" ;;
      "${label}") ok "the control plane binary reports ${stamped}, the same revision its image is labelled with" ;;
      *)         drift "the control plane binary reports ${stamped} and its image is labelled ${label} — the running image is not the image that label describes" ;;
    esac
  fi

  # 4b-iii. Orphan reaping — the check that catches a PID-exhaustion outage
  #         hours before it happens rather than after.
  #
  # On Ubuntu 24.04, the healthcheck's BusyBox `wget` forks `/usr/bin/ssl_client`
  # and exits without reaping it, the orphan is reparented to container PID 1, and
  # the pids cgroup controller uncharges at reap rather than at exit — so one PID
  # is lost per probe until `pids.max` is reached and nothing in the container can
  # fork at all. `deploy/compose.yaml` answers it with `init: true`
  # and the reasoning is written there.
  #
  # **This is not a restatement of that fix; it is an independent measurement of the
  # thing the fix is supposed to prevent.** `init: true` being present in the file
  # proves the file says it. Zero zombies in a container that has been up long enough
  # to have accumulated some is what proves an init is actually reaping. The two can
  # disagree — an old container still running from before the change is exactly that
  # case, and it is the one an operator is most likely to be standing in front of.
  #
  # Counted from the HOST rather than inside the container, deliberately: the failure
  # mode being measured is one where `docker exec` cannot start a process, so a probe
  # that needs an exec answers "cannot tell" precisely when the answer is "very bad".
  # This is also why the check above it, `foyerctl version` over `docker exec`, is not
  # a substitute — in that state it goes red with "this binary predates
  # internal/version, or the command failed", which is a true sentence that sends an
  # operator to the wrong place entirely.
  if [[ -n "${compose_err}" ]]; then
    skip "orphan reaping: compose could not be read, reported above"
  elif [[ -z "${cid}" ]]; then
    skip "orphan reaping: the foyer service is not running, reported above"
  else
    fpid="$(docker inspect -f '{{.State.Pid}}' "${cid}" 2>/dev/null)"
    if [[ -z "${fpid}" || "${fpid}" == "0" ]]; then
      drift "cannot read the foyer container's host PID, so orphan reaping is unmeasured"
    else
      zombies="$(ps -eo stat=,ppid= | awk -v p="${fpid}" '$1 ~ /^Z/ && $2 == p' | wc -l)"
      pidsmax="$(docker inspect -f '{{.HostConfig.PidsLimit}}' "${cid}" 2>/dev/null)"
      if [[ "${zombies}" -eq 0 ]]; then
        ok "the foyer container is reaping orphans; no zombies under PID ${fpid}"
      else
        drift "the foyer container has ${zombies} unreaped zombie children under PID ${fpid}. Each one holds a PID against this container's cgroup limit (${pidsmax:-unset}), which the pids controller releases only on reap. When the limit is reached NOTHING in this container can fork: the healthcheck fails with \`procReady not received\`, \`docker exec\` stops working, and the control plane goes on serving so its logs say nothing. Check that \`init: true\` is set on the foyer service in compose.yaml and recreate the container."
      fi
    fi
  fi

  for svc in postgres freeradius; do
    if [[ -n "${compose_err}" ]]; then
      skip "${svc}: compose could not be read, reported above"
    elif [[ -z "$("${SRC}/deploy/compose.sh" ps -q "${svc}" 2>/dev/null)" ]]; then
      drift "the ${svc} service is not running"
    else
      ok "${svc} is running"
    fi
  done

  # 4b-iv. The two process-level endpoints, and the one that is deliberately
  # absent from two of the three listeners.
  #
  # **This is the operator-facing half of `internal/obs`'s decisions, and it is
  # here because `verify.sh` is the tool an operator runs when something is
  # wrong.** The Go tests hold the behaviour; this holds it on the real box, where
  # a listener can be published that the tests never bind.
  #
  #   * `/healthz` on the console — liveness. Must answer 200. A process that
  #     cannot answer this is not running, and everything else here is moot.
  #   * `/readyz` on the console — readiness. Must answer 200, and the body must
  #     say `ready`. A liveness check only proves the process is up, so a control
  #     plane whose database is gone would report healthy and keep receiving
  #     packets.
  #   * `/metrics` on the policy port — the one listener `compose.yaml` does not
  #     publish. Probed from INSIDE the container, because it is not reachable from
  #     this host: that is the property being asserted, so reaching it from outside
  #     would be the failure.
  #   * `/metrics` must NOT answer on the two published listeners. This is the
  #     assertion that holds a security decision in place: a metrics endpoint on
  #     the guest portal publishes this venue's request counts, error rates and
  #     uptime to every unauthenticated device on the SSID, and on the console it
  #     puts a scrape surface on an operator's browser origin. A future edit that
  #     mounted it there would pass every Go test in the repository and be caught
  #     here.
  if [[ -n "${compose_err}" ]]; then
    skip "liveness, readiness and metrics: compose could not be read, reported above"
  elif [[ -z "${cid}" ]]; then
    skip "liveness, readiness and metrics: the foyer service is not running, reported above"
  elif ! command -v curl >/dev/null 2>&1; then
    skip "curl is not available; the control plane's own endpoints are unverified"
  else
    live="$(bounded curl -sk -o /dev/null -w '%{http_code}' --max-time 5 https://127.0.0.1:8443/healthz 2>/dev/null || true)"
    if [[ "${live}" == "200" ]]; then
      ok "the control plane answers /healthz on the console: the process is alive"
    else
      drift "the control plane answered ${live:-nothing} on /healthz: the process is not serving the console, and nothing below about readiness or metrics can be believed"
    fi

    # The body, not just the code. A readiness answer of 200 with `"ready":false`
    # would be a probe lying, and this is the only place that would be visible.
    ready_body="$(bounded curl -sk --max-time 5 https://127.0.0.1:8443/readyz 2>/dev/null || true)"
    if grep -q '"ready":true' <<<"${ready_body}"; then
      ok "the control plane reports itself ready: PostgreSQL is reachable and the stored network settings are in force"
    else
      drift "the control plane is not ready — it cannot serve guests. ${ready_body:-no answer on /readyz}. Check that postgres is up and that the migration ran; deploy/compose.yaml's healthcheck probes this same endpoint, so the container will also read (unhealthy)"
    fi

    # **From the host, with the policy client certificate — not from inside the
    # container.** The first line is checked because a scrape that returns
    # something unparseable collects nothing while looking like it works.
    #
    # From inside, the obvious probe
    #
    #   docker exec <cid> wget -q -O- http://127.0.0.1:8080/metrics
    #
    # answers `HTTP/1.0 400 Bad Request` — the Policy API listener is mutual TLS,
    # so a plaintext request is refused, and BusyBox `wget` in the control-plane
    # image cannot present a client certificate (there is no `curl` in it either).
    # Such a check could never pass, on any host, running any code: a check that
    # cannot reach its subject reports the subject as broken.
    #
    # The host has `curl`, and the pair is at `${SECRETS}/policy-tls/`, readable
    # only by the container's uid — hence the same door the console-certificate
    # checks below use.
    exposition="$(bounded curl -s --max-time 5 \
      --cert "${SECRETS}/policy-tls/policy.crt" --key "${SECRETS}/policy-tls/policy.key" \
      "https://127.0.0.1:8080/metrics" 2>/dev/null || true)"
    if head -c 64 <<<"${exposition}" | grep -q '^# HELP'; then
      ok "/metrics answers on the Policy API listener, the one compose.yaml does not publish, and the exposition is well formed"
    else
      # **Unverifiable from here, and that is a third answer, not a second.**
      #
      # compose.yaml deliberately does not publish 8080 — that is the same fact
      # the "port 8080 is not published" check above asserts, and it is why this
      # listener is the right home for the metrics. So there is no host-side
      # socket to ask, and from inside the control plane container the only
      # tools are BusyBox `wget` and no `curl`, neither of which can present the
      # client certificate mutual TLS requires.
      #
      # Calling that a DRIFT would be a red check that is not a defect, naming a
      # broken endpoint that is not broken. **Unknown is not the same as absent**,
      # and a check that cannot reach its subject says so rather than reporting
      # the subject.
      #
      # What would make it checkable is a scraper inside the deployment holding
      # the client certificate — or a `foyerctl` subcommand that writes the
      # exposition to stdout, which needs no published port at all and is the
      # obvious answer if a host operator ever has to answer "is the metrics
      # endpoint healthy" without standing up a scraper.
      skip "/metrics on the Policy API listener — UNVERIFIED from this host, and this is not a finding: 8080 is deliberately unpublished (the check above asserts it), so there is no socket here to ask, and the control-plane image carries no tool that can present the client certificate mutual TLS requires. Answer it with a scraper inside the deployment."
    fi

    # **The assertion is "no metrics exposition here", not "no 200 here".**
    #
    # `/metrics` on the admin console answers 200 — with the console's own
    # `index.html`. Both single-page consoles answer every unknown path with the
    # app shell, so a status-code test is red on **every correct deployment**: it
    # cannot tell an exposed exposition from an HTML page that happens to be 200. That is a
    # red check that is not a defect, which is the kind that teaches an operator
    # to ignore the tool.
    #
    # A Prometheus exposition begins `# HELP`. So the body is what is asked about,
    # and the status is reported beside it rather than deciding anything.
    for port_path in "80:/metrics on the guest portal" "8443:/metrics on the admin console"; do
      port="${port_path%%:*}"; what="${port_path#*:}"
      if [[ "${port}" == "8443" ]]; then
        body="$(bounded curl -sk --max-time 5 "https://127.0.0.1:${port}/metrics" 2>/dev/null || true)"
      else
        body="$(bounded curl -s --max-time 5 "http://127.0.0.1:${port}/metrics" 2>/dev/null || true)"
      fi
      if head -c 64 <<<"${body}" | grep -q '^# HELP'; then
        drift "${what} served a Prometheus exposition. Metrics carry this deployment's request counts, error rates and uptime, and on a guest-reachable listener that is a disclosure to every unauthenticated device on the SSID. It belongs on the Policy API alone, which compose.yaml does not publish."
      elif grep -qi '^content-type:.*text/html' <<<"${body}"; then
        ok "${what} is not exposed — the console's own HTML answered, which is its catch-all for unknown paths and carries no measurement"
      elif [[ -z "${body}" ]]; then
        ok "${what} is not exposed (no answer)"
      else
        ok "${what} is not exposed (no exposition)"
      fi
    done
  fi

  # 4b-ii. The FreeRADIUS the guests are actually authenticated by.
  #
  # `deploy/Dockerfile.freeradius` pins an exact version and refuses to fall back;
  # this asks the running container whether that survived. It is not redundant
  # with the build: an image built weeks ago from a different pin, or one pulled
  # rather than built, answers here and nowhere else.
  #
  # The expected version is READ OUT OF THE DOCKERFILE, not written here. A
  # second copy of "3.2.10-1" in this file is a second source of truth for the
  # pin, and it would go on agreeing with itself the day somebody changed the
  # real one.
  frcid="$("${SRC}/deploy/compose.sh" ps -q freeradius 2>/dev/null)"
  if [[ -n "${frcid}" ]]; then
    want_pin="$(sed -n 's/^ARG FREERADIUS_VERSION=\(.*\)$/\1/p' "${SRC}/deploy/Dockerfile.freeradius" | head -1)"
    want_upstream="${want_pin%-*}"
    if [[ -z "${want_upstream}" ]]; then
      drift "deploy/Dockerfile.freeradius declares no ARG FREERADIUS_VERSION, so nothing here knows what to expect"
    else
      got="$(docker exec "${frcid}" /usr/sbin/freeradius -v 2>&1 \
        | sed -n 's/.*FreeRADIUS Version \([0-9][0-9.]*\).*/\1/p' | head -1)"
      case "${got}" in
        "")               drift "the running freeradius container did not answer \`freeradius -v\`" ;;
        "${want_upstream}") ok "freeradius reports ${got}, the version deploy/Dockerfile.freeradius pins" ;;
        *)                drift "freeradius reports ${got}, but deploy/Dockerfile.freeradius pins ${want_pin}. The image serving guests is not the image this repository describes" ;;
      esac
    fi
  fi

  # 4c. The one-off `docker run` docs/rules/deployment.md names. Anything running on this
  #     host that is not part of this compose project was started by a human.
  strays="$(docker ps --format '{{.Names}}\t{{.Label "com.docker.compose.project"}}' 2>/dev/null \
    | awk -F'\t' '$2 != "foyer" {print $1}')"
  if [[ -z "${strays}" ]]; then
    ok "no container running outside the foyer compose project"
  else
    while IFS= read -r c; do
      [[ -z "${c}" ]] && continue
      drift "container ${c} is running and is not part of this repository's compose project"
    done <<< "${strays}"
  fi
fi

# ── 5. Secrets ───────────────────────────────────────────────────────────────
#
# Their VALUES are never read here and never printed. What is checked is that
# they exist where docs/rules/security.md §7 requires and that nothing widened
# their modes.
head_ "Secrets"
check_mode() {
  local path="$1" want="$2" got
  # `stat` on a path inside a 0700 directory this user does not own prints
  # nothing and exits non-zero, which a plain `||` chain would turn into an empty
  # `got` and then into "is mode , expected 400" — a drift with no object in it.
  # `can_see_secrets` is defined below; it is only called at run time.
  if can_see_secrets; then
    got="$(sudo -n stat -c '%a' "${path}" 2>/dev/null || sudo -n stat -f '%Lp' "${path}" 2>/dev/null || true)"
  else
    got="$(stat -c '%a' "${path}" 2>/dev/null || stat -f '%Lp' "${path}" 2>/dev/null || true)"
  fi
  if [[ -z "${got}" ]]; then
    # Not a mode and not a reason. Say which of the two it is.
    case "$(path_state "${path}")" in
      absent)  drift "${path} does not exist" ;;
      *)       skip "${path} — $(unreadable_note "${path}")" ;;
    esac
  elif [[ "${got}" == "${want}" ]]; then
    ok "${path} is mode ${got}"
  else
    drift "${path} is mode ${got}, expected ${want}"
  fi
}

# ── can this script even see into the secrets? ────────────────────────────────
#
# `first-boot.sh` makes the TLS directories 0700 **owned by the container's uid**
# (10001), and that is correct: `secrets/raddb` holds FreeRADIUS's database
# password and, with EAP-TLS, its private key. So the deploy user — not root, and
# deliberately not in the `docker` group for this — cannot list them. `deploy/sync.sh`
# runs as that user and `verify.sh` runs as that user, and `[[ -s path ]]` inside
# an unreadable directory answers **false for a file that is there**.
#
# Without the handling below, a healthy host — a console serving HTTPS on its own
# generated certificate, FreeRADIUS holding a generation — reports
#
#   DRIFT /opt/foyer/secrets/admin-tls/admin.crt does not exist
#
# and a handful more, all of them the check being unable to see through a 0700
# door. **A red check that is not a defect is worse than a missing one**, because
# the operator learns to ignore the tool, and then the run that matters is the one
# they skip.
#
# So: ask with `sudo` when this user has it, and when it does not, say **the check
# could not be made** rather than reporting a file as missing. Three states, not
# two.
#
# `sudo -n` never prompts. A host without passwordless sudo for the deploy user
# falls through to the plain test and then to `unreadable`, which is the honest
# answer rather than a hang.
can_see_secrets() { sudo -n test -d "${SECRETS}" >/dev/null 2>&1; }

# path_state echoes `present`, `absent` or `unreadable`. `absent` is only ever
# returned when the parent directory could actually be listed, so "I could not
# look" never becomes "it is not there".
path_state() {
  local path="$1" dir
  dir="$(dirname "${path}")"
  if can_see_secrets; then
    if sudo -n test -s "${path}" >/dev/null 2>&1; then echo present; else echo absent; fi
    return
  fi
  if [[ ! -r "${dir}" ]]; then
    echo unreadable
  elif [[ -s "${path}" ]]; then
    echo present
  else
    echo absent
  fi
}

# The one sentence both call sites need, so the two cannot drift apart in tone:
# a missing file and a file this user may not read are different findings.
unreadable_note() {
  printf '%s\n' "this user cannot read $(dirname "$1") — it is 0700 owned by the container's uid, which is correct. Re-run as a user that can, or with sudo: the file's absence is UNKNOWN, not established"
}
# check_private asserts the PROPERTY rather than the number: no group bit, no
# other bit. It exists for one file — the console's private key — and the reason
# it is not `check_mode … 600` is that a constraint should not be restated away
# from its authority. The exact mode that key is written with belongs to the code that writes
# it, which is `internal/tlsca` and not this file; pinning 0600 here would go on
# agreeing with itself the day that code chose 0400, and disagreeing the day it
# chose 0400 correctly. What this deployment actually requires is that nothing but
# the owner can read it, and that is what is asserted.
check_private() {
  local path="$1" got
  # Same blindness as `check_mode`, and the same remedy. `-e` and `stat` inside a
  # 0700 directory this user does not own both fail, and this check is about a
  # **private key** — so a blind run would report "does not exist" for a key that
  # is there, on a host where the console is serving with it.
  if can_see_secrets; then
    got="$(sudo -n stat -c '%a' "${path}" 2>/dev/null || sudo -n stat -f '%Lp' "${path}" 2>/dev/null || true)"
  else
    got="$(stat -c '%a' "${path}" 2>/dev/null || stat -f '%Lp' "${path}" 2>/dev/null || true)"
  fi
  if [[ -z "${got}" ]]; then
    case "$(path_state "${path}")" in
      absent) drift "${path} does not exist" ;;
      *)      skip "${path} — $(unreadable_note "${path}")" ;;
    esac
  elif [[ "${got: -2}" == "00" ]]; then
    ok "${path} is mode ${got} — no group or other access"
  else
    drift "${path} is mode ${got}: it is readable beyond its owner, and it is a private key"
  fi
}

# The console certificate is inside a 0700 directory, so reading its *contents*
# needs the same privilege as seeing that it exists. Without this, `openssl x509`
# fails on a perfectly good certificate and the run reports that it does not
# parse, on a file the control plane has already loaded and logged.
read_secret() {
  if can_see_secrets; then sudo -n "$@"; else "$@"; fi
}

check_mode "${SECRETS}" 700
check_mode "${SECRETS}/foyer.env" 600
check_mode "${SECRETS}/postgres_password" 600
# 0400, not 0600, and owned by the container's uid rather than the deploy user.
#
# Compose bind-mounts a secret with the host's ownership, and the control plane is
# not root, so a file only the deploy user could read would stop every service
# that needs it. The directory above is 0700 and is what keeps these private.
check_mode "${SECRETS}/kek" 400
check_mode "${SECRETS}/database_url" 400
# The superuser's URL (migrate only) and the two login passwords (migration 00019).
check_mode "${SECRETS}/owner_database_url" 400
check_mode "${SECRETS}/app_db_password" 400
check_mode "${SECRETS}/radius_db_password" 400
# The maintenance roles' password and URL files (migration 00021).
check_mode "${SECRETS}/reaper_db_password" 400
check_mode "${SECRETS}/retention_db_password" 400
check_mode "${SECRETS}/reaper_database_url" 400
check_mode "${SECRETS}/retention_database_url" 400
# The maintenance door's token (internal/jobs/remote.go).
check_mode "${SECRETS}/maintenance_token" 400

# None of these may be an environment variable on the running container.
# `docker inspect` and /proc/<pid>/environ are readable by anything that can reach
# the daemon or the namespace, and FOYER_KEK unseals every enveloped column in the
# database. They arrive as docker secrets; the _FILE form is what compose sets.
foyer_cid="$("${SRC}/deploy/compose.sh" ps -q foyer 2>/dev/null)"
if [[ -n "${foyer_cid}" ]]; then
  env_dump="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "${foyer_cid}" 2>/dev/null)"
  leaked=0
  while IFS= read -r line; do
    case "${line}" in
      # FOYER_POSTGRES_PASSWORD would be the superuser's password in the control
      # plane's environment. The maintenance jobs' two URLs carry their role's
      # password inline, exactly as FOYER_DATABASE_URL does.
      FOYER_KEK=*|FOYER_DATABASE_URL=*|FOYER_POSTGRES_PASSWORD=*|FOYER_REAPER_DATABASE_URL=*|FOYER_RETENTION_DATABASE_URL=*|FOYER_MAINTENANCE_TOKEN=*)
        drift "${line%%=*} is an environment variable on the running control plane; it should be a docker secret and a ${line%%=*}_FILE path"
        leaked=1
        ;;
    esac
  done <<< "${env_dump}"
  [[ ${leaked} -eq 0 ]] && ok "no unsealed secret in the control plane's environment"

  # **The control plane holds no login it does not need.** The stale-session
  # reaper's and the retention role's logins are the
  # `maintenance` service's, and FreeRADIUS's database password is FreeRADIUS's:
  # the generated tree names it as $ENV{FOYER_RADIUS_DB_PASSWORD}. A control plane
  # started from an older compose.yaml still has them, as a _FILE variable and as a
  # mounted secret; both are looked for.
  mounts="$(docker inspect -f '{{range .Mounts}}{{println .Destination}}{{end}}' "${foyer_cid}" 2>/dev/null)"
  held=0
  for v in FOYER_REAPER_DATABASE_URL_FILE FOYER_RETENTION_DATABASE_URL_FILE FOYER_POSTGRES_PASSWORD_FILE; do
    if grep -q "^${v}=" <<< "${env_dump}"; then
      drift "the running control plane is given ${v}: that login belongs to the maintenance container or to FreeRADIUS, not to the control plane. Run deploy/up.sh"
      held=1
    fi
  done
  for s in reaper_database_url retention_database_url radius_db_password reaper_db_password retention_db_password; do
    if grep -qx "/run/secrets/${s}" <<< "${mounts}"; then
      drift "the running control plane has the ${s} secret mounted: it was started from an older compose.yaml that gave it logins it does not need. Run deploy/up.sh"
      held=1
    fi
  done
  [[ ${held} -eq 0 ]] && ok "the control plane holds neither maintenance login nor FreeRADIUS's database password"
  if grep -q "^FOYER_MAINTENANCE_URL=" <<< "${env_dump}"; then
    ok "the control plane asks the maintenance container for erasure (FOYER_MAINTENANCE_URL)"
  else
    drift "the running control plane has no FOYER_MAINTENANCE_URL: erasure cannot reach the retention login. Run deploy/up.sh"
  fi
fi

# And the maintenance container was HANDED the two logins, and not the KEK.
# Without the logins it still starts — a pool that does not open is a job that
# fails every cycle (cmd/foyer/maintenance.go) — so section 5c asks whether they ran.
maint_cid="$("${SRC}/deploy/compose.sh" ps -q maintenance 2>/dev/null)"
if [[ -z "${maint_cid}" ]]; then
  drift "the maintenance service is not running: no stale session is closed, no retention period is applied, and no erasure can complete. Run deploy/up.sh"
else
  maint_env="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "${maint_cid}" 2>/dev/null)"
  for v in FOYER_REAPER_DATABASE_URL_FILE FOYER_RETENTION_DATABASE_URL_FILE; do
    if grep -q "^${v}=" <<< "${maint_env}"; then
      ok "the maintenance container is given ${v}"
    else
      drift "the running maintenance container has no ${v}, so its job cannot log in as its own role. Run deploy/up.sh"
    fi
  done
  if grep -qE "^FOYER_KEK(_FILE)?=" <<< "${maint_env}"; then
    drift "the maintenance container is given the KEK; it unseals nothing and must not hold it"
  else
    ok "the maintenance container holds no KEK"
  fi
fi
check_mode "${SECRETS}/policy-tls/policy.key" 400
check_mode "${SECRETS}/policy-tls/ca.key" 400
# 0440 root:1812, not 0400: radiusd reads it as `freerad`, through the image's
# foyer-radius-tls group. deploy/issue-radius-cert.sh sets it.
check_mode "${SECRETS}/radius-tls/radius.key" 440

# The mTLS boundary docs/rules/security.md §2 requires. Both halves have to be present or the
# data plane does not come up: rlm_rest opens ca_info_file at instantiation, and
# cmd/foyer refuses to serve a non-loopback Policy API with TLS and no client CA.
#
# Asserted as three files rather than as "TLS is configured", because "TLS is
# configured" is also true of a boundary that is encrypted and unauthenticated.
for f in "${SECRETS}/radius-tls/radius.crt" "${SECRETS}/radius-tls/ca.crt" "${SECRETS}/policy-tls/policy.crt"; do
  case "$(path_state "${f}")" in
    present) ok "$(basename "${f}") is present" ;;
    absent)  drift "${f} is missing — the rlm_rest boundary cannot be mutual without it. Run deploy/issue-radius-cert.sh" ;;
    *)       skip "$(basename "${f}") — $(unreadable_note "${f}")" ;;
  esac
done

# The CA key must NOT be where FreeRADIUS can read it. That container runs as
# root and mounts ${SECRETS}/radius-tls; a ca.key in there is the key that mints
# client certificates for the Policy API, sitting inside the data plane.
case "$(path_state "${SECRETS}/radius-tls/ca.key")" in
  absent)
    ok "the CA key is not inside the directory FreeRADIUS mounts" ;;
  present)
    drift "${SECRETS}/radius-tls/ca.key exists and is mounted into the FreeRADIUS container — that is the key that issues client certificates for the Policy API" ;;
  *)
    # **This one fails in the dangerous direction.** `-e` inside a directory this
    # user cannot read answers false, so a naive form reports "the CA key is not
    # there" on a host where it cannot see — a security check passing because it
    # is blind. An unknown is not a pass.
    skip "the CA key's absence — $(unreadable_note "${SECRETS}/radius-tls/ca.key")" ;;
esac

# A secret inside the shipped tree is the failure docs/rules/deployment.md warns
# about by name: `.env` with a real value in a commit is a violation of
# docs/rules/security.md §7, not compliance with this directory.
stray_env="$(find "${SRC}" -maxdepth 3 -name '.env' -o -maxdepth 3 -name '*.env' 2>/dev/null | head -5)"
if [[ -z "${stray_env}" ]]; then
  ok "no environment file inside the shipped tree"
else
  while IFS= read -r f; do
    [[ -z "${f}" ]] && continue
    drift "${f} is inside the shipped tree; secrets live in ${SECRETS} and in no repository"
  done <<< "${stray_env}"
fi

# ── 5a. Can this installation be brought back? ───────────────────────────────
#
# **The question is "restorable in principle", not "restores correctly".** The
# second one costs a second database, and verify.sh runs on a small host with a
# live venue on it; it is `deploy/restore.sh --dry-run` plus a free
# afternoon, and it is a separate thing. What can be answered here, cheaply and
# on the machine that would actually be restored, is whether **every piece a
# restore needs is present**:
#
#   * the schedule exists, so there is a next backup rather than a last one
#   * a dump exists and is recent enough to be worth anything
#   * that dump is intact — its own sha256 against its manifest
#   * **a KEK exists whose fingerprint is the one the dump was taken under**
#
# The last is the one that matters and the one nothing else in this script
# covers. `internal/store/envelope.go` seals every RADIUS shared secret, every
# controller credential and every NT hash under FOYER_KEK, and the key is
# deliberately not in the database it protects — so a dump whose KEK is not on
# this host is a dump that restores a venue nobody can decrypt a single secret
# out of, and **that installation looks entirely healthy**: the console serves,
# the portal serves, vouchers validate, and the failure appears the first time a
# controller registers or an operator rotates a secret. The check below is
# cheap, it reads no secret's value, and it is the difference between learning
# that on a Tuesday and learning it during an outage.
head_ "Backups"

# The naming, the fingerprint and the listing are deploy/backups.sh, shared with
# backup.sh and restore.sh, because a KEK fingerprint computed two ways is a
# fingerprint that eventually disagrees with itself — and the disagreement would
# be reported as "this backup does not match your KEK" on a host where nothing
# is wrong. images.sh, secretdirs.sh and consolenames.sh exist for the same
# reason.
if [[ -r "${SRC}/deploy/backups.sh" ]]; then
  # shellcheck source=backups.sh
  . "${SRC}/deploy/backups.sh"
else
  drift "${SRC}/deploy/backups.sh is missing, so nothing here can say what a backup is or how one is fingerprinted"
  # Nothing below can be asked. Saying so beats skipping silently: a run that
  # reports 87 checks and says nothing about backups reads as "backups: fine".
  skip "every backup check — ${SRC}/deploy/backups.sh is not on this host"
fi

if [[ -r "${SRC}/deploy/backups.sh" ]]; then
  # How old a backup may be before it stops being one. Two days, because the
  # timer is daily and a single missed run is a gap, while a week is seven days
  # of venue data that no longer exists anywhere.
  max_age_h="${FOYER_BACKUP_MAX_AGE_HOURS:-48}"

  if [[ ! -d "${BACKUPS}" ]]; then
    drift "${BACKUPS} does not exist: this installation has never been backed up, so nothing on it can be recovered if this disk fails. Run: sudo install -d -m 0700 ${BACKUPS} && ${SRC}/deploy/backup.sh"
  else
    check_mode "${BACKUPS}" 700
    check_mode "${SECRETS}/kek" 400   # already checked above; restated as the
                                      # other half of the pair this section is about

    # The schedule. A backup directory with nothing writing to it is the state
    # this whole section exists to catch, and it is invisible from the files:
    # every dump in there is a fine dump.
    if command -v systemctl >/dev/null 2>&1; then
      timer_state="$(bounded systemctl is-active foyer-backup.timer 2>/dev/null || true)"
      case "${timer_state}" in
        active) ok "foyer-backup.timer is active, so a backup happens without anybody running one" ;;
        "")     drift "foyer-backup.timer is not installed, so the only backups are the ones somebody ran by hand. Run: sudo ${SRC}/deploy/backup.sh --install" ;;
        *)      drift "foyer-backup.timer is ${timer_state}, not active, so no backup is being taken. Run: sudo systemctl restart foyer-backup.timer" ;;
      esac
      if bounded systemctl is-enabled foyer-backup.timer >/dev/null 2>&1; then
        ok "foyer-backup.timer is enabled, so it comes back after a reboot"
      else
        drift "foyer-backup.timer is not enabled: it will not run after this host reboots. Run: sudo systemctl enable foyer-backup.timer"
      fi
      # When it last ran, and whether that run succeeded. A timer that is
      # active and failing every night is the worst shape a schedule can take:
      # everything reports fine and nothing is happening.
      # Asked as two separate properties rather than `--value` over both: the
      # order systemd prints values in is not the order they were asked for, and
      # a check that depends on it is a check that will read a failure as a
      # success. `ExecMainStatus` alone is the exit code, and 0 is the only
      # value that means the dump landed.
      last_status="$(bounded systemctl show foyer-backup.service -p ExecMainStatus --value 2>/dev/null || true)"
      case "${last_status}" in
        "")  skip "whether the last foyer-backup.service run succeeded — it has never run on this host" ;;
        0)   ok "the last foyer-backup.service run exited 0" ;;
        *)   drift "the last foyer-backup.service run exited ${last_status}; a backup that fails every night is not a backup. Run ${SRC}/deploy/backup.sh by hand and read why" ;;
      esac
    else
      skip "the backup schedule — systemctl is not on this machine"
    fi

    newest="$(foyer_backups_newest_first | head -1)"
    if [[ -z "${newest}" ]]; then
      drift "${BACKUPS} holds no dump: nothing here can be restored. Run: ${SRC}/deploy/backup.sh"
    else
      new_name="$(basename "${newest}")"
      new_age_h=$(( ( $(date +%s) - $(foyer_mtime "${newest}") ) / 3600 ))
      if (( new_age_h > max_age_h )); then
        drift "the newest backup, ${new_name}, is ${new_age_h}h old (limit ${max_age_h}h): the timer is not producing them. Run: ${SRC}/deploy/backup.sh by hand, and read why"
      else
        ok "the newest backup is ${new_name}, ${new_age_h}h old"
      fi

      # ── the pieces a restore needs, one by one ───────────────────────────
      #
      # Each of these names its object, and each is a state a restore actually
      # fails in. They are separate checks rather than one "backups look fine"
      # because a single boolean over four files tells an operator nothing about
      # which file to go and look at.

      # 1. the manifest. Without it nothing knows which KEK opens this dump.
      new_manifest="${newest%.dump}.manifest"
      case "$(path_state "${new_manifest}")" in
        present) ok "$(basename "${new_manifest}") is present" ;;
        absent)  drift "${new_manifest} is missing: nothing can say which KEK opens ${new_name}, and deploy/restore.sh refuses a dump with no manifest rather than guess" ;;
        *)       skip "${new_manifest} — $(unreadable_note "${new_manifest}")" ;;
      esac

      # 2. the dump's own checksum. Read through the same door as the KEK.
      if [[ "$(path_state "${new_manifest}")" == "present" ]]; then
        if want_sha="$(read_secret awk '/^sha256:/ {print $2; exit}' "${new_manifest}" 2>/dev/null)"; then
          if got_sha="$(read_secret foyer_sha256 "${newest}" 2>/dev/null)"; then
            if [[ -n "${want_sha}" && "${got_sha}" == "${want_sha}" ]]; then
              ok "${new_name} matches the sha256 in its manifest, so the file on disk is the one that was written"
            else
              drift "${new_name} does not match the sha256 in its manifest: the file has changed since it was written, or the disk is failing, and a restore of it would be worse than no restore"
            fi
          else
            skip "${new_name}'s checksum — $(unreadable_note "${newest}")"
          fi
        else
          skip "${new_name}'s checksum — its manifest names no sha256"
        fi

        # 3. **the KEK.** The check this whole section is arranged around.
        want_kek="$(read_secret awk '/^kek-sha256:/ {print $2; exit}' "${new_manifest}" 2>/dev/null)"
        case "$(path_state "${SECRETS}/kek")" in
          absent)
            drift "${SECRETS}/kek is missing and ${new_name} was taken under a KEK hashing to ${want_kek}: that dump restores a database whose shared secrets, controller credentials and NT hashes are all ciphertext. This host cannot be recovered from its own backups"
            ;;
          unreadable)
            skip "whether this host's KEK opens ${new_name} — $(unreadable_note "${SECRETS}/kek")"
            ;;
 present)
            if [[ "${want_kek}" == "none" ]]; then
              drift "${new_name} records no KEK fingerprint: it was taken on a host with no KEK, so nothing can open its sealed columns. See deploy/README.md, the KEK"
            elif have_kek="$(read_secret foyer_kek_fingerprint "${SECRETS}/kek" 2>/dev/null)"; then
              if [[ "${have_kek}" == "${want_kek}" ]]; then
                ok "this host's KEK (sha256 ${have_kek}) is the one ${new_name} was taken under, so that dump is restorable here"
              else
                drift "this host's KEK hashes to ${have_kek} and ${new_name} was taken under ${want_kek}: restoring it would produce an installation that starts and cannot decrypt a single sealed column. Either the KEK was rotated without re-sealing, or this backup belongs to another install"
              fi
            else
              skip "whether this host's KEK opens ${new_name} — the KEK could not be read"
            fi
            ;;
        esac
      fi

      # 4. the inventory, so the count is on the record rather than in a
      #    directory listing nobody reads. Not a pass/fail: how many are kept is
      #    FOYER_BACKUP_KEEP's business, and this script does not own that number.
      kept_n="$(foyer_backups_newest_first | grep -c . || true)"
      ok "${kept_n} backup(s) in ${BACKUPS}, newest ${new_name}"
    fi
  fi
fi


# ── 5b. Who logs in to PostgreSQL ────────────────────────────────────────────
#
# FreeRADIUS and the control plane must not log in as the superuser: a privilege
# boundary made of `SET ROLE` is one `RESET ROLE` undoes. Migration 00019 makes
# foyer_app and foyer_radius login roles and cmd/foyer refuses anything more; this
# section asks the RUNNING database whether that is what actually happened,
# because a compose file can say foyer_app while a container started last month
# still holds the superuser's URL.
#
# Asked over the postgres container's local socket, which the official image
# trusts — the same path upgrade.sh's pg_dump uses. Logging in as foyer_app or
# foyer_radius that way needs no password and tests exactly the role's
# privileges, which are the question; no secret is read or printed here.
#
# The superuser role's name is READ OUT OF compose.yaml, for the pin-in-the-Dockerfile
# reason: a second copy of it here would agree with itself the day it changed.
head_ "Database logins"
pg_owner="$(sed -n 's/^[[:space:]]*POSTGRES_USER:[[:space:]]*//p' "${COMPOSE}" 2>/dev/null | head -1)"
pg_cid="$("${SRC}/deploy/compose.sh" ps -q postgres 2>/dev/null)"
psql_as() {
  local user="$1"; shift
  docker exec -i "${pg_cid}" psql -X -q -w -At -v ON_ERROR_STOP=1 -U "${user}" -d foyer "$@" 2>&1
}
if [[ -z "${pg_owner}" ]]; then
  drift "${COMPOSE} sets no POSTGRES_USER, so nothing here knows which role is the superuser"
elif [[ -z "${pg_cid}" ]]; then
  drift "the postgres service is not running, so nothing here can say who logs in to it"
else
  # The roles themselves: login, a password, and none of the attributes that
  # make RESET ROLE — or anything else — worth doing.
  # foyer_reaper and foyer_retention since migration 00021: the maintenance
  # jobs log in as them, because foyer_app may not SET ROLE into either.
  for role in foyer_app foyer_radius foyer_reaper foyer_retention; do
    attrs="$(psql_as "${pg_owner}" -c "SELECT concat(rolcanlogin::int, rolsuper::int, rolbypassrls::int, rolcreaterole::int, rolcreatedb::int, (rolpassword IS NOT NULL)::int) FROM pg_authid WHERE rolname = '${role}'")"
    case "${attrs}" in
      100001) ok "${role} is a login role with a password, and holds none of SUPERUSER, BYPASSRLS, CREATEROLE, CREATEDB" ;;
      "")     drift "${role} does not exist in the database — migration 00001 never ran here" ;;
      0*)     drift "${role} is NOLOGIN: migration 00019 (foyer_app, foyer_radius) or 00021 (foyer_reaper, foyer_retention) has not run, so that process cannot be logging in as its own role" ;;
      1????0) drift "${role} has no password: foyerctl migrate ran without its FOYER_*_DB_PASSWORD_FILE, so nothing can log in as it" ;;
      *)      drift "${role} has attributes ${attrs} (login, super, bypassrls, createrole, createdb, password) — it holds more than its grants" ;;
    esac
  done

  # Who is actually connected. The two roles must each have a session — the
  # control plane's pool and FreeRADIUS's rlm_sql pool both hold connections open
  # — and nothing but this script may be connected as a superuser.
  supers="$(psql_as "${pg_owner}" -c "SELECT string_agg(DISTINCT a.usename || ' from ' || coalesce(host(a.client_addr), 'the local socket'), ', ') FROM pg_stat_activity a JOIN pg_roles r ON r.rolname = a.usename WHERE a.datname = 'foyer' AND a.backend_type = 'client backend' AND r.rolsuper AND a.pid <> pg_backend_pid()")"
  if [[ -z "${supers}" ]]; then
    ok "no session on the foyer database is a superuser, other than this check's own"
  else
    drift "superuser session(s) on the foyer database: ${supers}. The control plane and FreeRADIUS must log in as foyer_app and foyer_radius; a running container with an old database_url is the usual cause. Run deploy/up.sh"
  fi
  for role in foyer_app foyer_radius; do
    n="$(psql_as "${pg_owner}" -c "SELECT count(*) FROM pg_stat_activity WHERE datname = 'foyer' AND usename = '${role}'")"
    if [[ "${n}" =~ ^[1-9][0-9]*$ ]]; then
      ok "${n} session(s) are logged in as ${role}"
    else
      drift "nothing is logged in as ${role} (got '${n}'): whatever should be is connecting as something else, or not at all"
    fi
  done

  # RESET ROLE from the application's login gains nothing.
  after_reset="$(psql_as foyer_app -c "RESET ROLE" -c "SELECT current_user || ' ' || rolsuper FROM pg_roles WHERE rolname = current_user")"
  if [[ "${after_reset}" == "foyer_app false" ]]; then
    ok "RESET ROLE from foyer_app is still foyer_app, not a superuser"
  else
    drift "RESET ROLE from foyer_app answered '${after_reset}', want 'foyer_app false'"
  fi
  set_owner="$(psql_as foyer_app -c "SET ROLE ${pg_owner}")"
  case "${set_owner}" in
    *"permission denied to set role"*) ok "foyer_app cannot SET ROLE ${pg_owner}" ;;
    *) drift "foyer_app tried SET ROLE ${pg_owner} and got '${set_owner}', not a permission refusal" ;;
  esac

  # FreeRADIUS's login is refused the operator and secret tables — each refusal
  # must NAME the table, as docs/rules/database.md requires.
  for t in operators operator_sessions shared_secrets controllers account_nt_hashes accounts; do
    out="$(psql_as foyer_radius -c "SELECT 1 FROM ${t} LIMIT 1")"
    case "${out}" in
      *"permission denied for table ${t}"*) ok "foyer_radius is refused ${t}" ;;
      *) drift "foyer_radius read ${t}, or failed for another reason: '${out}'" ;;
    esac
  done

  # Row security is forced wherever it is enabled, and the credential view's
  # owner is not a superuser (both migration 00019).
  unforced="$(psql_as "${pg_owner}" -c "SELECT string_agg(relname, ', ' ORDER BY relname) FROM pg_class WHERE relnamespace = 'public'::regnamespace AND relkind = 'r' AND relrowsecurity AND NOT relforcerowsecurity")"
  if [[ -z "${unforced}" ]]; then
    ok "every table with row security has it FORCED"
  else
    drift "row security is enabled and not forced on: ${unforced}"
  fi
  view_owner="$(psql_as "${pg_owner}" -c "SELECT r.rolname || ' ' || r.rolsuper FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner WHERE c.oid = 'radius_pap_verifiers'::regclass")"
  case "${view_owner}" in
    *" false") ok "radius_pap_verifiers is owned by ${view_owner% false}, not a superuser" ;;
    *) drift "radius_pap_verifiers is owned by '${view_owner}': the view FreeRADIUS reads runs with its owner's privileges" ;;
  esac

  # The two maintenance logins can do their one job and nothing next to it
  # (migrations 00002, 00020, 00021). Asked AS each role over the local socket, like
  # the foyer_app checks above; `WHERE false` inside a rolled-back transaction, so
  # PostgreSQL checks the privilege — it does so before it looks at a single row —
  # and nothing is written. Each refusal must name the table
  # (docs/rules/database.md): a statement that fails for another reason proves
  # nothing about grants. Unit and store tests cover these logins too; this is the
  # only place the running database is asked.
  priv() {
    local role="$1" sql="$2"
    psql_as "${role}" -c "BEGIN" -c "${sql}" -c "ROLLBACK"
  }
  expect_granted() {
    local role="$1" what="$2" sql="$3" out
    out="$(priv "${role}" "${sql}")"
    case "${out}" in
      *ERROR*|*error*|*denied*) drift "${role} cannot ${what}, which is its job: '${out}'" ;;
      *) ok "${role} can ${what}" ;;
    esac
  }
  expect_refused() {
    local role="$1" what="$2" table="$3" sql="$4" out
    out="$(priv "${role}" "${sql}")"
    case "${out}" in
      *"permission denied for table ${table}"*) ok "${role} is refused ${what}" ;;
      *) drift "${role} was not refused ${what} with a permission error on ${table}: '${out}'" ;;
    esac
  }
  expect_granted foyer_reaper "close a stale session in radacct" \
    "UPDATE radacct SET acctstoptime = acctstoptime, acctterminatecause = acctterminatecause, acctsessiontime = acctsessiontime, acctupdatetime = acctupdatetime WHERE false"
  expect_refused foyer_reaper "a byte counter in radacct" radacct \
    "UPDATE radacct SET acctinputoctets = acctinputoctets WHERE false"
  expect_refused foyer_reaper "DELETE on radacct" radacct "DELETE FROM radacct WHERE false"
  expect_granted foyer_retention "DELETE expired rows from radacct and radpostauth" \
    "DELETE FROM radacct WHERE false; DELETE FROM radpostauth WHERE false"
  expect_refused foyer_retention "UPDATE on radacct" radacct \
    "UPDATE radacct SET acctupdatetime = acctupdatetime WHERE false"
  expect_refused foyer_retention "INSERT into radpostauth" radpostauth \
    "INSERT INTO radpostauth (username) SELECT 'verify' WHERE false"
  expect_refused foyer_app "DELETE on radacct" radacct "DELETE FROM radacct WHERE false"

  # And the generated tree says foyer_radius. The login line only — the password
  # is on the next line of the same file and is never read here.
  if [[ -n "${frcid:-}" ]]; then
    login="$(docker exec "${frcid}" sed -n 's/^[[:space:]]*login[[:space:]]*=[[:space:]]*"\(.*\)".*/\1/p' /etc/raddb/mods-available/sql 2>/dev/null | head -1)"
    if [[ "${login}" == "foyer_radius" ]]; then
      ok "the running FreeRADIUS logs in to PostgreSQL as foyer_radius"
    else
      drift "the running FreeRADIUS's mods-available/sql logs in as '${login}', not foyer_radius. Press Apply on the console's Network screen, or restart the foyer service (deploy/up.sh): the control plane regenerates the tree at start"
    fi
  fi
fi

# ── 5c. The maintenance jobs ran on their own logins ─────────────────────────
#
# Grants are the half above. This is the other half: that the running maintenance
# container OPENED those pools and the jobs on them succeeded. A pool that does
# not open does not stop the process (cmd/foyer/maintenance.go says why), so the
# only trace of a reaper or retention sweep that has never worked is its log line
# — and a deployment where it never worked is exactly the one nobody reads the
# log of.
#
# The scheduler logs one line per run: `maintenance job ran job=<name>` or
# `maintenance job failed job=<name> … error=…`. The first run is about a second
# after start plus up to a tenth of the interval (30 s at the default five
# minutes), so an upgrade that calls this straight after the control plane turns
# healthy waits for it rather than reporting a job that has not had its turn.
head_ "Maintenance jobs"
if [[ -z "${maint_cid:-}" ]]; then
  drift "the maintenance service is not running, so nothing here can say whether its jobs ran"
else
  job_log=""
  for _ in $(seq 1 12); do
    job_log="$(docker logs "${maint_cid}" 2>&1 | grep -E "maintenance job (ran|failed) job=(stale-sessions|retention) |pool did not open|retention connection did not open" || true)"
    if grep -q "job=stale-sessions " <<< "${job_log}" && grep -q "job=retention " <<< "${job_log}"; then
      break
    fi
    sleep 5
  done
  if grep -qE "pool did not open|retention connection did not open" <<< "${job_log}"; then
    drift "the maintenance container logged that a maintenance login did not open: $(grep -E "pool did not open|retention connection did not open" <<< "${job_log}" | tail -1)"
  fi
  for job in stale-sessions:foyer_reaper retention:foyer_retention; do
    name="${job%%:*}" role="${job#*:}"
    last="$(grep -E "maintenance job (ran|failed) job=${name} " <<< "${job_log}" | tail -1)"
    case "${last}" in
      *"maintenance job ran"*)    ok "the ${name} job's last run succeeded, as ${role}" ;;
      *"maintenance job failed"*) drift "the ${name} job's last run FAILED (it runs as ${role}): ${last#*error=}" ;;
      *)                          drift "the ${name} job has not run in a minute of waiting; its first run is due within about 31 s of the maintenance container starting" ;;
    esac
  done
fi

# ── 6. What is listening ─────────────────────────────────────────────────────
#
# The published surface is a security decision (docs/rules/security.md §7), so it
# is verified rather than assumed: what must be published is, and what must not
# be is not.
head_ "Listeners"
if ! command -v ss >/dev/null 2>&1; then
  skip "ss is not available; the published surface is unverified"
else
  listening="$(ss -lntuH 2>/dev/null || true)"
  check_bind() {
    local port="$1" what="$2" want_loopback="$3"
    local wide
    wide="$(printf '%s\n' "${listening}" | awk -v p=":${port}" '$5 ~ p"$" {print $5}' | grep -vE '^(127\.0\.0\.1|\[::1\])' || true)"
    if [[ "${want_loopback}" == "yes" ]]; then
      if [[ -z "${wide}" ]]; then
        ok "${what} (${port}) is loopback only"
      else
        drift "${what} (${port}) is listening on ${wide//$'\n'/, } — it must be loopback only"
      fi
    else
      if [[ -n "${wide}" ]]; then
        ok "${what} (${port}) is published as intended"
      else
        drift "${what} (${port}) is not listening; the access points or the guests cannot reach it"
      fi
    fi
  }
  # The console is published on the LAN. A deployment that quietly went back to
  # binding loopback would be an operator who cannot open the console at all,
  # which is a drift worth naming rather than a safe default worth tolerating.
  #
  # Being on the port is one of two questions and this is the smaller one. What
  # answers there must be TLS, and its certificate must be valid for the address
  # an operator dials; both are asked in the console section below. A console
  # listening on 8443 and serving plaintext passes this check and is still wrong.
  check_bind 8443 "admin console" no
  check_bind 80 "guest portal" no
  check_bind 8880 "guest portal on UniFi's own port" no
  check_bind 1812 "RADIUS authentication" no
  check_bind 1813 "RADIUS accounting" no
  for p in 5432 8080 18120 11812 11813; do
    if printf '%s\n' "${listening}" | awk -v pp=":${p}" '$5 ~ pp"$"' | grep -qvE '^\S+\s+\S+\s+\S+\s+\S+\s+(127\.0\.0\.1|\[::1\])'; then
      drift "port ${p} is published on this host and compose.yaml says it must not be"
    else
      ok "port ${p} is not published"
    fi
  done
fi

# ── 6b. The guest portal's HTTPS listener: 443 and 8843, both ways ───────────
#
# compose.yaml publishes 443 and 8843 whether or not a certificate is configured,
# so the host's docker-proxy is listening on both either way and section 6 cannot
# tell the two states apart. What differs is what ANSWERS behind them, and there
# are exactly two correct answers (docs/portal-https.md):
#
#   no certificate   the plain portal on 80 serves guests itself, and nothing
#                    completes a TLS handshake on 443 or 8843 — HTTP is the
#                    default;
#   a certificate    the plain portal answers `302` to the https:// origin of the
#                    portal address, and 443 and 8843 present a certificate that is
#                    unexpired and covers that address's host.
#
# Which state applies is read from the plain portal's own answer, not from
# foyer.env: a certificate is uploaded in the console (Network → Portal
# certificate) and lives in the database, and an environment pair is only the
# installation's fallback. The probe is `/healthz`, because the plain listener
# redirects every GET while a certificate is served and `/` is the entry handler,
# which binds a browser to a device. Every other combination is drift, and one of
# them is the worst failure this section can see: guests redirected to an HTTPS
# origin nothing serves, which is a portal that no phone can reach.
head_ "Guest portal HTTPS"
if ! command -v curl >/dev/null 2>&1 || ! command -v openssl >/dev/null 2>&1; then
  skip "curl or openssl is not available; the portal's HTTPS listener is unverified"
else
  plain="$(bounded curl -s -o /dev/null -w '%{http_code} %{redirect_url}' --max-time 5 http://127.0.0.1:80/healthz 2>/dev/null || true)"
  plain_code="${plain%% *}"
  plain_target="${plain#* }"
  # presented <port> <sni> — the leaf the port presents, as PEM, or nothing.
  presented() {
    echo | bounded openssl s_client -connect "127.0.0.1:$1" -servername "$2" 2>/dev/null \
      | openssl x509 2>/dev/null || true
  }
  case "${plain_code}:${plain_target}" in
    30[1278]:https://*)
      host="${plain_target#https://}"
      host="${host%%/*}"
      host="${host%:*}"
      ok "the plain portal sends guests to ${plain_target%%\?*} — a portal certificate is configured"
      for port in 443 8843; do
        pem="$(presented "${port}" "${host}")"
        if [[ -z "${pem}" ]]; then
          drift "port ${port} completes no TLS handshake, and the plain portal is redirecting every guest to HTTPS: no phone can reach the portal. Check Network → Portal certificate in the console, and the control plane's log"
          continue
        fi
        if [[ "${host}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
          cover=(-checkip "${host}")
        else
          cover=(-checkhost "${host}")
        fi
        if ! openssl x509 -noout -checkend 0 <<< "${pem}" >/dev/null 2>&1; then
          drift "the portal certificate on ${port} has expired ($(openssl x509 -noout -enddate <<< "${pem}" | sed 's/^notAfter=//')). Nothing in Foyer renews it"
        elif openssl x509 -noout "${cover[@]}" <<< "${pem}" 2>/dev/null | grep -q "does match"; then
          ok "port ${port} serves an unexpired portal certificate that covers ${host}"
        else
          drift "the portal certificate on ${port} does not cover ${host}, the host guests are redirected to"
        fi
      done
      ;;
    2??:*|30[1278]:*)
      ok "the plain portal serves guests itself (${plain_code}) — no portal certificate, HTTP by default"
      for port in 443 8843; do
        if [[ -n "$(presented "${port}" localhost)" ]]; then
          drift "port ${port} presents a certificate while the plain portal is not redirecting to it: something other than Foyer's portal is answering there, or the two listeners disagree"
        else
          ok "port ${port} completes no TLS handshake, as it should with no portal certificate"
        fi
      done
      ;;
    *)
      drift "the plain portal on 80 answered '${plain}', neither a page nor a redirect, so which HTTPS state applies cannot be told"
      ;;
  esac
fi

# ── 7. The console is HTTPS, and the certificate on the wire is the one on disk ─
#
# HTTP for the portal by default, **HTTPS for the management console**, with a
# self-generated certificate. This section is what stops that from being a
# sentence in a README.
#
# **These checks are DRIFT, not skips, when the running image lacks the console
# certificate** — the machine genuinely does not match the repository then. Each
# one names an evaluable condition: the container's environment either carries the
# variable or does not, the file either exists or does not, the port either
# completes a handshake or does not. Nothing here is a skip whose reason expires
# and has to be remembered and deleted.
#
# The expected paths are READ OUT OF compose.yaml rather than written here, the
# same way the FreeRADIUS pin is read out of its Dockerfile: a second copy of
# `/run/foyer/admin-tls/admin.crt` in this file would go on agreeing with itself
# the day somebody moved it.
head_ "Console certificate"
want_cert="$(sed -n 's/^[[:space:]]*FOYER_ADMIN_TLS_CERT:[[:space:]]*//p' "${COMPOSE}" 2>/dev/null | head -1)"
want_key="$(sed -n 's/^[[:space:]]*FOYER_ADMIN_TLS_KEY:[[:space:]]*//p' "${COMPOSE}" 2>/dev/null | head -1)"
if [[ -z "${want_cert}" || -z "${want_key}" ]]; then
  drift "${COMPOSE} sets no FOYER_ADMIN_TLS_CERT and FOYER_ADMIN_TLS_KEY, so nothing here knows where the console's certificate should be. The console is always HTTPS; a compose file that does not say so is the drift"
else
  host_cert="${SECRETS}/admin-tls/$(basename "${want_cert}")"
  host_key="${SECRETS}/admin-tls/$(basename "${want_key}")"

  check_mode "${SECRETS}/admin-tls" 700
  check_private "${host_key}"

  # Does the RUNNING container carry what compose.yaml declares? A container
  # started before this configuration existed answers with an empty string.
  if [[ -n "${foyer_cid}" ]]; then
    got_cert="$(docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "${foyer_cid}" 2>/dev/null \
      | sed -n 's/^FOYER_ADMIN_TLS_CERT=//p' | head -1)"
    case "${got_cert}" in
      "")             drift "the running control plane has no FOYER_ADMIN_TLS_CERT in its environment: it was started from a compose.yaml that predates the console certificate. Place the current release in ${SRC} (sudo bash deploy/install.sh --secrets-only from its checkout) and run deploy/upgrade.sh" ;;
      "${want_cert}") ok "the running control plane was told ${got_cert}, which is what ${COMPOSE} declares" ;;
      *)              drift "the running control plane was told FOYER_ADMIN_TLS_CERT=${got_cert}, but ${COMPOSE} declares ${want_cert}" ;;
    esac
  fi

  cert_state="$(path_state "${host_cert}")"
  if [[ "${cert_state}" == "absent" ]]; then
    drift "${host_cert} does not exist. The control plane generates it on the first start that finds the directory empty, so an absent file means either that it has not started with this configuration, or that ${SECRETS}/admin-tls is not writable by the uid the container runs as"
  elif [[ "${cert_state}" != "present" ]]; then
    skip "the console certificate — $(unreadable_note "${host_cert}")"
  elif ! command -v openssl >/dev/null 2>&1; then
    skip "openssl is not installed; the console certificate cannot be inspected"
  else
    if ! read_secret openssl x509 -in "${host_cert}" -noout >/dev/null 2>&1; then
      drift "${host_cert} exists and does not parse as an X.509 certificate"
    else
      # Not expired — a PROPERTY, deliberately, and never the validity itself.
      # That parameter belongs to internal/tlsca, and pinning it here would be a
      # second source of truth: it is 825 days because Apple's evaluator refuses
      # 826 even for a leaf trusted as its own anchor, and a verify.sh asserting a
      # number would go red for a correct change to it.
      #
      # **This is also the host's only backstop for a real limitation.** The
      # control plane renews at PROCESS START and nowhere else, and nothing in
      # deploy/ schedules a restart, so a container up continuously past the
      # renewal window reaches expiry in service. Nothing automatic catches that.
      # This line does — when a human runs it.
      until_="$(read_secret openssl x509 -in "${host_cert}" -noout -enddate 2>/dev/null | sed 's/^notAfter=//')"
      if read_secret openssl x509 -in "${host_cert}" -noout -checkend 0 >/dev/null 2>&1; then
        ok "the console certificate is valid until ${until_}"
      else
        drift "the console certificate expired on ${until_}: the console now presents an expired certificate, and the only way in is a browser override an operator should never be trained to give"
      fi

      # **The check that carries the load.** Everything above is satisfied by a
      # certificate sitting in a directory; this one is satisfied only by the
      # console actually speaking TLS and presenting THAT certificate.
      #
      # Both fingerprints are derived — one from the file, one from the wire —
      # so neither side can be restated wrongly, and a disagreement is a real
      # finding: the process is serving something other than the file on disk,
      # which also means the fingerprint first-boot.sh printed is not the one a
      # browser will show, and the operator's only defence against accepting the
      # wrong certificate is wrong.
      #
      # **It survives renewal, including the per-handshake kind.** internal/tlsca
      # writes a renewed pair to disk BEFORE serving it, so the two sides move
      # together and a later restart agrees with what was running. That is also why
      # nothing here records a fingerprint and compares it between runs: with
      # per-handshake renewal the certificate can change while the process is up,
      # and a pinned value would go red at a moment nobody could correlate with a
      # deploy. Comparing two things measured in the same run does not have that
      # failure mode.
      file_fp="$(read_secret openssl x509 -in "${host_cert}" -noout -fingerprint -sha256 2>/dev/null | sed 's/^.*=//')"
      wire_fp="$(echo | openssl s_client -connect 127.0.0.1:8443 2>/dev/null \
        | openssl x509 -noout -fingerprint -sha256 2>/dev/null | sed 's/^.*=//')"
      if [[ -z "${wire_fp}" ]]; then
        # Nothing completed a handshake. Say WHICH failure it is: a console still
        # serving plaintext looks identical from here otherwise.
        #
        # The test is "answers 200", not "curl succeeded". A Go TLS listener
        # answers a plaintext request with 400 and a sentence about sending an
        # HTTP request to an HTTPS server, so a curl exit status cannot tell the
        # two apart and would call a plaintext console healthy.
        plain=""
        command -v curl >/dev/null 2>&1 && \
          plain="$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:8443/healthz 2>/dev/null || true)"
        if [[ "${plain}" == "200" ]]; then
          drift "127.0.0.1:8443 answered /healthz over PLAINTEXT http: the console is not serving TLS at all, and the session cookie of whoever configures RADIUS on this box crosses the loopback in the clear"
        else
          drift "nothing completed a TLS handshake on 127.0.0.1:8443, and it did not answer plaintext either — the console is not reachable on the port compose.yaml publishes"
        fi
      elif [[ "${wire_fp}" == "${file_fp}" ]]; then
        ok "the console presents the certificate in ${host_cert} (SHA-256 ${file_fp})"

        # ── and the certificate has to be valid for the address anybody dials ──
        #
        # This is the check the console's move onto the LAN made necessary, and it
        # is not implied by any of the ones above. A certificate can be present,
        # served, matching the file on disk, unexpired, correctly generated — and
        # cover only the names the CONTAINER could see, which never include this
        # host's LAN address. The operator's symptom is a browser refusing the
        # name; nothing in the process notices, because nothing in the process
        # knows what it was dialled by.
        #
        # openssl's own -verify_hostname / -verify_ip, not a grep of the SAN list:
        # matching text against subjectAltName would be a second copy of a rule
        # x509 already owns, and it is the copy being subtly narrower that
        # produces a console that verifies here and fails in a browser.
        # -verify_return_error is required — without it a name mismatch is a
        # message on stderr and an exit status of 0.
        # **The expected set comes from `consolenames.sh`, not from `hostname -I`.**
        #
        # A loop over `hostname -I` would demand docker bridge gateways that
        # `consolenames.sh` deliberately excludes — their subnets are recreated on
        # every `docker compose down -v`, and a declared name that changes rotates
        # the operator's fingerprint. Two copies of one rule drift the moment one
        # moves.
        #
        # Asking the same file the deployment asks makes the assertion *"the
        # certificate covers what this host declared"*, which is the property, rather
        # than *"…covers what I recomputed a different way"*, which is a guess that
        # happened to agree.
        # shellcheck source=/dev/null
        if . "${SRC}/deploy/consolenames.sh" 2>/dev/null && [[ -n "${FOYER_ADMIN_TLS_NAMES:-}" ]]; then
          for want in ${FOYER_ADMIN_TLS_NAMES}; do
            # An address is checked as an address and a name as a name: a verifier
            # asked about an IP consults iPAddress SANs and nothing else, so asking
            # -verify_hostname for one would pass against a certificate no browser
            # accepts.
            if [[ "${want}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
              check=(-verify_ip "${want}")
            else
              check=(-verify_hostname "${want}")
            fi
            # `openssl s_client` is a process that cannot read a 0700 directory
            # either, so the certificate is copied to a mode-600 temp file this
            # user can open. The bytes are the same — the fingerprint this block
            # compares against the wire is read from the ORIGINAL, so a stale copy
            # cannot make a mismatched pair look matched.
            ca_tmp="$(mktemp)"
            chmod 600 "${ca_tmp}"
            if read_secret cat "${host_cert}" > "${ca_tmp}" 2>/dev/null; then
              if echo | openssl s_client -connect 127.0.0.1:8443 \
                  -CAfile "${ca_tmp}" "${check[@]}" -verify_return_error \
                  >/dev/null 2>&1; then
                ok "the console's certificate is valid for ${want}"
              else
                drift "the console's certificate is NOT valid for ${want}, which deploy/consolenames.sh declares this host answers to — a browser dialling it refuses the NAME, which looks like a broken certificate and is an undeclared one. Check FOYER_ADMIN_TLS_NAMES reached the container"
              fi
            else
              skip "the console certificate's declared names — $(unreadable_note "${host_cert}")"
            fi
            rm -f "${ca_tmp}"
          done
        else
          drift "deploy/consolenames.sh produced no names, so nothing here knows what the console's certificate ought to cover — the check below it was skipped rather than answered"
        fi
      else
        drift "the console presents SHA-256 ${wire_fp} and ${host_cert} holds ${file_fp} — the process is serving a certificate that is not the file on disk, so the fingerprint first-boot.sh printed is not the one a browser shows"
      fi
    fi
  fi
fi

# ── Total ────────────────────────────────────────────────────────────────────
printf '\n'
if [[ ${drifts} -eq 0 ]]; then
  printf 'verify: %d checks, no drift.\n' "${checks}"
  printf '        This says the machine matches what was shipped. It does not say the\n'
  printf '        rebuild works — only installing on a clean host with install.sh does\n'
  printf '        that, because a guarantee nobody has made fail is not evidence.\n'
  exit 0
fi
printf 'verify: %d checks, %d DRIFT.\n' "${checks}" "${drifts}"
printf '        Each line above names the object. Fix it forward through the repository\n'
printf '        and keep a record of the change — an ad-hoc command on this box is a\n'
printf '        defect, the defect being that it is absent from deploy/.\n'
exit 1
