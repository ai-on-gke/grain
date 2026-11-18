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

# summarize.sh RUN: the distinct (legacy state, merge gates, review/approval
# observations) sequence of every task sampled during the run, and the
# p50/p95 of the sampler's GET /api/tasks/{id} latency.
set -euo pipefail
RUN=$1
S=$RUN/obs/samples.jsonl
for id in $(jq -r .id "$S" | sort -nu); do
	echo "## task $id: $(jq -r "select(.id==\"$id\") | .title" "$S" | tail -1)"
	jq -c "select(.id==\"$id\") | [.state, .gates, .obs]" "$S" | uniq
done
jq -r .ms "$S" | sort -n | awk '{a[NR]=$1} END {printf "sampler GET latency over %d reads: p50=%.1fms p95=%.1fms max=%.1fms\n", NR, a[int(NR*0.5)], a[int(NR*0.95)], a[NR]}'
