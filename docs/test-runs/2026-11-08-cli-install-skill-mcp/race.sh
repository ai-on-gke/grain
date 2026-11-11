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

# §2 item 3 (first half): the park-settled `grain wait --until
# awaiting-reply` -> immediate `grain reply` race (#888), against
# `ui/e2e/consolerestserver` -- a real ui.Server + orchestrator.RunCycle
# whose scripted agent makes real MCP calls through `grain mcpserver` and
# then lingers DELAY ms before its process exits (delayMsBeforeExit).
#
#   C=<consolerestserver URL> CODE=<access code> [N=20] [DELAY=1500] ./race.sh
set -u
GRAIN=${GRAIN:-/home/debian/bin/grain}
C=${C:?consolerestserver URL}
CODE=${CODE:?access code}
N=${N:-20}
NC=${NC:-5}
DELAY=${DELAY:-1500}
W=$(mktemp -d /tmp/grain-race.XXXXXX)
export HOME=$W XDG_CONFIG_HOME=$W/.config GRAIN_SANDBOX_CLI=0
unset GRAIN_SERVER GRAIN_TOKEN GRAIN_CONTEXT
op() { curl -s -H "Authorization: Bearer $CODE" -H 'Content-Type: application/json' "$@"; }
ms() { date +%s%3N; }

op -X PUT "$C/api/config" -d '{"externalAgentApi":true}' -o /dev/null -w 'PUT /api/config externalAgentApi -> %{http_code}\n'
env GRAIN_TOKEN="$CODE" $GRAIN login "$C" --name race --issue-token --expires 1d

# N ask_question turns, then a request_secret turn, a request_access turn
# and a closing comment -- every one lingering DELAY ms after its call.
python3 - "$((N + NC))" "$DELAY" >"$W/turns.json" <<'PY'
import json, sys
n, delay = int(sys.argv[1]), int(sys.argv[2])
turns = [{"mcpCalls": [{"name": "ask_question", "arguments": {
    "summary": "Race iteration %d: go or stop?" % (i + 1),
    "question": "Iteration %d of the park-settled race: reply go." % (i + 1)}}],
    "finalText": "asked %d" % (i + 1), "delayMsBeforeExit": delay} for i in range(n)]
turns.append({"mcpCalls": [{"name": "request_secret", "arguments": {
    "secret": "race-deploy-key", "summary": "Set race-deploy-key", "reason": "the deploy step"}}],
    "finalText": "asked for a secret", "delayMsBeforeExit": delay})
turns.append({"mcpCalls": [{"name": "request_access", "arguments": {
    "summary": "Grant read on acme/other", "reason": "reads acme/other",
    "requests": [{"kind": "read", "repo": "acme/other"}]}}],
    "finalText": "asked for access", "delayMsBeforeExit": delay})
turns.append({"mcpCalls": [{"name": "comment_on_issue", "arguments": {"comment": "race done"}}],
    "finalText": "done"})
print(json.dumps({"title": "park-settled race", "turns": turns}))
PY
op -X POST "$C/e2e/script-runs" -d @"$W/turns.json" -o /dev/null -w 'script-runs -> %{http_code}\n'
T=$($GRAIN -json create -repo acme/widgets -title "park-settled race" -body "race" | jq -r '.id')
env GRAIN_TOKEN="$CODE" $GRAIN --server "$C" approve "$T" >/dev/null
echo "task $T; $N iterations; each turn lingers ${DELAY}ms after its ask"
echo
printf '%-5s %-10s %-9s %-9s %s\n' iter wait-exit wake-ms reply show-line
conflicts=0; fails=0
for i in $(seq 1 "$N"); do
  t0=$(ms)
  timeout 120 $GRAIN wait "$T" --until awaiting-reply >/dev/null 2>"$W/wait.err"; we=$?
  t1=$(ms)
  show=$($GRAIN show "$T" | grep -m1 '^reason:' | tr -s ' ')
  out=$($GRAIN reply "$T" "go" 2>&1); re=$?
  case "$out" in *"not parked"*|*409*) conflicts=$((conflicts + 1));; esac
  [ "$we" = 0 ] && [ "$re" = 0 ] || fails=$((fails + 1))
  printf '%-5s %-10s %-9s %-9s %s\n' "$i" "$we" "$((t1 - t0))" "$re" "$show"
  [ "$re" = 0 ] || echo "      reply said: $out"
done
echo
echo "== control: $NC iterations replying the moment the task's *state* reads awaiting_reply"
echo "   (polling grain show, not grain wait) -- does the stored state ever read awaiting_reply while the attempt lingers ${DELAY}ms?"
printf '%-5s %-9s %-9s %s\n' iter seen-ms reply said
ctl409=0
for i in $(seq 1 "$NC"); do
  t0=$(ms)
  for _ in $(seq 1 600); do
    # Polled with the operator's access code: the gma_ token's 60
    # requests/minute would refuse a 20/s poll.
    [ "$(GRAIN_TOKEN="$CODE" $GRAIN --server "$C" -json show "$T" | jq -r .state)" = awaiting_reply ] && break
    sleep 0.05
  done
  t1=$(ms)
  out=$($GRAIN reply "$T" "go" 2>&1); re=$?
  case "$out" in *"not parked"*|*409*) ctl409=$((ctl409 + 1));; esac
  printf '%-5s %-9s %-9s %s\n' "$i" "$((t1 - t0))" "$re" "$(echo "$out" | head -c 160)"
  if [ "$re" != 0 ]; then
    timeout 120 $GRAIN wait "$T" --until awaiting-reply >/dev/null
    $GRAIN reply "$T" "go" >/dev/null 2>&1 || echo "      retry after grain wait failed"
  fi
done
echo "   control: $ctl409/$NC replies refused while the attempt was still live"
echo
echo "== the secret and access sub-cases"
timeout 120 $GRAIN wait "$T" --until awaiting-reply >/dev/null; echo "wait (secret) exit=$?"
$GRAIN show "$T" | head -8 | sed 's/^/    /'
$GRAIN -json show "$T" | jq -c '{state, derivedState, statusReason, reason, awaitingReply}' | sed 's/^/    /'
out=$($GRAIN reply "$T" "here" 2>&1); echo "    plain reply to a secret request -> exit=$? $out"
op -X PUT "$C/api/tasks/$T/secret" -d '{"value":"race-s3cret"}' -o /dev/null -w '    operator PUT secret (immediately after wake) -> %{http_code}\n'
timeout 120 $GRAIN wait "$T" --until awaiting-reply >/dev/null; echo "wait (access) exit=$?"
$GRAIN show "$T" | head -8 | sed 's/^/    /'
op -X POST "$C/api/tasks/$T/access-request/grant" -d '{}' -o /dev/null -w '    operator grant (immediately after wake) -> %{http_code}\n'
timeout 120 $GRAIN wait "$T" --until awaiting-reply >/dev/null; echo "wait (closing comment) exit=$?"
$GRAIN show "$T" | head -4 | sed 's/^/    /'
echo
echo "== grain wait: $N iterations, $conflicts x 409 \"not parked\", $fails failed wait/reply; control (state polling): $ctl409/$NC refused"
