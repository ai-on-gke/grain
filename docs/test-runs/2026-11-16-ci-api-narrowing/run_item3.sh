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

# Live walkthrough for docs/test-plans/2026-11-16.md §10 Item 3 (PR #1656,
# commit 5b936ac39): Stage 1 job-attempt reads forwarded through
# StateRepoPullRequestClient in production, job-level pipeline.rc-failed lines,
# flake-budget tracking across -discarded tags, rollup-only pass verdict, and
# matrix-shard normalization.
#
# Reuses the release-train setup pattern from
# docs/test-runs/2026-11-15-release-train-publishing-held/ against a real
# `grain daemon -host-sandboxes -sandbox-port-base=0` and multi-attempt ghsim.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
WORK=${WORK:-/var/tmp/walk-ciapi-item3}
DAEMON_PORT=${DAEMON_PORT:-4290}
GHSIM_PORT=${GHSIM_PORT:-4294}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"

cleanup() {
	for pf in "$WORK/sse.pid" "$WORK/daemon.pid" "$WORK/ghsim.pid"; do
		if [[ -f "$pf" ]]; then
			kill "$(cat "$pf")" 2>/dev/null || true
			rm -f "$pf"
		fi
	done
}
trap cleanup EXIT

rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/sandboxes" "$WORK/bin" "$WORK/home" "$WORK/captures"

echo "=== Building ghsim ==="
(cd "$ROOT" && go build -o "$WORK/bin/ghsim" "$HERE/ghsim.go")

echo "=== Seeding acme/grain bare repo from HEAD .grain/ and .github/workflows/ ==="
git init -q --bare -b main "$WORK/grain.git"
git -C "$WORK/grain.git" config http.receivepack true

TMP_WORK="$WORK/seed-work"
git init -q -b main "$TMP_WORK"
git -C "$ROOT" archive HEAD .grain .github/workflows README.md | tar -x -C "$TMP_WORK"
git -C "$TMP_WORK" add -A
git -C "$TMP_WORK" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed acme/grain from HEAD"
git -C "$TMP_WORK" tag v3.8.0
git -C "$TMP_WORK" push -q "$WORK/grain.git" main v3.8.0
BASE_SHA=$(git -C "$TMP_WORK" rev-parse HEAD)
echo "Base commit (v3.8.0): $BASE_SHA"

echo "ghp_walkthrough" >"$WORK/bot.token"
echo '{"directConfigWrites": true}' >"$WORK/data/state-repo.json"
cat >"$WORK/seed-git.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK/bot.token
EOF
"$GRAIN" connections -data-dir "$WORK/data" import "$WORK/seed-git.yaml" >"$WORK/connections.log" 2>&1

echo "=== Starting ghsim on $GHSIM ==="
"$WORK/bin/ghsim" \
	-addr "$GHSIM" \
	-repo "acme/grain=$WORK/grain.git" \
	-default main >"$WORK/ghsim.log" 2>&1 &
echo $! >"$WORK/ghsim.pid"
for _ in $(seq 50); do
	curl -sf "http://$GHSIM/repos/acme/grain" >/dev/null && break
	sleep 0.1
done

echo "=== Starting real grain daemon on $DAEMON (-sandbox-port-base=0) ==="
env -i \
	PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
	HOME="$WORK/home" \
	GCE_METADATA_HOST="127.0.0.1:9" \
	setsid "$GRAIN" daemon \
	-data-dir "$WORK/data" \
	-sandbox-dir "$WORK/sandboxes" \
	-host-sandboxes \
	-ui-addr "127.0.0.1:${DAEMON_PORT}" \
	-sandbox-port-base=0 \
	-as alice@example.com \
	-repo acme/grain \
	-default-target-repo acme/grain \
	-poll-interval 1s \
	-max-workers 1 \
	-github-host "$GHSIM" \
	-github-insecure-http >"$WORK/daemon.log" 2>&1 </dev/null &
echo $! >"$WORK/daemon.pid"

ACCESS_CODE=""
for _ in $(seq 150); do
	if [[ -s "$WORK/data/local-api-token" ]]; then
		ACCESS_CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
		if curl -sf -H "Authorization: Bearer $ACCESS_CODE" "$DAEMON/api/config" >/dev/null 2>&1; then
			break
		fi
	fi
	sleep 0.2
