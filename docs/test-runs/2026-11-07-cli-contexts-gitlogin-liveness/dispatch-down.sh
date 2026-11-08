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

# §8 item 4: a live dispatch-down condition on a real `grain daemon`
# (SQLite store, host sandboxes), with a stored secret's value inside the
# underlying error, read back through every surface and cleared by a
# restart once the fault is gone.
#
# The fault: the git proxy's port (sandbox-port-base + 0) is held by
# another process, so runDaemon fails with ConditionGitProxy. To put a
# secret inside that error deterministically, a secret whose value is
# the bound address ("127.0.0.1:<port>") is stored first: the bind error
# quotes it, so a working redactor must print [REDACTED] in its place.
#   start-d.sh starts the daemon (see the run report for its flags).
set -u
GRAIN=${GRAIN:-/home/debian/bin/grain}
D=${D:-/tmp/w/d/data}
S=http://127.0.0.1:4293
CODE=$(cat $D/local-api-token)
PORT=24576
SECRET_VALUE="127.0.0.1:$PORT"
export HOME=$(mktemp -d /tmp/grain-dd.XXXXXX) GRAIN_SANDBOX_CLI=0
export XDG_CONFIG_HOME=$HOME/.config
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_CONTEXT
run() { local label=$1; shift; echo "### $label"; echo "\$ $*"; "$@" 2>&1 | sed 's/^/    /'; echo "    -> exit=${PIPESTATUS[0]}"; }
authed() { env GRAIN_TOKEN="$CODE" $GRAIN --server $S "$@"; }
stop_daemon() { pkill -f "^grain daemon -data-dir $D"; for i in $(seq 40); do pgrep -f "^grain daemon -data-dir $D" >/dev/null || break; sleep 0.25; done; }

echo "== store a secret whose value the bind error will quote"
stop_daemon
printf '%s' "$SECRET_VALUE" | $GRAIN secrets -data-dir $D set bind-host value 2>&1 | sed 's/^/    /'

echo "== hold the git proxy's port and start the daemon"
python3 -c "import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1); s.bind(('127.0.0.1',$PORT)); s.listen(); time.sleep(600)" &
HOLDER=$!
sleep 0.5
bash /tmp/w/start-d.sh
for i in $(seq 60); do
  n=$(curl -s -H "Authorization: Bearer $CODE" $S/api/sandboxes | jq '.dispatchBlocked | length' 2>/dev/null)
  [ "${n:-0}" -gt 0 ] && break; sleep 0.5
done
grep 'dispatch blocked' /tmp/w/d/log | tail -1 | sed 's/^/    journal: /'

echo "== unauthenticated: nothing of the condition may show"
for p in /api/auth/config /api/auth/whoami /api/config /api/sandboxes /api/health; do
  body=$(curl -s $S$p)
  if echo "$body" | grep -qiE "git.proxy|address already in use|$PORT|dispatchBlocked"; then
    echo "    LEAK  GET $p (no credential): $(echo "$body" | head -c 300)"
  else
    echo "    ok    GET $p (no credential): $(echo "$body" | head -c 160)"
  fi
done

echo "== authenticated surfaces"
run "grain health" authed health
run "GET /api/sandboxes .dispatchBlocked" sh -c "curl -s -H 'Authorization: Bearer $CODE' $S/api/sandboxes | jq -c .dispatchBlocked"
run "GET /api/config .dispatchBlocked" sh -c "curl -s -H 'Authorization: Bearer $CODE' $S/api/config | jq -c '{reconcilerDown,dispatchBlocked}'"
run "file a task" authed -json create -repo acme/widgets -title "queued behind a dead git proxy" -body x -approve
ID=$(authed -json list | jq -r '.[] | select(.title=="queued behind a dead git proxy") | .id' | tail -1)
run "grain show <queued>" sh -c "env GRAIN_TOKEN=$CODE $GRAIN --server $S show $ID | head -8"
run "grain show -json <queued> .reason" sh -c "env GRAIN_TOKEN=$CODE $GRAIN --server $S -json show $ID | jq -c .reason"
run "grain list (queued row)" sh -c "env GRAIN_TOKEN=$CODE $GRAIN --server $S list | grep -E '^$ID '"
echo "### MCP read_grain_task over grain mcp (stdio; the plan names it grain_get_task, which this build does not register)"
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"walkthrough","version":"1"}}}' \
  "{\"jsonrpc\":\"2.0\",\"id\":2,\"method\":\"tools/call\",\"params\":{\"name\":\"read_grain_task\",\"arguments\":{\"id\":\"$ID\"}}}" |
  env GRAIN_TOKEN="$CODE" timeout 20 $GRAIN --server $S mcp 2>/dev/null | tail -1 | jq -r '.result.content[0].text // .error.message // .' 2>/dev/null | jq -c .reason | sed 's/^/    /'

echo "== the secret value never appears on an authenticated surface either"
for p in /api/sandboxes /api/config "/api/tasks/$ID" /api/health; do
  if curl -s -H "Authorization: Bearer $CODE" "$S$p" | grep -qF "$SECRET_VALUE"; then echo "    LEAK  $p carries $SECRET_VALUE"; else echo "    ok    $p: no $SECRET_VALUE"; fi
done
if authed health 2>&1 | grep -qF "$SECRET_VALUE"; then echo "    LEAK  grain health"; else echo "    ok    grain health: no $SECRET_VALUE"; fi

echo "== recovery: free the port, restart, condition gone"
kill $HOLDER; wait $HOLDER 2>/dev/null
bash /tmp/w/start-d.sh
sleep 3
run "GET /api/sandboxes .dispatchBlocked after restart" sh -c "curl -s -H 'Authorization: Bearer $CODE' $S/api/sandboxes | jq -c .dispatchBlocked"
run "grain show -json <task> .reason after restart" sh -c "env GRAIN_TOKEN=$CODE $GRAIN --server $S -json show $ID | jq -c '{state,reason}'"
