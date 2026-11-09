#!/bin/sh
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

# step.sh (host side, run by fake-agy while the run is live; copied to
# /home/debian/walk/step.sh): §2 items 3 and 4 inside one real run.
#   item 4: real gcloud in the run's container, on the token-mode
#           external_account config the gcp-key grant placed, and RFC 7662
#           introspection at token_info_url for a live token and at another
#           attempt's path;
#   item 3: the run's own grain MCP server (the agent's propose_task) sent
#           every refused mixed-grant shape, plus a control.
# Token values are cut to their first characters.
W=/home/debian/walk
cp "$HOME/.gemini/config/mcp_config.json" $W/mcp-live.json
c=$(docker ps --latest --format '{{.Names}}')
[ -n "$c" ] || { echo "== no sandbox container (a CLI probe), nothing probed"; exit 0; }
[ -f "$W/probed-$c" ] && { echo "== $c already probed"; exit 0; }
touch "$W/probed-$c"
echo "== sandbox container: $c ($(docker inspect -f '{{.Config.Image}}' "$c"))"
sbx() { docker exec "$c" sh -c "$1" 2>&1; }
cut_tok() { sed -E 's/(ya29\.|tok-|gma_)?([A-Za-z0-9_-]{6})[A-Za-z0-9._@-]*/\1\2<cut>/g'; }

echo "== item 4: gcloud on the placed config"
sbx 'ls -l ~/.gcp-service-account.json; cat ~/.gcp-service-account.json'
echo "-- gcloud config list (what activate.sh set)"
sbx 'gcloud config list 2>&1'
echo "-- gcloud auth print-access-token"
tok=$(sbx 'gcloud auth print-access-token 2>&1')
echo "$tok" | cut_tok
echo "-- a second credential_file_override command: gcloud auth list"
sbx 'gcloud auth list 2>&1'
info=$(sbx 'sed -n "s/.*\"token_info_url\": *\"\([^\"]*\)\".*/\1/p" ~/.gcp-service-account.json')
other=$(echo "$info" | sed -E 's|/runs/[^/]+/|/runs/grain-999-1-0/|')
echo "token_info_url: $info"
echo "-- introspect the live token at token_info_url (inside the container)"
sbx "curl -sS -m 10 -w ' HTTP %{http_code}' -X POST '$info' --data-urlencode 'token=$tok'"; echo
echo "-- introspect the same token at another attempt's path ($other)"
sbx "curl -sS -m 10 -w ' HTTP %{http_code}' -X POST '$other' --data-urlencode 'token=$tok'"; echo
printf '%s' "$tok" > $W/live-token.secret
echo "$info" > $W/token-info-url.txt

echo "== item 3: propose_task through the run's own grain MCP server"
python3 $W/propose-shapes.py
