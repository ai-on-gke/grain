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

# docs/test-plans/2026-11-14.md §4 items 1 and 2 against a daemon env.sh
# just started (STORE=sqlite|postgres): POST .../discover through provider
# ok / new model / 500 / hang / recovery, the 404 and 400 refusals, and
# the route as viewer, operator without connections:write, full operator,
# and external agent tokens.

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
set +e
CODE=$(tr -d '[:space:]' <"$WORK_DIR/data/local-api-token")
P=/api/deployment/agent-backends/anthro/discover

fake_mode ok
fake_models claude-walk-a
echo "--1 first refresh: $(discover anthro)"
fake_models claude-walk-a claude-walk-new
echo "--2 provider adds a model: $(discover anthro)"
fake_mode 500
echo "--3 provider 500: $(discover anthro)"
fake_mode hang
s=$(date +%s)
echo "--4 provider hangs: $(discover anthro) ($(($(date +%s) - s))s)"
fake_mode ok
echo "--5 recovered: $(discover anthro)"
echo "--6 unknown connection: $(curl -s -w ' HTTP %{http_code}' -X POST -H "Authorization: Bearer $CODE" "$DAEMON/api/deployment/agent-backends/nope/discover")"

# A framework that cannot be asked: PUT refuses to store one, so the row
# is written into the store directly, the way an old export would hold it.
legacy='{"id":"legacy-cmd","framework":"custom","command":"echo hi","models":["m1"]}'
if [[ ${STORE:-sqlite} == sqlite ]]; then
	sqlite3 "$WORK_DIR/data/store/grain.db" "update grain_config set agent_backends = json_insert(agent_backends, '\$[#]', json('$legacy'))"
else
	PGPASSWORD=grain psql -q -h 127.0.0.1 -U grain -d grain_walk -c "update grain_config set agent_backends = (agent_backends::jsonb || '[$legacy]'::jsonb)::text"
fi
echo "--7 non-discovering framework: $(curl -s -w ' HTTP %{http_code}' -X POST -H "Authorization: Bearer $CODE" "$DAEMON/api/deployment/agent-backends/legacy-cmd/discover")"
if [[ ${STORE:-sqlite} == sqlite ]]; then
	sqlite3 "$WORK_DIR/data/store/grain.db" "update grain_config set agent_backends = (select json_group_array(json(value)) from json_each(agent_backends) where json_extract(value,'\$.id')!='legacy-cmd')"
else
	PGPASSWORD=grain psql -q -h 127.0.0.1 -U grain -d grain_walk -c "update grain_config set agent_backends = (select jsonb_agg(e) from jsonb_array_elements(agent_backends::jsonb) e where e->>'id' <> 'legacy-cmd')::text"
fi

echo "-- roles"
api PUT /api/api-scope-sets '{"set":{"name":"walk/ops-no-conn","description":"operator without connections:write","includes":["grain/operator"],"deny":["connections:write"]},"reason":"walkthrough"}' >/dev/null
api POST /api/access/principals '{"entry":"user:viewer@example.com","role":"grain/viewer","reason":"walkthrough viewer"}' >/dev/null
api POST /api/access/principals '{"entry":"user:ops@example.com","role":"walk/ops-no-conn","reason":"walkthrough limited operator"}' >/dev/null
api POST /api/access/principals '{"entry":"user:admin2@example.com","role":"grain/operator","reason":"walkthrough full operator"}' >/dev/null
api PUT /api/settings '{"externalAgentApi":true}' >/dev/null
TOK=$(api POST /api/external-agents/tokens '{"name":"walk-agent","scopes":["connections:read","settings:read","connections:write"]}' | jq -r .token)
TOK2=$(api POST /api/external-agents/tokens '{"name":"walk-agent-ro","scopes":["connections:read","settings:read"]}' | jq -r .token)
for who in viewer@example.com ops@example.com admin2@example.com; do
	echo "$who: HTTP $(code_as "$who" POST "$P") $(head -c 170 "$WORK_DIR/captures/last.body")"
done
agent() { curl -s -o "$WORK_DIR/captures/last.body" -w '%{http_code}' -X POST -H "Authorization: Bearer $1" "${@:2}" "$DAEMON$P"; }
echo "agent token without connections:write: HTTP $(agent "$TOK2") $(head -c 170 "$WORK_DIR/captures/last.body")"
echo "agent token with connections:write, no reason: HTTP $(agent "$TOK") $(head -c 170 "$WORK_DIR/captures/last.body")"
echo "agent token with connections:write and X-Grain-Acting-Reason: HTTP $(agent "$TOK" -H "X-Grain-Acting-Reason: walkthrough") $(head -c 120 "$WORK_DIR/captures/last.body")"
echo "-- provider hits"
curl -s "$FAKE_CTL/hits" | tail -n 8
