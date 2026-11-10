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

# Walkthrough harness for docs/test-plans/2026-11-10.md section 8 item 2:
# the Stage 1 job histories pkg/orchestrator's
# TestStage1JobHistoriesFromTheReleaseTrainWalkthrough feeds grain, judged by
# scripts/candidate-gate.sh check-qualification (the train's judge) in the
# GitHub jobs shape release-train-task.sh reads. Prints each verdict.
#
#   bash docs/test-runs/2026-11-10-release-train/stage1-gate.sh [repo-root]
set -uo pipefail

root="$(cd "${1:-$(dirname "${BASH_SOURCE[0]}")/../../..}" && pwd)"
work="$(mktemp -d /tmp/stage1.XXXXXX)"
trap 'rm -rf "$work"' EXIT
sha=89abcdef0123456789abcdef0123456789abcdef
base=("go, shuffled and repeated (1)" "state repository soak" "sustained load" "metadata and model scalability"
  "merge liveness stress" "end-to-end long term tests" "long randomized cluster simulation" "postgres concurrency and stress"
  "reverse tunnel multiplexer stress" "ui e2e, repeated (1)" "installer trixie and hostmode" "govulncheck"
  "pkg/cli stress under load" "upgrade from previous versions")

# history <label> <name|attempt|status|conclusion>...: every base job green
# on attempt 1 unless a spec names it, then the specs as further rows.
history() {
  local label="$1" spec name attempt st concl rows=() status=0 out
  shift
  for name in "${base[@]}"; do
    local named=false
    for spec in "$@"; do [ "${spec%%|*}" != "$name" ] || named=true; done
    [ "$named" = true ] || rows+=("$(jq -cn --arg n "$name" '{name:$n, run_attempt:1, status:"completed", conclusion:"success"}')")
  done
  for spec in "$@"; do
    IFS='|' read -r name attempt st concl <<< "$spec"
    rows+=("$(jq -cn --arg n "$name" --argjson a "$attempt" --arg s "$st" --arg c "$concl" \
      '{name:$n, run_attempt:$a, status:$s, conclusion:(if $c == "" then null else $c end)}')")
  done
  printf '%s\n' "${rows[@]}" | jq -s --arg sha "$sha" '{total_count: length, jobs: (to_entries | map(.value + {id:(.key+1), run_id:7,
    workflow_name:"nightly", head_sha:$sha, created_at:"2026-11-10T00:00:00Z", started_at:"2026-11-10T00:00:00Z", completed_at:"2026-11-10T00:30:00Z"}))}' > "$work/jobs.json"
  out="$(CHECK_QUALIFICATION_APP=none bash "$root/scripts/candidate-gate.sh" check-qualification --rc-tag v1.4.0-rc.2 --rc-sha "$sha" \
    --runs-json "$work/jobs.json" 2>&1)" || status=$?
  echo "=== $label (exit $status)"
  printf '%s\n' "$out" | grep -vE '^PASS ' | sed 's/^/    /'
}

history "one job failing once then passing" "sustained load|1|completed|failure" "sustained load|2|completed|success"
history "one job failing twice" "sustained load|1|completed|failure" "sustained load|2|completed|failure"
history "three jobs failing once each" "sustained load|1|completed|failure" "govulncheck|1|completed|failure" "merge liveness stress|1|completed|failure"
history "matrix shards failing once each" "go, shuffled and repeated (1)|1|completed|failure" "go, shuffled and repeated (2)|1|completed|failure"
history "matrix shards: shard 1 fails its retry" "go, shuffled and repeated (1)|1|completed|failure" "go, shuffled and repeated (2)|1|completed|success" "go, shuffled and repeated (1)|2|completed|failure"
history "the roll-up job failing alone" "stage 1 qualification verdict|1|completed|failure" "stage 1 qualification verdict|2|completed|failure"
history "a job cancelled three attempts in a row" "sustained load|1|completed|cancelled" "sustained load|2|completed|cancelled" "sustained load|3|completed|cancelled"
history "a job blocked (cancelled, then queued)" "sustained load|1|completed|cancelled" "sustained load|2|queued|"
