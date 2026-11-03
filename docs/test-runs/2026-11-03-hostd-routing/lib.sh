#!/usr/bin/env bash
# Helpers for the walkthrough scripts: file a task with a hostd pin,
# watch its queue reason, and dump what the store recorded about it.
# Source after env.sh.

# file TITLE HOSTD DESCRIPTION -> prints the task id (or the refusal).
file() {
  local body
  body=$(jq -nc --arg t "$1" --arg h "$2" --arg d "$3" \
    '{title:$t, description:$d, noRepo:true, agentFramework:"antigravity", agentBackendId:"agy-ambient", approved:true} + (if $h == "" then {} else {hostd:$h} end)')
  api POST /api/tasks "$body"
}

task_id() { jq -r '.id // .task.id // empty'; }

# reason ID -> the task's state and queue reason, as the API has them.
reason() {
  api GET "/api/tasks/$1" | jq -c '{id, state, hostd, queueReason: (.queueReason // .waitingReason // .blockedReason // null), attempts: [.attempts[]? | {outcome, hostd}]}'
}

# watch ID SECONDS -> one reason line a second, with a timestamp.
watch() {
  local end=$((SECONDS + $2))
  while [ $SECONDS -lt "$end" ]; do
    printf '%s ' "$(date -u +%T)"; reason "$1"; sleep 1
  done
}

# records ID -> task_hostd, task_run_hostd and the capability audit's hostd.
records() {
  db "SELECT * FROM task_hostd WHERE task_id='$1';"
  db "SELECT * FROM task_run_hostd WHERE task_id='$1';"
  db "SELECT a.run_id, a.capability, a.hostd FROM task_run_capability_audit a JOIN task_run_hostd r USING (run_id) WHERE r.task_id='$1';" 2>&1
}
