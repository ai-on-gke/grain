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

# Walkthrough 2026-11-13 §9 items 1, 2 and 5: relsim.go (the GitHub
# simulator), kmsfakesrv.go (Cloud KMS) and a real `grain daemon`
# (SQLite, -sandbox-port-base=0, no sandboxes) signing release approvals
# with a KMS key version. Turns on acme/widgets' declarations so the
# daemon reads the release manager's schedules, and writes the access
# code to $root/code.
#
#   bash start-daemon.sh <root> [daemon-port]
#
# KMS_KEY overrides the key version (to try a bad one).
set -euo pipefail
root="${1:?root}"
port="${2:-4450}"
simport=$((port + 10))
kmsport=$((port + 20))
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$here/../../.." && pwd)"
grain="${GRAIN:-/home/debian/bin/grain}"
key="${KMS_KEY:-projects/demo-project/locations/us/keyRings/kr/cryptoKeys/k/cryptoKeyVersions/1}"
mkdir -p "$root/data/secrets/github" "$root/sandbox"
up() { [ "$(curl -s -o /dev/null -w '%{http_code}' "$1")" != 000 ]; }
if ! up "http://127.0.0.1:$simport/_calls"; then
  (cd "$repo_root" && go build -o "$root/relsim" "$here/relsim.go")
  nohup "$root/relsim" -addr "127.0.0.1:$simport" -dir "$root/sim" -repo-root "$repo_root" >"$root/sim.log" 2>&1 </dev/null &
  for _ in $(seq 1 40); do up "http://127.0.0.1:$simport/_calls" && break; sleep 0.25; done
fi
if ! up "http://127.0.0.1:$((kmsport + 1))/counts"; then
  (cd "$repo_root" && go build -o "$root/kmsfakesrv" "$here/kmsfakesrv.go")
  nohup "$root/kmsfakesrv" -addr "127.0.0.1:$kmsport" -ctl "127.0.0.1:$((kmsport + 1))" >"$root/kms.log" 2>&1 </dev/null &
  for _ in $(seq 1 40); do up "http://127.0.0.1:$((kmsport + 1))/counts" && break; sleep 0.25; done
fi
printf '{"*": "bot"}' >"$root/data/secrets/github/credentials.json"
printf 'sim-bot-token' >"$root/data/secrets/github/bot.token"
if [ -f "$root/daemon.pid" ]; then
  kill "$(cat "$root/daemon.pid")" 2>/dev/null || true
  for _ in $(seq 1 40); do kill -0 "$(cat "$root/daemon.pid")" 2>/dev/null || break; sleep 0.25; done
fi
GRAIN_KMS_ENDPOINT="http://127.0.0.1:$kmsport/v1/" GRAIN_KMS_TOKEN=fake-kms-token \
nohup "$grain" daemon -data-dir "$root/data" -sandbox-dir "$root/sandbox" -sandbox-port-base=0 \
  -disable-native-sandboxes -disable-self-upgrade -ui-addr "127.0.0.1:$port" \
  -github-host "127.0.0.1:$simport" -github-insecure-http -default-target-repo acme/widgets \
  -release-approval-kms-key "$key" -poll-interval 2s >>"$root/daemon.log" 2>&1 </dev/null &
pid=$!
echo $pid >"$root/daemon.pid"
for _ in $(seq 1 240); do up "http://127.0.0.1:$port/healthz" && break; kill -0 $pid 2>/dev/null || break; sleep 0.25; done
if ! kill -0 $pid 2>/dev/null; then echo "daemon exited:"; tail -3 "$root/daemon.log"; exit 1; fi
"$grain" access-code -data-dir "$root/data" >"$root/code"
curl -fsS -X PUT -H "Authorization: Bearer $(cat "$root/code")" -H 'Content-Type: application/json' \
  -d '{"declarations":true}' "http://127.0.0.1:$port/api/repos/acme/widgets/declarations" >/dev/null &&
  echo "declarations on for acme/widgets"
echo "daemon $pid on 127.0.0.1:$port, relsim on 127.0.0.1:$simport, kmsfake on 127.0.0.1:$kmsport"
