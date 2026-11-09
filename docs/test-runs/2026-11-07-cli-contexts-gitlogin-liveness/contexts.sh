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

# §8 item 1: named contexts, login/logout/whoami, precedence, keyring
# fallback and credential hygiene, against two live `grain demo` servers:
#   A  http://127.0.0.1:4291  access code only            (code in $A_CODE_FILE)
#   B  http://127.0.0.1:4292  access code + externalAgentApi (code in $B_CODE_FILE)
# Every command runs in a throwaway HOME / XDG_CONFIG_HOME.
set -u
GRAIN=${GRAIN:-/home/debian/bin/grain}
A=http://127.0.0.1:4291
B=http://127.0.0.1:4292
A_CODE_FILE=${A_CODE_FILE:-/tmp/w/srv-a/code}
B_CODE_FILE=${B_CODE_FILE:-/tmp/w/srv-b/code}
W=$(mktemp -d /tmp/grain-ctx.XXXXXX)
export HOME=$W XDG_CONFIG_HOME=$W/.config GRAIN_SANDBOX_CLI=0
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_CONTEXT DBUS_SESSION_BUS_ADDRESS
CFG=$XDG_CONFIG_HOME/grain

run() { # run <label> <cmd...>: print the command, its output and exit status
  local label=$1; shift
  echo "### $label"
  echo "\$ $*"
  "$@" 2>&1 | sed 's/^/    /'
  echo "    -> exit=${PIPESTATUS[0]}"
}
leak() { # leak <label> <secret> <path...>: fail if the secret is in a file
  local label=$1 secret=$2; shift 2
  if grep -rqF -- "$secret" "$@" 2>/dev/null; then echo "LEAK($label): secret found in $*"; else echo "ok($label): secret absent from $*"; fi
}

echo "== whoami, unauthenticated"
run "whoami A no credential" $GRAIN --server $A whoami
run "whoami A no credential --json" $GRAIN --server $A -json whoami

echo "== login A with the access code (stdin)"
run "login A --token-stdin without --auth" sh -c "$GRAIN login $A --name a --token-stdin < $A_CODE_FILE"
run "login A --token-stdin --auth access-code" sh -c "$GRAIN login $A --name a --token-stdin --auth access-code < $A_CODE_FILE"
ls -l $CFG/config.json $CFG/credentials/a | awk '{print "    " $1, $NF}'
leak config.json "$(cat $A_CODE_FILE)" $CFG/config.json
run "whoami (current=a)" $GRAIN whoami

echo "== login B: access code in GRAIN_TOKEN, mint a gma_ token"
run "login B refuses to keep the access code" env GRAIN_TOKEN="$(cat $B_CODE_FILE)" $GRAIN login $B --name b
run "login B --issue-token" env GRAIN_TOKEN="$(cat $B_CODE_FILE)" $GRAIN login $B --name b --issue-token --expires 1d
ls -l $CFG/credentials/b | awk '{print "    " $1, $NF}'
BTOK=$(cat $CFG/credentials/b)
case $BTOK in gma_*) echo "    stored credential for b is a gma_ token";; *) echo "    UNEXPECTED credential for b";; esac
leak config.json "$BTOK" $CFG/config.json
run "whoami (current=b, gma_)" $GRAIN whoami
run "context list" $GRAIN context list
run "context show" $GRAIN context show

echo "== precedence: --server > GRAIN_SERVER > --context/GRAIN_CONTEXT > current"
run "context use a" $GRAIN context use a
run "whoami --context b (flag beats current)" $GRAIN --context b whoami
run "GRAIN_CONTEXT=b whoami" env GRAIN_CONTEXT=b $GRAIN whoami
run "GRAIN_CONTEXT=b --context a whoami (flag beats env)" env GRAIN_CONTEXT=b $GRAIN --context a whoami
run "GRAIN_SERVER=B with current=a: a's access code must not travel to B" env GRAIN_SERVER=$B $GRAIN whoami
run "--server A with --context b: b's gma_ must not travel to A" $GRAIN --context b --server $A whoami
run "GRAIN_TOKEN overrides the context credential (garbage token)" env GRAIN_TOKEN=gma_not_a_real_token $GRAIN --context b whoami
run "GRAIN_TOKEN=<A code> with --server A, no context" env GRAIN_TOKEN="$(cat $A_CODE_FILE)" $GRAIN --server $A whoami
run "--context nope" $GRAIN --context nope whoami

echo "== insecure credential file is refused"
chmod 0644 $CFG/credentials/a
run "whoami --context a with 0644 credential" $GRAIN --context a whoami
chmod 0600 $CFG/credentials/a

