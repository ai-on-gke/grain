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

# seed93.sh <api> <token-file> <db>: fills a schema-93 grain daemon (built
# from 0e33f85f) with every retired grant shape §8 item 1 names, on every
# surface that holds grants. Everything the schema-93 API accepts goes
# through the API; what it has no route for (or refuses) is written with
# sqlite3 against the stopped-or-running store, and says so.
set -u
api=${1:-http://127.0.0.1:7811}
tok=$(cat "${2:-/home/debian/s93/d/local-api-token}")
db=${3:-/home/debian/s93/d/store/grain.db}
c() { # c METHOD PATH [JSON]
  local out code
  out=$(curl -sS -o /tmp/seed.out -w '%{http_code}' -X "$1" -H "Authorization: Bearer $tok" \
    -H 'Content-Type: application/json' ${3:+--data "$3"} "$api$2")
  code=$out
  printf '%s %s -> %s %s\n' "$1" "$2" "$code" "$(head -c 300 /tmp/seed.out | tr '\n' ' ')"
}
# Every retired shape a task may hold on 93. image-signing, registry-push
# and package-publish are CI-only there (refused on a task, template,
# schedule or session), so they go on repo CI capabilities and the policy's
# CI defaults through the API, and onto the grant tables with sqlite3 below
# (a store that predates the CI-only refusal can hold them).
RETIRED='["gcp-key:deploy","gcp-key","claude-proxy","openai-proxy","gemini-proxy","gemini-key"]'
CIONLY="image-signing registry-push package-publish"

# Deployment GCP settings (the default pool) and a named gcp_key row, so
# migration 97 has something to seed sets from, and gcp_project for 103.
c PUT /api/config '{"gcpProject":"walk-proj","gcpServiceAccountEmail":"grain-sa@walk-proj.iam.gserviceaccount.com","gcpServiceAccountCount":2}'
c POST /api/gcp-keys '{"name":"deploy","projectId":"walk-deploy","serviceAccountEmail":"deployer@walk-deploy.iam.gserviceaccount.com","serviceAccountCount":1,"maxKeyAgeHours":2}'

c PUT /api/github-tokens/default '{"value":"walk-stub-pat-not-a-real-token"}'
c POST /api/repos '{"repo":"acme/widgets"}'
c PUT /api/repos/acme/widgets/ci-capabilities '{"ciCapabilities":["gcp-key","claude-proxy","image-signing","registry-push","package-publish","gemini-key"]}'
# grain-ci cannot be attached by hand on 93 either: the CI mode grants it.
c PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"grain"}'
c PUT /api/repos/acme/widgets/capabilities '{"defaultCapabilities":["gcp-key:deploy","claude-proxy","gemini-key"]}'
c PUT /api/config "{\"defaultCapabilities\":[\"gcp-key\",\"openai-proxy\"]}"

c POST /api/tasks "{\"title\":\"retired grants\",\"description\":\"schema-93 seed\",\"repo\":\"acme/widgets\",\"capabilities\":$RETIRED}"
c POST /api/templates "{\"name\":\"retired-template\",\"title\":\"retired template\",\"description\":\"seed\",\"repo\":\"acme/widgets\",\"capabilities\":$RETIRED}"
c POST /api/schedules "{\"name\":\"retired-schedule\",\"recurrence\":{\"kind\":\"daily\",\"timeOfDay\":\"03:00\"},\"title\":\"nightly\",\"description\":\"seed\",\"repo\":\"acme/widgets\",\"capabilities\":$RETIRED}"
c POST /api/console-sessions "{\"id\":\"retired-session\",\"name\":\"retired-session\",\"title\":\"Retired session\",\"capabilities\":$RETIRED}"

# The capability policy (its rules take capability references only, so
# retired names live in the catalogue): catalogue defaults
# for tasks, CI and one repository, and in-process entries for each
# retired provider.
c PUT /api/capability-policy '{"policy":{"catalogue":{"defaults":{"task":["gcp-key","claude-proxy","gemini-key"],"ci":["image-signing","package-publish","gcp-key:deploy","grain-ci"],"repos":{"acme/widgets":{"task":["gcp-key:deploy","gemini-proxy"],"ci":["registry-push","openai-proxy"]}}},"inProcess":{"gcp-key":{"enabled":true},"claude-proxy":{"enabled":true},"image-signing":{"enabled":false},"gemini-key":{"enabled":true},"grain-ci":{"enabled":true}}}}}'

# A custom command connection beside a real one.
c PUT /api/config '{"agentBackends":[{"id":"stub","name":"Stub","framework":"claude"},{"id":"my-custom","name":"My custom agent","framework":"custom","command":"/usr/local/bin/my-agent --prompt-file {prompt}"}]}'

# What the schema-93 API refuses, written with sqlite3 (daemon stopped):
# the CI-only release grants on the three grant tables, grain-ci on the
# template and the schedule (task 2 holds it already, via=default, from
# the repo's CI mode), and both on the console session.
pkill -x grain93 && sleep 3
case $db in
postgres://*)
  psql -X -q "$db" <<'SQL'
INSERT INTO task_grant VALUES ('2','image-signing','label',''),('2','registry-push','label',''),('2','package-publish','label','');
INSERT INTO template_grant VALUES ('template-1','image-signing','label',''),('template-1','registry-push','label',''),('template-1','package-publish','label',''),('template-1','grain-ci','label','');
INSERT INTO schedule_grant VALUES ('sched-1','image-signing','label',''),('sched-1','registry-push','label',''),('sched-1','package-publish','label',''),('sched-1','grain-ci','label','');
UPDATE grain_config SET console_sessions = (
  SELECT json_agg(CASE WHEN s->>'ID' = 'retired-session'
    THEN jsonb_set(s, '{Capabilities}', (s->'Capabilities') || '["grain-ci","image-signing"]'::jsonb)
    ELSE s END)::text
  FROM jsonb_array_elements(console_sessions::jsonb) s) WHERE id = 1;
SQL
  ;;
*)
for cap in image-signing registry-push package-publish; do
  sqlite3 "$db" "INSERT INTO task_grant VALUES('2','$cap','label','');
    INSERT INTO template_grant VALUES('template-1','$cap','label','');
    INSERT INTO schedule_grant VALUES('sched-1','$cap','label','');"
done
sqlite3 "$db" "INSERT INTO template_grant VALUES('template-1','grain-ci','label','');
  INSERT INTO schedule_grant VALUES('sched-1','grain-ci','label','');"
python3 - "$db" <<'PY'
import json, sqlite3, sys
con = sqlite3.connect(sys.argv[1])
(raw,) = con.execute("SELECT console_sessions FROM grain_config WHERE id=1").fetchone()
sessions = json.loads(raw)
for s in sessions:
    if s.get("ID") == "retired-session":
        s["Capabilities"] += ["grain-ci", "image-signing"]
con.execute("UPDATE grain_config SET console_sessions=? WHERE id=1", (json.dumps(sessions),))
con.commit()
PY
  ;;
esac
