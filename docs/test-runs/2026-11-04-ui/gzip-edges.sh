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

# Measures the task-list poll and walks the gzip edges (§10 items 1 and 2)
# against a running demo. Usage: GRAIN_SERVER=... GRAIN_TOKEN=... ./gzip-edges.sh
set -u
S=${GRAIN_SERVER:?}
A=(-H "Authorization: Bearer ${GRAIN_TOKEN:?}")
size() { # path, extra curl args...
  local p=$1; shift
  local plain gz
  plain=$(curl -s "${A[@]}" "$@" -o /dev/null -w '%{size_download}' "$S$p")
  gz=$(curl -s "${A[@]}" "$@" -H 'Accept-Encoding: gzip' -o /dev/null -w '%{size_download}' "$S$p")
  printf '%-48s plain=%9s gzip=%8s ratio=%s\n' "$p" "$plain" "$gz" "$(awk -v a="$plain" -v b="$gz" 'BEGIN{ if (b>0) printf "%.1fx", a/b; else print "-" }')"
}
echo "== sizes"
size "/api/tasks?fields=full"
size "/api/tasks"
size "/api/tasks?state=closed&limit=200&cursor="
echo "== ETag / 304 with gzip"
H=$(curl -s "${A[@]}" -H 'Accept-Encoding: gzip' -D - -o /dev/null "$S/api/tasks")
echo "$H" | grep -i -E '^(etag|content-encoding|vary):'
ETAG=$(echo "$H" | awk -F': ' 'tolower($1)=="etag"{print $2}' | tr -d '\r')
curl -s "${A[@]}" -H 'Accept-Encoding: gzip' -H "If-None-Match: $ETAG" -o /dev/null -w 'If-None-Match (gzip) -> %{http_code} %{size_download} bytes\n' "$S/api/tasks"
curl -s "${A[@]}" -H "If-None-Match: $ETAG" -o /dev/null -w 'If-None-Match (identity, gzip etag) -> %{http_code} %{size_download} bytes\n' "$S/api/tasks"
echo "== event stream"
timeout 3 curl -s -N "${A[@]}" -H "Accept-Encoding: gzip" -H "Accept: text/event-stream" -D - -o /dev/null "$S/api/events?stream=1" | grep -i -E '^HTTP/|^(content-type|content-encoding):' || true
echo "== websocket upgrade"
curl -s -m 3 "${A[@]}" -H 'Accept-Encoding: gzip' -H 'Connection: Upgrade' -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' -D - -o /dev/null "$S${WS_PATH:-/api/ws}" | grep -i -E '^HTTP/|^(content-encoding|upgrade):' || true
echo "== range request on a static asset"
ASSET=$(curl -s "${A[@]}" "$S/" | grep -o '/assets/[^"]*\.js' | head -1)
curl -s "${A[@]}" -H 'Accept-Encoding: gzip' -H 'Range: bytes=0-99' -D - -o /dev/null "$S$ASSET" | grep -i -E '^HTTP/|^(content-encoding|content-range|content-length):'
echo "== range request on an API JSON response"
curl -s "${A[@]}" -H 'Accept-Encoding: gzip' -H 'Range: bytes=0-99' -D - -o /dev/null "$S/api/tasks" | grep -i -E '^HTTP/|^(content-encoding|content-range|content-length):'
if [ -n "${ARTIFACT_PATH:-}" ]; then
  echo "== CI artifact download"
  curl -s "${A[@]}" -H 'Accept-Encoding: gzip' -D - -o /dev/null "$S$ARTIFACT_PATH" | grep -i -E '^HTTP/|^(content-type|content-encoding|content-disposition):'
fi
