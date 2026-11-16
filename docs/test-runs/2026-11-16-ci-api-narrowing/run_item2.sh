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

# Live walkthrough harness for §10 Item 2 (ci:// slash & percent job names)
# against a real `grain daemon -host-sandboxes -sandbox-port-base=0`, multi-repo
# GitHub sim, a dispatched fake-agent host sandbox (sandbox CLI), and host CLI
# (`grain fs ls` / `grain fs read`), exercising both Grain CI and GitHub Actions.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
FAKE_AGY="$ROOT/docs/test-runs/2026-11-13-grain-health-exit-codes/fake-agy.py"
WORK=${WORK:-/var/tmp/walk-ciapi-item2}
DAEMON_PORT=${DAEMON_PORT:-4286}
GHSIM_PORT=${GHSIM_PORT:-4287}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"
export PYTHONDONTWRITEBYTECODE=1

cleanup() {
	for f in daemon ghsim; do
		if [[ -f "$WORK/$f.pid" ]]; then
			pid=$(cat "$WORK/$f.pid")
			kill "$pid" 2>/dev/null || true
			for _ in $(seq 50); do
				kill -0 "$pid" 2>/dev/null || break
				sleep 0.1
			done
		fi
	done
}
trap cleanup EXIT

rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/sandboxes" "$WORK/bin" "$WORK/home" "$WORK/runs" "$WORK/captures"
chmod +x "$FAKE_AGY"

echo "=== Building ghsim ==="
(cd "$ROOT" && go build -o "$WORK/bin/ghsim" "$HERE/ghsim.go")

echo "=== Seeding acme/widgets bare repo with slash and percent job names ==="
git init -q --bare -b main "$WORK/widgets.git"
git -C "$WORK/widgets.git" config http.receivepack true
tmp=$(mktemp -d)
git -C "$tmp" init -q -b main
mkdir -p "$tmp/.grain/workflows"
cat >"$tmp/README.md" <<'EOF'
# acme/widgets (Item 2 slash/percent job name walkthrough)
EOF
cat >"$tmp/.grain/workflows/slash-names.yml" <<'EOF'
name: slash-names
on:
  workflow_dispatch:
  push:
jobs:
  pkg_cli_stress:
    name: "pkg/cli stress under load"
    capabilities: []
    steps:
      - name: Build
        run: |
          echo "GRAIN_BUILD_OK"
      - name: Stress
        run: |
          echo "GRAIN_STRESS_LINE_1"
          echo "GRAIN_STRESS_MARKER_SLASH_JOB"
          mkdir -p out
          printf "slash job artifact body\n" > out/report.txt
          exit 1
    artifacts:
      - name: report
        path: out/report.txt
  pkg:
    name: "pkg"
    capabilities: []
    steps:
      - name: Run
        run: |
          echo "GRAIN_DECOY_PKG_JOB"
  coverage_pct:
    name: "coverage 100% pass"
    capabilities: []
    steps:
      - name: Run
        run: |
          echo "GRAIN_PCT_ONLY_OK"
  slash_and_pct:
    name: "pkg/cli 100% pass"
    capabilities: []
    steps:
      - name: Run
        run: |
          echo "GRAIN_SLASH_AND_PCT_OK"
  ratio_slash:
    name: "ratio 50/50"
    capabilities: []
    steps:
      - name: Run
        run: |
          echo "GRAIN_RATIO_SLASH_LOG"
  ratio_escaped:
    name: "ratio 50%2F50"
    capabilities: []
    steps:
      - name: Run
        run: |
          echo "GRAIN_RATIO_ESCAPED_LOG"
EOF
git -C "$tmp" add .
git -C "$tmp" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qm "chore: seed slash-name workflows"
HEAD_SHA=$(git -C "$tmp" rev-parse HEAD)
git -C "$tmp" push -q "$WORK/widgets.git" main
rm -rf "$tmp"

echo "=== Booting ghsim ==="
"$WORK/bin/ghsim" -addr "$GHSIM" -repo "acme/widgets=$WORK/widgets.git" -default main >"$WORK/ghsim.log" 2>&1 &
echo $! >"$WORK/ghsim.pid"
for _ in $(seq 50); do
	curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null && break
	sleep 0.1