echo "== argv: a long-running command's /proc/<pid>/cmdline holds no secret"
timeout 4 $GRAIN --context b logs 3 -follow >/dev/null 2>&1 &
sleep 1.5
for p in $(pgrep -f "^$GRAIN --context b logs"); do tr '\0' ' ' </proc/$p/cmdline; echo; done | sed 's/^/    argv: /'
if ps auxww | grep -F -- "$BTOK" | grep -v grep >/dev/null; then echo "LEAK(ps): gma_ token in ps"; else echo "ok(ps): gma_ token absent from ps auxww"; fi
wait

echo "== keyring: DBUS set, secret-tool absent -> 0600 file"
export DBUS_SESSION_BUS_ADDRESS=unix:path=/nonexistent
run "login A (no secret-tool on PATH)" sh -c "PATH=/usr/bin:/bin $GRAIN login $A --name a2 --token-stdin --auth access-code < $A_CODE_FILE"
grep -o '"a2":{[^}]*}' $CFG/config.json | sed 's/^/    /'

echo "== keyring: a fake secret-tool that records its argv (stdin is the secret)"
FK=$W/fakebin; mkdir -p $FK $W/fakekeyring
cat >$FK/secret-tool <<'EOF'
#!/bin/sh
# Fake freedesktop secret-tool: argv goes to argv.log, secrets to files.
echo "$*" >>"$FAKE_KEYRING/argv.log"
[ -n "${FAKE_KEYRING_LOCKED:-}" ] && { echo "secret-tool: Cannot create an item in a locked collection" >&2; exit 1; }
acct=; prev=; for a in "$@"; do [ "$prev" = account ] && acct=$a; prev=$a; done
case $1 in
  store)  cat >"$FAKE_KEYRING/$acct" ;;
  lookup) cat "$FAKE_KEYRING/$acct" 2>/dev/null || exit 1 ;;
  clear)  rm -f "$FAKE_KEYRING/$acct" ;;
esac
EOF
chmod +x $FK/secret-tool
export FAKE_KEYRING=$W/fakekeyring
run "login A into the keyring" sh -c "PATH=$FK:\$PATH $GRAIN login $A --name k --token-stdin --auth access-code < $A_CODE_FILE"
grep -o '"k":{[^}]*}' $CFG/config.json | sed 's/^/    /'
sed 's/^/    secret-tool argv: /' $FAKE_KEYRING/argv.log
leak secret-tool-argv "$(cat $A_CODE_FILE)" $FAKE_KEYRING/argv.log
ls $CFG/credentials/ | sed 's/^/    credentials dir: /'
run "whoami --context k (keyring reachable)" env PATH=$FK:$PATH $GRAIN --context k whoami
run "whoami --context k (keyring unreachable: no DBUS)" env -u DBUS_SESSION_BUS_ADDRESS $GRAIN --context k whoami

echo "== keyring locked -> falls back to the 0600 file"
run "login A with a locked keyring" sh -c "PATH=$FK:\$PATH FAKE_KEYRING_LOCKED=1 $GRAIN login $A --name locked --token-stdin --auth access-code < $A_CODE_FILE"
grep -o '"locked":{[^}]*}' $CFG/config.json | sed 's/^/    /'
ls -l $CFG/credentials/locked 2>&1 | awk '{print "    " $1, $NF}'

echo "== file -> keyring re-login leaves no stale file"
run "login A as file context fk" sh -c "PATH=/usr/bin:/bin $GRAIN login $A --name fk --token-stdin --auth access-code < $A_CODE_FILE"
run "re-login fk with the keyring reachable" sh -c "PATH=$FK:\$PATH $GRAIN login $A --name fk --token-stdin --auth access-code < $A_CODE_FILE"
grep -o '"fk":{[^}]*}' $CFG/config.json | sed 's/^/    /'
if [ -e $CFG/credentials/fk ]; then echo "STALE: $CFG/credentials/fk still holds the old secret"; else echo "ok: no stale credential file for fk"; fi

echo "== logout scrubs"
run "logout k while the keyring is unreachable" env -u DBUS_SESSION_BUS_ADDRESS $GRAIN logout k
if [ -e $FAKE_KEYRING/k ]; then echo "keyring still holds k's secret (expected while logout was refused)"; else echo "ok: keyring entry for k removed"; fi
run "logout k with the keyring reachable" env PATH=$FK:$PATH $GRAIN logout k
if [ -e $FAKE_KEYRING/k ]; then echo "STALE: keyring still holds k's secret after logout"; else echo "ok: keyring entry for k removed"; fi
run "logout b (revokes the gma_ token)" $GRAIN logout b
[ -e $CFG/credentials/b ] && echo "STALE: credentials/b remains" || echo "ok: credentials/b removed"
run "the revoked gma_ token no longer signs in" env GRAIN_TOKEN="$BTOK" $GRAIN --server $B whoami
run "logout a" $GRAIN logout a
run "whoami --context a after logout" $GRAIN --context a whoami
grep -rlF -- "$(cat $A_CODE_FILE)" $W 2>/dev/null | grep -v fakekeyring | sed 's/^/    still holds A code: /'
find $CFG -type f -printf '    %m %p\n'
