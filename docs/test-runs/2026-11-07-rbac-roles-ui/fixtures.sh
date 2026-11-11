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

# Seeds the daemon walkthrough.sh holds (HOLD=1) with what sweep.mjs walks
# and items 1-3 do not leave behind: a kanban board, a template, a suite of
# it and a schedule firing it. Uses the access code, which is unlimited.
#
#   docs/test-runs/2026-11-07-rbac-roles-ui/fixtures.sh WORK_DIR [DAEMON]
set -euo pipefail
WORK=$1
DAEMON=${2:-http://127.0.0.1:14320}
CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
post() {
	curl -sf -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" -d "$2" "$DAEMON$1" | jq -c '{id}'
}
post /api/boards '{"name":"Release Kanban","kind":"kanban"}'
post /api/templates '{"name":"weekly-triage","title":"Weekly triage","description":"Triage the queue"}'
post /api/suites '{"name":"nightly","templateIds":["template-1"],"mode":"count","count":1}'
post /api/schedules '{"templateId":"template-1","repo":"acme/app","recurrence":{"kind":"daily","timeOfDay":"09:00"}}'
