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

# Items 1 and 5: a run pinned to ws-a runs its CLI under ws-a's hostd, in
# ws-a's HOME, staged under ~/.grain/hostd-stage with the documented
# modes, and the carried MCP listener refuses Origin and foreign Host.
set -uo pipefail
cd "$(dirname "$0")" || exit
. ./env.sh
. ./lib.sh
R=$ROOT/reports/item1-$$.json
rm -f "$R"
id=$(file "item1: pinned to ws-a" ws-a "report=$R mode=steady ticks=24" | task_id)
echo "task $id pinned to ws-a"
for _ in $(seq 60); do [ -s "$R" ] && break; sleep 0.5; done
echo "--- fakeagy's report"
jq '{home, hostname, uid, pid, ppid, cwd, mcpUrl: (.mcpUrl | sub("/[0-9a-f]{16,}"; "/<secret>")), toolErrors, toolResults: [.toolResults[]? | .[0:120]]}' "$R"
pid=$(jq -r .pid "$R") ppid=$(jq -r .ppid "$R")
echo "--- ps: the CLI and its parent (ws-a's hostd is pid $(cat "$ROOT/ws-a/pid"))"
ps -o pid,ppid,user,args -p "$pid,$ppid" | cut -c1-160
echo "--- staged tree modes under ws-a's ~/.grain/hostd-stage"
find "$(hostd_home ws-a)/.grain/hostd-stage" -printf '%M %y %p\n' | sed "s#$(hostd_home ws-a)#~ws-a#" | head -40
url=$(jq -r .mcpUrl "$R")
hostport=${url#http://}; hostport=${hostport%%/*}
echo "--- carried listener $hostport"
printf 'no headers (POST initialize):  '; curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' -H 'Accept: application/json, text/event-stream' --data '{"jsonrpc":"2.0","id":9,"method":"tools/list"}' "$url"
printf 'Origin: http://evil.example:   '; curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Origin: http://evil.example' -H 'Content-Type: application/json' --data '{}' "$url"
printf 'Host: evil.example:            '; curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Host: evil.example' -H 'Content-Type: application/json' --data '{}' "$url"
printf 'Host: localhost:<port>:        '; curl -s -o /dev/null -w '%{http_code}\n' -X POST -H "Host: localhost:${hostport##*:}" -H 'Content-Type: application/json' --data '{}' "$url"
printf 'right Host, no secret:         '; curl -s -o /dev/null -w '%{http_code}\n' -X POST -H 'Content-Type: application/json' --data '{}' "http://$hostport/"
for _ in $(seq 90); do reason "$id" | grep -q '"outcome":"' && break; sleep 1; done
echo "--- outcome"
reason "$id"
api GET "/api/tasks/$id" | jq -c '.attempts[0] | {outcome, detail}'
echo "--- after the run: what is left under hostd-stage"
find "$(hostd_home ws-a)/.grain/hostd-stage" -printf '%M %y %p\n' | sed "s#$(hostd_home ws-a)#~ws-a#" | head -20
echo "--- records"
records "$id"
