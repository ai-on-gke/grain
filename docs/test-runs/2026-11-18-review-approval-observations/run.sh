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

# docs/test-plans/2026-11-18.md §2: runs the 2026-11-14 review-context walk
# (security review, path-trigger pr-review, force-push, base merge, fork PR)
# on a real daemon with GRAIN_GATE_OBSERVATION_TASKS set (ALLOW=set, tasks
# 1-32) or empty (ALLOW=empty), sampling legacy merge gates and observations
# of every task throughout (sample.sh), then runs extras.sh (Accept findings,
# takeover, approval receipts, repository move, bounds, latency) against the
# same daemon and exports normalized task rows (comments included) for diffing.
#   STORE=sqlite|postgres ALLOW=set|empty ./run.sh
set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
SELF=$HERE
STORE=${STORE:-sqlite}
ALLOW=${ALLOW:-set}
RUN=$HERE/out/$STORE-$ALLOW
WORK=/home/debian/walk-obs
rm -rf "$RUN"
mkdir -p "$RUN/obs"
if [[ $ALLOW == set ]]; then
	seq -s, 1 32 >/home/debian/obs-allowlist
else
	: >/home/debian/obs-allowlist
fi
if [[ $STORE == postgres ]]; then
	sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS grain_walk WITH (FORCE)" -c "CREATE DATABASE grain_walk OWNER grain"
fi
for p in daemon ghsim; do [[ -f $WORK/$p.pid ]] && kill "$(cat "$WORK/$p.pid")" 2>/dev/null || true; done
sleep 1
rm -rf "$WORK"
OUT=$RUN/obs WORK_DIR=$WORK DAEMON=http://127.0.0.1:4280 "$HERE/sample.sh" &
SAMPLER=$!
trap 'touch "$RUN/obs/stop-sampler"; wait $SAMPLER || true' EXIT
STORE=$STORE OUT=$RUN/walk WORK=$WORK GRAIN=$HERE/grain-obs.sh bash "$HERE/../2026-11-14-review-context/walk.sh"
# shellcheck source=../2026-11-14-review-context/env.sh
source "$HERE/../2026-11-14-review-context/env.sh"
CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
export CODE DAEMON WORK_DIR=$WORK RUN STORE ALLOW
if [[ -z ${SKIP_EXTRAS:-} ]]; then
	bash "$SELF/extras.sh" 2>&1 | tee "$RUN/extras.log" || echo "extras.sh failed; see $RUN/extras.log"
fi
sleep 15
touch "$RUN/obs/stop-sampler"
wait $SAMPLER || true
trap - EXIT
# Normalized rows: drop timestamps, SHAs, run/token-ish noise and the
# observation sidecars so the set/empty runs diff on legacy facts only.
# Comments and state history are part of each task row.
mkdir -p "$RUN/rows"
for id in $(api GET '/api/tasks?limit=500' | jq -r '.[].id' | sort -n); do
	api GET "/api/tasks/$id" | jq -S 'del(.gate_observations, .gate_observation_diagnostics)
	  | walk(if type == "string" then (gsub("[0-9a-f]{40}"; "<sha>") | gsub("[0-9a-f]{7,12}\\b"; "<short>")
	      | gsub("[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9:.]+(Z|[+-][0-9:]+)"; "<time>")) else . end)
	  | walk(if type == "object" then with_entries(select(.key | test("(At|_at|Time|time|Seconds|duration|Duration|cost|Cost|tokens|Tokens|elapsed)$") | not)) else . end)' >"$RUN/rows/task$id.json"
done
"$SELF/summarize.sh" "$RUN" >"$RUN/summary.txt" || true
[[ -n ${KEEP_DAEMON:-} ]] || stop_env "$WORK"
