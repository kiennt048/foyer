#!/usr/bin/env bash
# login-flood.sh — console sign-in flood against a memory-capped control plane.
#
# Question: with the control plane capped at the deployment's 320M, do >= 50
# parallel console sign-ins OOM the process, and does a RADIUS Access-Request
# sent through FreeRADIUS during the flood still get an answer from the Policy API?
#
# Needs the lab up (`make lab-up`) and nothing else. It changes the running lab:
# it caps foyer-lab-backend at 320M and restarts it (so the process sizes its
# argon2id bound from that limit, as it does on the deployment), registers a
# RADIUS client for 127.0.0.1 in the lab database, and restarts FreeRADIUS so it
# loads it. `make lab-down` undoes all of it.
#
# Two phases:
#   A. 60 sign-ins from ONE address — what the per-address bucket does.
#   B. FLOODERS x PER sign-ins at once from FLOODERS addresses, every username
#      distinct, so neither bucket stops them and the argon2id semaphore is the only
#      bound on memory. While it runs, radclient sends Access-Requests through
#      FreeRADIUS and times each answer.
#
# Output: $OUT (default lab/.state/login-flood/), and a summary on stdout.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OUT=${OUT:-$ROOT/lab/.state/login-flood}
LIMIT=${LIMIT:-320m}
FLOODERS=${FLOODERS:-6}
PER=${PER:-10}
PROBES=${PROBES:-8}
BACKEND=foyer-lab-backend
FR=foyer-lab-freeradius
PG=foyer-lab-postgres
FLOOD_IMAGE=${FLOOD_IMAGE:-postgres:16-alpine}

