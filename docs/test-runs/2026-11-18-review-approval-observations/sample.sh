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

# Samples every task's legacy state, merge gates and gate observations from
# GET /api/tasks/{id} every $SAMPLE_EVERY seconds into $OUT/samples.jsonl until
# $OUT/stop-sampler exists. One line per task and sample:
#   {t, ms, id, state, title, gates:[id=state], obs:[review/approval binding = status]}
set -euo pipefail
: "${OUT:?}" "${DAEMON:?}" "${WORK_DIR:?}"
while [[ ! -f "$OUT/stop-sampler" ]]; do
	CODE=$(tr -d '[:space:]' <"$WORK_DIR/data/local-api-token" 2>/dev/null) || { sleep 1; continue; }
	for id in $(curl -sS -H "Authorization: Bearer $CODE" "$DAEMON/api/tasks?limit=200" | jq -r '.[].id' 2>/dev/null); do
		start=$(date +%s.%N)
		body=$(curl -sS -H "Authorization: Bearer $CODE" "$DAEMON/api/tasks/$id") || continue
		end=$(date +%s.%N)
		jq -c --arg t "$(date -u +%H:%M:%S)" --arg ms "$(echo "($end-$start)*1000" | bc)" '{t: $t, ms: ($ms|tonumber), id, state, title: .title[0:50],
		  gates: [(.mergeGates // [])[] | "\(.id)=\(.state)"],
		  obs: (if .gate_observations == null then null else
		    [ .gate_observations as $v | $v.bindings[] | select(.type == "review" or .type == "approval") as $b
		      | ($v.results[] | select(.result.binding_id | endswith("/" + $b.id))) as $r
		      | "\($b.type) \($b.config.semantics) \($b.config.requirement_id // ($b.config.requirement_ids | join(","))) = \($r.result.status)" ] end)}' <<<"$body" >>"$OUT/samples.jsonl" || true
	done
	sleep "${SAMPLE_EVERY:-2}"
done
