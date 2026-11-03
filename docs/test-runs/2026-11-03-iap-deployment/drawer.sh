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

# Starts iapsim and a `grain demo -external-config-repo` behind it, then runs
# drawer.mjs (§4 items 4 and 5). Needs GRAIN (a binary built with the
# embedded UI), go, node with ui/node_modules, Playwright's Chromium, and
# free ports 4300/18080/18443.
#
#   docs/test-runs/2026-11-03-iap-deployment/drawer.sh [OUT]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
OUT=${1:-/tmp/iap-drawer}
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)
GRAIN=${GRAIN:-$HOME/bin/grain}
export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}
AUD=/projects/123/global/backendServices/456
ISS=https://iap.test.local
ISSUER=http://127.0.0.1:18080

(cd "$ROOT" && go build -o "$OUT/iapsim" tests/iap/iapsim.go) || exit 2
"$OUT/iapsim" -issuer 127.0.0.1:18080 -proxy 127.0.0.1:18443 -upstream http://127.0.0.1:4300 -aud "$AUD" -iss "$ISS" \
	>"$OUT/iapsim.log" 2>&1 &
SIM=$!
mkdir -p "$OUT/tmp"
TMPDIR="$OUT/tmp" "$GRAIN" demo -open=false -addr 127.0.0.1:4300 -as owner@example.com \
	-access-code-file "$OUT/access-code" \
	-external-config-repo https://git.example.com/made-up/grain-config.git \
	-ui-iap-audience "$AUD" -ui-iap-issuer "$ISS" -ui-iap-keys-url "$ISSUER/jwks" \
	>"$OUT/demo.log" 2>&1 &
DEMO_PID=$!
trap 'kill $DEMO_PID $SIM 2>/dev/null' EXIT
for _ in $(seq 150); do curl -s -o /dev/null http://127.0.0.1:4300/healthz && break; sleep 0.2; done

cd "$ROOT/ui" && OUT="$OUT" PROXY=http://127.0.0.1:18443 DEMO=http://127.0.0.1:4300 CODE="$(cat "$OUT/access-code")" \
	node "$HERE/drawer.mjs"
