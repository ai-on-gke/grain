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

# Executes §9 Item 3: `grain ci test-history` on GitHub Actions (with and
# without --provider), comparing in-sandbox vs host CLI, folded tails in
# `grain ci show`, no-recorded-tails reporting, and refusals without api:ci-read.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=docs/test-runs/2026-11-10-dispatch-wakes/env.sh
source "$HERE/env.sh"

cleanup() {
	stop_env /tmp/walk-item3 2>/dev/null || true
}
trap cleanup EXIT

NIGHTLY_TEST="TestOutboundTunnelConnectPeerForwardingReconnectGraceAndImmediateRevocation"

echo "================================================================"
echo "3a. Live deployment sandbox refusal (this repo ai-on-gke/grain, task 3956 without api:ci-read)"
echo "================================================================"
set +e
echo "-- Preinstalled /usr/local/bin/grain ci test-history $NIGHTLY_TEST:"
/usr/local/bin/grain ci test-history "$NIGHTLY_TEST" 2>&1
echo "exit=$?"
echo "-- Built HEAD /home/debian/bin/grain ci test-history $NIGHTLY_TEST (no --provider):"
/home/debian/bin/grain ci test-history "$NIGHTLY_TEST" 2>&1
echo "exit=$?"
echo "-- Built HEAD /home/debian/bin/grain ci test-history --provider github-actions $NIGHTLY_TEST:"
/home/debian/bin/grain ci test-history --provider github-actions "$NIGHTLY_TEST" 2>&1
echo "exit=$?"
set -e

echo "================================================================"
echo "3b. Real HEAD daemon: retired Grain CI runs + nightly.yml Actions runs with recorded tails"
echo "================================================================"
init_env /tmp/walk-item3
HEAD_SHA=$(git -C /tmp/walk-item3/widgets.git rev-parse main)

start_ghsim /tmp/walk-item3
POLL_INTERVAL=500ms start_daemon /home/debian/bin/grain /tmp/walk-item3

# Seed 2 retired Grain CI runs from 2026-10-31 in SQLite (matching PR #1034's scenario where retired Grain CI passed NIGHTLY_TEST)
sqlite3 "$DB" <<EOF
INSERT INTO ci_run (id, run_key, repo, ref, commit_sha, event, requested_by, started_at, finished_at, conclusion)
VALUES
  ('ci-oct31-2', 'key-2', 'acme/widgets', 'main', '$HEAD_SHA', 'push', 'seed', '2026-10-31 15:00:00', '2026-10-31 15:05:00', 'success'),
  ('ci-oct31-1', 'key-1', 'acme/widgets', 'main', '$HEAD_SHA', 'push', 'seed', '2026-10-31 12:00:00', '2026-10-31 12:05:00', 'success');
INSERT INTO ci_job (run_id, job_index, workflow, workflow_path, job_id, name, started_at, finished_at, conclusion, status)
VALUES
  ('ci-oct31-2', 0, 'ci', '.grain/workflows/ci.yml', 'unit', 'unit', '2026-10-31 15:00:00', '2026-10-31 15:05:00', 'success', 'completed'),
  ('ci-oct31-1', 0, 'ci', '.grain/workflows/ci.yml', 'unit', 'unit', '2026-10-31 12:00:00', '2026-10-31 12:05:00', 'success', 'completed');
INSERT INTO ci_step (run_id, job_index, step_index, name, conclusion, exit_code, output, started_at, finished_at)
VALUES
  ('ci-oct31-2', 0, 0, 'unit', 'success', 0, '', '2026-10-31 15:00:00', '2026-10-31 15:05:00'),
  ('ci-oct31-1', 0, 0, 'unit', 'success', 0, '', '2026-10-31 12:00:00', '2026-10-31 12:05:00');
INSERT INTO ci_step_log_chunk (run_id, job_index, step_index, chunk_index, output, created_at)
VALUES
  ('ci-oct31-2', 0, 0, 0, '=== RUN   $NIGHTLY_TEST' || char(10) || '--- PASS: $NIGHTLY_TEST (0.04s)' || char(10) || 'PASS' || char(10), '2026-10-31 15:05:00'),
  ('ci-oct31-1', 0, 0, 0, '=== RUN   $NIGHTLY_TEST' || char(10) || '--- PASS: $NIGHTLY_TEST (0.03s)' || char(10) || 'PASS' || char(10), '2026-10-31 12:05:00');
