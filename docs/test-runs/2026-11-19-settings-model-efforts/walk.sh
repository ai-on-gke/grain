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

# walk.sh [sqlite|postgres]: end-to-end walkthrough for
# docs/test-plans/2026-11-19.md §6 against a real grain daemon with
# -host-sandboxes -sandbox-port-base=0 and fake agy/codex/claude wrappers.

set -euo pipefail

DB=${1:?usage: walk.sh sqlite|postgres}
HERE=$(cd "$(dirname "$0")" && pwd)
BIN=${GRAIN_BIN:-/home/debian/bin/grain}
PORT=${WALK_PORT:-4282}
WORK=${WALK_WORK:-/var/tmp/walk-model-efforts-$DB}
SHOT_DIR=${WALK_SHOT_DIR:-$WORK/shots}
PGURL=${WALK_PGURL:-postgres://walk:walk@127.0.0.1:5432/me_walk?sslmode=disable}

rm -rf "$WORK"
mkdir -p "$WORK"/{data,scratch,logs,home} "$SHOT_DIR"
export WALK_LOG_DIR="$WORK/logs"

DAEMON_PID=""
cleanup() {
  if [[ -n "$DAEMON_PID" ]]; then
    kill "$DAEMON_PID" 2>/dev/null || true
    for _ in $(seq 50); do
      kill -0 "$DAEMON_PID" 2>/dev/null || break
      sleep 0.1
    done
    kill -9 "$DAEMON_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

EXTRA_FLAGS=()
if [[ "$DB" == "postgres" ]]; then
  sudo -u postgres psql -q -c "DROP DATABASE IF EXISTS me_walk;" -c "CREATE DATABASE me_walk OWNER walk;" >/dev/null
  EXTRA_FLAGS+=(-postgres-url "$PGURL")
elif [[ "$DB" != "sqlite" ]]; then
  echo "invalid DB: $DB" >&2
  exit 2
fi

echo "== Starting real grain daemon ($DB) on 127.0.0.1:$PORT =="
HOME="$WORK/home" \
WALK_LOG_DIR="$WORK/logs" \
GCE_METADATA_HOST="127.0.0.1:9" \
  "$BIN" daemon \
  -data-dir "$WORK/data" \
  -ui-addr "127.0.0.1:$PORT" \
  -sandbox-port-base=0 \
  -host-sandboxes \
  -sandbox-dir "$WORK/scratch" \
  -sandbox-disk-floor-share 0 \
  -sandbox-disk-floor-min-mb 0 \
  -ci=false \
  -poll-interval 1s \
  -max-workers 3 \
  -max-mergers 0 \
  -as alice@example.com \
  -agy-path "$HERE/fake-agy.sh" \
  -codex-path "$HERE/fake-codex.sh" \
  -claude-path "$HERE/fake-claude.sh" \
  "${EXTRA_FLAGS[@]}" \
  >"$WORK/daemon.log" 2>&1 </dev/null &
DAEMON_PID=$!

API="http://127.0.0.1:$PORT"
for _ in $(seq 150); do
  if [[ -s "$WORK/data/local-api-token" ]] && curl -fs -o /dev/null -H "Authorization: Bearer $(tr -d '[:space:]' <"$WORK/data/local-api-token")" "$API/api/config"; then
    break
  fi
  if ! kill -0 "$DAEMON_PID" 2>/dev/null; then
    echo "daemon exited early:" >&2
    tail -n 40 "$WORK/daemon.log" >&2
    exit 1
  fi
  sleep 0.2
done

TOKEN=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
api() {
  local method=$1 path=$2 body=${3:-}
  local args=(-fsS -X "$method" -H "Authorization: Bearer $TOKEN")
  if [[ -n "$body" ]]; then
    args+=(-H "Content-Type: application/json" --data "$body")
  fi
  curl "${args[@]}" "$API$path"
}

echo "== Running Playwright UI walkthrough ($DB) =="
PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
WALK_API="$API" \
WALK_TOKEN="$TOKEN" \
WALK_SHOT_DIR="$SHOT_DIR" \
WALK_DB="$DB" \
  node "$HERE/walk_ui.mjs"

echo "== Verifying DB persistence ($DB) =="
if [[ "$DB" == "sqlite" ]]; then
  sqlite3 "$WORK/data/store/grain.db" "SELECT default_model, model_settings FROM grain_config;"
else
  psql "$PGURL" -t -A -c "SELECT default_model, model_settings FROM grain_config;"
fi

echo "== Runner argv & Codex config.toml verification ($DB) =="
: > "$WORK/logs/invocations.jsonl"
api PUT /api/deployment '{
  "defaultModels": ["gemini-3.1-pro"],
  "modelSettings": [
    {"model": "gemini-3.1-pro", "effort": "high"},
    {"model": "opus-5.5-xhigh", "effort": "low"},
    {"model": "auto", "effort": "high"},
    {"model": "gpt-5.1-codex-max", "effort": "xhigh"},
    {"model": "claude-opus-5-5", "effort": "high"}
  ]
}' >/dev/null

# File 5 standalone (noRepo) tasks pinned to each target model.
MODELS=(
  "gemini-3.1-pro"
  "opus-5.5-xhigh"
  "auto"
  "gpt-5.1-codex-max"
  "claude-opus-5-5"
)
TASK_IDS=()
for m in "${MODELS[@]}"; do
  tid=$(api POST /api/tasks "$(jq -nc --arg m "$m" '{title: ("Runner argv check: " + $m), description: "Verify model effort argv handling.", noRepo: true, approved: true, model: $m}')" | jq -r '.id')
  echo "Filed task #$tid for model=$m"
  TASK_IDS+=("$tid")
done

# Wait for all 5 tasks to finish running (reach closed/done/failed/awaiting_reply).
for _ in $(seq 120); do
  pending=0
  for tid in "${TASK_IDS[@]}"; do
    st=$(api GET "/api/tasks/$tid" | jq -r '.state')
    if [[ "$st" == "queued" || "$st" == "running" || "$st" == "proposed" ]]; then
      pending=$((pending + 1))
    fi
  done
  [[ "$pending" -eq 0 ]] && break
  sleep 0.5
done

echo "== Task outcomes ($DB) =="
for i in "${!MODELS[@]}"; do
  tid="${TASK_IDS[$i]}"
  m="${MODELS[$i]}"
  api GET "/api/tasks/$tid" | jq -c --arg m "$m" '{id, model: $m, state, outcome}'
done

echo "== Captured runner invocations ($DB) =="
jq -c '{tool, argv, effortLine}' "$WORK/logs/invocations.jsonl"
cp "$WORK/logs/invocations.jsonl" "$SHOT_DIR/invocations.jsonl"

echo "== Asserting runner effort invariants ($DB) =="
jq -se 'any(.[]; .tool == "agy" and (.argv | index("gemini-3.1-pro")) != null and ((.argv | index("--effort")) as $i | $i != null and .argv[$i+1] == "high"))' "$WORK/logs/invocations.jsonl" >/dev/null
echo "[PASS] bare Antigravity (gemini-3.1-pro) received --effort high"

jq -se 'any(.[]; .tool == "agy" and (.argv | index("opus-5.5-xhigh")) != null and (.argv | index("--effort")) == null)' "$WORK/logs/invocations.jsonl" >/dev/null
echo "[PASS] named-effort Antigravity (opus-5.5-xhigh) omitted --effort despite stale stored effort"

jq -se 'any(.[]; .tool == "agy" and (.argv | index("auto")) != null and (.argv | index("--effort")) == null)' "$WORK/logs/invocations.jsonl" >/dev/null
echo "[PASS] auto omitted --effort despite stale stored effort"

jq -se 'any(.[]; .tool == "codex" and (.argv | index("gpt-5.1-codex-max")) != null and .effortLine == "model_reasoning_effort = \"xhigh\"")' "$WORK/logs/invocations.jsonl" >/dev/null
echo "[PASS] Codex (gpt-5.1-codex-max) received model_reasoning_effort = \"xhigh\" in config.toml"

jq -se 'any(.[]; .tool == "claude" and (.argv | index("claude-opus-5-5")) != null and (.argv | index("--effort")) == null)' "$WORK/logs/invocations.jsonl" >/dev/null
echo "[PASS] Claude (claude-opus-5-5) omitted --effort despite stale stored effort"

echo "== Walkthrough ($DB) complete =="
