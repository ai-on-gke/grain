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

# items1_2.sh <sqlite|postgres> [port]: §3 items 1 and 2 on a real daemon.
#
# Three SSE subscribers on GET /api/events (curl -N, since=now):
#   admin   -- the daemon's local token, unrestricted
#   viewer  -- an external-agent token holding tasks:read@acme/widgets only
#   flaky   -- the local token again, killed mid-sequence and reconnected with
#              Last-Event-ID set to the last id it printed
# then drives the notification and host-script sequences through the API and
# prints, per subscriber, the events it saw (arrival time, id, type, fields).
# A burst of 40 concurrent raises at the end checks ids arrive gap-free.
#
# Stubbed boundary: the host-script capability server needs a host with a
# configured scripts directory, so the task's host-script grant is written
# straight into task_grant, and the approval calls a run's mcpserver makes
# (POST .../local-script-approvals, /expire) are made with curl.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=common.sh
source "$HERE/common.sh"
kind=${1:-sqlite} port=${2:-4290}
W=/tmp/wl-items12-$kind
OUT=${OUT:-$HERE/out/items1_2-$kind}
mkdir -p "$OUT"
reset_store "$kind" "$W"
start_ghsim "$W" $((port + 4)) "$kind" || exit 1
start_daemon "$kind" "$W" "$port" || exit 1
setup_agent
api PUT /api/settings '{"externalAgentApi":true}' >/dev/null

task=$(api POST /api/tasks '{"title":"live events walk","body":"walk","repo":"acme/widgets"}' | tee "$W/task.json" | jq -r .id)
[[ $task == null ]] && { echo "filing the task failed: $(cat "$W/task.json")" >&2; exit 1; }
sql "$kind" "$W" "INSERT INTO task_grant (task_id,capability,via) VALUES ('$task','https://grain.dev/capabilities/host-script#host-script','grant')"
viewer=$(api POST /api/external-agents/tokens '{"name":"widgets-viewer","scopes":["tasks:read"],"repos":["acme/widgets"],"rateLimit":100000,"maxConcurrent":100}' | jq -r .token)
echo "task=$task viewer-token=${viewer:0:12}…" | tee "$OUT/setup.txt"

