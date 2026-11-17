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

# walk-repairs.sh NAME DRIVER [DSN] [CIMODE]: the repair half of the
# walkthrough on one store. A red `Tests` push to main and a red Stage 1
# (Nightly on an rc tag) reach a real daemon by signed webhook; the
# repairs' grants are read over the API; each repair's run then reads CI
# with the real in-sandbox CLI through the daemon's git proxy (runproxy);
# the base repair's grant is deleted to make a pre-#1492 row, and the next
# red run is appended to it after a restart.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
WT=${WT:-/home/debian/wt}
name=$1 driver=$2 dsn=${3:-} mode=${4:-github}
"$here/start.sh" "$name" "$driver" "$dsn"
T=$(cat "$WT/$name/local-api-token")
api() { curl -fsS -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$@"; }
sim() { curl -fsS -d "$1" http://127.0.0.1:4230/_sim/runs >/dev/null; }
hook() { python3 "$here/send-webhook.py" 127.0.0.1:4233 s3cret acme/widgets "$@" >/dev/null; }
waitlog() { for _ in $(seq 40); do grep -q "$1" "$WT/$name.log" && return 0; sleep 1; done; echo "timed out waiting for: $1"; return 1; }
grants() { api "http://127.0.0.1:4232/api/tasks/$1" | jq -c '{id,title,capabilities}'; }
stop() { for p in $(pgrep -f '^/home/debian/bin/grain daemon' || true); do kill "$p"; done; sleep 3; }
asrun() { "$here/asrun.sh" "$WT/$name" "$1" "$driver" "$dsn"; }

api -X PUT -d "{\"ciMode\":\"$mode\"}" http://127.0.0.1:4232/api/repos/acme/widgets/ci-mode | jq -c '{repo,ciMode}'
api -X POST -d '{"title":"Stage 1 (nightly on rc tags)","description":"Qualify a release candidate.","repo":"acme/widgets","base":"main","enabled":true,"capabilities":[],"recurrence":{"kind":"github_workflow_run","githubWorkflowRun":{"workflow":"nightly.yml","branches":["v*-rc.*"]}}}' \
  http://127.0.0.1:4232/api/schedules | jq -c '{id,title}'

echo "== red Tests push on main (run 9002)"
sim '{"ID":9002,"Name":"Tests","Path":".github/workflows/tests.yml","HeadBranch":"main","Event":"push","Jobs":[{"Name":"go","Conclusion":"failure","Log":"--- FAIL: TestWidgetCount"},{"Name":"lint","Conclusion":"success","Log":"ok"}]}'
hook Tests main 9002 failure go=failure lint=success
base=""
if [ "$mode" = github ]; then
  waitlog "run 9002 on acme/widgets is red; filed repair"
  base=$(grep -o "run 9002 on acme/widgets is red; filed repair [0-9]*" "$WT/$name.log" | awk '{print $NF}')
  grants "$base"
else
  sleep 10
  grep "run 9002" "$WT/$name.log" || echo "(no base-branch repair: grain CI, not GitHub Actions, judges this repository's main)"
fi

echo "== red Stage 1 on v1.2.0-rc.1 (run 9101)"
sim '{"ID":9101,"Name":"Nightly","Path":".github/workflows/nightly.yml","HeadBranch":"v1.2.0-rc.1","Event":"push","Jobs":[{"Name":"stage-1","Conclusion":"failure","Log":"--- FAIL: TestUpgradeFromLatest"}]}'
hook Nightly v1.2.0-rc.1 9101 failure stage-1=failure
waitlog "Stage 1 run 9101 on acme/widgets is red; filed repair"
stage1=$(grep -o "Stage 1 run 9101 on acme/widgets is red; filed repair [0-9]*" "$WT/$name.log" | awk '{print $NF}')
grants "$stage1"
stop

for id in $base $stage1; do
  printf "%s\n" "ci log 9101 --job stage-1" "read 'ci://acme/widgets/9101/stage-1/go test ._....log'" "ci list --repo acme/other" | asrun "$id"
done
[ -n "$base" ] || exit 0

echo "== repair $base as a pre-#1492 row: its grant deleted"
if [ "$driver" = postgres ]; then
  psql "$dsn" -qc "DELETE FROM task_grant WHERE task_id = '$base'"
else
  sqlite3 "$WT/$name/store/grain.db" "DELETE FROM task_grant WHERE task_id = '$base'"
fi
printf "%s\n" "ci log 9002 --job go" | asrun "$base"
KEEP=1 "$here/start.sh" "$name" "$driver" "$dsn"
grants "$base"
echo "== another red Tests push on main (run 9003)"
sim '{"ID":9003,"Name":"Tests","Path":".github/workflows/tests.yml","HeadBranch":"main","Event":"push","Jobs":[{"Name":"go","Conclusion":"failure","Log":"--- FAIL: TestWidgetCount"}]}'
hook Tests main 9003 failure go=failure
waitlog "run 9003 on acme/widgets is red; appended it to the open repair $base"
grep "run 9003" "$WT/$name.log"
grants "$base"
stop
printf "%s\n" "ci log 9003 --job go" | asrun "$base"
