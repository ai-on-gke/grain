#!/bin/bash
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

# §2 item 3: -disable-self-upgrade and -management-url on a live daemon
# over PostgreSQL. Leaves daemon i3 running on 127.0.0.1:4300 for
# item3-screens.mjs; kill it with `kill $(cat $W/i3.pid)`.
HERE=$(cd "$(dirname "$0")" && pwd)
. "$HERE/lib.sh"
DB=walk_i3; URL=$(pgurl $DB)
rm -rf "$W/i3-"*; recreate_db $DB

say "3a. invalid -management-url values are refused at startup"
for bad in 'javascript:alert(1)' 'ftp://registry.example.test/x' 'https://ops:hunter2@registry.example.test/x' '/instances/acme' 'https://' 'data:text/html,hi'; do
  P=$(daemon i3-bad 4300 "$W/i3-bad" -postgres-url "$URL" -disable-self-upgrade -management-url "$bad")
  wait_gone "$P"
  printf '%-45s -> %s\n' "$bad" "$(grep -m1 -o 'management-url must.*' "$W/i3-bad.log" || tail -1 "$W/i3-bad.log")"
done
grep -c hunter2 "$W/i3-bad.log" | sed 's/^/password echoed in the last refusal: /'

say "3b. a valid http:// URL is accepted (the flag allows http as well as https)"
P=$(daemon i3-http 4300 "$W/i3-http" -postgres-url "$URL" -disable-self-upgrade -management-url http://registry.example.test/instances/acme)
wait_up 4300 && api "$W/i3-http" 4300 /api/config | jq -c '{selfUpgradeDisabled, managementUrl}'
kill "$P"; wait_gone "$P"

say "3c. -disable-self-upgrade -management-url https://registry.example.test/instances/acme (with an -upgrade-image configured)"
P=$(daemon i3 4300 "$W/i3" -postgres-url "$URL" -disable-self-upgrade -management-url https://registry.example.test/instances/acme \
  -upgrade-image ghcr.io/ai-on-gke/grain:latest)
echo "$P" >"$W/i3.pid"
wait_up 4300 || { tail -20 "$W/i3.log"; exit 1; }
grep -h "self-upgrade" "$W/i3.log" | head -2
api "$W/i3" 4300 /api/config | jq -c '{selfUpgradeDisabled, managementUrl}'
api "$W/i3" 4300 /api/upgrade | jq -c '{enabled, selfUpgradeDisabled, managementUrl}'
for req in "POST /api/upgrade {\"branch\":\"main\"}" "PUT /api/upgrade/source {\"image\":\"ghcr.io/ai-on-gke/grain:latest\"}" "DELETE /api/upgrade/scheduled {}" "GET /api/upgrade/targets" "GET /api/upgrade/release-notes"; do
  set -- $req
  printf '%-28s -> ' "$1 $2"; api "$W/i3" 4300 "$2" -X "$1" ${3:+-d "$3"} -w ' [%{http_code}]' | cut -c1-200; echo
done
api "$W/i3" 4300 /api/database | jq -c '{driver: (.driver // .current // .), migrate: (.migrate // .canMigrate // null)}' 2>/dev/null | cut -c1-300

say "3d. automatic upgrades due right now are not acted on"
NOW=$(date -u +%H:%M)
api "$W/i3" 4300 /api/settings -X POST -d "{\"autoUpgradeOn\":\"daily\",\"autoUpgradeChannel\":\"main\",\"autoUpgradeTime\":\"$NOW\",\"timeZone\":\"UTC\"}" -w ' [%{http_code}]\n' | cut -c1-300
api "$W/i3" 4300 /api/settings | jq -c '{autoUpgradeOn, autoUpgradeChannel, autoUpgradeTime}'
sleep 25 # two reconcile cycles
echo "auto_upgrade_run rows: $(psql_g $DB -c 'SELECT count(*) FROM auto_upgrade_run')"
grep -ci "upgrad.*\(start\|hold\|pull\)" "$W/i3.log" | sed 's/^/upgrade start\/hold lines in the daemon log: /'
api "$W/i3" 4300 /api/upgrade | jq -c '{enabled, state: (.state // .status // null), scheduled: (.scheduled // null)}'
