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

# scripts/iap-e2e/run_test.sh — Self-test for scripts/iap-e2e/run.sh.
#
# Exercises the full 8-case qualification runner, CLI IAP JWT authentication,
# JSON + Markdown report generation, failure exit code handling, and cleanup
# trap locally against a real grain daemon without touching GCP.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
RUN_SH="${SCRIPT_DIR}/run.sh"

TMP_OUT="$(mktemp -d -t grain-iap-e2e-test.XXXXXX)"
trap 'rm -rf "$TMP_OUT"' EXIT

echo "1. Checking --help and unknown-flag rejection..."
"$RUN_SH" --help | grep -q "Cloud Run Direct IAP"
if "$RUN_SH" --definitely-unknown-flag >/dev/null 2>&1; then
  echo "FAIL: expected unknown flag to exit non-zero" >&2
  exit 1
fi

echo "2. Building grain binary once for self-tests..."
GRAIN_BIN="${TMP_OUT}/grain"
(cd "$REPO_ROOT" && go build -o "$GRAIN_BIN" ./cmd/grain)

echo "3. Running full 8-case qualification suite in --local-sim mode..."
PASS_OUT="${TMP_OUT}/pass"
"$RUN_SH" \
  --local-sim \
  --grain-bin "$GRAIN_BIN" \
  --service "sim-iap-e2e" \
  --out "$PASS_OUT"

test -f "${PASS_OUT}/report.json"
test -f "${PASS_OUT}/report.md"

jq -e '.overall == "PASS" and .passed == 8 and .failed == 0 and (.cases | length == 8)' "${PASS_OUT}/report.json" >/dev/null
jq -e '([.cases[].case] == [1,2,3,4,5,6,7,8])' "${PASS_OUT}/report.json" >/dev/null
grep -q "covered by the simulated suite (task 2510)" "${PASS_OUT}/report.md"

echo "4. Verifying non-zero exit status when a case fails..."
FAIL_OUT="${TMP_OUT}/fail"
if GRAIN_IAP_E2E_SIM_FAIL_CASE=4 "$RUN_SH" \
  --local-sim \
  --grain-bin "$GRAIN_BIN" \
  --service "sim-iap-fail" \
  --out "$FAIL_OUT" >/dev/null 2>&1; then
  echo "FAIL: expected run.sh to exit non-zero when Case 4 fails" >&2
  exit 1
fi

jq -e '.overall == "FAIL" and .passed == 7 and .failed == 1' "${FAIL_OUT}/report.json" >/dev/null

echo "PASS: scripts/iap-e2e/run_test.sh verified all cases, reports, and exit codes."
