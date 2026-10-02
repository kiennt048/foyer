#!/bin/sh
# Foyer's FreeRADIUS supervisor: how a tree the admin console generated reaches
# radiusd and takes effect, with no operator command and no shell command stored
# anywhere. The protocol, the survey behind it and the Go side are in
# internal/raddb/handoff.go; TestTheSupervisorSpeaksThisProtocol drives this file
# against a fake radiusd.
#
# It runs as the FreeRADIUS container's command, under tini (`init: true`), as
# root, and **radiusd never does.** Every radiusd it starts — the `-XC` check and
# the server — is started as `freerad` with no capabilities (`setpriv`, below).
# If radiusd started as root and dropped privileges only because the tree's own
# `security { user = }` said so, a control plane that wrote a tree without the
# line would have a root radiusd in the FreeRADIUS container. So the privilege
# drop is this file's, and the tree has no say in it. Measured on 3.2.10: radiusd
# started as freerad on a tree that says `user = freerad` reads it, binds 1812 and
# prints "Ready to process requests".
#
# And the tree is checked against the files the generator writes before any of
# it is copied into place (check_tree): a file, a directory or a link the
# generator never emits — `mods-enabled/exec`, a link out of the tree — refuses
# the generation, and the running server keeps running.
#
#   HANDOFF/gen-<N>/    written by the control plane
#   HANDOFF/request     generation=<N>, mode=apply|startup
#   HANDOFF/status      written here: generation, state, restarted, at, detail
#   HANDOFF/supervisor  written here at start: protocol=1
#
# One generation, in order:
#   1. copy gen-<N> into the colour not running (LIVE/blue or LIVE/green), owned
#      root:foyer-radius-tls and unreadable to anyone else, because radiusd reads
#      EAP keys as `freerad` after it drops privileges;
#   2. point LINK (/etc/raddb) at it and run `radiusd -XC` — a refusal points
#      LINK back and the running server never notices;
#   3. stop radiusd, start it on the new tree, wait for "Ready to process
#      requests" — a server that exits or never says so is stopped, LINK goes
#      back to the previous colour, and that tree is started again;
#   4. write the answer.
# `mode=startup` skips 2-3 when the tree equals the running one, so a restart of
# the control plane does not restart FreeRADIUS for nothing.
#
# The detail line is the console's, so it is sanitised: one line, bounded, and
# with any line naming a password or a secret dropped before it is written.
set -u

HANDOFF=${FOYER_RADDB_HANDOFF:-/var/lib/foyer/raddb}
LIVE=${FOYER_RADDB_LIVE:-/var/lib/foyer/raddb-live}
LINK=${FOYER_RADDB_LINK:-/etc/raddb}
RUN=${FOYER_SUPERVISOR_RUN:-/run/foyer-supervisor}
RADIUSD=${RADIUSD:-radiusd}
GROUP=${FOYER_RADDB_GROUP:-foyer-radius-tls}
POLL=${FOYER_SUPERVISOR_POLL:-1}
READY_SECONDS=${FOYER_SUPERVISOR_READY_SECONDS:-30}
PROTOCOL=1

# FreeRADIUS's own database password. The control plane generates the tree with
# `password = $ENV{FOYER_RADIUS_DB_PASSWORD}` in mods-available/sql, so the
# control plane's container holds no FreeRADIUS credential; radiusd expands the
# variable when it parses the tree (on 3.2.10). This container is the one given
# the docker secret, and it is exported here, before radiusd -XC and radiusd are
# run, and to nothing else. A value already in the environment wins — the lab
# passes it that way. Absent both, radiusd's SQL login fails and it does not start,
# which is the fail-closed answer.
DBPW_FILE=${FOYER_RADIUS_DB_PASSWORD_FILE:-/run/secrets/radius_db_password}
if [ -z "${FOYER_RADIUS_DB_PASSWORD:-}" ] && [ -r "$DBPW_FILE" ]; then
	FOYER_RADIUS_DB_PASSWORD=$(cat "$DBPW_FILE")
	export FOYER_RADIUS_DB_PASSWORD
