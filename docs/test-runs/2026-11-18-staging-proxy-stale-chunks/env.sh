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

# Environment for docs/test-plans/2026-11-18.md §7. Reuses the slow
# identity-verifier harness of docs/test-runs/2026-11-16-identity-sessions/
# (daemon 4291 behind the HTTPS identity-aware proxy 4292, control 4293,
# ghsim 4294) and swaps its staging echo for this directory's hostile
# staging_echo.py, served as persistent sandbox "preview" on 4281.
#
#   source env.sh
#   up_walk [sqlite|postgres]      # build, seed, start, wait for /staging/preview
#   start_walk_daemon "$WORK_DIR"  # restart the daemon on the same data

set -euo pipefail

STALE_HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=../2026-11-16-identity-sessions/env.sh
source "$STALE_HERE/../2026-11-16-identity-sessions/env.sh"

install_hostile_echo() {
	local work=${1:-$WORK_DIR}
	local tmp
	tmp=$(mktemp -d)
	git clone -q "$work/widgets.git" "$tmp/w"
	cp "$STALE_HERE/staging_echo.py" "$tmp/w/staging_echo.py"
	git -C "$tmp/w" add staging_echo.py
	git -C "$tmp/w" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: hostile staging echo for §7"
	git -C "$tmp/w" push -q origin main
	rm -rf "$tmp"
}

wait_staging() {
	for _ in $(seq 300); do
		if curl -sk "$PROXY_HTTPS/staging/preview/echo" -o /dev/null -w '%{http_code}' | grep -q 200; then
			return 0
		fi
		sleep 0.5
	done
	echo "staging preview never answered; tail of $WORK_DIR/daemon.log:" >&2
	tail -n 40 "$WORK_DIR/daemon.log" >&2
	return 1
}

up_walk() {
	STORE=${1:-sqlite}
	export STORE
	init_walk_env "/tmp/walk-stale-$STORE"
	install_hostile_echo "$WORK_DIR"
	start_walk_ghsim "$WORK_DIR"
	start_walk_daemon "$WORK_DIR"
	seed_walk_data "$WORK_DIR"
	# The persistent sandbox row was written behind the daemon's back:
	# restart so it is picked up and started.
	start_walk_daemon "$WORK_DIR"
	wait_staging
}
