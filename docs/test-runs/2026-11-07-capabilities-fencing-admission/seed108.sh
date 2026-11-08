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

# seed108.sh <api> <token-file> <db> <daemon-pid>: fills a schema-108
# grain daemon (built from cc66a6bc) with every retired grant shape §6
# item 1 names, on every surface that holds grants. The rows (task,
# template, schedule, repository, console session) are made through the
# schema-108 API; the grants themselves are then written with SQL against
# the stopped store, so SQLite and PostgreSQL receive byte-identical rows
# whatever the 108 API would have accepted.
set -u
api=$1
tok=$(cat "$2")
db=$3
pid=$4
c() { # c METHOD PATH [JSON]
  local code
  code=$(curl -sS -o /tmp/seed.out -w '%{http_code}' -X "$1" -H "Authorization: Bearer $tok" \
    -H 'Content-Type: application/json' ${3:+--data "$3"} "$api$2")
  printf '%s %s -> %s %s\n' "$1" "$2" "$code" "$(head -c 200 /tmp/seed.out | tr '\n' ' ')"
}
c PUT /api/github-tokens/default '{"value":"walk-stub-pat-not-a-real-token"}'
c PUT /api/github-tokens/bot '{"value":"walk-stub-bot-pat-not-a-real-token"}'
c POST /api/repos '{"repo":"acme/widgets"}'
c POST /api/repos '{"repo":"acme/denied"}'
c POST /api/tasks '{"title":"retired grants","description":"schema-108 seed","repo":"acme/widgets"}'
c POST /api/tasks '{"title":"priority only","description":"schema-108 seed","repo":"acme/widgets"}'
c POST /api/tasks '{"title":"durable only","description":"schema-108 seed","repo":"acme/widgets"}'
c POST /api/templates '{"name":"retired-template","title":"retired template","description":"seed","repo":"acme/widgets"}'
c POST /api/schedules '{"name":"retired-schedule","recurrence":{"kind":"daily","timeOfDay":"03:00"},"title":"nightly","description":"seed","repo":"acme/widgets"}'
c POST /api/console-sessions '{"id":"retired-session","name":"retired-session","title":"Retired session"}'
kill "$pid"; sleep 4

case $db in
postgres://*) q() { psql -X -q -At "$db" -c "$1"; } ;;
*) q() { sqlite3 "$db" "$1"; } ;;
esac
tpl=$(q "SELECT id FROM template WHERE name='retired-template'")
sch=$(q "SELECT id FROM schedule WHERE title='nightly'")
t1=$(q "SELECT id FROM task WHERE title='retired grants'")
t2=$(q "SELECT id FROM task WHERE title='priority only'")
t3=$(q "SELECT id FROM task WHERE title='durable only'")
echo "task=$t1,$t2,$t3 template=$tpl schedule=$sch"

GCP='https://grain.dev/capabilities/gcp'
DEFAULT_SET="$GCP#gcp-key={\"account\":\"default\",\"activate\":true}"
GHOST_SET="$GCP#gcp-key={\"account\":\"ghost-set\",\"activate\":false}"
NAMED_SET="$GCP#gcp-key={\"account\":\"deploy\"}"
RETIRED=("$DEFAULT_SET" "$GHOST_SET" self-debug bootstrap-playbooks github-credential:bot read-issues priority-dispatch durable)

sql=/tmp/seed108.sql
: >"$sql"
esc() { printf "%s" "${1//\'/\'\'}"; }
for g in "${RETIRED[@]}"; do
  e=$(esc "$g")
  if [ "${CLEAN:-0}" = 1 ] && [ "$g" = "$GHOST_SET" ]; then
    echo "INSERT INTO task_grant (task_id, capability, via) VALUES ('$t1', '$e', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
    continue
  fi
  echo "INSERT INTO task_grant (task_id, capability, via) VALUES ('$t1', '$e', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
  echo "INSERT INTO template_grant (template_id, capability, via) VALUES ('$tpl', '$e', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
  echo "INSERT INTO schedule_grant (schedule_id, capability, via) VALUES ('$sch', '$e', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
done
echo "INSERT INTO task_grant (task_id, capability, via) VALUES ('$t2', 'priority-dispatch', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
echo "INSERT INTO task_grant (task_id, capability, via) VALUES ('$t3', 'durable', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
echo "INSERT INTO task_grant (task_id, capability, via) VALUES ('$t3', '$(esc "$NAMED_SET")', 'label') ON CONFLICT DO NOTHING;" >>"$sql"

# Deployment and repository defaults, repository CI capabilities, the gcp
# server's permission sets, the console session and the capability policy,
# all edited as JSON from what the 108 daemon wrote.
for col in default_capabilities mcp_servers console_sessions capability_policy; do
  q "SELECT $col FROM grain_config WHERE id=1" >"/tmp/seed108.$col"