mkdir -p "$OUT"
rm -f "$OUT"/*.json "$OUT"/*.log

for c in $BACKEND $FR $PG; do
  docker inspect "$c" > /dev/null 2>&1 || { echo "$c is not running: make lab-up first" >&2; exit 2; }
done
NET=$(docker inspect -f '{{range $k, $v := .NetworkSettings.Networks}}{{$k}} {{end}}' $BACKEND | awk '{print $1}')

wait_backend() {
  for _ in $(seq 1 60); do
    if curl -sk -o /dev/null -w '%{http_code}' https://localhost:8443/healthz 2> /dev/null | grep -q 200; then return 0; fi
    sleep 1
  done
  echo "backend did not come back" >&2
  exit 1
}

echo "== cap $BACKEND at $LIMIT (memory and swap), restart it so it sizes itself from the limit"
docker update --memory "$LIMIT" --memory-swap "$LIMIT" $BACKEND > /dev/null
docker restart $BACKEND > /dev/null
wait_backend
PLAN=$(docker logs $BACKEND 2>&1 | grep 'memory: Go soft limit' | tail -1 || true)
echo "   ${PLAN:-no memory plan line in the log: this backend predates the fix}"
RESTARTS_BEFORE=$(docker inspect -f '{{.RestartCount}}' $BACKEND)

echo "== register a RADIUS client for radclient (127.0.0.1 inside $FR)"
SECRET=$(openssl rand -hex 16)
docker exec -i $PG psql -q -U foyer -d foyer > /dev/null << SQL
DELETE FROM nas WHERE shortname = 'login-flood';
INSERT INTO nas (nasname, shortname, type, secret, require_message_authenticator, limit_proxy_state)
VALUES ('127.0.0.1', 'login-flood', 'other', '$SECRET', true, true);
SQL
docker restart $FR > /dev/null
sleep 3

now_ms() { python3 -c 'import time; print(int(time.time() * 1000))'; }

# radius_answer: policy | unavailable | degraded | unexplained | none. An
# Access-Reject is not an answer from the Policy API by itself: the tree answers
# one of its own, "policy engine unavailable", when the Policy API is down.
. "$ROOT/lab/measure/radius-answer.sh"

probe() { # prints "<ms> <policy|unavailable|degraded|unexplained|none>"
  local t0 t1 out
  t0=$(now_ms)
  out=$(printf 'User-Name = "flood-probe"\nUser-Password = "irrelevant"\nNAS-IP-Address = 127.0.0.1\nCalling-Station-Id = "02:11:22:33:44:55"\nMessage-Authenticator = 0x00\n' |
    docker exec -i $FR radclient -x -t 5 -r 1 127.0.0.1:1812 auth "$SECRET" 2>&1 || true)
  t1=$(now_ms)
  echo "$((t1 - t0)) $(radius_answer <<< "$out")"
}

echo "== probe before the flood"
for _ in 1 2 3; do probe; done | tee "$OUT/probe-idle.log"

echo "== build the flood tool for the daemon's platform"
ARCH=$(docker version -f '{{.Server.Arch}}')
(cd "$ROOT" && GOOS=linux GOARCH="$ARCH" CGO_ENABLED=0 go build -o "$OUT/loginflood" ./lab/measure/loginflood)

flood() { # $1 = prefix, $2 = n
  docker run --rm --network "$NET" -v "$OUT/loginflood:/loginflood:ro" \
    --entrypoint /loginflood "$FLOOD_IMAGE" -n "$2" -prefix "$1"
}

echo "== sample memory every 0.5 s"
(while :; do docker stats --no-stream --format '{{.MemUsage}}' $BACKEND 2> /dev/null || true; sleep 0.5; done) > "$OUT/mem.log" &
SAMPLER=$!
trap 'kill $SAMPLER 2> /dev/null || true' EXIT

echo "== phase A: 60 sign-ins from one address"
flood "flooda-$RANDOM" 60 | tee "$OUT/phaseA.json"

echo "== phase B: $FLOODERS addresses x $PER sign-ins at once, with $PROBES RADIUS probes during it"
FLOOD_PIDS=()
for i in $(seq 1 "$FLOODERS"); do
  flood "floodb$i-$RANDOM" "$PER" > "$OUT/phaseB-$i.json" &
  FLOOD_PIDS+=($!)
done
sleep 0.3
for _ in $(seq 1 "$PROBES"); do probe; sleep 0.25; done | tee "$OUT/probe-flood.log"
for p in "${FLOOD_PIDS[@]}"; do wait "$p" || true; done
cat "$OUT"/phaseB-*.json
sleep 2
kill $SAMPLER 2> /dev/null || true

echo "== verdict"
OOM=$(docker inspect -f '{{.State.OOMKilled}}' $BACKEND)
STATUS=$(docker inspect -f '{{.State.Status}} exit={{.State.ExitCode}}' $BACKEND)
RESTARTS_AFTER=$(docker inspect -f '{{.RestartCount}}' $BACKEND)
# The cgroup's own high-water mark and OOM counter: exact, where docker stats is a
# sample every second or two and can miss a 64 MiB spike. Reset by a restart, so
# after an OOM kill these describe the new process and the restart count is the
# evidence.
CG_PEAK=$(docker exec $BACKEND cat /sys/fs/cgroup/memory.peak 2> /dev/null || echo 0)
CG_OOM=$(docker exec $BACKEND sh -c 'grep "^oom_kill " /sys/fs/cgroup/memory.events' 2> /dev/null || echo "oom_kill ?")
PEAK=$(awk '{v=$1; u=v; gsub(/[0-9.]/,"",u); gsub(/[A-Za-z]/,"",v);
  if (u=="GiB") v*=1024; else if (u=="KiB") v/=1024; else if (u=="B") v/=1048576;
  if (v>m) m=v} END {printf "%.1f", m}' "$OUT/mem.log")
ANSWERED=$(awk '$2=="policy"' "$OUT/probe-flood.log" | wc -l | tr -d ' ')
BREAKDOWN=$(awk '{n[$2]++} END {for (k in n) printf "%s %d, ", k, n[k]}' "$OUT/probe-flood.log" | sed 's/, $//')
SLOWEST=$(awk '{if ($1>m) m=$1} END {print m+0}' "$OUT/probe-flood.log")
{
  echo "memory plan:        ${PLAN:-none logged}"
  echo "container limit:    $LIMIT"
  echo "peak memory:        ${PEAK} MiB (docker stats samples); cgroup memory.peak $((CG_PEAK / 1048576)) MiB"
  echo "cgroup events:      $CG_OOM"
  echo "OOMKilled:          $OOM"
  echo "state:              $STATUS; restarts $RESTARTS_BEFORE -> $RESTARTS_AFTER"
  echo "RADIUS during flood: $ANSWERED/$PROBES answered by the Policy API; slowest ${SLOWEST} ms"
  echo "  by answer:         ${BREAKDOWN:-no probes} (only 'policy' counts; 'unavailable' is the tree's own reject)"
} | tee "$OUT/verdict.log"
