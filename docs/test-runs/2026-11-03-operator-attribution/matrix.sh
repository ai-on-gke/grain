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

# Items 1 and 5 of docs/test-plans/2026-11-03.md §3: every attributed
# action once per principal source, against the environment env.sh starts,
# then the store dumped as JSON.
#
#   matrix.sh [OUT]      # OUT defaults to /tmp/oa; dumps land in OUT/dump-*.json
#
# Sources: iap  = alice@example.com through iapsim's proxy (no access code)
#          code = the access code as a bearer token, straight to the demo
#          loop = loopback, no access code (expected: 401, see the report)
#          agent= the access code plus X-Grain-Acting-Task (the daemon's API
#                 proxy as a sandbox's run reaches it) for a task holding api grants
#          ext  = an external-agent gma_ token
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/env.sh"
OUT=${1:-/tmp/oa}
if [[ ${REUSE:-} != 1 ]]; then
	start_env "$OUT"
	trap stop_env EXIT
else
	CODE=$(cat "$OUT/access-code")
	DB=$(ls "$OUT"/tmp/grain-demo-*/grain.db | head -1)
fi
LOG=$OUT/actions.tsv
printf 'source\taction\tstatus\terror\n' > "$LOG"

# call SOURCE ACTION METHOD PATH [BODY]
call() {
	local src=$1 act=$2 m=$3 p=$4 body=${5:-}
	local base=$DEMO hdr=()
	case $src in
	iap) base=$PROXY hdr=(-H "$(as alice@example.com)") ;;
	code) hdr=(-H "Authorization: Bearer $CODE") ;;
	loop) hdr=() ;;
	agent) hdr=(-H "Authorization: Bearer $CODE" -H "X-Grain-Acting-Task: $AGENT_TASK" -H "X-Grain-Acting-Reason: walkthrough") ;;
	ext) hdr=(-H "Authorization: Bearer $EXT" -H "X-Grain-Acting-Reason: walkthrough") ;;
	esac
	local args=(-s -o "$OUT/last.json" -w '%{http_code}' -X "$m" "${hdr[@]}" -H 'Content-Type: application/json')
	[[ -n $body ]] && args+=(-d "$body")
	local st
	st=$(curl "${args[@]}" "$base$p")
	local err=""
	[[ $st -ge 400 ]] && err=$(jq -r '.error // empty' "$OUT/last.json" 2>/dev/null | tr '\t\n' '  ' | cut -c1-200)
	printf '%s\t%s\t%s\t%s\n' "$src" "$act" "$st" "$err" >> "$LOG"
	cat "$OUT/last.json"
}

# The external agent API is off by default (Settings → externalAgentApi).
call code setup-ext-api PUT /api/settings '{"externalAgentApi":true}' > /dev/null

# The agent's own task, and the gma_ token, made with the access code.
AGENT_TASK=$(call code setup-agent-task POST /api/tasks '{"title":"agent host task","repo":"acme/widgets"}' | jq -r .id)
for g in api:tasks:write api:tasks:read api:memory:write api:release-notes:write api:tasks:admin api:repos:write api:dispatch:write; do
	call code "setup-grant-$g" POST "/api/tasks/$AGENT_TASK/capabilities" "{\"id\":\"$g\",\"attach\":true}" > /dev/null
done
EXT=$(call code setup-ext-token POST /api/external-agents/tokens \
	'{"name":"ext1","scopes":["tasks:write","tasks:read","tasks:admin","memory:write","release-notes:write","repos:write","dispatch:write"]}' | jq -r .token)
echo "agent task $AGENT_TASK, ext token ${EXT:0:8}…"

