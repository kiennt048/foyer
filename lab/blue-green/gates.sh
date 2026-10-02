#!/bin/sh
# The blue/green procedure, run where radiusd lives.
#
#   generate -> raddb.next
#     0. dialect        radiusd -v, checked against >= 3.2.9, < 3.3
#     1. config-check   radiusd -XC -d raddb.next
#     2. second-server  a SECOND radiusd, alternate ports, candidate tree
#     3. smoke          radclient suite + eapol_test EAP suite
#     4. cutover        swap the symlink, restart, verify, roll back on failure
#
# Gate 1 is necessary and not sufficient. FreeRADIUS's own man page says so:
# "these checks are necessarily incomplete. The server can return a zero status
# code when run with -C, but may still exit with an error when run normally" —
# and some modules are skipped entirely. Gates 2 and 3 carry the real load, and
# a run that stops after gate 1 has proved that the file parses and nothing more.
#
# This is the same procedure internal/raddb's Applier runs for a host install,
# and a Go test asserts the two name the same gates in the same order. They are
# separate implementations because the control plane and radiusd are separate
# containers here and only one of them has the binaries.
#
# Environment:
#   ROOT             directory holding raddb.next, raddb.blue, raddb.green and
#                    the raddb symlink                       (default /var/lib/foyer)
#   CANDIDATE        candidate tree                          (default $ROOT/raddb.next)
#   PROBE_HOST       where the candidate listens             (default 127.0.0.1)
#   PROBE_SECRET     shared secret of the probe client       (required)
#   LIVE_AUTH_PORT   the live authentication port            (default 1812)
#   ALT_AUTH_PORT    where gate 2 runs the candidate         (default 11812)
#   EAP              1 to run the eapol_test case            (default 0)
#   EAP_DIR          where make-eap-certs.sh put its output  (default /var/lib/foyer/eap-certs)
#   EAP_CONF         eapol_test configuration                (default $EAP_DIR/eap-tls.conf)
#   MANAGE_LIVE      1 to start and restart the live server from ROOT/raddb
#                                                            (default 0)
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=${ROOT:-/var/lib/foyer}
CANDIDATE=${CANDIDATE:-$ROOT/raddb.next}
PROBE_HOST=${PROBE_HOST:-127.0.0.1}
LIVE_AUTH_PORT=${LIVE_AUTH_PORT:-1812}
ALT_AUTH_PORT=${ALT_AUTH_PORT:-11812}
EAP=${EAP:-0}
EAP_DIR=${EAP_DIR:-/var/lib/foyer/eap-certs}
EAP_CONF=${EAP_CONF:-$EAP_DIR/eap-tls.conf}
MANAGE_LIVE=${MANAGE_LIVE:-0}
RADIUSD=${RADIUSD:-radiusd}
RADCLIENT=${RADCLIENT:-radclient}
EAPOL_TEST=${EAPOL_TEST:-eapol_test}

: "${PROBE_SECRET:?PROBE_SECRET is required; there is no default shared secret and there never will be}"

if [ "$ALT_AUTH_PORT" = "$LIVE_AUTH_PORT" ]; then
	echo "refusing: the alternate port is the live port; gate 2 would fight the running server for the socket" >&2
	exit 2
fi

mkdir -p "$ROOT"

say() { printf '\n=== gate %s — %s ===\n' "$1" "$2"; }
fail() { printf 'GATE %s FAILED: %s\n' "$1" "$2" >&2; exit 1; }

