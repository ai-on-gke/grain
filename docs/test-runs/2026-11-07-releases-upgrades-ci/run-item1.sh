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

# run-item1.sh exercises §9.1 end-to-end against a live grain daemon, dockerd,
# and releases_forge on 127.0.0.1:4280.
set -euo pipefail

GRAIN="${GRAIN:-/home/debian/bin/grain}"
FORGE_BIN="${FORGE_BIN:-/tmp/item1-build/releases_forge}"
META_ENV="${META_ENV:-/tmp/item1-build/manifest-meta.env}"
ROOT="/tmp/item1-live"

# shellcheck source=/dev/null
source "$META_ENV"

cleanup() {
  if [ -n "${DAEMON_PID:-}" ]; then kill "$DAEMON_PID" 2>/dev/null || true; fi
  if [ -n "${FORGE_PID:-}" ]; then kill "$FORGE_PID" 2>/dev/null || true; fi
  wait 2>/dev/null || true
}
trap cleanup EXIT

pkill -x releases_forge 2>/dev/null || true
pkill -x grain 2>/dev/null || true
sleep 0.3

rm -rf "$ROOT"
mkdir -p "$ROOT"

cat > "$ROOT/verify-sig.sh" <<EOF
#!/usr/bin/env bash
set -euo pipefail
printf '[{"critical":{"identity":{"docker-reference":"docker.io/grainai/grain"},"image":{"docker-manifest-digest":"%s"},"type":"cosign container image signature"}}]\n' "$IMG_ID_1"
EOF
chmod +x "$ROOT/verify-sig.sh"

"$FORGE_BIN" \
  -addr 127.0.0.1:4280 \
  -mode normal \
  -image-id "$IMG_ID_1" \
  -swapped-image-id "$IMG_ID_2" \
  > "$ROOT/forge.log" 2>&1 &
FORGE_PID=$!
for _ in $(seq 1 30); do
  curl -fsS http://127.0.0.1:4280/_mode >/dev/null 2>&1 && break
  sleep 0.2
done

wait_upgrade_done() {
  local code="$1"
  local port="$2"
  for _ in $(seq 1 120); do
    local ph
    ph=$(curl -fsS -H "Authorization: Bearer $code" "http://127.0.0.1:$port/api/upgrade" | jq -r '.phase')
    if [ "$ph" != "running" ]; then
      return 0
    fi
    sleep 0.5
  done
  echo "Timed out waiting for upgrade to finish" >&2
  return 1
}

start_daemon_case() {
  local name="$1"
  shift
  if [ -n "${DAEMON_PID:-}" ]; then
    kill "$DAEMON_PID" 2>/dev/null || true
    wait "$DAEMON_PID" 2>/dev/null || true
    DAEMON_PID=""
  fi
  local dir="$ROOT/$name"
  mkdir -p "$dir/data" "$dir/sandbox"
  printf 'GRAIN_IMAGE=docker.io/grainai/grain:v3.1.0\n' > "$dir/image.env"
  cat > "$dir/restart.sh" <<EOF
#!/usr/bin/env bash
printf '%s %s\n' "\$(date -u +%FT%TZ)" "\$(cat '$dir/image.env')" >> '$dir/restarts.log'
EOF
  chmod +x "$dir/restart.sh"

  "$GRAIN" daemon \
    -data-dir "$dir/data" \
    -sandbox-dir "$dir/sandbox" \
    -sandbox-port-base=0 \
    -disable-native-sandboxes \
    -ui-addr 127.0.0.1:4283 \
    -github-host 127.0.0.1:4280 \
    -github-insecure-http \
    -upgrade-image docker.io/grainai/grain \
    -upgrade-image-ref-file "$dir/image.env" \
    -upgrade-restart-cmd "$dir/restart.sh" \
    "$@" > "$dir/daemon.log" 2>&1 &
  DAEMON_PID=$!
  for _ in $(seq 1 60); do
    curl -fsS http://127.0.0.1:4283/healthz >/dev/null 2>&1 && break
    sleep 0.25
  done
  ACCESS_CODE=$("$GRAIN" access-code -data-dir "$dir/data")
  CASE_DIR="$dir"
}

echo "=== 1A. Schema v2 channel pointer + tagged-not-published v3.2.1 (#749) + signature binding ==="
docker rmi -f docker.io/grainai/grain:v3.2.0 "$IMG_ID_1" "$IMG_ID_2" >/dev/null 2>&1 || true
curl -fsS -X POST 'http://127.0.0.1:4280/_mode?set=normal' >/dev/null
curl -fsS -X DELETE 'http://127.0.0.1:4280/_log' >/dev/null

