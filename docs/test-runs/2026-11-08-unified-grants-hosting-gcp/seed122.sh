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

# seed122.sh <api> <token-file> <db> <daemon-pid>: seeds a schema-122 store
# (SQLite or PostgreSQL) built by grain122 with realistic rows covering
# migrations 123-128:
#   - tasks, templates, schedules, task_grant_wait rows, and console sessions
#     carrying `console` and `github-sandbox`;
#   - grain_config.default_capabilities and repo_config.default_capabilities /
#     repo_config.ci_capabilities carrying `console` and `github-sandbox`;
#   - capability_policy rules and catalogue defaults referencing `console`,
#     `github-sandbox`, and `grain-ci`;
#   - gcp#gcp-key={"account":"unconfigured-set"} on a template, schedule,
#     task, and repo default (the shape migration 109 previously left behind).
set -euo pipefail

api=$1
tok=$(cat "$2")
db=$3
pid=$4

req() {
  local code
  code=$(curl -sS -o /tmp/seed122.out -w '%{http_code}' -X "$1" \
    -H "Authorization: Bearer $tok" \
    -H 'Content-Type: application/json' \
    ${3:+--data "$3"} "$api$2")
  printf '%s %s -> %s %s\n' "$1" "$2" "$code" "$(head -c 180 /tmp/seed122.out | tr '\n' ' ')"
}

req PUT /api/github-tokens/default '{"value":"walk-stub-pat-not-real"}'
req POST /api/repos '{"repo":"acme/widgets"}'
req POST /api/repos '{"repo":"acme/denied"}'
req POST /api/tasks '{"title":"console and sandbox task","description":"schema-122 seed","repo":"acme/widgets"}'
req POST /api/tasks '{"title":"waiting on github-sandbox","description":"schema-122 wait seed","repo":"acme/widgets","approved":true}'
req POST /api/tasks '{"title":"waiting on console","description":"schema-122 console wait seed","repo":"acme/widgets","approved":true}'
req POST /api/templates '{"name":"legacy-template","title":"legacy template","description":"seed","repo":"acme/widgets"}'
req POST /api/schedules '{"name":"legacy-schedule","recurrence":{"kind":"daily","timeOfDay":"04:00"},"title":"nightly legacy","description":"seed","repo":"acme/widgets"}'
req POST /api/console-sessions '{"id":"console-dev","name":"console-dev","title":"Console dev session"}'

kill "$pid"
for _ in $(seq 30); do
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.2
done

case "$db" in
postgres://*) q() { psql -X -q -At "$db" -c "$1"; } ;;
*) q() { sqlite3 "$db" "$1"; } ;;
esac

t1=$(q "SELECT id FROM task WHERE title='console and sandbox task'")
t2=$(q "SELECT id FROM task WHERE title='waiting on github-sandbox'")
t3=$(q "SELECT id FROM task WHERE title='waiting on console'")
tpl=$(q "SELECT id FROM template WHERE name='legacy-template'")
sch=$(q "SELECT id FROM schedule WHERE title='nightly legacy'")
echo "seeded ids: tasks=$t1,$t2,$t3 template=$tpl schedule=$sch"

GCP_UNCONFIGURED='https://grain.dev/capabilities/gcp#gcp-key={"account":"unconfigured-set"}'
GCP_CONFIGURED='https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"deploy","primary":true}'

sql=/tmp/seed122.sql
: >"$sql"
esc() { printf "%s" "${1//\'/\'\'}"; }

for g in console github-sandbox "$GCP_UNCONFIGURED"; do
  eg=$(esc "$g")
  echo "INSERT INTO task_grant (task_id, capability, via) VALUES ('$t1', '$eg', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
  echo "INSERT INTO template_grant (template_id, capability, via) VALUES ('$tpl', '$eg', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
  echo "INSERT INTO schedule_grant (schedule_id, capability, via) VALUES ('$sch', '$eg', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
done

echo "INSERT INTO task_grant (task_id, capability, via) VALUES ('$t2', 'github-sandbox', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
echo "INSERT INTO task_grant (task_id, capability, via) VALUES ('$t3', 'console', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
echo "INSERT INTO task_grant_wait (task_id, capability, resource, waiting_at) VALUES ('$t2', 'github-sandbox', 'GitHub App rate limit', '2026-11-08T22:00:00Z') ON CONFLICT (task_id) DO UPDATE SET capability = excluded.capability, resource = excluded.resource;" >>"$sql"
echo "INSERT INTO task_grant_wait (task_id, capability, resource, waiting_at) VALUES ('$t3', 'console', 'interactive slot', '2026-11-08T22:01:00Z') ON CONFLICT (task_id) DO UPDATE SET capability = excluded.capability, resource = excluded.resource;" >>"$sql"

