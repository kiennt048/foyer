#!/usr/bin/env bash
#
# nas.sh — be a NAS against the running lab, and watch the data plane decide.
#
# ── what this is for ──────────────────────────────────────────────────────────
#
# Sending a real Access-Request is the only way to see the AAA data plane decide
# something, and by hand it takes three things: register a device in the
# console, read the shared secret off the one-time dialog it returns, and type it
# into a radclient command line. The secret is 40 uppercase alphanumerics with no
# separators, and reading it off a screen inserts dashes. The result is this, and
# it reads like a broken product:
#
#   Info: Dropping packet without response because of error: Received packet
#   from 127.0.0.1 with invalid Message-Authenticator! (Shared secret is
#   incorrect.)
#
# That is a mistyped string, not a defect, and nothing on the wire can say which.
#
#   bash lab/nas.sh auth    # an Access-Request the policy engine should allow,
#                           # and one it should refuse — both shown
#   bash lab/nas.sh acct    # the same allow, then Accounting-Start and -Stop
#                           # for that session, and the radacct row
#
# ── where the shared secret comes from, and where it does not go ─────────────
#
# `nas.secret`, read as **foyer_radius** — the login FreeRADIUS itself uses,
# which holds SELECT on that table and nothing else (migration 00002:116). That
# is the store the data plane authenticates against, not the console: the
# console's copy is write-only in the API on purpose, and a tool that asked the
# UI for it would be asking a human to re-type a secret, which is the thing this
# file exists to stop.
#
# It is never printed, never logged, and never written to a file. radclient
# takes a shared secret as an argv and offers no other form, so it reaches the
# container's argv and no further: the secret travels on the `docker exec`
# standard input, after a sentinel line, so it is not in the *host's* process
# list either — `ps` on the developer's own machine shows the command, and a
# secret in it is a secret in a place a human reads twice. Everything this file
# prints about it is its length.
#
# ── why the packet is sent from inside the FreeRADIUS container ──────────────
#
# Sending UDP to a port Docker publishes on 127.0.0.1 rewrites the source
# address to the Docker bridge gateway, so a host-side radclient presents itself
# as the gateway and no `nas` row matches it. The obvious repair — adding the
# gateway to the client table — is not made here, and must not be. Sending from
# inside the container makes the source really 127.0.0.1, which is the row
# lab/policy uses.
#
# ── the posture ──────────────────────────────────────────────────────────────
#
# This is a lab tool and says so: it reads the lab's own database through the
# same login the data plane uses, and it mints one voucher if the lab has none
# unused, because a voucher is a guest's bearer credential printed on a slip —
# not a secret — and the accept half of the demonstration needs one. It creates
# nothing else. A site, a device and its sealed secret are an operator's to make
# in the console; when they are missing this reports that as the finding it is,
# with the console action named.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# radius_answer classifies a radclient -x transcript into policy / unavailable /
# degraded / unexplained / none. It answers "who decided that", and a reject
# that is really an unreachable control plane is the difference between a wrong
# voucher and an outage.
# shellcheck source=measure/radius-answer.sh
source "${HERE}/measure/radius-answer.sh"

FR_CONTAINER=foyer-lab-freeradius
PG_CONTAINER=foyer-lab-postgres

# The address the packet leaves from, which is also the address the client table
# is keyed by. Not a knob: see the source-address note above.
ADDR=127.0.0.1
AUTH_ADDR=127.0.0.1:1812
ACCT_ADDR=127.0.0.1:1813

# One send, not four. radclient retransmits by default, and a voucher is
# single-use across the portal and RADIUS alike (internal/aaa/policy.go), so a
# retransmit of an accepted request is a refusal of one this tool just caused.
TIMEOUT=5

# The sentinel the packet and the secret are separated by on stdin.
SEP=--foyer-secret--

# `voucherKey` requires exactly 12 characters from the contract's alphabet, so a
# reject probe has to be well-formed to reach the voucher path at all: a string
# of the wrong length is refused as an identity, which is a different rejection
# and would prove nothing. D E N Y are all inside A-H J-K M-N P-Z.
VOUCHER_ALPHABET=23456789ABCDEFGHJKMNPQRSTUVWXYZ

