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

# item2_5_walkthrough.sh: Runs §3 item 2 (repository read/write lines end to end
# in Chromium across all 5 surfaces, focus return, #target, declared push: refs,
# and CI-only repo#read menu exclusion) and §3 item 5 (bad .grain/templates/x.yml
# declarations pushed to GitHub simulator repo's default branch: grain declarations
# check refusals, previous declarations staying live, and RepoPage surfacing last_error).
set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
HEAD_BIN="${HEAD_BIN:-/home/debian/bin/grain}"
OUT_DIR="${OUT_DIR:-$HERE/out}"
WORK="/var/tmp/unified-grants/item2-5"
GHSIM_ADDR="127.0.0.1:4283"
PORT=4284

mkdir -p "$OUT_DIR"

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

init_repo() {
  local owner=$1 name=$2 def=$3 extra=${4:-}
  local bare="$WORK/repos/$owner/$name.git"
  mkdir -p "$(dirname "$bare")"
  git init -q --bare -b "$def" "$bare"
  local clone="$WORK/clone-$name"
  git init -q -b "$def" "$clone"
  git -C "$clone" remote add origin "$bare"
  echo "# $owner/$name" >"$clone/README.md"
  git -C "$clone" add README.md
  git -C "$clone" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed"
  git -C "$clone" push -q origin "$def"
  if [[ -n "$extra" ]]; then
    git -C "$clone" checkout -q -b "$extra"
    echo "$extra" >>"$clone/README.md"
    git -C "$clone" commit -qam "chore: $extra"
    git -C "$clone" push -q origin "$extra"
    git -C "$clone" checkout -q "$def"
  fi
}

init_repo acme widgets main release/v1
init_repo acme lib main
init_repo acme other trunk staging

# Push a valid declared template with `push:` refs to acme/widgets so Item 2 can
# verify read-only push: lines AND Item 5 can verify it stays live across bad syncs!
CLONE="$WORK/clone-widgets"
mkdir -p "$CLONE/.grain/templates"
cat >"$CLONE/.grain/templates/release.yml" <<'YAML'
title: Cut a release
body: Cut the next release and update candidate and latest refs.
push:
  - refs/heads/candidate
  - ref: refs/tags/latest
    force: true
YAML
git -C "$CLONE" add .grain/templates/release.yml
git -C "$CLONE" -c user.name=seed -c user.email=seed@example.com commit -qm "feat: declare release template with push refs"
git -C "$CLONE" push -q origin main

"$WORK/bin/ghsim" -addr "$GHSIM_ADDR" -root "$WORK/repos" \
  -repo acme/widgets=main -repo acme/lib=main -repo acme/other=trunk \
  >"$WORK/ghsim.log" 2>&1 &
echo $! >"$WORK/ghsim.pid"

export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN

"$HEAD_BIN" daemon -data-dir "$WORK/data" -ui-addr "127.0.0.1:$PORT" -sandbox-port-base=0 \
  -disable-native-sandboxes -ci=false -poll-interval 1s -as owner@example.com \
  -default-target-repo acme/widgets \
  -github-host "$GHSIM_ADDR" -github-insecure-http \
  >"$WORK/daemon.log" 2>&1 &
echo $! >"$WORK/daemon.pid"

for _ in $(seq 200); do
  if [[ -s "$WORK/data/local-api-token" ]] && curl -fs -o /dev/null -H "Authorization: Bearer $(tr -d '[:space:]' <"$WORK/data/local-api-token")" "http://127.0.0.1:$PORT/api/config"; then
    break
  fi
  sleep 0.1
done

TOK=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
API="http://127.0.0.1:$PORT"
call() {
  local method=$1 path=$2 body=${3:-}
  local args=(-sS -X "$method" -H "Authorization: Bearer $TOK")
  [[ -n "$body" ]] && args+=(-H "Content-Type: application/json" --data "$body")
  curl "${args[@]}" "$API$path"
}

call PUT /api/github-tokens/default '{"value":"ghp_walkthrough_stub"}' >/dev/null
for r in acme/widgets acme/lib acme/other; do
  call POST /api/repos "{\"repo\":\"$r\"}" >/dev/null
done
call PUT /api/repos/acme/widgets/declarations '{"declarations":true}' >/dev/null

# Wait for declaration sync of acme/widgets:release.
DECL_TMPL=""
for _ in $(seq 50); do
  DECL_TMPL=$(call GET /api/templates | jq -r '.[] | select(.declaredIn != null and .name == "release") | .id // empty')
  [[ -n "$DECL_TMPL" ]] && break
  sleep 0.25
done
[[ -n "$DECL_TMPL" ]] || { echo "declared release template did not sync; log:" >&2; tail -n 30 "$WORK/daemon.log" >&2; exit 1; }
echo "Synced declared template: $DECL_TMPL"

TPL=$(call POST /api/templates '{"name":"item2-template","title":"Item 2 Template","description":"§3 item 2","repo":"acme/widgets"}' | jq -r .id)
SCH=$(call POST /api/schedules '{"name":"item2-schedule","title":"Item 2 Schedule","description":"§3 item 2","repo":"acme/widgets","recurrence":{"kind":"daily","timeOfDay":"04:00"}}' | jq -r .id)
call POST /api/console-sessions '{"id":"ops","title":"Ops","readsAll":false}' >/dev/null
PUSH_RESP=$(call POST /api/tasks "{\"templateId\":\"$DECL_TMPL\",\"approved\":false}")
PUSH_TASK=$(jq -r '.id // empty' <<<"$PUSH_RESP")
[[ -n "$PUSH_TASK" ]] || { echo "filing from declared template failed: $PUSH_RESP" >&2; exit 1; }
sleep 1.5