done

echo "=== Seeding GitHub Actions run 9101 with slash, decoy, percent, and %2F collision jobs ==="
NOW_ISO=$(date -u +%Y-%m-%dT%H:%M:%SZ)
cat >"$WORK/seed-gha.json" <<EOF
{
  "repo": "acme/widgets",
  "runs": [
    {
      "id": 9101,
      "name": "CI",
      "path": ".github/workflows/ci.yml",
      "headBranch": "main",
      "headSha": "$HEAD_SHA",
      "event": "push",
      "status": "completed",
      "conclusion": "failure",
      "runAttempt": 1,
      "createdAt": "$NOW_ISO",
      "updatedAt": "$NOW_ISO",
      "jobs": [
        {
          "name": "pkg/cli stress under load",
          "conclusion": "failure",
          "failingStep": "Stress",
          "log": "2026-11-16T15:00:01Z GHA_SLASH_JOB_START\n2026-11-16T15:00:02Z GHA_SLASH_JOB_STRESS_MARKER\n"
        },
        {
          "name": "pkg",
          "conclusion": "success",
          "log": "2026-11-16T15:00:01Z GHA_DECOY_PKG_LOG\n"
        },
        {
          "name": "coverage 100% pass",
          "conclusion": "success",
          "log": "2026-11-16T15:00:01Z GHA_PCT_ONLY_OK\n"
        },
        {
          "name": "pkg/cli 100% pass",
          "conclusion": "success",
          "log": "2026-11-16T15:00:01Z GHA_SLASH_AND_PCT_OK\n"
        },
        {
          "name": "ratio 50/50",
          "conclusion": "failure",
          "failingStep": "Check",
          "log": "2026-11-16T15:00:01Z GHA_RATIO_SLASH_LOG\n"
        },
        {
          "name": "ratio 50%2F50",
          "conclusion": "success",
          "log": "2026-11-16T15:00:01Z GHA_RATIO_ESCAPED_LOG\n"
        }
      ]
    }
  ]
}
EOF
curl -sf -X POST "http://$GHSIM/_sim/seed-actions" -H "Content-Type: application/json" --data-binary "@$WORK/seed-gha.json"

echo "=== Importing git connection + fake-agy model and booting real grain daemon ==="
echo "ghp_walkthrough" >"$WORK/bot.token"
echo "walkthrough-gemini-key" >"$WORK/gemini.key"
echo '{"directConfigWrites": true}' >"$WORK/data/state-repo.json"
cat >"$WORK/seed-git.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK/bot.token
EOF
cat >"$WORK/seed-models.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK/bot.token
models:
  - id: gemini
    framework: antigravity
    models: [gemini-3.1-pro]
    credential_file: $WORK/gemini.key
default_model: gemini-3.1-pro
EOF
"$GRAIN" connections -data-dir "$WORK/data" import "$WORK/seed-git.yaml" >"$WORK/connections.log" 2>&1

env -i \
	PATH="/home/debian/bin:/usr/local/bin:/usr/bin:/bin" \
	HOME="$WORK/home" \
	WALK_DIR="$WORK" \
	PYTHONDONTWRITEBYTECODE=1 \
	GCE_METADATA_HOST="127.0.0.1:9" \
	setsid "$GRAIN" daemon \
	-data-dir "$WORK/data" \
	-sandbox-dir "$WORK/sandboxes" \
	-host-sandboxes \
	-ui-addr "127.0.0.1:${DAEMON_PORT}" \
	-sandbox-port-base=0 \
	-as alice@example.com \
	-repo acme/widgets \
	-default-target-repo acme/widgets \
	-poll-interval 500ms \
	-max-workers 1 \
	-max-mergers 0 \
	-agy-path "$FAKE_AGY" \
	-gemini-api-key-file "$WORK/gemini.key" \
	-github-host "$GHSIM" \
	-github-insecure-http >>"$WORK/daemon.log" 2>&1 </dev/null &
echo $! >"$WORK/daemon.pid"

for _ in $(seq 120); do
	if [[ -s "$WORK/data/local-api-token" ]] && curl -sf "$DAEMON/api/auth/status" >/dev/null 2>&1; then
		break
	fi
	sleep 0.25
