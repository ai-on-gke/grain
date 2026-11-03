#!/bin/sh
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

# Item 3, upgrade half: a schema-85 build (75331e14) exports a deployment
# holding the nightly-ci-review github_workflow_run schedule; this build's
# `grain state check` refuses the dump (another schema) and
# `grain state check -migrate` rehearses it; then this build's daemon
# imports the dump into a fresh data dir.
set -u
OLD=/home/debian/bin/grain-s85
NEW=/home/debian/bin/grain
W=$(mktemp -d)
D=$W/old
HOME=$W setsid "$OLD" daemon -data-dir "$D" -ui-addr 127.0.0.1:18732 -sandbox-backend host \
  -sandbox-dir "$D/sandboxes" -sandbox-port-base=0 -github-host 127.0.0.1:1 -github-insecure-http \
  >"$W/old.log" 2>&1 </dev/null &
P=$!
for _ in $(seq 1 60); do curl -s -o /dev/null http://127.0.0.1:18732/ && break; sleep 1; done
T=$(cat "$D/local-api-token")
curl -s -H "Authorization: Bearer $T" -H 'Content-Type: application/json' http://127.0.0.1:18732/api/schedules -d '{
  "title":"Nightly CI review","description":"Triage a red nightly.yml run on main.","repo":"acme/widgets","base":"main","overlap":"skip",
  "recurrence":{"kind":"github_workflow_run","githubWorkflowRun":{"workflow":"nightly.yml","conclusions":["failure","timed_out","startup_failure"],"branches":["main"]}}}' | jq -c '{id,title}'
sleep 15
kill "$P"; sleep 2
git clone -q "$D/state-repo" "$W/dump"
echo "dump schema-version: $(cat "$W/dump/schema-version")"
grep -n -A5 'trigger:' "$W/dump/settings/schedules.yml" | head -8
echo "== grain state check (this build)"; "$NEW" state check "$W/dump"; echo "exit=$?"
echo "== grain state check -migrate (this build)"; "$NEW" state check -migrate "$W/dump"; echo "exit=$?"
echo "== import into a fresh data dir (this build)"
F=$W/fresh/.grain; mkdir -p "$F"; git clone -q "$W/dump" "$F/state-repo"
HOME=$W/fresh setsid "$NEW" daemon -data-dir "$F" -ui-addr 127.0.0.1:18733 -sandbox-backend host \
  -sandbox-dir "$F/sandboxes" -sandbox-port-base=0 -github-host 127.0.0.1:1 -github-insecure-http \
  >"$W/fresh.log" 2>&1 </dev/null &
P=$!
for _ in $(seq 1 60); do curl -s -o /dev/null http://127.0.0.1:18733/ && break; sleep 1; done
sleep 3
T2=$(cat "$F/local-api-token")
curl -s -H "Authorization: Bearer $T2" http://127.0.0.1:18733/api/schedules |
  jq -c '(.[]? // .schedules[]?) | select(.title=="Nightly CI review") | {id,title,repo,base,recurrence}'
grep -i 'staterepo' "$W/fresh.log" | head -4 | cut -c1-300
kill "$P"
