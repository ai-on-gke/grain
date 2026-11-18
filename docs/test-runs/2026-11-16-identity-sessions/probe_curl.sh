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

# Executes Item 2 (SessionGrace matrix) and Item 3 (bundle-exemption curl probe
# table + hub tunnel check) against the live grain-slowverifier daemon.

set -euo pipefail

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
WORK=${WORK_DIR:-/tmp/walk-id-sessions}
DAEMON_HTTP=${DAEMON_HTTP:-http://127.0.0.1:4291}
PROXY_HTTPS=${PROXY_HTTPS:-https://127.0.0.1:4292}
PROXY_ALT_HTTPS=${PROXY_ALT_HTTPS:-https://127.0.0.1:4295}
CONTROL_HTTP=${CONTROL_HTTP:-http://127.0.0.1:4293}
TUNNEL_HTTP=${TUNNEL_HTTP:-http://127.0.0.1:4296}
TUNNEL_CTL=${TUNNEL_CTL:-http://127.0.0.1:4297}

MAIN_JS=$(basename "$(ls "$ROOT"/pkg/ui/static-next/main-*.js | head -n1)")
STYLES_CSS=$(basename "$(ls "$ROOT"/pkg/ui/static-next/styles-*.css | head -n1)")
CHUNK_JS=$(basename "$(ls "$ROOT"/pkg/ui/static-next/chunk-*.js | head -n1)")
LEGACY_CSS=$(basename "$(ls "$ROOT"/pkg/ui/static/assets/index-*.css | head -n1)")

echo "=== ITEM 2: SessionGrace in practice ==="
# Set TTL=2000ms, delay=0, mode=ok, proxyEmail=alice@example.com
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/reset" >/dev/null
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/config" \
	-d '{"mode":"ok","delayMs":0,"ttlMs":2000,"proxyMode":"sign","proxyEmail":"alice@example.com","stripRetryAfter":false}' >/dev/null

rm -f "$WORK/grace.cookies"
S1=$(curl -sk --max-time 5 -D "$WORK/g1.hdr" -c "$WORK/grace.cookies" -o "$WORK/g1.body" -w '%{http_code}' "$PROXY_HTTPS/api/auth/user")
V1=$(curl -sf --max-time 5 "$CONTROL_HTTP/stats" | jq -c '{fullVerifies,localVerifies,graceAdmits}')
SC1=$(grep -ic '^set-cookie: __Host-grain_verified=' "$WORK/g1.hdr" || true)
echo "2.1 Issue session (TTL=2s, mode=ok): status=$S1 setCookie=$SC1 stats=$V1"

curl -sf --max-time 5 -X POST "$CONTROL_HTTP/reset" >/dev/null
S2=$(curl -sk --max-time 5 -D "$WORK/g2.hdr" -b "$WORK/grace.cookies" -o /dev/null -w '%{http_code}' "$PROXY_HTTPS/api/tasks")
V2=$(curl -sf --max-time 5 "$CONTROL_HTTP/stats" | jq -c '{fullVerifies,localVerifies,graceAdmits}')
SC2=$(grep -ic '^set-cookie: __Host-grain_verified=' "$WORK/g2.hdr" || true)
echo "2.2 Live session fast-path (<2s): status=$S2 setCookie=$SC2 stats=$V2"

sleep 2.5
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/reset" >/dev/null
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/config" -d '{"mode":"down"}' >/dev/null

# Repeated grace requests (<5m past expiry): must admit Alice and NEVER re-issue Set-Cookie
for n in 1 2 3; do
	Sn=$(curl -sk --max-time 5 -D "$WORK/g3_${n}.hdr" -b "$WORK/grace.cookies" -o "$WORK/g3_${n}.body" -w '%{http_code}' "$PROXY_HTTPS/api/auth/user")
	SCn=$(grep -ic '^set-cookie:' "$WORK/g3_${n}.hdr" || true)
	Vn=$(curl -sf --max-time 5 "$CONTROL_HTTP/stats" | jq -c '{fullVerifies,localVerifies,graceAdmits,unavailableErrors}')
	echo "2.3.${n} Expired (<5m) + backend down (same principal alice, repeat #${n}): status=$Sn setCookie=$SCn stats=$Vn"
done

# Different header principal (bob@example.com) carrying Alice's cookie while backend down -> refused 503
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/reset" >/dev/null
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/config" -d '{"mode":"down","proxyEmail":"bob@example.com"}' >/dev/null
S_BOB=$(curl -sk --max-time 5 -D "$WORK/gbob.hdr" -b "$WORK/grace.cookies" -o "$WORK/gbob.body" -w '%{http_code}' "$PROXY_HTTPS/api/auth/user")
V_BOB=$(curl -sf --max-time 5 "$CONTROL_HTTP/stats" | jq -c '{fullVerifies,localVerifies,graceAdmits,unavailableErrors}')
echo "2.4 Expired (<5m) + backend down + different header principal (bob): status=$S_BOB stats=$V_BOB body=$(cat "$WORK/gbob.body")"

# Different Host (127.0.0.1:4295) carrying 127.0.0.1:4292's cookie while backend down -> refused 503
RAW_GRACE=$(awk '/__Host-grain_verified/ {print $7}' "$WORK/grace.cookies")
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/reset" >/dev/null
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/config" -d '{"mode":"down","proxyEmail":"alice@example.com"}' >/dev/null
S_HOST=$(curl -sk --max-time 5 -H "Cookie: __Host-grain_verified=$RAW_GRACE" -o "$WORK/ghost.body" -w '%{http_code}' "$PROXY_ALT_HTTPS/api/auth/user")
V_HOST=$(curl -sf --max-time 5 "$CONTROL_HTTP/stats" | jq -c '{fullVerifies,localVerifies,graceAdmits,unavailableErrors}')
echo "2.5 Expired (<5m) + backend down + other Host (4295): status=$S_HOST stats=$V_HOST"

# Backend recovers -> full verify runs and re-issues fresh cookie
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/reset" >/dev/null
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/config" -d '{"mode":"ok","ttlMs":60000,"proxyEmail":"alice@example.com"}' >/dev/null
S_REC=$(curl -sk --max-time 5 -D "$WORK/grec.hdr" -b "$WORK/grace.cookies" -o /dev/null -w '%{http_code}' "$PROXY_HTTPS/api/auth/user")
SC_REC=$(grep -ic '^set-cookie: __Host-grain_verified=' "$WORK/grec.hdr" || true)
V_REC=$(curl -sf --max-time 5 "$CONTROL_HTTP/stats" | jq -c '{fullVerifies,localVerifies,graceAdmits}')
echo "2.6 Backend recovered (mode=ok): status=$S_REC setCookie=$SC_REC stats=$V_REC"

echo ""
echo "=== ITEM 3: Bundle exemption probe table (verifier mode=refuse) ==="
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/config" -d '{"mode":"refuse","delayMs":0}' >/dev/null

probe_one() {
	local label=$1 method=$2 path=$3 extra_hdr=${4:-}
	curl -sf --max-time 5 -X POST "$CONTROL_HTTP/reset" >/dev/null
	local args=(--path-as-is -s --max-time 5 -D "$WORK/p.hdr" -o /dev/null -w '%{http_code} %{size_download}' -H "X-Forwarded-For: 198.51.100.99" -H "X-Slow-Proxy-Assertion: v1|alice@example.com|127.0.0.1:4291|0|bad")
	if [[ -n "$extra_hdr" ]]; then
		args+=(-H "$extra_hdr")
	fi
	if [[ "$method" == "HEAD" ]]; then
		args+=(--head)
	else
		args+=(-X "$method")
	fi
	local res code cl
	res=$(curl "${args[@]}" "${DAEMON_HTTP}${path}")
	code=${res%% *}
	cl=${res##* }
	local cc etag ce ver
	cc=$(awk -F': ' 'tolower($1)=="cache-control"{gsub("\r","",$2); print $2}' "$WORK/p.hdr" | tail -n1)
	etag=$(awk -F': ' 'tolower($1)=="etag"{gsub("\r","",$2); print $2}' "$WORK/p.hdr" | tail -n1)
	ce=$(awk -F': ' 'tolower($1)=="content-encoding"{gsub("\r","",$2); print $2}' "$WORK/p.hdr" | tail -n1)
	ver=$(curl -sf --max-time 5 "$CONTROL_HTTP/stats" | jq -r '.fullVerifies')
	printf "%-44s | %-6s | %-6s | %-36s | %-20s | %-6s | %-8s | %s\n" \
		"$label" "$method" "$code" "${cc:--}" "${etag:--}" "${ce:--}" "$cl" "$ver"
}

printf "%-44s | %-6s | %-6s | %-36s | %-20s | %-6s | %-8s | %s\n" \
	"Probe" "Method" "Status" "Cache-Control" "ETag" "Enc" "BodyB" "Verify"
printf -- "%.0s-" {1..150}; echo ""

probe_one "/next/$MAIN_JS (gzip)" "GET" "/next/$MAIN_JS" "Accept-Encoding: gzip"
# Capture weak & strong ETags for conditional probes
GZIP_ETAG=$(awk -F': ' 'tolower($1)=="etag"{gsub("\r","",$2); print $2}' "$WORK/p.hdr" | tail -n1)
probe_one "/next/$MAIN_JS (identity)" "GET" "/next/$MAIN_JS" ""
STRONG_ETAG=$(awk -F': ' 'tolower($1)=="etag"{gsub("\r","",$2); print $2}' "$WORK/p.hdr" | tail -n1)
probe_one "/next/$MAIN_JS (HEAD)" "HEAD" "/next/$MAIN_JS" ""
probe_one "/next/$MAIN_JS (If-None-Match strong)" "GET" "/next/$MAIN_JS" "If-None-Match: $STRONG_ETAG"
probe_one "/next/$MAIN_JS (Range: bytes=0-15)" "GET" "/next/$MAIN_JS" "Range: bytes=0-15"
probe_one "/next/$STYLES_CSS" "GET" "/next/$STYLES_CSS" "Accept-Encoding: gzip"
probe_one "/assets/$LEGACY_CSS" "GET" "/assets/$LEGACY_CSS" "Accept-Encoding: gzip"
probe_one "POST /next/$MAIN_JS" "POST" "/next/$MAIN_JS" ""
probe_one "/next/${MAIN_JS}?v=1" "GET" "/next/${MAIN_JS}?v=1" ""
probe_one "/next/${MAIN_JS}?" "GET" "/next/${MAIN_JS}?" ""
probe_one "/next//$CHUNK_JS" "GET" "/next//$CHUNK_JS" ""
probe_one "/next/%2e/$CHUNK_JS" "GET" "/next/%2e/$CHUNK_JS" ""
PCT_MAIN="/next/%6d${MAIN_JS#m}"
probe_one "$PCT_MAIN (pct-encoded)" "GET" "$PCT_MAIN" ""
probe_one "/next/./$CHUNK_JS" "GET" "/next/./$CHUNK_JS" ""
probe_one "/next/$MAIN_JS/" "GET" "/next/$MAIN_JS/" ""
probe_one "/next/${MAIN_JS}.map" "GET" "/next/${MAIN_JS}.map" ""
probe_one "/next/chunk-ZZZZZZZZ.js (missing)" "GET" "/next/chunk-ZZZZZZZZ.js" ""
probe_one "/next/index.html (GET)" "GET" "/next/index.html" ""
probe_one "/next/index.html (HEAD)" "HEAD" "/next/index.html" ""
probe_one "/next/ (SPA root)" "GET" "/next/" ""
probe_one "/next/inbox (SPA route)" "GET" "/next/inbox" ""
probe_one "/next/grain-favicon-light.svg (unhashed)" "GET" "/next/grain-favicon-light.svg" ""
probe_one "/ (classic root)" "GET" "/" ""
probe_one "/api/tasks" "GET" "/api/tasks" ""

# Hub tunnel probe
(cd "$ROOT" && go build -o "$WORK/bin/probe_hub_tunnel" "$HERE/probe_hub_tunnel.go")
"$WORK/bin/probe_hub_tunnel" -addr "127.0.0.1:4296" -ctl "127.0.0.1:4297" >"$WORK/tunnel.log" 2>&1 &
TPID=$!
trap 'kill $TPID 2>/dev/null || true' EXIT
for _ in $(seq 30); do
	curl -sf --max-time 2 "$TUNNEL_CTL/stats" >/dev/null 2>&1 && break
	sleep 0.1
done

curl -sf --max-time 5 -X POST "$TUNNEL_CTL/reset" >/dev/null
TCODE1=$(curl --path-as-is -s --max-time 5 -D "$WORK/t1.hdr" -o /dev/null -w '%{http_code}' -H "X-Grain-Registry-Assertion: forged" "$TUNNEL_HTTP/next/$MAIN_JS")
TCC1=$(awk -F': ' 'tolower($1)=="cache-control"{gsub("\r","",$2); print $2}' "$WORK/t1.hdr" | tail -n1)
TV1=$(curl -sf --max-time 5 "$TUNNEL_CTL/stats" | jq -r '.verifies')
printf "%-44s | %-6s | %-6s | %-36s | %-20s | %-6s | %-8s | %s\n" \
	"HubTunnel /next/$MAIN_JS (refused)" "GET" "$TCODE1" "${TCC1:--}" "-" "-" "0" "$TV1"

curl -sf --max-time 5 -X POST "$TUNNEL_CTL/reset?admit=1" >/dev/null
TCODE2=$(curl --path-as-is -s --max-time 5 -D "$WORK/t2.hdr" -o "$WORK/t2.body" -w '%{http_code}' -H "X-Grain-Registry-Assertion: admit-alice" "$TUNNEL_HTTP/next/$MAIN_JS")
TCC2=$(awk -F': ' 'tolower($1)=="cache-control"{gsub("\r","",$2); print $2}' "$WORK/t2.hdr" | tail -n1)
TETAG2=$(awk -F': ' 'tolower($1)=="etag"{gsub("\r","",$2); print $2}' "$WORK/t2.hdr" | tail -n1)
TBLEN2=$(wc -c <"$WORK/t2.body" | tr -d ' ')
TV2=$(curl -sf --max-time 5 "$TUNNEL_CTL/stats" | jq -r '.verifies')
printf "%-44s | %-6s | %-6s | %-36s | %-20s | %-6s | %-8s | %s\n" \
	"HubTunnel /next/$MAIN_JS (admitted)" "GET" "$TCODE2" "${TCC2:--}" "${TETAG2:--}" "-" "$TBLEN2" "$TV2"

kill "$TPID" 2>/dev/null || true

# Also verify authenticated missing asset & .map return 404 no-store (not SPA fallback!)
echo ""
echo "=== Authenticated missing-asset & ETag checks ==="
curl -sf --max-time 5 -X POST "$CONTROL_HTTP/config" -d '{"mode":"ok","delayMs":0,"ttlMs":60000,"proxyEmail":"alice@example.com"}' >/dev/null
for p in "/next/chunk-ZZZZZZZZ.js" "/next/${MAIN_JS}.map" "/assets/missing-12345678.js"; do
	c=$(curl -sk --max-time 5 -D "$WORK/m.hdr" -o "$WORK/m.body" -w '%{http_code}' "$PROXY_HTTPS$p")
	mcc=$(awk -F': ' 'tolower($1)=="cache-control"{gsub("\r","",$2); print $2}' "$WORK/m.hdr" | tail -n1)
	mct=$(awk -F': ' 'tolower($1)=="content-type"{gsub("\r","",$2); print $2}' "$WORK/m.hdr" | tail -n1)
	echo "Authenticated GET $p -> status=$c Cache-Control=$mcc Content-Type=$mct"
done