fi

rpid=""
reader=""

# The user every radiusd runs as. Not a setting: both images create it and refuse
# to build without it (internal/raddb.ServerUser), and the one other value anyone
# could give it is the value this exists to refuse.
SERVER_USER=freerad

# The files, directories and links a generated tree may hold: what
# internal/raddb's Generator writes with every module it can enable, and the
# EAP-TLS material cmd/foyer writes beside it (certs/foyer-eap).
# TestTheSupervisorAllowsExactlyTheGeneratedTree holds this list equal to the
# generator's output in both directions, so a new generated file fails a test
# rather than every apply in the field. A link is allowed only as
# <kind>-enabled/<name> -> ../<kind>-available/<name>, naming an allowed file.
TREE_FILES="radiusd.conf clients.conf dictionary
mods-available/sql mods-available/rest mods-available/pap mods-available/preprocess
mods-available/expr mods-available/always mods-available/eap
mods-config/sql/main/postgresql/queries.conf
sites-available/default sites-available/status sites-available/dynamic-clients
certs/foyer-eap/server.pem certs/foyer-eap/server.key certs/foyer-eap/ca.pem"
TREE_DIRS="mods-available mods-enabled mods-config mods-config/sql mods-config/sql/main
mods-config/sql/main/postgresql sites-available sites-enabled certs certs/foyer-eap"

log() { printf 'foyer-supervisor: %s\n' "$*" >&2; }
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

write_file() {
	tmp="$1.tmp.$$"
	printf '%s\n' "$2" >"$tmp" && chmod 0644 "$tmp" && mv -f "$tmp" "$1"
}

# One line, no credential, at most 600 characters.
sanitise() {
	grep -v -i -e password -e secret | tr '\r\n\t' '   ' | tr -s ' ' | cut -c1-600
}

# The lines of radiusd's output worth an operator's time: its errors, else its
# last words. Info and Debug lines are never reasons — 3.2.10 prints "Info: Debug
# state unknown" at every start, and "unknown" is one of the words matched.
errlines() {
	f=$1
	hits=$(grep -v -E '(^|[[:space:]])(Info|Debug):' "$f" |
		grep -i -E 'error|fail|unknown|invalid|expect|missing|refus|cannot|could not|no such' | tail -5)
	[ -n "$hits" ] || hits=$(tail -3 "$f")
	printf '%s\n' "$hits" | sanitise
}

answer() { # generation state restarted detail
	write_file "$HANDOFF/status" "protocol=$PROTOCOL
generation=$1
state=$2
restarted=$3
at=$(now)
detail=$(printf '%s' "$4" | sanitise)"
	log "generation $1: $2 (restarted: $3) $4"
}

colour() { # the colour LINK points at, or nothing
	t=$(readlink "$LINK" 2>/dev/null) || return 0
	basename "$t"
}

point() { # colour | "" — LINK at LIVE/<colour>, or no LINK at all
	if [ -n "$1" ]; then ln -sfn "$LIVE/$1" "$LINK"; else rm -f "$LINK"; fi
}

listed() { # word list — whether word is one of list's words
	for w in $2; do [ "$w" = "$1" ] && return 0; done
	return 1
}

