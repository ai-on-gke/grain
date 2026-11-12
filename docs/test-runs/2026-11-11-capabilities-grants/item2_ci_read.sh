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

# item2_ci_read.sh: §6 item 2. A real dispatch on a ci_mode: github repository:
# the task holds api:ci-read on its own repository by default, `grain ci list`
# and `grain pr status` answer for it and are refused for another repository,
# the prompt names no grain ci run-job/retrigger, request_access for grain#ci
# is refused with the api:ci-read hint, and a capability-policy deny rule
# removes the default on the next filing. Then a deny rule on repo#write holds
# a task that writes beyond its target.
set -euo pipefail
WORK=/var/tmp/capgrants/item2; GHSIM_ADDR=127.0.0.1:4292; PORT=4293
# shellcheck source=common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
setup_world
cp "$HERE/hook_ci.py" "$WORK/walk/hook.py"
start; configure
call PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"github"}' >"$OUT/item2-ci-mode.json"
T=$(call POST /api/tasks '{"title":"CI read default","description":"item 2","repo":"acme/widgets","approved":true}' | jq -r .id)
echo "task $T: $(wait_state "$T")"
call GET "/api/tasks/$T" | jq '{id,state,capabilities}' >"$OUT/item2-task.json"
cp "$WORK/walk/runs/hook-$T.json" "$OUT/item2-hook.json" || true
grep -o 'grain \(ci\|pr\) [a-z-]*' "$WORK/walk/runs/prompt-$T.jsonl" >"$OUT/item2-prompt-ci.txt" || true
cp "$WORK/walk/runs/prompt-$T.jsonl" "$OUT/item2-prompt.jsonl"
sort "$OUT/item2-prompt-ci.txt" | uniq -c >"$OUT/item2-prompt-ci-counts.txt"; cat "$OUT/item2-prompt-ci-counts.txt"

echo "== deny rule on api#ci-read, next filing"
call PUT /api/capability-policy '{"policy":{"rules":[{"capability":"https://grain.dev/capabilities/api#ci-read","deny":true}]}}' >"$OUT/item2-policy-put.json"
T2=$(call POST /api/tasks '{"title":"CI read default under deny","description":"item 2","repo":"acme/widgets","approved":false}' | jq -r .id)
call GET "/api/tasks/$T2" | jq '{id,state,capabilities}' >"$OUT/item2-task-denied.json"

echo "== deny rule on repo#write: a task writing beyond its target is held"
call PUT /api/capability-policy '{"policy":{"rules":[{"capability":"https://grain.dev/capabilities/repo#write","deny":true}]}}' >"$OUT/item2-policy-repo-write.json"
T3=$(call POST /api/tasks '{"title":"Writes beyond target","description":"item 2","repo":"acme/widgets","approved":true,"writes":[{"repo":"acme/lib"}]}' | jq -r .id)
T4=$(call POST /api/tasks '{"title":"Writes only target","description":"item 2","repo":"acme/widgets","approved":true}' | jq -r .id)
sleep 6
for t in "$T3" "$T4"; do call GET "/api/tasks/$t" | jq -c '{id,title,state,approved,writes,capabilities,comments:[.comments[]?|.body[0:300]]}'; done >"$OUT/item2-repo-write-deny.json"
cat "$OUT/item2-repo-write-deny.json"
stop
