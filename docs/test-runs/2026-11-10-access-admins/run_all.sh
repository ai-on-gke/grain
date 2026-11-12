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

# Runs the full docs/test-plans/2026-11-10.md §2 walkthrough:
#   1. Daemon with IAP/JWKS + Cloud Identity group stub + GitHub simulator (items 1-5)
#   2. Daemon with IAP/JWKS + group entries and NO group checker (item 2c)
#
#   docs/test-runs/2026-11-10-access-admins/run_all.sh [OUT_DIR]

set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
OUT_DIR=${1:-/var/tmp/access-admins-out}
mkdir -p "$OUT_DIR"

# shellcheck source=env.sh
source "$HERE/env.sh"
trap stop_env EXIT

echo "=== Phase 1: main walkthrough (IAP + Cloud Identity + ghsim) ==="
start_env /var/tmp/access-admins-main on || exit 2
(cd "$ROOT/ui" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
	node "$HERE/walkthrough.mjs" main "$OUT_DIR")
rc1=$?
stop_env

echo "=== Phase 2: no-group-checker daemon (item 2c) ==="
start_env /var/tmp/access-admins-nogroup off || exit 2
(cd "$ROOT/ui" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
	node "$HERE/walkthrough.mjs" nogroup "$OUT_DIR")
rc2=$?
stop_env

exit $(( rc1 != 0 || rc2 != 0 ? 1 : 0 ))
