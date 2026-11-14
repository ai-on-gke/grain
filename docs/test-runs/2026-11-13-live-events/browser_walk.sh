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

# browser_walk.sh <sqlite|postgres> [port] [grain-binary] [out-dir]:
# docs/test-plans/2026-11-13-afternoon.md §2 items 2 and 3 in a browser.
#
# Starts a real daemon (common.sh: -host-sandboxes, the fake agy, ghsim for
# acme/widgets), pauses dispatch, files the walk's tasks -- one on
# acme/widgets holding a host-script grant (written into task_grant as
# items1_2.sh does; item 1's ghsa_callback.sh walks the real minted-token
# path) and one carrying 3,000 comments -- declares a widgets-reader role
# (tasks:read on acme/widgets only) for widgets@example.com, signed in
# through iapsim, then runs browser_walk.mjs. Ports: daemon <port>, ghsim
# +4, iapsim issuer +5 and proxy +6.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=common.sh
source "$HERE/common.sh"
kind=${1:-sqlite} port=${2:-4293}
GRAIN=${3:-$GRAIN}
OUT=${4:-$HERE/out/browser-$kind}
W=/tmp/wl-browser-$kind
mkdir -p "$OUT"
reset_store "$kind" "$W"
start_ghsim "$W" $((port + 4)) "$kind" || exit 1
# The viewer is a person, not a token: tests/iap/iapsim.go signs IAP
# assertions for whoever the X-Iapsim-Email header names, and its proxy
# forwards to the daemon, which verifies them against iapsim's JWK set.
IAP_AUD=/projects/123456/global/backendServices/789012 IAP_ISS=https://iap.test.local
(cd "$HERE/../../.." && go build -o "$W/bin/iapsim" tests/iap/iapsim.go)
"$W/bin/iapsim" -issuer "127.0.0.1:$((port + 5))" -proxy "127.0.0.1:$((port + 6))" \
	-upstream "http://127.0.0.1:$port" -aud "$IAP_AUD" -iss "$IAP_ISS" >"$W/iapsim.log" 2>&1 </dev/null &
echo $! >"$W/iapsim.pid"
start_daemon "$kind" "$W" "$port" "$GRAIN" -ui-iap-audience "$IAP_AUD" -ui-iap-issuer "$IAP_ISS" \
	-ui-iap-keys-url "http://127.0.0.1:$((port + 5))/jwks" || exit 1
setup_agent
api PUT /api/settings '{"externalAgentApi":true}' >/dev/null
api POST /api/pipeline-pause '{"reason":"browser walk: nothing dispatches"}' >/dev/null

widgets=$(api POST /api/tasks '{"title":"widgets inbox walk","body":"walk","repo":"acme/widgets"}' | jq -r .id)
sql "$kind" "$W" "INSERT INTO task_grant (task_id,capability,via) VALUES ('$widgets','https://grain.dev/capabilities/host-script#host-script','grant')"
long=$(api POST /api/tasks '{"title":"3,000-comment deep-link walk","body":"walk","repo":"acme/widgets"}' | jq -r .id)
api PUT /api/api-scope-sets '{"set":{"name":"widgets-reader","description":"Task reader restricted to acme/widgets","scopes":["tasks:read"],"repos":["acme/widgets"]}}' >"$W/scope-set.json"
api POST /api/access/principals '{"entry":"user:widgets@example.com","role":"widgets-reader","reason":"live-events walk: repo-scoped viewer"}' >"$W/principal.json"

PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright} node "$HERE/browser_walk.mjs" \
	--url "$DAEMON" --token "$(token)" --viewer-url "http://127.0.0.1:$((port + 6))" --viewer-email widgets@example.com \
	--widgets-task "$widgets" --long-task "$long" --out "$OUT" ${ALLOW_FAILURES:+--allow-failures}
rc=$?
stop_daemon
stop_ghsim "$W"
kill "$(cat "$W/iapsim.pid")" 2>/dev/null
exit $rc
