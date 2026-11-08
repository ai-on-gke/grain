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

# policy-probe.sh <api> <token-file>: files tasks through the migrated
# daemon's API with grants that the schema-108 policy's deny rules and
# in-process switches named under their retired names, and prints what the
# API says for each. Run against the in-place-upgraded and the
# export/import stores alike.
api=$1
tok=$(cat "$2")
c() {
  local code
  code=$(curl -sS -o /tmp/probe.out -w '%{http_code}' -X "$1" -H "Authorization: Bearer $tok" \
    -H 'Content-Type: application/json' ${3:+--data "$3"} "$api$2")
  printf '%s %s\n' "$code" "$(python3 -c 'import json,sys
d=json.load(open("/tmp/probe.out"))
print(d.get("error") or "task %s admission=%s caps=%s" % (d.get("id"), d.get("admission"), d.get("capabilities")))' 2>/dev/null || head -c 300 /tmp/probe.out)"
}
c POST /api/repos '{"repo":"acme/skill-denied"}' >/dev/null
probe() { # probe <repo> <grant>
  local g
  g=$(python3 -c 'import json,sys; print(json.dumps(sys.argv[1]))' "$2")
  printf '== %s  %s\n   ' "$1" "$2"
  c POST /api/tasks "{\"title\":\"probe\",\"description\":\"policy probe\",\"repo\":\"$1\",\"capabilities\":[$g]}"
}
CM='https://grain.dev/capabilities/config-map'
SK='https://grain.dev/capabilities/skill'
probe acme/denied "$CM#config-map={\"config_maps\":[\"bootstrap\"]}"
probe acme/denied "$SK#config-map={\"config_maps\":[\"bootstrap\"]}"
probe acme/denied 'https://grain.dev/capabilities/github#read-issues={}'
probe acme/denied 'https://grain.dev/capabilities/repo#credential={"credential":"bot"}'
probe acme/skill-denied "$CM#config-map={\"config_maps\":[\"bootstrap\"]}"
probe acme/skill-denied "$SK#config-map={\"config_maps\":[\"bootstrap\"]}"
probe acme/widgets 'https://grain.dev/capabilities/github#read-issues={}'
probe acme/widgets "$CM#config-map={\"config_maps\":[\"bootstrap\"]}"
probe acme/widgets 'https://grain.dev/capabilities/repo#credential={"credential":"bot"}'
probe acme/widgets 'https://grain.dev/capabilities/grain#source={}'
probe acme/widgets 'read-issues'
probe acme/widgets 'priority-dispatch'
