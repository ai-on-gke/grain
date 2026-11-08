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

# run-item4.sh exercises docs/test-plans/2026-11-07.md §9 item 4 against live
# grain daemons:
#
#   4A  forbidden git_ref schedules (#744) refused over REST (POST/PATCH
#       /api/schedules), the CLI (`grain declarations check`, `grain state
#       check` with and without -data-dir), and a merged grain/config change
#       on a running daemon (Bug F6); grandfathered schedules can be paused;
#       Processes -> Schedules shows off-target suppression, and Settings ->
#       Upgrade refuses moving a verifying deployment onto release assets,
#       both rendered at 1440/390 (screens.cjs);
#   4B  pkg/pkgcache /artifacts-uploads/ multi-chunk push and auth
#       boundaries (#811; testdata/item4_pkgcache_probe.go).
#
# Needs: $GRAIN (a grain build of this checkout), prep-item1.sh's
# /tmp/item1-build (for the Settings -> Upgrade daemon's release forge),
# Playwright's chromium (ui/node_modules), jq, git.
set -euo pipefail

GRAIN="${GRAIN:-/home/debian/bin/grain}"
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(git -C "$HERE" rev-parse --show-toplevel)"
ROOT=/tmp/item4-live
FORGE_BIN="${FORGE_BIN:-/tmp/item1-build/releases_forge}"
META_ENV="${META_ENV:-/tmp/item1-build/manifest-meta.env}"
A=127.0.0.1:4290 # forbidden git_ref daemon
B=127.0.0.1:4291 # verifying upgrade daemon

pass() { printf 'PASS %s\n' "$*"; }
# expect DESCRIPTION COMMAND...: fail with DESCRIPTION unless COMMAND succeeds.
expect() {
  local what="$1"
  shift
  if ! "$@"; then fail "$what"; fi
}
has() { grep -qF -- "$1" <<<"$2"; }
fail() {
  printf 'FAIL %s\n' "$*"
  exit 1
}

PIDS=()
cleanup() {
  for p in "${PIDS[@]}"; do kill "$p" 2>/dev/null || true; done
  wait 2>/dev/null || true
}
trap cleanup EXIT

start_daemon() { # name addr [flags...]
  local name="$1" addr="$2"
  shift 2
  local dir="$ROOT/$name"
  mkdir -p "$dir/data" "$dir/sandbox"
  "$GRAIN" daemon -data-dir "$dir/data" -sandbox-dir "$dir/sandbox" -sandbox-port-base=0 \
    -disable-native-sandboxes -ui-addr "$addr" "$@" >"$dir/daemon.log" 2>&1 &
  DAEMON_PID=$!
  PIDS+=("$DAEMON_PID")
  for _ in $(seq 1 80); do
    curl -fsS "http://$addr/healthz" >/dev/null 2>&1 && break
    sleep 0.25
  done
  DAEMON_CODE=$("$GRAIN" access-code -data-dir "$dir/data")
}

api() { # code method addr path [json] -> prints "<status> <body>"
  local code="$1" method="$2" addr="$3" path="$4" body="${5:-}"
  local args=(-sS -o "$ROOT/resp.json" -w '%{http_code}' -X "$method" -H "Authorization: Bearer $code")
  if [ -n "$body" ]; then args+=(-H 'Content-Type: application/json' -d "$body"); fi
  local st
  st=$(curl "${args[@]}" "http://$addr$path")
  printf '%s %s' "$st" "$(cat "$ROOT/resp.json")"
}

watch_body() { # title watched-repo
  printf '{"title":"%s","repo":"acme/widgets","recurrence":{"kind":"git_ref","gitRef":{"repo":"%s","ref":"refs/heads/main"}}}' "$1" "$2"
}

pkill -x grain 2>/dev/null || true
pkill -x releases_forge 2>/dev/null || true
sleep 0.5
rm -rf "$ROOT"
mkdir -p "$ROOT/a/data"
# This deployment backs its encrypted secrets up to acme/host-secrets.
printf '{"remote":"git@github.com:acme/host-secrets.git","branch":"main"}\n' >"$ROOT/a/data/secrets-repo.json"

