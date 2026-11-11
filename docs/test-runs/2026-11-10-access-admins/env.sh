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

# Environment for docs/test-plans/2026-11-10.md §2: a real `grain daemon`
# (SQLite, -sandbox-port-base=0) behind tests/iap/iapsim.go (local IAP JWKS
# issuer + reverse proxy + Cloud Identity stub) and ghsim.go (acme/widgets +
# acme/other over HTTP).
#
#   source env.sh
#   start_env [WORK] [GROUP_CHECKER_MODE]   # GROUP_CHECKER_MODE: on (default) | off
#   stop_env

HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT=$(cd "$HERE/../../.." && pwd)
GRAIN=${GRAIN:-/home/debian/bin/grain}
AUD="/projects/123456/global/backendServices/789012"
ISS="https://iap.test.local"
ISSUER="http://127.0.0.1:4292"
PROXY="http://127.0.0.1:4293"
DAEMON="http://127.0.0.1:4291"
GHSIM="127.0.0.1:4294"
IAP_HDR="X-Goog-IAP-JWT-Assertion"

start_env() {
	WORK=${1:-/var/tmp/access-admins}
	local group_mode=${2:-on}
	stop_env
	rm -rf "$WORK"
	mkdir -p "$WORK/data" "$WORK/home" "$WORK/bin"
	(cd "$ROOT" && go build -o "$WORK/bin/iapsim" tests/iap/iapsim.go && go build -o "$WORK/bin/ghsim" "$HERE/ghsim.go") || return 2

	# acme/widgets (with a repo-declared default review template) and acme/other.
	mkdir -p "$WORK/git/acme"
	git init -q --bare -b main "$WORK/git/acme/widgets.git"
	git init -q --bare -b main "$WORK/git/acme/other.git"
	local tmp
	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	mkdir -p "$tmp/.grain/templates"
	echo "# acme/widgets" >"$tmp/README.md"
	cat >"$tmp/.grain/templates/review.yml" <<'YAML'
name: Default widgets review
title: Review pull request on acme/widgets
base: main
capabilities:
  - 'https://grain.dev/capabilities/repo#read={"repo":"acme/other"}'
body: |
  Review the pull request on acme/widgets.
YAML
	git -C "$tmp" add README.md .grain/templates/review.yml
	git -C "$tmp" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed widgets and default review"
	git -C "$tmp" push -q "$WORK/git/acme/widgets.git" main
	rm -rf "$tmp"

	tmp=$(mktemp -d)
	git -C "$tmp" init -q -b main
	echo "# acme/other" >"$tmp/README.md"
	git -C "$tmp" add README.md
	git -C "$tmp" -c user.name=seed -c user.email=seed@example.com commit -qm "chore: seed other"
	git -C "$tmp" push -q "$WORK/git/acme/other.git" main
	rm -rf "$tmp"

	export HOME="$WORK/home" GCE_METADATA_HOST="127.0.0.1:9" GRAIN_SANDBOX_CLI=0
	unset GOOGLE_APPLICATION_CREDENTIALS GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_USER GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN GRAIN_ALLOWED_PRINCIPALS

	# bob@example.com and temp@example.com belong to group:ops@example.com.
	# alice@example.com and carol@example.com do not.
	"$WORK/bin/iapsim" -issuer "${ISSUER#http://}" -proxy "${PROXY#http://}" -upstream "$DAEMON" -aud "$AUD" -iss "$ISS" \
		-group "ops@example.com=bob@example.com,temp@example.com" \
		>"$WORK/iapsim.log" 2>&1 &
	echo $! >"$WORK/iapsim.pid"

	"$WORK/bin/ghsim" -addr "$GHSIM" -git-root "$WORK/git" -default main \
		>"$WORK/ghsim.log" 2>&1 &
	echo $! >"$WORK/ghsim.pid"

	for _ in $(seq 80); do curl -sf "$ISSUER/jwks" >/dev/null && break; sleep 0.1; done

	# Fake service-account ADC pointing at iapsim's /token when group checker is on.
	local ci_flags=()
	if [[ "$group_mode" == "on" ]]; then
		openssl genrsa 2048 2>/dev/null >"$WORK/sa.key"
		jq -n --rawfile k "$WORK/sa.key" --arg t "$ISSUER/token" \
			'{type:"service_account",project_id:"iapsim",private_key_id:"1",private_key:$k,client_email:"grain-daemon@iapsim.iam.gserviceaccount.com",client_id:"1",token_uri:$t}' \
			>"$WORK/adc.json"
		export GOOGLE_APPLICATION_CREDENTIALS="$WORK/adc.json"
		ci_flags=(-ui-cloud-identity-endpoint "$ISSUER")
	fi

	# GitHub credential so tasks may target acme/widgets and acme/other.
	echo "ghp_walkthrough_redacted" >"$WORK/bot.token"
	printf 'git:\n  - credential: bot\n    token_file: %s\n' "$WORK/bot.token" >"$WORK/seed.yaml"
	"$GRAIN" connections -data-dir "$WORK/data" import "$WORK/seed.yaml" >"$WORK/connections.log" 2>&1 || return 2

	# shellcheck disable=SC2086
	"$GRAIN" daemon -data-dir "$WORK/data" -ui-addr "${DAEMON#http://}" -sandbox-port-base=0 \
		-disable-native-sandboxes -ci=false -poll-interval 2s -as owner@example.com \
		-repo acme/widgets -repo acme/other -default-target-repo acme/widgets \
		-github-host "$GHSIM" -github-insecure-http \
		-ui-iap-audience "$AUD" -ui-iap-issuer "$ISS" -ui-iap-keys-url "$ISSUER/jwks" \
		"${ci_flags[@]}" \
		${DAEMON_EXTRA_FLAGS:-} >"$WORK/daemon.log" 2>&1 &
	echo $! >"$WORK/daemon.pid"

	for _ in $(seq 300); do
		[[ $(curl -s -o /dev/null -w '%{http_code}' "$DAEMON/" || true) != "000" ]] && break
		sleep 0.1
	done

	CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	DB="$WORK/data/store/grain.db"
	BODY="$WORK/body.json"
	export WORK CODE DB BODY DAEMON PROXY ISSUER AUD ISS GRAIN
}

stop_env() {
	[[ -z ${WORK:-} ]] && return 0
	local p pid
	for p in daemon iapsim ghsim; do
		[[ -f "$WORK/$p.pid" ]] || continue
		pid=$(cat "$WORK/$p.pid")
		kill "$pid" 2>/dev/null
		for _ in $(seq 50); do kill -0 "$pid" 2>/dev/null || break; sleep 0.1; done
	done
	return 0
}
