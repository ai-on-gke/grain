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

# run_item1.sh: real-daemon walkthrough for §10 Item 1 (narrowed CI API tokens
# for grain_config.ci_api_repos). Starts a multi-repo ghsim serving acme/widgets
# (ci_mode: github) and acme/secret-other (ci_mode: grain), starts a real
# `grain daemon -host-sandboxes -sandbox-port-base=0`, verifies admission fails
# before ci_api_repos lists acme/widgets, enables ci_api_repos live, and runs
# probe.sh inside a real acme/widgets scheduled grain-ci job holding a minted
# narrowed CI API token.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
WORK=${WORK:-/var/tmp/walk-ciapi-item1}
DAEMON_PORT=${DAEMON_PORT:-4290}
GHSIM_PORT=${GHSIM_PORT:-4294}
DAEMON="http://127.0.0.1:${DAEMON_PORT}"
GHSIM="127.0.0.1:${GHSIM_PORT}"
SECRET_WORD="cobalt-zebra-secret"

cleanup() {
  for f in "$WORK/daemon.pid" "$WORK/ghsim.pid"; do
    if [[ -f "$f" ]]; then
      local pid; pid=$(cat "$f")
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 40); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
      rm -f "$f"
    fi
  done
}
trap cleanup EXIT

rm -rf "$WORK"
mkdir -p "$WORK/data" "$WORK/sandboxes" "$WORK/bin" "$WORK/home" "$WORK/repos" "$WORK/sync"

echo "==> Building multi-repo ghsim"
(cd "$ROOT" && go build -o "$WORK/bin/ghsim" "$HERE/ghsim.go")

echo "==> Seeding acme/widgets and acme/secret-other bare repos"
git init -q --bare -b main "$WORK/repos/widgets.git"
git -C "$WORK/repos/widgets.git" config http.receivepack true
git init -q --bare -b main "$WORK/repos/secret-other.git"
git -C "$WORK/repos/secret-other.git" config http.receivepack true

# Seed acme/widgets
tmp=$(mktemp -d)
git -C "$tmp" init -q -b main
mkdir -p "$tmp/.grain/workflows" "$tmp/.grain/schedules" "$tmp/.grain/templates"
echo "# widgets" >"$tmp/README.md"

cat >"$tmp/.grain/workflows/probe.yml" <<EOF
name: probe
on:
  schedule:
    - cron: "0 0 * * *"
  workflow_dispatch:
