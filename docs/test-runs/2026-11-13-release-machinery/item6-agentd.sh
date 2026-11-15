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

# Walkthrough 2026-11-13 §9 item 6: `make dist-agentd` with a throwaway
# Ed25519 release key stamped in and signing each manifest, served by a
# real `grain daemon` from GRAIN_AGENTD_DIST_DIR with that key pinned
# (GRAIN_AGENTD_RELEASE_PUBLIC_KEY), then checked the way a host does,
# and refused once another platform's binary or a tampered one is put in
# its place.
#
#   bash item6-agentd.sh <root> [daemon-port]
set -euo pipefail
root="${1:?root}"
port="${2:-4490}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$here/../../.." && pwd)"
grain="${GRAIN:-/home/debian/bin/grain}"
rm -rf "$root" && mkdir -p "$root/data" "$root/sandbox" "$root/host"
api="http://127.0.0.1:$port"
(cd "$repo_root" && go build -o "$root/grain-agentd-sign" ./pkg/agentd/release/cmd/grain-agentd-sign)
sign="$root/grain-agentd-sign"

echo "== build and sign with a throwaway key"
"$sign" keygen -private-key "$root/throwaway.key" -public-key "$root/throwaway.pub"
(cd "$repo_root" && RELEASE=v0.0.0-walkthrough AGENTD_RELEASE_PUBLIC_KEY="$(cat "$root/throwaway.pub")" \
  AGENTD_RELEASE_SIGNING_KEY="$root/throwaway.key" ./scripts/build-agentd.sh "$root/dist" 2>&1 | grep -E 'wrote|signed' || true)
for p in amd64 arm64; do
  jq -c '{version, platform, size, signed: (.signature != null and .signature != "")}' "$root/dist/grain-agentd-linux-$p.manifest.json"
done

echo "== the controller serves each platform's signed binary"
nohup env GRAIN_AGENTD_DIST_DIR="$root/dist" GRAIN_AGENTD_RELEASE_PUBLIC_KEY="$(cat "$root/throwaway.pub")" GRAIN_AGENTD_RELEASE_URL=off \
  "$grain" daemon -data-dir "$root/data" -sandbox-dir "$root/sandbox" -sandbox-port-base=0 -disable-native-sandboxes \
  -disable-self-upgrade -ui-addr "127.0.0.1:$port" -github-host 127.0.0.1:9 -github-insecure-http \
  >"$root/daemon.log" 2>&1 </dev/null &
echo $! >"$root/daemon.pid"
for _ in $(seq 1 240); do [ "$(curl -s -o /dev/null -w '%{http_code}' "$api/healthz")" != 000 ] && break; sleep 0.25; done
fetch() { # arch -> writes $root/host/<arch>{,.manifest.json}, prints statuses
  local m b
  m=$(curl -s -o "$root/host/$1.manifest.json" -w '%{http_code}' "$api/api/hostd/agentd/linux/$1/manifest.json")
  b=$(curl -s -o "$root/host/$1" -w '%{http_code}' "$api/api/hostd/agentd/linux/$1")
  echo "manifest HTTP $m, binary HTTP $b"
}
host_check() { # arch: what an enrolled host pinned to the throwaway key concludes
  "$sign" verify -manifest "$root/host/$1.manifest.json" -binary "$root/host/$1" -public-key "$root/throwaway.pub" 2>&1 | tail -1
}
for p in amd64 arm64; do
  echo "linux/$p: $(fetch $p)"
  echo "  host verify (throwaway key): $(host_check $p)"
  echo "  host verify (the repository's own key): $("$sign" verify -manifest "$root/host/$p.manifest.json" -binary "$root/host/$p" \
    -public-key "$repo_root/pkg/agentd/release/agentd-release.pub" 2>&1 | tail -1)"
done
echo "  host verify: arm64's binary against amd64's manifest: $("$sign" verify -manifest "$root/host/amd64.manifest.json" \
  -binary "$root/host/arm64" -public-key "$root/throwaway.pub" 2>&1 | tail -1)"

echo "== another platform's binary in amd64's place"
cp "$root/dist/grain-agentd-linux-amd64" "$root/amd64.orig"
cp "$root/dist/grain-agentd-linux-arm64" "$root/dist/grain-agentd-linux-amd64"
echo "linux/amd64: $(fetch amd64)"
echo "== a tampered amd64 binary (same size, one byte changed)"
cp "$root/amd64.orig" "$root/dist/grain-agentd-linux-amd64"
printf '\x00' | dd of="$root/dist/grain-agentd-linux-amd64" bs=1 seek=4096 conv=notrunc status=none
touch "$root/dist/grain-agentd-linux-amd64"
echo "linux/amd64: $(fetch amd64)"
echo "  host verify of the tampered file: $("$sign" verify -manifest "$root/dist/grain-agentd-linux-amd64.manifest.json" \
  -binary "$root/dist/grain-agentd-linux-amd64" -public-key "$root/throwaway.pub" 2>&1 | tail -1)"
echo "== the original back"
cp "$root/amd64.orig" "$root/dist/grain-agentd-linux-amd64"
echo "linux/amd64: $(fetch amd64)"

echo "== enroll a host with the downloaded linux/$(go env GOARCH) binary"
arch=$(go env GOARCH)
"$grain" access-code -data-dir "$root/data" >"$root/code"
join=$(curl -fsS -X POST -H "Authorization: Bearer $(cat "$root/code")" -H 'Content-Type: application/json' \
  -d '{"name":"walkthrough"}' "$api/api/hostds/enrollment-codes" | jq -r '.code // .secret // .joinCode // empty')
chmod +x "$root/host/$arch"
mkdir -p "$root/hosthome" "$root/fakebin"
# A fake agent CLI: install only looks for one to put in the allowlist.
printf '#!/bin/sh\necho "fake claude 0.0.0"\n' >"$root/fakebin/claude" && chmod +x "$root/fakebin/claude"
PATH="$root/fakebin:$PATH" HOME="$root/hosthome" timeout 60 "$root/host/$arch" install -server "$api" -join "$join" -name walkthrough-host -no-start \
  >"$root/install.log" 2>&1 && echo "install: ok" || echo "install: exit $? ($(tail -1 "$root/install.log"))"
grep -E 'release|enroll|joined|registered' "$root/install.log" | sed 's/^/  /' || true
echo "  hosts the daemon knows: $(curl -fsS -H "Authorization: Bearer $(cat "$root/code")" "$api/api/hostds" | jq -c '[.[]? | {name, platform: (.platform // .arch // null)}]' 2>/dev/null)"
pinned="$root/hosthome/.config/grain/agentd-release.pub"
echo "  pinned key on the host: $(grep -o 'key_id: [0-9a-f]*' "$pinned" 2>/dev/null || head -c 80 "$pinned" 2>/dev/null || echo none)"
echo "  host verify with its pinned key: $("$sign" verify -manifest "$root/host/$arch.manifest.json" -binary "$root/host/$arch" \
  -public-key "$pinned" 2>&1 | tail -1)"
echo "  host verify of the other platform's binary: $("$sign" verify -manifest "$root/host/$arch.manifest.json" \
  -binary "$root/host/$([ "$arch" = amd64 ] && echo arm64 || echo amd64)" -public-key "$pinned" 2>&1 | tail -1)"
kill "$(cat "$root/daemon.pid")" 2>/dev/null || true