# ── output ───────────────────────────────────────────────────────────────────

say() { printf '%s\n' "$*"; }
step() {
	local title="$1" rule='' i
	for ((i = 0; i < ${#title}; i++)); do rule="${rule}-"; done
	printf '\n  %s\n  %s\n' "${title}" "${rule}"
}

# A finding is a cause this tool cannot establish, printed with what to do about
# it. Silence from radiusd is a finding, not a success: see no_reply_finding.
finding() {
	local headline="$1"
	shift
	printf '\n  FINDING: %s\n' "${headline}" >&2
	local line
	for line in "$@"; do
		printf '    %s\n' "${line}" >&2
	done
	printf '\n' >&2
	exit 1
}

# ── the lab ──────────────────────────────────────────────────────────────────

psql_as() {
	# $1 = role, $2 = SQL. No `-i`: nothing is piped in, and a container whose
	# stdin is the developer's terminal is a psql waiting for a query. The
	# official postgres image initialises its cluster with `--auth-local=trust`,
	# so a socket connection needs no password and none appears on any command
	# line.
	docker exec "${PG_CONTAINER}" psql -U "$1" -d foyer -Atq -v ON_ERROR_STOP=1 -c "$2"
}

require_lab() {
	command -v docker >/dev/null 2>&1 ||
		finding "docker is not on PATH." "This tool drives the running lab; there is no in-process substitute."
	docker info --format '{{.ServerVersion}}' >/dev/null 2>&1 ||
		finding "the Docker daemon does not answer 'docker info'." "Start it, then: make lab-up"
	local c
	for c in "${FR_CONTAINER}" "${PG_CONTAINER}"; do
		[[ "$(docker inspect -f '{{.State.Status}}' "$c" 2>/dev/null || true)" == "running" ]] ||
			finding "${c} is not running." "The lab is the thing this tool talks to:  make lab-up"
	done
	# Check the VERSION, not merely that a binary exists. A cached alpine
	# FreeRADIUS is 3.0.27 — a different dialect from the 3.2.x unlang
	# internal/raddb emits — and it starts and answers well enough that nobody
	# notices (lab/Dockerfile.freeradius).
	FR_VERSION="$(docker exec "${FR_CONTAINER}" radiusd -v 2>/dev/null | head -1 || true)"
	[[ "${FR_VERSION}" == *"FreeRADIUS Version 3.2."* ]] ||
		finding "the lab's FreeRADIUS is not 3.2.x: ${FR_VERSION:-radiusd -v said nothing}" \
			"The generated tree is 3.2.x dialect (lab/Dockerfile.freeradius). Rebuild the image:" \
			"  docker compose -f lab/docker-compose.yml build freeradius"
}

# ── the client, read the way FreeRADIUS reads it ─────────────────────────────

resolve_client() {
	step "1. the client this packet presents as"
	# `<<=` and `masklen` are the lookup internal/store's RADIUSClientSite makes,
	# so this is the address the Policy API will resolve to a site.
	local row lines
	row="$(psql_as foyer_radius "
		SELECT nasname || '|' || shortname || '|' || secret
		  FROM nas
		 WHERE '${ADDR}'::inet <<= nasname::inet
		 ORDER BY masklen(nasname::inet) DESC, nasname
		 LIMIT 1;")" || finding "could not read the lab database as foyer_radius." \
		"  docker exec ${PG_CONTAINER} psql -U foyer_radius -d foyer -c 'SELECT nasname FROM nas;'"

	[[ -n "${row}" ]] || finding "no row in \`nas\` covers ${ADDR}, so radiusd discards every packet from it." \
		"radiusd's own words for this, which you can check:" \
		"  docker logs --tail 30 ${FR_CONTAINER} | grep 'unknown client'" \
		"" \
		"The row is a projection of a device registered in the console, keyed by the" \
		"address the packet arrives from. Register one there (Network > Devices) with the" \
		"address ${ADDR} and the control plane writes this row itself: no CLI, no sync."

	lines="$(printf '%s\n' "${row}" | wc -l | tr -d ' ')"
	[[ "${lines}" == "1" ]] || finding "the client query returned ${lines} lines; expected exactly one." \
		"  SELECT nasname, shortname FROM nas ORDER BY nasname;"

	IFS='|' read -r NAS_NAME NAS_SHORT SECRET <<<"${row}"
	[[ -n "${SECRET}" ]] || finding "the nas row for ${NAS_NAME} has an empty secret."

	say "  client     ${NAS_NAME} (${NAS_SHORT})"
	say "  secret     ${#SECRET} characters, read as foyer_radius (SELECT on nas). Not shown, not logged, not written."

	SITE="$(psql_as foyer "
		SELECT site_id FROM radius_clients
		 WHERE enabled AND '${ADDR}'::inet <<= address
		 ORDER BY masklen(address) DESC, id
		 LIMIT 1;")" || finding "could not read radius_clients."

	[[ -n "${SITE}" ]] || finding "a client is registered for ${ADDR}, but no ENABLED radius_clients row covers it." \
		"The Policy API resolves a venue from that table by the packet's source address and" \
		"refuses outright when it resolves to nothing. The answer would be an" \
		"Access-Reject with \"denied by policy\", which on the wire is indistinguishable" \
		"from a wrong voucher." \
		"" \
		"  SELECT address, enabled, site_id FROM radius_clients ORDER BY address;"

	say "  site       ${SITE}  (the venue the Policy API resolves ${ADDR} to)"
}

# ── an identity to present ───────────────────────────────────────────────────

rand_code() {
	# Not `tr … | head -c 12`: head exits after twelve bytes, tr dies on SIGPIPE,
	# and a command substitution under `set -o pipefail` reports 141 as a failure
	# of the whole script. Every output character is inside the contract's
	# alphabet whatever the modulus does, which is the only property the CHECK on
	# vouchers.code needs of this.
	local hex out='' i n
	hex="$(openssl rand -hex 16)"
	for ((i = 0; i < 12; i++)); do
		n=$((16#${hex:i:1} % ${#VOUCHER_ALPHABET}))
		out="${out}${VOUCHER_ALPHABET:n:1}"
	done
	printf '%s' "${out}"
}

voucher_state() {
	psql_as foyer "
		SELECT state || ' | ' ||
		       coalesce(to_char(first_used_at, 'YYYY-MM-DD HH24:MI:SS'), '-') || ' | ' ||
		       coalesce(to_char(expires_at, 'YYYY-MM-DD HH24:MI:SS'), '-')
		  FROM vouchers WHERE code = '${1}';"
}

# An unused code at this site, or one minted for this probe. The claim is
# single-use, so `auth` and `acct` each consume a different code; a code already
# spent by an earlier run is not a bug in either.
unused_voucher() {
	step "2. the identity this packet will present"
	CODE="$(psql_as foyer "
		SELECT v.code
		  FROM vouchers v
		 WHERE v.site_id = '${SITE}'
		   AND v.state = 'unused'
		   AND (v.expires_at IS NULL OR v.expires_at > now())
		 ORDER BY v.created_at, v.code
		 LIMIT 1;")" || finding "could not read the voucher table."

	if [[ -n "${CODE}" ]]; then
		say "  voucher    ${CODE}  (an unused code the lab already holds for ${SITE})"
		return
	fi

	# None, and the accept half needs one. Mint it: the same alphabet, the same
	# length and the same single-use state machine the console's batch operation
	# produces, written as the database owner. A voucher is printed on a slip and
	# handed to a guest; it is not one of the things this file refuses to print.
	local bid vid code
	bid="batch-lab-nas-$(openssl rand -hex 4)"
	vid="voucher-lab-nas-$(openssl rand -hex 4)"
	code="$(rand_code)"
	psql_as foyer "
		WITH t AS (SELECT id FROM tenants ORDER BY created_at, id LIMIT 1)
		INSERT INTO voucher_batches
		       (id, tenant_id, site_id, label, count, duration_minutes, enforced_by, expiry_mode)
		SELECT '${bid}', t.id, '${SITE}', 'lab nas.sh probe', 1, 60, 'unenforced', 'from-first-use'
		  FROM t;" >/dev/null ||
		finding "could not create a voucher batch at ${SITE}." \
			"Is the site still there?  SELECT id, name FROM sites;"
	psql_as foyer "
		INSERT INTO vouchers (id, tenant_id, batch_id, site_id, code)
		SELECT '${vid}', b.tenant_id, b.id, b.site_id, '${code}'
		  FROM voucher_batches b WHERE b.id = '${bid}';" >/dev/null ||
		finding "could not mint a voucher." "  SELECT code, state FROM vouchers ORDER BY created_at DESC LIMIT 5;"
	CODE="${code}"
	say "  voucher    ${CODE}  — minted for this probe; the lab held no unused code at ${SITE}."
	say "             Create a real batch in the console (Vouchers) and this stops happening."
}

# A well-formed code that is in no batch, so the refusal is the engine's and not
# a malformed identity's. Verified absent rather than assumed: a collision is one
# in 8e17, and a claim about what a packet proved is worth three lines.
absent_code() {
	local code taken tries=0
	while [ "${tries}" -lt 5 ]; do
		tries=$((tries + 1))
		code="$(rand_code)"
		taken="$(psql_as foyer "SELECT count(*) FROM vouchers WHERE code = '${code}';")"
		if [[ "${taken}" == "0" ]]; then
			printf '%s' "${code}"
			return 0
		fi
	done
	finding "could not find a voucher code that does not exist." \
		"Every code this lab minted is taken, which is not a state a lab can be in."
}

# ── the packet ───────────────────────────────────────────────────────────────

# $1 = radclient's stdin, $2 = server:port, $3 = auth|acct. The secret arrives on
# stdin after the sentinel, never on the host's command line. radclient has no
# file form for it, so it is the container's argv and stops there.
send() {
	printf '%s\n%s\n%s\n' "$1" "${SEP}" "${SECRET}" |
		docker exec -i "${FR_CONTAINER}" sh -c '
			sep="'"${SEP}"'"
			# ONE read of stdin. Two command substitutions each start a fresh
			# reader on the same pipe, so the first drains it and the second finds
			# nothing.
			in=$(cat)
			packet=$(printf "%s\n" "$in" | sed -n "1,/^${sep}\$/p" | sed "\$d")
			secret=$(printf "%s\n" "$in" | sed -n "/^${sep}\$/,\$p" | sed "1d")
			if [ -z "$packet" ] || [ -z "$secret" ]; then
				echo "nas.sh: the packet/secret split on stdin failed" >&2
				exit 64
			fi
			printf "%s\n" "$packet" | radclient -x -t '"${TIMEOUT}"' -r 1 "$1" "$2" "$secret"
		' radclient "$2" "$3"
}

ACCESS_OUT=""

# $1 = User-Name, $2 = what this probe is meant to prove. The transcript lands in
# ACCESS_OUT so the caller can judge it: re-sending to keep a copy would spend a
# second single-use code and see the refusal this file is about to explain.
access_request() {
	step "$2"
	say "  Access-Request   User-Name=\"$1\"  NAS-IP-Address=${ADDR}  sent from inside ${FR_CONTAINER}"
	# Reset per call: a stale non-zero from the previous probe would otherwise be
	# reported against this one.
	ACCESS_RC=0
	ACCESS_OUT="$(send "User-Name = \"$1\"
NAS-IP-Address = ${ADDR}
NAS-Identifier = \"${NAS_SHORT}\"
Calling-Station-Id = \"02:11:22:33:44:55\"
Called-Station-Id = \"00:11:22:33:44:00:foyer-lab\"
Message-Authenticator = 0x00
User-Password = \"irrelevant\"" "${AUTH_ADDR}" auth)" || ACCESS_RC=$?


	# **radclient's exit code is not the answer, and reading it as one is the bug
	# this comment exists for.** `radclient … auth` expects an Access-Accept, so
	# the probe that MUST be refused makes radclient print
	#
	#     (0) -: Expected Access-Accept got Access-Reject
	#
	# and exit non-zero. That is the *correct* outcome of the most important
	# assertion in this file, and reading the exit code would report it as
	#
	#     FINDING: radclient could not be run inside foyer-lab-freeradius
	#
	# — a refusal presented as a broken tool, which teaches the reader that a
	# working policy engine is a broken lab.
	#
	# So the transcript decides, not the exit code. A reply of either kind means
	# radclient ran; no reply at all is the silence case, and `no_reply_finding`
	# already tells the two causes apart from radiusd's own log.
	if ! grep -q '^Received Access-' <<<"${ACCESS_OUT}"; then
		if [[ -z "${ACCESS_OUT//[[:space:]]/}" ]]; then
			finding "radclient produced no output at all inside ${FR_CONTAINER} (exit ${ACCESS_RC})." \
				"  docker exec ${FR_CONTAINER} radclient -v"
		fi
		no_reply_finding "Access-Request" "${ACCESS_OUT}"
	fi
	# Kept for the record, and deliberately not acted on: the number belongs in
	# a report, not in a verdict.
	[[ "${ACCESS_RC}" == "0" ]] || say "  (radclient exited ${ACCESS_RC}, which for a refused probe is the expected answer — see below)"
}

# ── reading the answer ───────────────────────────────────────────────────────

# Silence is a finding, and radiusd's log is the only thing that separates the
# two causes. They are different answers to the same symptom.
no_reply_finding() {
	local what="$1" out="$2" log
	log="$(docker logs --tail 60 "${FR_CONTAINER}" 2>&1 || true)"

	{
		printf '\n'
		if grep -q 'No reply from server' <<<"${out}"; then
			printf '  radiusd sent nothing back.\n'
		else
			printf '  radclient printed neither a send nor a receive.\n'
		fi
		printf '  An unanswered %s is neither a pass nor a fail: it never reached\n' "${what}"
		printf '  the Policy API. The cause is one of exactly two, and only\n'
		printf "  radiusd's log separates them:\n\n"
		if grep -q 'unknown client' <<<"${log}"; then
			printf '    FOUND: radiusd says "Ignoring request ... from unknown client %s".\n' "${ADDR}"
			printf '    There is no row in `nas` for the address the packet came from.\n'
			printf '      SELECT nasname, shortname FROM nas ORDER BY nasname;\n'
		fi
		if grep -q 'invalid Message-Authenticator' <<<"${log}"; then
			printf '    FOUND: radiusd says "invalid Message-Authenticator! (Shared secret is\n'
			printf '    incorrect.)" The client WAS found and its secret did not verify — which\n'
			printf '    is what a hand-typed secret looks like, and what a rotated one looks like.\n'
			printf '      SELECT nasname, shortname, length(secret) FROM nas ORDER BY nasname;\n'
		fi
		if ! grep -qE 'unknown client|invalid Message-Authenticator' <<<"${log}"; then
			printf "    NOT FOUND: neither line is in the last 60 lines of radiusd's log. The\n"
			printf '    packet may not have left this container at all, or radiusd restarted\n'
			printf '    and lost it.\n'
			printf '      docker logs --tail 60 %s\n' "${FR_CONTAINER}"
			printf '      docker inspect -f "{{.State.Status}}" %s\n' "${FR_CONTAINER}"
		fi
		printf '\n  radiusd, in full:\n'
		printf '%s\n' "${log}" | sed 's/^/    /'
		printf '\n  foyerctl doctor is the other half of this answer: it compares the client\n'
		printf '  table against the system of record and says which of the two is stale.\n'
		printf '    docker exec foyer-lab-backend foyerctl doctor\n\n'
	} >&2
	exit 1
}

# $1 = the transcript, $2 = Accept or Reject. Returns non-zero when the answer is
# not the one the probe was for, so this file can fail.
judge_access() {
	local out="$1" want="$2" class other
	class="$(radius_answer <<<"${out}")"
	if grep -q "^Received Access-${want}" <<<"${out}"; then
		printf '\n'
		printf '  Access-%s, and the Policy API is what sent it (radius_answer: %s).\n' "${want}" "${class}"
		return 0
	fi
	other="$(grep -oE '^Received Access-(Accept|Reject)' <<<"${out}" | head -1 || true)"
	{
		printf '\n'
		printf '  The probe was meant to prove Access-%s and got %s.\n' "${want}" "${other:-nothing at all}"
		case "${class}" in
		unavailable)
			printf '  The Policy API did not answer at all. This is the generated tree failure\n'
			printf '  branch, not a decision about the identity:\n'
			printf '    docker logs --tail 60 foyer-lab-backend\n'
			;;
		degraded)
			printf '  The site fails OPEN, so this accept was minted blind and carries\n'
			printf '  Class Foyer:degraded. It is not a decision about the identity.\n'
			;;
		unexplained)
			printf '  The reply matches no branch internal/raddb generates, so the running tree\n'
			printf '  is not the tree this checkout generates:\n'
			printf '    docker exec %s cat /var/lib/foyer/raddb/status\n' "${FR_CONTAINER}"
			;;
		*)
			printf '  The Policy API decided, and it decided the other way. For a code that is\n'
			printf '  in no batch that is correct. For the accept half it means the code was not\n'
			printf '  claimable: spent, expired, revoked, or sold at another venue.\n'
			printf '    SELECT code, state, expires_at, site_id FROM vouchers ORDER BY created_at DESC LIMIT 10;\n'
			;;
		esac
	} >&2
	return 1
}

