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

# §1 item 6: a schema-86 data dir holding executor tokens upgrades to 93.
#
#   OLD=/var/tmp/hr/grain86 NEW=/home/debian/bin/grain R=/var/tmp/hr86 upgrade-86.sh
#
# OLD is built from `git show 49d57a8e^1` (the parent of PR #411's merge):
#   git worktree add /var/tmp/old86 49d57a8e^1 && (cd /var/tmp/old86 && go build -o $OLD ./cmd/grain)
set -u
OLD=${OLD:-/var/tmp/hr/grain86}
NEW=${NEW:-grain}
R=${R:-/var/tmp/hr86}
PORT=${PORT:-4320}
DATA=$R/data
rm -rf "$R"; mkdir -p "$R"
B=http://127.0.0.1:$PORT

run() { # run BIN LOG
  HOME=$R nohup "$1" daemon -data-dir "$DATA" -ui-addr 127.0.0.1:$PORT -sandbox-port-base=0 \
    -disable-native-sandboxes -poll-interval 2s >"$R/$2" 2>&1 &
  echo $! >"$R/pid"
  for _ in $(seq 120); do [ "$(curl -s -o /dev/null -w "%{http_code}" "$B/")" != 000 ] && [ -f "$DATA/local-api-token" ] && return; sleep 0.5; done
  echo "daemon did not answer; tail of $2:"; tail -20 "$R/$2"; exit 1
}
stopd() { kill "$(cat "$R/pid")"; for _ in $(seq 60); do kill -0 "$(cat "$R/pid")" 2>/dev/null || return; sleep 0.5; done; }
tok() { tr -d '\n' <"$DATA/local-api-token"; }

echo "== schema 86 ($("$OLD" version 2>/dev/null | head -1))"
run "$OLD" old.log
for n in laptop ci-box; do
  curl -sS -H "Authorization: Bearer $(tok)" -H 'Content-Type: application/json' \
    -d "{\"name\":\"$n\"}" "$B/api/executors/tokens" | sed 's/"token":"[^"]*"/"token":"<redacted>"/'; echo
done
curl -sS -H "Authorization: Bearer $(tok)" "$B/api/executors/tokens" | head -c 400; echo
stopd
echo "store schema / executor rows before:"
sqlite3 "$DATA/store/grain.db" "select version from grain_schema; select id,name,prefix from executor_token;" 2>&1 \
  || sqlite3 "$(ls "$DATA"/store/*.db | head -1)" "select id,name,prefix from executor_token;"

echo "== upgrade to $("$NEW" version 2>/dev/null | head -1)"
run "$NEW" new.log
grep -iE "migrat|schema|executor|refus|error" "$R/new.log" | head -20
echo "store after:"
DB=$(ls "$DATA"/store/*.db | head -1)
sqlite3 "$DB" "select version from grain_schema; select name from sqlite_master where name like 'executor%' or name like 'hostd%';"
echo "== /settings/executors and the API"
curl -sS -o /dev/null -w 'GET /settings/executors -> %{http_code} %{redirect_url}\n' -H "Authorization: Bearer $(tok)" "$B/settings/executors"
curl -sS -w '\nGET /api/executors/tokens -> %{http_code}\n' -H "Authorization: Bearer $(tok)" "$B/api/executors/tokens" | tail -c 300
curl -sS -H "Authorization: Bearer $(tok)" "$B/api/hostds"; echo
echo "== grain help mentions of executor"
"$NEW" help 2>&1 | grep -in executor
"$NEW" daemon -h 2>&1 | grep -in executor
stopd
