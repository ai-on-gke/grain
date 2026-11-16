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

# item4-git.sh: §11 item 4, the git built-in's data plane on 4254.
# Configures https://grain.dev/capabilities/git against a made-up
# upstream (127.0.0.1:9, a secret holding a fake token), checks it binds
# 127.0.0.1:4254 beside the daemon's own git proxy and that `grain
# health` is clean, then occupies 4254 with socat, restarts the daemon,
# and checks `grain health` names the built-in that cannot start.
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
E=$HERE/env.sh
ROOT=${ROOT:-/var/tmp/hed}
ADDR=${ADDR:-127.0.0.1:4292}
export GRAIN_SERVER="http://$ADDR" GRAIN_TOKEN=walkthrough-local-api-token-0123456789abcdef
api() { curl -fsS -H "Authorization: Bearer $GRAIN_TOKEN" -H 'Content-Type: application/json' "$@"; }
git_state() {
	api "$GRAIN_SERVER/api/capability-servers" |
		jq -c '.[] | select(.id=="https://grain.dev/capabilities/git") | {state: .status.state, reason: .status.reason}'
}

api -X PUT "$GRAIN_SERVER/api/secrets/walk-git/token" -d '{"value":"walk-fake-upstream-token"}' >/dev/null
api -X PUT "$GRAIN_SERVER/api/capability-servers" -d '{"id":"https://grain.dev/capabilities/git","config":{"command":"grain","args":["capability","git"],"builtin":"git","config":{"upstream":"http://127.0.0.1:9","repos":["acme/widgets"],"token":"$secret:walk-git/token"}}}' >/dev/null
sleep 3
echo "--- configured:"; git_state
ss -ltnp | grep -E '127.0.0.1:(4254|9728)\b' || true
echo "--- the data plane on 4254, with no credential:"
curl -sS -i "http://127.0.0.1:4254/acme/widgets.git/info/refs?service=git-upload-pack" | sed -n '1p;3p;$p'
echo
echo "--- grain health:"; "${BIN:-/home/debian/bin}/grain" health && echo "(exit 0)"

"$E" daemon-stop
socat TCP-LISTEN:4254,bind=127.0.0.1,fork,reuseaddr /dev/null </dev/null >/dev/null 2>&1 &
squat=$!
sleep 0.5
"$E" daemon-start
sleep 5
echo "--- with 4254 occupied:"; git_state
"${BIN:-/home/debian/bin}/grain" health || echo "(exit $?)"
kill "$squat"
"$E" daemon-stop
"$E" daemon-start
sleep 3
echo "--- 4254 free again:"; git_state
