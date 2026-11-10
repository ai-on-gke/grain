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

# item4.sh <data-dir> <port> <walk-dir> <cli-name> <n> <seconds>: files n
# no-repo tasks whose fake CLI sleeps <seconds>, approves them all at once
# (no run-now, so the dispatcher's own worker limit decides), and samples
# every second how many fake CLI processes are running -- one per live run,
# since a run's execs are sequential. Prints "max concurrent: N".
D=$(dirname "$0")
dir=$1 port=$2 w=$3 cli=$4 n=$5 secs=$6
ids=
for i in $(seq "$n"); do
  id=$("$D/api.sh" "$dir" "$port" POST /api/tasks "{\"title\":\"item4 slow $i\",\"body\":\"say done\",\"noRepo\":true}" | head -n 1 | jq -r .id)
  echo "slow:$secs" > "$w/mode-$id"; ids="$ids $id"
done
for id in $ids; do "$D/api.sh" "$dir" "$port" POST "/api/tasks/$id/approve" '{}' >/dev/null; done
max=0; idle=0
for t in $(seq 400); do
  c=$(pgrep -fc "^/bin/sh [^ ]*$cli --input" || true)
  echo "$(date -u +%T) running=$c"
  [ "$c" -gt "$max" ] && max=$c
  if [ "$c" -eq 0 ] && [ "$t" -gt 20 ]; then idle=$((idle+1)); [ $idle -ge 15 ] && break; else idle=0; fi
  sleep 1
done
echo "tasks:$ids"
echo "max concurrent: $max"