# sub <name> <token> [last-event-id]: an SSE reader stamping each line.
sub() {
	local name=$1 tok=$2 last=${3:-}
	(curl -sN -H "Authorization: Bearer $tok" -H 'Accept: text/event-stream' \
		${last:+-H "Last-Event-ID: $last"} "$DAEMON/api/events" |
		while IFS= read -r line; do printf '%s %s\n' "$(date +%s.%N)" "$line"; done >>"$OUT/$name.sse") &
	echo $! >"$W/$name.sub"
	sleep 0.5
}
unsub() { pkill -P "$(cat "$W/$1.sub")" curl 2>/dev/null; kill "$(cat "$W/$1.sub")" 2>/dev/null; true; }
rm -f "$OUT"/*.sse "$OUT/actions.log"
sub admin "$(token)"
sub viewer "$viewer"
sub flaky "$(token)"

act() { # act <label> <METHOD> <path> [json]: one action, timestamped
	printf '%s %s\n' "$(date +%s.%N)" "$1" >>"$OUT/actions.log"
	api "$2" "$3" "${4:-}" >"$W/last.json"
	jq -e .error "$W/last.json" >/dev/null 2>&1 && echo "  -> error: $(jq -r .error "$W/last.json")" >>"$OUT/actions.log"
	sleep 0.3
}
deadline() { date -u -d "+$1 sec" +%FT%TZ; }

act "raise N1 warning (task $task)" POST /api/notifications '{"subject":"walk.widgets","title":"widgets red","severity":"warning","task":"'"$task"'"}'
n1=$(jq -r .notification.id "$W/last.json")
act "raise N1 again, same severity (no event expected)" POST /api/notifications '{"subject":"walk.widgets","title":"widgets red","severity":"warning","task":"'"$task"'"}'
act "raise N1 at critical (re-raise)" POST /api/notifications '{"subject":"walk.widgets","title":"widgets very red","severity":"critical","task":"'"$task"'"}'
act "ack N1" POST "/api/notifications/$n1/ack" '{}'
# Drop the flaky stream here and reconnect it after three more events.
last=$(grep -o ' id: [0-9]*' "$OUT/flaky.sse" | tail -n 1 | awk '{print $2}')
unsub flaky
echo "flaky dropped after id $last" >>"$OUT/actions.log"
act "ack N1 again (no event expected)" POST "/api/notifications/$n1/ack" '{}'
act "clear N1" POST "/api/notifications/$n1/clear" '{}'
act "raise N2 repo acme/secret (viewer must not see)" POST /api/notifications '{"subject":"walk.secret","title":"secret red","repo":"acme/secret"}'
act "raise N3 deployment-wide (viewer must not see)" POST /api/notifications '{"subject":"walk.hosts","title":"hosts down","severity":"critical"}'
sub flaky "$(token)" "$last"
echo "flaky reconnected with Last-Event-ID: $last" >>"$OUT/actions.log"

L="/api/tasks/$task/local-script-approvals"
act "host-script A1 requested" POST "$L" '{"tool":"host_script","script":"deploy.sh","args":["--env","staging"],"deadline":"'"$(deadline 120)"'"}'
a1=$(jq -r .id "$W/last.json")
act "A1 approved" POST "$L/$a1/answer" '{"approve":true}'
act "host-script A2 requested" POST "$L" '{"tool":"host_script","script":"rollback.sh","deadline":"'"$(deadline 120)"'"}'
a2=$(jq -r .id "$W/last.json")
act "A2 denied" POST "$L/$a2/answer" '{"approve":false}'
act "host-script A3 requested" POST "$L" '{"tool":"host_script","script":"migrate.sh","args":["1"],"deadline":"'"$(deadline 120)"'"}'
a3=$(jq -r .id "$W/last.json")
act "A3 expired by its caller (/expire)" POST "$L/$a3/expire" '{}'
act "host-script A4 requested, 2 s deadline" POST "$L" '{"tool":"host_script","script":"slow.sh","deadline":"'"$(deadline 2)"'"}'
a4=$(jq -r .id "$W/last.json")
sleep 3
act "A4 past its deadline: a task-page read (GET /api/tasks/$task)" GET "/api/tasks/$task"
act "A4 caller gives up (/expire)" POST "$L/$a4/expire" '{}'
echo "A4 as the caller sees it: $(jq -c '{id,status}' "$W/last.json")" >>"$OUT/actions.log"

printf '%s burst: 40 concurrent raises\n' "$(date +%s.%N)" >>"$OUT/actions.log"
for i in $(seq 40); do api POST /api/notifications '{"subject":"walk.burst.'"$i"'","title":"burst '"$i"'","task":"'"$task"'"}' >/dev/null & done
wait_bg() { for p in $(jobs -p); do :; done; }
sleep 4
for s in admin viewer flaky; do unsub "$s"; done

summ() { # summ <file>: id type key-fields, one per event
	grep ' data: ' "$1" | sed 's/^[0-9.]* data: //' |
		jq -r '[.id, .type, (.notificationId // ""), (.severity // ""), (.approvalId // ""), (.decision // ""), (.repo // "-"), (.actor // "")] | @tsv'
}
for s in admin viewer flaky; do summ "$OUT/$s.sse" >"$OUT/$s.events.tsv"; done
api GET '/api/events?since=0&limit=500' | jq -r '.events[] | [.id, .type, (.repo // "-")] | @tsv' >"$OUT/store.tsv"
{
	echo "== actions"; cat "$OUT/actions.log"
	for s in admin viewer flaky; do
		echo "== $s ($(wc -l <"$OUT/$s.events.tsv") events, $(cut -f1 "$OUT/$s.events.tsv" | sort | uniq -d | wc -l) duplicate ids)"
		grep -v 'walk.burst\|burst' "$OUT/$s.events.tsv" | awk -F'\t' '$2 !~ /^notification.raised$/ || $3 < 4 || 1' | head -n 30
	done
	echo "== admin ids strictly increasing: $(cut -f1 "$OUT/admin.events.tsv" | awk 'NR>1 && $1<=p {bad=1} {p=$1} END {print bad ? "NO" : "yes"}')"
	echo "== burst: store has $(grep -c notification.raised "$OUT/store.tsv") raised; admin saw $(grep -c notification.raised "$OUT/admin.events.tsv"); flaky saw $(grep -c notification.raised "$OUT/flaky.events.tsv")"
	echo "== admin vs store ids after the stream opened (diff empty = nothing lost):"
	first=$(head -n 1 "$OUT/admin.events.tsv" | cut -f1)
	diff <(awk -F'\t' -v f="$first" '$1>=f {print $1}' "$OUT/store.tsv") <(cut -f1 "$OUT/admin.events.tsv") && echo "(none)"
	echo "== flaky vs admin ids (diff empty = reconnect lost and duplicated nothing):"
	diff <(cut -f1 "$OUT/admin.events.tsv") <(cut -f1 "$OUT/flaky.events.tsv") && echo "(none)"
} | tee "$OUT/summary.txt"
stop_daemon
stop_ghsim "$W"