jobs:
  own-artifact:
    artifacts:
      - name: widgets-report
        path: widgets-report.txt
    steps:
      - name: Produce own artifact
        run: |
          echo "widgets-ok-report" > widgets-report.txt
          echo "own-artifact-step-done"
          exit 1
  probe-release-gate:
    capabilities:
      - https://grain.dev/capabilities/api#release-gate-read
    steps:
      - name: Export release-gate token and wait for probe-all
        run: |
          if [ -z "\${GRAIN_API_TOKEN:-}" ]; then
            echo "NO_RELEASE_GATE_TOKEN" > "$WORK/sync/rg.err"
            exit 1
          fi
          printf '%s' "\$GRAIN_API_TOKEN" > "$WORK/sync/rg.token"
          for i in \$(seq 120); do
            [ -f "$WORK/sync/probe.done" ] && exit 0
            sleep 0.5
          done
          exit 0
  probe-no-rg:
    capabilities:
      - https://grain.dev/capabilities/api#tasks:from-template
    steps:
      - name: Export from-template-only token and wait for probe-all
        run: |
          if [ -z "\${GRAIN_API_TOKEN:-}" ]; then
            exit 1
          fi
          printf '%s' "\$GRAIN_API_TOKEN" > "$WORK/sync/norg.token"
          for i in \$(seq 120); do
            [ -f "$WORK/sync/probe.done" ] && exit 0
            sleep 0.5
          done
          exit 0
  probe-all:
    capabilities:
      - https://grain.dev/capabilities/api#ci:read
      - https://grain.dev/capabilities/api#tasks:read
      - https://grain.dev/capabilities/api#tasks:from-template
      - https://grain.dev/capabilities/api#sandboxes:read
      - https://grain.dev/capabilities/api#templates:read
      - https://grain.dev/capabilities/api#suites:read
      - https://grain.dev/capabilities/api#releases:read
      - https://grain.dev/capabilities/api#repos:read
      - https://grain.dev/capabilities/api#schedules:read
      - https://grain.dev/capabilities/api#dispatch:read
    steps:
      - name: Run probe.sh with real minted CI API token
        run: |
          set -euo pipefail
          if [ -z "\${GRAIN_API_TOKEN:-}" ] || [ -z "\${GRAIN_API_URL:-}" ]; then
            echo "PROBE: NO_API_TOKEN"
            exit 1
          fi
          echo "PROBE: GOT_API_TOKEN url=\$GRAIN_API_URL"
          for i in \$(seq 60); do
            [ -s "$WORK/sync/rg.token" ] && [ -s "$WORK/sync/norg.token" ] && break
            sleep 0.25
          done
          set -a
          . "$WORK/sync/fixtures.env"
          set +a
          export RELEASE_GATE_TOKEN="\$(cat "$WORK/sync/rg.token" 2>/dev/null || true)"
          export NORG_TOKEN="\$(cat "$WORK/sync/norg.token" 2>/dev/null || true)"
          export OUT_JSON="$WORK/probe-results.json"
          set +e
          bash "$HERE/probe.sh" >"$WORK/probe-table.md" 2>"$WORK/probe.err"
          rc=\$?
          set -e
          echo "\$rc" > "$WORK/sync/probe.rc"
          touch "$WORK/sync/probe.done"
          cat "$WORK/probe-table.md"
          exit 0
EOF

cat >"$tmp/.grain/schedules/probe.yml" <<'EOF'
name: Widgets CI API probe
workflow: probe
grain-ci: true
every:
  minutes: 20
EOF

# Templates covering every grant class
cat >"$tmp/.grain/templates/clean.yml" <<'EOF'
name: Clean template
title: Clean widgets task
body: Plain task with no extra grants.
EOF

cat >"$tmp/.grain/templates/own-ci-read.yml" <<'EOF'
name: Own CI read
title: Own-repo CI read task
body: Holds api:ci:read scoped to acme/widgets.
grants:
  - capability: https://grain.dev/capabilities/api#ci:read
    with:
      repo: acme/widgets
EOF

cat >"$tmp/.grain/templates/own-grain-ci.yml" <<'EOF'
name: Own grain CI
title: Own grain#ci task
body: Holds grain#ci.
grants:
  - https://grain.dev/capabilities/grain#ci
EOF

cat >"$tmp/.grain/templates/cross-read.yml" <<'EOF'
name: Cross-repo read
title: Cross read task
body: Attempts to read acme/secret-other.
grants:
  - capability: https://grain.dev/capabilities/repo#read
    with:
      repo: acme/secret-other
EOF

cat >"$tmp/.grain/templates/cross-write.yml" <<'EOF'
name: Cross-repo write
title: Cross write task
body: Attempts to write acme/secret-other.
grants:
  - capability: https://grain.dev/capabilities/repo#write
    with:
      repo: acme/secret-other
EOF

cat >"$tmp/.grain/templates/cross-api-grant.yml" <<'EOF'
name: Cross-repo API grant
title: Cross API grant task
body: Attempts api:tasks:read on acme/secret-other.
grants:
  - capability: https://grain.dev/capabilities/api#tasks:read
    with:
      repo: acme/secret-other
EOF

cat >"$tmp/.grain/templates/unscoped-api-grant.yml" <<'EOF'
name: Unscoped API grant
title: Unscoped API grant task
body: Declares unscoped api:tasks:read.
grants:
  - https://grain.dev/capabilities/api#tasks:read
EOF

cat >"$tmp/.grain/templates/non-ci-write-grant.yml" <<'EOF'
name: Non-CI write grant
title: Non-CI write grant task
body: Declares api:repos:write scoped to acme/widgets.
grants:
  - capability: https://grain.dev/capabilities/api#repos:write
    with:
      repo: acme/widgets
