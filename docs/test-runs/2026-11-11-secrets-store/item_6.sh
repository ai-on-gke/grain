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

# item_6.sh: §3 item 6, redaction through the daemon. The daemon runs as
# grain-walk.service (start_daemon_unit) so read_grain_log's "daemon"
# source reads its real journal. A stored secret's value and the migrated
# GitHub PAT are written into that journal by a shell moved into the
# daemon's unit (env.sh's journal-inject.txt), then a real
# run reads them back. Values are checked by grep, never printed.
set -uo pipefail
source "$(dirname "$0")/env.sh"
step() { printf '\n== %s\n' "$*"; }
JOURNAL_GRANT='https://grain.dev/capabilities/grain#journal={}'
inject() { echo "$1" >>"$WORK/journal-inject.txt"; sleep 1; } # one line in the unit's journal (env.sh)
leaks() { # leaks FILE: how many times a planted value appears in FILE
	local n=0 v
	for v in "$WORK/values/redact-me" "$WORK/values/pat2"; do n=$((n + $(grep -cF "$(tr -d '[:space:]' <"$v")" "$1" || true))); done
	echo "$n"
}

stop_daemon
start_daemon_unit "$HEAD_BIN" && setup_agent
head -c 24 /dev/urandom | base64 | tr -dc A-Za-z0-9 | sed 's/^/sk-walk-/' >"$WORK/values/redact-me"
api PUT /api/secrets/walk-redact/value "$(jq -n --rawfile v "$WORK/values/redact-me" '{value:($v|rtrimstr("\n"))}')"; echo
inject "walk-marker-1 stored=$(cat "$WORK/values/redact-me") pat=$(tr -d '[:space:]' <"$WORK/values/pat2")"
sudo journalctl -u grain-walk.service --no-pager | grep -c walk-marker-1 | sed 's/^/marker lines in the unit journal: /'

step "6a. read_grain_log hides the stored value and the migrated PAT"
rm -f "$WORK/walk/runs/"*.json
T=$(run_task "read_grain_log redaction" '{"tools":[["read_grain_log",{"source":"daemon","contains":"walk-marker-1"}]]}' "$JOURNAL_GRANT")
jq -r '.calls[0].text' "$WORK/walk/runs/$T.json" >"$WORK/6a.txt"
echo "planted values visible in the answer: $(leaks "$WORK/6a.txt")"
grep walk-marker-1 "$WORK/6a.txt" | sed -E 's/^.*(walk-marker-1.*)$/\1/' | cut -c1-200
grep -i 'redacted' "$WORK/6a.txt" | tail -1 | cut -c1-200

step "6b. daemon not answering mid-run: lines are withheld, not half-redacted"
# Stopping the unit cancels the daemon's runs with it, so the daemon is
# frozen instead (SIGSTOP): the run's mcpserver is up, the journal is
# readable, and the redaction it asks the daemon for never comes.
rm -f "$WORK/walk/paused" "$WORK/walk/continue"
inject "walk-marker-2 stored=$(cat "$WORK/values/redact-me")"
T=$(file_task "read_grain_log with the daemon stopped" '{"pause_after":1,"tools":[["read_grain_log",{"source":"daemon","contains":"walk-marker-2"}],["read_grain_log",{"source":"daemon","contains":"walk-marker-2"}]]}' "$JOURNAL_GRANT")
for _ in $(seq 120); do [[ -f "$WORK/walk/paused" ]] && break; sleep 1; done
main=$(systemctl show -p MainPID --value grain-walk.service)
kill -STOP "$main"
curl -s -m 3 -o /dev/null -w 'daemon API while frozen: %{http_code}\n' "$API/api/config"
touch "$WORK/walk/continue"
for _ in $(seq 120); do [[ -f "$WORK/walk/runs/$T.json" ]] && break; sleep 1; done
kill -CONT "$main"
for i in 0 1; do
	jq -r ".calls[$i].text" "$WORK/walk/runs/$T.json" >"$WORK/6b-$i.txt"
	echo "call $((i + 1)) ($([[ $i == 0 ]] && echo daemon up || echo daemon frozen)): planted values visible $(leaks "$WORK/6b-$i.txt"); withheld lines $(grep -c 'withheld' "$WORK/6b-$i.txt"); error=$(jq -r ".calls[$i].isError" "$WORK/walk/runs/$T.json")"
	grep -E 'walk-marker-2|withheld' "$WORK/6b-$i.txt" | head -2 | cut -c1-160
done

step "6c. the run's own credentials cannot reach POST /api/secrets/redact"
T=$(run_task "redact route probe" '{"tools":[["_redact_probe",{}]]}')
jq -c ".calls[0].results[], .token_files" "$WORK/walk/runs/$T.json"
echo "owner (local API token): $(api POST /api/secrets/redact '{"lines":["probe"]}' -o /dev/null -w '%{http_code}' 2>/dev/null; curl -s -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $(cat "$WORK/data/local-api-token")" -H 'Content-Type: application/json' --data '{"lines":["probe"]}' "$API/api/secrets/redact")"
echo "no credential: $(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' --data '{"lines":["probe"]}' "$API/api/secrets/redact")"
