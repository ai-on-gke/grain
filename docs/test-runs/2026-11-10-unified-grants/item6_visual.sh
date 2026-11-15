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

# item6_visual.sh <before|after>: Seeds a real daemon with many repository
# read/write lines, long repository/branch names, declared push: refs, and
# mixed-spelling/bare defaults, runs item6_layout_check.mjs across 16
# surface/width/theme cells, and captures 1440x900 & 390x844 light/dark
# screenshots + contact-sheet.png via ui/e2e/screenshot.mjs.
set -euo pipefail

PHASE="${1:-after}"
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
HEAD_BIN="${HEAD_BIN:-/home/debian/bin/grain}"
OUT_DIR="${OUT_DIR:-$HERE/out}"
SHOT_DIR="/var/tmp/unified-grants/screenshots-$PHASE"
WORK="/var/tmp/unified-grants/item6-$PHASE"
GHSIM_ADDR="127.0.0.1:4287"
PORT=4288

mkdir -p "$OUT_DIR" "$SHOT_DIR"

cleanup() {
  for pf in "$WORK/daemon.pid" "$WORK/ghsim.pid"; do
    if [[ -f "$pf" ]]; then
      pid=$(cat "$pf")
      kill "$pid" 2>/dev/null || true
      for _ in $(seq 30); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
    fi
  done
}
trap cleanup EXIT

rm -rf "$WORK"
mkdir -p "$WORK/bin" "$WORK/repos" "$WORK/data" "$WORK/home"

(cd "$ROOT" && go build -o "$WORK/bin/ghsim" "$HERE/ghsim.go")

LONG_A="acme-platform/extremely-long-shared-configuration-and-infrastructure-repository"
LONG_B="acme-platform/another-very-long-microservice-deployment-manifests-repository"
LONG_C="acme-platform/cross-region-observability-and-telemetry-collector-pipelines"

init_repo() {
  local owner=$1 name=$2 def=$3 extra=${4:-}
  local bare="$WORK/repos/$owner/$name.git"
  mkdir -p "$(dirname "$bare")"
  git init -q --bare -b "$def" "$bare"
  local tmp
  tmp=$(mktemp -d)
  git -C "$tmp" init -q -b "$def"
  echo "# $owner/$name" >"$tmp/README.md"
  if [[ "$owner/$name" == "acme/widgets" ]]; then
    mkdir -p "$tmp/.grain/templates"
    cat >"$tmp/.grain/templates/release.yml" <<'YAML'
title: Cut release with long push refs
body: Update release candidate and latest tag refs.
push:
  - refs/heads/release-candidate-2026-q4-long-branch
  - ref: refs/tags/latest
    force: true
YAML
    git -C "$tmp" add .grain/templates/release.yml
  fi
  git -C "$tmp" add README.md
  git -C "$tmp" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed"
  git -C "$tmp" push -q "$bare" "$def"
  if [[ -n "$extra" ]]; then
    git -C "$tmp" branch "$extra"
    git -C "$tmp" push -q "$bare" "$extra"
  fi
  rm -rf "$tmp"
}

init_repo acme widgets main release/v1
init_repo acme lib main
init_repo acme other trunk staging
init_repo acme-platform extremely-long-shared-configuration-and-infrastructure-repository main release/2026-11-10-extremely-long-integration-branch-name
init_repo acme-platform another-very-long-microservice-deployment-manifests-repository main
init_repo acme-platform cross-region-observability-and-telemetry-collector-pipelines main

"$WORK/bin/ghsim" -addr "$GHSIM_ADDR" -root "$WORK/repos" \
  -repo acme/widgets=main \
  -repo acme/lib=main \
  -repo acme/other=trunk \
  -repo "$LONG_A=main" \
  -repo "$LONG_B=main" \
  -repo "$LONG_C=main" \
  >"$WORK/ghsim.log" 2>&1 &
echo $! >"$WORK/ghsim.pid"

export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN

start_d() {
  "$HEAD_BIN" daemon -data-dir "$WORK/data" -ui-addr "127.0.0.1:$PORT" -sandbox-port-base=0 \
    -disable-native-sandboxes -ci=false -poll-interval 1s -as owner@example.com \
    -default-target-repo acme/widgets \
    -github-host "$GHSIM_ADDR" -github-insecure-http \
    >"$WORK/daemon.log" 2>&1 &
  echo $! >"$WORK/daemon.pid"
  for _ in $(seq 200); do
    if [[ -s "$WORK/data/local-api-token" ]] && curl -fs -o /dev/null -H "Authorization: Bearer $(tr -d '[:space:]' <"$WORK/data/local-api-token")" "http://127.0.0.1:$PORT/api/config"; then
      return 0
    fi
    sleep 0.1
  done
  exit 1
}