# check_tree prints the first entry of the tree in $1 that is not one the
# generator writes, and nothing for a tree that is only those. A name with any
# character outside [A-Za-z0-9._/-] is refused before it is compared, so no
# name can be two words of the list.
check_tree() {
	[ -f "$1/radiusd.conf" ] && [ ! -L "$1/radiusd.conf" ] || {
		echo radiusd.conf
		return
	}
	(cd "$1" && find . -mindepth 1) | while IFS= read -r p; do
		p=${p#./}
		case $p in *[!A-Za-z0-9._/-]* | '' | .* | */.*)
			echo "$p"
			return
			;;
		esac
		if [ -L "$1/$p" ]; then
			case $p in
			mods-enabled/*) want="../mods-available/${p#mods-enabled/}" ;;
			sites-enabled/*) want="../sites-available/${p#sites-enabled/}" ;;
			*) want="" ;;
			esac
			if [ -z "$want" ] || [ "$(readlink "$1/$p")" != "$want" ] || ! listed "${want#../}" "$TREE_FILES"; then
				echo "$p"
				return
			fi
		elif [ -d "$1/$p" ]; then
			listed "$p" "$TREE_DIRS" || {
				echo "$p"
				return
			}
		elif [ -f "$1/$p" ]; then
			listed "$p" "$TREE_FILES" || {
				echo "$p"
				return
			}
		else
			echo "$p"
			return
		fi
	done
}

start_radiusd() {
	rm -f "$RUN/ready" "$RUN/fifo" "$RUN/startup.log"
	mkfifo "$RUN/fifo"
	# The reader copies radiusd's output to ours (docker logs) and notes the
	# line that says it is serving. Everything before that line is kept, bounded
	# by what a start prints, so a failed start can say why.
	(
		while IFS= read -r line; do
			printf '%s\n' "$line"
			if [ ! -f "$RUN/ready" ]; then
				printf '%s\n' "$line" >>"$RUN/startup.log"
				case $line in *"Ready to process requests"*) : >"$RUN/ready" ;; esac
			fi
		done <"$RUN/fifo"
	) &
	reader=$!
	# $AS_SERVER unquoted on purpose: it is empty or setpriv's fixed words.
	$AS_SERVER "$RADIUSD" -f -l stdout -d "$LINK" >"$RUN/fifo" 2>&1 &
	rpid=$!
	i=0
	while [ "$i" -lt $((READY_SECONDS * 4)) ]; do
		[ -f "$RUN/ready" ] && return 0
		if ! kill -0 "$rpid" 2>/dev/null; then
			wait "$rpid" 2>/dev/null
			rpid=""
			wait "$reader" 2>/dev/null
			return 1
		fi
		sleep 0.25
		i=$((i + 1))
	done
	return 1
}

stop_radiusd() {
	if [ -n "$rpid" ]; then
		kill "$rpid" 2>/dev/null
		i=0
		while kill -0 "$rpid" 2>/dev/null && [ "$i" -lt 60 ]; do
			sleep 0.25
			i=$((i + 1))
		done
		kill -9 "$rpid" 2>/dev/null
		wait "$rpid" 2>/dev/null
	fi
	rpid=""
	[ -n "$reader" ] && wait "$reader" 2>/dev/null
	reader=""
}

install_colour() { # colour source
	dst="$LIVE/$1"
	rm -rf "$dst" && mkdir -p "$LIVE" && cp -RP "$2" "$dst" || return 1
	if [ "$(id -u)" = 0 ]; then
		chown -R "root:$GROUP" "$dst" && chmod -R u=rwX,g=rX,o= "$dst" || return 1
	fi
}

apply() { # generation mode
	gen=$1
	mode=$2
	src="$HANDOFF/gen-$gen"
	if [ ! -d "$src" ]; then
		answer "$gen" failed no "the generated tree gen-$gen is not in the handoff directory"
		return
	fi
	prev=$(colour)
	if [ "$mode" = startup ] && [ -n "$prev" ] && diff -r -q "$src" "$LIVE/$prev" >/dev/null 2>&1; then
		if [ -n "$rpid" ]; then
			answer "$gen" applied no "FreeRADIUS is already running this configuration"
		elif start_radiusd; then
			answer "$gen" applied yes ""
		else
			answer "$gen" failed no "FreeRADIUS did not start: $(errlines "$RUN/startup.log")"
		fi
		return
	fi

	case $prev in blue) next=green ;; *) next=blue ;; esac
	bad=$(check_tree "$src")
	if [ -n "$bad" ]; then
		answer "$gen" failed no "the generated tree holds $bad, which is not a file Foyer generates, so FreeRADIUS kept running the previous configuration"
		return
	fi
	if ! install_colour "$next" "$src"; then
		answer "$gen" failed no "the generated tree could not be copied into place"
		return
	fi
	point "$next"
	if ! $AS_SERVER "$RADIUSD" -XC -d "$LINK" >"$RUN/check.log" 2>&1; then
		point "$prev"
		answer "$gen" failed no "radiusd -C refused the configuration, and FreeRADIUS kept running the previous one: $(errlines "$RUN/check.log")"
		return
	fi

	stop_radiusd
	if start_radiusd; then
		answer "$gen" applied yes ""
		return
	fi
	why=$(errlines "$RUN/startup.log")
	stop_radiusd
	point "$prev"
	if [ -n "$prev" ] && start_radiusd; then
		answer "$gen" failed yes "FreeRADIUS did not start on the new configuration, so the previous one is running again: $why"
	else
		answer "$gen" failed yes "FreeRADIUS did not start on the new configuration and nothing is running: $why"
	fi
}

shutdown() {
	stop_radiusd
	exit 0
}

mkdir -p "$RUN" "$LIVE" "$HANDOFF"
trap shutdown TERM INT

# Every radiusd starts as SERVER_USER, its supplementary groups (foyer-radius-tls,
# for the EAP key) and no capabilities, with no_new_privs so nothing it executes
# can gain any — whatever the tree's `security { user = }` says. As root that
# needs setpriv (util-linux, in both base images); without it, or without the
# user, this refuses to start at all rather than run radiusd as root. A
# supervisor that is not root has no privilege to drop, and radiusd started from
# it has none either.
AS_SERVER=""
if [ "$(id -u)" = 0 ]; then
	if ! command -v setpriv >/dev/null 2>&1; then
		log "setpriv is not installed, and radiusd is never started as root; refusing to start"
		exit 1
	fi
	uid=$(id -u "$SERVER_USER" 2>/dev/null)
	case $uid in
	'' | 0 | *[!0-9]*)
		log "the user $SERVER_USER does not exist or is root, and radiusd is never started as root; refusing to start"
		exit 1
		;;
	esac
	AS_SERVER="setpriv --reuid=$SERVER_USER --regid=$SERVER_USER --init-groups --no-new-privs --inh-caps=-all --bounding-set=-all --"
fi

# The image may ship a real directory where LINK goes; a symlink created over it
# would land inside it.
if [ -d "$LINK" ] && [ ! -L "$LINK" ]; then
	mv "$LINK" "$LINK.image"
fi

write_file "$HANDOFF/supervisor" "protocol=$PROTOCOL
started=$(now)"

# A container restart keeps its filesystem, and with it the tree that was
# running: serve it again at once rather than waiting for the control plane.
if [ -n "$(colour)" ]; then
	start_radiusd || log "the tree that was running before this restart did not start again"
fi

handled=""
first=1
waiting=0
while :; do
	gen=""
	mode=apply
	if [ -f "$HANDOFF/request" ]; then
		gen=$(sed -n 's/^generation=//p' "$HANDOFF/request" | head -1)
		mode=$(sed -n 's/^mode=//p' "$HANDOFF/request" | head -1)
	fi
	case $gen in
	'' | *[!0-9]*)
		if [ "$waiting" = 0 ]; then
			log "waiting for the control plane's first generation in $HANDOFF"
			waiting=1
		fi
		;;
	*)
		if [ "$gen" != "$handled" ]; then
			# The first request this process sees may be one a previous process
			# already answered; restart only if the tree differs.
			[ "$first" = 1 ] && mode=startup
			[ "$mode" = startup ] || mode=apply
			apply "$gen" "$mode"
			handled=$gen
			first=0
		fi
		;;
	esac
	if [ -n "$rpid" ] && ! kill -0 "$rpid" 2>/dev/null; then
		wait "$rpid" 2>/dev/null
		log "radiusd exited on its own; exiting so the container restarts"
		exit 1
	fi
	sleep "$POLL" &
	wait $!
done
