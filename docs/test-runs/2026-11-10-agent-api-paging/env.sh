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

# Harness for docs/test-plans/2026-11-10.md §6: builds ghsim and seed,
# populates >250 git branches, >250 CI workflows, >250 host capability
# entries, >250 agent models, >250 allowed upgrade targets, and >250 rows
# per database collection, then starts a real `grain daemon -sandbox-port-base=0`
# over SQLite (127.0.0.1:4281) or PostgreSQL (127.0.0.1:4282).

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
GHSIM_ADDR="127.0.0.1:4283"

build_helpers() {
	local bindir=$1
	mkdir -p "$bindir"
	(cd "$ROOT" && go build -o "$bindir/ghsim" "$HERE/ghsim.go")
	(cd "$ROOT" && go build -o "$bindir/seed" "$HERE/seed.go")
}

prepare_git_and_stubs() {
	local work=$1
	mkdir -p "$work/bin" "$work/home" "$work/upgrade-src"

	# 1. Bare repository acme/widgets on trunk with 265 branches and 265 .grain/workflows/*.yml files.
	git init -q --bare -b trunk "$work/widgets.git"
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b trunk
	mkdir -p "$tmp/.grain/workflows"
	echo "# acme/widgets" >"$tmp/README.md"
	for i in $(seq -w 0 264); do
		cat >"$tmp/.grain/workflows/wf-$i.yml" <<EOF
name: Workflow $i
on:
  workflow_dispatch: {}
  push: {}
jobs:
  verify:
    steps:
      - name: Check $i
        run: echo ok-$i
EOF
	done
	git -C "$tmp" add .
	git -C "$tmp" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed workflows"
	local head_sha
	head_sha=$(git -C "$tmp" rev-parse HEAD)
	git -C "$tmp" push -q "$work/widgets.git" trunk
	# Create 265 branch refs directly in bare repo for fast seeding.
	for i in $(seq -w 0 264); do
		printf "%s\n" "$head_sha" >"$work/widgets.git/refs/heads/feat-branch-$i"
	done
	git -C "$work/widgets.git" pack-refs --all
	rm -rf "$tmp"

	# 2. Fake agy binary listing 265 models for GET /api/agent-models.
	cat >"$work/bin/fake-agy" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "--help" ]]; then
	echo "  --effort string   Reasoning effort (low|medium|high)"
	exit 0
