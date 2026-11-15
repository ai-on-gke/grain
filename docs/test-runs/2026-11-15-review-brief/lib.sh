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

# Helpers for run_walkthrough.sh: file a reviewed task whose fake agent run
# pushes a branch touching a trust-boundary path (pkg/ci/) and docs/, wait
# for the reviews grain files for its pull request, and dump each review's
# description and prompt. Source env.sh first.

# file_reviewed_task TITLE BODY_FILE DESIGNS_JSON [SPEC] [DESIGN_DOC]
# Files TITLE unapproved (body read raw from BODY_FILE, so invalid UTF-8
# reaches the API as bytes), scripts its fake agent run to push a commit,
# links any implements-spec / design-doc targets in the store, then approves
# it. Prints the task id.
file_reviewed_task() {
	local title=$1 body_file=$2 designs=${3:-[]} spec=${4:-} design_doc=${5:-}
	local req="$WORK_DIR/req-$RANDOM.json"
	python3 -B - "$title" "$body_file" "$designs" >"$req" <<'PY'
import json, sys
title, body_file, designs = sys.argv[1], sys.argv[2], json.loads(sys.argv[3])
body = open(body_file, "rb").read() if body_file else b""
head = {"title": title, "repo": "acme/widgets", "approved": False}
if designs:
    head["designs"] = designs
enc = json.dumps(head)[:-1].encode()
# The description is spliced in byte for byte: json.dumps would escape
# invalid UTF-8 away before the server ever saw it.
esc = body.replace(b"\\", b"\\\\").replace(b'"', b'\\"').replace(b"\n", b"\\n").replace(b"\t", b"\\t").replace(b"\r", b"\\r")
sys.stdout.buffer.write(enc + b', "description": "' + esc + b'"}')
PY
	local id
	id=$(curl -fsS -X POST -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" \
		--data-binary "@$req" "$DAEMON/api/tasks" | jq -r '.id')
	local slug="t${id}"
	cat >"$WORK_DIR/action-${id}.json" <<EOF
{"kind": "mcp_sequence", "calls": [
  {"tool": "run_command", "args": {"description": "pushing the walkthrough change",
    "command": "cd work && mkdir -p pkg/ci docs && echo 'package ci // ${slug}' > pkg/ci/${slug}.go && echo '# ${slug}' > docs/${slug}.md && git add -A && git commit -qm 'feat: walkthrough change ${slug}' -m 'Walkthrough commit for the review brief walk.' && git push -q origin HEAD"}},
  {"tool": "comment_on_issue", "args": {"comment": "pushed the walkthrough change"}}
]}
EOF
	if [[ -n "$spec" ]]; then
		store_exec "INSERT INTO task_link (task_id, kind, target, blocks) VALUES ('$id', 'implements-spec', '$spec', false);" >/dev/null
	fi
	if [[ -n "$design_doc" ]]; then
		store_exec "INSERT INTO task_link (task_id, kind, target, blocks) VALUES ('$id', 'design-doc', '$design_doc', false);" >/dev/null
	fi
	api POST "/api/tasks/$id/approve" '{}' >/dev/null
	echo "$id"
}

# reviews_of ID -- ids of every review task about task ID (title names it).
reviews_of() {
	local id=$1 title
	title=$(api GET "/api/tasks/$id" | jq -r '.title')
	api GET "/api/tasks?limit=500" | jq -r --arg t "$title" --arg id "$id" \
		'.[] | select(.id != $id and (.title | contains($t)) and (.title | test("review"; "i"))) | .id' | sort -n
}

# wait_for TIMEOUT_SECONDS CMD... -- polls CMD until it succeeds.
wait_for() {
	local timeout=$1
	shift
	local deadline=$((SECONDS + timeout))
	while ((SECONDS < deadline)); do
		if "$@"; then
			return 0
		fi
		sleep 1
	done
	echo "timed out after ${timeout}s waiting for: $*" >&2
	return 1
}

# dump_task ID LABEL -- writes the task's description and title.
dump_task() {
	local id=$1 label=$2
	api GET "/api/tasks/$id" >"$WORK_DIR/captures/${label}.json"
	jq -j '.description // .body // ""' "$WORK_DIR/captures/${label}.json" >"$WORK_DIR/captures/${label}.body.md"
}
