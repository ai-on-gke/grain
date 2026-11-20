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

# Real-daemon environment for docs/test-plans/2026-11-16.md §7: the
# 2026-11-15-resume-later-release harness (real `grain daemon
# -sandbox-port-base=0`, fake agy CLI, GitHub sim with Actions seeding) plus
# a `human: required` path trigger committed to acme/widgets, a second
# repository for a repo-scoped writer to be shut out of, and an access code
# so a browser can sign in by email under an allowlisted role.

OPS_HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-15-resume-later-release/env.sh
source "$OPS_HERE/../2026-11-15-resume-later-release/env.sh"

init_ops_env() {
	init_rl_env "$1"
	local tmp
	tmp=$(mktemp -d)
	git clone -q "$WORK_DIR/widgets.git" "$tmp"
	mkdir -p "$tmp/.grain/triggers"
	# The shape of this repository's own .grain/triggers/grain-declarations.yml.
	cat >"$tmp/.grain/triggers/walk-declarations.yml" <<'YML'
on: pull_request
paths: [".grain/**"]
gate: true
human: required
sensitive: true
YML
	git -C "$tmp" add -A
	git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: declare a human-required path trigger"
	git -C "$tmp" push -q origin main
	rm -rf "$tmp"
}

# grain_as runs the grain CLI against the walkthrough daemon.
grain_as() {
	env -i PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" HOME="$WORK_DIR/home" \
		GRAIN_SERVER="$DAEMON" GRAIN_TOKEN="$CODE" "$GRAIN" "$@"
}

# IAP stand-in (tests/iap/iapsim.go) so a browser context is a named person
# whose allowlisted role the daemon enforces: a request carrying
# x-goog-iap-jwt-assertion minted by $IAP_ISSUER/sign is that person, under
# their role, where the access code alone would make everyone an operator.
IAP_ISSUER=http://127.0.0.1:4302
IAP_AUD=/projects/123/global/backendServices/456
IAP_ISS=https://iap.test.local
IAP_FLAGS=(-ui-iap-audience "$IAP_AUD" -ui-iap-issuer "$IAP_ISS" -ui-iap-keys-url "$IAP_ISSUER/jwks" -allow-unisolated-host-sandboxes)

start_iapsim() {
	local work=${1:-$WORK_DIR}
	# One stand-in serves every walkthrough daemon: its keys are all they read.
	if curl -sf "$IAP_ISSUER/jwks" >/dev/null; then return 0; fi
	(cd "$ROOT" && go build -o "$work/bin/iapsim" ./tests/iap/iapsim.go)
	"$work/bin/iapsim" -issuer 127.0.0.1:4302 -proxy 127.0.0.1:4303 -upstream "$DAEMON" -aud "$IAP_AUD" -iss "$IAP_ISS" >"$work/iapsim.log" 2>&1 &
	echo $! >"$work/iapsim.pid"
	for _ in $(seq 50); do
		curl -sf "$IAP_ISSUER/jwks" >/dev/null && return 0
		sleep 0.1
	done
	echo "iapsim failed to start; see $work/iapsim.log" >&2
	return 1
}

# iap_jwt prints a fresh assertion for email $1.
iap_jwt() { curl -fsS "$IAP_ISSUER/sign?email=$1&aud=$IAP_AUD&iss=$IAP_ISS&exp=6h"; }

# seed_roles allowlists alice@example.com as grain/operator (the admin) and
# wendy@example.com under acme/widgets-writer: tasks and CI of acme/widgets
# only (docs/design/access-rbac.md §1).
seed_roles() {
	api PUT /api/api-scope-sets '{"set":{"name":"acme/widgets-writer","description":"walkthrough writer on acme/widgets only","scopes":["tasks:read","tasks:write","ci:read","sandboxes:read"],"repos":["acme/widgets"]}}' >/dev/null
	grain_as access allow --role grain/operator --reason walkthrough user:alice@example.com
	grain_as access allow --role acme/widgets-writer user:wendy@example.com
}

# park_and_age files one more resume_later(ci: my-branch) task, runs it
# now under the pause, and moves its wait 65 minutes into the past (its
# run, deferral, suspension and comment), past ciWaits' one hour.
park_and_age() {
	cat >"$WORK_DIR/action-default.json" <<'JSON'
[{"kind": "mcp_sequence", "calls": [
   {"tool": "run_command", "args": {"command": "cd work && date +%s%N > walk.txt && git add walk.txt && git commit -qm 'chore: touch walk.txt' -m 'Walkthrough commit.' && git push -q origin HEAD && grain pr open", "description": "pushing the branch and opening its pull request"}},
   {"tool": "resume_later", "args": {"reason": "waiting for CI on my branch", "when": {"ci": "my-branch"}}}]}]
JSON
	local id s f
	id=$(api POST /api/tasks "{\"title\":\"$1\",\"repo\":\"acme/widgets\",\"approved\":true}" | jq -r .id)
	api POST "/api/tasks/$id/run-now" '{}' >/dev/null
	wait_state "$id" deferred 90 >/dev/null
	echo '[{"kind": "nothing_needed", "reason": "walkthrough default completion"}]' >"$WORK_DIR/action-default.json"
	s=$(date -u -d '-66 min' '+%Y-%m-%d %H:%M:%S+00:00')
	f=$(date -u -d '-65 min' '+%Y-%m-%d %H:%M:%S+00:00')
	store_exec "UPDATE task_run SET started_at='$s', finished_at='$f' WHERE task_id='$id' AND finished_at IS NOT NULL; UPDATE task SET deferred_at='$f' WHERE id='$id'; UPDATE task_suspension SET created_at='$f' WHERE task_id='$id'; UPDATE task_comment SET created_at='$f' WHERE task_id='$id' AND body LIKE 'grain: this task''s run stopped with resume_later%';" >/dev/null
	echo "$id"
}
