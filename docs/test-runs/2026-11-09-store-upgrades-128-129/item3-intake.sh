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

# item3-intake.sh <api> <token-file>: §2 item 3 on the operator-facing
# intake paths of a real daemon. Each refused mixed-grant shape is sent to
# POST /api/tasks, to POST /api/mcp grain_invoke(api_create_task) and to
# grain_submit_task; the task count is read before and after so a refusal
# that still stored something shows up. (propose_task inside a real run is
# step.sh + propose-shapes.py.)
set -u
api=$1
tok=$(cat "$2")
TARGET='{"capability":"https://grain.dev/capabilities/repo#target","with":{"repo":"acme/widgets","base":"main"}}'
READ='{"capability":"https://grain.dev/capabilities/repo#read","with":{"repo":"acme/widgets"}}'
labels=("capabilities + grants: []" "reads: [] + grants:" "base + repo#target" "noRepo + repo#target" "control: grants only")
shapes=(
  '"capabilities":["api:ci-read"],"grants":[]'
  '"reads":[],"grants":['"$READ"']'
  '"base":"main","grants":['"$TARGET"']'
  '"noRepo":true,"grants":['"$TARGET"']'
  '"grants":["api:ci-read"]'
)
count() { curl -s -H "Authorization: Bearer $tok" "$api/api/tasks?limit=500" | jq '(if type=="array" then . else .tasks end) | length'; }
mcp() { # mcp <tool> <arguments-json>
  curl -s -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' "$api/api/mcp" \
    --data '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"'"$1"'","arguments":'"$2"'}}' |
    jq -r '"isError=\(.result.isError // false) \(.result.content[0].text // .error.message)"' | cut -c1-260
}
for i in "${!shapes[@]}"; do
  s=${shapes[$i]}
  body='{"title":"intake: '"${labels[$i]}"'","description":"§2 item 3","repo":"acme/widgets",'"$s"'}'
  [[ ${labels[$i]} == noRepo* ]] && body='{"title":"intake: '"${labels[$i]}"'","description":"§2 item 3",'"$s"'}'
  echo "== ${labels[$i]}: {$s}"
  before=$(count)
  printf -- '-- POST /api/tasks -> '
  curl -s -o /tmp/intake.out -w '%{http_code} ' -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' "$api/api/tasks" --data "$body"
  jq -c '.error // {id, repo, base, capabilities, reads}' /tmp/intake.out | cut -c1-260
  printf -- '-- grain_invoke(api_create_task) -> '
  mcp grain_invoke '{"tool":"api_create_task","arguments":{"body":'"$body"'}}'
  sub=$(echo "$body" | sed 's/"noRepo"/"no_repo"/')
  printf -- '-- grain_submit_task -> '
  mcp grain_submit_task "$sub"
  echo "-- tasks before $before, after $(count)"
done
