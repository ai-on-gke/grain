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

# start-demo.sh RUN_DIR [PORT]: shared steps 2 and 6 for §2 -- a fresh
# `grain demo -github-sim -fake-ci -reconcile 2s` on loopback, its access
# code in RUN_DIR/code, its log in RUN_DIR/demo.log, its pid in
# RUN_DIR/pid and the simulated acme/widgets bare repository's path in
# RUN_DIR/bare.
set -euo pipefail
run="${1:?run dir}"
port="${2:-18080}"
grain="${GRAIN:-/home/debian/bin/grain}"
if [ -f "$run/pid" ]; then kill "$(cat "$run/pid")" 2>/dev/null || true; sleep 1; fi
rm -rf "$run"
mkdir -p "$run"
before="$(ls -d /tmp/grain-demo-* 2>/dev/null || true)"
nohup "$grain" demo -github-sim -fake-ci -reconcile 2s -addr "127.0.0.1:$port" \
  -access-code-file "$run/code" >"$run/demo.log" 2>&1 </dev/null &
echo $! >"$run/pid"
for _ in $(seq 1 60); do
  curl -fsS -o /dev/null "http://127.0.0.1:$port/healthz" 2>/dev/null && break
  grep -q "serving fake tasks" "$run/demo.log" 2>/dev/null && break
  sleep 0.5
done
for d in /tmp/grain-demo-*; do
  [ -d "$d" ] || continue
  case "$before" in *"$d"*) ;; *) echo "$d/githubsim/acme/widgets.git" >"$run/bare" ;; esac
done
echo "demo pid $(cat "$run/pid"), code $(cat "$run/code"), bare $(cat "$run/bare")"
