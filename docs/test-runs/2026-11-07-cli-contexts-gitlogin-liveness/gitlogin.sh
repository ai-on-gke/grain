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

# §8 item 3: `grain git-login -web` against a local GitHub OAuth
# device-flow stub, walking authorization_pending, slow_down,
# expired_token, access_denied and success, then reading back what the
# deployment (grain demo on :4291) stored and the signature it will sign
# pull requests and comments with.
set -u
GRAIN=${GRAIN:-/home/debian/bin/grain}
HERE=$(cd "$(dirname "$0")" && pwd)
S=http://127.0.0.1:4291
CODE=$(cat ${A_CODE_FILE:-/tmp/w/srv-a/code})
export HOME=$(mktemp -d /tmp/grain-gl.XXXXXX) GRAIN_SANDBOX_CLI=0 BROWSER=true
export XDG_CONFIG_HOME=$HOME/.config
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_CONTEXT
run() { local label=$1; shift; echo "### $label"; echo "\$ $*"; "$@" 2>&1 | sed 's/^/    /'; echo "    -> exit=${PIPESTATUS[0]}"; }

flow() { # flow <port> <script> <label> [extra git-login flags]
  local port=$1 script=$2 label=$3; shift 3
  python3 "$HERE/deviceflow-stub.py" "$port" "$script" & local pid=$!
  sleep 0.5
  run "$label (stub answers: $script)" env GRAIN_TOKEN="$CODE" timeout 60 $GRAIN --server $S git-login -web \
    -host 127.0.0.1:$port -base-url http://127.0.0.1:$port -scope acme/widgets "$@"
  curl -s 127.0.0.1:$port/_log | jq -c '.[] | select(.path=="/login/oauth/access_token") | {answer,gap_s,grant_type_ok}' | sed 's/^/    poll: /'
  curl -s 127.0.0.1:$port/_log | jq -c '.[] | select(.path!="/login/oauth/access_token" and .path!="/login/device/code")' | sed 's/^/    other: /'
  kill $pid; wait $pid 2>/dev/null
}

flow 4294 expired "expired_token"
flow 4295 denied "access_denied"
flow 4296 pending,slow_down,pending,token "pending -> slow_down -> pending -> success"
flow 4297 token "success with an explicit -author" -author "Dana Example"

echo "== what the deployment stored (token never echoed)"
run "deployment git connections" sh -c "env GRAIN_TOKEN=$CODE $GRAIN --server $S -json deployment 2>/dev/null | jq -c '.gitConnections // .git // empty' | head -c 2000"
if env GRAIN_TOKEN=$CODE $GRAIN --server $S -json deployment 2>/dev/null | grep -q gho_walkthrough; then echo "    LEAK: the delegated token is readable back"; else echo "    ok: the delegated token is not readable back"; fi
