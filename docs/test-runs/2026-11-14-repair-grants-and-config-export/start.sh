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

# start.sh NAME [postgres DSN]: a simulated GitHub on 127.0.0.1:4230 and a
# real control-plane daemon (no sandboxes) on 127.0.0.1:4232, its webhook
# receiver on 127.0.0.1:4233, under $WT/NAME. Both are stopped first;
# KEEP=1 restarts the daemon on its existing data (the simulator starts empty).
set -euo pipefail
WT=${WT:-/home/debian/wt}
name=$1
driver=${2:-sqlite}
dsn=${3:-}
for p in $(pgrep -f '^/home/debian/bin/(simgh|grain daemon)' || true); do kill "$p" || true; done
sleep 2
[ -n "${KEEP:-}" ] || rm -rf "$WT/$name"
rm -rf "$WT/$name-sim"
mkdir -p "$WT/$name"
nohup /home/debian/bin/simgh -addr 127.0.0.1:4230 -dir "$WT/$name-sim" >"$WT/$name-sim.log" 2>&1 &
sleep 1
db=(-db-driver "$driver")
[ -n "$dsn" ] && db+=(-db-dsn "$dsn")
GITHUB_WEBHOOK_SECRET=s3cret nohup /home/debian/bin/grain daemon -data-dir "$WT/$name" "${db[@]}" \
  -sandbox-port-base=0 -disable-native-sandboxes -ui-addr 127.0.0.1:4232 \
  -github-webhook-addr 127.0.0.1:4233 -github-host 127.0.0.1:4230 -github-insecure-http \
  -repo acme/widgets -deployment-owner alice@example.com >"$WT/$name.log" 2>&1 &
for _ in $(seq 60); do
  [ -s "$WT/$name/local-api-token" ] && curl -fs -o /dev/null -H "Authorization: Bearer $(cat "$WT/$name/local-api-token")" http://127.0.0.1:4232/api/config && break
  sleep 1
done
echo "daemon up: token $WT/$name/local-api-token"
