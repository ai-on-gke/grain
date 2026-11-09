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
# Catalog isolation matrix for the 2026-11-07 §1 walkthrough: run read-side
# `grain registry` subcommands as four principals (alice = conn-alice owner,
# carol = alice's group, bob = other tenant, root = registry admin) against
# replica A and B, and print what each one sees. Expects env-up.sh plus the
# conn-alice / bob-ws connected entries the walkthrough registers.
set -uo pipefail
G="${G:-/home/debian/bin/grain}"
b64u() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
fakejwt() { printf '%s.%s.sig' "$(printf '{"alg":"none"}' | b64u)" "$(printf '{"email":"%s","sub":"%s","aud":"walk","exp":%d}' "$1" "$1" $(($(date +%s) + 3600)) | b64u)"; }
unset GRAIN_SERVER GRAIN_TOKEN
for who in alice@alpha.test carol@alpha.test bob@beta.test root@alpha.test; do
  tok="$(fakejwt "$who")"
  for reg in http://127.0.0.1:4251 http://127.0.0.1:4261; do
    echo "################ $who via $reg"
    for cmd in "list" "list -all" "directory" "directory -repo alpha/app" "directory -repo beta/svc" \
      "route alpha/app" "route beta/svc" "show conn-alice" "show bob-ws"; do
      # shellcheck disable=SC2086
      out="$(GRAIN_REGISTRY_URL="$reg" GRAIN_REGISTRY_TOKEN="$tok" "$G" registry $cmd 2>&1 | tr '\n' '|' | cut -c1-220)"
      printf '  %-26s %s\n' "$cmd" "$out"
    done
  done
done
