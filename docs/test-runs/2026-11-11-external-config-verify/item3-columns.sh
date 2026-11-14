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

# item3-columns.sh: §11 item 3 on a second, internal-mode daemon
# (127.0.0.1:4293, data in $B). Four limited roles -- settings:write
# alone, and with secrets:write, connections:write or capabilities:admin
# -- are given in turn to dave@example.com, whom the API socket names.
# For each config-change proposal (a branch of grain/config altering one
# tables/grain_config.json column), every role tries
# POST /api/tasks/1/config-change/approve; prints the status per role.
set -euo pipefail
B=${B:-/home/debian/w11b}
GRAIN=${GRAIN:-/home/debian/bin/grain}
API=http://127.0.0.1:4293
pkill -f "grain daemon -data-dir $B/data" || true
sleep 2
rm -rf "$B"
mkdir -p "$B/data"
nohup "$GRAIN" daemon -data-dir "$B/data" -ui-addr 127.0.0.1:4293 -sandbox-port-base=0 -disable-native-sandboxes \
	-host-accounts "$(id -u)=dave@example.com" >"$B/daemon.log" 2>&1 &
for _ in $(seq 60); do
	[ -s "$B/data/local-api-token" ] && curl -s -o /dev/null "$API/api/state-repo" && break
	sleep 1
done
TOK=$(cat "$B/data/local-api-token")
code() { curl -s -H "Authorization: Bearer $TOK" -X "$1" "$API$2" ${3:+-d "$3"}; }
sock() { curl -s -w '\n%{http_code}' --unix-socket "$B/data/api/grain.sock" -X "$1" "http://localhost$2" ${3:+-d "$3"}; }
declare -A extra=([s-settings]="" [s-secrets]="secrets:write" [s-connections]="connections:write" [s-caps]="capabilities:admin")
for set in s-settings s-secrets s-connections s-caps; do
	scopes=$(jq -nc --arg x "${extra[$set]}" '["settings:read","settings:write","tasks:read","tasks:write"] + (if $x == "" then [] else [$x] end)')
	code PUT /api/api-scope-sets "{\"set\":{\"name\":\"$set\",\"description\":\"walkthrough\",\"scopes\":$scopes,\"version\":1}}" >/dev/null
done
code POST /api/access/principals '{"entry":"user:dave@example.com","role":"s-settings","reason":"walkthrough"}' >/dev/null
for _ in $(seq 60); do
	[ "$(jq length "$B/data/state-repo/tables/grain_config.json" 2>/dev/null || echo 0)" -gt 0 ] && break
	sleep 1
done
rm -rf "$B/c"
git clone -q "$B/data/state-repo" "$B/c"
git -C "$B/c" config user.name "Walk Through"
git -C "$B/c" config user.email walk@example.com

# propose NAME JQ-EDIT: a branch altering grain_config row 0, proposed on
# task 1; prints its head SHA.
propose() {
	git -C "$B/c" fetch -q origin main
	git -C "$B/c" checkout -q -B "grain/config/1/$1" origin/main
	jq --indent 2 "$2" "$B/c/tables/grain_config.json" >"$B/c/g.json" && mv "$B/c/g.json" "$B/c/tables/grain_config.json"
	git -C "$B/c" commit -qam "config change: $1"
	git -C "$B/c" push -q -f origin "grain/config/1/$1"
	local head
	head=$(git -C "$B/c" rev-parse HEAD)
	code POST /api/tasks/1/config-change "$(jq -nc --arg b "grain/config/1/$1" --arg h "$head" --arg s "$1" '{branch:$b, headSha:$h, summary:$s}')" |
		jq -c '{files: .pendingConfigChange.files?, columns: .pendingConfigChange.columns?, error}' >&2
	echo "$head"
}

# Several columns hold JSON text (mcp_servers, agent_backends); the
# edits keep each column's own type.
for c in "max_workers|.[0].max_workers += 1" \
	"git_connections|.[0].git_connections = \"[{\\\"name\\\":\\\"walk\\\"}]\"" \
	"mcp_servers|.[0].mcp_servers |= (fromjson + {\"https://mcp.example.com/walk\": {}} | tojson)" \
	"agent_backends|.[0].agent_backends = \"[{\\\"id\\\":\\\"walk\\\"}]\"" \
	"future_column|.[0].a_future_column = \"set by a later schema\""; do
	name=${c%%|*}
	edit=${c#*|}
	echo "== $name"
	head=$(propose "$name" "$edit")
	for set in s-settings s-secrets s-connections s-caps; do
		code POST /api/access/principals/user%3Adave%40example.com "{\"role\":\"$set\",\"reason\":\"walkthrough\"}" >/dev/null 2>&1 || true
		code PATCH /api/access/principals/user%3Adave%40example.com "{\"role\":\"$set\",\"reason\":\"walkthrough\"}" >/dev/null 2>&1 || true
		out=$(sock POST /api/tasks/1/config-change/approve "{\"headSha\":\"$head\"}")
		status=$(tail -1 <<<"$out")
		echo "  $set: HTTP $status $(head -n -1 <<<"$out" | jq -r '.error // "approved"' 2>/dev/null | cut -c1-200)"
		[ "$status" = 200 ] && break
	done
done
