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

# Environment for docs/test-plans/2026-11-11.md §3: real `grain daemon`s
# (SQLite, -host-sandboxes, -sandbox-port-base=0) against ghsim.go
# (acme/widgets over HTTP, with App token minting) and kmsfakesrv.go
# (pkg/secrets/kmsfake as a server). Only Cloud KMS is stubbed.
#
#   source env.sh
#   start_sims                       # ghsim + kmsfakesrv, bare acme/widgets
#   start_daemon BIN [VAR=V ...]     # a daemon on $WORK/data
#   stop_daemon
#   api METHOD PATH [BODY]           # curl with the local API token
#   run_task TITLE PLAN_JSON         # file a run for fake-agy.sh; prints its id
#   stop_all
#
# Secret values are generated into $WORK/values (owner-only) and never
# printed; checks compare digests.

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
WORK=${WORK:-/var/tmp/ss/walk}
HEAD_BIN=${HEAD_BIN:-/home/debian/bin/grain}
OLD_BIN=${OLD_BIN:-/home/debian/bin/grain-old}
PORT=${PORT:-4271}
API="http://127.0.0.1:$PORT"
GHSIM=127.0.0.1:4274
KMS_URL=http://127.0.0.1:4275/v1/
KMS_CTL=http://127.0.0.1:4276
KMS_KEY=projects/demo-project/locations/us/keyRings/grain-walkthrough-keyring/cryptoKeys/grain-secrets-key-with-a-deliberately-long-name

export GOMODCACHE="${GOMODCACHE:-$(go env GOMODCACHE)}" GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0 WALK_DIR="$WORK/walk"
export GRAIN_KMS_ENDPOINT="$KMS_URL" GRAIN_KMS_TOKEN=fake-kms-token
# The scripted agent calls grant tools over MCP, not the in-sandbox CLI.
export GRAIN_MCP_SURFACE=full
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN GOOGLE_APPLICATION_CREDENTIALS

start_sims() {
	mkdir -p "$WORK"/{bin,git/acme,home,scratch,walk/runs,values}
	chmod 700 "$WORK/values"
	(cd "$ROOT" && go build -o "$WORK/bin/ghsim" "$HERE/ghsim.go" && go build -o "$WORK/bin/kmsfakesrv" "$HERE/kmsfakesrv.go" &&
		go build -o "$WORK/bin/storedigest" "$HERE/storedigest.go") || return 2
	if [[ ! -d "$WORK/git/acme/widgets.git" ]]; then
		git init -q --bare -b main "$WORK/git/acme/widgets.git"
		git -C "$WORK/git/acme/widgets.git" config http.receivepack true
		local tmp
		tmp=$(mktemp -d)
		git -C "$tmp" init -q -b main
		echo "# acme/widgets" >"$tmp/README.md"
		git -C "$tmp" add README.md
		git -C "$tmp" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed widgets"
		git -C "$tmp" push -q "$WORK/git/acme/widgets.git" main
		rm -rf "$tmp"
	fi
	"$WORK/bin/ghsim" -addr "$GHSIM" -git-root "$WORK/git" >"$WORK/ghsim.log" 2>&1 &
	echo $! >"$WORK/ghsim.pid"
	"$WORK/bin/kmsfakesrv" >"$WORK/kms.log" 2>&1 &
	echo $! >"$WORK/kms.pid"
	sleep 1
}

# start_daemon BIN [VAR=value ...] starts a daemon on $WORK/data and waits
# for its API.
start_daemon() {
	local bin=$1
	shift
	# shellcheck disable=SC2086
	env "$@" "$bin" daemon -data-dir "$WORK/data" -ui-addr "127.0.0.1:$PORT" -sandbox-port-base=0 \
		-host-sandboxes -sandbox-dir "$WORK/scratch" -sandbox-disk-floor-share 0 -sandbox-disk-floor-min-mb 0 \
		-ci=false -poll-interval 1s -as owner@example.com \
		-repo acme/widgets -default-target-repo acme/widgets \
		-github-host "$GHSIM" -github-insecure-http \
		${DAEMON_EXTRA_FLAGS:-} >>"$WORK/daemon.log" 2>&1 </dev/null &
	echo $! >"$WORK/daemon.pid"
	for _ in $(seq 300); do
		if [[ -s "$WORK/data/local-api-token" ]] && curl -fs -o /dev/null -H "Authorization: Bearer $(tr -d '[:space:]' <"$WORK/data/local-api-token")" "$API/api/config"; then
			return 0
		fi
		kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null || { echo "daemon exited" >&2; return 1; }
		sleep 0.2
	done
	return 1
}

stop_daemon() {
	[[ -f "$WORK/daemon.pid" ]] || return 0
	local pid
	pid=$(cat "$WORK/daemon.pid")
	kill "$pid" 2>/dev/null || true
	for _ in $(seq 100); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
	kill -9 "$pid" 2>/dev/null || true
	rm -f "$WORK/daemon.pid"
}

stop_all() {
	stop_daemon
	local p
	for p in ghsim kms; do
		[[ -f "$WORK/$p.pid" ]] && kill "$(cat "$WORK/$p.pid")" 2>/dev/null || true
		rm -f "$WORK/$p.pid"
	done
	return 0
}

