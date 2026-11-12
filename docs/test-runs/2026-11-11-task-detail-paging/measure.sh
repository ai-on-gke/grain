#!/bin/sh
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

# measure.sh <data-dir> <port> <task> [n]: GET /api/tasks/<task> n times
# (default 5) unpaged and with the task page's own limits
# (comments_limit=50&attempts_limit=20), then with the maximum (500/500),
# printing "<query> bytes=<size> ms=<min>/<median>/<max>" plus the paged
# answer's own comment/attempt counts and olderComments/olderAttempts.
dir=$1 port=$2 task=$3 n=${4:-5}
tok=$(cat "$dir/local-api-token")
for q in "" "?comments_limit=50&attempts_limit=20" "?comments_limit=500&attempts_limit=500"; do
  url="http://127.0.0.1:$port/api/tasks/$task$q"
  times=$(for _ in $(seq "$n"); do
    curl -s -o /dev/null -H "Authorization: Bearer $tok" -w '%{time_total}\n' "$url"
  done | sort -n | awk '{t[NR]=$1*1000} END {printf "%.1f/%.1f/%.1f", t[1], t[int((NR+1)/2)], t[NR]}')
  body=$(curl -s -H "Authorization: Bearer $tok" "$url")
  counts=$(printf '%s' "$body" | jq -c '{comments: (.comments|length), attempts: (.attempts|length), olderComments, olderAttempts, failedAttempts, questionCommentId: .questionCommentId, questionSummary, usageRuns: .usage.runs, costUSD: .usage.costUSD}')
  echo "${q:-(unpaged)} bytes=$(printf '%s' "$body" | wc -c) ms=$times $counts"
done
