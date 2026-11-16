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

# walk_item_3_own.sh is §8 item 3: a template-backed schedule's own
# settings. nightly (admission: durable, repo#read acme/upstream,
# api#memory) is fired by own, which adds api#ci-read. Each step fires
# own and prints what the filed task carries and what the schedule row
# itself stores (only its own difference).
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=/dev/null
source "$HERE/helpers.sh"

fire_own() {
  close_open own; due own; sleep 4
  local id; id=$(dbv "SELECT task_id FROM task_tag WHERE tag='schedule:repo:schedule:acme:widgets:own' ORDER BY CAST(task_id AS INTEGER) DESC LIMIT 1")
  echo "--- task $id"
  db "SELECT id, admission, hosting FROM task WHERE id='$id'"
  db "SELECT capability, via FROM task_grant WHERE task_id='$id' ORDER BY capability"
  db "SELECT owner||'/'||name AS reads FROM task_read WHERE task_id='$id'"
  echo "--- schedule row"
  db "SELECT id, admission, hosting FROM schedule WHERE id='repo:schedule:acme:widgets:own'"
  db "SELECT capability, via FROM schedule_grant WHERE schedule_id='repo:schedule:acme:widgets:own'"
}

step "3a. first firing: template's admission, read and grant plus the schedule's own grant"
fire_own

step "3b. second firing, nothing edited: the schedule's own grant survives a firing"
fire_own

step "3c. edit the template: api#memory -> api#tasks:read, acme/upstream -> acme/downstream"
sed -i 's#api\#memory#api\#tasks:read#; s#repo: acme/upstream#repo: acme/downstream#' "$W/.grain/templates/nightly.yml"
cat "$W/.grain/templates/nightly.yml"
push_widgets "nightly: new grant and read"; resync
db "SELECT last_error FROM repo_declaration_sync WHERE name='widgets'"
fire_own

step "3d. remove the schedule's own grant"
sed -i '/^grants:/,$d' "$W/.grain/schedules/own.yml"
cat "$W/.grain/schedules/own.yml"
push_widgets "own: drop its own grant"; resync
db "SELECT last_error FROM repo_declaration_sync WHERE name='widgets'"
fire_own

step "3e. a schedule-own admission above the template's (bypass is not above durable; console hosting is)"
printf 'hosting: console\n' >>"$W/.grain/schedules/own.yml"
push_widgets "own: hosting console"; resync
db "SELECT last_error FROM repo_declaration_sync WHERE name='widgets'"
fire_own
