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

# Item 3: the github_workflow_run schedule round trip.
#  1. create .grain/schedules/nightly-ci-review.yml's schedule (same title,
#     body, base and trigger) on the walkthrough daemon, against acme/widgets;
#  2. wait for the daemon to export it into its state repository;
#  3. `grain state check` and `grain state check -migrate` the export (a clone,
#     with schema-version rewound to 85 for the -migrate rehearsal, as an
#     upgrade from v3.1.0 would present it);
#  4. import it into a fresh data dir (a second daemon whose state-repo is a
#     clone of the export) and read the schedule back.
set -u
G=/home/debian/bin/grain
S=http://127.0.0.1:18702
T=$(cat /home/debian/live/.grain/local-api-token)
OUT=${OUT:-/home/debian/work/docs/test-runs/2026-11-03-config-state}
curl -s -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$S/api/schedules" -d '{
  "title": "Nightly CI review", "description": "Triage a red nightly.yml run on main.",
  "repo": "acme/widgets", "base": "main", "overlap": "skip",
  "recurrence": {"kind": "github_workflow_run", "githubWorkflowRun":
    {"workflow": "nightly.yml", "conclusions": ["failure","timed_out","startup_failure"], "branches": ["main"]}}
}' | jq -c '{id, title, recurrence}'
sleep 25
STATE=/home/debian/live/.grain/state-repo
grep -n -A8 'title: Nightly CI review' "$STATE/settings/schedules.yml"
WORK=$(mktemp -d)
git clone -q "$STATE" "$WORK/dump"
echo "== grain state check (schema $(cat "$WORK/dump/schema-version"))"
$G state check "$WORK/dump"; echo "exit=$?"
echo "== grain state check -migrate"
$G state check -migrate "$WORK/dump"; echo "exit=$?"
# Fresh data dir: a second daemon whose state repository starts as the dump.
F=$WORK/fresh/.grain
mkdir -p "$F"
git clone -q "$STATE" "$F/state-repo"
HOME=$WORK/fresh setsid "$G" daemon -data-dir "$F" -ui-addr 127.0.0.1:18712 -sandbox-backend host \
  -sandbox-dir "$F/sandboxes" -sandbox-port-base=0 -github-host 127.0.0.1:1 -github-insecure-http \
  >"$WORK/fresh.log" 2>&1 </dev/null &
PID=$!
for _ in $(seq 1 60); do curl -s -o /dev/null http://127.0.0.1:18712/ && break; sleep 1; done
sleep 3
T2=$(cat "$F/local-api-token")
echo "== the fresh data dir's schedule"
curl -s -H "Authorization: Bearer $T2" http://127.0.0.1:18712/api/schedules |
  jq -c '(.[]? // .schedules[]?) | select(.title=="Nightly CI review") | {id, title, repo, base, recurrence}'
grep -i 'staterepo\|import' "$WORK/fresh.log" | head -5
kill "$PID"
