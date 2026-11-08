#!/usr/bin/env bash
# Copyright 2026 Google LLC
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# §1 item 3 (drain): with conn-alice's tunnel on replica A (the daemon dials
# the registry through frontlb.go on :4259), hold an SSE stream open through
# A, drive steady traffic through B and through the front LB, SIGTERM A and
# count every status code clients saw.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
. "$HERE/lib.sh"
OUT="$WALK/drain"
rm -rf "$OUT" && mkdir -p "$OUT"
H=(-H "Host: conn-alice.grain-user.test" -H "X-Test-Email: alice@example.com")

# Put conn-alice's tunnel on A: make sure A is up, then bounce B so the daemon
# redials through the front LB (which prefers A).
SCOPE_OWNERS=1 ONLY=a "$HERE/env-up.sh" >/dev/null
SCOPE_OWNERS=1 ONLY=b RESTART=1 "$HERE/env-up.sh" >/dev/null
sleep 4
echo "daemon tunnel dials (front LB): $(grep -c -- '-> 127.0.0.1:4251' "$WALK/logs/frontlb.log") to A, $(grep -c -- '-> 127.0.0.1:4261' "$WALK/logs/frontlb.log") to B; latest: $(tail -n 1 "$WALK/logs/frontlb.log")"
ASSET=$(curl -sS "${H[@]}" "$RB/" | grep -o 'src="/assets/[^"]*"' | head -n 1 | cut -d'"' -f2)
echo "tunnel sessions before: $("${PSQL[@]}" -c "select entry_name||'@'||replica_id from registry.tunnel_sessions" 2>/dev/null | tr '\n' ' ')"
# Long-lived SSE stream through replica A.
# NO_SSE=1 skips it, to compare a drain with no long-lived stream on the tunnel.
[ "${NO_SSE:-0}" = 1 ] && SSEURL="$RA/healthz-none" || SSEURL="$RA/api/events?stream=1"
curl -sS -N --max-time 25 -o "$OUT/sse-a.out" -w '%{http_code} %{time_total}s\n' "${H[@]}" -H 'Accept: text/event-stream' "$SSEURL" >"$OUT/sse-a.status" 2>"$OUT/sse-a.err" &
SSE=$!
# A slow-reading plain HTTP request through A.
curl -sS --limit-rate 40k -o "$OUT/slow-a.out" -w '%{http_code} %{size_download}B %{time_total}s\n' "${H[@]}" -H 'Accept-Encoding: identity' "$RA$ASSET" >"$OUT/slow-a.status" 2>"$OUT/slow-a.err" &
SLOW=$!
load() { # load <label> <base-url>
  local end=$(($(date +%s) + 14))
  while [ "$(date +%s)" -lt "$end" ]; do
    code=$(curl -sS -o "$OUT/$1.body" -w "%{http_code}" --max-time 10 "${H[@]}" "$2/api/whoami" 2>>"$OUT/$1.err") || code=curl-fail
    echo "$code" >>"$OUT/$1.codes"
    [ "$code" = 200 ] || echo "$(date -u +%T.%N | cut -c1-12) $code $(head -c 300 "$OUT/$1.body")" >>"$OUT/$1.failures"
    sleep 0.05
  done
}
load via-b "$RB" &
LB1=$!
load via-lb http://127.0.0.1:4259 &
LB2=$!
sleep 3
echo "$(date -u +%T.%N | cut -c1-12) SIGTERM replica A (pid $(cat "$WALK/registry-a.pid"))"
kill -TERM "$(cat "$WALK/registry-a.pid")"
T0=$(date +%s%N)
while kill -0 "$(cat "$WALK/registry-a.pid")" 2>/dev/null; do sleep 0.05; done
echo "replica A exited after $((($(date +%s%N) - T0) / 1000000))ms"
wait $LB1 $LB2
wait $SSE $SLOW
echo "tunnel sessions after: $("${PSQL[@]}" -c "select entry_name||'@'||replica_id from registry.tunnel_sessions" 2>/dev/null | tr '\n' ' ')"
for f in via-b via-lb; do
  echo "$f status codes: $(sort "$OUT/$f.codes" | uniq -c | tr '\n' ' ')"
  [ -f "$OUT/$f.failures" ] && sed 's/^/    /' "$OUT/$f.failures"
done
echo "SSE through A: $(cat "$OUT/sse-a.status") $(wc -c <"$OUT/sse-a.out")B received; err: $(head -c 200 "$OUT/sse-a.err")"
echo "slow HTTP through A: $(cat "$OUT/slow-a.status") err: $(head -c 200 "$OUT/slow-a.err")"
echo "daemon log during drain:"
grep "registry tunnel\|registry heartbeat" "$WALK/logs/d1.log" | tail -n 6 | sed 's/^/    /'
echo "replica A log tail:"
tail -n 6 "$WALK/logs/registry-a.log" | sed 's/^/    /'
