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

# §8 item 2: the external-agent loop (create -> wait -> inbox -> reply ->
# logs) driven only from the CLI with a gma_ token, against a live
# `ui/e2e/consolerestserver` (real ui.Server + orchestrator.RunCycle; the
# agent is scripted and makes real MCP calls through `grain mcpserver`).
#   C=<url> CODE=<access code> ./lifecycle.sh
set -u
GRAIN=${GRAIN:-/home/debian/bin/grain}
C=${C:?consolerestserver URL}
CODE=${CODE:?access code}
W=$(mktemp -d /tmp/grain-life.XXXXXX)
export HOME=$W XDG_CONFIG_HOME=$W/.config GRAIN_SANDBOX_CLI=0
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_CONTEXT DBUS_SESSION_BUS_ADDRESS

run() { local label=$1; shift; echo "### $label"; echo "\$ $*"; "$@" 2>&1 | sed 's/^/    /'; echo "    -> exit=${PIPESTATUS[0]}"; }
op() { curl -s -H "Authorization: Bearer $CODE" -H 'Content-Type: application/json' "$@"; }
script() { op -X POST "$C/e2e/script-runs" -d "$1" -o /dev/null -w '    script-runs -> %{http_code}\n'; }

echo "== operator turns on externalAgentApi; the workstation mints a gma_ token"
op -X PUT "$C/api/config" -d '{"externalAgentApi":true}' -o /dev/null -w '    PUT /api/config -> %{http_code}\n'
run "login --issue-token" env GRAIN_TOKEN="$CODE" $GRAIN login "$C" --name c --issue-token --expires 1d
run "whoami" $GRAIN whoami

echo "== scripted turns for T1: ask_question, then request_secret, then request_access, then finish"
script '{"title":"T1 external loop","turns":[
 {"mcpCalls":[{"name":"ask_question","arguments":{"summary":"Which retry budget should T1 use?","question":"Should the retry budget be 3 or 5?"}}],"finalText":"asked"},
 {"mcpCalls":[{"name":"request_secret","arguments":{"secret":"widgets-deploy-key","summary":"Set widgets-deploy-key for the deploy step","reason":"The deploy step needs a key."}}],"finalText":"asked for a secret"},
 {"mcpCalls":[{"name":"request_access","arguments":{"summary":"Grant read on acme/other","reason":"T1 reads acme/other.","requests":[{"kind":"read","repo":"acme/other"}]}}],"finalText":"asked for access"},
 {"mcpCalls":[{"name":"comment_on_issue","arguments":{"comment":"T1 done."}}],"finalText":"done"}]}'

echo "== file T1, and T2 depending on it"
run "create without -repo (external caller)" $GRAIN create -title "no repo" -body x
run "create T1" $GRAIN -json create -repo acme/widgets -title "T1 external loop" -body "external loop"
T1=$($GRAIN -json list 2>/dev/null | jq -r '.[] | select(.title=="T1 external loop") | .id' | head -1)
run "create T2 -depends-on T1" $GRAIN create -repo acme/widgets -title "T2 waits on T1" -body "after T1" -depends-on "$T1"
T2=$($GRAIN -json list 2>/dev/null | jq -r '.[] | select(.title=="T2 waits on T1") | .id' | head -1)
echo "    T1=$T1 T2=$T2"
run "an agent cannot approve its own filing" $GRAIN approve "$T2"
run "operator approves T1 (access code)" env GRAIN_TOKEN="$CODE" $GRAIN --server "$C" approve "$T1"
run "operator approves T2 (access code)" env GRAIN_TOKEN="$CODE" $GRAIN --server "$C" approve "$T2"
run "list -json T2 blocked" sh -c "$GRAIN -json list | jq -c '.[] | select(.id==\"$T2\") | {id,state,dependsOn,blocked,reason,statusReason}'"
run "list shows the dependency" sh -c "$GRAIN list | grep -E '^($T1|$T2) '"
run "show T2 shows depends_on" sh -c "$GRAIN show $T2 | head -20"
run "show T2 -json depends_on" sh -c "$GRAIN -json show $T2 | jq -c '{id,state,dependsOn,depends_on,blockedBy}'"
run "record.md for T2 (consolerestserver serves no namespace; see demo run)" sh -c "$GRAIN fs read task://$T2/record.md | head -30"