for col in default_capabilities mcp_servers console_sessions capability_policy; do
  q "SELECT $col FROM grain_config WHERE id=1" >"/tmp/seed122.$col"
done

python3 - "$sql" "$GCP_UNCONFIGURED" "$GCP_CONFIGURED" <<'PY'
import json, sys

sql_path, gcp_uncfg, gcp_cfg = sys.argv[1:4]

def read(col):
    raw = open(f"/tmp/seed122.{col}").read().strip()
    return json.loads(raw) if raw else None

def lit(v):
    return "'" + json.dumps(v, sort_keys=True).replace("'", "''") + "'"

out = open(sql_path, "a")

# Deployment default capabilities include console and github-sandbox.
out.write("UPDATE grain_config SET default_capabilities = %s WHERE id = 1;\n" % lit(
    ["console", "github-sandbox", "api:tasks:read"]))

servers = read("mcp_servers") or {}
servers["https://grain.dev/capabilities/gcp"] = {
    "command": "grain",
    "args": ["capability", "gcp"],
    "builtin": "gcp",
    "config": {
        "admin_credential_source": "ambient",
        "permission_sets": [
            {"name": "deploy", "project": "walk-proj", "roles": ["roles/run.developer"]}
        ],
    },
}
out.write("UPDATE grain_config SET mcp_servers = %s WHERE id = 1;\n" % lit(servers))

sessions = read("console_sessions") or []
for s in sessions:
    sid = s.get("ID") or s.get("id")
    if sid == "console-dev":
        key = "Capabilities" if "Capabilities" in s else "capabilities"
        s[key] = ["console", "github-sandbox", "api:tasks:read"]
out.write("UPDATE grain_config SET console_sessions = %s WHERE id = 1;\n" % lit(sessions))

policy = read("capability_policy") or {}
policy["rules"] = [
    {"capability": "console", "repos": ["acme/denied"], "deny": True},
    {"capability": "github-sandbox", "repos": ["acme/denied"], "deny": True},
    {"capability": "grain-ci", "repos": ["acme/denied"], "deny": True},
    {"capability": "github-sandbox", "runKinds": ["task", "ci"]},
    {"capability": "grain-ci", "runKinds": ["task"]},
]
policy["catalogue"] = {
    "defaults": {
        "task": ["console", "github-sandbox", "api:tasks:read"],
        "ci": ["github-sandbox"],
        "repos": {
            "acme/widgets": {
                "task": ["console", "github-sandbox", gcp_uncfg, "grain-ci"],
                "ci": ["github-sandbox"],
            }
        },
    },
    "servers": [
        {
            "id": "https://grain.dev/capabilities/gcp",
            "enabled": True,
            "methods": [
                {
                    "name": "gcp-key",
                    "args": {
                        "permission_set": {"allowed": ["deploy", "unconfigured-set"]}
                    },
                }
            ],
        },
        {"id": "https://grain.dev/capabilities/github", "enabled": True},
        {"id": "https://grain.dev/capabilities/grain", "enabled": True},
    ],
    "inProcess": {
        "console": {"enabled": True},
        "github-sandbox": {"enabled": True},
    },
}
out.write("UPDATE grain_config SET capability_policy = %s WHERE id = 1;\n" % lit(policy))

out.write(
    "INSERT INTO repo_config (owner, name, ci_mode, default_capabilities, ci_capabilities) "
    "VALUES ('acme', 'widgets', 'grain', %s, %s) "
    "ON CONFLICT (owner, name) DO UPDATE SET "
    "ci_mode = excluded.ci_mode, "
    "default_capabilities = excluded.default_capabilities, "
    "ci_capabilities = excluded.ci_capabilities;\n"
    % (
        lit(["console", "github-sandbox", gcp_uncfg]),
        lit(["github-sandbox"]),
    )
)
PY

case "$db" in
postgres://*) psql -X -q -v ON_ERROR_STOP=1 "$db" -f "$sql" ;;
*) sqlite3 -bail "$db" <"$sql" ;;
esac
echo "applied $(wc -l <"$sql") SQL statements"