future=$(date -u -d '+3 days' +%Y-%m-%dT%H:%M:%SZ)
for src in iap code loop agent ext; do
	# A person files with auto-merge on; an agent may not (it is refused), so it files without.
	am=true
	[[ $src == agent || $src == ext ]] && am=false
	id=$(call "$src" file POST /api/tasks "{\"title\":\"filed by $src\",\"repo\":\"acme/widgets\",\"autoMerge\":$am}" | jq -r '.id // empty')
	echo "$src filed task ${id:-<none>}"
	echo "$src	$id" >> "$OUT/tasks.tsv"
	[[ -z $id ]] && id=$AGENT_TASK
	call "$src" comment POST "/api/tasks/$id/comments" "{\"body\":\"a comment from $src\"}" > /dev/null
	call "$src" approve POST "/api/tasks/$id/approve" '{}' > /dev/null
	call "$src" withdraw-approval POST "/api/tasks/$id/withdraw-approval" '{}' > /dev/null
	call "$src" auto-merge-off PATCH "/api/tasks/$id" '{"autoMerge":false}' > /dev/null
	call "$src" defer POST "/api/tasks/$id/defer" "{\"deferredUntil\":\"$future\"}" > /dev/null
	call "$src" undefer POST "/api/tasks/$id/undefer" '{}' > /dev/null
	call "$src" depends-on POST "/api/tasks/$id/depends-on" "{\"id\":\"$AGENT_TASK\",\"attach\":true}" > /dev/null
	call "$src" depends-off POST "/api/tasks/$id/depends-on" "{\"id\":\"$AGENT_TASK\",\"attach\":false}" > /dev/null
	call "$src" pause POST "/api/tasks/$id/pause" '{"reason":"walkthrough pause"}' > /dev/null
	call "$src" resume POST "/api/tasks/$id/resume" '{}' > /dev/null
	call "$src" auto-merge-on PATCH "/api/tasks/$id" '{"autoMerge":true}' > /dev/null
	# Item 5: a forged author on memory and release-note writes.
	call "$src" memory PUT /api/memory/documents "{\"name\":\"plain-$src\",\"summary\":\"s\",\"text\":\"doc by $src\"}" > /dev/null
	call "$src" memory-forged PUT /api/memory "{\"text\":\"memory by $src\",\"author\":\"bob@example.com\",\"authorKind\":\"human\"}" > /dev/null
	call "$src" memory-doc-forged PUT /api/memory/documents "{\"name\":\"doc-$src\",\"summary\":\"s\",\"text\":\"doc by $src\",\"author\":\"bob@example.com\",\"authorKind\":\"human\"}" > /dev/null
	call "$src" release-note-forged PUT /api/release-notes "{\"title\":\"notes by $src\",\"text\":\"t\",\"author\":\"bob@example.com\",\"authorKind\":\"human\"}" > /dev/null
	call "$src" repo-settings PUT /api/repos/acme/widgets/settings "{\"promptExtension\":\"set by $src\"}" > /dev/null
	call "$src" pipeline-pause POST /api/pipeline-pause "{\"reason\":\"paused by $src\"}" > /dev/null
	call "$src" pipeline-resume DELETE /api/pipeline-pause > /dev/null
	call "$src" close POST "/api/tasks/$id/close" '{"reason":"done"}' > /dev/null
done

dump() { sqlite3 -json "$DB" "$2" > "$OUT/dump-$1.json"; }
dump task_event "select id, task_id, kind, actor_kind, actor_id, actor_source, behalf_kind, behalf_id, behalf_source, reason, detail from task_event order by id"
dump task_comment "select id, task_id, author_kind, author_id, author_source, author_behalf_kind, author_behalf_id, author_behalf_source, body from task_comment order by id"
dump config_audit "select * from config_audit order by id"
dump task "select id, title, origin_actor_kind, origin_actor_id, origin_actor_source, origin_behalf_kind, origin_behalf_id, origin_behalf_source, approval_actor_kind, approval_actor_id, approval_actor_source, auto_merge from task order by cast(id as int)"
dump memory "select * from memory"
dump memory_document "select * from memory_document"
dump release_note "select * from release_note"
dump task_pause "select * from task_pause"
dump pipeline_pause "select * from pipeline_pause"
dump external_agent_token "select id, name, prefix, scopes, created_by, created_at from external_agent_token"
echo "dumps in $OUT"
