#!/usr/bin/env bash
# ok/bad always succeed, so `A && ok || bad` is an if-then-else in this file.
# shellcheck disable=SC2015
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

# run-item1.sh: walkthrough 2026-11-08 §5 item 1 -- scripts/release-assets.sh
# and scripts/build-cli.sh against releases_fake.go, in a scratch clone under
# /tmp (never the checkout), then a live grain daemon reading the result.
#
#   bash run-item1.sh [ROOT]     (default ROOT=/tmp/s5-item1)
set -uo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
checkout="$(cd "$here/../../.." && pwd)"
ROOT="${1:-/tmp/s5-item1}"
GRAIN="${GRAIN:-/home/debian/bin/grain}"
PORT=4380
DPORT=4383
rm -rf "$ROOT"
mkdir -p "$ROOT"
before_cfg="$(sha256sum "$checkout/.git/config" | cut -d' ' -f1)"
pass=0
fail=0
ok() { echo "PASS: $*"; pass=$((pass + 1)); }
bad() { echo "FAIL: $*"; fail=$((fail + 1)); }

# --- fake GitHub ------------------------------------------------------------
(cd "$here" && go build -o "$ROOT/releases_fake" releases_fake.go)
"$ROOT/releases_fake" -addr 127.0.0.1:$PORT -repo ai-on-gke/grain >"$ROOT/fake.log" 2>&1 &
FAKE_PID=$!
trap 'kill $FAKE_PID ${DAEMON_PID:-} 2>/dev/null' EXIT
for _ in $(seq 1 40); do curl -fsS "http://127.0.0.1:$PORT/_log" >/dev/null 2>&1 && break; sleep 0.25; done
export GITHUB_SERVER_URL="http://127.0.0.1:$PORT" GITHUB_API_URL="http://127.0.0.1:$PORT/api/v3" GITHUB_TOKEN=fake-token RELEASE_ASSETS_REPO=ai-on-gke/grain

# --- scratch clone: c0 -> c1 -> c2 on main, and side (c0 -> x) ---------------
git init -q --bare "$ROOT/origin.git"
git clone -q --no-local --no-tags "$checkout" "$ROOT/repo"
cd "$ROOT/repo" || exit 1
git remote set-url origin "$ROOT/origin.git"
git checkout -q -B main
git -c user.name=walk -c user.email=walk@example.com commit -q --allow-empty -m c1
c1="$(git rev-parse HEAD)"
git -c user.name=walk -c user.email=walk@example.com commit -q --allow-empty -m c2
c2="$(git rev-parse HEAD)"
git checkout -q -b side "$c1~1"
git -c user.name=walk -c user.email=walk@example.com commit -q --allow-empty -m side
cx="$(git rev-parse HEAD)"
git checkout -q main
git push -q origin main side
echo "c1=$c1 c2=$c2 side=$cx"

# --- synthetic images: tiny FROM scratch images per commit ------------------
mkimages() {
  local c="$1" short="${1:0:12}" key
  for key in grain guest k8s-sandbox; do
    mkdir -p "$ROOT/img/$key"
    printf '%s %s\n' "$key" "$c" >"$ROOT/img/$key/payload"
    if [ "$key" = grain ]; then
      # The daemon health-checks a loaded grain image (`docker run IMAGE
      # schema-version`), so this one carries a static grain binary.
      cp "$ROOT/grain-static" "$ROOT/img/$key/grain"
      printf 'FROM scratch\nCOPY grain /usr/local/bin/grain\nCOPY payload /payload\nENTRYPOINT ["/usr/local/bin/grain"]\nCMD ["daemon"]\n' >"$ROOT/img/$key/Dockerfile"
    else
      printf 'FROM scratch\nCOPY payload /payload\n' >"$ROOT/img/$key/Dockerfile"
    fi
    docker build -q -t "grain/$key:sha-$short" "$ROOT/img/$key" >/dev/null
  done
  cat >"$ROOT/state-$short.env" <<EOF
RELEASE_IMAGE_TARGET=assets
RELEASE_IMAGE_COMMIT=$c
RELEASE_IMAGE_GRAIN=grain/grain:sha-$short
RELEASE_IMAGE_GUEST=grain/guest:sha-$short
RELEASE_IMAGE_K8S_SANDBOX=grain/k8s-sandbox:sha-$short
EOF
}
package() {
  RELEASE_IMAGES_STATE="$ROOT/state-${1:0:12}.env" RELEASE_ASSETS_DIR="$ROOT/assets-${1:0:12}" \
    bash scripts/release-assets.sh package
}
publish() {
  local c="$1"
  shift
  RELEASE_ASSETS_DIR="$ROOT/assets-${c:0:12}" bash scripts/release-assets.sh publish "$@"
}
state() { curl -fsS "http://127.0.0.1:$PORT/_state"; }
assets_of() { state | jq -r --arg t "$1" '.releases[] | select(.tag_name == $t) | .assets[].name' | sort | tr '\n' ' '; }
manifest_commit() {
  local id
  id="$(state | jq -r --arg t "$1" '.releases[] | select(.tag_name == $t) | .assets[] | select(.name == "images.json") | .id')"
  curl -fsS -H 'Accept: application/octet-stream' "http://127.0.0.1:$PORT/api/v3/repos/ai-on-gke/grain/releases/assets/$id" | jq -r .commit
}

