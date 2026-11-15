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

# item_8.sh: §3 item 8, typed-in credentials. A throwaway clone of the
# walkthrough deployment's own grain/config: main gains a capability server
# holding a literal header (what a store from before the rule holds), then
# a pull-request branch adds a second literal. `grain state check -base`
# fails the branch, naming only the added literal, and warns about the
# held one. Then the live daemon's API: a new literal is refused, the held
# one stays editable. Values are random; output is checked by grep.
set -uo pipefail
source "$(dirname "$0")/env.sh"
step() { printf '\n== %s\n' "$*"; }
R=$WORK/config-pr
rm -rf "$R"
git clone -q "$WORK/data/state-repo" "$R"
gitc() { git -C "$R" -c user.name=walk -c user.email=walk@example.com "$@"; }
held=$(head -c 18 /dev/urandom | base64 | tr -dc A-Za-z0-9)
added=$(head -c 18 /dev/urandom | base64 | tr -dc A-Za-z0-9)
add_server() { # add_server URL VALUE
	python3 - "$R/tables/grain_config.json" "$1" "$2" <<'PY'
import json, sys
path, url, value = sys.argv[1:4]
rows = json.load(open(path))
servers = json.loads(rows[0]["mcp_servers"] or "{}")
servers[url] = {"serverUrl": url + "/mcp", "headers": {"Authorization": "Bearer " + value}}
rows[0]["mcp_servers"] = json.dumps(servers, sort_keys=True)
json.dump(rows, open(path, "w"), indent=2)
PY
}
redact() { sed "s/$held/<held value>/g; s/$added/<added value>/g"; }

step "8a. main holds a literal header (as a store from before the rule)"
add_server https://caps.example.com/held "$held"
gitc commit -qam "seed: a server with a typed-in header"
"$HEAD_BIN" state check -base main "$R" 2>&1 | redact | grep -iE 'literal|typed|credential|ok|warn|fail|error' | cut -c1-260
echo "[rc=${PIPESTATUS[0]}]"

step "8b. a config PR adds a second literal: the check fails"
gitc checkout -q -b config-pr
add_server https://caps.example.com/typed-in "$added"
gitc commit -qam "pr: add a server with a typed-in header"
"$HEAD_BIN" state check -base main "$R" >"$WORK/8b.txt" 2>&1; rc=$?
redact <"$WORK/8b.txt" | grep -iE 'literal|typed|credential|warn|fail|error' | cut -c1-260
echo "[rc=$rc] values printed: $(grep -cE "$held|$added" "$WORK/8b.txt")"

step "8c. the same PR referencing \$secret: instead passes"
gitc checkout -q main; gitc branch -D config-pr -q 2>/dev/null; gitc checkout -q -b config-pr-ref
python3 - "$R/tables/grain_config.json" <<'PY'
import json, sys
rows = json.load(open(sys.argv[1]))
s = json.loads(rows[0]["mcp_servers"])
s["https://caps.example.com/referenced"] = {"serverUrl": "https://caps.example.com/referenced/mcp", "headers": {"Authorization": "Bearer $secret:walk-cap-token"}}
rows[0]["mcp_servers"] = json.dumps(s, sort_keys=True)
json.dump(rows, open(sys.argv[1], "w"), indent=2)
PY
gitc commit -qam "pr: add a server referencing a secret"
"$HEAD_BIN" state check -base main "$R" 2>&1 | redact | grep -iE 'literal|typed|credential|warn|fail|error' | cut -c1-260
echo "[rc=${PIPESTATUS[0]}]"

step "8d. the API refuses a new literal"
put_server() { # put_server ID BODY_JSON_CONFIG -> status line, answer in $WORK/8d.txt
	curl -sS -X PUT -H "Authorization: Bearer $(cat "$WORK/data/local-api-token")" -H 'Content-Type: application/json' \
		--data "$(jq -nc --arg id "$1" --argjson c "$2" '{id:$id,config:$c}')" "$API/api/capability-servers" -o "$WORK/8d.txt" -w '%{http_code}\n'
}
put_server https://caps.example.com/api-typed "$(jq -nc --arg v "$added" '{serverUrl:"https://caps.example.com/api-typed/mcp",headers:{Authorization:("Bearer "+$v)}}')"
redact <"$WORK/8d.txt" | cut -c1-300; echo
echo "values in the DB: $(sqlite3 "$WORK/data/store/grain.db" "select mcp_servers from grain_config" | grep -c "$added")"
put_server https://caps.example.com/api-ref '{"serverUrl":"https://caps.example.com/api-ref/mcp","headers":{"Authorization":"Bearer $secret:walk-cap-token"}}'
redact <"$WORK/8d.txt" | cut -c1-200; echo

step "8e. a literal the store already holds stays editable"
# Put a held literal in the live store the way a pre-rule build left it,
# then restart: the daemon moves it into the secrets store at start.
DB=$WORK/data/store/grain.db
command -v sqlite3 >/dev/null || { sudo apt-get update -qq && sudo apt-get install -y -qq sqlite3 >/dev/null; }
servers=$(sqlite3 "$DB" "select mcp_servers from grain_config limit 1")
servers=$(jq -c --arg v "$held" '. + {"https://caps.example.com/held": {serverUrl:"https://caps.example.com/held/mcp", headers:{Authorization:("Bearer "+$v)}}}' <<<"${servers:-{\}}")
stop_daemon_unit; stop_daemon
sqlite3 "$DB" "update grain_config set mcp_servers = '$servers'"
start_daemon "$HEAD_BIN" && sleep 2
grep -iE 'typed|literal|moved .*header|capability server.*secret' "$WORK/daemon.log" | tail -3 | redact | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-260
held_cfg() { sqlite3 "$WORK/data/store/grain.db" "select mcp_servers from grain_config" | jq -c '.["https://caps.example.com/held"]' | redact; }
held_cfg
put_server https://caps.example.com/held "$(sqlite3 "$WORK/data/store/grain.db" "select mcp_servers from grain_config" | jq -c '.["https://caps.example.com/held"] | .serverUrl = "https://caps.example.com/held/v2/mcp"')"
echo "edit held server (answer above)"; redact <"$WORK/8d.txt" | cut -c1-200; echo
held_cfg
echo "values in the DB now: $(sqlite3 "$DB" "select mcp_servers from grain_config" | grep -c "$held")"
