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

# walk_item_2_sandbox.sh is §8 item 2: a template's `sandbox:` size. The
# big template (disk-gb: 60) is fired by its schedule and filed by hand;
# both tasks carry 60, and dispatching them (the pipeline resumed, the
# fake agent exiting at once) logs the shape the run asked its sandbox
# backend for. Then an over-ceiling value and an unknown key are pushed
# and refused at load, each named in repo_declaration_sync.last_error
# and in `grain health`'s refused declarations, with the last good
# commit's declarations kept.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=/dev/null
source "$HERE/helpers.sh"
GRAIN=${GRAIN_BIN:-/home/debian/bin/grain}

step "2a. the big schedule fires: the task carries disk-gb 60"
close_open big; due big; sleep 4
sched_tasks big 2

step "2b. filed by hand from the template"
HAND=$(api POST /api/tasks '{"repo":"acme/widgets","templateId":"repo:template:acme:widgets:big","approve":true}' | jq -r '.id // .task.id')
db "SELECT id, title, sandbox_cpus AS cpus, sandbox_memory_mb AS mem, sandbox_disk_gb AS disk FROM task WHERE id='$HAND'"

step "2c. dispatch: resume the pipeline and read the shape each run was built with"
api DELETE /api/pipeline-pause >/dev/null 2>&1 || true
sleep 15
db "SELECT r.task_id, r.id AS run, r.sandbox, r.outcome FROM task_run r WHERE r.task_id IN (SELECT id FROM task WHERE sandbox_disk_gb=60) ORDER BY r.task_id" 2>&1 || true
grep -E "sandbox size|no shape of its own|run [0-9]+-1 " "$WORK_ROOT/daemon.log" | tail -n 8 || true
api POST /api/pipeline-pause '{"reason":"§8 walkthrough"}' >/dev/null

step "2d. over the ceiling: disk-gb 5000"
cp "$W/.grain/templates/big.yml" "$WORK_ROOT/big.yml.good"
sed -i 's/disk-gb: 60/disk-gb: 5000/' "$W/.grain/templates/big.yml"
push_widgets "big: disk-gb 5000"; resync
db "SELECT \"commit\", last_error FROM repo_declaration_sync WHERE name='widgets'"
db "SELECT id, sandbox FROM template WHERE id='repo:template:acme:widgets:big'"
"$GRAIN" -server "$API_URL" -token "$TOK" health 2>&1 | sed -n '/declarations/,/^$/p' || true

step "2e. an unknown key: sandbox: {disk-gb: 60, gpus: 1}"
cp "$WORK_ROOT/big.yml.good" "$W/.grain/templates/big.yml"
echo '  gpus: 1' >>"$W/.grain/templates/big.yml"
push_widgets "big: unknown sandbox key"; resync
db "SELECT \"commit\", last_error FROM repo_declaration_sync WHERE name='widgets'"
db "SELECT id, sandbox FROM template WHERE id='repo:template:acme:widgets:big'"
"$GRAIN" -server "$API_URL" -token "$TOK" health 2>&1 | sed -n '/declarations/,/^$/p' || true

step "2f. zero: disk-gb 0"
cp "$WORK_ROOT/big.yml.good" "$W/.grain/templates/big.yml"
sed -i 's/disk-gb: 60/disk-gb: 0/' "$W/.grain/templates/big.yml"
push_widgets "big: disk-gb 0"; resync
db "SELECT last_error FROM repo_declaration_sync WHERE name='widgets'"

step "2g. restore"
cp "$WORK_ROOT/big.yml.good" "$W/.grain/templates/big.yml"
push_widgets "big: restore"; resync
db "SELECT last_error FROM repo_declaration_sync WHERE name='widgets'"
