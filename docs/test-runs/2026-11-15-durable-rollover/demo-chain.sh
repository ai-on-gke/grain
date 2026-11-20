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

# Starts `grain demo -github-sim -fake-ci -reconcile 2s` on 127.0.0.1:4290,
# rolls the demo's seeded successor (22, of 21) over once more so the
# chain is 21 -> 22 -> 23, and gives 22 and 23 a long title. Prints the
# access code to demo/tok under $OUT (default /tmp/demo).

set -euo pipefail
GRAIN=${GRAIN:-/home/debian/bin/grain}
OUT=${OUT:-/tmp/demo}
PORT=${PORT:-4290}
mkdir -p "$OUT"
[[ -f "$OUT/demo.pid" ]] && kill "$(cat "$OUT/demo.pid")" 2>/dev/null || true
sleep 1
setsid "$GRAIN" demo -github-sim -fake-ci -reconcile 2s -addr "127.0.0.1:$PORT" >"$OUT/demo.log" 2>&1 </dev/null &
echo $! >"$OUT/demo.pid"
for _ in $(seq 100); do
	code=$(sed -n 's/.*the access code is \([A-Z0-9-]*\) .*/\1/p' "$OUT/demo.log" | head -n1)
	[[ -n "$code" ]] && curl -sf "http://127.0.0.1:$PORT/api/tasks" -H "Authorization: Bearer $code" >/dev/null && break
	sleep 0.2
done
echo "$code" >"$OUT/tok"
api() { curl -fsS -X "$1" -H "Authorization: Bearer $code" -H 'Content-Type: application/json' ${3:+-d "$3"} "http://127.0.0.1:$PORT$2"; }
api GET "/api/tasks/21" | jq -e '.rolledOverTo == "22"' >/dev/null
api POST /api/tasks/22/rollover '{}'
echo
long='Watch the acme/widgets nightly build and keep its flaky integration-suite quarantine list, release-notes digest and dependency-bump backlog current across every supported branch'
for id in 22 23; do api PATCH "/api/tasks/$id" "$(jq -nc --arg t "$long" '{title:$t}')" >/dev/null; done
api GET /api/tasks/22 | jq -c '{id, rolledOverFrom, rolledOverTo}'