# ── modes ────────────────────────────────────────────────────────────────────

mode_auth() {
	say "lab/nas.sh auth — one Access-Request the lab should allow, one it should refuse."
	resolve_client
	unused_voucher

	local before after absent claimed=1
	before="$(voucher_state "${CODE}")"
	access_request "${CODE}" "3. Access-Request with a valid voucher code (expect Access-Accept)"
	judge_access "${ACCESS_OUT}" "Accept"
	after="$(voucher_state "${CODE}")"

	step "4. what the engine did to that code in the database"
	printf '  before   %s\n' "${before}"
	printf '  after    %s\n' "${after}"
	if [[ "${before}" == unused* && "${after}" == "in-use"* ]]; then
		printf '  The claim is the decision: one statement, in the Policy API, before the reply.\n'
	else
		claimed=0
		printf '  The row did not move from unused to in-use, so whatever answered did not claim\n'
		printf '  it, and the Access-Accept above did not come from a claim.\n'
	fi

	absent="$(absent_code)"
	access_request "${absent}" "5. Access-Request with a code that is in no batch (expect Access-Reject)"
	judge_access "${ACCESS_OUT}" "Reject"

	printf '\n'
	printf '  Both answers were required. The accept says the engine claimed a real code;\n'
	printf '  the reject says it refuses one it has never heard of. A tool that only ever\n'
	printf '  sees accepts has proved nothing.\n'

	if [[ "${claimed}" == "0" ]]; then
		printf '\n  FINDING: an Access-Accept that did not claim the voucher.\n' >&2
		printf '    The reply was right and the decision was not made, which is the state a\n' >&2
		printf '    fail-open site produces and the one thing this probe exists to catch.\n' >&2
		printf '      SELECT code, state, first_used_at FROM vouchers ORDER BY created_at DESC LIMIT 5;\n' >&2
		return 1
	fi
}

