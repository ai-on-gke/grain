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

# Starts trainsim.go and a real `grain daemon` (SQLite, -sandbox-port-base=0,
# no sandboxes) against it for walkthrough 2026-11-11 §7 items 2 and 3,
# turns on acme/widgets' own declarations so the daemon reads the release
# train's schedules from it, and writes the access code to $root/code.
#
#   bash start-daemon.sh <root> [daemon-port] [repo-root]
#
# GRAIN_RELEASE_LOOP_IDLE_AFTER (e.g. 1m) is passed through to the daemon.
set -euo pipefail
root="${1:?root}"
port="${2:-4420}"
simport=$((port + 10))
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${3:-$here/../../..}" && pwd)"
grain="${GRAIN:-/home/debian/bin/grain}"
mkdir -p "$root/data/secrets/github" "$root/sandbox"
if ! curl -fsS -o /dev/null "http://127.0.0.1:$simport/_calls" 2>/dev/null; then
  (cd "$repo_root" && go build -o "$root/trainsim" "$here/trainsim.go")
  nohup "$root/trainsim" -addr "127.0.0.1:$simport" -dir "$root/sim" -repo-root "$repo_root" >"$root/sim.log" 2>&1 </dev/null &
  echo $! >"$root/sim.pid"
  for _ in $(seq 1 40); do curl -fsS -o /dev/null "http://127.0.0.1:$simport/_calls" 2>/dev/null && break; sleep 0.25; done
fi
printf '{"*": "bot"}' >"$root/data/secrets/github/credentials.json"
printf 'sim-bot-token' >"$root/data/secrets/github/bot.token"
if [ -f "$root/daemon.pid" ]; then
  kill "$(cat "$root/daemon.pid")" 2>/dev/null || true
  for _ in $(seq 1 40); do kill -0 "$(cat "$root/daemon.pid")" 2>/dev/null || break; sleep 0.25; done
fi
nohup "$grain" daemon -data-dir "$root/data" -sandbox-dir "$root/sandbox" -sandbox-port-base=0 \
  -disable-native-sandboxes -disable-self-upgrade -ui-addr "127.0.0.1:$port" \
  -github-host "127.0.0.1:$simport" -github-insecure-http -default-target-repo acme/widgets \
  -poll-interval 2s >>"$root/daemon.log" 2>&1 </dev/null &
echo $! >"$root/daemon.pid"
for _ in $(seq 1 240); do curl -fsS -o /dev/null "http://127.0.0.1:$port/healthz" 2>/dev/null && break; sleep 0.25; done
"$grain" access-code -data-dir "$root/data" >"$root/code"
curl -fsS -X PUT -H "Authorization: Bearer $(cat "$root/code")" -H 'Content-Type: application/json' \
  -d '{"declarations":true}' "http://127.0.0.1:$port/api/repos/acme/widgets/declarations" >/dev/null &&
  echo "declarations on for acme/widgets"
echo "daemon $(cat "$root/daemon.pid") on 127.0.0.1:$port, trainsim on 127.0.0.1:$simport"
