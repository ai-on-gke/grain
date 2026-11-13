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

# Environment for docs/test-plans/2026-11-11.md §5: a real `grain daemon`
# (-sandbox-port-base=0, SQLite by default, PostgreSQL with STORE=postgres)
# behind tests/iap/iapsim.go, the only stubbed parts: IAP's proxy and its
# JWKS sign-in verifier.
#
#   source env.sh
#   start_env [WORK]      # DAEMON_EXTRA_FLAGS / STORE=postgres honoured
#   stop_env

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
AUD="/projects/123456/global/backendServices/789012"
ISS="https://iap.test.local"
ISSUER="http://127.0.0.1:4302"
PROXY="http://127.0.0.1:4303"
DAEMON="http://127.0.0.1:4301"

start_env() {
	WORK=${1:-/var/tmp/caller-identity}
	stop_env
	rm -rf "$WORK"
	mkdir -p "$WORK/data" "$WORK/home" "$WORK/bin"
	chmod 755 "$WORK"
	(cd "$ROOT" && go build -o "$WORK/bin/iapsim" tests/iap/iapsim.go) || return 2

	export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0
	unset GOOGLE_APPLICATION_CREDENTIALS GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER \
		GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN GRAIN_ALLOWED_PRINCIPALS GRAIN_HOST_ACCOUNTS

	"$WORK/bin/iapsim" -issuer "${ISSUER#http://}" -proxy "${PROXY_BIND:-${PROXY#http://}}" -upstream "$DAEMON" -aud "$AUD" -iss "$ISS" \
		>"$WORK/iapsim.log" 2>&1 &
	echo $! >"$WORK/iapsim.pid"
	for _ in $(seq 80); do curl -sf "$ISSUER/jwks" >/dev/null && break; sleep 0.1; done

	# A (fake) GitHub credential so tasks may target acme/widgets.
	echo "ghp_walkthrough_redacted" >"$WORK/bot.token"
	printf 'git:\n  - credential: bot\n    token_file: %s\n' "$WORK/bot.token" >"$WORK/seed.yaml"
	"$GRAIN" connections -data-dir "$WORK/data" import "$WORK/seed.yaml" >"$WORK/connections.log" 2>&1 || return 2

	local store=()
	if [[ ${STORE:-sqlite} == postgres ]]; then
		store=(-postgres-url "${PG_DSN:-postgres://grain:grain@127.0.0.1:5432/grain_walk?sslmode=disable}")
	fi
	# shellcheck disable=SC2086
	"$GRAIN" daemon -data-dir "$WORK/data" -ui-addr "${DAEMON#http://}" -sandbox-port-base=0 \
		-disable-native-sandboxes -ci=false -poll-interval 2s -as owner@example.com \
		-repo acme/widgets -default-target-repo acme/widgets \
		-ui-iap-audience "$AUD" -ui-iap-issuer "$ISS" -ui-iap-keys-url "$ISSUER/jwks" \
		"${store[@]}" ${DAEMON_EXTRA_FLAGS:-} >"$WORK/daemon.log" 2>&1 &
	echo $! >"$WORK/daemon.pid"

	for _ in $(seq 300); do
		[[ $(curl -s -o /dev/null -w '%{http_code}' "$DAEMON/" || true) != "000" ]] && break
		sleep 0.1
	done
	CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	DB="$WORK/data/store/grain.db"
	export WORK CODE DB DAEMON PROXY ISSUER AUD ISS GRAIN
}

# code_as NAME METHOD PATH [BODY]: a request the access code authenticates
# as the named session NAME (the CLI's X-Grain-Access-Name), straight to
# the daemon over loopback.
code_as() {
	local name=$1 method=$2 path=$3 body=${4:-}
	curl -s -o "$WORK/last.json" -w '%{http_code}' -X "$method" "$DAEMON$path" \
		-H "Authorization: Bearer $CODE" -H "X-Grain-Access-Name: $name" \
		-H 'Content-Type: application/json' ${body:+--data "$body"}
}

stop_env() {
	[[ -z ${WORK:-} ]] && return 0
	local p pid
	for p in daemon iapsim; do
		[[ -f "$WORK/$p.pid" ]] || continue
		pid=$(cat "$WORK/$p.pid")
		kill "$pid" 2>/dev/null
		for _ in $(seq 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
	done
	return 0
}