done
[[ -n "$ACCESS_CODE" ]] || { echo "daemon failed to start"; tail -n 50 "$WORK/daemon.log"; exit 1; }
AUTH=(-H "Authorization: Bearer $ACCESS_CODE")

api() {
	local method=$1 path=$2 body=${3:-}
	if [[ -n "$body" ]]; then
		curl -fsS -X "$method" "${AUTH[@]}" -H "Content-Type: application/json" -d "$body" "$DAEMON$path"
	else
		curl -fsS -X "$method" "${AUTH[@]}" "$DAEMON$path"
	fi
}

seed_runs() {
	curl -fsS -X POST -H "Content-Type: application/json" -d "{\"repo\":\"acme/grain\",\"runs\":$1}" "http://$GHSIM/_sim/seed-actions" >/dev/null
}

iso() { date -u -d "$1" +%FT%TZ; }

echo "=== Pausing dispatch, enabling ci_mode=github and declarations=true on acme/grain ==="
api POST /api/pipeline-pause '{"reason":"walkthrough Item 3: observe Stage 1 verdicts without dispatching Stage 2 workers"}' >/dev/null
api PUT /api/repos/acme/grain/ci-mode '{"ciMode":"github"}' >/dev/null
for _ in $(seq 10); do
	api PUT /api/repos/acme/grain/declarations '{"declarations":true}' >/dev/null && break
	sleep 1
done

echo "Waiting for release-qualification suite import..."
for _ in $(seq 60); do
	if api GET /api/suites | jq -e '(if type=="array" then . else .items end)[]? | select(.id == "repo:suite:acme:grain:release-qualification")' >/dev/null 2>&1; then
		echo "Imported repo:suite:acme:grain:release-qualification"
		break
	fi
	sleep 0.5
done

# Start SSE capture on /api/events for the full walkthrough.
curl -sN "${AUTH[@]}" -H "Accept: text/event-stream" "$DAEMON/api/events" >"$WORK/captures/sse-events.log" 2>&1 &
echo $! >"$WORK/sse.pid"

echo ""
echo "=== PHASE 1: Discarded Stage 1 red candidate (v3.9.0-rc.1 + v3.9.0-rc.1-discarded) ==="
echo "rc1-change" >"$TMP_WORK/rc1.txt"
git -C "$TMP_WORK" add rc1.txt
git -C "$TMP_WORK" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "feat: rc.1 commit"
git -C "$TMP_WORK" tag v3.9.0-rc.1
git -C "$TMP_WORK" tag v3.9.0-rc.1-discarded
git -C "$TMP_WORK" push -q "$WORK/grain.git" main v3.9.0-rc.1 v3.9.0-rc.1-discarded
RC1_SHA=$(git -C "$TMP_WORK" rev-parse HEAD)
echo "Pushed v3.9.0-rc.1 and v3.9.0-rc.1-discarded at $RC1_SHA"

NOW_ISO=$(iso now)
RUN_9201=$(cat <<EOF
{
  "id": 9201,
  "name": "nightly",
  "path": ".github/workflows/nightly.yml",
  "headBranch": "v3.9.0-rc.1",
  "headSha": "$RC1_SHA",
  "event": "push",
  "status": "completed",
  "conclusion": "failure",
  "runAttempt": 2,
  "createdAt": "$NOW_ISO",
  "updatedAt": "$NOW_ISO",
  "jobs": [
    {"name": "pkg/cli stress under load", "status": "completed", "conclusion": "failure", "runAttempt": 1},
    {"name": "pkg/cli stress under load", "status": "completed", "conclusion": "success", "runAttempt": 2},
    {"name": "sustained load", "status": "completed", "conclusion": "failure", "runAttempt": 1},
    {"name": "sustained load", "status": "completed", "conclusion": "failure", "runAttempt": 2},
    {"name": "stage 1 qualification verdict", "status": "completed", "conclusion": "failure", "runAttempt": 1},
    {"name": "stage 1 qualification verdict", "status": "completed", "conclusion": "failure", "runAttempt": 2}
  ]
}
EOF
)
seed_runs "[$RUN_9201]"

echo "Waiting for release-qualification reconcile tick (<= 65s) to raise pipeline.rc-failed:v3.9.0-rc.1..."
for i in $(seq 45); do
	if api GET "/api/notifications?open=1" | jq -e '(if type=="array" then . else .items end)[]? | select(.subject == "pipeline.rc-failed:v3.9.0-rc.1")' >/dev/null 2>&1; then
		echo "Phase 1 notification raised after ~$((i * 2))s!"
		break
	fi
	sleep 2
