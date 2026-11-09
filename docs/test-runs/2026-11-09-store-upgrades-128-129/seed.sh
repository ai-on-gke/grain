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

# seed.sh <api> <token-file> <db> <daemon-pid> <legacy|canonical>: fills a
# store an old grain daemon wrote with the 2026-11-07 O1 shape -- a grant
# the store's own capability catalogue disables (in_process.read-issues
# off) sitting in deployment defaults, catalogue defaults, a repository's
# defaults and CI capabilities, a template and a schedule -- plus a task
# parked on a grant wait under the retired name github-sandbox (and the
# same stale wait row on the proposed task).
#
# The rows (repository, tasks, template, schedule) are made through the old
# daemon's API; the grants are written with SQL against the stopped store
# so SQLite and PostgreSQL get byte-identical rows whatever the old API
# accepts (api-probe.sh records what it refuses).
#
# legacy:    names as v3.1.0 (schema 87) stored them (read-issues and
#            github-sandbox were still live names; test-quarantine was
#            already api:test-quarantine, migration 76), comma lists.
# canonical: names as a 119/127 store holds them after migrations 67-124
#            (github#read-issues, api:test-quarantine, github#sandbox-repo
#            on the task), JSON lists -- except task_grant_wait, which
#            keeps github-sandbox: the stale row migration 129 is for.
set -u
api=$1
tok=$(cat "$2")
db=$3
pid=$4
style=$5
c() { # c METHOD PATH [JSON]
  local code
  code=$(curl -sS -o /tmp/seed.out -w '%{http_code}' -X "$1" -H "Authorization: Bearer $tok" \
    -H 'Content-Type: application/json' ${3:+--data "$3"} "$api$2")
  cat /tmp/seed.out >>/tmp/seed.all; echo >>/tmp/seed.all
  printf '%s %s -> %s %s\n' "$1" "$2" "$code" "$(head -c 160 /tmp/seed.out | tr '\n' ' ')"
}
: >/tmp/seed.all
c PUT /api/github-tokens/default '{"value":"walk-stub-pat-not-a-real-token"}'
c POST /api/repos '{"repo":"acme/widgets"}'
c POST /api/tasks '{"title":"disabled grant","description":"store-upgrade seed","repo":"acme/widgets"}'
c POST /api/tasks '{"title":"parked on github-sandbox","description":"store-upgrade seed","repo":"acme/widgets"}'
t2=$(sed -n 's/.*"id":"\([0-9]*\)","title":"parked on github-sandbox".*/\1/p' /tmp/seed.all)
c POST "/api/tasks/$t2/approve" '{}'
c POST /api/templates '{"name":"disabled-template","title":"disabled template","description":"seed","repo":"acme/widgets"}'
c POST /api/schedules '{"name":"disabled-schedule","recurrence":{"kind":"daily","timeOfDay":"03:00"},"title":"nightly","description":"seed","repo":"acme/widgets"}'
kill "$pid"
for _ in $(seq 30); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done

case $db in
postgres://*) q() { psql -X -q -At "$db" -c "$1"; } ;;
*) q() { sqlite3 "$db" "$1"; } ;;
esac
tpl=$(q "SELECT id FROM template WHERE name='disabled-template'")
sch=$(q "SELECT id FROM schedule WHERE title='nightly'")
t1=$(q "SELECT id FROM task WHERE title='disabled grant'")
t2=$(q "SELECT id FROM task WHERE title='parked on github-sandbox'")
echo "task=$t1,$t2 template=$tpl schedule=$sch"

if [ "$style" = legacy ]; then
  DIS=read-issues KEEP=api:test-quarantine SANDBOX=github-sandbox
else
  DIS='https://grain.dev/capabilities/github#read-issues={}'
  KEEP=api:test-quarantine
  SANDBOX='https://grain.dev/capabilities/github#sandbox-repo={}'
fi
sql=/tmp/seed-upgrade.sql
: >"$sql"
for g in "$DIS" "$KEEP"; do
  echo "INSERT INTO task_grant (task_id, capability, via) VALUES ('$t1', '$g', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
  echo "INSERT INTO template_grant (template_id, capability, via) VALUES ('$tpl', '$g', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
  echo "INSERT INTO schedule_grant (schedule_id, capability, via) VALUES ('$sch', '$g', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
done
echo "INSERT INTO task_grant (task_id, capability, via) VALUES ('$t2', '$SANDBOX', 'label') ON CONFLICT DO NOTHING;" >>"$sql"
echo "INSERT INTO task_grant_wait (task_id, capability, resource, waiting_at) VALUES ('$t2', 'github-sandbox', 'github-app', '2026-11-09 04:00:00') ON CONFLICT DO NOTHING;" >>"$sql"
# The same stale row on the proposed task, which admission never probes, so
# the dump after the upgrade shows what migration 129 itself left (the
# approved task's wait is re-derived by HEAD's first admission cycle).
echo "INSERT INTO task_grant_wait (task_id, capability, resource, waiting_at) VALUES ('$t1', 'github-sandbox', 'github-app', '2026-11-09 04:00:00') ON CONFLICT DO NOTHING;" >>"$sql"

q "SELECT capability_policy FROM grain_config WHERE id=1" >/tmp/seed-upgrade.policy
python3 - "$sql" "$style" "$DIS" "$KEEP" <<'PY'
import json, sys
sql, style, dis, keep = sys.argv[1:5]
def lst(items):
    v = ",".join(items) if style == "legacy" else json.dumps(items)
    return "'" + v.replace("'", "''") + "'"
def lit(v):
    return "'" + json.dumps(v, sort_keys=True).replace("'", "''") + "'"
raw = open("/tmp/seed-upgrade.policy").read().strip()
policy = json.loads(raw) if raw else {}
policy["catalogue"] = {
    "defaults": {
        "task": [dis, keep],
        "ci": [dis],
        "repos": {"acme/widgets": {"task": [dis], "ci": [dis, keep]}},
    },
    "inProcess": {"read-issues": {"enabled": False}},
}
out = open(sql, "a")
out.write("UPDATE grain_config SET default_capabilities = %s WHERE id = 1;\n" % lst([dis, keep]))
out.write("UPDATE grain_config SET capability_policy = %s WHERE id = 1;\n" % lit(policy))
out.write("INSERT INTO repo_config (owner, name, default_capabilities, ci_capabilities) VALUES ('acme', 'widgets', %s, %s) "
          "ON CONFLICT (owner, name) DO UPDATE SET default_capabilities = excluded.default_capabilities, ci_capabilities = excluded.ci_capabilities;\n"
          % (lst([dis, keep]), lst([keep, dis])))
PY
case $db in
postgres://*) psql -X -q -v ON_ERROR_STOP=1 "$db" -f "$sql" ;;
*) sqlite3 -bail "$db" <"$sql" ;;
esac
echo "seeded: $(wc -l <"$sql") statements"