stop_d() {
  local pid
  pid=$(cat "$WORK/daemon.pid")
  kill "$pid" 2>/dev/null || true
  for _ in $(seq 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
  rm -f "$WORK/daemon.pid"
}

start_d
TOK=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
API="http://127.0.0.1:$PORT"
call() {
  local method=$1 path=$2 body=${3:-}
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOK")
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" --data "$body")
  curl "${args[@]}" "$API$path"
}

call PUT /api/github-tokens/default '{"value":"ghp_walkthrough_stub"}' >/dev/null
for r in acme/widgets acme/lib acme/other "$LONG_A" "$LONG_B" "$LONG_C"; do
  call POST /api/repos "{\"repo\":\"$r\"}" >/dev/null
done
call PUT /api/repos/acme/widgets/declarations '{"declarations":true}' >/dev/null

DECL_TMPL=""
for _ in $(seq 50); do
  DECL_TMPL=$(call GET /api/templates | jq -r '.[] | select(.declaredIn != null and .name == "release") | .id // empty')
  [[ -n "$DECL_TMPL" ]] && break
  sleep 0.25
done

READS_JSON=$(jq -cn --arg a "$LONG_A" --arg b "$LONG_B" --arg c "$LONG_C" '["acme/lib","acme/other",$a,$b,$c]')
WRITES_JSON=$(jq -cn --arg a "$LONG_A" --arg b "$LONG_B" '[{"repo":"acme/other","base":"staging"},{"repo":$a,"base":"release/2026-11-10-extremely-long-integration-branch-name"},{"repo":$b,"base":""}]')

RICH_RESP=$(call POST /api/tasks "$(jq -cn --argjson r "$READS_JSON" --argjson w "$WRITES_JSON" '{title:"Rich repository grants visual check", description:"§3 item 6", repo:"acme/widgets", base:"release/v1", approved:false, reads:$r, writes:$w, capabilities:["https://grain.dev/capabilities/api#tasks:read={\"repo\":\"acme/widgets\"}","https://grain.dev/capabilities/grain#source={}"]}')")
RICH_TASK=$(jq -r '.id // empty' <<<"$RICH_RESP")
[[ -n "$RICH_TASK" ]] || { echo "RICH_TASK failed: $RICH_RESP" >&2; exit 1; }

RICH_TMPL=$(call POST /api/templates "$(jq -cn --argjson r "$READS_JSON" --argjson w "$WRITES_JSON" '{name:"rich-repo-grants", title:"Rich template", description:"§3 item 6", repo:"acme/widgets", base:"release/v1", reads:$r, writes:$w, capabilities:["https://grain.dev/capabilities/api#tasks:read={\"repo\":\"acme/widgets\"}"]}')" | jq -r .id)

call POST /api/console-sessions "$(jq -cn --argjson r "$READS_JSON" --argjson w "$WRITES_JSON" '{id:"ops", title:"Ops", readsAll:false, reads:$r, writes:$w}')" >/dev/null
sleep 1.5

# Seed mixed-spelling & bare self-debug defaults (deployment canonical + bare self-debug,
# repo unmigrated legacy api:tasks:read@acme/widgets) so New task overlay before/after
# visibly demonstrates F1 alongside Item 6!
stop_d
sqlite3 "$WORK/data/store/grain.db" <<'SQL'
UPDATE grain_config SET default_capabilities = '["https://grain.dev/capabilities/api#tasks:read={\"repo\":\"acme/widgets\"}","self-debug"]' WHERE id = 1;
INSERT INTO repo_config (owner, name, default_capabilities, ci_capabilities, declarations)
VALUES ('acme', 'widgets', '["api:tasks:read@acme/widgets"]', '[]', 1)
ON CONFLICT (owner, name) DO UPDATE SET default_capabilities = excluded.default_capabilities;
INSERT INTO task_grant (task_id, capability, via) VALUES ('2', 'self-debug', 'label') ON CONFLICT DO NOTHING;
SQL
start_d
TOK=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
sleep 1.5

IDS_JSON=$(jq -cn --arg richTask "$RICH_TASK" --arg richTemplate "$RICH_TMPL" '{richTask:$richTask, richTemplate:$richTemplate}')

echo "== [$PHASE] Running Item 6 bounding-box layout check =="
(cd "$ROOT/ui" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  node "$HERE/item6_layout_check.mjs" "$API" "$TOK" "$IDS_JSON" "$OUT_DIR/item6-layout-$PHASE.json")

echo "== [$PHASE] Capturing 1440x900 & 390x844 light/dark screenshots =="
(cd "$ROOT/ui" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  node e2e/screenshot.mjs \
  --url "$API" --code "$TOK" --full-page \
  --pages "/tasks/${RICH_TASK}#task-${RICH_TASK}-grants-label,/processes/templates/${RICH_TMPL}#template-grants-label" \
  --out "$SHOT_DIR/pages")

(cd "$ROOT/ui" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  node e2e/screenshot.mjs \
  --url "$API" --code "$TOK" \
  --pages "/" \
  --click 'role=button[name=/New task$/]' \
  --click 'role=combobox[name=/^Repos/]' \
  --click 'role=option[name=/acme\/widgets/]' \
  --out "$SHOT_DIR/new-task")

(cd "$ROOT/ui" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  node e2e/screenshot.mjs \
  --url "$API" --code "$TOK" \
  --pages "/session/director" \
  --click 'role=button[name="Edit Director capabilities"]' \
  --click 'role=button[name="Add grant…"]' \
  --click 'role=menuitem[name=/Read a repository/]' \
  --click 'role=textbox[name="Repo to read"]' \
  --out "$SHOT_DIR/director")

echo "[$PHASE] Visual check complete. Contact sheets in $SHOT_DIR"
