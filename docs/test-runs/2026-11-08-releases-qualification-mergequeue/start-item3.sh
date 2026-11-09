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

# start-item3.sh ROOT DRIVER [PORT]: a githubsim_server.go on PORT+10 (once
# per ROOT) and a real `grain daemon` on PORT pointed at it as a GitHub
# Enterprise host, on SQLite (DRIVER=sqlite) or a local PostgreSQL database
# (DRIVER=postgres, $PG_URL). The daemon's GitHub credential is a token file
# under -data-dir/secrets/github, the way a deployment's ladder holds one.
# Calling it again on the same ROOT restarts only the daemon, on the same
# data dir and database.
set -euo pipefail
root="${1:?root}"
driver="${2:?sqlite|postgres}"
port="${3:-4393}"
simport=$((port + 10))
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
grain="${GRAIN:-/home/debian/bin/grain}"
mkdir -p "$root/data/secrets/github" "$root/sandbox"
if ! curl -fsS -o /dev/null "http://127.0.0.1:$simport/_dispatches" 2>/dev/null; then
  (cd "$here/../../.." && go build -o "$root/githubsim_server" "$here/githubsim_server.go")
  nohup "$root/githubsim_server" -addr "127.0.0.1:$simport" -dir "$root/sim" >"$root/sim.log" 2>&1 </dev/null &
  echo $! >"$root/sim.pid"
  for _ in $(seq 1 40); do curl -fsS -o /dev/null "http://127.0.0.1:$simport/_dispatches" 2>/dev/null && break; sleep 0.25; done
fi
printf '{"*": "bot"}' >"$root/data/secrets/github/credentials.json"
printf 'sim-bot-token' >"$root/data/secrets/github/bot.token"
if [ -f "$root/daemon.pid" ]; then
  kill "$(cat "$root/daemon.pid")" 2>/dev/null || true
  for _ in $(seq 1 40); do kill -0 "$(cat "$root/daemon.pid")" 2>/dev/null || break; sleep 0.25; done
fi
db=()
[ "$driver" = postgres ] && db=(-postgres-url "${PG_URL:?PG_URL}")
nohup "$grain" daemon -data-dir "$root/data" -sandbox-dir "$root/sandbox" -sandbox-port-base=0 \
  -disable-native-sandboxes -disable-self-upgrade -ui-addr "127.0.0.1:$port" \
  -github-host "127.0.0.1:$simport" -github-insecure-http -default-target-repo acme/widgets \
  -poll-interval 2s ${db[@]+"${db[@]}"} >>"$root/daemon.log" 2>&1 </dev/null &
echo $! >"$root/daemon.pid"
for _ in $(seq 1 120); do curl -fsS -o /dev/null "http://127.0.0.1:$port/healthz" 2>/dev/null && break; sleep 0.25; done
"$grain" access-code -data-dir "$root/data" >"$root/code"
echo "daemon $(cat "$root/daemon.pid") on 127.0.0.1:$port ($driver), githubsim on 127.0.0.1:$simport"