EOF

cat >"$tmp/.grain/templates/grain-daemon-grant.yml" <<'EOF'
name: Grain daemon grant
title: Grain source daemon grant task
body: Declares grain#source.
grants:
  - https://grain.dev/capabilities/grain#source
EOF

cat >"$tmp/.grain/templates/dangerous-grant.yml" <<'EOF'
name: Dangerous grant
title: Dangerous settings write task
body: Declares api:settings:write.
grants:
  - https://grain.dev/capabilities/api#settings:write
EOF

cat >"$tmp/.grain/templates/open-network.yml" <<'EOF'
name: Open network
title: Open network task
body: "Declares network: open."
network: open
EOF

cat >"$tmp/.grain/templates/admission-bypass.yml" <<'EOF'
name: Admission bypass
title: Admission bypass task
body: "Declares admission: bypass."
admission: bypass
EOF

cat >"$tmp/.grain/templates/high-priority.yml" <<'EOF'
name: High priority
title: High priority task
body: "Declares priority: 9."
priority: 9
EOF

cat >"$tmp/.grain/templates/keep-running.yml" <<'EOF'
name: Keep running
title: Keep running task
body: "Declares keep-running: true."
keep-running: true
EOF

cat >"$tmp/.grain/templates/declared-push.yml" <<'EOF'
name: Declared push
title: Declared push task
body: Declares push refs.
push:
  - ref: refs/tags/v*
EOF

git -C "$tmp" add -A
git -C "$tmp" -c user.name="Seed" -c user.email="seed@example.com" commit -qm "seed widgets"
git -C "$tmp" push -q "$WORK/repos/widgets.git" main
WIDGETS_SHA=$(git -C "$tmp" rev-parse HEAD)
rm -rf "$tmp"

# Seed acme/secret-other
tmp=$(mktemp -d)
git -C "$tmp" init -q -b main
mkdir -p "$tmp/.grain/workflows" "$tmp/.grain/schedules" "$tmp/.grain/templates"
echo "# secret-other ($SECRET_WORD)" >"$tmp/README.md"
cat >"$tmp/.grain/workflows/secret-ci.yml" <<EOF
name: secret-ci
on:
  push:
    branches: [main]
  workflow_dispatch:
jobs:
  secret-job:
    artifacts:
      - name: secret-artifact
        path: secret-artifact.txt
    steps:
      - name: Write secret log and artifact
        run: |
          echo "secret-artifact-${SECRET_WORD}" > secret-artifact.txt
          echo "secret-log-line-${SECRET_WORD}"
          exit 1
EOF
cat >"$tmp/.grain/templates/secret-tmpl.yml" <<EOF
name: Secret other template (${SECRET_WORD})
title: Secret other task (${SECRET_WORD})
body: Secret body ${SECRET_WORD}
EOF
cat >"$tmp/.grain/schedules/secret-sched.yml" <<EOF
name: Secret other schedule (${SECRET_WORD})
template: secret-tmpl
every:
  minutes: 20
EOF
git -C "$tmp" add -A
git -C "$tmp" -c user.name="Seed" -c user.email="seed@example.com" commit -qm "seed secret-other"
git -C "$tmp" push -q "$WORK/repos/secret-other.git" main
OTHER_SHA=$(git -C "$tmp" rev-parse HEAD)
rm -rf "$tmp"

echo "==> Starting multi-repo ghsim on $GHSIM"
"$WORK/bin/ghsim" \
  -addr "$GHSIM" \
  -repo "acme/widgets=$WORK/repos/widgets.git" \
  -repo "acme/secret-other=$WORK/repos/secret-other.git" \
  -default main >"$WORK/ghsim.log" 2>&1 &
echo $! >"$WORK/ghsim.pid"
for _ in $(seq 50); do
  curl -sf "http://$GHSIM/repos/acme/widgets" >/dev/null && break
  sleep 0.1