(cd "$checkout" && CGO_ENABLED=0 go build -o "$ROOT/grain-static" ./cmd/grain)
echo "=== 1A. package + publish sha-<c1> and channel-main ==="
mkimages "$c1"
mkimages "$c2"
mkimages "$cx"
package "$c1" >"$ROOT/package-c1.log" 2>&1 && ok "package c1" || { bad "package c1"; cat "$ROOT/package-c1.log"; }
ls "$ROOT/assets-${c1:0:12}"
bash scripts/release-assets.sh validate "$ROOT/assets-${c1:0:12}" && ok "validate c1 assets"
curl -fsS -X DELETE "http://127.0.0.1:$PORT/_log"
publish "$c1" "sha-${c1:0:12}" channel-main 2>&1 | tee "$ROOT/publish-c1.log"
curl -fsS "http://127.0.0.1:$PORT/_log" >"$ROOT/log-publish-c1.txt"
[ "$(assets_of "sha-${c1:0:12}")" = "grain-image.tar.gz grain-image.tar.gz.sha256 guest-image.tar.gz guest-image.tar.gz.sha256 images.json k8s-sandbox-image.tar.gz k8s-sandbox-image.tar.gz.sha256 " ] &&
  ok "sha-${c1:0:12} holds 3 tarballs + 3 sidecars + images.json" || bad "sha-c1 assets: $(assets_of "sha-${c1:0:12}")"
[ "$(assets_of channel-main)" = "images.json " ] && ok "channel-main holds images.json alone (contract v2)" || bad "channel-main assets: $(assets_of channel-main)"
[ "$(manifest_commit channel-main)" = "$c1" ] && ok "channel-main names c1" || bad "channel-main commit"
# images.json is the holder's last upload
last_up="$(grep 'POST /api/uploads' "$ROOT/log-publish-c1.txt" | grep "/releases/" | head -7 | tail -1)"
case "$last_up" in *name=images.json) ok "sha-c1: images.json uploaded after every tarball/sidecar" ;; *) bad "sha-c1 last upload: $last_up" ;; esac

echo "=== 1B. CLI archives: build-cli.sh + cli-validate + cli-publish onto v3.2.0 ==="
if [ -z "${SKIP_CLI:-}" ]; then
  RELEASE=v3.2.0 bash scripts/build-cli.sh "$ROOT/cli" >"$ROOT/build-cli.log" 2>&1 && ok "build-cli.sh" || { bad "build-cli.sh"; tail -5 "$ROOT/build-cli.log"; }
  ls "$ROOT/cli"
  RELEASE_CLI_DIR="$ROOT/cli" bash scripts/release-assets.sh cli-validate && ok "cli-validate"
  git tag v3.2.0 "$c1" && git tag latest "$c1" && git push -q origin v3.2.0 latest
  RELEASE_ASSETS_DIR="$ROOT/assets-${c1:0:12}" bash scripts/release-assets.sh promote 2>&1 | tee "$ROOT/promote.log"
  [ "$(state | jq -r '.latest as $l | .releases[] | select(.id == $l) | .tag_name')" = v3.2.0 ] && ok "promote (no arg) published v3.2.0 as GitHub's latest" || bad "latest marker"
  RELEASE_CLI_DIR="$ROOT/cli" bash scripts/release-assets.sh cli-publish v3.2.0 2>&1 | tee "$ROOT/cli-publish.log"
  assets_of v3.2.0 | tee "$ROOT/v3.2.0-assets.txt"
  grep -q 'checksums.txt' "$ROOT/v3.2.0-assets.txt" && grep -q 'grain-darwin-arm64.tar.gz' "$ROOT/v3.2.0-assets.txt" && ok "v3.2.0 carries the 4 CLI archives + checksums.txt" || bad "cli-publish"
  RELEASE_CLI_DIR="$ROOT/cli" bash scripts/release-assets.sh cli-publish v3.2.0 2>&1 | grep -q 'already' && ok "cli-publish again leaves an existing checksums.txt alone"
fi

