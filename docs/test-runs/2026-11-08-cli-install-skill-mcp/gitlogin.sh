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

# §2 item 3 (second half): `grain git-login` without -author (SignAs, #889)
# records the credential *owner's* forge profile name as the signature
# ("Signed by <owner> (via Grain)"), against a real `grain daemon` and a
# forge stub answering GET /api/v3/user.
#
#   D=<daemon URL> CODE=<access code> DATA=<daemon -data-dir> ./gitlogin.sh
set -u
GRAIN=${GRAIN:-/home/debian/bin/grain}
D=${D:?daemon URL}
CODE=${CODE:?access code}
DATA=${DATA:?daemon data dir}
HERE=$(cd "$(dirname "$0")" && pwd)
export HOME=$(mktemp -d /tmp/grain-gl.XXXXXX) GRAIN_SANDBOX_CLI=0 XDG_CONFIG_HOME=$HOME/.config
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_CONTEXT
run() { local label=$1; shift; echo "### $label"; echo "\$ $*"; "$@" 2>&1 | sed 's/^/    /'; echo "    -> exit=${PIPESTATUS[0]}"; }
# Git config that would sign as the wrong person if the commit identity
# were used for the signature instead of the forge's answer for the token.
git config --global user.name "Daemon Operator"
git config --global user.email "operator@example.com"

record() { # record <host:port>: the stored host record (never the token)
  for f in "$DATA"/secrets/github/*.host.json; do
    jq -c --arg h "$1" 'select(.host==$h)' "$f"
  done
}
login() { # login <port> <label> [git-login flags]
  local port=$1 label=$2; shift 2
  run "$label" sh -c "echo gho_walkthrough_$port | GRAIN_TOKEN=$CODE $GRAIN --server $D git-login -with-token -host 127.0.0.1:$port -base-url http://127.0.0.1:$port -scope acme/allowed $*"
  echo "    stored: $(record 127.0.0.1:$port)"
}
stub() { python3 "$HERE/forge-user-stub.py" "$@" & sleep 0.5; }

stub 4331 dana-owner "Dana Owner"; P1=$!
login 4331 "no -author: signs as the token's owner (forge profile name)"
login 4331 "explicit -author overrides" -author "\"Release Bot Override\""
login 4331 "no -author again after an explicit one: back to the owner (not the stale override)"
run "the token is not readable back" sh -c "GRAIN_TOKEN=$CODE $GRAIN --server $D -json deployment 2>/dev/null | grep -c gho_walkthrough; curl -s -H 'Authorization: Bearer $CODE' $D/api/github-tokens | grep -c gho_walkthrough"
run "forge saw the token only on /api/v3/user" sh -c "curl -s 127.0.0.1:4331/_log | jq -c '[.[] | .path] | unique'"
kill $P1

stub 4332 grain-ci "grain-ci[bot]"; P2=$!
login 4332 "a [bot] profile name is never used as a human signature"
kill $P2

stub 4333 nameless ""; P3=$!
login 4333 "a profile with no name"
kill $P3