echo "=== 4A.1 REST: POST/PATCH /api/schedules refuse forbidden git_ref repos (#744) ==="
start_daemon a "$A" -github-host 127.0.0.1:4299 -github-insecure-http
PID_a=$DAEMON_PID
CODE_a=$DAEMON_CODE
out=$(api "$CODE_a" PUT "$A" /api/deployment '{"targetRepos":["acme/widgets"]}')
[ "${out%% *}" = 200 ] || fail "PUT /api/deployment targetRepos: $out"
for case in \
  "grain/config-alpha|400|grain/config-alpha not found: only grain/config exists" \
  "acme/host-secrets|400|encrypted secrets backup repository" \
  "ACME/Host-Secrets|400|encrypted secrets backup repository" \
  "not a repo|400|git_ref trigger needs" \
  "grain/config|201|" \
  "acme/widgets|201|" \
  "offlist/upstream|201|"; do
  IFS='|' read -r repo want sub <<<"$case"
  out=$(api "$CODE_a" POST "$A" /api/schedules "$(watch_body "Watch $repo" "$repo")")
  st="${out%% *}"
  body="${out#* }"
  [ "$st" = "$want" ] || fail "POST watching $repo -> $st, want $want: $body"
  if [ -n "$sub" ]; then
    grep -qF "$sub" <<<"$body" || fail "POST watching $repo: $body lacks $sub"
    pass "POST watching $repo -> $st $(jq -r .error <<<"$body" | cut -c1-140)"
  else
    pass "POST watching $repo -> $st $(jq -r .id <<<"$body")"
  fi
done
WIDGETS_ID=$(curl -fsS -H "Authorization: Bearer $CODE_a" "http://$A/api/schedules" | jq -r '.[] | select(.title=="Watch acme/widgets") | .id')
OFF_ID=$(curl -fsS -H "Authorization: Bearer $CODE_a" "http://$A/api/schedules" | jq -r '.[] | select(.title=="Watch offlist/upstream") | .id')
out=$(api "$CODE_a" PATCH "$A" "/api/schedules/$WIDGETS_ID" '{"recurrence":{"kind":"git_ref","gitRef":{"repo":"grain/config-beta","ref":"refs/heads/main"}}}')
expect "PATCH onto retired alias: $out" [ "${out%% *}" = 400 ]
expect "PATCH onto retired alias: $out" has "grain/config-beta not found" "$out"
pass "PATCH $WIDGETS_ID onto grain/config-beta -> 400"
supp=$(curl -fsS -H "Authorization: Bearer $CODE_a" "http://$A/api/schedules" | jq -c ".[] | select(.id==\"$OFF_ID\") | .suppressed")
[ "$(jq -r .reason <<<"$supp")" = watchedRepoOffTarget ] || fail "off-target suppression: $supp"
pass "$OFF_ID watching offlist/upstream reports suppressed=$supp"

