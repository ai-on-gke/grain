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

# Environment for docs/test-runs/2026-11-03-operator-attribution.md:
# a real `grain demo -github-sim -secrets-store` behind tests/iap/iapsim.go
# (simulated IAP), as docs/test-runs/2026-11-02-iap-end-to-end.md runs it.
#
#   source env.sh; start_env OUT   # starts iapsim + demo, sets CODE, DB, PROXY, DEMO
#
# Needs GRAIN (a grain built with the embedded UI) and IAPSIM
# (go build -o $IAPSIM tests/iap/iapsim.go).
GRAIN=${GRAIN:-/home/debian/bin/grain}
IAPSIM=${IAPSIM:-/home/debian/bin/iapsim}
AUD=/projects/123/global/backendServices/456
ISS=https://iap.test.local
ISSUER=http://127.0.0.1:18080
PROXY=http://127.0.0.1:18443
DEMO=http://127.0.0.1:4300

as() { echo "X-Iapsim-Email: $1"; }

start_env() {
	OUT=$1
	shift
	mkdir -p "$OUT/tmp"
	"$IAPSIM" -issuer 127.0.0.1:18080 -proxy 127.0.0.1:18443 -upstream "$DEMO" -aud "$AUD" -iss "$ISS" \
		> "$OUT/iapsim.log" 2>&1 &
	IAPSIM_PID=$!
	TMPDIR=$OUT/tmp GCE_METADATA_HOST=127.0.0.1:9 "$GRAIN" demo -open=false -addr 127.0.0.1:4300 -as owner@example.com \
		-access-code-file "$OUT/access-code" -github-sim -fake-ci -secrets-store -reconcile 2s \
		-ui-iap-audience "$AUD" -ui-iap-issuer "$ISS" -ui-iap-keys-url "$ISSUER/jwks" \
		-ui-allowed-principals user:alice@example.com,user:owner@example.com "$@" \
		> "$OUT/demo.log" 2>&1 &
	DEMO_PID=$!
	for _ in $(seq 150); do
		curl -s -o /dev/null "$DEMO/healthz" && break
		sleep 0.2
	done
	CODE=$(cat "$OUT/access-code")
	DB=$(ls "$OUT"/tmp/grain-demo-*/grain.db | head -1)
	export CODE DB
}

stop_env() { kill "$DEMO_PID" "$IAPSIM_PID" 2>/dev/null; wait 2>/dev/null; }

# Callers, one per principal source.
iap() { curl -s -H "$(as alice@example.com)" -H 'Content-Type: application/json' "$@"; }  # (a) via the proxy: use $PROXY
code() { curl -s -H "Authorization: Bearer $CODE" -H 'Content-Type: application/json' "$@"; } # (b)
loop() { curl -s -H 'Content-Type: application/json' "$@"; }                                 # (c)
