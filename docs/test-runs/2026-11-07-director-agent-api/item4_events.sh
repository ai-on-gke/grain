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

# §4 item 4 (events half): two GET /api/events SSE subscribers -- the
# operator (access code) and an external credential holding tasks:read on
# acme/app only -- across task creation, approval, comments, defer and close
# in acme/app, acme/other and the no-repository Director; then the
# restricted subscriber reconnects with Last-Event-ID and must replay
# exactly what it missed. Uses the daemon env.sh started.
set -uo pipefail
# shellcheck source=/dev/null
source "${WALKTHROUGH_DIR:-/var/tmp/grain-walkthrough-3488}/vars.env"
AC=(-H "X-Grain-Access-Code: ${ACCESS_CODE}" -H "Content-Type: application/json")
OUT="${WALKTHROUGH_DIR}/events"
mkdir -p "$OUT"
TOKEN="$(curl -sS -X POST "${UI_URL}/api/external-agents/tokens" "${AC[@]}" \
  -d '{"name":"events-reader","scopes":["tasks:read"],"repos":["acme/app"],"owner":"alice@example.com"}' | jq -r .token)"
BEARER=(-H "Authorization: Bearer ${TOKEN}")
post() { curl -sS -o /dev/null -X POST "${UI_URL}$1" "${AC[@]}" -d "${2:-{\}}"; }
file() { curl -sS -X POST "${UI_URL}/api/tasks" "${AC[@]}" -d "$1" | jq -r .id; }
ids() { sed -n 's/^id: //p' "$1"; }
events() { sed -n 's/^data: //p' "$1"; }

echo "## a repo-scoped credential asking for another repository's events"
curl -sS -o /dev/null -w 'GET /api/events?repo=acme/other: HTTP %{http_code}\n' "${UI_URL}/api/events?repo=acme/other" "${BEARER[@]}"

curl -sSN "${UI_URL}/api/events?stream=1" "${AC[@]}" >"$OUT/operator.sse" &
OP=$!
curl -sSN "${UI_URL}/api/events?stream=1" "${BEARER[@]}" >"$OUT/restricted-1.sse" &
RS=$!
sleep 1

echo "## phase 1: both subscribers connected"
A="$(file '{"title":"events app","repo":"acme/app","approved":false}')"
B="$(file '{"title":"events other","repo":"acme/other","approved":false}')"
post "/api/tasks/${A}/approve"
post "/api/tasks/${B}/approve"
post "/api/tasks/${A}/comments" '{"body":"comment on app"}'
post "/api/tasks/${B}/comments" '{"body":"comment on other"}'
post "/api/tasks/1/comments" '{"body":"comment on the Director"}'
sleep 1
kill "$RS" 2>/dev/null
LAST="$(ids "$OUT/restricted-1.sse" | tail -1)"
echo "restricted subscriber disconnected after id ${LAST}"

echo "## phase 2: while the restricted subscriber is away"
post "/api/tasks/${A}/comments" '{"body":"missed comment on app"}'
post "/api/tasks/${B}/close" '{"reason":"walkthrough"}'
post "/api/tasks/${A}/defer" '{"until":"2030-01-01T00:00:00Z","reason":"walkthrough"}'
sleep 1

echo "## phase 3: reconnect with Last-Event-ID: ${LAST}"
curl -sSN "${UI_URL}/api/events?stream=1" "${BEARER[@]}" -H "Last-Event-ID: ${LAST}" >"$OUT/restricted-2.sse" &
RS2=$!
sleep 1
post "/api/tasks/${A}/close" '{"reason":"walkthrough"}'
sleep 1
kill "$RS2" "$OP" 2>/dev/null
wait 2>/dev/null

echo
echo "## results (A=${A} acme/app, B=${B} acme/other, Director=1)"
for f in operator restricted-1 restricted-2; do
  printf '%-13s %2d events: %s\n' "$f" "$(ids "$OUT/$f.sse" | wc -l)" \
    "$(events "$OUT/$f.sse" | jq -r '"\(.id):\(.type):\(.taskId):\(.repo // "-"):\(.state // "")"' | tr '\n' ' ')"
done
mono() { ids "$1" | awk 'NR>1 && $1<=prev {bad=1} {prev=$1} END {print bad ? "NOT increasing" : "strictly increasing"}'; }
for f in operator restricted-1 restricted-2; do echo "ordering $f: $(mono "$OUT/$f.sse")"; done
echo "restricted events outside acme/app: $(cat "$OUT"/restricted-*.sse | sed -n 's/^data: //p' | jq -r 'select(.repo != "acme/app") | .id' | wc -l)"
OP_APP="$(events "$OUT/operator.sse" | jq -r 'select(.repo=="acme/app") | .id' | tr '\n' ' ')"
RS_ALL="$(cat "$OUT/restricted-1.sse" "$OUT/restricted-2.sse" | sed -n 's/^id: //p' | tr '\n' ' ')"
echo "operator's acme/app ids:      ${OP_APP}"
echo "restricted ids (both conns):  ${RS_ALL}"
[[ "$OP_APP" == "$RS_ALL" ]] && echo "replay: identical -- nothing missed, nothing duplicated" || echo "replay: MISMATCH"
echo "restricted ready-frame cursors: $(grep -h '^: ready' "$OUT"/restricted-*.sse | tr '\n' ' ')"
echo "## JSON paging with the restricted credential from cursor ${LAST}"
curl -sS "${UI_URL}/api/events?since=${LAST}&limit=2" "${BEARER[@]}" | jq -c '{ids:[.events[].id],cursor,hasMore}'