done
api GET "/api/notifications" | jq '[(if type=="array" then . else .items end)[] | {id, subject, severity, title, body, clearedAt}]' | tee "$WORK/captures/phase1-notifications.json"

echo ""
echo "=== PHASE 2: Active v3.9.0-rc.2 spending flake retry #2 + rollup-only failure (passes Stage 1) ==="
echo "rc2-change" >"$TMP_WORK/rc2.txt"
git -C "$TMP_WORK" add rc2.txt
git -C "$TMP_WORK" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "fix: rc.2 commit"
git -C "$TMP_WORK" tag v3.9.0-rc.2
git -C "$TMP_WORK" push -q "$WORK/grain.git" main v3.9.0-rc.2
RC2_SHA=$(git -C "$TMP_WORK" rev-parse HEAD)
echo "Pushed v3.9.0-rc.2 at $RC2_SHA"

NOW2_ISO=$(iso now)
RUN_9202=$(cat <<EOF
{
  "id": 9202,
  "name": "nightly",
  "path": ".github/workflows/nightly.yml",
  "headBranch": "v3.9.0-rc.2",
  "headSha": "$RC2_SHA",
  "event": "push",
  "status": "completed",
  "conclusion": "failure",
  "runAttempt": 2,
  "createdAt": "$NOW2_ISO",
  "updatedAt": "$NOW2_ISO",
  "jobs": [
    {"name": "pkg/cli stress under load", "status": "completed", "conclusion": "failure", "runAttempt": 1},
    {"name": "pkg/cli stress under load", "status": "completed", "conclusion": "success", "runAttempt": 2},
    {"name": "sustained load", "status": "completed", "conclusion": "success", "runAttempt": 1},
    {"name": "stage 1 qualification verdict", "status": "completed", "conclusion": "failure", "runAttempt": 1},
    {"name": "stage 1 qualification verdict", "status": "completed", "conclusion": "failure", "runAttempt": 2}
  ]
}
EOF
)
RUN_TESTS=$(cat <<EOF
{
  "id": 9210,
  "name": "Tests",
  "path": ".github/workflows/tests.yml",
  "headBranch": "main",
  "headSha": "$RC2_SHA",
  "event": "push",
  "status": "completed",
  "conclusion": "success",
  "runAttempt": 1,
  "createdAt": "$NOW2_ISO",
  "updatedAt": "$NOW2_ISO",
  "jobs": [{"name": "go", "status": "completed", "conclusion": "success", "runAttempt": 1}]
}
EOF
)
RUN_REL=$(cat <<EOF
{
  "id": 9211,
  "name": "Release",
  "path": ".github/workflows/release.yml",
  "headBranch": "main",
  "headSha": "$RC2_SHA",
  "event": "push",
  "status": "completed",
  "conclusion": "success",
  "runAttempt": 1,
  "createdAt": "$NOW2_ISO",
  "updatedAt": "$NOW2_ISO",
  "jobs": [{"name": "publish", "status": "completed", "conclusion": "success", "runAttempt": 1}]
}
EOF
)
seed_runs "[$RUN_9201, $RUN_9202, $RUN_TESTS, $RUN_REL]"

echo "Waiting for release-qualification reconcile tick (<= 65s) to raise pipeline.flake-budget and start Stage 2..."
for i in $(seq 45); do
	if api GET "/api/notifications?open=1" | jq -e '(if type=="array" then . else .items end)[]? | select(.subject == "pipeline.flake-budget:acme/grain:pkg/cli stress under load")' >/dev/null 2>&1; then
		echo "Phase 2 flake-budget notification raised after ~$((i * 2))s!"
		break
	fi
	sleep 2
done
api GET "/api/notifications" | jq '[(if type=="array" then . else .items end)[] | {id, subject, severity, title, body, clearedAt}]' | tee "$WORK/captures/phase2-notifications.json"
api GET "/api/suite-runs" | jq '[(if type=="array" then . else .items end)[]? | {id, suiteId, status, tag, passCount}]' | tee "$WORK/captures/phase2-suite-runs.json"
api GET "/api/tasks" | jq '[(if type=="array" then . else .items end)[]? | select(.id != "1") | {id, state, title, priority}]' | tee "$WORK/captures/phase2-tasks.json"