done

# Seed an own GitHub Actions run on acme/widgets and a foreign one on acme/secret-other
curl -fsS -X POST -H "Content-Type: application/json" "http://$GHSIM/_sim/seed-actions" -d "{
  \"repo\": \"acme/widgets\",
  \"runs\": [{
    \"id\": 9002,
    \"name\": \"Widgets Actions\",
    \"path\": \".github/workflows/ci.yml\",
    \"headBranch\": \"main\",
    \"headSha\": \"${WIDGETS_SHA}\",
    \"event\": \"push\",
    \"status\": \"completed\",
    \"conclusion\": \"failure\",
    \"runAttempt\": 1,
    \"jobs\": [{
      \"name\": \"widgets-gha-job\",
      \"conclusion\": \"failure\",
      \"log\": \"widgets-gha-log-ok\\n\"
    }]
  }]
}" >/dev/null
curl -fsS -X POST -H "Content-Type: application/json" "http://$GHSIM/_sim/seed-actions" -d "{
  \"repo\": \"acme/secret-other\",
  \"runs\": [{
    \"id\": 9001,
    \"name\": \"Secret Actions\",
    \"path\": \".github/workflows/secret.yml\",
    \"headBranch\": \"main\",
    \"headSha\": \"${OTHER_SHA}\",
    \"event\": \"push\",
    \"status\": \"completed\",
    \"conclusion\": \"failure\",
    \"runAttempt\": 1,
    \"jobs\": [{
      \"name\": \"secret-gha-job\",
      \"conclusion\": \"failure\",
      \"log\": \"secret-gha-log-${SECRET_WORD}\\n\"
    }]
  }]
}" >/dev/null

echo "ghp_walkthrough" >"$WORK/bot.token"
echo '{"directConfigWrites": true}' >"$WORK/data/state-repo.json"
cat >"$WORK/seed-git.yaml" <<EOF
git:
  - credential: bot
    token_file: $WORK/bot.token
EOF
"$GRAIN" connections -data-dir "$WORK/data" import "$WORK/seed-git.yaml" >"$WORK/connections.log" 2>&1

echo "==> Starting real grain daemon on $DAEMON"
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
  -repo acme/widgets \
  -repo acme/secret-other \
  -default-target-repo acme/widgets \
  -poll-interval 1s \
  -max-workers 1 \
  -github-host "$GHSIM" \
  -github-insecure-http >>"$WORK/daemon.log" 2>&1 </dev/null &
echo $! >"$WORK/daemon.pid"

CODE=""
for _ in $(seq 150); do
  if [[ -s "$WORK/data/local-api-token" ]]; then
    CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
    if curl -sf -H "Authorization: Bearer $CODE" "$DAEMON/api/config" >/dev/null 2>&1; then
      break
    fi
  fi
  sleep 0.2
done
[[ -n "$CODE" ]] || { echo "daemon failed to start"; tail -n 40 "$WORK/daemon.log"; exit 1; }