done
ACCESS_CODE=$(tr -d '\r\n' <"$WORK/data/local-api-token")
AUTH=(-H "Authorization: Bearer $ACCESS_CODE")
curl -sf "${AUTH[@]}" "$DAEMON/api/auth/status" >/dev/null
"$GRAIN" connections -data-dir "$WORK/data" import "$WORK/seed-models.yaml" >>"$WORK/connections.log" 2>&1
sleep 0.8

echo "=== Configuring acme/widgets ci_mode=grain and starting a Grain CI run ==="
curl -sf "${AUTH[@]}" -X PUT "$DAEMON/api/repos/acme/widgets/ci-mode" -H "Content-Type: application/json" -d '{"ciMode":"grain"}' >/dev/null
GRAIN_RUN_JSON=$(curl -sf "${AUTH[@]}" -X POST "$DAEMON/api/ci/runs" -H "Content-Type: application/json" \
	-d '{"repo":"acme/widgets","ref":"main","workflow":".grain/workflows/slash-names.yml"}')
GRAIN_RUN_ID=$(echo "$GRAIN_RUN_JSON" | jq -r '.id')
echo "Started Grain CI run: $GRAIN_RUN_ID"

for _ in $(seq 180); do
	CONC=$(curl -sf "${AUTH[@]}" "$DAEMON/api/ci/runs/$GRAIN_RUN_ID" | jq -r '.conclusion // ""')
	if [[ -n "$CONC" ]]; then
		echo "Grain CI run $GRAIN_RUN_ID concluded: $CONC"
		break
	fi
	sleep 0.5
done
curl -sf "${AUTH[@]}" "$DAEMON/api/ci/runs/$GRAIN_RUN_ID" | jq '{id, conclusion, jobs: [.jobs[] | {jobId, name, conclusion, artifacts: [.artifacts[]? | {job, name, stored}]}]}'

echo "=== Preparing fake-agent sandbox probe script ==="
cat >"$WORK/sandbox-probe.sh" <<EOF
#!/usr/bin/env bash
set -u
GHA="gha:acme:widgets:9101"
GRUN="$GRAIN_RUN_ID"

run_case() {
  local label="\$1"; shift
  echo "=== CASE: \$label ==="
  local out rc
  out=\$("\$@" 2>&1)
  rc=\$?
  echo "EXIT=\$rc"
  printf '%s\n' "\$out"
}

# --- GitHub Actions ---
run_case "GHA ls run" grain ls "ci://acme/widgets/\$GHA"
run_case "GHA ls slash job (%2F)" grain ls "ci://acme/widgets/\$GHA/pkg%2Fcli stress under load"
run_case "GHA ls slash job (raw /)" grain ls "ci://acme/widgets/\$GHA/pkg/cli stress under load"
run_case "GHA read slash job (%2F)" grain read "ci://acme/widgets/\$GHA/pkg%2Fcli stress under load/job.log"
run_case "GHA read slash job (raw /)" grain read "ci://acme/widgets/\$GHA/pkg/cli stress under load/job.log"
run_case "GHA ci log --job slash raw" grain ci log "\$GHA" --job "pkg/cli stress under load" --tail 50
run_case "GHA ci log --job slash %2F" grain ci log "\$GHA" --job "pkg%2Fcli stress under load" --tail 50
run_case "GHA ci log --job decoy pkg" grain ci log "\$GHA" --job "pkg" --tail 50
run_case "GHA read pct-only job" grain read "ci://acme/widgets/\$GHA/coverage 100% pass/job.log"
run_case "GHA ci log --job pct-only" grain ci log "\$GHA" --job "coverage 100% pass" --tail 50
run_case "GHA read slash+pct (%2F+%25)" grain read "ci://acme/widgets/\$GHA/pkg%2Fcli 100%25 pass/job.log"
run_case "GHA read slash+pct (raw)" grain read "ci://acme/widgets/\$GHA/pkg/cli 100% pass/job.log"
run_case "GHA ci log --job slash+pct" grain ci log "\$GHA" --job "pkg/cli 100% pass" --tail 50
run_case "GHA ci log --job ratio 50/50 (collision check)" grain ci log "\$GHA" --job "ratio 50/50" --tail 50
run_case "GHA ci log --job ratio 50%252F50 (ls-listed form of ratio 50%2F50)" grain ci log "\$GHA" --job "ratio 50%252F50" --tail 50
run_case "GHA read ratio 50/50 raw" grain read "ci://acme/widgets/\$GHA/ratio 50/50/job.log"
run_case "GHA read ratio 50%2F50 (%2F form -> ratio 50/50)" grain read "ci://acme/widgets/\$GHA/ratio 50%2F50/job.log"
run_case "GHA read ratio 50%252F50 (%252F form -> ratio 50%2F50)" grain read "ci://acme/widgets/\$GHA/ratio 50%252F50/job.log"

