#!/usr/bin/env bash
# Items 2, 3, 4 and 6 of §2 against env.sh's daemon and hostds: label
# selectors, a disconnected pin waiting, capacity holding a second task,
# a daemon restart under a run on ws-a, and a per-hostd usage-limit pause.
# Prints each task's pin, its queue reason (activity) over time, and its
# task_hostd / task_run_hostd / audit rows.
set -uo pipefail
cd "$(dirname "$0")"
. ./env.sh
. ./lib.sh
set +e

# state ID -> one line: state, activity, attempts' hostds and outcomes.
state() {
  api GET "/api/tasks/$1" | jq -c '{id, state, pin: .hostd, activity, attempts: [.attempts[]? | {outcome, hostd: .hostd.hostd}]}'
}
# until_started ID SECONDS -> prints a line each time the state changes.
follow() {
  local last="" end=$((SECONDS + $2)) s
  while [ $SECONDS -lt "$end" ]; do
    s=$(state "$1")
    [ "$s" != "$last" ] && printf '%s %s\n' "$(date -u +%T)" "$s" && last=$s
    echo "$s" | grep -q "\"outcome\":\"${3:-succeeded}\"" && return 0
    sleep 0.5
  done
}
report() { echo "$ROOT/reports/$1.json"; }
home_of() { jq -r '.home' "$(report "$1")" 2>/dev/null | sed "s#$ROOT/##"; }

if [ "${ONLY:-}" != 6 ]; then
echo "=== item 2: labels"
t2a=$(file "item2: gpu=yes" "gpu=yes" "report=$(report i2a) mode=plain" | task_id)
follow "$t2a" 40
echo "ran in: $(home_of i2a)"
printf 'gpu=no (matches none) -> '; file "item2: gpu=no" "gpu=no" "x" | jq -c '.' | cut -c1-300
printf 'ws-nope (unknown name) -> '; file "item2: unknown" "ws-nope" "x" | jq -c '.' | cut -c1-300
printf 'PUT /api/tasks/%s/hostd gpu=no -> ' "$t2a"; api PUT "/api/tasks/$t2a/hostd" '{"hostd":"gpu=no"}' | cut -c1-300; echo

echo "=== item 3a: a pin on a disconnected hostd waits, and starts on reconnect"
hostd_down ws-b
sleep 2
t3=$(file "item3: pinned to ws-b while down" ws-b "report=$(report i3) mode=plain" | task_id)
follow "$t3" 12
echo "the board's view of it (GET /api/tasks?state=queued):"
api GET "/api/tasks?state=queued" | jq -c --arg id "$t3" '[.[]? // .tasks[]? | select(.id == $id) | {id, activity, reason: .reason.reason}]'
echo "$(date -u +%T) restarting ws-b"
hostd_up ws-b
follow "$t3" 40
echo "ran in: $(home_of i3)"

echo "=== item 3b: capacity: 1 on ws-a holds a second task"
capacity ws-a 1
c1=$(file "item3b: first on ws-a" ws-a "report=$(report c1) mode=steady ticks=16" | task_id)
sleep 1
c2=$(file "item3b: second on ws-a" ws-a "report=$(report c2) mode=plain" | task_id)
( follow "$c1" 60 > "$ROOT/c1.follow" ) &
watcher=$!
follow "$c2" 60
wait "$watcher"
cat "$ROOT/c1.follow"

fi
echo "=== item 6: a usage limit on ws-a's ambient login pauses ws-a only"
l1=$(file "item6: limit on ws-a" ws-a "report=$(report l1) mode=limit" | task_id)
follow "$l1" 30 paused
db "SELECT scope_key, connection_id, model, until_at FROM usage_limit_pause;"
echo "Settings -> Models pauses (API):"
api GET /api/settings | jq -c '[.. | objects | select(has("connection") or has("connectionId")) | select((.connection // .connectionId | tostring) | test("agy"))] | .[0:4]' | cut -c1-600
la=$(file "item6: next on ws-a" ws-a "report=$(report l2) mode=plain" | task_id)
lb=$(file "item6: on ws-b" ws-b "report=$(report l3) mode=plain" | task_id)
ll=$(file "item6: on local" "" "report=$(report l4) mode=plain" | task_id)
follow "$lb" 40
follow "$ll" 40
follow "$la" 8
echo "ws-b ran in: $(home_of l3); local ran in: $(home_of l4)"
api DELETE /api/pause '{"connection":"agy-ambient@hostd/ws-a","model":"fake-agy-model"}' >/dev/null

for id in ${t2a:-} ${t3:-} ${c1:-} ${c2:-} "$l1" "$la" "$lb" "$ll"; do
  echo "--- records for task $id"
  records "$id"
done
echo "$la" >"$ROOT/paused-task.id"