candidate_pid=""
cleanup() {
	if [ -n "$candidate_pid" ] && kill -0 "$candidate_pid" 2>/dev/null; then
		# A candidate left running holds the alternate port, and the next run's
		# gate 2 then fails on a socket rather than on the configuration.
		kill "$candidate_pid" 2>/dev/null || true
		wait "$candidate_pid" 2>/dev/null || true
	fi
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------- gate 0
say 0 dialect
version_line=$($RADIUSD -v 2>&1 | head -1)
version=$(printf '%s' "$version_line" | sed -n 's/.*FreeRADIUS Version \([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\).*/\1/p')
[ -n "$version" ] || fail 0 "no FreeRADIUS version in: $version_line"

major=$(printf '%s' "$version" | cut -d. -f1)
minor=$(printf '%s' "$version" | cut -d. -f2)
patch=$(printf '%s' "$version" | cut -d. -f3)

# >= 3.2.9, < 3.3. A range, not a pin: 3.2.11 will be a security release and an
# equality check would turn every upstream security fix into a Foyer outage.
if [ "$major" -ne 3 ] || [ "$minor" -ne 2 ] || [ "$patch" -lt 9 ]; then
	fail 0 "FreeRADIUS $version is outside >= 3.2.9, < 3.3 (found: $version_line)"
fi
echo "FreeRADIUS $version"

[ -d "$CANDIDATE" ] || fail 0 "candidate tree $CANDIDATE does not exist; generate it first"

# ---------------------------------------------------------------- gate 1
say 1 config-check
if ! out=$($RADIUSD -XC -d "$CANDIDATE" 2>&1); then
	printf '%s\n' "$out" | tail -30 >&2
	fail 1 "radiusd -XC rejected $CANDIDATE"
fi
echo "configuration parses"

# ---------------------------------------------------------------- gate 2
say 2 second-server
# No -n. It does not name an instance: it makes radiusd read <name>.conf instead
# of radiusd.conf, and the candidate has to run the same file the live server
# will run or gate 2 is testing something else. -f writes no pidfile, so the two
# servers do not collide over one.
$RADIUSD -f -d "$CANDIDATE" -i "$PROBE_HOST" -p "$ALT_AUTH_PORT" \
	>"$ROOT/candidate.log" 2>&1 &
candidate_pid=$!

ready=0
i=0
while [ "$i" -lt 40 ]; do
	if ! kill -0 "$candidate_pid" 2>/dev/null; then
		tail -30 "$ROOT/candidate.log" >&2
		fail 2 "the candidate server exited during startup — this is precisely what gate 1 cannot see"
	fi
	if $RADCLIENT -t 1 -r 1 -f "$HERE/probes/status-server.request" \
		"$PROBE_HOST:$ALT_AUTH_PORT" status "$PROBE_SECRET" >/dev/null 2>&1; then
		ready=1
		break
	fi
	i=$((i + 1))
	sleep 0.25
done
[ "$ready" -eq 1 ] || { tail -30 "$ROOT/candidate.log" >&2; fail 2 "the candidate server never answered on $ALT_AUTH_PORT"; }
echo "candidate answering on $ALT_AUTH_PORT"

# ---------------------------------------------------------------- gate 3
say 3 smoke
ran=""

for req in "$HERE"/probes/*.request; do
	[ -e "$req" ] || fail 3 "no radclient cases in $HERE/probes; an empty suite passes every assertion in it"
	name=$(basename "$req" .request)
	type=$(sed -n 's/^#packet-type:[[:space:]]*//p' "$req" | head -1)
	type=${type:-auth}
	# radclient's exit status answers "did I get the reply I assume you wanted",
	# and it assumes Access-Accept. A case whose whole point is that the server
	# says no would fail on the exit status while doing exactly what it should,
	# so the case states what it expects and that is what is judged. A case with
	# no #expect line is a case that asserts nothing.
	out=$($RADCLIENT -x -t 3 -r 1 -f "$req" "$PROBE_HOST:$ALT_AUTH_PORT" "$type" "$PROBE_SECRET" 2>&1 || true)
	expect=$(sed -n 's/^#expect:[[:space:]]*//p' "$req" | head -1)
	[ -n "$expect" ] || fail 3 "radclient case $name has no #expect line"
	if ! printf '%s' "$out" | grep -q "$expect"; then
		printf '%s\n' "$out" | tail -20 >&2
		fail 3 "radclient case $name did not produce: $expect"
	fi
	ran="$ran $name"
done

if [ "$EAP" = "1" ]; then
	# radclient and radtest do not speak EAP at all. A project whose entire
	# architectural argument is "EAP is too hard to implement ourselves" cannot
	# have zero EAP coverage, and eapol_test is the only way to get it without
	# an access point.
	[ -f "$EAP_CONF" ] || fail 3 "EAP=1 but $EAP_CONF does not exist"
	if ! out=$($EAPOL_TEST -c "$EAP_CONF" -a "$PROBE_HOST" -p "$ALT_AUTH_PORT" -s "$PROBE_SECRET" -t 10 2>&1); then
		printf '%s\n' "$out" | tail -30 >&2
		fail 3 "eapol_test case $(basename "$EAP_CONF")"
	fi
	printf '%s' "$out" | grep -q "SUCCESS" || { printf '%s\n' "$out" | tail -30 >&2; fail 3 "eapol_test did not report SUCCESS"; }
	ran="$ran $(basename "$EAP_CONF" .conf)"
else
	echo "EAP=0: the eapol_test case is not being run. Say so in the report; a"
	echo "suite that silently skips its only EAP case is not an EAP suite."
fi
echo "passed:$ran"

cleanup
candidate_pid=""

# ---------------------------------------------------------------- gate 4
say 4 cutover
link="$ROOT/raddb"
if [ -L "$link" ]; then
	prev=$(basename "$(readlink "$link")")
else
	prev=raddb.green
fi
case "$prev" in
raddb.blue) new=raddb.green ;;
raddb.green) new=raddb.blue ;;
*) fail 4 "$link points at $prev, which is neither colour; refusing to guess" ;;
esac

