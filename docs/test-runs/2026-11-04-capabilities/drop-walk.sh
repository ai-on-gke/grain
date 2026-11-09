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

# drop-walk.sh: §8 item 4 on the walk daemon, inside the fake agent's
# 100-second model-turn cutoff. Files a task holding the droppable server's
# grant (capsrv.sh), waits for its run to hold the grant (step-server-drop.sh
# touches ~/walk/ready), holds the server down and kills it, reads the alert
# and `grain health`, then lets the run make two grain MCP tool calls
# (mcp-call.py) and prints what came back.
set -u
T=$(cat /home/debian/rd/d/local-api-token)
api=http://127.0.0.1:7830
w=/home/debian/walk
rm -f $w/go $w/ready $w/capsrv.down
: > $w/runs/task-unknown.log
id=$(curl -s -X POST -H "Authorization: Bearer $T" -H 'Content-Type: application/json' $api/api/tasks \
  --data '{"title":"server drop probe","description":"walk item 4","repo":"acme/widgets","agentBackendId":"ag-b","capabilities":["https://walk.example/caps/cm#config-map={\"config_maps\":[\"bootstrap\"]}"]}' |
  python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
curl -s -X POST -H "Authorization: Bearer $T" $api/api/tasks/$id/approve -o /dev/null
echo "task $id filed and approved at $(date -u +%T)"
for _ in $(seq 60); do [ -f $w/ready ] && break; sleep 1; done
echo "run holding the grant at $(date -u +%T)"
d=$(pgrep -f '^/home/debian/bin/grain daemon -data-dir /home/debian/rd/d' | head -1)
srv=$(ps --ppid "$d" -o pid=,lstart=,args= --sort=start_time | grep 'capability config-map' | tail -1 | awk '{print $1}')
touch $w/capsrv.down
kill -9 "$srv"
echo "server pid $srv killed and held down at $(date -u +%T)"
sleep 4
echo "== alert"
curl -s -H "Authorization: Bearer $T" $api/api/notifications | python3 -c '
import json,sys
for n in json.load(sys.stdin):
    if n["subject"].startswith("capability.server-down"): print(n["subject"], "|", n["title"], "|", n["body"].splitlines()[0])'
echo "== grain health"
GRAIN_SERVER=$api GRAIN_TOKEN=$T /home/debian/bin/grain-cli health
touch $w/go
sleep 12
echo "== the run's tool calls"
sed -n '/go: calling/,/^== .* done/p' $w/runs/task-unknown.log
echo "== task $id comments by capability-change"
curl -s -H "Authorization: Bearer $T" $api/api/tasks/$id/comments | python3 -c '
import json,sys
d=json.load(sys.stdin); items=d if isinstance(d,list) else d.get("comments",[])
for c in items:
    if "capability" in json.dumps(c.get("author","")).lower() or "capability server" in c.get("body",""): print(c.get("createdAt"), json.dumps(c.get("author")), "\n", c.get("body"))'
rm -f $w/capsrv.down
curl -s -X POST -H "Authorization: Bearer $T" $api/api/tasks/$id/close -o /dev/null
