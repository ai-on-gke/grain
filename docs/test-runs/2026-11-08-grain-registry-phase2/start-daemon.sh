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

# Register connected entry conn-alice (owner alice@example.com) through
# replica A and start the tunnelled `grain daemon` with the registry verifier.
#   docs/test-runs/2026-11-08-grain-registry-phase2/start-daemon.sh [registry-url]
set -euo pipefail
. "$(dirname "$0")/lib.sh"
REG="${1:-$RA}"
mkdir -p "$WALK/d1/scratch"
if [ ! -s "$WALK/conn-alice.token" ]; then
  "$G" registry register -standalone -registry-url "$REG" -registry-token "$(fakejwt alice@example.com)" \
    -mint-token -token-file "$WALK/conn-alice.token" -listed conn-alice >"$WALK/logs/register.log" 2>&1
fi
nohup "$G" daemon -data-dir "$WALK/d1" -sandbox-dir "$WALK/d1/scratch" -disable-native-sandboxes -sandbox-port-base=0 -ci=false \
  -deployment-owner=alice@example.com -ui-allowed-principals=user:alice@example.com \
  -ui-addr=127.0.0.1:4270 -ui-allowed-hosts=conn-alice.grain-user.test,127.0.0.1,localhost \
  -registry-url "$REG" -registry-entry conn-alice -registry-token-file "$WALK/conn-alice.token" -registry-connect \
  -ui-require-verifier=registry -ui-registry-issuer=grain-registry -ui-registry-audience=https://conn-alice.grain-user.test \
  -ui-registry-keys-url=http://127.0.0.1:4253/.well-known/jwks.json >>"$WALK/logs/d1.log" 2>&1 &
echo $! >"$WALK/d1.pid"
for _ in $(seq 1 60); do
  curl -fsS -o /dev/null http://127.0.0.1:4270/healthz 2>/dev/null && break
  sleep 0.5
done
echo "daemon pid $(cat "$WALK/d1.pid")"
