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

# setup-daemon.sh: configures the walk's real daemon (127.0.0.1:7830, host
# sandboxes, GitHub = githost on 127.0.0.1:4250) for items 2-4: two
# antigravity connections run by fake-agy, each with its own (fake) Gemini
# API key, the acme/widgets repository and a stub GitHub credential.
api=${API:-http://127.0.0.1:7830}
tok=$(cat "${TOKEN_FILE:-/home/debian/rd/d/local-api-token}")
c() {
  code=$(curl -sS -o /tmp/setup.out -w '%{http_code}' -X "$1" -H "Authorization: Bearer $tok" \
    -H 'Content-Type: application/json' ${3:+--data "$3"} "$api$2")
  printf '%s %s -> %s %s\n' "$1" "$2" "$code" "$(head -c 200 /tmp/setup.out | tr '\n' ' ')"
}
c PUT /api/github-tokens/default '{"value":"walk-stub-pat-not-a-real-token"}'
c POST /api/repos '{"repo":"acme/widgets"}'
c PUT /api/config '{"agentBackends":[
  {"id":"ag-a","name":"Agent A","framework":"antigravity","cliPath":"/home/debian/walk/fake-agy","models":["fake-model"]},
  {"id":"ag-b","name":"Agent B","framework":"antigravity","cliPath":"/home/debian/walk/fake-agy","models":["fake-model"]}]}'
# Each connection's own key. Neither is a real key: Google answers both
# with "API key not valid", and credential_access_audit says which was read.
c PUT /api/agent-backends/ag-a/credential '{"value":"walk-key-A-not-real"}'
c PUT /api/agent-backends/ag-b/credential '{"value":"walk-key-B-not-real"}'
