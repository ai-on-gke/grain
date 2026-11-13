#!/bin/sh
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

# stub-claude.sh: stands in for Claude Code on walkhost (only the model
# provider is stubbed). It records its argv, waits (so the daemon can be
# restarted mid-run), then does what Claude Code does at the end of a turn:
# forks the Stop hook named in the --settings file with a turn that called
# no tool and said only "All done.", recording the hook's verdict -- grain's
# stop rule should nudge ("block") such a turn.
case "${1:-}" in
--version|-v|version) echo "2.0.0 (Claude Code)"; exit 0 ;;
esac
log=/home/walkhost/stub-logs
mkdir -p "$log"
out="$log/claude.$$.txt"
settings=""
prev=""
for a in "$@"; do
	[ "$prev" = --settings ] && settings=$a
	prev=$a
done
{
	echo "argv: $*"
	echo "--- env"
	env | sort
} >"$out"
[ -f /home/walkhost/stub-hook-delay ] && sleep "$(cat /home/walkhost/stub-hook-delay)"
if [ -n "$settings" ] && [ -f "$settings" ]; then
	echo "--- settings" >>"$out"
	cat "$settings" >>"$out"
	echo >>"$out"
	cmd=$(python3 -c 'import json,sys
d=json.load(open(sys.argv[1]))
for m in d.get("hooks",{}).get("Stop",[]):
    for h in m.get("hooks",[]):
        print(h["command"]); sys.exit(0)' "$settings")
	tr=$(mktemp)
	printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"All done."}]}}' >"$tr"
	verdict=$(printf '{"hook_event_name":"Stop","stop_hook_active":false,"transcript_path":"%s","last_assistant_message":"All done."}' "$tr" |
		sh -c "$cmd" 2>>"$out")
	echo "stop hook => ${verdict:-<no opinion>} (exit $?)" >>"$out"
	rm -f "$tr"
fi
echo '{"type":"system","subtype":"init","session_id":"stub"}'
echo '{"type":"result","subtype":"success","is_error":false,"result":"stub claude done.","session_id":"stub"}'
echo "--- done" >>"$out"