echo "=== 1C. channel-main backward / non-ancestor move is refused ==="
package "$cx" >"$ROOT/package-cx.log" 2>&1
publish "$cx" "sha-${cx:0:12}" >/dev/null 2>&1
curl -fsS -X DELETE "http://127.0.0.1:$PORT/_log"
publish "$cx" channel-main 2>&1 | tee "$ROOT/publish-cx-channel.log"
rc=${PIPESTATUS[0]}
curl -fsS "http://127.0.0.1:$PORT/_log" >"$ROOT/log-backward.txt"
[ "$(manifest_commit channel-main)" = "$c1" ] && ok "channel-main still names c1 after a non-ancestor publish (rc=$rc)" || bad "channel moved backward"
grep -q 'not an ancestor' "$ROOT/publish-cx-channel.log" && ok "refusal says why" || bad "no refusal message"
grep -Eq 'POST /api/uploads|DELETE|PATCH' "$ROOT/log-backward.txt" && bad "backward publish wrote: $(grep -E 'POST|DELETE|PATCH' "$ROOT/log-backward.txt")" || ok "backward publish made no write"
echo "--- latest is never a release name"
publish "$c1" latest >"$ROOT/publish-latest.log" 2>&1 && bad "publish latest succeeded" || ok "publish latest refused: $(tail -1 "$ROOT/publish-latest.log")"
echo "=== 1D. channel-main forward onto sha-<c2>: images.json swap order ==="
package "$c2" >"$ROOT/package-c2.log" 2>&1
curl -fsS -X DELETE "http://127.0.0.1:$PORT/_log"
publish "$c2" "sha-${c2:0:12}" channel-main 2>&1 | tee "$ROOT/publish-c2.log"
curl -fsS "http://127.0.0.1:$PORT/_log" >"$ROOT/log-forward.txt"
[ "$(manifest_commit channel-main)" = "$c2" ] && ok "channel-main advanced to c2" || bad "channel-main not on c2"
[ "$(assets_of channel-main)" = "images.json " ] && ok "channel-main still holds images.json alone" || bad "channel-main assets: $(assets_of channel-main)"
grep -nE 'POST /api/uploads|DELETE /api/v3/repos/[^ ]*/releases/assets|PATCH /api/v3/repos/[^ ]*/(releases/assets|git/refs)' "$ROOT/log-forward.txt" | tee "$ROOT/forward-writes.txt"
up_next="$(grep -n 'name=images.json.next' "$ROOT/log-forward.txt" | head -1 | cut -d: -f1)"
del_old="$(grep -n 'DELETE /api/v3/repos/.*/releases/assets/' "$ROOT/log-forward.txt" | tail -1 | cut -d: -f1)"
rename="$(grep -n '"name":"images.json"' "$ROOT/log-forward.txt" | head -1 | cut -d: -f1)"
last_tar="$(grep -n 'name=.*\.tar\.gz' "$ROOT/log-forward.txt" | tail -1 | cut -d: -f1)"
holder_manifest="$(grep -n 'name=images.json$' "$ROOT/log-forward.txt" | head -1 | cut -d: -f1)"
echo "last tarball/sidecar upload=$last_tar holder images.json=$holder_manifest channel .next upload=$up_next delete old=$del_old rename=$rename"
[ -n "$last_tar" ] && [ "$holder_manifest" -gt "$last_tar" ] && ok "sha-c2's images.json uploaded after its tarballs" || bad "holder order"
[ "$up_next" -gt "$holder_manifest" ] && [ "$del_old" -gt "$up_next" ] && [ "$rename" -gt "$del_old" ] &&
  ok "channel-main: tarballs exist on sha-c2 first, then images.json.next up, old images.json deleted, .next renamed into place last" || bad "channel order"
git ls-remote "$ROOT/origin.git" >/dev/null
[ "$(state | jq -r '.tags["channel-main"]')" = "$c2" ] && ok "channel-main git tag moved to c2" || bad "channel-main tag"

echo "--- v3.2.1 at c2 (git tag latest still names c1) is not marked latest"
git tag v3.2.1 "$c2" && git push -q origin v3.2.1
RELEASE_ASSETS_DIR="$ROOT/assets-v321" bash scripts/release-assets.sh promote v3.2.1 >"$ROOT/promote-v321.log" 2>&1
[ "$(state | jq -r '.latest as $l | .releases[] | select(.id == $l) | .tag_name')" = v3.2.0 ] && ok "promote v3.2.1 published it without moving GitHub's latest off v3.2.0" || bad "latest moved to $(state | jq -r '.latest')"
RELEASE_ASSETS_DIR="$ROOT/assets-x" bash scripts/release-assets.sh promote-version >"$ROOT/promote-version.log" 2>&1
[ "$(cat "$ROOT/promote-version.log")" = v3.2.0 ] && ok "promote-version follows the git tag latest (v3.2.0)" || bad "promote-version: $(cat "$ROOT/promote-version.log")"

