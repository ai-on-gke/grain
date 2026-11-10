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

# Brings up what ui-walk.mjs walks, and leaves it running:
#   - fakegcp-walk.sh KEEP=1: the fake-GCP hub (alice :4281, root :4284), with
#     walk-creating held in `creating` by SA_DELAY (default 60 s);
#   - a connected entry walk-conn with a minted credential;
#   - interop.sh's shapeless HEAD hub (compute: none), fronted as alice on :4235.
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
G="${G:-/tmp/bin-new/grain}"
PAIR=new-hub KEEP=1 HUB_ONLY=1 "$HERE/interop.sh" >/tmp/noshapes-up.log 2>&1
nohup /tmp/interop-new-hub/bin/iapstub -key /tmp/interop-new-hub/keys/iap.pem -addr 127.0.0.1:4235 \
  -upstream http://127.0.0.1:4232 -audience /projects/walk/global/backendServices/hub -as alice@example.com >/tmp/interop-new-hub/logs/iap-4235.log 2>&1 &
KEEP=1 WAIT_TICKS=60 "$HERE/fakegcp-walk.sh" >/tmp/fakegcp-keep.txt 2>&1
tail -n 2 /tmp/fakegcp-keep.txt
b64u() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
T=$(printf '%s.%s.sig' "$(printf '{"alg":"none"}' | b64u)" "$(printf '{"email":"alice@example.com","sub":"alice@example.com","aud":"walk","exp":%d}' $(($(date +%s) + 3600)) | b64u)")
"$G" hub register -standalone -hub-url http://127.0.0.1:4281 -hub-token "$T" -mint-token -token-file /tmp/fakegcp-walk/walk-conn.token walk-conn 2>&1 | head -n 1
echo "shapeless hub /api/shapes: $(curl -sS http://127.0.0.1:4235/api/shapes)"