echo "== T1 parks on ask_question; grain wait wakes on it"
run "wait T1 --until awaiting-reply" timeout 60 $GRAIN wait "$T1" -until awaiting-reply
run "show T1 liveness" sh -c "$GRAIN show $T1 | head -12"
run "inbox" $GRAIN inbox
run "inbox -json (T1 item)" sh -c "$GRAIN -json inbox | jq -c '.' | head -c 1500"
run "inbox -repo acme/other (nothing of acme/widgets may leak in)" $GRAIN inbox -repo acme/other
run "reply T1" $GRAIN reply "$T1" "Use 5."

echo "== T1 parks on request_secret"
run "wait T1 (secret)" timeout 60 $GRAIN wait "$T1" -until awaiting-reply
run "show T1 liveness (secret)" sh -c "$GRAIN show $T1 | head -12"
run "inbox -kind secret" $GRAIN inbox -kind secret
run "reply cannot set a secret" $GRAIN reply "$T1" "here is the key: s3cr3t-value"
run "show T1 after a plain reply to a secret request" sh -c "$GRAIN show $T1 | head -6"

echo "== the agent's gma_ token cannot set the secret; the operator sets it in the task's secret box"
run "PUT /api/tasks/T1/secret with the gma_ token" sh -c "curl -s -o /dev/null -w '%{http_code}' -X PUT -H 'Authorization: Bearer '\$(cat $XDG_CONFIG_HOME/grain/credentials/c) -H 'Content-Type: application/json' $C/api/tasks/$T1/secret -d '{\"value\":\"s3cr3t-value\"}'; echo"
op -X PUT "$C/api/tasks/$T1/secret" -d '{"value":"s3cr3t-value"}' -o /dev/null -w '    operator PUT /api/tasks/T1/secret -> %{http_code}\n'

echo "== T1 parks on request_access"
run "wait T1 (access)" timeout 60 $GRAIN wait "$T1" -until awaiting-reply
run "show T1 (access)" sh -c "$GRAIN show $T1 | head -12"
run "inbox (access)" $GRAIN inbox
run "reply cannot grant access" $GRAIN reply "$T1" "granted, go ahead"
run "the agent's token cannot grant its own access request" sh -c "curl -s -w ' %{http_code}' -X POST -H 'Authorization: Bearer '\$(cat $XDG_CONFIG_HOME/grain/credentials/c) -H 'Content-Type: application/json' $C/api/tasks/$T1/access-request/grant -d '{}'; echo"
op -X POST "$C/api/tasks/$T1/access-request/grant" -d '{}' -o /dev/null -w '    operator POST access-request/grant -> %{http_code}\n'
run "wait T1 (after the grant, the closing comment)" timeout 60 $GRAIN wait "$T1" -until awaiting-reply
run "show T1 (answered)" sh -c "$GRAIN show $T1 | head -4"
run "operator closes T1 (access code)" env GRAIN_TOKEN="$CODE" $GRAIN --server "$C" close "$T1"
run "wait T1 closed" timeout 30 $GRAIN wait "$T1"
run "T2 dispatches once T1 closed (its default scripted turn parks it answered)" timeout 90 $GRAIN wait "$T2" -until closed,awaiting-reply
run "show T2" sh -c "$GRAIN show $T2 | head -5"
run "list -closed T1/T2" sh -c "$GRAIN list -closed | grep -E '^($T1|$T2) '"

echo "== inbox scoping: repo-less items stay out of a -repo filter"
run "inbox (all)" $GRAIN inbox
run "inbox -repo acme/widgets" $GRAIN inbox -repo acme/widgets

echo "== a token limited to acme/other cannot wait on, read or reply to an acme/widgets task"
run "login --name other --repos acme/other (login makes it current)" env GRAIN_TOKEN="$CODE" $GRAIN login "$C" --name other --issue-token --repos acme/other --expires 1d
run "context use c" $GRAIN context use c
run "whoami --context other" $GRAIN --context other whoami
run "wait --context other on T1" timeout 30 $GRAIN --context other wait "$T1"
run "-json wait --context other on T1" timeout 30 $GRAIN --context other -json wait "$T1"
run "show --context other T1" $GRAIN --context other show "$T1"
run "logs --context other T1" $GRAIN --context other logs "$T1"
run "inbox --context other" $GRAIN --context other inbox

echo "== wait boundaries"
run "wait on an unknown task" timeout 30 $GRAIN wait 999999
run "-json wait on an unknown task" timeout 30 $GRAIN -json wait 999999

echo "== logs"
run "logs T1" sh -c "$GRAIN logs $T1 | tail -20"
run "logs T1 -attempt 1 -tail 5" $GRAIN logs "$T1" -attempt 1 -tail 5
run "logs unknown task" $GRAIN logs 999999
