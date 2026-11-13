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

# Deterministic reproduction & post-fix verification for findings F1, F2, F3
# discovered during the 2026-11-13 §2 QA control plane walkthrough.
# Pass EXPECT_FIXED=0 to record pre-fix breakage without failing exit status;
# default EXPECT_FIXED=1 asserts all three fixes hold.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"

EXPECT_FIXED=${EXPECT_FIXED:-1}
work="/tmp/walk-qa-repro-${STORE:-sqlite}"
trap 'stop_env "$work"' EXIT

init_env "$work" "$GRAIN"
start_ghsim "$work"
start_daemon "$GRAIN" "$work"
sleep 1.0

qa_cli() {
	"$GRAIN" -server "$DAEMON" -token "$CODE" "$@"
}

failures=0
echo "=== [Findings reproduction / store=${STORE:-sqlite} / expect_fixed=$EXPECT_FIXED] ==="

# --- F1: `grain qa enable` clobbering an explicit 0% (shadow-only) rollout after `grain qa disable` ---
qa_cli qa enable -flow deterministic-s3-only -rollout 0 >/dev/null
qa_cli qa disable >/dev/null
after_dis=$(qa_cli --json qa status)
after_reenable=$(qa_cli --json qa enable)
reenabled_pct=$(echo "$after_reenable" | jq -r '.rolloutPct')
echo "  [F1] after disable rolloutPct=$(echo "$after_dis" | jq -r '.rolloutPct'); after bare 'grain qa enable' rolloutPct=$reenabled_pct (want 0)"
if [[ "$reenabled_pct" != "0" ]]; then
	echo "  [F1] REPRODUCED: bare 'grain qa enable' clobbered saved 0% shadow-only rollout to ${reenabled_pct}%"
	failures=$((failures + 1))
fi

# --- F2: Originless child task (`origin_ref = ''`, `proposed-by -> root`) split off from its tree's sticky arm ---
qa_cli qa enable -flow golden-v1-minimal -rollout 30 -epoch 1 >/dev/null
sleep 0.6

bucket_of() {
	python3 - "$1" <<'PY'
import hashlib, struct, sys
d = hashlib.sha256(f"task:{sys.argv[1]}:1".encode()).digest()
print(struct.unpack(">I", d[:4])[0] % 100)
PY
}

root_bucket_id=""
for _ in $(seq 30); do
	cand=$(api POST /api/tasks '{"title":"Legacy root candidate","body":"Root task","repo":"acme/widgets"}' | jq -r '.id')
	if [[ "$(bucket_of "$cand")" -lt 30 ]]; then
		root_bucket_id="$cand"
		break
	fi
done

child_bucket_id=""
for _ in $(seq 30); do
	cand=$(api POST /api/tasks '{"title":"Originless child candidate","body":"Child task filed before origin_ref","repo":"acme/widgets"}' | jq -r '.id')
	if [[ "$(bucket_of "$cand")" -ge 30 ]]; then
		child_bucket_id="$cand"
		break
	fi
done

# Shape child_bucket_id as a pre-origin child task (origin_ref = '', proposed-by -> root_bucket_id)
sql_scalar "UPDATE task SET origin_ref = '' WHERE id IN ('$root_bucket_id', '$child_bucket_id');" >/dev/null
sql_scalar "INSERT INTO task_link (task_id, kind, target, blocks) VALUES ('$child_bucket_id', 'proposed-by', '$root_bucket_id', FALSE);" >/dev/null

tree_origin=$(api GET "/api/tasks/${child_bucket_id}/tree" | jq -r '.origin')
api POST "/api/tasks/${root_bucket_id}/approve" '{}' >/dev/null
api POST "/api/tasks/${child_bucket_id}/approve" '{}' >/dev/null
wait_task_closed "$root_bucket_id" 150
wait_task_closed "$child_bucket_id" 150

PERF_FILE="$work/data/perf/qa-checks.jsonl"
root_arm=$(jq -r --arg tid "$root_bucket_id" 'select(.stage=="S0" and .checkId=="arm-assign" and .taskId==$tid) | .verdict' "$PERF_FILE" | head -n1)
child_arm=$(jq -r --arg tid "$child_bucket_id" 'select(.stage=="S0" and .checkId=="arm-assign" and .taskId==$tid) | .verdict' "$PERF_FILE" | head -n1)
echo "  [F2] child tree API origin='$tree_origin'; root($root_bucket_id) arm=$root_arm, child($child_bucket_id) arm=$child_arm (want matching '$root_arm')"
if [[ "$root_arm" != "$child_arm" ]]; then
	echo "  [F2] REPRODUCED: originless child task in tree '$tree_origin' got arm '$child_arm' while root got '$root_arm'"
	failures=$((failures + 1))
fi

# --- F3: Scoped `gma_...` `grain/viewer` token gets `access: null` on GET /api/config, leaving UI editable ---
api POST /api/settings '{"externalAgentApi":true}' >/dev/null
vtok=$(api POST /api/external-agents/tokens '{"name":"f3-viewer","scopeSet":"grain/viewer"}' | jq -r '.token')
vcfg_access=$(curl -sS -H "Authorization: Bearer $vtok" "$DAEMON/api/config" | jq -c '.access')

ui_probe=$(PLAYWRIGHT_BROWSERS_PATH="${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}" DAEMON="$DAEMON" VTOK="$vtok" node --input-type=module - <<'MJS'
import path from "node:path";
const root = "/home/debian/work";
const { chromium } = await import(path.join(root, "ui-ng/node_modules/@playwright/test/index.mjs"));
const browser = await chromium.launch();
const ctx = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  extraHTTPHeaders: { Authorization: `Bearer ${process.env.VTOK}` },
});
const page = await ctx.newPage();
await page.goto(`${process.env.DAEMON}/next/settings/tasks`, { waitUntil: "networkidle" });
await page.getByTestId("switch-qa-enabled").waitFor({ state: "visible" });
const out = {
  readonlyPillVisible: await page.getByTestId("section-role-readonly-pill").isVisible(),
  readonlyBannerVisible: await page.getByTestId("settings-role-readonly").isVisible(),
  qaSwitchDisabled: await page.getByTestId("switch-qa-enabled").isDisabled(),
};
await browser.close();
console.log(JSON.stringify(out));
MJS
)
echo "  [F3] GET /api/config .access=$vcfg_access; UI probe=$ui_probe"
if [[ "$vcfg_access" == "null" ]] || [[ "$(echo "$ui_probe" | jq -r '.qaSwitchDisabled')" != "true" ]]; then
	echo "  [F3] REPRODUCED: grain/viewer gma_ token received .access=$vcfg_access and editable QA controls"
	failures=$((failures + 1))
fi

if [[ "$EXPECT_FIXED" == "1" && "$failures" -gt 0 ]]; then
	echo "FAIL: $failures finding(s) still unfixed" >&2
	exit 1
fi
echo "  completed findings check (unfixed_count=$failures)"
