#!/usr/bin/env bash
# body-flood.sh — anonymous request-body flood, the half a sign-in flood cannot see.
#
# Question: can a caller with NO session and NO password exhaust the 320M control
# plane by sending request bodies the console accepts, and does the Policy API keep
# answering FreeRADIUS while it happens?
#
# The request validator decodes a JSON body into a tree before the gate looks at
# the caller, and for a body of small values the tree is tens of times the text.
# This sends ROUNDS x 50 anonymous 1 MiB `[0,0,0,...]` bodies at once (HTTP/1.1,
# chunked, so no declared length can refuse them unread) to POST /api/v0/accounts,
# and probes FreeRADIUS with radclient during it.
#
# Needs the lab up (`make lab-up`, and rebuild with
# `docker compose -f lab/docker-compose.yml up -d --build`, so the running image
# is the tree being measured). It changes the running lab: it caps
# foyer-lab-backend at 320M and restarts it, and replaces the RADIUS client for
# 127.0.0.1. `make lab-down` undoes it. Results: $OUT and stdout.
#
# HTTP/1.1 on purpose. Over HTTP/2, with the same hand-set `Transfer-Encoding`,
# an unbounded tree answered all 150 with 413 and never decoded a body, so such a
# run measures nothing.
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OUT=${OUT:-$ROOT/lab/.state/body-flood}
LIMIT=${LIMIT:-320m}
ROUNDS=${ROUNDS:-3}
BACKEND=foyer-lab-backend
FR=foyer-lab-freeradius
PG=foyer-lab-postgres
URL=${URL:-https://localhost:8443/api/v0/accounts}

mkdir -p "$OUT"
rm -f "$OUT"/*.log "$OUT"/*.json

for c in $BACKEND $FR $PG; do
  docker inspect "$c" > /dev/null 2>&1 || { echo "$c is not running: make lab-up first" >&2; exit 2; }
done

health() {
  for _ in $(seq 1 90); do
    curl -sk -o /dev/null -w '%{http_code}' https://localhost:8443/healthz 2> /dev/null | grep -q 200 && return 0
    sleep 1
  done
  return 1
}

echo "== cap $BACKEND at $LIMIT and restart it"
docker update --memory "$LIMIT" --memory-swap "$LIMIT" $BACKEND > /dev/null
docker restart $BACKEND > /dev/null
health || { echo "backend did not come back" >&2; exit 1; }
docker logs $BACKEND 2>&1 | grep 'memory: Go soft limit' | tail -1

python3 -c "import sys; sys.stdout.write('[' + '0,'*524280 + '0]')" > "$OUT/big.json"
echo "== one request ($(wc -c < "$OUT/big.json" | tr -d ' ') bytes)"
curl -sk --http1.1 -o "$OUT/one.json" -w '%{http_code}\n' -H 'Content-Type: application/json' \
  -H 'Transfer-Encoding: chunked' --data-binary @"$OUT/big.json" "$URL"
head -c 300 "$OUT/one.json"
echo

echo "== register a RADIUS client for radclient (127.0.0.1 inside $FR)"
SECRET=$(openssl rand -hex 16)
docker exec -i $PG psql -q -U foyer -d foyer > /dev/null << SQL
DELETE FROM nas WHERE nasname = '127.0.0.1';
INSERT INTO nas (nasname, shortname, type, secret, require_message_authenticator, limit_proxy_state)
VALUES ('127.0.0.1', 'body-flood', 'other', '$SECRET', true, true);
SQL
docker restart $FR > /dev/null
sleep 3

now_ms() { python3 -c 'import time; print(int(time.time() * 1000))'; }
probe() { # prints "<ms> <answered:0|1>"
  local t0 t1 out answered=0
  t0=$(now_ms)
  out=$(printf 'User-Name = "flood-probe"\nUser-Password = "irrelevant"\nNAS-IP-Address = 127.0.0.1\nCalling-Station-Id = "02:11:22:33:44:55"\nMessage-Authenticator = 0x00\n' |
    docker exec -i $FR radclient -x -t 5 -r 1 127.0.0.1:1812 auth "$SECRET" 2>&1 || true)
  t1=$(now_ms)
  grep -q 'Received Access-' <<< "$out" && answered=1
  echo "$((t1 - t0)) $answered"
}
oom_lines() { docker run --rm --privileged alpine dmesg 2> /dev/null | grep -c 'Killed process .*(foyer)'; }

echo "== probes at rest"
for _ in 1 2; do probe; done | tee "$OUT/probe-idle.log"
OOM_BEFORE=$(oom_lines)
RESTARTS_BEFORE=$(docker inspect -f '{{.RestartCount}}' $BACKEND)

(while :; do docker stats --no-stream --format '{{.MemUsage}}' $BACKEND 2> /dev/null || true; sleep 0.5; done) > "$OUT/mem.log" &
SAMPLER=$!
trap 'kill $SAMPLER 2> /dev/null || true' EXIT

echo "== flood: $ROUNDS x 50 anonymous 1 MiB bodies at once, with RADIUS probes during it"
PIDS=()
for _ in $(seq 1 "$ROUNDS"); do
  (seq 1 50 | xargs -P 50 -I{} curl -sk --http1.1 -o /dev/null -w '%{http_code}\n' --max-time 20 \
    -H 'Content-Type: application/json' -H 'Transfer-Encoding: chunked' \
    --data-binary @"$OUT/big.json" "$URL" >> "$OUT/codes.log" 2>&1) &
  PIDS+=($!)
done
sleep 0.5
for _ in 1 2 3 4 5 6; do probe; sleep 0.3; done | tee "$OUT/probe-flood.log"
for p in "${PIDS[@]}"; do wait "$p"; done
sleep 3
kill $SAMPLER 2> /dev/null || true
health || echo "backend not healthy after the flood"

PEAK=$(awk '{v=$1; u=v; gsub(/[0-9.]/,"",u); gsub(/[A-Za-z]/,"",v);
  if (u=="GiB") v*=1024; else if (u=="KiB") v/=1024; else if (u=="B") v/=1048576;
  if (v>m) m=v} END {printf "%.1f", m}' "$OUT/mem.log")
{
  echo "answers:             $(sort "$OUT/codes.log" | uniq -c | tr '\n' ' ') (000 = connection lost)"
  echo "restarts:            $RESTARTS_BEFORE -> $(docker inspect -f '{{.RestartCount}}' $BACKEND)"
  echo "kernel OOM kills of foyer: $OOM_BEFORE -> $(oom_lines)"
  echo "peak memory:         ${PEAK} MiB (docker stats samples); cgroup memory.peak $(($(docker exec $BACKEND cat /sys/fs/cgroup/memory.peak 2> /dev/null || echo 0) / 1048576)) MiB (current process)"
  echo "RADIUS during flood: $(awk '$2==1' "$OUT/probe-flood.log" | wc -l | tr -d ' ')/6 answered; slowest $(awk '{if ($1>m) m=$1} END {print m+0}' "$OUT/probe-flood.log") ms"
} | tee "$OUT/verdict.log"
