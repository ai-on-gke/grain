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

# Walkthrough harness for docs/test-plans/2026-11-10.md section 8 item 6:
# extracts promote.yml's "Resolve the release and whether it still needs
# its CLI" step and runs it with the real curl against a local HTTP server
# standing in for github.com and api.github.com (GITHUB_SERVER_URL,
# GITHUB_API_URL), in a scratch clone tagged v9.9.0 and latest (pushed to a local bare
# origin). Prints the
# step's log and GITHUB_OUTPUT for each case. Contacts nothing outside
# 127.0.0.1.
#
#   bash docs/test-runs/2026-11-10-release-train/promote-cli-decision.sh [repo-root]
set -uo pipefail

root="$(cd "${1:-$(dirname "${BASH_SOURCE[0]}")/../../..}" && pwd)"
work="$(mktemp -d /tmp/promote-cli.XXXXXX)"
trap 'kill "$server" 2>/dev/null; rm -rf "$work"' EXIT
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

awk '/- name: Resolve the release and whether it still needs its CLI/{on=1} on&&/run: \|/{grab=1; next}
  grab&&/^      - name:/{exit} grab{sub(/^          /,""); print}' "$root/.github/workflows/promote.yml" > "$work/step.sh"
[ -s "$work/step.sh" ] || { echo "could not extract the step from promote.yml" >&2; exit 2; }

git init -q -b main "$work/repo" && cd "$work/repo" || exit 1
mkdir scripts && cp "$root/scripts/release-assets.sh" scripts/
git add . && git commit -q -m seed && git tag -a v9.9.0 -m "Release v9.9.0" && git tag -fa latest -m latest
git init -q --bare "$work/origin.git" && git remote add origin "$work/origin.git" && git push -q origin main --tags
commit="$(git rev-parse HEAD)"

mkdir -p "$work/www"
port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')"
(cd "$work/www" && exec python3 -m http.server --bind 127.0.0.1 "$port" >/dev/null 2>&1) &
server=$!
for _ in $(seq 50); do curl -fs -o /dev/null "http://127.0.0.1:$port/" && break; sleep 0.1; done

run_case() { # run_case <label> [PROMOTE_VERSION]
  local status=0
  : > "$work/out"
  echo "=== $1 (PROMOTE_VERSION=${2:-})"
  (cd "$work/repo" && env GITHUB_OUTPUT="$work/out" GITHUB_SERVER_URL="http://127.0.0.1:$port" GITHUB_API_URL="http://127.0.0.1:$port/api" \
    GITHUB_REPOSITORY=acme/widgets PROMOTE_VERSION="${2:-}" bash "$work/step.sh") 2>&1 | sed 's/^/    log: /' || status=$?
  sed 's/^/    output: /' "$work/out"
}
dl="$work/www/acme/widgets/releases/download"
api="$work/www/api/repos/acme/widgets/releases/tags"

rm -rf "$work/www/acme" "$work/www/api"
run_case "no GitHub Release and no release assets"
mkdir -p "$dl/sha-${commit:0:12}" && echo '{}' > "$dl/sha-${commit:0:12}/images.json"
run_case "release assets only (sha-<commit> images.json, no GitHub Release yet)"
rm -rf "$work/www/acme" && mkdir -p "$api" && echo '{}' > "$api/v9.9.0"
run_case "a full GitHub Release without its CLI"
mkdir -p "$dl/v9.9.0" && echo x > "$dl/v9.9.0/checksums.txt"
run_case "a release already carrying checksums.txt"
rm -rf "$work/www/acme" "$work/www/api"
run_case "asked for by name with nothing published" v9.9.0
