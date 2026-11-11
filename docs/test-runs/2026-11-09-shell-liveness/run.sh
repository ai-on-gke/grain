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

# docs/test-plans/2026-11-09.md §5: start a real `grain demo` and the
# proxies walk.mjs drives, then run walk.mjs's modes against them.
#
#   docs/test-runs/2026-11-09-shell-liveness/run.sh [OUT_DIR] [MODE...]
#
# Needs `grain` (built from HEAD) on PATH, socat, and ui/node_modules.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
out=${1:-/tmp/shell-liveness}
shift || true
modes=${*:-stop proxies recover slow mutations phone shots}
mkdir -p "$out"
setsid grain demo -github-sim -fake-ci -addr 127.0.0.1:4290 -open=false \
  -access-code-file "$out/code" >"$out/demo.log" 2>&1 &
demo=$!
for _ in $(seq 60); do curl -fs -o /dev/null 127.0.0.1:4290/ && break; sleep 0.5; done
# 4291: switchable proxy (stall/headers/delay); 4293: strict /grain
# sub-path; 4294: /grain sub-path that also passes root paths; 4292:
# socat, SIGSTOPped by walk.mjs as a proxy holding the socket.
setsid node "$here/proxy.mjs" -listen 4291 -upstream 127.0.0.1:4290 >"$out/p4291.log" 2>&1 &
setsid node "$here/proxy.mjs" -listen 4293 -upstream 127.0.0.1:4290 -prefix /grain >"$out/p4293.log" 2>&1 &
setsid node "$here/proxy.mjs" -listen 4294 -upstream 127.0.0.1:4290 -prefix /grain -passthrough >"$out/p4294.log" 2>&1 &
setsid socat TCP-LISTEN:4292,fork,reuseaddr,bind=127.0.0.1 TCP:127.0.0.1:4290 >"$out/socat.log" 2>&1 &
trap 'kill -CONT $demo 2>/dev/null; pkill -CONT -x socat; kill $demo; pkill -x socat; pkill -f "$here/proxy.mjs"' EXIT
sleep 1
cd "$root/ui"
export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}
for m in $modes; do
  node "$here/walk.mjs" "$m" http://127.0.0.1:4290 "$demo" "$(cat "$out/code")" "$out" | tee "$out/$m.out"
done
