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

# reopen.sh <data-dir> <port> <title>: §2.5 on a real daemon with the stub
# agent CLI (../2026-11-10-token-usage/fake-agy.sh, configured by that
# walk's setup.sh). Files a no-repo task and runs it once (the stub answers
# in prose and makes no tool call, so the run fails yet the task is
# awaiting_reply/answered with no delivered mark), closes it, comments as a
# person, reopens it and waits for the next run, printing the state and run
# count at each step. The reopen event's detail and whether run 2's prompt
# carries the comment are read from the store afterwards (see the report).
D=$(dirname "$0"); dir=$1 port=$2 title=$3
api() { "$D/api.sh" "$dir" "$port" "$@" | head -n 1; }
id=$(api POST /api/tasks "{\"title\":\"$title\",\"body\":\"say done\",\"noRepo\":true}" | jq -r .id)
api POST "/api/tasks/$id/approve" '{}' >/dev/null; api POST "/api/tasks/$id/run-now" '{}' >/dev/null
runs() { api GET "/api/tasks/$id" | jq '[.attempts[]? | select(.outcome != null and .outcome != "")] | length'; }
for _ in $(seq 90); do [ "$(runs)" -ge 1 ] && break; sleep 2; done
sleep 3
echo "task $id after run 1: $(api GET "/api/tasks/$id" | jq -c '{state, reason: .reason.reason, runs: (.attempts|length)}')"
api POST "/api/tasks/$id/close" '{}' >/dev/null
api POST "/api/tasks/$id/comments" '{"body":"Does this cover the staging cluster too? (left while closed)"}' >/dev/null
sleep 5
echo "closed + comment: $(api GET "/api/tasks/$id" | jq -c '{state, runs: (.attempts|length)}')"
api POST "/api/tasks/$id/reopen" '{}' >/dev/null
echo "just reopened: $(api GET "/api/tasks/$id" | jq -c '{state, runs: (.attempts|length)}')"
for _ in $(seq 60); do [ "$(runs)" -ge 2 ] && break; sleep 2; done
sleep 10
echo "after reopen: $(api GET "/api/tasks/$id" | jq -c '{state, reason: .reason.reason, runs: (.attempts|length)}')"
echo "$id" > "$dir.reopen-task"