mode_acct() {
	say "lab/nas.sh acct — one session: Access-Request, then Accounting-Start and -Stop."
	resolve_client
	unused_voucher

	local sid start_out stop_out row
	sid="lab-nas-$(openssl rand -hex 4)"

	access_request "${CODE}" "3. Access-Request (expect Access-Accept; the session below is this one)"
	judge_access "${ACCESS_OUT}" "Accept"

	step "4. Accounting-Start, session ${sid}, to 1813"
	start_out="$(send "Acct-Status-Type = Start
User-Name = \"${CODE}\"
Acct-Session-Id = \"${sid}\"
NAS-IP-Address = ${ADDR}
NAS-Identifier = \"${NAS_SHORT}\"
NAS-Port = 1
Calling-Station-Id = \"02:11:22:33:44:55\"
Called-Station-Id = \"00:11:22:33:44:00:foyer-lab\"" "${ACCT_ADDR}" acct)" ||
		finding "radclient could not be run inside ${FR_CONTAINER}."
	printf '%s\n' "${start_out}" | sed 's/^/  | /'
	grep -q 'Received Accounting-Response' <<<"${start_out}" ||
		no_reply_finding "Accounting-Request" "${start_out}"

	step "5. Accounting-Stop for the same session"
	stop_out="$(send "Acct-Status-Type = Stop
