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

# ci-run.sh <event>: start a grain CI run of acme/widgets at main on the
# walk daemon, wait for it to finish, and print its record (warnings,
# each job's state and the end of its log).
T=$(cat /home/debian/rd/d/local-api-token)
api=http://127.0.0.1:7830
commit=$(git -C /tmp/wsrc rev-parse HEAD)
id=$(curl -sS -H "Authorization: Bearer $T" -H 'Content-Type: application/json' -XPOST $api/api/ci/runs \
  -d "{\"repo\":\"acme/widgets\",\"ref\":\"main\",\"commit\":\"$commit\",\"event\":\"$1\",\"recheck\":true}" |
  python3 -c 'import json,sys; print(json.load(sys.stdin).get("id",""))')
echo "run $id ($1)"
for _ in $(seq 60); do
  state=$(curl -sS -H "Authorization: Bearer $T" $api/api/ci/runs/$id | python3 -c 'import json,sys; print(json.load(sys.stdin).get("state",""))')
  case $state in passed|failed|error|cancelled|success|failure) break;; esac
  sleep 3
done
curl -sS -H "Authorization: Bearer $T" $api/api/ci/runs/$id | python3 -c '
import json, sys
r = json.load(sys.stdin)
print("state:", r.get("state"))
for j in r.get("jobs") or []:
    print("job", j.get("name"), j.get("state"), "warning:", j.get("warning") or "-")
    for s in j.get("steps") or []:
        out = (s.get("output") or s.get("log") or "")
        print("  step", s.get("name"), s.get("state"))
        print("   |", out[-1500:].replace("\n", "\n   | "))
'