echo "=== 4A.2 A grandfathered schedule whose watched repo became forbidden can be paused ==="
out=$(api "$CODE_a" POST "$A" /api/schedules "$(watch_body "Watch acme/later-secrets" "acme/later-secrets")")
[ "${out%% *}" = 201 ] || fail "POST acme/later-secrets: $out"
LATER_ID=$(jq -r .id <<<"${out#* }")
kill "$PID_a"
wait "$PID_a" 2>/dev/null || true
printf '{"remote":"https://github.com/acme/later-secrets.git","branch":"main"}\n' >"$ROOT/a/data/secrets-repo.json"
start_daemon a "$A" -github-host 127.0.0.1:4299 -github-insecure-http
PID_a=$DAEMON_PID
CODE_a=$DAEMON_CODE
out=$(api "$CODE_a" PATCH "$A" "/api/schedules/$LATER_ID" '{"recurrence":{"kind":"git_ref","gitRef":{"repo":"acme/later-secrets","ref":"refs/heads/main"}}}')
expect "re-saving grandfathered watch: $out" [ "${out%% *}" = 400 ]
expect "re-saving grandfathered watch: $out" has "encrypted secrets backup repository" "$out"
pass "PATCH $LATER_ID keeping its now-forbidden watch -> 400"
out=$(api "$CODE_a" PATCH "$A" "/api/schedules/$LATER_ID" '{"enabled":false,"recurrence":{"kind":"git_ref","gitRef":{"repo":"acme/later-secrets","ref":"refs/heads/main"}}}')
expect "pausing grandfathered watch: $out" [ "${out%% *}" = 200 ]
expect "pausing grandfathered watch: $out" [ "$(jq -r .enabled <<<"${out#* }")" = false ]
pass "PATCH $LATER_ID enabled:false -> 200 (paused)"

echo "=== 4A.3 CLI: grain declarations check on .grain/schedules (#744, Bug F5) ==="
decl="$ROOT/decl"
mkdir -p "$decl/.grain/templates" "$decl/.grain/schedules"
printf 'title: Sync\n' >"$decl/.grain/templates/sync.yml"
for watched in upstream/core grain/config-alpha grain/config; do
  printf 'template: sync\ntrigger:\n  git_ref:\n    repo: %s\n    ref: refs/heads/main\n' "$watched" >"$decl/.grain/schedules/watch.yml"
  set +e
  msg=$("$GRAIN" declarations check -repo acme/widgets "$decl" 2>&1)
  rc=$?
  set -e
  case "$watched" in
  upstream/core) [ $rc = 0 ] || fail "declarations check watching $watched: rc=$rc $msg" ;;
  grain/config-alpha) expect "rc=$rc" [ $rc != 0 ] && expect "watching $watched: rc=$rc $msg" has "grain/config-alpha not found: only grain/config exists" "$msg" ;;
  grain/config) expect "rc=$rc" [ $rc != 0 ] && expect "watching $watched: rc=$rc $msg" has "grain's own configuration" "$msg" ;;
  esac
  pass "grain declarations check watching $watched -> rc=$rc $(tail -n 1 <<<"$msg" | cut -c1-140)"
done

echo "=== 4A.4 CLI: grain state check on a copy of the live state repo (#744, Bug F5) ==="
sc="$ROOT/state-check"
mkdir -p "$sc"
for c in ok malformed retired secrets; do
  cp -r "$ROOT/a/data/state-repo" "$sc/$c"
  rm -rf "$sc/$c/.git"
done
sed -i 's|repo: offlist/upstream|repo: "not a repo"|' "$sc/malformed/settings/schedules.yml"
sed -i 's|repo: offlist/upstream|repo: grain/config-alpha|' "$sc/retired/settings/schedules.yml"
sed -i 's|repo: offlist/upstream|repo: acme/host-secrets|' "$sc/secrets/settings/schedules.yml"
printf '{"remote":"git@github.com:acme/host-secrets.git","branch":"main"}\n' >"$sc/secrets-repo.json"
for c in ok malformed retired secrets secrets+data-dir; do
  dir="$sc/${c%%+*}"
  args=(state check "$dir")
  [ "$c" = secrets+data-dir ] && args=(state -data-dir "$sc" check "$dir")
  set +e
  msg=$("$GRAIN" "${args[@]}" 2>&1)
  rc=$?
  set -e
  case "$c" in
  ok | secrets) [ $rc = 0 ] || fail "state check $c: rc=$rc $msg" ;;
  malformed) expect "rc=$rc" [ $rc != 0 ] && expect "$c: $msg" has "git_ref trigger needs \`repo: owner/name\`" "$msg" ;;
  retired) expect "rc=$rc" [ $rc != 0 ] && expect "$c: $msg" has "grain/config-alpha not found" "$msg" ;;
  secrets+data-dir) expect "rc=$rc" [ $rc != 0 ] && expect "$c: $msg" has "encrypted secrets backup repository" "$msg" ;;
  esac
  pass "grain ${args[*]/#$ROOT/…} -> rc=$rc $(grep -v '^ ' <<<"$msg" | tail -n 1 | cut -c1-160)"
done

