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

# item_3.sh: §3 item 3, the KMS round trip against kmsfakesrv.go. Run after
# items_1_2.sh (a HEAD data dir whose connection uses the App credential).
set -uo pipefail
source "$(dirname "$0")/env.sh"
step() { printf '\n== %s\n' "$*"; }
counts() { curl -s "$KMS_CTL/counts"; }
keyhead() { head -c 24 "$WORK/data/secrets/secrets.key"; echo; }
status() { api GET /api/state-repo | jq -c '. as $s | with_entries(select(.key|test("(?i)secretsKey|kms|unwrap")))'; }
mints() { curl -s "$GHSIM/_sim/mints"; }
clone_check() {
	local m0 t
	m0=$(mints)
	t=$(run_task "$1" '{"tools":[["run_command",{"description":"ls","command":"cd work && git log --oneline -1 | cut -c1-7"}]]}')
	echo "run $t: $(jq -r '.calls[0].text' "$WORK/walk/runs/$t.json" | grep -c exit=0) ok call(s); App token mints $m0 -> $(mints)"
}

step "3a. wrap with the daemon up"
kill -0 "$(cat "$WORK/daemon.pid" 2>/dev/null)" 2>/dev/null || start_daemon "$HEAD_BIN"
setup_agent
pid=$(cat "$WORK/daemon.pid")
echo "kms before: $(counts)"
GRAIN_SERVER=$API GRAIN_TOKEN=$(cat "$WORK/data/local-api-token") "$HEAD_BIN" state -data-dir "$WORK/data" key wrap -kms "$KMS_KEY" 2>&1 | sed 's/grain-secret-pub-v1:[^)]*/<public key>/'
echo "kms after: $(counts)"; keyhead
grep 'wrapped the secrets key' "$WORK/daemon.log" | tail -1 | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-120
[[ $(cat "$WORK/daemon.pid") == "$pid" ]] && kill -0 "$pid" && echo "same daemon process, no restart"
status
clone_check "run after live wrap"

step "3b. restart: one unwrap per process"
c0=$(counts); stop_daemon; start_daemon "$HEAD_BIN"; sleep 3
clone_check "run after restart"
"$HEAD_BIN" secrets -data-dir "$WORK/data" list >/dev/null
echo "kms $c0 -> $(counts) (the daemon, then one 'grain secrets list' process)"
status

for mode in deny down; do
	step "3c. KMS refuses at start ($mode)"
	stop_daemon
	curl -s "$KMS_CTL/$mode?on=1" >/dev/null
	: >"$WORK/daemon-$mode.log"
	start_daemon "$HEAD_BIN" && echo "daemon answers its API with KMS $mode"
	sleep 5
	kill -0 "$(cat "$WORK/daemon.pid")" 2>/dev/null && echo "daemon still running after 5s"
	status
	grep -iE 'kms|unwrap|secrets key' "$WORK/daemon.log" | tail -4 | sed 's/^[0-9/]* [0-9:]* //' | cut -c1-260
	curl -s "$KMS_CTL/$mode?on=0" >/dev/null
	echo "KMS back; waiting for the backoff"
	for i in $(seq 90); do
		[[ $(status | jq -r '.secretsKeyLastUnwrap // empty') > "$(date -u -d '-60 sec' +%FT%TZ)" ]] && { echo "recovered after ~${i}s"; break; }
		sleep 1
	done
	status
	clone_check "run after KMS $mode recovered"
done

step "3d. unwrap with the daemon up"
GRAIN_SERVER=$API GRAIN_TOKEN=$(cat "$WORK/data/local-api-token") "$HEAD_BIN" state -data-dir "$WORK/data" key unwrap 2>&1 | sed 's/grain-secret-pub-v1:[^)]*/<public key>/'
keyhead | cut -c1-20
status

step "3e. wrap and unwrap with the daemon down (in-process)"
stop_daemon
echo "kms before: $(counts)"
"$HEAD_BIN" state -data-dir "$WORK/data" key wrap -kms "$KMS_KEY" 2>&1 | sed 's/grain-secret-pub-v1:[^)]*/<public key>/'
keyhead
"$WORK/bin/storedigest" "$WORK/data" github-credential-bot/token | cut -c1-41
"$HEAD_BIN" state -data-dir "$WORK/data" key unwrap 2>&1 | sed 's/grain-secret-pub-v1:[^)]*/<public key>/'
keyhead | cut -c1-20
echo "kms after: $(counts)"

step "3f. wrap lists the plaintext copies it can see"
"$HEAD_BIN" state -data-dir "$WORK/data" key show >"$WORK/pub-before"
# key import of the live key itself leaves a replaced-* archive behind.
"$HEAD_BIN" state -data-dir "$WORK/data" key import -key-file "$WORK/data/secrets/secrets.key" 2>&1 | sed 's/grain-secret-pub-v1:[^ ]*/<public key>/g'
ls "$WORK/data/secrets" | grep secrets.key
"$HEAD_BIN" state -data-dir "$WORK/data" key wrap -kms "$KMS_KEY" 2>&1 | sed 's/grain-secret-pub-v1:[^)]*/<public key>/'
"$HEAD_BIN" state -data-dir "$WORK/data" key unwrap >/dev/null 2>&1 && echo "unwrapped again"
start_daemon "$HEAD_BIN"
