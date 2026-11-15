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

# deny.sh NAME [driver dsn]: a fresh daemon whose capability policy denies
# api#ci-read, a red `main` run, and the repair that files.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
WT=${WT:-/home/debian/wt}
"$here/start.sh" "$@"
T=$(cat "$WT/$1/local-api-token")
api() { curl -fsS -H "Authorization: Bearer $T" -H 'Content-Type: application/json' "$@"; }
api -X PUT -d '{"ciMode":"github"}' http://127.0.0.1:4232/api/repos/acme/widgets/ci-mode >/dev/null
api -X PUT -d '{"policy":{"rules":[{"capability":"https://grain.dev/capabilities/api#ci-read","deny":true}]}}' \
  http://127.0.0.1:4232/api/capability-policy | head -c 300; echo
curl -fsS -d '{"ID":9201,"Name":"Tests","Path":".github/workflows/tests.yml","HeadBranch":"main","Event":"push","Jobs":[{"Name":"go","Conclusion":"failure","Log":"--- FAIL: TestWidgetCount"}]}' \
  http://127.0.0.1:4230/_sim/runs >/dev/null
python3 "$here/send-webhook.py" 127.0.0.1:4233 s3cret acme/widgets Tests main 9201 failure go=failure
for _ in $(seq 30); do grep -q "filed repair" "$WT/$1.log" && break; sleep 1; done
id=$(grep -o "filed repair [0-9]*" "$WT/$1.log" | awk '{print $3}')
api "http://127.0.0.1:4232/api/tasks/$id" | jq '{id,title,capabilities,description}'