api() {
  local method=$1 path=$2 body=${3:-}
  local out code
  if [[ -n "$body" ]]; then
    out=$(curl -sS -w '\n%{http_code}' -X "$method" -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" -d "$body" "$DAEMON$path")
  else
    out=$(curl -sS -w '\n%{http_code}' -X "$method" -H "Authorization: Bearer $CODE" "$DAEMON$path")
  fi
  code=${out##*$'\n'}
  out=${out%$'\n'*}
  if [[ "$code" -lt 200 || "$code" -ge 300 ]]; then
    echo "API ERROR $method $path -> HTTP $code: $out" >&2
    return 1
  fi
  printf '%s\n' "$out"
}

# Pause agent dispatch so filed tasks stay queued/proposed for inspection
api POST /api/pipeline-pause '{"paused":true,"reason":"item1 walkthrough"}' >/dev/null
# acme/widgets stays ci_mode=github; acme/secret-other uses ci_mode=grain
api PUT /api/repos/acme/widgets/ci-mode '{"ciMode":"github"}' >/dev/null
api PUT /api/repos/acme/secret-other/ci-mode '{"ciMode":"grain"}' >/dev/null
api PUT /api/repos/acme/widgets/declarations '{"declarations":true}' >/dev/null
api PUT /api/repos/acme/secret-other/declarations '{"declarations":true}' >/dev/null

echo "==> Waiting for repo declarations to load"
for _ in $(seq 60); do
  if api GET /api/schedules | jq -e 'any(.[]; .id | contains("probe"))' >/dev/null 2>&1 \
     && api GET /api/schedules | jq -e 'any(.[]; .repo == "acme/secret-other")' >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done

PROBE_SCHED_ID="repo:schedule:acme:widgets:probe"
FOREIGN_SCHED_ID=$(api GET /api/schedules | jq -r '.[] | select(.repo == "acme/secret-other") | .id' | head -n1)
FOREIGN_TEMPLATE_ID=$(api GET /api/templates | jq -r '.[] | select(.repo == "acme/secret-other") | .id' | head -n1)
echo "PROBE_SCHED_ID=$PROBE_SCHED_ID FOREIGN_SCHED_ID=$FOREIGN_SCHED_ID FOREIGN_TEMPLATE_ID=$FOREIGN_TEMPLATE_ID"

echo "==> Seeding cross-repo fixtures (tasks, attachments, notifications, persistent sandboxes, foreign CI run, cross-repo template)"
OWN_ATT_B64=$(printf 'widgets-attachment-ok' | base64 -w0)
FOREIGN_ATT_B64=$(printf "foreign-attachment-${SECRET_WORD}" | base64 -w0)

OWN_TASK_JSON=$(api POST /api/tasks "{
  \"repo\":\"acme/widgets\",
  \"title\":\"Widgets own task\",
  \"description\":\"Own task description\",
  \"externalRef\":\"WALK-42\",
  \"approved\":true,
  \"attachments\":[{\"filename\":\"own.txt\",\"contentType\":\"text/plain\",\"content\":\"${OWN_ATT_B64}\"}]
}")
OWN_TASK_ID=$(jq -r '.id' <<<"$OWN_TASK_JSON")
OWN_ATTACH_ID=$(jq -r '.attachments[0].id // empty' <<<"$OWN_TASK_JSON")

FOREIGN_TASK_JSON=$(api POST /api/tasks "{
  \"repo\":\"acme/secret-other\",
  \"title\":\"Foreign secret task (${SECRET_WORD})\",
  \"description\":\"Foreign secret description (${SECRET_WORD})\",
  \"externalRef\":\"WALK-42\",
  \"approved\":true,
  \"attachments\":[{\"filename\":\"secret.txt\",\"contentType\":\"text/plain\",\"content\":\"${FOREIGN_ATT_B64}\"}]
}")
FOREIGN_TASK_ID=$(jq -r '.id' <<<"$FOREIGN_TASK_JSON")
FOREIGN_ATTACH_ID=$(jq -r '.attachments[0].id // empty' <<<"$FOREIGN_TASK_JSON")

NOREPO_TASK_JSON=$(api POST /api/tasks "{
  \"noRepo\":true,
  \"title\":\"Standalone no-repo task (${SECRET_WORD}-norepo)\",
  \"description\":\"No-repo secret description (${SECRET_WORD})\",
  \"approved\":true
}")
NOREPO_TASK_ID=$(jq -r '.id' <<<"$NOREPO_TASK_JSON")
echo "Tasks: own=${OWN_TASK_ID} foreign=${FOREIGN_TASK_ID} norepo=${NOREPO_TASK_ID} (norepo.repo=$(jq -r '.repo // ""' <<<"$NOREPO_TASK_JSON"))"

# Foreign open pending-release notification (v9.9.0) + foreign warning notification
api POST /api/notifications "{
  \"subject\":\"release.pending:v9.9.0\",
  \"title\":\"Approve v9.9.0 (${SECRET_WORD})\",
  \"body\":\"Foreign pending release body (${SECRET_WORD})\",
  \"severity\":\"warning\",
  \"repo\":\"acme/secret-other\"
}" >/dev/null

# UI-created template targeting acme/widgets that reads acme/secret-other with secret word in title
api POST /api/templates "{
  \"name\":\"Cross-repo UI template (${SECRET_WORD})\",
  \"title\":\"UI template reading foreign repo (${SECRET_WORD})\",
  \"description\":\"Reads acme/secret-other (${SECRET_WORD})\",
  \"repo\":\"acme/widgets\",
  \"reads\":[\"acme/secret-other\"]
}" >/dev/null

