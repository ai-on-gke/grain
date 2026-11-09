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

# docs/test-plans/2026-11-09.md §7 item 2: every invalid `grain login`
# combination exits 2 with a specific code and leaves ~/.config/grain
# byte-identical; the documented `grain -token CODE login <url>
# --issue-token` mints a gma_ token a following `grain list` uses.
# Needs util-linux `script` for the TTY-stdin case. REUSE=1 reuses a
# running env.
#
#   item2_login_refusals.sh [OUT]
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=env.sh
source "$HERE/env.sh"
OUT=${1:-$HERE/item2-login-refusals.out}
if [[ ${REUSE:-} == 1 ]]; then
	WORK=${WORK:-/var/tmp/ext-rails}
	CODE=$(tr -d '[:space:]' <"$WORK/data/local-api-token")
	DB="$WORK/data/store/grain.db" BODY="$WORK/body.json"
	export HOME="$WORK/home" GRAIN_SANDBOX_CLI=0
	unset GRAIN_SERVER GRAIN_TOKEN GRAIN_AUTH_TOKEN GRAIN_ID_TOKEN GRAIN_CONTEXT GRAIN_SANDBOX_PROXY GRAIN_SANDBOX_TOKEN
else
	start_env "${EXT_WORK:-}" || exit 2
	trap stop_env EXIT
fi
: >"$OUT"
FAILS=0
check() { # id desc expected-regex got
	local res=PASS
	[[ $4 =~ $3 ]] || { res=FAIL; FAILS=$((FAILS + 1)); }
	printf '%s\t%s\t%s\t%s\n' "$res" "$1" "$2" "${4:0:300}" | sed -E 's/gma_[A-Za-z0-9_]+/gma_REDACTED/g' | tee -a "$OUT"
}
redact() { sed -E "s/gma_[A-Za-z0-9_]+/gma_REDACTED/g; s/$CODE/CODE_REDACTED/g"; }
code PUT /api/settings '{"externalAgentApi":true}' >/dev/null
cfgsum() { (cd "$HOME" && find .config -type f 2>/dev/null | sort | xargs -r sha256sum) | sha256sum | cut -c1-16; }

# A context that exists already, so "untouched" means more than "absent".
"$GRAIN" -token "$CODE" login "$DAEMON" --name seeded --issue-token >"$WORK/seed.out" 2>&1
check 2.0 "seed context minted with the documented form" '^0$' "$?"
SEEDED=$(cfgsum)

run() { # id desc want-code stdin args... -> checks exit 2, code and config
	local id=$1 desc=$2 want=$3 in=$4
	shift 4
	local before after rc
	before=$(cfgsum)
	if [[ $in == tty ]]; then
		# script(1) gives grain a TTY on stdin; nothing is typed.
		timeout 10 script -qec "$(printf '%q ' "$GRAIN" -json "$@")" /dev/null </dev/null >"$WORK/login.out" 2>&1
	else
		printf '%s\n' "$in" | "$GRAIN" -json "$@" >"$WORK/login.out" 2>&1
	fi
	rc=$?
	after=$(cfgsum)
	local shown=piped
	[[ $in == tty || -z $in ]] && shown=${in:-empty}
	echo "## $id: grain $* (stdin: $shown)" | redact >>"$OUT"
	redact <"$WORK/login.out" | head -c 600 >>"$OUT"
	echo >>"$OUT"
	check "$id.exit" "$desc: exit" '^2$' "$rc"
	check "$id.code" "$desc: code" "$want" "$(tr '\n' ' ' <"$WORK/login.out" | redact)"
	check "$id.cfg" "$desc: config untouched" "^${before}$" "$after"
}
run 2.1 "--issue-token --token-stdin (code piped)" 'token_stdin_issue_token' "$CODE" \
	login "$DAEMON" --auth access-code --issue-token --token-stdin --name c1
run 2.2 "--issue-token --token-stdin (gma_ piped)" 'token_stdin_issue_token' "gma_notreal" \
	login "$DAEMON" --issue-token --token-stdin --name c2
run 2.3 "--token-stdin with a TTY stdin" '"code"' tty \
	login "$DAEMON" --token-stdin --name c3
run 2.4 "--issue-token --auth iap" '"code"' "" \
	login "$DAEMON" --issue-token --auth iap --name c4
run 2.5 "--token-stdin with a non-gma_ secret and no --auth access-code" '"code"' "not-a-token" \
	login "$DAEMON" --token-stdin --name c5
run 2.6 "--token-stdin with an empty stdin" '"code"' "" \
	login "$DAEMON" --token-stdin --name c6
run 2.7 "--refresh --token-stdin" 'token_stdin_refresh' "gma_notreal" \
	login --refresh --token-stdin --name seeded
run 2.8 "--scope-set without --issue-token" 'needs_issue_token' "" \
	login "$DAEMON" --scope-set grain/viewer --name c8
run 2.9 "--expires garbage" '"code"' "" \
	login "$DAEMON" --issue-token --expires soon --name c9
run 2.10 "a URL with no scheme" '"code"' "" \
	login 127.0.0.1:4291 --name c10
run 2.8b "--repos and --expires without --issue-token" 'needs_issue_token' "" \
	-token "$CODE" login "$DAEMON" --repos acme/widgets --expires 7d --name c8b
check 2.11 "config still the seeded one after all refusals" "^${SEEDED}$" "$(cfgsum)"

# The help text names the working form.
"$GRAIN" login --help >"$WORK/help.out" 2>&1
check 2.12 "login --help: --token-stdin says not with --issue-token" 'not with --issue-token' "$(tr '\n' ' ' <"$WORK/help.out")"

# The documented form, then a following listing with that context only.
"$GRAIN" -token "$CODE" login "$DAEMON" --name doc --issue-token >"$WORK/doc.out" 2>&1
check 2.13 "grain -token CODE login <url> --issue-token: exit 0" '^0 .*signed in to .* as context doc \(external-token\)' "$? $(tr '\n' ' ' <"$WORK/doc.out")"
check 2.14 "the stored credential is a gma_ token" '^gma_' "$(cat "$HOME"/.config/grain/credentials/doc* 2>/dev/null || jq -r '.contexts.doc.credential' "$HOME/.config/grain/config.json" 2>/dev/null)"
"$GRAIN" --context doc list >"$WORK/list.out" 2>&1
check 2.15 "grain --context doc list with the minted token: exit 0" '^0$' "$?"
check 2.16 "the daemon saw the gma_ credential" 'external:' "$(grep -c 'login-doc\|external:' "$WORK/daemon.log" | sed 's/^[1-9].*/external:/')"
echo "FAILS=$FAILS" | tee -a "$OUT"
exit "$FAILS"
