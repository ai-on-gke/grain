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

# item4_ci_api_repos.sh: §6 item 4. acme/widgets runs grain CI with a job
# declaring an api grant (seed4/widgets/.grain/workflows/probe.yml). The job
# runs before acme/widgets is listed in grain_config.ci_api_repos, after a
# merged tables/grain_config.json change lists it, and after a second change
# removes it -- all on one daemon, with no restart.
set -euo pipefail
WORK=/var/tmp/capgrants/item4; GHSIM_ADDR=127.0.0.1:4298; PORT=4299
# shellcheck source=common.sh
source "$(dirname "${BASH_SOURCE[0]}")/common.sh"
SEED_DIR="$HERE/seed4" setup_world
start; configure
call PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"grain"}' >/dev/null
run_probe() {
  local id c t
  # A fresh commit each time: grain CI reuses a job's result for a commit it
  # already ran, and the question is what the *next* job holds.
  t=$(mktemp -d); git clone -q "$WORK/repos/acme/widgets.git" "$t"
  git -C "$t" -c user.name=s -c user.email=s@example.com commit -q --allow-empty -m "probe $(date +%s%N)"
  git -C "$t" push -q origin main; rm -rf "$t"
  # workflow_dispatch: api grants are withheld from push runs.
  id=$(call POST /api/ci/runs '{"repo":"acme/widgets","ref":"main","event":"workflow_dispatch"}' | jq -r '.id // empty')
  for _ in $(seq 120); do
    c=$(call GET "/api/ci/runs/$id" | jq -r '.conclusion // empty')
    [[ -n "$c" ]] && break
    sleep 0.5
  done
  echo "run $id: $c"
  sqlite3 "$WORK/data/store/grain.db" "SELECT output FROM ci_step WHERE run_id='$id'"
  call GET "/api/ci/runs/$id" | jq -c '[.jobs[]? | {name, conclusion, error, warning}]' 
}
set_ci_api_repos() { # value for tables/grain_config.json's ci_api_repos
  "$HEAD_BIN" state -data-dir "$WORK/data" sync >/dev/null 2>&1 || true
  local f="$WORK/data/state-repo/tables/grain_config.json"
  python3 - "$f" "$1" <<'PY'
import json, sys
p, v = sys.argv[1:]
d = json.load(open(p))
rows = d if isinstance(d, list) else d.get("rows", d)
def setv(r):
    r["ci_api_repos"] = v
if isinstance(rows, list):
    for r in rows: setv(r)
else:
    setv(rows)
json.dump(d, open(p, "w"), indent=2)
PY
  git -C "$WORK/data/state-repo" -c user.name=op -c user.email=op@example.com commit -qam "ci_api_repos: '$1'"
  for _ in $(seq 60); do
    [[ "$(sqlite3 "$WORK/data/store/grain.db" 'SELECT ci_api_repos FROM grain_config WHERE id=1')" == "$1" ]] && return
    sleep 0.5
  done
  echo "ci_api_repos did not apply"
}
{
  echo "## 1. not listed"; run_probe
  echo "## 2. merged change lists acme/widgets"; set_ci_api_repos "acme/widgets"
  echo "grain_config.ci_api_repos=$(sqlite3 "$WORK/data/store/grain.db" 'SELECT ci_api_repos FROM grain_config WHERE id=1')"; run_probe
  echo "## 3. merged change removes it"; set_ci_api_repos ""
  echo "grain_config.ci_api_repos=$(sqlite3 "$WORK/data/store/grain.db" 'SELECT ci_api_repos FROM grain_config WHERE id=1')"; run_probe
  echo "daemon pid unchanged: $(cat "$WORK/daemon.pid")"
} >"$OUT/item4-ci-api-repos.txt" 2>&1
cat "$OUT/item4-ci-api-repos.txt"
stop