# --- Grain CI ---
run_case "Grain ls run" grain ls "ci://acme/widgets/\$GRUN"
run_case "Grain ls slash job (raw /)" grain ls "ci://acme/widgets/\$GRUN/pkg/cli stress under load"
run_case "Grain ls slash job (%2F)" grain ls "ci://acme/widgets/\$GRUN/pkg%2Fcli stress under load"
run_case "Grain read slash job Stress.log (raw /)" grain read "ci://acme/widgets/\$GRUN/pkg/cli stress under load/Stress.log"
run_case "Grain read slash job Stress.log (%2F)" grain read "ci://acme/widgets/\$GRUN/pkg%2Fcli stress under load/Stress.log"
run_case "Grain ci log --job slash raw (list steps)" grain ci log "\$GRUN" --job "pkg/cli stress under load" --tail 50
run_case "Grain ci log --job slash raw --step Stress" grain ci log "\$GRUN" --job "pkg/cli stress under load" --step Stress --tail 50
run_case "Grain ci log --job decoy pkg --step Run" grain ci log "\$GRUN" --job "pkg" --step Run --tail 50
run_case "Grain ci log --job pct-only --step Run" grain ci log "\$GRUN" --job "coverage 100% pass" --step Run --tail 50
run_case "Grain ci log --job slash+pct --step Run" grain ci log "\$GRUN" --job "pkg/cli 100% pass" --step Run --tail 50
run_case "Grain ci log --job ratio 50/50 --step Run (collision check)" grain ci log "\$GRUN" --job "ratio 50/50" --step Run --tail 50
run_case "Grain ci log --job ratio 50%252F50 --step Run (ls-listed form of ratio 50%2F50)" grain ci log "\$GRUN" --job "ratio 50%252F50" --step Run --tail 50
run_case "Grain ls artifacts" grain ls "ci://acme/widgets/\$GRUN/artifacts"
run_case "Grain ls artifacts/pkg_cli_stress" grain ls "ci://acme/widgets/\$GRUN/artifacts/pkg_cli_stress"
run_case "Grain ls artifacts by slash display name" grain ls "ci://acme/widgets/\$GRUN/artifacts/pkg/cli stress under load"
run_case "Grain ci artifact by jobId (tar extract)" bash -c "set -o pipefail; grain ci artifact pkg_cli_stress report --run '\$GRUN' -o - | tar -xzO"
run_case "Grain ci artifact by slash display name (tar extract)" bash -c "set -o pipefail; grain ci artifact 'pkg/cli stress under load' report --run '\$GRUN' -o - | tar -xzO"
run_case "Grain ci artifact by %2F display name (tar extract)" bash -c "set -o pipefail; grain ci artifact 'pkg%2Fcli stress under load' report --run '\$GRUN' -o - | tar -xzO"
EOF
chmod +x "$WORK/sandbox-probe.sh"

cat >"$WORK/action-default.json" <<EOF
{
  "kind": "mcp_sequence",
  "calls": [
    {
      "tool": "run_command",
      "args": {
        "command": "bash $WORK/sandbox-probe.sh > $WORK/captures/sandbox-out.txt 2>&1; cat $WORK/captures/sandbox-out.txt",
        "description": "probing ci:// slash and percent job names inside sandbox"
      }
    },
    {
      "tool": "comment_on_issue",
      "args": {
        "comment": "Item 2 sandbox probes finished; output saved to $WORK/captures/sandbox-out.txt"
      }
    }
  ]
}
EOF

