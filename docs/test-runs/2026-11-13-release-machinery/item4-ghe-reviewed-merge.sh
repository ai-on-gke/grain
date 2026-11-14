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

# Walkthrough 2026-11-13 §9 item 4: cmd/reviewed-merge (the check
# release.yml and the daemon's -upgrade-reviewed-merge-repo share,
# pkg/reviewedmerge.REST) asked about ghe.example.com/acme/widgets with no
# base URL, so it derives https://ghe.example.com/api/v3 itself.
# ghe.example.com is pointed at 127.0.0.1 in /etc/hosts, served by
# ghesim.go over TLS with a throwaway certificate trusted through
# SSL_CERT_FILE. Needs sudo (port 443, /etc/hosts).
#
#   bash item4-ghe-reviewed-merge.sh <root>
set -euo pipefail
root="${1:?root}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$here/../../.." && pwd)"
mkdir -p "$root"
grep -q 'ghe.example.com' /etc/hosts || echo '127.0.0.1 ghe.example.com' | sudo tee -a /etc/hosts >/dev/null
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -days 1 -subj '/CN=ghe.example.com' \
  -addext 'subjectAltName=DNS:ghe.example.com' -keyout "$root/ghe.key" -out "$root/ghe.pem" 2>/dev/null
(cd "$repo_root" && go build -o "$root/ghesim" "$here/ghesim.go" && go build -o "$root/reviewed-merge" ./cmd/reviewed-merge)
sudo pkill -x ghesim 2>/dev/null || true
sudo nohup "$root/ghesim" -addr 127.0.0.1:443 -ctl 127.0.0.1:4297 -cert "$root/ghe.pem" -key "$root/ghe.key" \
  -dir "$root" -shas "$root/shas.json" >"$root/ghesim.log" 2>&1 </dev/null &
for _ in $(seq 1 80); do [ -s "$root/shas.json" ] && curl -fsS -o /dev/null http://127.0.0.1:4297/_calls 2>/dev/null && break; sleep 0.25; done
base=$(jq -r .base "$root/shas.json"); approved=$(jq -r .approved "$root/shas.json"); self=$(jq -r .self "$root/shas.json")
check() { # commit since -> exit code, output
  set +e
  SSL_CERT_FILE="$root/ghe.pem" GITHUB_API_URL='' GITHUB_TOKEN=ghe-read-token \
    "$root/reviewed-merge" -repo ghe.example.com/acme/widgets -commit "$1" -since "$2" -mergers grain-bot 2>&1
  echo "exit $?"
  set -e
}
calls() { curl -fsS http://127.0.0.1:4297/_calls | sed '/^$/d' | sed 's/^/    /'; }
mark=0
newcalls() { local all; all=$(calls); echo "$all" | tail -n +$((mark + 1)); mark=$(echo "$all" | wc -l); }

echo "== a. the approved merge (alice's PR, approved by bob)"
check "$approved" "$base"
echo "  forge calls:"; newcalls
echo "== b. the self-merge on top (alice's PR, merged by alice, no review)"
check "$self" "$approved"
echo "  forge calls:"; newcalls
echo "== c. the approved merge again, the first request answered 502"
curl -fsS -XPOST 'http://127.0.0.1:4297/_fail?n=1&status=502'
check "$approved" "$base"
echo "  forge calls:"; newcalls
echo "== d. every request answered 502"
curl -fsS -XPOST 'http://127.0.0.1:4297/_fail?n=100&status=502'
check "$approved" "$base"
echo "  forge calls (count): $(newcalls | wc -l)"
curl -fsS -XPOST 'http://127.0.0.1:4297/_fail?n=0'
echo "== e. any request to api.github.com?"
calls | grep -c 'api.github.com' || true
