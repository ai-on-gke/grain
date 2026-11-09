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

# run-ghcr-prune.sh: 2026-11-09 §6 item 2. Starts registry-fake.go on
# 127.0.0.1:443, points hub.docker.com and api.github.com at it in
# /etc/hosts (restored, or removed if there was none, on exit), and runs the real cmd/ghcr-prune with
# SSL_CERT_FILE set to the fake's CA: a Docker Hub dry run, -apply, and a
# second dry run; then the same for the legacy GHCR guest-base package.
#
# Usage: run-ghcr-prune.sh <repo checkout> <out dir>
set -euo pipefail

repo=$(cd "${1:?repo checkout}" && pwd)
out=$(mkdir -p "${2:?out dir}" && cd "$2" && pwd)
here=$(cd "$(dirname "$0")" && pwd)

go build -o "$out/registry-fake" "$here/registry-fake.go"
(cd "$repo" && go build -o "$out/ghcr-prune" ./cmd/ghcr-prune)

# A sandbox may have no /etc/hosts at all; put back exactly what was there.
had_hosts=false
if [ -e /etc/hosts ]; then
	had_hosts=true
	sudo cp /etc/hosts "$out/hosts.orig"
fi
restore() {
	if [ "$had_hosts" = true ]; then sudo cp "$out/hosts.orig" /etc/hosts; else sudo rm -f /etc/hosts; fi
	[ -n "${fake:-}" ] && sudo kill "$fake" 2>/dev/null || true
}
trap restore EXIT
echo "127.0.0.1 hub.docker.com api.github.com" | sudo tee -a /etc/hosts >/dev/null

rm -f "$out/deletes.log" "$out/ca.pem"
sudo "$out/registry-fake" -out "$out" >"$out/fake.log" 2>&1 &
fake=$!
for _ in $(seq 50); do [ -s "$out/ca.pem" ] && break; sleep 0.1; done
sleep 0.3

export SSL_CERT_FILE="$out/ca.pem"
unset PACKAGE_PUBLISH_REGISTRY REGISTRY_PUSH_REGISTRY GRAIN_DEPLOYED_IMAGES
hub=(-registry docker.io -namespace grainai -username grainai -owner ai-on-gke -repo grain
	-packages grain,sandbox,sandbox-base,sandbox-k8s)
"$out/ghcr-prune" "${hub[@]}" >"$out/hub-dry-run.txt" 2>&1
"$out/ghcr-prune" "${hub[@]}" -token fake-pat -apply >"$out/hub-apply.txt" 2>&1
"$out/ghcr-prune" "${hub[@]}" >"$out/hub-dry-run-after.txt" 2>&1

gh=(-registry ghcr.io -owner ai-on-gke -repo grain -packages grain,guest-base -token fake-token)
"$out/ghcr-prune" "${gh[@]}" >"$out/ghcr-dry-run.txt" 2>&1
"$out/ghcr-prune" "${gh[@]}" -apply >"$out/ghcr-apply.txt" 2>&1
"$out/ghcr-prune" "${gh[@]}" >"$out/ghcr-dry-run-after.txt" 2>&1
echo "done: $(wc -l <"$out/deletes.log") DELETE calls"
