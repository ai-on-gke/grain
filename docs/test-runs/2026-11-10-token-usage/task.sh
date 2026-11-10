#!/bin/bash
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

# task.sh <data-dir> <port> <walk-dir> <mode> <title>: files a no-repo task
# (no GitHub involved), sets the fake CLI's mode for it, approves it, asks
# for it to run now, and waits (up to 3m) for its first run to end.
# Prints the task id.
D=$(dirname "$0")
dir=$1 port=$2 w=$3 mode=$4 title=$5
id=$("$D/api.sh" "$dir" "$port" POST /api/tasks "{\"title\":\"$title\",\"body\":\"say done\",\"noRepo\":true}" | head -n 1 | jq -r .id)
echo "$mode" > "$w/mode-$id"
"$D/api.sh" "$dir" "$port" POST "/api/tasks/$id/approve" '{}' >/dev/null
"$D/api.sh" "$dir" "$port" POST "/api/tasks/$id/run-now" '{}' >/dev/null
for _ in $(seq 90); do
  n=$("$D/api.sh" "$dir" "$port" GET "/api/tasks/$id" | head -n 1 | jq '[.attempts[]? | select(.outcome != null and .outcome != "")] | length')
  [ "${n:-0}" -gt 0 ] && break
  sleep 2
done
echo "$id"
