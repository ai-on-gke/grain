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

# Seeds a `grain demo -secrets-store` for items 3 and 5 of
# docs/test-plans/2026-11-08-afternoon.md §1: the retired-capability
# secrets (github-app, registry-push, cosign-key, cosign-password), one
# secret bound to a capability server, one nothing claims, and enough
# filler secrets that Other secrets holds 20+ rows (the demo already
# lists 13 capability servers).
#
#   seed.sh <base-url> <access-code>

set -euo pipefail
BASE=$1
CODE=$2
op() { curl -sf -H "Authorization: Bearer $CODE" -H "Content-Type: application/json" "$@"; }
put() { op -X PUT "$BASE/api/secrets/$1" -d "{\"value\":\"SEEDED-VALUE-${1//\//-}\"}" >/dev/null; }

for s in github-app/app-id github-app/private-key registry-push/token \
	cosign-key/key cosign-password/value unclaimed-thing/token; do
	put "$s"
done
# A capability server's own secret, bound through Bind a secret.
op -X POST "$BASE/api/capability-servers/bind-secret" \
	-d '{"id":"https://capabilities.demo.grain.example/cloud","kind":"header","name":"Authorization","secret":"cloud-bearer/token","prefix":"Bearer ","value":"SEEDED-VALUE-cloud"}' >/dev/null
for i in $(seq -w 1 18); do
	put "filler-team-credential-with-a-long-name-$i/token"
done
op "$BASE/api/secrets" | jq -c '{count: (.secrets|length), names: [.secrets[]|{name, capabilityServers}]}'