EOF
# Restart daemon so CIStore loads the seeded retired Grain CI runs into memory if cached
POLL_INTERVAL=500ms start_daemon /home/debian/bin/grain /tmp/walk-item3
echo "-- Recorded retired Grain CI runs before switching to ci_mode: github:"
api GET "/api/ci/runs?repo=acme/widgets&provider=grain" | jq '.runs | map({id, provider, conclusion, startedAt})'

# Switch acme/widgets to ci_mode: github!
api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"github"}' >/dev/null

# Seed GitHub Actions runs on ghsim (2 nightly.yml runs failing NIGHTLY_TEST + 2 green Tests runs + 1 nightly.yml failing WITHOUT recorded tail)
curl -sS -X POST "http://$GHSIM/_sim/seed-actions" -d @- <<EOF
{
  "runs": [
    {
      "id": 9505,
      "name": "Nightly",
      "path": ".github/workflows/nightly.yml",
      "headBranch": "main",
      "headSha": "$HEAD_SHA",
      "event": "schedule",
      "status": "completed",
      "conclusion": "failure",
      "createdAt": "2026-11-10T03:00:00Z",
      "updatedAt": "2026-11-10T03:22:00Z",
      "jobs": [
        {
          "name": "go-long",
          "conclusion": "failure",
          "failingStep": "go test -race",
          "log": "=== RUN   $NIGHTLY_TEST\n    server_test.go:412: expected 200 during reconnect_grace, got 503\n--- FAIL: $NIGHTLY_TEST (2.01s)\nFAIL\tgithub.com/ai-on-gke/grain/pkg/hub/server\t2.412s"
        }
      ]
    },
    {
      "id": 9504,
      "name": "Tests",
      "path": ".github/workflows/tests.yml",
      "headBranch": "main",
      "headSha": "$HEAD_SHA",
      "event": "push",
      "status": "completed",
      "conclusion": "success",
      "createdAt": "2026-11-09T22:00:00Z",
      "updatedAt": "2026-11-09T22:14:00Z",
      "jobs": [
        {
          "name": "go unit",
          "conclusion": "success",
          "log": "ok"
        }
      ]
    },
    {
      "id": 9503,
      "name": "Nightly",
      "path": ".github/workflows/nightly.yml",
      "headBranch": "main",
      "headSha": "$HEAD_SHA",
      "event": "schedule",
      "status": "completed",
      "conclusion": "failure",
      "createdAt": "2026-11-09T03:00:00Z",
      "updatedAt": "2026-11-09T03:25:00Z",
      "jobs": [
        {
          "name": "go-long",
          "conclusion": "failure",
          "failingStep": "go test -race",
          "log": "=== RUN   $NIGHTLY_TEST\n    server_test.go:412: reconnect_grace expired early: 503\n--- FAIL: $NIGHTLY_TEST (2.03s)\nFAIL\tgithub.com/ai-on-gke/grain/pkg/hub/server\t2.501s"
        }
      ]
    },
    {
      "id": 9502,
      "name": "Nightly",
      "path": ".github/workflows/nightly.yml",
      "headBranch": "main",
      "headSha": "$HEAD_SHA",
      "event": "schedule",
      "status": "completed",
      "conclusion": "failure",
      "createdAt": "2026-11-08T03:00:00Z",
      "updatedAt": "2026-11-08T03:20:00Z",
      "jobs": [
        {
          "name": "go-long",
          "conclusion": "failure",
          "failingStep": "go test -race",
          "log": "=== RUN   TestUntailedNightlyFailure\n--- FAIL: TestUntailedNightlyFailure (1.10s)\nFAIL"
        }
      ]
    },
    {
      "id": 9501,
      "name": "Tests",
      "path": ".github/workflows/tests.yml",
      "headBranch": "main",
      "headSha": "$HEAD_SHA",
      "event": "push",
      "status": "completed",
      "conclusion": "success",
      "createdAt": "2026-11-08T01:00:00Z",
      "updatedAt": "2026-11-08T01:12:00Z",
      "jobs": [
        {
          "name": "go unit",
          "conclusion": "success",
          "log": "ok"
        }
      ]
    }
  ]
}
EOF

