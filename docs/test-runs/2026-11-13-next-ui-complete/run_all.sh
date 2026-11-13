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

# Runs the complete docs/test-plans/2026-11-13.md §1 walkthrough:
#   1. Real `grain daemon -sandbox-port-base=0` (SQLite) for items 1, 2, 5, 6
#      (and real-daemon CLI catalog verification for item 4).
#   2. `grain demo -github-sim -fake-ci` for items 3, 4, 7, 8, and 9.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}
export PYTHONDONTWRITEBYTECODE=1

# shellcheck source=env.sh
source "$HERE/env.sh"

WORK=${WORK_DIR:-/tmp/walk-next-ui}
trap 'stop_env "$WORK"; rm -rf "$WORK"' EXIT

echo "=== Part 1: Real grain daemon (SQLite) — §1 items 1, 2, 5, 6 ==="
init_env "$WORK"
start_ghsim "$WORK"
start_iapsim "$WORK"
start_daemon "$WORK"
seed_daemon

daemon_rc=0
node "$HERE/walk_daemon.mjs" "$WORK/seeded.json" || daemon_rc=$?
stop_env "$WORK"

echo ""
echo "=== Part 2: grain demo -github-sim -fake-ci — §1 items 3, 4, 7, 8, 9 ==="
demo_rc=0
node "$HERE/walk_demo.mjs" "$@" || demo_rc=$?

if [[ $daemon_rc -ne 0 || $demo_rc -ne 0 ]]; then
	echo "Walkthrough exited with failures (daemon_rc=$daemon_rc, demo_rc=$demo_rc)" >&2
	exit 1
fi
echo "All §1 walkthrough checks passed."