echo "=== 4A.5 A merged grain/config change on the running daemon (Bug F6) ==="
sr="$ROOT/a/data/state-repo"
git -C "$sr" -c user.name=op -c user.email=op@example.com log --oneline -1 >/dev/null
sed -i "0,/repo: acme\/widgets\$/s||repo: grain/config-alpha|" "$sr/settings/schedules.yml"
grep -q 'repo: grain/config-alpha' "$sr/settings/schedules.yml" || fail "editing schedules.yml"
git -C "$sr" -c user.name=op -c user.email=op@example.com commit -qam "operator: watch the retired alias"
BAD=$(git -C "$sr" rev-parse --short HEAD)
for _ in $(seq 1 60); do
  grep -q "refusing to apply the settings that just arrived" "$ROOT/a/daemon.log" && break
  sleep 0.5
done
refusal=$(grep -A2 "refusing to apply the settings that just arrived" "$ROOT/a/daemon.log" | head -n 3)
expect "daemon did not refuse $BAD: $refusal" has "grain/config-alpha not found: only grain/config exists" "$refusal"
pass "daemon refused $BAD:"
printf '    %s\n' "$refusal"
watched=$(curl -fsS -H "Authorization: Bearer $CODE_a" "http://$A/api/schedules" | jq -r ".[] | select(.id==\"$WIDGETS_ID\") | .recurrence.gitRef.repo")
[ "$watched" = acme/widgets ] || fail "$WIDGETS_ID now watches $watched"
pass "$WIDGETS_ID still watches $watched in the database"
git -C "$sr" -c user.name=op -c user.email=op@example.com revert --no-edit HEAD >/dev/null
sed -i "0,/title: Watch acme\/widgets\$/s||title: Watch acme/widgets (renamed)|" "$sr/settings/schedules.yml"
git -C "$sr" -c user.name=op -c user.email=op@example.com commit -qam "operator: rename the widgets watch"
for _ in $(seq 1 60); do
  t=$(curl -fsS -H "Authorization: Bearer $CODE_a" "http://$A/api/schedules" | jq -r ".[] | select(.id==\"$WIDGETS_ID\") | .title")
  [ "$t" = "Watch acme/widgets (renamed)" ] && break
  sleep 0.5
done
[ "$t" = "Watch acme/widgets (renamed)" ] || fail "follow-up change not applied: $t"
pass "after reverting $BAD, a follow-up rename applied (title=\"$t\"); the grandfathered $LATER_ID row did not wedge the import"

echo "=== 4A.6 UI at 1440/390: Processes -> Schedules suppression (#744) & Settings -> Upgrade refusal (#661) ==="
# shellcheck source=/dev/null
source "$META_ENV"
"$FORGE_BIN" -addr 127.0.0.1:4280 -mode normal -image-id "$IMG_ID_1" -swapped-image-id "$IMG_ID_2" >"$ROOT/forge.log" 2>&1 &
PIDS+=($!)
mkdir -p "$ROOT/b"
printf 'GRAIN_IMAGE=docker.io/grainai/grain:v3.1.0\n' >"$ROOT/b/image.env"
printf '#!/usr/bin/env bash\nexit 0\n' >"$ROOT/b/restart.sh"
printf '#!/usr/bin/env bash\necho []\n' >"$ROOT/b/verify.sh"
chmod +x "$ROOT/b/restart.sh" "$ROOT/b/verify.sh"
start_daemon b "$B" -github-host 127.0.0.1:4280 -github-insecure-http \
  -upgrade-image docker.io/grainai/grain -upgrade-image-ref-file "$ROOT/b/image.env" \
  -upgrade-restart-cmd "$ROOT/b/restart.sh" -upgrade-verify-cmd "$ROOT/b/verify.sh" \
  -upgrade-release-repo 127.0.0.1:4280/ai-on-gke/grain
CODE_b=$DAEMON_CODE
SCHED_URL="http://$A" SCHED_CODE="$CODE_a" SCHED_TITLE="Watch offlist/upstream" \
  UPGRADE_URL="http://$B" UPGRADE_CODE="$CODE_b" OUT="$ROOT/shots" \
  node "$HERE/screens.cjs"

echo "=== 4B pkg/pkgcache /artifacts-uploads/ multi-chunk push & auth boundaries (#811) ==="
(cd "$REPO" && go run "$HERE/testdata/item4_pkgcache_probe.go")

echo "ALL §9 ITEM 4 CHECKS PASSED"
