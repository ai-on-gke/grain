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

# docs/test-plans/2026-11-09.md §1 items 1 and 3 (seed half): file a
# task, a template, a schedule, the deployment defaults and acme/widgets'
# repo defaults holding both API grant spellings through the real API,
# then -- since every write path now rewrites a retired bare alias
# (self-debug -> api:self-debug + grain#source={}) -- plant a held bare
# id the way an older store still has one, with the daemon stopped, and
# restart it. Writes before.json: every object as GET answers it.
#
#   bash item1_seed.sh            # starts its own environment
#   REUSE=1 bash item1_seed.sh    # seeds the one already running
set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
if [[ -n ${REUSE:-} ]]; then
	WORK=${WORK:-/var/tmp/access-forms}
	CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	DB="$WORK/data/store/grain.db" BODY="$WORK/body.json"
else
	start_env
fi
OUT=${OUT:-$HERE}
must() { local want=$1 got; shift; got=$("$@"); [[ $got == "$want" ]] || { echo "FAIL $* -> $got $(body)"; exit 1; }; }
API_TASKS='"https://grain.dev/capabilities/api#tasks:read"'
API_CI='"https://grain.dev/capabilities/api#ci-read={\"repo\":\"acme/widgets\"}"'

must 201 code POST /api/tasks "{\"title\":\"Held bare id task\",\"repo\":\"acme/widgets\",\"capabilities\":[$API_TASKS,$API_CI]}"
TASK=$(jq -r .id "$BODY")
must 201 code POST /api/templates "{\"name\":\"held-bare\",\"title\":\"Held bare id template\",\"body\":\"b\",\"capabilities\":[$API_TASKS,$API_CI]}"
TMPL=$(jq -r .id "$BODY")
must 201 code POST /api/schedules "{\"title\":\"Held bare id schedule\",\"body\":\"b\",\"repo\":\"acme/widgets\",\"recurrence\":{\"kind\":\"everyNHours\",\"everyNHours\":24},\"capabilities\":[$API_TASKS,$API_CI]}"
SCHED=$(jq -r .id "$BODY")
must 200 code PUT /api/settings "{\"defaultCapabilities\":[$API_TASKS]}"
must 200 code PUT /api/repos/acme/widgets/capabilities "{\"defaultCapabilities\":[$API_CI]}"

# Plant the held bare ids: self-debug on every agent-side set, and the
# live CI alias release-gate-read on the repository's CI capabilities.
stop_env_daemon() { kill "$(cat "$WORK/daemon.pid")"; for _ in $(seq 50); do kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null || break; sleep 0.1; done; }
stop_env_daemon
sqlite3 "$DB" <<SQL
INSERT INTO task_grant (task_id, capability, via) VALUES ('$TASK', 'self-debug', 'label');
INSERT INTO template_grant (template_id, capability, via) VALUES ('$TMPL', 'self-debug', 'label');
INSERT INTO schedule_grant (schedule_id, capability, via) VALUES ('$SCHED', 'self-debug', 'label');
UPDATE grain_config SET default_capabilities = 'self-debug,' || default_capabilities;
UPDATE repo_config SET default_capabilities = 'self-debug,' || default_capabilities, ci_capabilities = 'release-gate-read' WHERE owner = 'acme' AND name = 'widgets';
SQL
start_daemon

printf '{"task":"%s","template":"%s","schedule":"%s"}\n' "$TASK" "$TMPL" "$SCHED" >"$OUT/seeded-ids.json"
snapshot >"$OUT/before.json"
cat "$OUT/before.json"
