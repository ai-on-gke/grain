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

# refusals.sh: on a running HEAD daemon (after head.sh with KEEP=1), file
# every bare name on every API path that records a grant and print the
# HTTP status and answer of each.
set -uo pipefail
source "$(dirname "$0")/common.sh"
env_world
st() { local m=$1 p=$2 b=$3; curl -sS -o /tmp/ra.body -w '%{http_code}' -X "$m" -H "Authorization: Bearer $(cat "$WORK/data/local-api-token")" -H 'Content-Type: application/json' --data "$b" "http://127.0.0.1:$PORT$p"; printf ' %s\n' "$(jq -r '.error // .message // (.|tostring)' /tmp/ra.body 2>/dev/null | head -c 400 || head -c 400 /tmp/ra.body)"; }
for n in $(echo "$BARE" | jq -r '.[]'); do
  echo "## $n"
  printf 'POST /api/tasks               '; st POST /api/tasks "$(jq -nc --arg n "$n" '{title:"r",description:"r",repo:"acme/widgets",capabilities:[$n]}')"
  printf 'POST /api/tasks grants        '; st POST /api/tasks "$(jq -nc --arg n "$n" '{title:"r",description:"r",repo:"acme/widgets",grants:[$n]}')"
  printf 'POST /api/tasks/2/capabilities '; st POST /api/tasks/2/capabilities "$(jq -nc --arg n "$n" '{capability:$n,attach:true}')"
  printf 'POST /api/templates           '; st POST /api/templates "$(jq -nc --arg n "$n" '{name:("r-"+$n),title:"r",description:"r",repo:"acme/widgets",capabilities:[$n]}')"
  printf 'POST /api/schedules           '; st POST /api/schedules "$(jq -nc --arg n "$n" '{name:("r-"+$n),title:"r",description:"r",repo:"acme/widgets",recurrence:{kind:"daily",timeOfDay:"03:00"},capabilities:[$n]}')"
  printf 'PUT /api/config defaults      '; st PUT /api/config "$(jq -nc --arg n "$n" '{defaultCapabilities:[$n]}')"
  printf 'PUT repo defaultCapabilities  '; st PUT /api/repos/acme/lib/settings "$(jq -nc --arg n "$n" '{defaultCapabilities:[$n]}')"
  printf 'PUT repo ciCapabilities       '; st PUT /api/repos/acme/lib/settings "$(jq -nc --arg n "$n" '{ciCapabilities:[$n]}')"
done
