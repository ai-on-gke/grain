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

# Reruns docs/test-runs/2026-11-11-merge-gates.md from the repository root:
#   1. Deterministic orchestrator/ui regression tests;
#   2. Real `grain daemon -sandbox-port-base=0 -host-sandboxes` over SQLite
#      (and PostgreSQL when psql is available), with githubsim, `.grain/triggers`
#      fixtures, grain CI, `grain pr submit` / `grain merge-queue`, and
#      `fake-agy.sh` stubbing only the model provider;
#   3. HTTP override checks and Playwright gate screenshots at 1440x900 and
#      390x844 in light and dark.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
OUT=${OUT:-/tmp/merge-gates}
mkdir -p "$OUT" /home/debian/bin

go test ./pkg/orchestrator -count=1 -v \
  -run 'TestMergeGatesWalkthrough20261111|TestSyncPullRequestsDoesNotForceACommitPushedAfterTheOverride' \
  | grep -v log_test.go | tee "$OUT/orchestrator.log" | tail -5
go test ./pkg/ui -count=1 -run 'TestAGateOverrideForAnEarlierHead|TestAListRowsCIGateMatchesTheTaskPage' | tail -2

(cd ui && npm ci && npm run build) >/dev/null
go build -o /home/debian/bin/grain ./cmd/grain

HEAD_BIN=/home/debian/bin/grain bash docs/test-runs/2026-11-11-merge-gates/live-daemon.sh sqlite | tee "$OUT/live-sqlite.log"
if command -v psql >/dev/null 2>&1; then
  HEAD_BIN=/home/debian/bin/grain bash docs/test-runs/2026-11-11-merge-gates/live-daemon.sh postgres | tee "$OUT/live-postgres.log"
fi

code="merge-gates-$$"
echo "$code" >"$OUT/code.txt"
/home/debian/bin/grain demo -github-sim -fake-ci -review-questions -merge-queue-states -human-review \
  -addr 127.0.0.1:4291 -access-code-file "$OUT/code.txt" >"$OUT/demo.log" 2>&1 &
demo=$!
trap 'kill $demo 2>/dev/null || true' EXIT
sleep 6
(cd ui && GRAIN_URL=http://127.0.0.1:4291 GRAIN_CODE="$code" PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  node ../docs/test-runs/2026-11-11-merge-gates/gates-ui.mjs "$OUT/shots" >/dev/null)
GRAIN_CODE="$code" docs/test-runs/2026-11-11-merge-gates/http-overrides.sh | tee "$OUT/http.log"
