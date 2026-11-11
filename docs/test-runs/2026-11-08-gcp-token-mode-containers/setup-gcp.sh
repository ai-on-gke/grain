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

# setup-gcp.sh: the walk's gcp half, on top of 2026-11-04's setup-daemon.sh
# (fake GitHub, ag-a/ag-b run by fake-agy). Starts gcpwalk (gcpfake behind
# grain's configurable gcp server), registers it under grain's canonical
# gcp id with the org-deploy set, and files the walk task as the operator
# with the person-granted grant. AUTO=1 files it with autoApproveProposals
# and no base branch (the parent whose proposals AutoSubmits judges).
#
# Built beforehand (from the checkout):
#   go build -o ~/walk/bin/gcpwalk docs/test-runs/2026-11-08-gcp-token-mode-containers/gcpwalk.go
#   cp docs/test-runs/2026-11-08-gcp-token-mode-containers/{fake-agy.sh,step-gcp-token.sh,exchange.sh,mcp-propose.py} ~/walk/
#   (fake-agy.sh -> ~/walk/fake-agy, step-gcp-token.sh -> ~/walk/step.sh)
# The daemon, with nftables installed so container sandboxes are fenced
# (an unfenced container refuses every external grant):
#   grain daemon -data-dir ~/walk/rd/d -sandbox-port-base=0 -container-sandboxes \
#     -container-sandbox-image grain-sbx:walk -container-git-proxy-host 10.240.0.1 \
#     -github-host 127.0.0.1:4250 -github-insecure-http
set -eu
W=/home/debian/walk
api=${API:-http://127.0.0.1:4242}
tok=$(cat "${TOKEN_FILE:-$W/rd/d/local-api-token}")
bearer=$(head -c32 /dev/urandom | od -An -tx1 | tr -d ' \n')
setsid nohup "$W/bin/gcpwalk" 127.0.0.1:4260 127.0.0.1:4252 127.0.0.1:4261 "$bearer" > "$W/gcpwalk.log" 2>&1 &
sleep 1
# No max_lease: a set's max_lease bounds the grant's whole deadline, and a
# run's is at least 1h45m away (the refusal is max-lease-refusal.txt).
jq -n --arg b "$bearer" '{id: "https://grain.dev/capabilities/gcp", config: {
  serverUrl: "http://127.0.0.1:4260/mcp", headers: {Authorization: ("Bearer " + $b)}, builtin: "gcp",
  config: {permission_sets: [{name: "org-deploy", service_account: "deployer@walk-proj.iam.gserviceaccount.com",
    roles: ["roles/run.admin"], credential: "token"}]}}}' |
  curl -sS -o /dev/null -w 'PUT /api/capability-servers -> %{http_code}\n' -X PUT \
    -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' --data @- "$api/api/capability-servers"
task=$(jq -n --arg auto "${AUTO:-}" '{title: "walk: gcp token mode", description: "Walk gcp token mode in a real container sandbox.",
  repo: "acme/widgets", agentBackendId: "ag-a",
  capabilities: ["https://grain.dev/capabilities/gcp#gcp-key={\"permission_set\":\"org-deploy\"}"]}
  + (if $auto == "" then {base: "main"} else {autoApproveProposals: true} end)')
id=$(curl -sS -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' --data "$task" "$api/api/tasks" | jq -r .id)
# Filed by the operator, it is still proposed until approved.
curl -sS -o /dev/null -w "approve task $id -> %{http_code}\n" -X POST -H "Authorization: Bearer $tok" \
  -H 'Content-Type: application/json' --data '{}' "$api/api/tasks/$id/approve"
