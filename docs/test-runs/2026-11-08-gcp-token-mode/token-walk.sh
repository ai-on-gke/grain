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

# token-walk.sh <step> [set]: files a task holding
# gcp#gcp-key={permission_set: <set>} (default ext-deploy) as the person, approves it,
# lets fake-agy run <step> (step-gcp-token.sh, step-propose.sh) inside
# the run, waits for the run to end, then -- item 3 -- exchanges the
# token the run saved, from the host, at the gcp server's listener, and
# prints the attempt's capability audit and the gcp server's audit lines.
set -u
W=/home/debian/walk
T=$(cat $W/rd/d/local-api-token)
api=http://127.0.0.1:7830
step=${1:?step script}
cp "$step" $W/step.sh
start=$(date -u '+%Y/%m/%d %T')
set=${2:-ext-deploy}
echo "$set" > $W/propose-set
touch $W/propose-armed
grant='https://grain.dev/capabilities/gcp#gcp-key={"permission_set":"'$set'"}'
body=$(python3 -c 'import json,sys; print(json.dumps({"title":"gcp token-mode probe","description":"walk: gcp token mode","repo":"acme/widgets","agentBackendId":"ag-a","autoApproveProposals":True,"capabilities":[sys.argv[1]]}))' "$grant")
id=$(curl -s -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' $api/api/tasks --data "$body" |
  python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("id") or d)')
curl -s -X POST -H "Authorization: Bearer $T" $api/api/tasks/$id/approve -o /dev/null
echo "== task $id filed by the person with $grant and approved at $(date -u +%T)"
sleep 5
for _ in $(seq 150); do
  st=$(curl -s -H "Authorization: Bearer $T" $api/api/tasks/$id | python3 -c 'import json,sys; d=json.load(sys.stdin); a=(d.get("attempts") or [{}])[-1]; print(d.get("state"), a.get("outcome") or "", a.get("endedAt") or "")')
  [ -z "$(docker ps -q --filter name=grain-sandbox-$id-)" ] && case "$st" in queued*|running*) ;; *) break;; esac
  sleep 2
done
echo "== $(date -u +%T) task $id: $st"
sleep 3
echo "== $(date -u +%T) the run's container is gone: $(docker ps -a --filter name=grain-sandbox-$id- --format '{{.Names}} {{.Status}}' | head -1)"
for f in $W/saved/*.url; do
  [ -f "$f" ] || continue
  a=$(basename "$f" .url); url=$(cat "$f")
  hosturl=$(echo "$url" | sed 's|127.0.0.1:[0-9]*|127.0.0.1:4252|')
  echo "== after release, attempt $a: the saved per-attempt token at $hosturl (from the host)"
  curl -sS -m 10 -w ' HTTP %{http_code}\n' -X POST "$hosturl" --data-urlencode "subject_token=$(cat $W/saved/$a.token)"
  rm -f "$f" "$W/saved/$a.token"
done
echo "== attempt 1's capability audit"
curl -s -H "Authorization: Bearer $T" $api/api/tasks/$id/attempts/1/capability-audit | python3 -m json.tool | sed -E 's/("(token|secret)[^"]*": *")[^"]{6}[^"]*"/\1<cut>"/I' | head -60
echo "== the gcp server's audit lines since $start"
awk -v s="$start" '$0 >= s' $W/daemon.log | grep -E "gcp: (audit|-fake-cloud)|capabilities/gcp" | tail -30
echo "== proposals task $id filed"
curl -s -H "Authorization: Bearer $T" "$api/api/tasks?limit=50" | python3 -c '
import json,sys
d=json.load(sys.stdin); items=d if isinstance(d,list) else d.get("tasks",[])
for t in items:
    if t.get("id") in (None,): continue
    if t.get("title","").startswith("walk:") and t.get("state") != "closed":
        print(json.dumps({k:t.get(k) for k in ("id","title","state","approved","approvedBy","capabilities","reason")}))
' "$id"
echo "$id" > $W/last-task
