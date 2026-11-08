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

# step.sh (host side, run by fake-agy while the run is live): the gcp token
# mode probe. The agent CLI runs on the host and the run's sandbox is a
# container, so the probe looks into it with docker exec, as the agent's
# run_command would. The walk clock is gcpwalk.go's (127.0.0.1:4261).
W=/home/debian/walk
ctl=http://127.0.0.1:4261
cfg="$HOME/.gemini/config/mcp_config.json"
cp "$cfg" $W/mcp-live.json
c=$(docker ps --latest --format '{{.Names}}')
# fake-agy runs this for each turn of the run; probe on the first only.
[ -f "$W/probed-$c" ] && { echo "== $c already probed"; exit 0; }
touch "$W/probed-$c"
echo "== sandbox container: $c ($(docker inspect -f '{{.Config.Image}}' "$c"))"
sbx() { docker exec "$c" sh -c "$1" 2>&1; }
docker cp -q $W/exchange.sh "$c":/tmp/exchange.sh

echo "== 1. what the grant placed"
sbx 'ls -l ~/.gcp-service-account* ~/.gcp-keys 2>&1; find ~/.gcp-token -printf "%M %u %p\n" 2>&1'
echo "-- ~/.gcp-service-account.json"
sbx 'cat ~/.gcp-service-account.json'
echo "-- ~/.gcp-service-account.about"
sbx 'cat ~/.gcp-service-account.about'
echo "-- subject token: $(sbx 'f=$(sh /tmp/exchange.sh field file); t=$(cat "$f"); echo "$f, ${#t} chars"')"
echo "-- private_key or client_email anywhere under ~? $(sbx 'grep -rl "private_key\|client_email" ~ 2>/dev/null || echo none')"
echo "-- gcloud: $(sbx 'command -v gcloud || echo "not in the image (activate.sh exits 0 without it)"')"
sbx 'sh /tmp/exchange.sh field token_url' > $W/token-url.txt
sbx 'cat "$(sh /tmp/exchange.sh field file)"' > $W/subject.secret
echo "-- token_url saved on the host for the after-release check: $(cat $W/token-url.txt)"

echo "== 2. exchange at the forwarded listener, then refresh as the clock runs to the deadline"
echo "walk clock: $(curl -sS $ctl/now)"
echo "-- exchange (t+0): $(sbx 'sh /tmp/exchange.sh')"
echo "-- exchange with a wrong subject token: $(sbx 'SUBJECT=wrong sh /tmp/exchange.sh')"
for by in 30m 1h 1h 1h; do
  echo "-- advance $by -> $(curl -sS -X POST "$ctl/advance?by=$by")"
  echo "   refresh: $(sbx 'sh /tmp/exchange.sh')"
done
echo "-- ID token helper (audience https://walk.example): $(sbx 'sh ~/.gcp-token/*/org-deploy-id-token https://walk.example; echo " exit $?"')"
echo "-- advance 1h (past the deadline) -> $(curl -sS -X POST "$ctl/advance?by=1h")"
echo "   refresh: $(sbx 'sh /tmp/exchange.sh')"
echo "   ID token: $(sbx 'sh ~/.gcp-token/*/org-deploy-id-token https://walk.example; echo " exit $?"')"
echo "-- rewind the walk clock to real time -> $(curl -sS -X POST "$ctl/advance?by=-4h30m")"
echo "   refresh (before the deadline again): $(sbx 'sh /tmp/exchange.sh')"
echo "-- lifetimes gcpfake's generateAccessToken was asked for: $(curl -sS $ctl/lifetimes)"

echo "== 3. propose_task with the same grant, through the run's own grain MCP server"
python3 $W/mcp-propose.py 2>&1
