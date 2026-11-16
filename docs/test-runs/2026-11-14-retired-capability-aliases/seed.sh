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

# seed.sh: with an old build (BIN=grain-v350), file every bare name through
# the API on every path that stores a grant, then dump the raw rows.
set -euo pipefail
source "$(dirname "$0")/common.sh"
setup_world; start_ghsim; env_world; start
{
echo "== version"; "$BIN" version 2>&1 | head -1
echo "== schema"; q "SELECT max(version) FROM schema_migrations" 2>/dev/null || q "SELECT * FROM schema_version" 2>/dev/null || true
call PUT /api/github-tokens/default '{"value":"ghp_walkthrough_stub"}'; echo
call PUT /api/github-tokens/bot '{"value":"ghp_walkthrough_bot"}'; echo
for r in acme/widgets acme/lib; do call POST /api/repos "{\"repo\":\"$r\"}"; echo; done
stop; start   # a new GitHub credential is offered only after a restart
echo "== tasks (one per bare name)"
for n in $(echo "$BARE" | jq -r '.[]'); do
  printf '%s -> ' "$n"
  call POST /api/tasks "$(jq -nc --arg n "$n" '{title:("bare "+$n),description:"seed",repo:"acme/widgets",capabilities:[$n],approve:false}')" | jq -c '{id,state,error}'
done
# The names v3.5.0 accepted on a task, for the list-shaped stores. gcp-key*,
# gemini-key and the release grants are refused without configured servers.
OK='["claude-proxy","bootstrap-playbooks","github-credential:bot","read-issues","github-sandbox","self-debug","local-scripts:deploy","grain-log"]'
echo "== template"
call POST /api/templates "$(jq -nc --argjson c "$OK" '{name:"bare-names",title:"bare names",description:"seed",repo:"acme/widgets",capabilities:$c}')" | jq -c '{id,error,capabilities,grants}'
echo "== schedule"
call POST /api/schedules "$(jq -nc --argjson c "$OK" '{name:"bare-names",title:"bare names",description:"seed",repo:"acme/widgets",recurrence:{kind:"daily",timeOfDay:"03:00"},capabilities:$c}')" | jq -c '{id,error,capabilities,grants}'
echo "== deployment default_capabilities"
call PUT /api/config "$(jq -nc --argjson c "$OK" '{defaultCapabilities:$c}')" | jq -c '{error,defaultCapabilities}'
echo "== repo default + CI capabilities"
call PUT /api/repos/acme/widgets/settings "$(jq -nc --argjson c "$OK" '{defaultCapabilities:$c}')" | jq -c '{error,defaultCapabilities}'
for n in grain-ci image-signing registry-push self-debug read-issues github-credential:bot local-scripts:deploy grain-log bootstrap-playbooks; do
  printf 'ci %s -> ' "$n"; call PUT /api/repos/acme/lib/settings "$(jq -nc --arg n "$n" '{ciCapabilities:[$n]}')" | jq -c '{error,ciCapabilities}'
done
CI='["grain-ci","image-signing","registry-push","self-debug","read-issues","github-credential:bot","grain-log"]'
call PUT /api/repos/acme/widgets/settings "$(jq -nc --argjson c "$CI" '{ciCapabilities:$c}')" | jq -c '{error,ciCapabilities}'
echo "== raw rows"
for t in task_grant template_grant schedule_grant; do echo "-- $t"; q "SELECT * FROM $t ORDER BY 1,2"; done
echo "-- grain_config.default_capabilities"; q "SELECT default_capabilities FROM grain_config"
echo "-- repo_config"; q "SELECT owner, name, default_capabilities, ci_capabilities FROM repo_config"
} 2>&1
stop; kill "$(cat "$WORK/ghsim.pid")" 2>/dev/null || true
