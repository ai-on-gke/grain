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

# docs/test-plans/2026-11-11.md §12: a real `grain daemon` over SQLite with
# the GitHub simulator serving acme/widgets, a repository carrying 60 skill
# directories across .claude/skills and skills/ (one malformed, one symlink
# escaping the repository, one named like the config-map server's built-in
# "bootstrap" skill), plus folders the precedence walk points layers at.
#
#   bash env.sh start   # builds helpers, seeds, starts ghsim + daemon
#   bash env.sh stop
#
# Then: python3 walk.py --work /var/tmp/grain-skills
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
WORK=${WORK:-/var/tmp/grain-skills}
GHSIM_ADDR=127.0.0.1:4283
PORT=4281

skill() { # skill <dir> <name-or-empty> <description>
	mkdir -p "$1"
	if [[ -n "$2" ]]; then
		printf -- '---\nname: %s\ndescription: %s\n---\n# %s\n\nBody text that never enters the prompt.\n' "$2" "$3" "$2" >"$1/SKILL.md"
	else
		printf -- '# Untitled\n\n%s\n' "$3" >"$1/SKILL.md"
	fi
}

make_repo() {
	git init -q --bare -b main "$WORK/git/acme/widgets.git"
	git -C "$WORK/git/acme/widgets.git" config http.receivepack true
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	echo "# acme/widgets" >"$tmp/README.md"
	local long
	long=$(printf 'Long description %.0s' $(seq 1 45)) # ~810 chars, clipped to 500
	skill "$tmp/.claude/skills/claude-00" claude-00 "$long"
	for i in $(seq -w 1 28); do
		skill "$tmp/.claude/skills/claude-$i" "claude-$i" "Claude-folder skill $i for acme widgets."
	done
	mkdir -p "$tmp/.claude/skills/broken"
	printf -- '---\nname: broken\ndescription: never closed\n' >"$tmp/.claude/skills/broken/SKILL.md"
	for i in $(seq -w 0 27); do
		skill "$tmp/skills/skill-$i" "" "Root-folder skill $i, described by its first paragraph."
	done
	skill "$tmp/skills/bootstrap" bootstrap "Repository skill that clashes with the config-map server's bootstrap skill."
	ln -s /etc "$tmp/skills/escape"
	for i in $(seq -w 0 39); do
		skill "$tmp/docs/long-skills/long-$i" "long-$i" "$(printf 'Wordy %s %.0s' "$i" $(seq 1 60))"
	done
	for layer in deployment repo declared template task; do
		skill "$tmp/layer/$layer/from-$layer" "from-$layer" "Proves the $layer layer chose this folder."
	done
	git -C "$tmp" add -A
	git -C "$tmp" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed skills"
	git -C "$tmp" push -q "$WORK/git/acme/widgets.git" main
	rm -rf "$tmp"
}

start() {
	stop || true
	sudo rm -rf "$WORK"
	mkdir -p "$WORK/data" "$WORK/sbx" "$WORK/bin" "$WORK/home" "$WORK/prompts" "$WORK/git/acme"
	(cd "$ROOT" && go build -o "$WORK/bin/ghsim" "$HERE/ghsim.go" && go build -o "$WORK/bin/seed" "$HERE/seed.go")
	make_repo
	"$WORK/bin/seed" -data-dir "$WORK/data" >"$WORK/seed.log" 2>&1
	cat >"$WORK/bin/fake-agy" <<EOF
#!/bin/sh
exec python3 $HERE/fake_agent.py --out $WORK/prompts "\$@"
EOF
	chmod +x "$WORK/bin/fake-agy"

	echo fake-gemini-key >"$WORK/gemini.key"
	launch
}

launch() { # (re)start ghsim + daemon over the existing $WORK
	export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0
	unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN
	setsid "$WORK/bin/ghsim" -addr "$GHSIM_ADDR" -bare "$WORK/git/acme/widgets.git" >>"$WORK/ghsim.log" 2>&1 < /dev/null &
	echo $! >"$WORK/ghsim.pid"
	setsid "$GRAIN" daemon \
		-data-dir "$WORK/data" -db-driver sqlite \
		-ui-addr "127.0.0.1:$PORT" \
		-sandbox-port-base=0 -host-sandboxes -sandbox-dir "$WORK/sbx" \
		-max-ci-jobs 0 -poll-interval 3s \
		-as owner@example.com \
		-github-host "$GHSIM_ADDR" -github-insecure-http \
		-agy-path "$WORK/bin/fake-agy" -agy-run container \
		-gemini-api-key-file "$WORK/gemini.key" \
		${DAEMON_EXTRA_FLAGS:-} >>"$WORK/daemon.log" 2>&1 < /dev/null &
	echo $! >"$WORK/daemon.pid"
	for _ in $(seq 300); do
		if [[ -f "$WORK/data/local-api-token" ]] && [[ $(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$PORT/" || true) != "000" ]]; then
			echo "daemon up on http://127.0.0.1:$PORT"
			return 0
		fi
		kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null || { tail -n 40 "$WORK/daemon.log" >&2; return 1; }
		sleep 0.2
	done
	return 1
}

restart() {
	stop
	launch
}

stop() {
	[[ -d "$WORK" ]] || return 0
	for p in daemon ghsim; do
		[[ -f "$WORK/$p.pid" ]] || continue
		kill "$(cat "$WORK/$p.pid")" 2>/dev/null || true
	done
	sleep 1
}

"${1:-start}"
