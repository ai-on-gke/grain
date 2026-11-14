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

# Walkthrough 2026-11-13 §9 item 5 on the daemon start-daemon.sh started
# with -release-approval-kms-key against kmsfakesrv.go:
#   a. GET /api/release-approval-key serves KMS's public key for the version;
#   b. a person's Approve of the pending v1.1.0 dispatches promote-release.yml
#      with a record KMS signed, which scripts/verify-release-approval.sh
#      accepts with that key, and refuses once tampered or with another key;
#   c. a daemon given a malformed key version refuses to start.
#
#   bash item5-kms.sh <root> [daemon-port]
set -euo pipefail
root="${1:?root}"
port="${2:-4450}"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$here/../../.." && pwd)"
sim="http://127.0.0.1:$((port + 10))"
kms="http://127.0.0.1:$((port + 20))"
api="http://127.0.0.1:$port"
auth="Authorization: Bearer $(cat "$root/code")"
key="projects/demo-project/locations/us/keyRings/kr/cryptoKeys/k/cryptoKeyVersions/1"

echo "== a. the approval key the daemon serves"
curl -fsS -H "$auth" "$api/api/release-approval-key" >"$root/approval-key.json"
jq -r '.variable, .note' "$root/approval-key.json"
jq -r '.publicKey' "$root/approval-key.json" >"$root/served.pem"
curl -fsS -H 'Authorization: Bearer fake-kms-token' "$kms/v1/$key/publicKey" | jq -r '.pem' >"$root/kms.pem"
if cmp -s <(openssl pkey -pubin -in "$root/served.pem" -outform DER) <(openssl pkey -pubin -in "$root/kms.pem" -outform DER); then
  echo "served key == KMS getPublicKey for $key: yes"
else
  echo "served key == KMS getPublicKey for $key: NO"
fi
echo "local key file made beside the KMS one: $(ls "$root/data"/release-approval* 2>/dev/null || echo none)"

echo "== b. a person's Approve of v1.1.0"
curl -fsS -X PUT -H "$auth" -H 'Content-Type: application/json' -d '{"ciMode":"github"}' \
  "$api/api/repos/acme/widgets/ci-mode" >/dev/null || echo "(ci-mode PUT refused)"
echo "signs before: $(curl -fsS "http://127.0.0.1:$((port + 21))/counts" | jq .signs)"
code=$(curl -sS -o "$root/approve.json" -w '%{http_code}' -X POST -H "$auth" -H 'Content-Type: application/json' -d '{}' \
  "$api/api/repos/acme/widgets/pending-releases/v1.1.0/approve")
echo "approve: HTTP $code $(head -c 400 "$root/approve.json")"
echo "signs after: $(curl -fsS "http://127.0.0.1:$((port + 21))/counts" | jq .signs)"
curl -fsS "$sim/_dispatches" >"$root/dispatches.json"
d=$(jq -c '[.[] | select(.workflow | test("promote-release"))] | last' "$root/dispatches.json")
echo "dispatch: $(jq -c '{workflow, ref, version: .inputs.version, decision: .inputs.decision}' <<<"$d")"
verify() { # pem record signature
  (cd "$root/sim/seed" && APPROVAL="$2" APPROVAL_SIGNATURE="$3" APPROVAL_PUBLIC_KEY="$(cat "$1")" \
    EXPECT_REPO=acme/widgets EXPECT_VERSION=v1.1.0 EXPECT_DECISION=approve \
    bash "$repo_root/scripts/verify-release-approval.sh" 2>&1)
}
rec=$(jq -r '.inputs.approval' <<<"$d")
sig=$(jq -r '.inputs.approval_signature' <<<"$d")
git -C "$root/sim/seed" fetch -q --tags "$root/sim/acme/widgets.git" 2>/dev/null || true
echo "verify with the KMS key: $(verify "$root/kms.pem" "$rec" "$sig") (exit $?)" || true
tampered=$(printf '%s' "$rec" | base64 -d | sed 's/approve/reject/' | base64 -w0)
echo "verify a tampered record: $(verify "$root/kms.pem" "$tampered" "$sig" || true)"
openssl ecparam -name prime256v1 -genkey -noout -out "$root/other.key" 2>/dev/null
openssl ec -in "$root/other.key" -pubout -out "$root/other.pem" 2>/dev/null
echo "verify with another key: $(verify "$root/other.pem" "$rec" "$sig" || true)"

echo "== c. a daemon given a bad key version"
for bad in "projects/demo-project/locations/us/keyRings/kr/cryptoKeys/k" "not-a-key-version"; do
  rm -rf "$root/bad" && mkdir -p "$root/bad/data" "$root/bad/sandbox"
  set +e
  GRAIN_KMS_ENDPOINT="$kms/v1/" GRAIN_KMS_TOKEN=fake-kms-token timeout 20 "${GRAIN:-/home/debian/bin/grain}" daemon \
    -data-dir "$root/bad/data" -sandbox-dir "$root/bad/sandbox" -sandbox-port-base=0 -disable-native-sandboxes \
    -disable-self-upgrade -ui-addr 127.0.0.1:$((port + 5)) -github-host 127.0.0.1:9 -github-insecure-http \
    -release-approval-kms-key "$bad" >"$root/bad/daemon.log" 2>&1
  rc=$?
  set -e
  echo "-release-approval-kms-key $bad: exit $rc: $(grep -iE 'kms|key version' "$root/bad/daemon.log" | tail -1 | cut -c1-300)"
done
