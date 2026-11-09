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

# docs/test-plans/2026-11-09.md §1 items 2, 3, 4 (live half) and 5 over
# the API, the CLI and the real propose_task tool, against a real
# `grain daemon`. Prints PASS/FAIL per check; exits non-zero on a FAIL.
#
#   bash items_api.sh            # starts its own environment
#   REUSE=1 bash items_api.sh    # uses the one already running
set -uo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
if [[ -n ${REUSE:-} ]]; then
	WORK=${WORK:-/var/tmp/access-forms}
	CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	DB="$WORK/data/store/grain.db" BODY="$WORK/body.json"
	export WORK CODE DB BODY HOME="$WORK/home" GRAIN_SANDBOX_CLI=0
else
	start_env
fi
fails=0
check() { # name want got
	if [[ $2 == "$3" ]]; then echo "PASS $1"; else echo "FAIL $1: want [$2] got [$3]"; fails=$((fails + 1)); fi
}
grants() { sqlite3 "$DB" "SELECT capability FROM task_grant WHERE task_id = '$1' ORDER BY capability" | paste -sd, -; }
CANON='api:ci-read@acme/widgets,api:tasks:read'
REFUSAL='capability grant \"read-issues\": a name that is not a capability reference (https://...) is written as a plain string, \"read-issues\", not as a {capability, with} object'

# Item 2: the removed route and flags.
check 2.1-route-post 404 "$(code POST /api/capabilities/release-gate-read/check '{}')"
check 2.2-route-get 404 "$(code GET /api/capabilities/release-gate-read/check)"
"$GRAIN" settings -check-capability x >"$WORK/check-capability.out" 2>&1
check 2.3-settings-flag-exit 2 "$?"
check 2.4-settings-flag-msg yes "$(grep -q 'flag provided but not defined: -check-capability' "$WORK/check-capability.out" && echo yes)"
"$GRAIN" demo -legacy-capability >"$WORK/legacy-capability.out" 2>&1
check 2.5-demo-flag-exit 2 "$?"
check 2.6-demo-flag-msg yes "$(grep -q 'flag provided but not defined: -legacy-capability' "$WORK/legacy-capability.out" && echo yes)"

# Item 3: both spellings, through every way in.
code POST /api/tasks '{"title":"3.1 grants","repo":"acme/widgets","grants":["https://grain.dev/capabilities/api#tasks:read",{"capability":"https://grain.dev/capabilities/api#ci-read","with":{"repo":"acme/widgets"}}]}' >/dev/null
check 3.1-post-grants "$CANON" "$(grants "$(jq -r .id "$BODY")")"
code POST /api/tasks '{"title":"3.2 caps","repo":"acme/widgets","capabilities":["https://grain.dev/capabilities/api#tasks:read","https://grain.dev/capabilities/api#ci-read={\"repo\":\"acme/widgets\"}"]}' >/dev/null
check 3.2-post-capabilities "$CANON" "$(grants "$(jq -r .id "$BODY")")"
code POST /api/tasks '{"title":"3.3 caps objects","repo":"acme/widgets","capabilities":["https://grain.dev/capabilities/api#tasks:read",{"capability":"https://grain.dev/capabilities/api#ci-read","with":{"repo":"acme/widgets"}}]}' >/dev/null
check 3.3-post-capability-objects "$CANON" "$(grants "$(jq -r .id "$BODY")")"
g create -title "3.4 CLI" -repo acme/widgets -capability 'https://grain.dev/capabilities/api#tasks:read' \
	-capability 'https://grain.dev/capabilities/api#ci-read={"repo":"acme/widgets"}' >"$WORK/create.out"
check 3.4-cli-create "$CANON" "$(grants "$(sqlite3 "$DB" "SELECT id FROM task WHERE title = '3.4 CLI'")")"
PARENT=$(sqlite3 "$DB" "SELECT id FROM task WHERE title = '3.1 grants'")
propose "$PARENT" '{"title":"3.5 propose grants","body":"x","grants":["https://grain.dev/capabilities/api#tasks:read",{"capability":"https://grain.dev/capabilities/api#ci-read","with":{"repo":"acme/widgets"}}]}' >/dev/null
check 3.5-propose-grants "$CANON" "$(grants "$(cat "$WORK/proposed.id")")"
propose "$PARENT" '{"title":"3.6 propose caps","body":"x","capabilities":["https://grain.dev/capabilities/api#tasks:read","https://grain.dev/capabilities/api#ci-read={\"repo\":\"acme/widgets\"}"]}' >/dev/null
check 3.6-propose-capabilities "$CANON" "$(grants "$(cat "$WORK/proposed.id")")"
code POST /api/templates '{"name":"t37","title":"3.7","body":"b","capabilities":["https://grain.dev/capabilities/api#tasks:read","https://grain.dev/capabilities/api#ci-read={\"repo\":\"acme/widgets\"}"]}' >/dev/null
check 3.7-template "$CANON" "$(jq -r '.capabilities | sort | join(",")' "$BODY")"
code POST /api/schedules '{"title":"3.8","body":"b","repo":"acme/widgets","recurrence":{"kind":"everyNHours","everyNHours":24},"capabilities":["https://grain.dev/capabilities/api#tasks:read","https://grain.dev/capabilities/api#ci-read={\"repo\":\"acme/widgets\"}"]}' >/dev/null
check 3.8-schedule "$CANON" "$(jq -r '.capabilities | sort | join(",")' "$BODY")"
code GET /api/capability-methods >/dev/null
check 3.9-methods-list-api-refs-to-ui yes "$(jq -r 'if any(.[]; (.ref // "") | startswith("https://grain.dev/capabilities/api#")) then "yes" else "no" end' "$BODY")"

# Item 4 (live half): a bare label proposed live is expanded by
# ui.Client.CreateTask; the relay at run end keeps it bare (F2).
propose "$PARENT" '{"title":"4.1 propose self-debug","body":"x","capabilities":["self-debug"]}' >/dev/null
check 4.1-live-bare-label 'api:self-debug,https://grain.dev/capabilities/grain#source={}' "$(grants "$(cat "$WORK/proposed.id")")"

# Item 5: the refusal copy, verbatim, everywhere.
code POST /api/tasks '{"title":"5.1","repo":"acme/widgets","grants":[{"capability":"read-issues"}]}' >/dev/null
check 5.1-post-grants-object "{\"error\":\"grants: $REFUSAL\"}" "$(body)"
code POST /api/tasks '{"title":"5.2","repo":"acme/widgets","capabilities":[{"capability":"read-issues"}]}' >/dev/null
check 5.2-post-capabilities-object "{\"error\":\"capabilities: $REFUSAL\"}" "$(body)"
REFUSAL_PLAIN=${REFUSAL//\\\"/\"}
check 5.3-propose-grants-object "grants: $REFUSAL_PLAIN" "$(propose "$PARENT" '{"title":"5.3","body":"x","grants":[{"capability":"read-issues"}]}')"
check 5.4-propose-capabilities-object "capabilities: $REFUSAL_PLAIN" "$(propose "$PARENT" '{"title":"5.4","body":"x","capabilities":[{"capability":"read-issues"}]}')"

echo "fails=$fails"
[[ -n ${REUSE:-} ]] || stop_env
((fails == 0))