# Two persistent sandboxes (disabled so they don't boot, sorted alpha-other before zeta-widgets!)
sqlite3 "$WORK/data/store/grain.db" "
INSERT INTO persistent_sandbox (name, repo, command, enabled)
VALUES
  ('alpha-other-secret-svc', 'acme/secret-other', 'echo ${SECRET_WORD}', 0),
  ('zeta-widgets-svc', 'acme/widgets', 'echo widgets-svc', 0);
"

# Foreign grain CI run producing a failed job + artifact
FOREIGN_RUN_ID=$(api POST /api/ci/runs '{"repo":"acme/secret-other","ref":"main","event":"workflow_dispatch"}' | jq -r '.id')
for _ in $(seq 120); do
  c=$(api GET "/api/ci/runs/${FOREIGN_RUN_ID}" | jq -r '.conclusion // empty')
  [[ -n "$c" ]] && break
  sleep 0.5
done
echo "FOREIGN_RUN_ID=$FOREIGN_RUN_ID conclusion=$c"
FOREIGN_ARTIFACT_PATH=$(api GET "/api/ci/runs/${FOREIGN_RUN_ID}/artifacts" | jq -r '(.artifacts // [])[0] | if . then "\(.job)/\(.name)" else "" end')
echo "FOREIGN_ARTIFACT_PATH=$FOREIGN_ARTIFACT_PATH"

# Step A: Prove admission gate BEFORE acme/widgets is in grain_config.ci_api_repos
echo "==> Phase 1: trigger probe schedule BEFORE ci_api_repos lists acme/widgets"
cat >"$WORK/sync/fixtures.env" <<EOF
OWN_REPO=acme/widgets
FOREIGN_REPO=acme/secret-other
OWN_TASK_ID=${OWN_TASK_ID}
FOREIGN_TASK_ID=${FOREIGN_TASK_ID}
NOREPO_TASK_ID=${NOREPO_TASK_ID}
OWN_ATTACH_ID=${OWN_ATTACH_ID}
FOREIGN_ATTACH_ID=${FOREIGN_ATTACH_ID}
FOREIGN_RUN_ID=${FOREIGN_RUN_ID}
FOREIGN_ARTIFACT_PATH=${FOREIGN_ARTIFACT_PATH}
OWN_GHA_RUN_ID=gha:acme:widgets:9002
FOREIGN_GHA_RUN_ID=gha:acme:secret-other:9001
FOREIGN_SCHED_ID=${FOREIGN_SCHED_ID}
FOREIGN_SECRET_WORD=${SECRET_WORD}
FOREIGN_SANDBOX_NAME=alpha-other-secret-svc
FOREIGN_TEMPLATE_ID=${FOREIGN_TEMPLATE_ID}
EXT_REF=WALK-42
EOF

api POST "/api/schedules/${PROBE_SCHED_ID}/run" '{}' >/dev/null
PRE_RUN_ID=""
for _ in $(seq 60); do
  PRE_RUN_ID=$(api GET "/api/ci/runs?repo=acme/widgets" | jq -r '(.runs // .)[0].id // empty')
  [[ -n "$PRE_RUN_ID" ]] && break
  sleep 0.5
done
for _ in $(seq 120); do
  pre_c=$(api GET "/api/ci/runs/${PRE_RUN_ID}" | jq -r '.conclusion // empty')
  [[ -n "$pre_c" ]] && break
  sleep 0.5