echo "=== 1E. a live grain daemon's release-asset upgrade prober reads it ==="
mkdir -p "$ROOT/daemon/data" "$ROOT/daemon/sandbox"
printf 'GRAIN_IMAGE=grain/grain:sha-%s\n' "${c1:0:12}" >"$ROOT/daemon/image.env"
printf '#!/bin/sh\necho restart >>%s/daemon/restarts.log\n' "$ROOT" >"$ROOT/daemon/restart.sh"
chmod +x "$ROOT/daemon/restart.sh"
"$GRAIN" daemon -data-dir "$ROOT/daemon/data" -sandbox-dir "$ROOT/daemon/sandbox" -sandbox-port-base=0 \
  -disable-native-sandboxes -ui-addr 127.0.0.1:$DPORT -github-host 127.0.0.1:$PORT -github-insecure-http \
  -upgrade-image grain/grain -upgrade-image-ref-file "$ROOT/daemon/image.env" -upgrade-restart-cmd "$ROOT/daemon/restart.sh" \
  -upgrade-image-source release-assets -upgrade-image-fallback none -upgrade-release-repo 127.0.0.1:$PORT/ai-on-gke/grain \
  -upgrade-allowed-branches 'latest,main,v[0-9]*.[0-9]*.[0-9]*' >"$ROOT/daemon/daemon.log" 2>&1 &
DAEMON_PID=$!
for _ in $(seq 1 120); do curl -fsS "http://127.0.0.1:$DPORT/healthz" >/dev/null 2>&1 && break; sleep 0.25; done
code="$("$GRAIN" access-code -data-dir "$ROOT/daemon/data")"
curl -fsS -X DELETE "http://127.0.0.1:$PORT/_log"
curl -sS -H "Authorization: Bearer $code" "http://127.0.0.1:$DPORT/api/upgrade/targets" | tee "$ROOT/targets.json" | jq -c . | head -c 3000
echo
jq -e '.. | strings | select(test("main"))' "$ROOT/targets.json" >/dev/null && ok "upgrade targets list the main channel" || bad "no main target"
curl -fsS "http://127.0.0.1:$PORT/_log" >"$ROOT/log-daemon.txt"
echo "--- daemon requests"
head -30 "$ROOT/log-daemon.txt"
echo "--- POST /api/upgrade {branch: main} (verifies images.json + sidecars, then docker loads)"
curl -sS -X POST -H "Authorization: Bearer $code" -H 'Content-Type: application/json' \
  "http://127.0.0.1:$DPORT/api/upgrade" -d '{"branch":"main"}' | jq -c . | head -c 1500
echo
for _ in $(seq 1 120); do
  ph="$(curl -fsS -H "Authorization: Bearer $code" "http://127.0.0.1:$DPORT/api/upgrade" | jq -r .phase)"
  case "$ph" in idle | done | failed | error | restarting | "") break ;; esac
  sleep 0.5
done
curl -fsS -H "Authorization: Bearer $code" "http://127.0.0.1:$DPORT/api/upgrade" | tee "$ROOT/upgrade-status.json" | jq -c . | head -c 2000
echo
curl -fsS "http://127.0.0.1:$PORT/_log" >"$ROOT/log-daemon-upgrade.txt"
grep -c 'sha256\|tar.gz\|images.json' "$ROOT/log-daemon-upgrade.txt" || true
cat "$ROOT/daemon/image.env"
id2="$(jq -r .images.grain.imageId "$ROOT/assets-${c2:0:12}/images.json")"
jq -e '.phase == "ok"' "$ROOT/upgrade-status.json" >/dev/null && ok "upgrade to main: every step done (pull, health check, unit flags, schema, record, restart)" || bad "upgrade phase: $(jq -r .phase "$ROOT/upgrade-status.json")"
loaded="$(docker image inspect --format '{{.Id}}' "$(sed -n 's/^GRAIN_IMAGE=//p' "$ROOT/daemon/image.env")")"
[ "$loaded" = "$id2" ] && ok "image.env ($(cat "$ROOT/daemon/image.env")) now names c2's grain image $id2" || bad "image.env names $loaded, want $id2"
grep -q 'release channel-main' "$ROOT/upgrade-status.json" && ok "the image was loaded from release channel-main's images.json + sha- holder" || bad "source"

after_cfg="$(sha256sum "$checkout/.git/config" | cut -d' ' -f1)"
[ "$before_cfg" = "$after_cfg" ] && ok "checkout .git/config unchanged" || bad "checkout .git/config changed"
echo "item1: $pass passed, $fail failed"
