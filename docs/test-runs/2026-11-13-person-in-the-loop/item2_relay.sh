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

# §4 item 2: `grain task message <id> --relay` from every kind of caller,
# against a daemon up.sh started (SQLite or STORE=postgres).
set -uo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/common.sh"
set +e
echo '{"kind":"plain_reply","response":"idle"}' >"$WORK_DIR/action-default.json"

RELAY='grain task message TARGET --relay "Blue, please."; echo rc=$?'
relay_from() { # relay_from <acting task> <target>: script and wake <acting>'s run
	local acting=$1 target=$2
	script_next "$acting" "$(jq -nc --arg c "${RELAY//TARGET/$target}" '{"kind":"mcp_sequence","calls":[{"tool":"run_command","args":{"command":$c,"description":"relay"}}]}')"
}
last_out() { tail -1 "$WORK_DIR/runs/mcp-$1.jsonl" | jq -r '.outputs[-1].text' | grep -v '^$' | grep -v '^std' | head -3; }

"$G" access allow --reason walk user:alice@example.com >/dev/null 2>&1
"$G" access allow --role grain/viewer --reason walk user:bob@example.com >/dev/null 2>&1
api PUT /api/api-scope-sets '{"set":{"name":"walk/other-repo","description":"contributor on acme/other only","includes":["grain/contributor"],"repos":["acme/other"]}}' >/dev/null
"$G" access allow --role walk/other-repo --reason walk user:carol@example.com >/dev/null 2>&1

# The task waiting on a person.
Q=$(new_task "relay target" '{"kind":"mcp_sequence","calls":[{"tool":"ask_question","args":{"question":"Red or blue?","summary":"Pick red or blue for the widget"}}]}')
wait_state "$Q" awaiting_reply

# A: alice's own console session, alice's turn -> allowed.
S=$(as_person alice@example.com POST /api/console-sessions '{"title":"alice relay"}' | jq -r '.sessions[] | select(.title=="alice relay") | .taskId')
relay_from "$S" "$Q"
as_person alice@example.com POST "/api/tasks/$S/comments" '{"body":"Tell the task I want blue."}' >/dev/null
sleep 6
echo "== A verified alice, her session $S -> $(last_out "$S" | tr '\n' ' ')"
echo "   target $Q state: $(state "$Q") (re-ran and asked again: the default action)"
api GET "/api/tasks/$Q" | jq -c '.comments[] | select(.relayedVia) | {author, authorSource, authorLabel, relayedVia}'

wait_state "$Q" awaiting_reply
# B: an ordinary task run, holding api:tasks:write so the relay rule (not
# a missing grant) is what answers.
R=$(new_task "ordinary run" '{"kind":"plain_reply","response":"x"}' '{"grants":["https://grain.dev/capabilities/api#tasks:write"]}')
wait_state "$R" awaiting_reply
relay_from "$R" "$Q"
api POST "/api/tasks/$R/run-now" '{}' >/dev/null
sleep 6
echo "== B ordinary task run $R -> $(last_out "$R" | tr '\n' ' ')"

# C: an external agent token.
api PUT /api/settings '{"externalAgentApi":true}' >/dev/null
TOK=$(api POST /api/external-agents/tokens '{"name":"relayer","scopes":["tasks:read","tasks:write"],"repos":["acme/widgets"]}' | jq -r .token)
printf '== C external agent token -> '
curl -s -X POST -H "Authorization: Bearer $TOK" -H 'Content-Type: application/json' \
	-d '{"body":"Blue, please.","relay":true}' -w ' HTTP %{http_code}\n' "$DAEMON/api/tasks/$Q/comments"

# D: a session whose latest turn is the access code's (unverified operator).
relay_from "$S" "$Q"
api POST "/api/tasks/$S/comments" '{"body":"(access code) relay blue"}' >/dev/null
sleep 6
echo "== D session $S, unverified access-code turn -> $(last_out "$S" | tr '\n' ' ')"

# E: a session whose latest turn is bob's (verified, viewer: may not reply).
relay_from "$S" "$Q"
printf '   bob posting to the session: '
as_person bob@example.com POST "/api/tasks/$S/comments" '{"body":"bob: relay blue"}' | head -c 200
echo
sleep 6
echo "== E session $S, bob's turn -> $(last_out "$S" | tr '\n' ' ')"

# F: carol, a contributor on acme/other only: the target is on acme/widgets.
relay_from "$S" "$Q"
printf '   carol posting to the session: '
as_person carol@example.com POST "/api/tasks/$S/comments" '{"body":"carol: relay blue"}' | head -c 200
echo
sleep 6
echo "== F session $S, carol's turn (role on acme/other only) -> $(last_out "$S" | tr '\n' ' ')"

# G: a session relaying onto itself.
relay_from "$S" "$S"
as_person alice@example.com POST "/api/tasks/$S/comments" '{"body":"relay onto yourself"}' >/dev/null
sleep 6
echo "== G session $S onto itself -> $(last_out "$S" | tr '\n' ' ')"
echo "target $Q final state: $(state "$Q"); relayed comments: $(api GET "/api/tasks/$Q" | jq '[.comments[] | select(.relayedVia)] | length')"
