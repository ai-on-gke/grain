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

# figma-demo.sh <grain>: §6 item 5's Settings -> Capabilities figma path
# over `grain demo` (whose figma server talks to an in-process fake Figma
# API): configure a pat account through the same PUT the Settings form
# makes, then Test it through the same POST the Test button makes.
# -secrets-store gives the demo a real secrets store: without it the PUT
# is refused (the demo has no local secrets directory to write to).
set -u
G=${1:-/home/debian/bin/grain-head}
dir=$(mktemp -d)
echo walk-access-code-0123456789abcdef >"$dir/code"
setsid "$G" demo -github-sim -fake-ci -reconcile 2s -addr 127.0.0.1:4220 -open=false -secrets-store \
  -access-code-file "$dir/code" >"$dir/demo.log" 2>&1 </dev/null &
pid=$!
trap 'kill $pid; rm -rf "$dir"' EXIT
tok=$(cat "$dir/code")
for _ in $(seq 60); do curl -fsS -o /dev/null -H "Authorization: Bearer $tok" http://127.0.0.1:4220/api/config && break; sleep 1; done
c() {
  echo "== $1 $2"
  curl -sS -X "$1" -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
    ${3:+--data "$3"} "http://127.0.0.1:4220$2" | head -c 900
  echo
}
FIGMA=https://grain.dev/capabilities/figma
c GET /api/capability-servers | python3 -c 'import json,sys
for s in json.load(sys.stdin):
    if "figma" in s.get("id", ""): print({k: v for k, v in s.items() if k not in ("configSchema", "config")})' 2>/dev/null
cfg=$(curl -sS -H "Authorization: Bearer $tok" http://127.0.0.1:4220/api/capability-servers | python3 -c '
import json, sys
for s in json.load(sys.stdin):
    if s.get("id") == "'$FIGMA'":
        c = s.get("config") or {}
        c.setdefault("config", {})["accounts"] = [{"name": "design", "auth_mode": "pat"}]
        print(json.dumps({"id": s["id"], "config": c,
                          "configSecrets": {"personal_access_tokens": json.dumps({"design": "figd_walk_not_a_real_token"})}}))
')
c PUT /api/capability-servers "$cfg"
sleep 3
c POST /api/capability-servers/figma-test "{\"id\":\"$FIGMA\",\"account\":\"design\"}"
c POST /api/capability-servers/figma-test "{\"id\":\"$FIGMA\",\"account\":\"nobody\"}"
c POST /api/capability-servers/test "{\"id\":\"$FIGMA\"}"