rm -rf "${ROOT:?}/$new"
mv "$CANDIDATE" "$ROOT/$new"

# rename over the link rather than remove-and-recreate: the second has a window
# in which a restarting radiusd finds no configuration at all.
ln -sfn "$new" "$link.swap"
mv -T "$link.swap" "$link" 2>/dev/null || mv "$link.swap" "$link"
echo "$prev -> $new"

if [ "$MANAGE_LIVE" = "1" ]; then
	if [ -f "$ROOT/live.pid" ] && kill -0 "$(cat "$ROOT/live.pid")" 2>/dev/null; then
		kill "$(cat "$ROOT/live.pid")" 2>/dev/null || true
		wait "$(cat "$ROOT/live.pid")" 2>/dev/null || true
	fi
	$RADIUSD -f -d "$link" -i "$PROBE_HOST" -p "$LIVE_AUTH_PORT" \
		>"$ROOT/live.log" 2>&1 &
	echo $! >"$ROOT/live.pid"

	ok=0
	i=0
	while [ "$i" -lt 40 ]; do
		if $RADCLIENT -t 1 -r 1 -f "$HERE/probes/status-server.request" \
			"$PROBE_HOST:$LIVE_AUTH_PORT" status "$PROBE_SECRET" >/dev/null 2>&1; then
			ok=1
			break
		fi
		i=$((i + 1))
		sleep 0.25
	done

	if [ "$ok" -ne 1 ]; then
		# The swap is cosmetic unless a failure puts the previous tree back and
		# restarts on it.
		ln -sfn "$prev" "$link.swap"
		mv -T "$link.swap" "$link" 2>/dev/null || mv "$link.swap" "$link"
		$RADIUSD -f -d "$link" -i "$PROBE_HOST" -p "$LIVE_AUTH_PORT" \
			>"$ROOT/live.log" 2>&1 &
		echo $! >"$ROOT/live.pid"
		fail 4 "the live server did not answer after cutover; rolled back to $prev"
	fi
	echo "live server answering on $LIVE_AUTH_PORT"
else
	echo "MANAGE_LIVE=0: the symlink is swapped and the live server has NOT been"
	echo "restarted. Restarting is the supervisor's job, and SIGHUP is not an"
	echo "escape: it does not reliably reload clients, listeners or virtual"
	echo "servers in 3.2.x."
fi

printf '\nall gates passed\n'