User-Name = \"${CODE}\"
Acct-Session-Id = \"${sid}\"
NAS-IP-Address = ${ADDR}
NAS-Identifier = \"${NAS_SHORT}\"
NAS-Port = 1
Calling-Station-Id = \"02:11:22:33:44:55\"
Called-Station-Id = \"00:11:22:33:44:00:foyer-lab\"
Acct-Session-Time = 1
Acct-Terminate-Cause = User-Request
Acct-Input-Octets = 0
Acct-Output-Octets = 0" "${ACCT_ADDR}" acct)" ||
		finding "radclient could not be run inside ${FR_CONTAINER}."
	printf '%s\n' "${stop_out}" | sed 's/^/  | /'
	grep -q 'Received Accounting-Response' <<<"${stop_out}" ||
		no_reply_finding "Accounting-Request" "${stop_out}"

	step "6. what FreeRADIUS wrote to radacct — its table, not Foyer's"
	# Foyer never writes radacct (docs/rules/database.md). The row below is the evidence that
	# the two halves met, and acctuniqueid is what the generated preacct section
	# computed: without it radacct's UNIQUE admits one session per install.
	row="$(psql_as foyer "
		SELECT concat_ws(' | ',
		       acctsessionid, coalesce(to_char(acctstarttime, 'YYYY-MM-DD HH24:MI:SS'), '-'),
		       coalesce(to_char(acctstoptime, 'YYYY-MM-DD HH24:MI:SS'), '-'),
		       coalesce(acctuniqueid, '-'),
		       coalesce(acctsessiontime::text, '-'))
		  FROM radacct WHERE acctsessionid = '${sid}'
		 ORDER BY radacctid;")" ||
		finding "could not read radacct." \
			"  docker exec ${PG_CONTAINER} psql -U foyer -d foyer -c 'SELECT * FROM radacct LIMIT 5;'"
	if [[ -n "${row}" ]]; then
		printf '%s\n' "${row}" | sed 's/^/  | /'
		printf '\n'
		printf '  acctsessionid | acctstarttime | acctstoptime | acctuniqueid | acctsessiontime\n'
		printf '  One row, opened and closed, with a real Acct-Unique-Session-Id: the stop\n'
		printf '  matched the start instead of writing a second one.\n'
	else
		printf '\n  FINDING: no radacct row for %s.\n' "${sid}" >&2
		printf '    The Accounting-Responses above are real and the row is still absent. Every\n' >&2
		printf '    figure the console shows about a session comes out of this table, so this is\n' >&2
		printf '    the failure this command exists to catch — and it is not a formatting one.\n' >&2
		printf '      docker exec %s psql -U foyer -d foyer -c "SELECT acctsessionid, acctuniqueid FROM radacct ORDER BY radacctid DESC LIMIT 5;"\n' "${PG_CONTAINER}" >&2
		return 1
	fi
}

# ── main ─────────────────────────────────────────────────────────────────────

usage() {
	cat >&2 <<'USAGE'
lab/nas.sh — be a NAS against the running lab, and watch the data plane decide.

  bash lab/nas.sh auth    an Access-Request the policy engine should allow, and
                          one it should refuse
  bash lab/nas.sh acct    the same allow, then Accounting-Start and -Stop for
                          that session, and the row FreeRADIUS wrote

Needs the lab up (make lab-up). Exits non-zero when a probe does not answer as
described, because a tool that cannot fail is not evidence.
USAGE
}

main() {
	case "${1:-}" in
	auth | acct) ;;
	*) usage; exit 2 ;;
	esac
	require_lab
	say "  FreeRADIUS: ${FR_VERSION}"
	case "$1" in
	auth) mode_auth ;;
	acct) mode_acct ;;
	esac
}

main "$@"