api() {
	local method=$1 path=$2 body=${3:-}
	local tok
	tok=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	local args=(-sS -X "$method" -H "Authorization: Bearer $tok")
	[[ -n "$body" ]] && args+=(-H "Content-Type: application/json" --data "$body")
	curl "${args[@]}" "$API$path"
}

# setup_agent points the deployment at fake-agy.sh.
setup_agent() {
	api PUT /api/config "{\"agentBackends\":[{\"id\":\"ag-walk\",\"name\":\"Walk Agy\",\"framework\":\"antigravity\",\"cliPath\":\"$HERE/fake-agy.sh\",\"models\":[\"fake-model\"]}]}" >/dev/null
	api PUT /api/agent-backends/ag-walk/credential '{"value":"walk-fake-agent-key"}' >/dev/null
}

# file_task TITLE PLAN_JSON [GRANT] files a run for fake-agy.sh: unapproved,
# with its plan written to plan-<id>.json and GRANT attached by the owner,
# then approved. Prints the id.
file_task() {
	local id
	id=$(api POST /api/tasks "$(jq -n --arg t "$1" '{title:$t,description:"walkthrough",repo:"acme/widgets",approved:false}')" | jq -r .id)
	printf '%s' "$2" >"$WORK/walk/plan-$id.json"
	rm -f "$WORK/walk/runs/$id.json"
	[[ -n ${3:-} ]] && api POST "/api/tasks/$id/capabilities" "$(jq -n --arg g "$3" '{grant:$g,attach:true}')" >/dev/null
	api POST "/api/tasks/$id/approve" '{}' >/dev/null
	echo "$id"
}

# run_task TITLE PLAN_JSON [GRANT]: file_task, then wait for the hook's
# result file. Prints the id.
run_task() {
	local id
	id=$(file_task "$@")
	for _ in $(seq 120); do [[ -f "$WORK/walk/runs/$id.json" ]] && break; sleep 1; done
	echo "$id"
}

# digest FILE prints a short sha256 of a file's bytes.
digest() { sha256sum "$1" | cut -c1-12; }

# tree lists the credential files under the data dir: mode, size, path.
tree() {
	(cd "$WORK/data" && find . -maxdepth 3 \( -path ./store -o -path ./state -o -path ./state-repo -o -path ./secrets-repo \) -prune -o -type f \
		\( -name '*.token*' -o -name '*.app.json*' -o -name '*.ssh.json*' -o -name '*-ca.pem*' -o -name 'release-approval-key*' \
		-o -name '*repo-token*' -o -name 'secrets.key*' \) -printf '%m %6s %p\n' | sort -k3)
}

# start_daemon_unit BIN starts the daemon as the system unit
# grain-walk.service (running as this user, in group systemd-journal), so
# read_grain_log's "daemon" source has a journal to read: the daemon names
# the unit it runs under (systemlog.OwnUnit). Its log is the unit's
# journal, not $WORK/daemon.log. A line appended to
# $WORK/journal-inject.txt is echoed onto the unit's own stdout by a tail
# started beside the daemon, so it lands in the same journal under the
# unit -- the stand-in for the daemon logging a credential by accident.
start_daemon_unit() {
	local bin=$1
	stop_daemon_unit
	local envs=() v
	for v in HOME GCE_METADATA_HOST GRAIN_SANDBOX_CLI GRAIN_MCP_SURFACE WALK_DIR GRAIN_KMS_ENDPOINT GRAIN_KMS_TOKEN GOMODCACHE GOCACHE PATH; do
		envs+=(--setenv="$v=${!v}")
	done
	sudo systemd-run --quiet --unit=grain-walk --uid="$(id -u)" --gid="$(id -g)" -p SupplementaryGroups=systemd-journal -p KillMode=process \
		"${envs[@]}" bash -c 'tail -n0 -F "$0" & exec "$@"' "$WORK/journal-inject.txt" "$bin" daemon -data-dir "$WORK/data" -ui-addr "127.0.0.1:$PORT" -sandbox-port-base=0 \
		-host-sandboxes -sandbox-dir "$WORK/scratch" -sandbox-disk-floor-share 0 -sandbox-disk-floor-min-mb 0 \
		-ci=false -poll-interval 1s -as owner@example.com \
		-repo acme/widgets -default-target-repo acme/widgets \
		-github-host "$GHSIM" -github-insecure-http
	for _ in $(seq 300); do
		curl -fs -o /dev/null -H "Authorization: Bearer $(tr -d '[:space:]' <"$WORK/data/local-api-token" 2>/dev/null)" "$API/api/config" && return 0
		sleep 0.2
	done
	return 1
}

# stop_daemon_unit stops the daemon. KillMode=process leaves a run that
# outlives it running (item 6's "daemon stopped mid-run"); the tail is
# killed, and the unit is waited out so the next start can reuse its name.
stop_daemon_unit() {
	sudo systemctl stop grain-walk.service 2>/dev/null || true
	pkill -f "tail -n0 -F $WORK/journal-inject.txt" 2>/dev/null || true
	for _ in $(seq 100); do
		systemctl is-active --quiet grain-walk.service || break
		sleep 0.1
	done
	sudo systemctl reset-failed grain-walk.service 2>/dev/null || true
}
