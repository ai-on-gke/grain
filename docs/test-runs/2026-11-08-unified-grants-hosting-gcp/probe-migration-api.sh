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

# probe-migration-api.sh <api> <token-file> <grain-bin>: probes live REST
# deprecation warnings (legacy `console` and `github-sandbox`), `.grain/`
# declaration checks, and #851 merged-catalogue HTTP 400 validation against a
# migrated daemon without leaving mutated policy/defaults behind.
set -euo pipefail

api=$1
tok=$(cat "$2")
gbin=$3

call() {
  local label=$1 method=$2 path=$3 body=${4:-}
  local code
  code=$(curl -sS -o /tmp/probe-api.out -w '%{http_code}' -X "$method" \
    -H "Authorization: Bearer $tok" \
    -H 'Content-Type: application/json' \
    ${body:+--data "$body"} "$api$path")
  printf '[%s] %s %s -> HTTP %s\n' "$label" "$method" "$path" "$code"
  python3 -c '
import json, sys
raw = open("/tmp/probe-api.out").read().strip()
try:
    d = json.loads(raw)
    if isinstance(d, dict):
        keep = {k: d[k] for k in ("id","name","hosting","capabilities","warnings","error","fields","activity","activityBySetup") if k in d}
        print("  ", json.dumps(keep if keep else d, sort_keys=True)[:350])
    else:
        print("  ", str(d)[:350])
except Exception:
    print("  ", raw[:350])
'
}

echo "=== A. Inspect migrated task 3 (queued with task_grant_wait) ==="
call "migrated-task-3" GET /api/tasks/3

echo "=== B. Legacy console & github-sandbox over REST (one-release deprecation) ==="
call "legacy-rest-task" POST /api/tasks \
  '{"title":"legacy console+sandbox via REST","description":"probe","repo":"acme/widgets","capabilities":["console","github-sandbox"]}'
legacy_tid=$(python3 -c 'import json; print(json.load(open("/tmp/probe-api.out"))["id"])')

call "legacy-rest-template" POST /api/templates \
  '{"name":"probe-legacy-tpl","title":"probe tpl","description":"probe","repo":"acme/widgets","capabilities":["console","github-sandbox"]}'
legacy_tpl=$(python3 -c 'import json; print(json.load(open("/tmp/probe-api.out"))["id"])')

call "legacy-rest-schedule" POST /api/schedules \
  '{"name":"probe-legacy-sch","recurrence":{"kind":"daily","timeOfDay":"05:00"},"title":"probe sch","description":"probe","repo":"acme/widgets","capabilities":["console","github-sandbox"]}'
legacy_sch=$(python3 -c 'import json; print(json.load(open("/tmp/probe-api.out"))["id"])')

# Clean up temporary task/template/schedule so exported state-repo matches prepared
curl -sS -X DELETE -H "Authorization: Bearer $tok" "$api/api/templates/$legacy_tpl" >/dev/null
curl -sS -X DELETE -H "Authorization: Bearer $tok" "$api/api/schedules/$legacy_sch" >/dev/null
curl -sS -X POST -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' \
  --data '{"reason":"cleanup probe"}' "$api/api/tasks/$legacy_tid/close" >/dev/null || true

echo "=== C. #851 Merged-catalogue validation (both save orders -> HTTP 400) ==="
# Snapshot current policy so we can test disabling a server/inProcess capability currently in defaults/CI
curl -fsS -H "Authorization: Bearer $tok" "$api/api/capability-policy" >/tmp/current-policy.json

# 1. Disable github server in capability-policy while github#sandbox-repo={} is in deployment defaults & acme/widgets CI -> expect 400
bad_disable_github=$(python3 - <<'PY'
import json
cur = json.load(open("/tmp/current-policy.json"))
pol = cur.get("policy", cur)
cat = pol.setdefault("catalogue", {})
servers = cat.setdefault("servers", [])
found = False
for s in servers:
    if s.get("id") == "https://grain.dev/capabilities/github":
        s["enabled"] = False
        found = True
if not found:
    servers.append({"id": "https://grain.dev/capabilities/github", "enabled": False})
print(json.dumps({"policy": pol}))
PY
)
call "851-disable-github-while-in-defaults-and-ci" PUT /api/capability-policy "$bad_disable_github"

# 2. Disable retired inProcess github-sandbox while github#sandbox-repo={} is in defaults & CI -> expect 400
bad_disable_inproc=$(python3 - <<'PY'
import json
cur = json.load(open("/tmp/current-policy.json"))
pol = cur.get("policy", cur)
cat = pol.setdefault("catalogue", {})
cat.setdefault("inProcess", {})["github-sandbox"] = {"enabled": False}
print(json.dumps({"policy": pol}))
PY
)
call "851-disable-inproc-alias-while-in-defaults-and-ci" PUT /api/capability-policy "$bad_disable_inproc"

# 3. Reverse order: disable release & corp servers in policy (200 OK since unused), then try adding release#image-signing={} to acme/widgets ciCapabilities -> expect 400, and corp#research={} to deployment defaultCapabilities -> expect 400!
disable_unused=$(python3 - <<'PY'
import json
cur = json.load(open("/tmp/current-policy.json"))
pol = cur.get("policy", cur)
cat = pol.setdefault("catalogue", {})
servers = [s for s in cat.get("servers", []) if s.get("id") not in ("https://grain.dev/capabilities/release", "https://grain.dev/capabilities/corp")]
servers.append({"id": "https://grain.dev/capabilities/release", "enabled": False})
servers.append({"id": "https://grain.dev/capabilities/corp", "enabled": False})
cat["servers"] = servers
print(json.dumps({"policy": pol}))
PY
)
call "851-disable-unused-release-and-corp-servers" PUT /api/capability-policy "$disable_unused"
call "851-add-disabled-to-repo-ci" PUT /api/repos/acme/widgets/ci-capabilities \
  '{"ciCapabilities":["https://grain.dev/capabilities/github#sandbox-repo={}","https://grain.dev/capabilities/release#image-signing={}"]}'
call "851-add-disabled-to-deployment-defaults" PUT /api/settings \
  '{"pollInterval":"10s","githubHost":"github.com","defaultCapabilities":["api:tasks:read","https://grain.dev/capabilities/github#sandbox-repo={}","https://grain.dev/capabilities/corp#research={}"]}'

# Restore original policy so state-repo export is identical to prepared!
orig_policy=$(python3 - <<'PY'
import json
cur = json.load(open("/tmp/current-policy.json"))
pol = cur.get("policy", cur)
print(json.dumps({"policy": pol}))
PY
)
call "restore-policy" PUT /api/capability-policy "$orig_policy"

echo "=== D. .grain/ declarations check with legacy console and github-sandbox ==="
decl_dir=$(mktemp -d)
git -C "$decl_dir" init -q --initial-branch=main
mkdir -p "$decl_dir/.grain/templates" "$decl_dir/.grain/schedules"
cat >"$decl_dir/.grain/templates/legacy-decl.yml" <<'YAML'
name: legacy-decl
title: Legacy declared template
body: Exercises retired console and github-sandbox in .grain/templates.
capabilities:
  - console
  - github-sandbox
YAML
cat >"$decl_dir/.grain/schedules/legacy-sched.yml" <<'YAML'
name: legacy-sched
template: legacy-decl
schedule: "0 6 * * *"
capabilities:
  - console
  - github-sandbox
YAML
"$gbin" declarations check -repo acme/widgets "$decl_dir" 2>&1 || true
rm -rf "$decl_dir"