fi
if [[ "${1:-}" == "models" ]]; then
	for i in $(seq -w 0 264); do
		eff="high"
		if (( 10#$i % 3 == 1 )); then eff="medium"; fi
		if (( 10#$i % 3 == 2 )); then eff="low"; fi
		printf "gemini-4.%s-pro-%s\tGemini 4.%s Pro (%s)\n" "$i" "$eff" "$i" "$eff"
	done
	exit 0
fi
exit 0
EOF
	chmod +x "$work/bin/fake-agy"

	# 3. Root-owned hostd allowlist with 265 capability entries + live grain hostd on unix socket.
	local allowlist="$work/hostd-allowlist.yaml"
	{
		echo "capabilities:"
		for i in $(seq -w 0 264); do
			printf "  host-cap-%s:\n    command: /bin/true\n" "$i"
		done
	} >"$allowlist"
	sudo chown root:root "$allowlist"
	sudo chmod 0644 "$allowlist"

	# 4. Upgrade source checkout directory + comma-separated list of 265 allowed branches.
	git -C "$work/upgrade-src" init -q -b main
	echo "seed" >"$work/upgrade-src/README"
	git -C "$work/upgrade-src" add README
	git -C "$work/upgrade-src" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: init"
	local branches=()
	for i in $(seq -w 0 264); do
		branches+=("release-track-$i")
	done
	(IFS=,; echo "${branches[*]}") >"$work/upgrade-branches.txt"
}

start_paging_env() {
	local driver=${1:-sqlite}
	local work=${2:-/var/tmp/grain-paging-$driver}
	local port="4281"
	if [[ "$driver" == "postgres" ]]; then
		port="4282"
	fi
	local daemon_url="http://127.0.0.1:$port"

	stop_paging_env "$work" || true
	sudo rm -rf "$work"
	mkdir -p "$work/data" "$work/sbx" "$work/bin" "$work/home"

	build_helpers "$work/bin"
	prepare_git_and_stubs "$work"

	export HOME="$work/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0
	unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN

	"$work/bin/ghsim" -addr "$GHSIM_ADDR" -bare "$work/widgets.git" -token "tok-walkthrough" >"$work/ghsim.log" 2>&1 &
	echo $! >"$work/ghsim.pid"

	"$GRAIN" hostd -allowlist "$work/hostd-allowlist.yaml" -socket "$work/hostd.sock" -state-dir "$work/hostd-state" >"$work/hostd.log" 2>&1 &
	echo $! >"$work/hostd.pid"
	for _ in $(seq 100); do
		[[ -S "$work/hostd.sock" ]] && break
		sleep 0.05
	done

	local db_flags=(-db-driver "$driver")
	local seed_db_flags=(-db-driver "$driver")
	if [[ "$driver" == "postgres" ]]; then
		: "${PAGING_PG_DSN:?PAGING_PG_DSN must be set for postgres mode}"
		db_flags+=(-db-dsn "$PAGING_PG_DSN")
		seed_db_flags+=(-db-dsn "$PAGING_PG_DSN")
	fi

	"$work/bin/seed" -data-dir "$work/data" "${seed_db_flags[@]}" \
		-gchat-url "http://$GHSIM_ADDR/mcp/gchat" \
		-tokens-out "$work/tokens.json" \
		-count 260 >"$work/seed.log" 2>&1

	echo "fake-gemini-key" >"$work/gemini.key"
	echo "fake-claude-token" >"$work/claude.tok"
	local upg_branches
	upg_branches=$(cat "$work/upgrade-branches.txt")

	# Pause pipeline dispatch initially via poll-interval 1h so seeded queued tasks stay predictable during static walks.
	"$GRAIN" daemon \
		-data-dir "$work/data" \
		"${db_flags[@]}" \
		-ui-addr "127.0.0.1:$port" \
		-sandbox-port-base=0 \
		-host-sandboxes \
		-sandbox-dir "$work/sbx" \
		-max-ci-jobs 0 \
		-poll-interval 1h \
		-as owner@example.com \
		-github-host "$GHSIM_ADDR" \
		-github-insecure-http \
		-agy-path "$work/bin/fake-agy" \
		-agy-run container \
		-gemini-api-key-file "$work/gemini.key" \
		-claude-oauth-token-file "$work/claude.tok" \
		-hostd-socket "$work/hostd.sock" \
		-upgrade-src-dir "$work/upgrade-src" \
		-upgrade-install-path "$work/bin/grain-upgraded" \
		-upgrade-allowed-branches "$upg_branches" \
		${DAEMON_EXTRA_FLAGS:-} >"$work/daemon.log" 2>&1 &
	echo $! >"$work/daemon.pid"

	for _ in $(seq 300); do
		if [[ -f "$work/data/local-api-token" ]] && [[ $(curl -s -o /dev/null -w '%{http_code}' "$daemon_url/" || true) != "000" ]]; then
			break
		fi
		if ! kill -0 "$(cat "$work/daemon.pid")" 2>/dev/null; then
			cat "$work/daemon.log" >&2
			return 1
		fi
		sleep 0.1
	done

	export PAGING_WORK="$work"
	export PAGING_DAEMON="$daemon_url"
	export PAGING_CODE
	PAGING_CODE=$(tr -d '[:space:]' <"$work/data/local-api-token")
}

stop_paging_env() {
	local work=${1:-${PAGING_WORK:-}}
	[[ -n "$work" && -d "$work" ]] || return 0
	local p pid
	for p in daemon hostd ghsim; do
		[[ -f "$work/$p.pid" ]] || continue
		pid=$(cat "$work/$p.pid")
		kill "$pid" 2>/dev/null || true
		for _ in $(seq 50); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
	done
}
