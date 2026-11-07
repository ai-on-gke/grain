#!/bin/sh
# fake-agy (container form): the stub framework for a real `grain daemon`
# whose sandboxes are containers -- an antigravity connection whose CLI path
# is this file, baked into the walk's sandbox image. It answers --version
# and `models`; for a run it records the sandbox's environment and home,
# fetches and runs the walk's current probe (GET $COLLECTOR/step), sends
# everything to $COLLECTOR/task-<id>, and prints agy's stream-json for a
# successful one-turn run.
for arg in "$@"; do
  case "$arg" in --version) echo 'fake agy (walk)'; exit 0;; models) exit 0;; esac
done
IFS= read -r _ || true
COLLECTOR=${COLLECTOR:-http://10.240.0.1:4260}
task=$(printf '%s' "${GRAIN_TASK_ID:-unknown}" | tr -cd 'A-Za-z0-9_-')
out=$(mktemp)
{
  echo "== $(date -u +%FT%TZ) fake agy run in $(hostname), pwd=$(pwd) HOME=$HOME"
  echo "== env (GRAIN_*, GOOGLE_*, CLOUDSDK_*, *BASE_URL; secret values cut)"
  env | grep -E '^(GRAIN_|GOOGLE_|CLOUDSDK_|[A-Z_]*BASE_URL)' | sed -E 's/((TOKEN|KEY|SECRET)[A-Z_]*=).{6}.*/\1<cut>/' | sort
  echo "== ls -la \$HOME"
  ls -la "$HOME" 2>&1
  curl -fsS -m 10 "$COLLECTOR/step" -o /tmp/step.sh && sh /tmp/step.sh 2>&1
  echo "== $(date -u +%FT%TZ) done"
} > "$out" 2>&1
curl -sS -m 10 --data-binary @"$out" "$COLLECTOR/task-$task" >/dev/null 2>&1
printf '%s\n' '{"event":"init","conversation_id":"fake-agy-walk","init":{"tools":[],"permission_mode":"always-proceed"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-walk","step_index":0,"step_type":"agent_response","state":"ACTIVE","text_delta":"walk step done"}}'
printf '%s\n' '{"event":"step_update","step_update":{"conversation_id":"fake-agy-walk","step_index":0,"step_type":"agent_response","state":"DONE"}}'
printf '%s\n' '{"event":"result","result":{"conversation_id":"fake-agy-walk","status":"SUCCESS","response":"walk step done","num_turns":1}}'