start_daemon_case "case-1a" \
  -upgrade-image-source release-assets \
  -upgrade-image-fallback none \
  -upgrade-release-repo 127.0.0.1:4280/ai-on-gke/grain \
  -upgrade-verify-cmd "$ROOT/verify-sig.sh" \
  -upgrade-allowed-branches 'latest,main,release/*,v[0-9]*.[0-9]*.[0-9]*'

TARGETS_JSON=$(curl -fsS -H "Authorization: Bearer $ACCESS_CODE" http://127.0.0.1:4283/api/upgrade/targets)
echo "$TARGETS_JSON" | jq '{targets, versionsError}'
echo "$TARGETS_JSON" | jq -e '(.targets | map(.name) | index("v3.2.0")) != null and (.targets | map(.name) | index("v3.2.1")) == null' >/dev/null

echo "--- Requesting unpublished v3.2.1 (must fail closed naming v3.2.0) ---"
curl -fsS -X POST -H "Authorization: Bearer $ACCESS_CODE" -H 'Content-Type: application/json' \
  http://127.0.0.1:4283/api/upgrade -d '{"branch":"v3.2.1"}' >/dev/null
wait_upgrade_done "$ACCESS_CODE" 4283
STATUS_UNPUB=$(curl -fsS -H "Authorization: Bearer $ACCESS_CODE" http://127.0.0.1:4283/api/upgrade)
echo "$STATUS_UNPUB" | jq '{phase, detail}'
echo "$STATUS_UNPUB" | jq -e '.phase == "failed" and (.detail | contains("v3.2.1") and contains("not published") and contains("v3.2.0"))' >/dev/null
test ! -f "$CASE_DIR/restarts.log"

echo "--- Requesting latest (v2 channel pointer channel-latest -> v3.2.0) ---"
curl -fsS -X POST -H "Authorization: Bearer $ACCESS_CODE" -H 'Content-Type: application/json' \
  http://127.0.0.1:4283/api/upgrade -d '{"branch":"latest"}' >/dev/null
wait_upgrade_done "$ACCESS_CODE" 4283
STATUS_OK=$(curl -fsS -H "Authorization: Bearer $ACCESS_CODE" http://127.0.0.1:4283/api/upgrade)
echo "$STATUS_OK" | jq '{phase, source, detail, steps: [.steps[] | {name, status, detail}]}'
echo "$STATUS_OK" | jq -e '.phase == "ok" and .source == "release-assets"' >/dev/null
cat "$CASE_DIR/image.env"
cat "$CASE_DIR/restarts.log"
grep -q "docker.io/grainai/grain:latest" "$CASE_DIR/image.env"
test -s "$CASE_DIR/restarts.log"

echo "=== 1B. Slash-branch tag mapping release/v3.2.0 -> channel-release-v3.2.0 (#799) ==="
docker rmi -f docker.io/grainai/grain:v3.2.0 "$IMG_ID_1" "$IMG_ID_2" >/dev/null 2>&1 || true
curl -fsS -X DELETE 'http://127.0.0.1:4280/_log' >/dev/null
start_daemon_case "case-1b" \
  -upgrade-image-source release-assets \
  -upgrade-image-fallback none \
  -upgrade-release-repo 127.0.0.1:4280/ai-on-gke/grain \
  -upgrade-verify-cmd "$ROOT/verify-sig.sh" \
  -upgrade-allowed-branches 'latest,main,release/*,v[0-9]*.[0-9]*.[0-9]*'

curl -sS -X POST -H "Authorization: Bearer $ACCESS_CODE" -H 'Content-Type: application/json' \
  http://127.0.0.1:4283/api/upgrade -d '{"branch":"release/v3.2.0"}' | jq '{phase, branch, detail, error}'
wait_upgrade_done "$ACCESS_CODE" 4283
STATUS_SLASH=$(curl -fsS -H "Authorization: Bearer $ACCESS_CODE" http://127.0.0.1:4283/api/upgrade)
echo "$STATUS_SLASH" | jq '{phase, branch, source, detail}'
echo "$STATUS_SLASH" | jq -e '.phase == "ok" and .source == "release-assets"' >/dev/null
FORGE_REQS=$(curl -fsS 'http://127.0.0.1:4280/_log')
echo "$FORGE_REQS" | jq .
echo "$FORGE_REQS" | jq -e '.requests | any(contains("/releases/tags/channel-release-v3.2.0"))' >/dev/null

echo "=== 1C. Mid-update images.json.next-only defers without falling back to registry (#661) ==="
curl -fsS -X POST 'http://127.0.0.1:4280/_mode?set=mid-update' >/dev/null
start_daemon_case "case-1c" \
  -upgrade-image-source release-assets \
  -upgrade-image-fallback registry \
  -upgrade-release-repo 127.0.0.1:4280/ai-on-gke/grain \
  -upgrade-allowed-branches 'latest,main,v[0-9]*.[0-9]*.[0-9]*'

curl -fsS -X POST -H "Authorization: Bearer $ACCESS_CODE" -H 'Content-Type: application/json' \
  http://127.0.0.1:4283/api/upgrade -d '{"branch":"main"}' >/dev/null
wait_upgrade_done "$ACCESS_CODE" 4283
STATUS_MID=$(curl -fsS -H "Authorization: Bearer $ACCESS_CODE" http://127.0.0.1:4283/api/upgrade)
echo "$STATUS_MID" | jq '{phase, source, detail}'
echo "$STATUS_MID" | jq -e '.phase == "failed" and (.detail | contains("moving release is mid-update"))' >/dev/null
test ! -f "$CASE_DIR/restarts.log"

echo "=== 1D. Swapped tarball rejected by images.json imageId check AND by verifyLoaded signature binding (#661 a2681b21) ==="
docker rmi -f docker.io/grainai/grain:v3.2.0 "$IMG_ID_1" "$IMG_ID_2" >/dev/null 2>&1 || true
start_daemon_case "case-1d" \
  -upgrade-image-source release-assets \
  -upgrade-image-fallback none \
  -upgrade-release-repo 127.0.0.1:4280/ai-on-gke/grain \
  -upgrade-verify-cmd "$ROOT/verify-sig.sh" \
  -upgrade-allowed-branches 'latest,main,v[0-9]*.[0-9]*.[0-9]*'

echo "--- 1D.i: swapped-id (tarball #2 vs images.json imageId #1) ---"
curl -fsS -X POST 'http://127.0.0.1:4280/_mode?set=swapped-id' >/dev/null
curl -fsS -X POST -H "Authorization: Bearer $ACCESS_CODE" -H 'Content-Type: application/json' \
  http://127.0.0.1:4283/api/upgrade -d '{"branch":"v3.2.0"}' >/dev/null
wait_upgrade_done "$ACCESS_CODE" 4283
STATUS_SWAP_ID=$(curl -fsS -H "Authorization: Bearer $ACCESS_CODE" http://127.0.0.1:4283/api/upgrade)
echo "$STATUS_SWAP_ID" | jq '{phase, detail}'
echo "$STATUS_SWAP_ID" | jq -e '.phase == "failed" and (.detail | contains("integrity check failed"))' >/dev/null
test ! -f "$CASE_DIR/restarts.log"

echo "--- 1D.ii: swapped-sig (both tarball #2 and images.json imageId #2 swapped; VerifyCmd trusts only #1) ---"
docker rmi -f docker.io/grainai/grain:v3.2.0 "$IMG_ID_1" "$IMG_ID_2" >/dev/null 2>&1 || true
curl -fsS -X POST 'http://127.0.0.1:4280/_mode?set=swapped-sig' >/dev/null
curl -fsS -X POST -H "Authorization: Bearer $ACCESS_CODE" -H 'Content-Type: application/json' \
  http://127.0.0.1:4283/api/upgrade -d '{"branch":"v3.2.0"}' >/dev/null
wait_upgrade_done "$ACCESS_CODE" 4283
STATUS_SWAP_SIG=$(curl -fsS -H "Authorization: Bearer $ACCESS_CODE" http://127.0.0.1:4283/api/upgrade)
echo "$STATUS_SWAP_SIG" | jq '{phase, detail, steps: [.steps[] | {name, status, detail}]}'
echo "$STATUS_SWAP_SIG" | jq -e '.phase == "failed" and (.detail | contains("reported signatures for") and contains("the image loaded from the release"))' >/dev/null
test ! -f "$CASE_DIR/restarts.log"

echo "=== 1E. Single-asset -upgrade-release-asset without images.json (Bug F2 after-fix) ==="
docker rmi -f docker.io/grainai/grain:v3.2.0 "$IMG_ID_1" "$IMG_ID_2" >/dev/null 2>&1 || true
curl -fsS -X POST 'http://127.0.0.1:4280/_mode?set=asset-only' >/dev/null
start_daemon_case "case-1e" \
  -upgrade-image-source release-assets \
  -upgrade-image-fallback none \
  -upgrade-release-repo 127.0.0.1:4280/ai-on-gke/grain \
  -upgrade-release-asset grain-image.tar.gz \
  -upgrade-verify-cmd "$ROOT/verify-sig.sh" \
  -upgrade-allowed-branches 'latest,main,v[0-9]*.[0-9]*.[0-9]*'

TARGETS_ASSET_ONLY=$(curl -fsS -H "Authorization: Bearer $ACCESS_CODE" http://127.0.0.1:4283/api/upgrade/targets)
echo "$TARGETS_ASSET_ONLY" | jq '{targets, versionsError}'
echo "$TARGETS_ASSET_ONLY" | jq -e '(.targets | map(.name) | index("v3.2.0")) != null and (.targets | map(.name) | index("v3.2.1")) == null' >/dev/null

curl -fsS -X POST -H "Authorization: Bearer $ACCESS_CODE" -H 'Content-Type: application/json' \
  http://127.0.0.1:4283/api/upgrade -d '{"branch":"v3.2.0"}' >/dev/null
wait_upgrade_done "$ACCESS_CODE" 4283
STATUS_ASSET_ONLY=$(curl -fsS -H "Authorization: Bearer $ACCESS_CODE" http://127.0.0.1:4283/api/upgrade)
echo "$STATUS_ASSET_ONLY" | jq '{phase, source, detail}'
echo "$STATUS_ASSET_ONLY" | jq -e '.phase == "ok" and .source == "release-assets"' >/dev/null
test -s "$CASE_DIR/restarts.log"

echo "=== 1F. Settings -> Upgrade PUT /api/upgrade/source refusals on verifying deployment & disallowed repo (#661) ==="
curl -fsS -X POST 'http://127.0.0.1:4280/_mode?set=normal' >/dev/null
start_daemon_case "case-1f" \
  -upgrade-verify-cmd "$ROOT/verify-sig.sh" \
  -upgrade-allowed-branches 'latest,main,v[0-9]*.[0-9]*.[0-9]*'

HTTP_VERIFY_MODE=$(curl -sS -o "$ROOT/refuse-mode.json" -w '%{http_code}' \
  -X PUT -H "Authorization: Bearer $ACCESS_CODE" -H 'Content-Type: application/json' \
  http://127.0.0.1:4283/api/upgrade/source -d '{"mode":"release-assets"}')
echo "PUT mode=release-assets with -upgrade-verify-cmd -> HTTP $HTTP_VERIFY_MODE: $(cat "$ROOT/refuse-mode.json")"
test "$HTTP_VERIFY_MODE" = "400"
jq -e '.error | contains("-upgrade-verify-cmd")' "$ROOT/refuse-mode.json" >/dev/null

HTTP_VERIFY_FB=$(curl -sS -o "$ROOT/refuse-fb.json" -w '%{http_code}' \
  -X PUT -H "Authorization: Bearer $ACCESS_CODE" -H 'Content-Type: application/json' \
  http://127.0.0.1:4283/api/upgrade/source -d '{"mode":"registry","fallback":"release-assets","fallbackExplicit":true}')
echo "PUT fallback=release-assets with -upgrade-verify-cmd -> HTTP $HTTP_VERIFY_FB: $(cat "$ROOT/refuse-fb.json")"
test "$HTTP_VERIFY_FB" = "400"
jq -e '.error | contains("-upgrade-verify-cmd")' "$ROOT/refuse-fb.json" >/dev/null

HTTP_BAD_REPO=$(curl -sS -o "$ROOT/refuse-repo.json" -w '%{http_code}' \
  -X PUT -H "Authorization: Bearer $ACCESS_CODE" -H 'Content-Type: application/json' \
  http://127.0.0.1:4283/api/upgrade/source -d '{"mode":"registry","releaseRepository":"github.com/attacker/evil"}')
echo "PUT disallowed releaseRepository -> HTTP $HTTP_BAD_REPO: $(cat "$ROOT/refuse-repo.json")"
test "$HTTP_BAD_REPO" = "400"
jq -e '.error | contains("not one this deployment may upgrade from")' "$ROOT/refuse-repo.json" >/dev/null

echo "ALL ITEM 1 LIVE DAEMON CHECKS PASSED"
