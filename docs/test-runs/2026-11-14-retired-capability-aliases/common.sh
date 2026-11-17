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

# common.sh: sourced by the item scripts. Starts the GitHub simulator over
# bare repositories acme/{widgets,lib} and a real `grain daemon` (BIN picks
# the build: an old tag or HEAD) on SQLite or PostgreSQL (STORE=sqlite|postgres),
# and defines call() for its API.
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
OUT="${OUT_DIR:-$HERE/out}"
WORK="${WORK:-/var/tmp/retired-aliases/${STORE:-sqlite}}"
PORT="${PORT:-4391}"
GHSIM_ADDR="${GHSIM_ADDR:-127.0.0.1:4392}"
PGDSN="${PGDSN:-postgres://grain:grain@127.0.0.1:5432/grain_retired?sslmode=disable}"
mkdir -p "$OUT"

# The bare names PR #1455 stopped honouring at grant time.
BARE='["gcp-key","gcp-key:ops","claude-proxy","gemini-key","image-signing","registry-push","grain-ci","bootstrap-playbooks","github-credential:bot","read-issues","github-sandbox","self-debug","local-scripts:deploy","grain-log"]'

setup_world() {
  rm -rf "$WORK"; mkdir -p "$WORK/repos/acme" "$WORK/data" "$WORK/home" "$WORK/scratch" "$WORK/walk/runs"
  (cd "$ROOT" && go build -o "$WORK/ghsim" ./docs/test-runs/2026-11-14-retired-capability-aliases/ghsim.go)
  for r in widgets lib; do
    git init -q --bare -b main "$WORK/repos/acme/$r.git"
    local t; t=$(mktemp -d); git -C "$t" init -q -b main; echo "# acme/$r" >"$t/README.md"
    git -C "$t" add . && git -C "$t" -c user.name=s -c user.email=s@example.com commit -qm seed
    git -C "$t" push -q "$WORK/repos/acme/$r.git" main; rm -rf "$t"
  done
  if [[ "${STORE:-sqlite}" == postgres ]]; then
    sudo -u postgres psql -qc "DROP DATABASE IF EXISTS grain_retired" -c "CREATE DATABASE grain_retired OWNER grain" >/dev/null
  fi
}
start_ghsim() {
  "$WORK/ghsim" -addr "$GHSIM_ADDR" -root "$WORK/repos" -repo acme/widgets=main -repo acme/lib=main >"$WORK/ghsim.log" 2>&1 &
  echo $! >"$WORK/ghsim.pid"
}
env_world() {
  export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0 WALK_DIR="$WORK/walk"
  unset GRAIN_SERVER GRAIN_TOKEN GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN
}
start() {
  local store=()
  [[ "${STORE:-sqlite}" == postgres ]] && store=(-db-driver postgres -db-dsn "$PGDSN")
  # shellcheck disable=SC2086
  "$BIN" daemon -data-dir "$WORK/data" "${store[@]}" -ui-addr "127.0.0.1:$PORT" -sandbox-port-base=0 \
    -host-sandboxes -sandbox-dir "$WORK/scratch" -sandbox-disk-floor-share 0 -sandbox-disk-floor-min-mb 0 \
    -poll-interval 1s -as owner@example.com -default-target-repo acme/widgets \
    -github-host "$GHSIM_ADDR" -github-insecure-http ${DAEMON_EXTRA:-} >>"$WORK/daemon.log" 2>&1 &
  echo $! >"$WORK/daemon.pid"
  for _ in $(seq 600); do
    kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null || { tail -n 30 "$WORK/daemon.log"; return 1; }
    if [[ -s "$WORK/data/local-api-token" ]] && curl -fs -o /dev/null -H "Authorization: Bearer $(cat "$WORK/data/local-api-token")" "http://127.0.0.1:$PORT/api/config"; then return; fi
    sleep 0.2
  done
  echo "daemon did not start"; return 1
}
stop() { kill "$(cat "$WORK/daemon.pid")"; while kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null; do sleep 0.1; done; rm -f "$WORK/daemon.pid"; }
call() {
  local a=(-sS -X "$1" -H "Authorization: Bearer $(cat "$WORK/data/local-api-token")")
  [[ -n "${3:-}" ]] && a+=(-H "Content-Type: application/json" --data "$3")
  curl "${a[@]}" "http://127.0.0.1:$PORT$2"
}
q() { # raw SQL against the store
  if [[ "${STORE:-sqlite}" == postgres ]]; then psql -At -F'|' "$PGDSN" -c "$1"; else sqlite3 -separator "|" "$WORK/data/store/grain.db" "$1"; fi
}
