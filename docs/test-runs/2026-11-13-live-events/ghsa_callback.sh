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

# ghsa_callback.sh <sqlite|postgres> [port] [grain-binary]:
# docs/test-plans/2026-11-13-afternoon.md §2 item 1 -- real `ghsa_` approval
# callback tokens on a real daemon.
#
# Unlike items1_2.sh, nothing on the approval path is stubbed: the daemon
# runs fenced Docker container sandboxes (-container-sandboxes on the
# grain-walk network, -container-sandbox-fence require), so it admits the
# built-in host-script capability server; a task holding four
# `…/host-script#host-script` grants is dispatched; and each call is a real
# `grain host-script <name>` run inside the task's container. The
# host-script server (a `grain capability host-script` child of the daemon)
# was handed a daemon-minted `ghsa_` callback token at mint, and its
# localexec.HTTPApprover posts every approval with it:
#
#   deploy   -- approved by the operator (alice@example.com)
#   rollback -- denied by the operator
#   migrate  -- the caller gives up (timeout 1.5 on the CLI): the server's
#               approver calls /expire with its ghsa_ token
#   slow     -- 2 s deadline; the host-script server is SIGSTOPped across
#               it, a task read sweeps the approval to expired, then SIGCONT
#               lets the approver's own ghsa_ /expire arrive late (it must
#               add no second local_script.answered)
#
# Stubbed boundaries: GitHub (ghsim.go) and the agent CLI (the
# 2026-11-04-capabilities fake agy, which here only holds the run open).
#
# Needs: docker, the grain-walk network (10.241.0.0/16, gateway
# 10.241.0.1, inter-container traffic off) and the grain-sbx:walk image
# (2026-11-04-capabilities/Dockerfile.walk-sandbox with cmd/grain-sandbox
# as /usr/local/bin/grain), nft (direct or `sudo -n nft`), jq, sqlite3 or
# psql.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=common.sh
source "$HERE/common.sh"
kind=${1:-sqlite} port=${2:-4291}
GRAIN=${3:-$GRAIN}
AGY="$HERE/../2026-11-04-capabilities/fake-agy.sh"
W=/tmp/wl-ghsa-$kind
OUT=${OUT:-$HERE/out/ghsa-$kind}
mkdir -p "$OUT"
rm -f "$OUT"/*
reset_store "$kind" "$W"
mkdir -p "$W/runs"
start_ghsim "$W" $((port + 4)) "$kind" || exit 1

KIND=$kind WORK=$W PORT=$port DAEMON="http://127.0.0.1:$port"
# shellcheck disable=SC2046
WALK_DIR="$W" setsid "$GRAIN" daemon -data-dir "$W/data" -ui-addr "127.0.0.1:$port" \
	-container-sandboxes -container-sandbox-network grain-walk -container-sandbox-image grain-sbx:walk \
	-container-git-proxy-host 10.241.0.1 -container-sandbox-fence require \
	-sandbox-port-base=0 -poll-interval 1s -as alice@example.com -max-mergers 0 \
	-agent-framework antigravity -agy-path "$AGY" \
	-github-host "$GHSIM" -github-insecure-http -repo acme/widgets \
	$(store_flags "$kind" "$W") >>"$W/daemon.log" 2>&1 </dev/null &
echo $! >"$W/daemon.pid"
for _ in $(seq 120); do
	[[ -s $W/data/local-api-token ]] && curl -fs -o /dev/null -H "Authorization: Bearer $(token)" "$DAEMON/api/config" && break
	sleep 0.5
done

api PUT /api/config '{"maxWorkers":2,"defaultModel":"fake-model","agentBackends":[
  {"id":"agy-walk","name":"Walk agy","framework":"antigravity","cliPath":"'"$AGY"'","models":["fake-model"]}]}' >/dev/null
api PUT /api/agent-backends/agy-walk/credential '{"value":"walk-key-not-real"}' >/dev/null
api PUT /api/settings '{"externalAgentApi":true}' >/dev/null
api PUT /api/capability-servers "$(jq -nc '{id:"https://grain.dev/capabilities/host-script",
  config:{command:"grain",args:["capability","host-script"],builtin:"host-script",config:{scripts:[
   {name:"deploy",description:"Deploy widgets",script:"echo deployed:$*",timeout:120,approve:true},
   {name:"rollback",description:"Roll back widgets",script:"echo rolled-back",timeout:120,approve:true},
   {name:"migrate",description:"Migrate widgets",script:"echo migrated:$*",timeout:120,approve:true},
   {name:"slow",description:"Slow job",script:"echo slow",timeout:2,approve:true}]}}}')" >/dev/null
for _ in $(seq 30); do
	api GET /api/capability-servers | jq -e '.[] | select(.id|test("host-script")) | .status.state == "attached"' >/dev/null && break
	sleep 0.5
done

grant() { jq -nc --arg s "$1" '{capability:"https://grain.dev/capabilities/host-script#host-script",with:{script:$s}}'; }
task=$(api POST /api/tasks "$(jq -nc --argjson g "[$(grant deploy),$(grant rollback),$(grant migrate),$(grant slow)]" \
	'{title:"ghsa_ callback walk",body:"walk",repo:"acme/widgets",grants:$g}')" | jq -r .id)
viewer=$(api POST /api/external-agents/tokens '{"name":"widgets-viewer","scopes":["tasks:read"],"repos":["acme/widgets"],"rateLimit":100000,"maxConcurrent":100}' | jq -r .token)
head=$(api GET '/api/events?since=0&limit=1000' | jq -r '[.events[].id|tonumber] | max // 0')
echo "task=$task head-event=$head" | tee "$OUT/setup.txt"

# The fake agent only parks the run until the walk is done.
printf 'touch %s/agent.ready\nfor i in $(seq 1200); do [ -f %s/agent.done ] && break; sleep 0.5; done\n' "$W" "$W" >"$W/step.sh"

sub() { # sub <name> <token>: an SSE reader stamping each line
	(curl -sN -H "Authorization: Bearer $2" -H 'Accept: text/event-stream' \
		"$DAEMON/api/events?types=local_script.pending,local_script.answered" |
		while IFS= read -r line; do printf '%s %s\n' "$(date +%s.%N)" "$line"; done >>"$OUT/$1.sse") &
	echo $! >"$W/$1.sub"
}
unsub() { pkill -P "$(cat "$W/$1.sub")" curl 2>/dev/null; kill "$(cat "$W/$1.sub")" 2>/dev/null; true; }
sub admin "$(token)"
sub viewer "$viewer"
sleep 0.5

log() { printf '%s %s\n' "$(date +%s.%N)" "$*" | tee -a "$OUT/actions.log"; }
api POST "/api/tasks/$task/approve" '{}' >/dev/null
for _ in $(seq 180); do [[ -f $W/agent.ready ]] && break; sleep 1; done
box=$(docker ps --filter ancestor=grain-sbx:walk --format '{{.Names}}' | head -n 1)
log "run parked in container $box"
hs() { # hs <label> <cmd...>: a real `grain host-script` call in the sandbox
	local label=$1
	shift
	(docker exec -u debian "$box" bash -lc "$*; echo exit=\$?" >"$OUT/call-$label.txt" 2>&1 &)
}
pending() { # pending <script>: the pending approval's id, once posted
	for _ in $(seq 100); do
		id=$(api GET /api/local-script-approvals | jq -r --arg s "$1" '[.[] | select(.script==$s and .status=="pending")][0].id // empty')
		[[ -n $id ]] && { echo "$id"; return 0; }
		sleep 0.2
	done
	return 1
}

hs deploy "grain host-script deploy --env staging"
a=$(pending deploy) && log "deploy pending as $a"
api POST "/api/tasks/$task/local-script-approvals/$a/answer" '{"approve":true}' >/dev/null
log "deploy approved by the operator"

hs rollback "grain host-script rollback"
a=$(pending rollback) && log "rollback pending as $a"
api POST "/api/tasks/$task/local-script-approvals/$a/answer" '{"approve":false}' >/dev/null
log "rollback denied by the operator"

hs migrate "timeout 1.5 grain host-script migrate 1"
a=$(pending migrate) && log "migrate pending as $a; the caller gives up at 1.5 s"
sleep 3

srv=$(pgrep -f '[c]apability host-script' | head -n 1)
hs slow "grain host-script slow"
a=$(pending slow) && log "slow pending as $a (2 s deadline); SIGSTOP host-script server pid $srv"
kill -STOP "$srv"
sleep 3
api GET "/api/tasks/$task" >/dev/null
log "past the deadline: a task read (GET /api/tasks/$task) sweeps $a"
sleep 1
kill -CONT "$srv"
log "SIGCONT: the approver's own ghsa_ /expire arrives late"
sleep 4

touch "$W/agent.done"
sleep 2
unsub admin
unsub viewer

for f in "$OUT"/call-*.txt; do log "$(basename "$f" .txt): $(tr '\n' ' ' <"$f")"; done
api GET "/api/events?since=$head&limit=500" >"$W/events.json"
jq -r '.events[] | [.id, .type, (.approvalId // "-"), (.script // "-"), (.decision // "-"), (.actorKind // "-"), (.actor // "-"), (.actorSource // "-")] | @tsv' "$W/events.json" >"$OUT/events.tsv"
sse_tsv() { grep ' data: ' "$1" | sed 's/^[0-9.]* data: //' | jq -r '[.id, .type, (.approvalId // "-"), (.decision // "-"), (.actorKind // "-"), (.actor // "-")] | @tsv'; }
sse_tsv "$OUT/admin.sse" >"$OUT/admin.events.tsv"
sse_tsv "$OUT/viewer.sse" >"$OUT/viewer.events.tsv"
if [[ $kind == postgres ]]; then
	cols=$(sql "$kind" "$W" "SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name='integration_event' AND column_name LIKE 'actor%'")
else
	cols=$(sql "$kind" "$W" "SELECT group_concat(name, ',') FROM pragma_table_info('integration_event') WHERE name LIKE 'actor%'")
fi
sql "$kind" "$W" "SELECT id, type, $cols FROM integration_event WHERE type LIKE 'local_script.%' ORDER BY id" >"$OUT/integration_event.txt"
ids=$(api GET '/api/events?since=0&limit=1000' | jq -r '.events[].id')
{
	echo "== actions"
	cat "$OUT/actions.log"
	echo "== local_script.* on GET /api/events (id type approval script decision actorKind actor source)"
	grep local_script "$OUT/events.tsv"
	echo "== integration_event rows (id|type|$cols)"
	cat "$OUT/integration_event.txt"
	echo "== local_script.answered per approval (want 1 each):"
	grep local_script.answered "$OUT/events.tsv" | cut -f3 | sort | uniq -c
	echo "== every event id 1..max present: $(echo "$ids" | sort -n | awk 'NR!=$1 {bad=1} END {print bad ? "NO" : "yes (" NR " ids)"}')"
	echo "== admin SSE saw $(wc -l <"$OUT/admin.events.tsv"); viewer (tasks:read@acme/widgets) saw $(wc -l <"$OUT/viewer.events.tsv")"
	echo "== admin vs viewer ids (diff empty = viewer missed nothing on its repo):"
	diff <(cut -f1 "$OUT/admin.events.tsv") <(cut -f1 "$OUT/viewer.events.tsv") && echo "(none)"
} | tee "$OUT/summary.txt"
stop_daemon
stop_ghsim "$W"
docker rm -f $(docker ps -aq --filter ancestor=grain-sbx:walk) >/dev/null 2>&1 || true