echo ""
echo "=== PHASE 3: Active red Stage 1 (v3.9.0-rc.3, no -discarded tag, matrix shard) ==="
echo "rc3-change" >"$TMP_WORK/rc3.txt"
git -C "$TMP_WORK" add rc3.txt
git -C "$TMP_WORK" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "feat: rc.3 commit"
git -C "$TMP_WORK" tag v3.9.0-rc.3
git -C "$TMP_WORK" push -q "$WORK/grain.git" main v3.9.0-rc.3
RC3_SHA=$(git -C "$TMP_WORK" rev-parse HEAD)
echo "Pushed v3.9.0-rc.3 at $RC3_SHA"

NOW3_ISO=$(iso now)
RUN_9203=$(cat <<EOF
{
  "id": 9203,
  "name": "nightly",
  "path": ".github/workflows/nightly.yml",
  "headBranch": "v3.9.0-rc.3",
  "headSha": "$RC3_SHA",
  "event": "push",
  "status": "completed",
  "conclusion": "failure",
  "runAttempt": 2,
  "createdAt": "$NOW3_ISO",
  "updatedAt": "$NOW3_ISO",
  "jobs": [
    {"name": "go, shuffled and repeated (1)", "status": "completed", "conclusion": "success", "runAttempt": 1},
    {"name": "go, shuffled and repeated (2)", "status": "completed", "conclusion": "failure", "runAttempt": 1},
    {"name": "go, shuffled and repeated (2)", "status": "completed", "conclusion": "failure", "runAttempt": 2},
    {"name": "stage 1 qualification verdict", "status": "completed", "conclusion": "failure", "runAttempt": 1},
    {"name": "stage 1 qualification verdict", "status": "completed", "conclusion": "failure", "runAttempt": 2}
  ]
}
EOF
)
seed_runs "[$RUN_9201, $RUN_9202, $RUN_9203, $RUN_TESTS, $RUN_REL]"

echo "Waiting for release-qualification reconcile tick (<= 65s) to raise pipeline.rc-failed:v3.9.0-rc.3..."
for i in $(seq 45); do
	if api GET "/api/notifications?open=1" | jq -e '(if type=="array" then . else .items end)[]? | select(.subject == "pipeline.rc-failed:v3.9.0-rc.3")' >/dev/null 2>&1; then
		echo "Phase 3 notification raised after ~$((i * 2))s!"
		break
	fi
	sleep 2
done
api GET "/api/notifications" | jq '[(if type=="array" then . else .items end)[] | {id, subject, severity, title, body, clearedAt}]' | tee "$WORK/captures/phase3-notifications.json"

echo ""
echo "=== Checking /api/repos/acme/grain/release, /releases, /next/ routes, grain health, SSE events, and ghsim HTTP calls ==="
api GET "/api/repos/acme/grain/release" | jq '.' | tee "$WORK/captures/repo-release.json"
api GET "/api/repos/acme/grain/releases" | jq '.' | tee "$WORK/captures/repo-releases.json"
echo "HTTP status /next/inbox: $(curl -s -o /dev/null -w '%{http_code}' "${AUTH[@]}" "$DAEMON/next/inbox")"
echo "HTTP status /next/repos/acme/grain/releases: $(curl -s -o /dev/null -w '%{http_code}' "${AUTH[@]}" "$DAEMON/next/repos/acme/grain/releases")"
env -i PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" HOME="$WORK/home" \
	GRAIN_SERVER="$DAEMON" GRAIN_TOKEN="$ACCESS_CODE" "$GRAIN" health | tee "$WORK/captures/health.txt" || true

echo "=== /api/events notification.* JSON events ==="
api GET "/api/events?type=notification.raised&type=notification.cleared" | jq '.' | tee "$WORK/captures/notification-events.json"

echo "=== SSE /api/events captured (notification / suite / task events) ==="
grep -E "^(event:|data:)" "$WORK/captures/sse-events.log" | grep -E "pipeline\.|rc-failed|flake-budget|release-qualification|notification" | head -n 40 || true

echo "=== ghsim HTTP requests proving StateRepoPullRequestClient forwarded Stage 1 reads ==="
grep -E "ghsim req:" "$WORK/ghsim.log" | grep -E "head_sha=|filter=all|/tags" | head -n 40 || true

echo "=== daemon.log release/notification lines ==="
grep -E "notification|release-qualification|stage1|Stage 1|rc-failed|flake-budget" "$WORK/daemon.log" | tail -n 40 || true