echo "== Running Item 2 Playwright walkthrough =="
ARGS_JSON=$(jq -n --arg templateId "$TPL" --arg scheduleId "$SCH" --arg pushTaskId "$PUSH_TASK" '{templateId:$templateId, scheduleId:$scheduleId, pushTaskId:$pushTaskId}')
(cd "$ROOT/ui" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  node "$HERE/item2_ui.mjs" item2 "$API" "$TOK" "$ARGS_JSON" "$OUT_DIR/item2-ui.json")

echo "== Running Item 5: Declaration refusals in a real sync =="
# Case 5a: .grain/templates/x.yml naming repo#target
cat >"$CLONE/.grain/templates/x.yml" <<'YAML'
title: Bad target grant
body: Should be refused at parse time.
capabilities:
  - capability: https://grain.dev/capabilities/repo#target
    with:
      repo: acme/widgets
      base: main
YAML
git -C "$CLONE" add .grain/templates/x.yml
git -C "$CLONE" -c user.name=seed -c user.email=seed@example.com commit -qm "test: add bad repo#target template"
git -C "$CLONE" push -q origin main
HEAD_5A=$(git -C "$CLONE" rev-parse HEAD)

set +e
CHECK_5A=$(cd "$CLONE" && "$HEAD_BIN" declarations check -repo acme/widgets 2>&1)
RC_5A=$?
set -e
echo "5a grain declarations check (exit=$RC_5A): $CHECK_5A"

# Backdate checked_at so reconcileDeclarations polls immediately instead of waiting 2m.
sqlite3 "$WORK/data/store/grain.db" "UPDATE repo_declaration_sync SET checked_at = '2026-11-10 00:00:00 +0000 UTC';"
for _ in $(seq 50); do
  LAST_ERR=$(call GET /api/repos/acme/widgets/declarations | jq -r '.declarationsStatus.error // empty')
  [[ "$LAST_ERR" == *"repo#target"* ]] && break
  sleep 0.25
done
DECL_STATUS_5A=$(call GET /api/repos/acme/widgets/declarations)
LIVE_TMPL_5A=$(call GET /api/templates | jq -c '[.[] | select(.declaredIn != null) | {id, name}]')
(cd "$ROOT/ui" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  node "$HERE/item2_ui.mjs" item5-error "$API" "$TOK" "{}" "$OUT_DIR/item5a-ui.json")

# Case 5b: .grain/templates/x.yml naming repo#write + base (and check repo#read + base too)
cat >"$CLONE/.grain/templates/x.yml" <<'YAML'
title: Bad write base grant
body: Should be refused at parse time because declared writes cannot carry base.
capabilities:
  - capability: https://grain.dev/capabilities/repo#write
    with:
      repo: acme/lib
      base: staging
YAML
git -C "$CLONE" add .grain/templates/x.yml
git -C "$CLONE" -c user.name=seed -c user.email=seed@example.com commit -qm "test: add bad repo#write+base template"
git -C "$CLONE" push -q origin main
HEAD_5B=$(git -C "$CLONE" rev-parse HEAD)

set +e
CHECK_5B=$(cd "$CLONE" && "$HEAD_BIN" declarations check -repo acme/widgets 2>&1)
RC_5B=$?
set -e
echo "5b grain declarations check (exit=$RC_5B): $CHECK_5B"

sqlite3 "$WORK/data/store/grain.db" "UPDATE repo_declaration_sync SET checked_at = '2026-11-10 00:00:00 +0000 UTC';"
for _ in $(seq 50); do
  LAST_ERR=$(call GET /api/repos/acme/widgets/declarations | jq -r '.declarationsStatus.error // empty')
  [[ "$LAST_ERR" == *"repo#write"* ]] && break
  sleep 0.25
done
DECL_STATUS_5B=$(call GET /api/repos/acme/widgets/declarations)
LIVE_TMPL_5B=$(call GET /api/templates | jq -c '[.[] | select(.declaredIn != null) | {id, name}]')
(cd "$ROOT/ui" && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
  node "$HERE/item2_ui.mjs" item5-error "$API" "$TOK" "{}" "$OUT_DIR/item5b-ui.json")

# Also check repo#read + base via grain declarations check
cat >"$CLONE/.grain/templates/x.yml" <<'YAML'
title: Bad read base grant
body: Should be refused at parse time because repo#read takes only repo.
capabilities:
  - capability: https://grain.dev/capabilities/repo#read
    with:
      repo: acme/lib
      base: main
YAML
set +e
CHECK_5C=$(cd "$CLONE" && "$HEAD_BIN" declarations check -repo acme/widgets 2>&1)
RC_5C=$?
set -e

jq -n \
  --arg head5a "$HEAD_5A" --argjson rc5a "$RC_5A" --arg check5a "$CHECK_5A" \
  --argjson status5a "$DECL_STATUS_5A" --argjson live5a "$LIVE_TMPL_5A" \
  --arg head5b "$HEAD_5B" --argjson rc5b "$RC_5B" --arg check5b "$CHECK_5B" \
  --argjson status5b "$DECL_STATUS_5B" --argjson live5b "$LIVE_TMPL_5B" \
  --argjson rc5c "$RC_5C" --arg check5c "$CHECK_5C" \
  '{
    repoTarget: {commit: $head5a, cliExit: $rc5a, cliOutput: $check5a, syncStatus: $status5a, liveDeclaredTemplates: $live5a},
    repoWriteWithBase: {commit: $head5b, cliExit: $rc5b, cliOutput: $check5b, syncStatus: $status5b, liveDeclaredTemplates: $live5b},
    repoReadWithBaseCli: {cliExit: $rc5c, cliOutput: $check5c}
  }' >"$OUT_DIR/item5-sync.json"

echo "Items 2 & 5 DONE."