# Record failing job tails in github_ci_job for runs 9505 and 9503 (leaving 9502 WITHOUT a recorded tail in github_ci_job!)
sqlite3 "$DB" <<EOF
INSERT INTO github_ci_job (repo, check_run_id, source, app, ref, commit_sha, workflow, run_id, run_attempt, job_name, conclusion, started_at, completed_at, failing_step, run_url, job_url, output, failure_tail, tail_source, tail_fetched, via, recorded_at, updated_at)
VALUES
  ('acme/widgets', 99505, 'github', 'github-actions', 'main', '$HEAD_SHA', '.github/workflows/nightly.yml', 9505, 1, 'go-long', 'failure', '2026-11-10 03:00:00', '2026-11-10 03:22:00', 'go test -race', 'https://github.example/acme/widgets/actions/runs/9505', 'https://github.example/acme/widgets/actions/runs/9505/job/99505', '', '=== RUN   $NIGHTLY_TEST' || char(10) || '    server_test.go:412: expected 200 during reconnect_grace, got 503' || char(10) || '--- FAIL: $NIGHTLY_TEST (2.01s)' || char(10) || 'FAIL	github.com/ai-on-gke/grain/pkg/hub/server	2.412s', 'actions-log', 1, 'poll', '2026-11-10 03:22:05', '2026-11-10 03:22:05'),
  ('acme/widgets', 99503, 'github', 'github-actions', 'main', '$HEAD_SHA', '.github/workflows/nightly.yml', 9503, 1, 'go-long', 'failure', '2026-11-09 03:00:00', '2026-11-09 03:25:00', 'go test -race', 'https://github.example/acme/widgets/actions/runs/9503', 'https://github.example/acme/widgets/actions/runs/9503/job/99503', '', '=== RUN   $NIGHTLY_TEST' || char(10) || '    server_test.go:412: reconnect_grace expired early: 503' || char(10) || '--- FAIL: $NIGHTLY_TEST (2.03s)' || char(10) || 'FAIL	github.com/ai-on-gke/grain/pkg/hub/server	2.501s', 'actions-log', 1, 'poll', '2026-11-09 03:25:05', '2026-11-09 03:25:05');
EOF
sqlite3 "$DB" "UPDATE grain_config SET external_agent_api = 1;"
POLL_INTERVAL=500ms start_daemon /home/debian/bin/grain /tmp/walk-item3
api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"github"}' >/dev/null

echo "================================================================"
echo "3c. Dispatched sandbox task WITHOUT api:ci-read -> refusal"
echo "================================================================"
T_NOCI=$(api POST /api/tasks '{"title":"Item3c: sandbox without api:ci-read","body":"walk","repo":"acme/widgets"}' | jq -r .id)
cat > "/tmp/walk-item3/action-$T_NOCI.json" <<EOF
[
  {
    "kind": "mcp_sequence",
    "calls": [
      {"tool": "run_command", "args": {"command": "/home/debian/bin/grain ci test-history $NIGHTLY_TEST 2>&1 || true", "description": "test-history without api:ci-read"}},
      {"tool": "nothing_needed", "args": {"reason": "done"}}
    ]
  }
]
EOF
api POST "/api/tasks/$T_NOCI/approve" '{}' >/dev/null
for _ in $(seq 40); do [[ $(api GET "/api/tasks/$T_NOCI" | jq '.attempts | length') -ge 1 && $(api GET "/api/tasks/$T_NOCI" | jq -r .state) != "running" ]] && break; sleep 0.25; done
jq -r '.outputs[0].text' "/tmp/walk-item3/runs/mcp-$T_NOCI.jsonl"

echo "================================================================"
echo "3d. Dispatched sandbox task WITH api:ci-read -> default, --provider github-actions, --provider grain, untailed test, ci show"
echo "================================================================"
T_CI_RESP=$(api POST /api/tasks '{"title":"Item3d: sandbox with api:ci-read","body":"walk","repo":"acme/widgets","capabilities":["api:ci-read@acme/widgets"]}')
T_CI=$(echo "$T_CI_RESP" | jq -r .id)
if [[ -z "$T_CI" || "$T_CI" == "null" ]]; then
	echo "CreateTask failed: $T_CI_RESP" >&2
	exit 1
fi
cat > "/tmp/walk-item3/action-$T_CI.json" <<EOF
[
  {
    "kind": "mcp_sequence",
    "calls": [
      {"tool": "run_command", "args": {"command": "/home/debian/bin/grain ci test-history $NIGHTLY_TEST", "description": "sandbox default"}},
      {"tool": "run_command", "args": {"command": "/home/debian/bin/grain ci test-history --provider github-actions $NIGHTLY_TEST", "description": "sandbox --provider github-actions"}},
      {"tool": "run_command", "args": {"command": "/home/debian/bin/grain ci test-history --provider grain $NIGHTLY_TEST", "description": "sandbox --provider grain"}},
      {"tool": "run_command", "args": {"command": "/home/debian/bin/grain ci test-history TestUntailedNightlyFailure", "description": "sandbox untailed test"}},
      {"tool": "run_command", "args": {"command": "/home/debian/bin/grain ci show gha:acme:widgets:9505", "description": "sandbox ci show"}},
      {"tool": "nothing_needed", "args": {"reason": "done"}}
    ]
  }
]
EOF
api POST "/api/tasks/$T_CI/approve" '{}' >/dev/null
for _ in $(seq 50); do [[ $(api GET "/api/tasks/$T_CI" | jq '.attempts | length') -ge 1 && $(api GET "/api/tasks/$T_CI" | jq -r .state) != "running" ]] && break; sleep 0.25; done

