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
# bare repositories acme/{widgets,lib,other} and a real HEAD `grain daemon
# -host-sandboxes` dispatching fake-agy.sh, and defines call() for its API.
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
HEAD_BIN="${HEAD_BIN:-/home/debian/bin/grain}"
OUT="${OUT_DIR:-$HERE/out}"
mkdir -p "$OUT"

cleanup() { for pf in "$WORK"/*.pid; do [[ -f "$pf" ]] && kill "$(cat "$pf")" 2>/dev/null || true; done; }
trap cleanup EXIT

setup_world() {
  rm -rf "$WORK"; mkdir -p "$WORK/repos" "$WORK/data" "$WORK/home" "$WORK/scratch" "$WORK/walk/runs"
  echo "$WORK/walk" >/var/tmp/capgrants/current-walk-dir
  (cd "$ROOT" && go build -o "$WORK/ghsim" ./docs/test-runs/2026-11-10-unified-grants/ghsim.go)
  for r in widgets lib other; do
    git init -q --bare -b main "$WORK/repos/acme/$r.git"
    local t; t=$(mktemp -d); git -C "$t" init -q -b main; echo "# acme/$r" >"$t/README.md"
    if [[ -n "${SEED_DIR:-}" && -d "$SEED_DIR/$r" ]]; then cp -r "$SEED_DIR/$r/." "$t/"; mv "$t/dot-grain" "$t/.grain"; fi
    git -C "$t" add . && git -C "$t" -c user.name=s -c user.email=s@example.com commit -qm seed
    git -C "$t" push -q "$WORK/repos/acme/$r.git" main; rm -rf "$t"
  done
  "$WORK/ghsim" -addr "$GHSIM_ADDR" -root "$WORK/repos" -repo acme/widgets=main -repo acme/lib=main -repo acme/other=main >"$WORK/ghsim.log" 2>&1 &
  echo $! >"$WORK/ghsim.pid"
  export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0 WALK_DIR="$WORK/walk"
  unset GRAIN_SERVER GRAIN_TOKEN GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN
}

start() {
  # shellcheck disable=SC2086
  "$HEAD_BIN" daemon -data-dir "$WORK/data" -ui-addr "127.0.0.1:$PORT" -sandbox-port-base=0 \
    -host-sandboxes -sandbox-dir "$WORK/scratch" -sandbox-disk-floor-share 0 -sandbox-disk-floor-min-mb 0 \
    -poll-interval 1s -as owner@example.com -default-target-repo acme/widgets \
    -github-host "$GHSIM_ADDR" -github-insecure-http ${DAEMON_EXTRA:-} >>"$WORK/daemon.log" 2>&1 &
  echo $! >"$WORK/daemon.pid"
  for _ in $(seq 300); do
    kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null || { tail -n 30 "$WORK/daemon.log"; exit 1; }
    if [[ -s "$WORK/data/local-api-token" ]] && curl -fs -o /dev/null -H "Authorization: Bearer $(cat "$WORK/data/local-api-token")" "http://127.0.0.1:$PORT/api/config"; then return; fi
    sleep 0.2
  done
  echo "daemon did not start"; exit 1
}
stop() { kill "$(cat "$WORK/daemon.pid")"; while kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null; do sleep 0.1; done; rm -f "$WORK/daemon.pid"; }
call() {
  local a=(-sS -X "$1" -H "Authorization: Bearer $(cat "$WORK/data/local-api-token")")
  [[ -n "${3:-}" ]] && a+=(-H "Content-Type: application/json" --data "$3")
  curl "${a[@]}" "http://127.0.0.1:$PORT$2"
}
configure() {
  call PUT /api/github-tokens/default '{"value":"ghp_walkthrough_stub"}' >/dev/null
  for r in acme/widgets acme/lib acme/other; do call POST /api/repos "{\"repo\":\"$r\"}" >/dev/null; done
  call PUT /api/config "{\"agentBackends\":[{\"id\":\"ag-walk\",\"name\":\"Walk Agy\",\"framework\":\"antigravity\",\"cliPath\":\"$HERE/fake-agy.sh\",\"models\":[\"fake-model\"]}]}" >/dev/null
  call PUT /api/agent-backends/ag-walk/credential '{"value":"walk-fake-gemini-key"}' >/dev/null
}
wait_state() { # task id, jq predicate on state
  for _ in $(seq 240); do
    s=$(call GET "/api/tasks/$1" | jq -r .state)
    case "$s" in done|closed|failed|awaiting_reply|needs-attention|waiting|held) echo "$s"; return;; esac
    sleep 0.5
  done
  echo "timeout($s)"
}