done
CLEAN=${CLEAN:-0} python3 - "$sql" "$DEFAULT_SET" "$GHOST_SET" "$NAMED_SET" <<'PY'
import json, os, sys
sql, default_set, ghost_set, named_set = sys.argv[1:5]
def read(col):
    raw = open(f"/tmp/seed108.{col}").read().strip()
    return json.loads(raw) if raw else None
def lit(v):
    return "'" + json.dumps(v, sort_keys=True).replace("'", "''") + "'"
# CLEAN=1 leaves out the two shapes the HEAD export refuses (see the
# report): a gcp-key grant naming an unconfigured set anywhere the state
# repository carries (only task 2 keeps it), and read-issues granted while
# in_process.read-issues is off (it is left on).
clean = os.environ["CLEAN"] == "1"
retired = [default_set, ghost_set, "self-debug", "bootstrap-playbooks",
           "github-credential:bot", "read-issues", "priority-dispatch", "durable"]
out = open(sql, "a")
out.write("UPDATE grain_config SET default_capabilities = %s WHERE id = 1;\n" % lit(
    [default_set, "self-debug", "priority-dispatch", "read-issues"]))
servers = read("mcp_servers") or {}
servers["https://grain.dev/capabilities/gcp"] = {
    "command": "grain", "args": ["capability", "gcp"], "builtin": "gcp",
    "config": {"admin_credential_source": "ambient", "permission_sets": [
        {"name": "sandbox-agent", "project": "walk-proj", "roles": ["roles/viewer"]},
        {"name": "deploy", "project": "walk-proj", "roles": ["roles/run.developer"]}]}}
out.write("UPDATE grain_config SET mcp_servers = %s WHERE id = 1;\n" % lit(servers))
sessions = read("console_sessions") or []
for s in sessions:
    if s.get("ID") == "retired-session":
        s["Capabilities"] = [g for g in retired if not (clean and g == ghost_set)]
out.write("UPDATE grain_config SET console_sessions = %s WHERE id = 1;\n" % lit(sessions))
policy = read("capability_policy") or {}
policy["rules"] = [
    # A deny on the retired in-process name must survive as a deny on what
    # replaced it, and bind the implicit skill alias of config-map.
    {"capability": "bootstrap-playbooks", "repos": ["acme/denied"], "deny": True},
    {"capability": "read-issues", "repos": ["acme/denied"], "deny": True},
    {"capability": "github-credential:bot", "repos": ["acme/denied"], "deny": True},
    {"capability": "self-debug", "runKinds": ["task"]},
    {"capability": "priority-dispatch", "repos": ["acme/denied"], "deny": True},
    {"capability": "durable", "runKinds": ["task"]},
    {"capability": "https://grain.dev/capabilities/skill#config-map", "repos": ["acme/skill-denied"], "deny": True},
]
policy["catalogue"] = {
    "defaults": {
        # read-issues is switched off in inProcess below, so it is kept out
        # of the catalogue defaults (a catalogue naming a disabled default
        # is refused on export whatever the schema; 2026-11-04 F2).
        "task": ["self-debug", "priority-dispatch", default_set],
        "ci": ["bootstrap-playbooks", "github-credential:bot", "durable"] + (
            [ghost_set] if not clean else []),
        "repos": {"acme/widgets": {"task": [named_set, "durable"], "ci": ["priority-dispatch", "self-debug"]}},
    },
    "servers": [{"id": "https://grain.dev/capabilities/gcp", "methods": [{"name": "gcp-key", "args": {
        "account": {"allowed": ["default", "deploy", "ghost-set"]}, "activate": {"allowed": ["true", "false"]}}}]}],
    "inProcess": {"self-debug": {"enabled": True}, "read-issues": {"enabled": clean},
                  "bootstrap-playbooks": {"enabled": True}, "priority-dispatch": {"enabled": True},
                  "durable": {"enabled": True}},
}
out.write("UPDATE grain_config SET capability_policy = %s WHERE id = 1;\n" % lit(policy))
# The 108 API adds repositories to the config without a repo_config row,
# so the row is written here: default capabilities and CI capabilities.
out.write("INSERT INTO repo_config (owner, name, default_capabilities, ci_capabilities) VALUES ('acme', 'widgets', %s, %s) "
          "ON CONFLICT (owner, name) DO UPDATE SET default_capabilities = excluded.default_capabilities, ci_capabilities = excluded.ci_capabilities;\n" % (
    lit([named_set, "bootstrap-playbooks", "github-credential:bot", "durable"]),
    lit(([] if clean else [ghost_set]) + ["self-debug", "read-issues", "bootstrap-playbooks", "github-credential:bot", "priority-dispatch"])))
PY
case $db in
postgres://*) psql -X -q -v ON_ERROR_STOP=1 "$db" -f "$sql" ;;
*) sqlite3 -bail "$db" <"$sql" ;;
esac
echo "seeded: $(wc -l <"$sql") statements"