echo "=== Dispatching real fake-agent task with api:ci:read@acme/widgets ==="
TASK_JSON=$(curl -sf "${AUTH[@]}" -X POST "$DAEMON/api/tasks" -H "Content-Type: application/json" -d '{
  "title": "Walkthrough Item 2: probe ci:// slash & percent job names",
  "description": "Run sandbox-probe.sh inside a real host sandbox",
  "repo": "acme/widgets",
  "approved": true,
  "capabilities": ["api:ci:read@acme/widgets"]
}')
TASK_ID=$(echo "$TASK_JSON" | jq -r '.id')
echo "Filed task $TASK_ID; waiting for completion..."

for _ in $(seq 120); do
	ST=$(curl -sf "${AUTH[@]}" "$DAEMON/api/tasks/$TASK_ID" | jq -r '.state')
	if [[ "$ST" != "queued" && "$ST" != "running" ]]; then
		echo "Task $TASK_ID finished in state: $ST"
		break
	fi
	sleep 0.5
done

echo "=== Host CLI (grain fs) probes against daemon /api/fs/ci ==="
export GRAIN_TOKEN="$ACCESS_CODE"
{
	echo "=== HOST: grain fs ls GHA run ==="
	"$GRAIN" -server "$DAEMON" fs ls "ci://acme/widgets/gha:acme:widgets:9101" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs read GHA slash (%2F) ==="
	"$GRAIN" -server "$DAEMON" fs read "ci://acme/widgets/gha:acme:widgets:9101/pkg%2Fcli stress under load/job.log" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs read GHA slash (raw /) ==="
	"$GRAIN" -server "$DAEMON" fs read "ci://acme/widgets/gha:acme:widgets:9101/pkg/cli stress under load/job.log" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs read GHA pct-only ==="
	"$GRAIN" -server "$DAEMON" fs read "ci://acme/widgets/gha:acme:widgets:9101/coverage 100% pass/job.log" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs read GHA slash+pct (%2F+%25) ==="
	"$GRAIN" -server "$DAEMON" fs read "ci://acme/widgets/gha:acme:widgets:9101/pkg%2Fcli 100%25 pass/job.log" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs read GHA ratio 50/50 raw ==="
	"$GRAIN" -server "$DAEMON" fs read "ci://acme/widgets/gha:acme:widgets:9101/ratio 50/50/job.log" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs read GHA ratio 50%2F50 (%2F -> ratio 50/50) ==="
	"$GRAIN" -server "$DAEMON" fs read "ci://acme/widgets/gha:acme:widgets:9101/ratio 50%2F50/job.log" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs read GHA ratio 50%252F50 (%252F -> ratio 50%2F50) ==="
	"$GRAIN" -server "$DAEMON" fs read "ci://acme/widgets/gha:acme:widgets:9101/ratio 50%252F50/job.log" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs ls Grain run ==="
	"$GRAIN" -server "$DAEMON" fs ls "ci://acme/widgets/$GRAIN_RUN_ID" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs read Grain slash Stress.log (raw /) ==="
	"$GRAIN" -server "$DAEMON" fs read "ci://acme/widgets/$GRAIN_RUN_ID/pkg/cli stress under load/Stress.log" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs read Grain slash+pct Run.log (%2F+%25) ==="
	"$GRAIN" -server "$DAEMON" fs read "ci://acme/widgets/$GRAIN_RUN_ID/pkg%2Fcli 100%25 pass/Run.log" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs ls Grain artifacts ==="
	"$GRAIN" -server "$DAEMON" fs ls "ci://acme/widgets/$GRAIN_RUN_ID/artifacts" 2>&1 || echo "EXIT=$?"
	echo "=== HOST: grain fs read Grain artifact by slash display name (tar extract) ==="
	"$GRAIN" -server "$DAEMON" fs read "ci://acme/widgets/$GRAIN_RUN_ID/artifacts/pkg/cli stress under load/report" | tar -xzO 2>&1 || echo "EXIT=$?"
} >"$WORK/captures/host-out.txt"

echo "=== SANDBOX OUTPUT ==="
cat "$WORK/captures/sandbox-out.txt" 2>/dev/null || echo "(no sandbox output captured!)"
echo "=== HOST OUTPUT ==="
cat "$WORK/captures/host-out.txt"
