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

# Copyright 2026 Google LLC
# SPDX-License-Identifier: Apache-2.0
#
# Management-side `grain registry` walk for the 2026-11-07 §1 walkthrough:
# unlisted privacy, scopes, rotate-credential, two-step transfer, unregister
# URL validation, delete/restore and create, as alice, bob and carol.
# Expects env-up.sh and the conn-alice / bob-ws entries to exist.
set -uo pipefail
G="${G:-/home/debian/bin/grain}"
RA=http://127.0.0.1:4251
RB=http://127.0.0.1:4261
b64u() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
fakejwt() { printf '%s.%s.sig' "$(printf '{"alg":"none"}' | b64u)" "$(printf '{"email":"%s","sub":"%s","aud":"walk","exp":%d}' "$1" "$1" $(($(date +%s) + 3600)) | b64u)"; }
unset GRAIN_SERVER GRAIN_TOKEN
as() { # as <email> <registry> <args...>
  local who=$1 reg=$2
  shift 2
  echo "\$ [$who @ ${reg##*:}] grain registry $*"
  GRAIN_REGISTRY_URL="$reg" GRAIN_REGISTRY_TOKEN="$(fakejwt "$who")" "$G" registry "$@" 2>&1 | sed 's/^/    /' | head -n 12
  echo "    (exit ${PIPESTATUS[0]})"
}

echo "=== unlisted privacy"
as bob@beta.test $RA update -unlisted bob-ws
for who in alice@alpha.test carol@alpha.test root@alpha.test; do
  as "$who" $RB directory
  as "$who" $RB route beta/svc
  as "$who" $RB show bob-ws
done
as bob@beta.test $RB show bob-ws

echo "=== scopes (owner vs other tenant)"
as alice@alpha.test $RA scopes -set alpha/app:services/api:primary -set alpha/lib:secondary conn-alice
as bob@beta.test $RA scopes -set alpha/app conn-alice
as bob@beta.test $RA scopes -set alpha/app bob-ws
as alice@alpha.test $RB route alpha/app services/api

echo "=== rotate-credential (other tenant refused, owner rotates with overlap)"
as bob@beta.test $RA rotate-credential conn-alice
as alice@alpha.test $RA rotate-credential -label walk-rotation -overlap 1h -token-file /tmp/grwalk/conn-alice.rotated.token conn-alice

echo "=== two-step transfer bob-ws -> carol"
as alice@alpha.test $RA transfer -to carol@alpha.test bob-ws
as bob@beta.test $RA transfer -to carol@alpha.test bob-ws
as alice@alpha.test $RA accept-transfer bob-ws
as carol@alpha.test $RA accept-transfer bob-ws
as carol@alpha.test $RA show bob-ws
as bob@beta.test $RA show bob-ws

echo "=== unregister URL validation (must refuse before minting any OIDC token)"
# No GRAIN_REGISTRY_TOKEN here: the CLI would mint an OIDC token with gcloud,
# so put a gcloud on PATH that records being called.
mkdir -p /tmp/grwalk/fakebin && rm -f /tmp/grwalk/gcloud-called
printf '#!/bin/sh\necho "$@" >> /tmp/grwalk/gcloud-called\necho fake.oidc.token\n' >/tmp/grwalk/fakebin/gcloud
chmod +x /tmp/grwalk/fakebin/gcloud
for u in http://registry.example.com 'ftp://registry.example.com' 'https://user:pw@registry.example.com' 'https://registry.example.com/x?y=1#frag'; do
  echo "\$ grain registry unregister -standalone -oidc -registry-url '$u' conn-alice"
  PATH=/tmp/grwalk/fakebin:$PATH "$G" registry unregister -standalone -oidc -registry-url "$u" conn-alice 2>&1 | sed 's/^/    /' | head -n 3
done
echo "    gcloud invoked: $( [ -f /tmp/grwalk/gcloud-called ] && cat /tmp/grwalk/gcloud-called || echo never)"

echo "=== delete / restore"
as bob@beta.test $RA delete -yes carol-gone 2>/dev/null
as alice@alpha.test $RA delete -yes bob-ws
as carol@alpha.test $RA delete -yes bob-ws
as carol@alpha.test $RA list -include-deleted
as alice@alpha.test $RA restore bob-ws
as carol@alpha.test $RA restore bob-ws
as carol@alpha.test $RA list

echo "=== create (compute.provider: none)"
as alice@alpha.test $RA create -name alice-vm -shape standard
as mallory@evil.test $RA list
