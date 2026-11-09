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

# Shared helpers for the 2026-11-08 §1 walk scripts (sourced, not run).
G="${G:-/home/debian/bin/grain}"
WALK="${WALK:-/tmp/gr2}"
RA=http://127.0.0.1:4251
RB=http://127.0.0.1:4261
SA=ci-bot@walk-proj.iam.gserviceaccount.com
PSQL=(psql -h 127.0.0.1 -p 4240 -U postgres registry -tA)
unset GRAIN_SERVER GRAIN_TOKEN
b64u() { base64 -w0 | tr '+/' '-_' | tr -d '='; }
# fakejwt builds an unsigned three-part token whose email claim the IAP stub
# re-signs as a real IAP assertion.
fakejwt() { printf '%s.%s.sig' "$(printf '{"alg":"none"}' | b64u)" "$(printf '{"email":"%s","sub":"%s","aud":"walk","exp":%d}' "$1" "$1" $(($(date +%s) + 3600)) | b64u)"; }
as() { # as <email> <registry> <grain registry args...>
  local who=$1 reg=$2
  shift 2
  echo "\$ [$who @ ${reg##*:}] grain registry $*"
  GRAIN_REGISTRY_URL="$reg" GRAIN_REGISTRY_TOKEN="$(fakejwt "$who")" "$G" registry "$@" 2>&1 | sed 's/^/    /' | head -n "${HEADN:-14}"
  echo "    (exit ${PIPESTATUS[0]})"
}
api() { # api <email> <registry> <METHOD> <path> [json]
  local who=$1 reg=$2 m=$3 p=$4 body=${5:-}
  echo "\$ [$who @ ${reg##*:}] $m $p ${body}"
  if [ -n "$body" ]; then
    curl -sS -o /tmp/gr2-api.out -w '    HTTP %{http_code}\n' -X "$m" -H "X-Test-Email: $who" -H 'Content-Type: application/json' -d "$body" "$reg$p"
  else
    curl -sS -o /tmp/gr2-api.out -w '    HTTP %{http_code}\n' -X "$m" -H "X-Test-Email: $who" "$reg$p"
  fi
  head -c "${HEADC:-600}" /tmp/gr2-api.out | sed 's/^/    /'
  echo
}
# obo <registry> <entry> <on-behalf-of> [path] [method]: a delegated request
# from the ci-bot service account onto <entry>.grain-user.test; prints
# "<http status> <time_total>s" and the first bytes of the body.
obo() {
  local reg=$1 entry=$2 who=$3 path=${4:-/api/whoami} m=${5:-GET}
  curl -sS -o /tmp/gr2-obo.out -w '%{http_code} %{time_total}s' -X "$m" \
    -H "Host: $entry.grain-user.test" -H "X-Test-Email: ${OBO_SA:-$SA}" -H "X-Grain-On-Behalf-Of: $who" "$reg$path"
  printf ' %s\n' "$(head -c "${HEADC:-200}" /tmp/gr2-obo.out | tr '\n' ' ')"
}