done
echo "Phase 1 (before ci_api_repos) run=${PRE_RUN_ID} conclusion=${pre_c}"
api GET "/api/ci/runs/${PRE_RUN_ID}" | jq -c '[.jobs[] | {name, conclusion, error, warning}]'

# Step B: Add acme/widgets to grain_config.ci_api_repos (live, no restart!)
echo "==> Phase 2: set grain_config.ci_api_repos = 'acme/widgets' live (no daemon restart)"
sqlite3 "$WORK/data/store/grain.db" "UPDATE grain_config SET ci_api_repos = 'acme/widgets' WHERE id = 1;"
echo "grain_config.ci_api_repos=$(sqlite3 "$WORK/data/store/grain.db" 'SELECT ci_api_repos FROM grain_config WHERE id = 1;')"

# Push an empty commit to acme/widgets so grain CI runs fresh (not cached from Phase 1)
tmp=$(mktemp -d)
git clone -q "http://$GHSIM/acme/widgets.git" "$tmp"
git -C "$tmp" -c user.name="Seed" -c user.email="seed@example.com" commit -q --allow-empty -m "trigger phase 2 probe"
git -C "$tmp" push -q origin main
rm -rf "$tmp"

# Update OWN_RUN_ID to PRE_RUN_ID (which ran own-artifact and succeeded on acme/widgets!)
OWN_ARTIFACT_PATH=$(api GET "/api/ci/runs/${PRE_RUN_ID}/artifacts" | jq -r '(.artifacts // [])[0] | if . then "\(.job)/\(.name)" else "" end')
cat >>"$WORK/sync/fixtures.env" <<EOF
OWN_RUN_ID=${PRE_RUN_ID}
OWN_ARTIFACT_PATH=${OWN_ARTIFACT_PATH}
EOF
echo "OWN_RUN_ID=$PRE_RUN_ID OWN_ARTIFACT_PATH=$OWN_ARTIFACT_PATH"

# Wait for the reconcile loop to settle Phase 1's review task and close it so the schedule can fire again
for _ in $(seq 30); do
  for tid in $(api GET "/api/tasks?repo=acme/widgets" | jq -r --arg own "$OWN_TASK_ID" '.[] | select(.id != $own and .state != "closed") | .id'); do
    api POST "/api/tasks/${tid}/close" '{"reason":"close phase 1 review task before phase 2"}' >/dev/null || true
  done
  if api POST "/api/schedules/${PROBE_SCHED_ID}/run" '{}' >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
POST_RUN_ID=""
for _ in $(seq 60); do
  cand=$(api GET "/api/ci/runs?repo=acme/widgets" | jq -r '(.runs // .)[0].id // empty')
  if [[ -n "$cand" && "$cand" != "$PRE_RUN_ID" ]]; then
    POST_RUN_ID="$cand"
    break
  fi
  sleep 0.5
done
echo "Waiting for Phase 2 run=${POST_RUN_ID}..."
for _ in $(seq 180); do
  post_c=$(api GET "/api/ci/runs/${POST_RUN_ID}" | jq -r '.conclusion // empty')
  [[ -n "$post_c" ]] && break
  sleep 0.5
done
echo "Phase 2 (with ci_api_repos=acme/widgets) run=${POST_RUN_ID} conclusion=${post_c}"
api GET "/api/ci/runs/${POST_RUN_ID}" | jq -c '[.jobs[] | {name, conclusion, error, warning}]'

if [[ -f "$WORK/probe-table.md" ]]; then
  echo ""
  echo "=== PROBE TABLE ==="
  cat "$WORK/probe-table.md"
  echo "=== PROBE EXIT CODE: $(cat "$WORK/sync/probe.rc" 2>/dev/null || echo unknown) ==="
else
  echo "ERROR: probe-table.md was not created! Step output:"
  sqlite3 "$WORK/data/store/grain.db" "SELECT job_id, name, output FROM ci_step WHERE run_id='${POST_RUN_ID}'"
  echo "daemon log tail:"
  tail -n 50 "$WORK/daemon.log"
  exit 1
fi
