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

# api-probe.sh <old-binary> <port>: whether an old build's own API lets an
# operator make the O1 shape seed.sh writes with SQL -- a grant in the
# deployment's and a repository's defaults, a template and a schedule, and
# then its catalogue entry (in_process.read-issues) switched off with
# PUT /api/capability-policy.
set -u
OLD=$1 port=$2
here=$(cd "$(dirname "$0")" && pwd)
W=/home/debian/walk2/api-probe-$(basename "$OLD")
pkill -f "$W" ; sleep 1; rm -rf "$W"
"$here/start.sh" "$OLD" "$W" "$port" >/dev/null || exit 1
tok=$(cat "$W/local-api-token") api=http://127.0.0.1:$port
c() { # c METHOD PATH [JSON]
  local code
  code=$(curl -sS -o /tmp/probe.out -w '%{http_code}' -X "$1" -H "Authorization: Bearer $tok" \
    -H 'Content-Type: application/json' ${3:+--data "$3"} "$api$2")
  printf '%s %s -> %s %s\n' "$1" "$2" "$code" "$(head -c 300 /tmp/probe.out | tr '\n' ' ')"
}
RI='https://grain.dev/capabilities/github#read-issues={}'
c PUT /api/github-tokens/default '{"value":"walk-stub-pat-not-a-real-token"}' | cut -c1-60
c POST /api/repos '{"repo":"acme/widgets"}' | cut -c1-60
c PUT /api/settings '{"defaultCapabilities":["'"$RI"'"]}' | cut -c1-120
c PUT /api/repos/acme/widgets/settings '{"defaultCapabilities":["'"$RI"'"],"ciCapabilities":["'"$RI"'"]}' | cut -c1-120
c POST /api/templates '{"name":"probe-template","title":"probe","description":"probe","repo":"acme/widgets","capabilities":["'"$RI"'"]}' | cut -c1-120
c POST /api/schedules '{"name":"probe-schedule","recurrence":{"kind":"daily","timeOfDay":"03:00"},"title":"probe","description":"probe","repo":"acme/widgets","capabilities":["'"$RI"'"]}' | cut -c1-120
policy=$(curl -s -H "Authorization: Bearer $tok" "$api/api/capability-policy" | jq -c '(.policy // .) | .catalogue = ((.catalogue // {}) | .inProcess = ((.inProcess // {}) + {"read-issues": {"enabled": false}}))')
c PUT /api/capability-policy "{\"policy\":$policy}"
echo "# stored afterwards: in_process.read-issues and the grant in default_capabilities"
curl -s -H "Authorization: Bearer $tok" "$api/api/capability-policy" | jq -c '(.policy // .) | .catalogue.inProcess'
curl -s -H "Authorization: Bearer $tok" "$api/api/settings" | jq -c '.defaultCapabilities'
kill "$(cat "$W.pid")"