extract_stdout() {
	jq -r ".outputs[$1].text" "/tmp/walk-item3/runs/mcp-$T_CI.jsonl" | sed -n '/^stdout:$/,/^stderr:$/{/^stdout:$/d;/^stderr:$/d;p;}'
}
extract_stdout 0 > /tmp/sbx-default.txt
extract_stdout 1 > /tmp/sbx-gha.txt
extract_stdout 2 > /tmp/sbx-grain.txt
extract_stdout 3 > /tmp/sbx-untailed.txt
extract_stdout 4 > /tmp/sbx-show.txt

echo "--- [Sandbox: default (no --provider)] ---"
cat /tmp/sbx-default.txt
echo "--- [Sandbox: --provider github-actions] ---"
cat /tmp/sbx-gha.txt
echo "--- [Sandbox: --provider grain] ---"
cat /tmp/sbx-grain.txt
echo "--- [Sandbox: TestUntailedNightlyFailure (no recorded tail)] ---"
cat /tmp/sbx-untailed.txt
echo "--- [Sandbox: grain ci show gha:acme:widgets:9505] ---"
cat /tmp/sbx-show.txt

echo "================================================================"
echo "3e. Host CLI comparison (admin token, ci:read-only token, and token lacking ci:read)"
echo "================================================================"
host_grain() {
	local tok=$1; shift
	env -i PATH="/usr/bin:/bin" HOME="/tmp/walk-item3/home" \
		/home/debian/bin/grain -server "$DAEMON" -token "$tok" "$@"
}

host_grain "$CODE" ci test-history -repo acme/widgets "$NIGHTLY_TEST" > /tmp/host-default.txt 2>&1
host_grain "$CODE" ci test-history -repo acme/widgets -provider github-actions "$NIGHTLY_TEST" > /tmp/host-gha.txt 2>&1
host_grain "$CODE" ci test-history -repo acme/widgets -provider grain "$NIGHTLY_TEST" > /tmp/host-grain.txt 2>&1
host_grain "$CODE" ci show gha:acme:widgets:9505 > /tmp/host-show.txt 2>&1

echo "--- [Host (admin): default (no -provider)] ---"
cat /tmp/host-default.txt
echo "--- [Diff: Sandbox default vs Host admin default] ---"
diff -u /tmp/sbx-default.txt /tmp/host-default.txt || true
echo "--- [Diff: Sandbox --provider github-actions vs Host -provider github-actions] ---"
diff -u /tmp/sbx-gha.txt /tmp/host-gha.txt || true
echo "--- [Diff: Sandbox --provider grain vs Host -provider grain] ---"
diff -u /tmp/sbx-grain.txt /tmp/host-grain.txt || true
echo "--- [Host (admin): grain ci show gha:acme:widgets:9505] ---"
cat /tmp/host-show.txt

# Mint external-agent token with ONLY ci:read on acme/widgets (lacking capabilities:read!)
TOK_CI_ONLY=$(api POST /api/external-agents/tokens '{"name":"ci-read-only","scopes":["ci:read"],"repos":["acme/widgets"]}' | jq -r .token)
echo "--- [Host (ci:read-only token, NO capabilities:read): default (no -provider)] ---"
host_grain "$TOK_CI_ONLY" ci test-history -repo acme/widgets "$NIGHTLY_TEST" 2>&1 || true

# Mint external-agent token WITHOUT ci:read (tasks:read only) -> must return forbidden!
TOK_NO_CI=$(api POST /api/external-agents/tokens '{"name":"tasks-read-only","scopes":["tasks:read"],"repos":["acme/widgets"]}' | jq -r .token)
echo "--- [Host (token LACKING ci:read): refusal] ---"
set +e
host_grain "$TOK_NO_CI" ci test-history -repo acme/widgets "$NIGHTLY_TEST" 2>&1
echo "exit=$?"
set -e

stop_env /tmp/walk-item3
echo "ITEM 3 COMPLETE!"
