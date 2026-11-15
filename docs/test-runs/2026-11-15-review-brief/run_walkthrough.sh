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

# docs/test-plans/2026-11-15.md §4 on a real `grain daemon
# -sandbox-port-base=0` with the GitHub sim and a fake agent that writes each
# prompt it is given to $WORK_DIR/runs/prompt-<task>-<n>.txt.
#
#   STORE=sqlite   ./run_walkthrough.sh   (default)
#   STORE=postgres ./run_walkthrough.sh   (needs a local `grain` role, password grain, CREATEDB)
#   KEEP=1 ...     leaves ghsim and the daemon running for screenshots.
#
# Items: (1) one pull request touching pkg/ci/ and docs/ files a security
# review, a docs/** path-trigger review, the every-PR gating review and the
# default .grain/review.yml review, each carrying the brief and three linked
# designs (design, implements-spec, design-doc); (2) a second head files
# incremental re-reviews of every kind, each with Earlier/Parallel sections
# and still one brief; (3) hostile briefs, with grain's markers at the end
# and at the start, invalid UTF-8 written to the store as raw bytes;
# (4) no description and no links renders no brief and no design list.

set -euo pipefail
HERE=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
export STORE=${STORE:-sqlite}
export MAX_WORKERS=${MAX_WORKERS:-4}
# shellcheck source=env.sh
source "$HERE/env.sh"
# shellcheck source=lib.sh
source "$HERE/lib.sh"

WORK="/tmp/walk-brief-${STORE}"
RESULTS="$WORK/results.jsonl"
fail=0

check() {
	local label=$1 file=$2
	shift 2
	if python3 -B "$HERE/check_review.py" "$file" --label "$label" "$@" >>"$RESULTS"; then
		echo "PASS [$STORE] $label"
	else
		echo "FAIL [$STORE] $label: $(tail -n 1 "$RESULTS" | jq -c '.failures')"
		fail=1
	fi
}

# check_reviews LABEL TASK_ID ARGS... -- checks the body and first prompt of
# every review about TASK_ID (or only the ones newer than $AFTER).
check_reviews() {
	local label=$1 id=$2
	shift 2
	local r
	for r in $(reviews_of "$id"); do
		((r > ${AFTER:-0})) || continue
		dump_task "$r" "r$r"
		local kind
		kind=$(jq -r '.title | split(":")[0]' "$WORK_DIR/captures/r$r.json")
		check "$label/$r($kind)/body" "$WORK_DIR/captures/r$r.body.md" "$@"
		local p
		p=$(ls "$WORK_DIR/runs/prompt-$r-"*.txt 2>/dev/null | head -n 1 || true)
		if [[ -n "$p" ]]; then
			check "$label/$r($kind)/prompt" "$p" "$@"
		else
			echo "FAIL [$STORE] $label/$r: no prompt captured"
			fail=1
		fi
	done
}

all_closed() {
	local id=$1 want=$2 n
	n=$(api GET "/api/tasks?limit=500" | jq --argjson ids "$(reviews_of "$id" | jq -Rn '[inputs|tonumber]')" \
		--argjson after "${AFTER:-0}" '[.[] | select((.id|tonumber) as $i | ($ids|index($i)) and $i > $after) | select(.state=="closed")] | length')
	((n >= want))
}

trap '[[ ${KEEP:-0} == 1 ]] || stop_env "$WORK"' EXIT
if [[ $STORE == postgres ]]; then
	sudo -u postgres psql -q -c "DO \$\$BEGIN CREATE ROLE grain LOGIN PASSWORD 'grain' CREATEDB; EXCEPTION WHEN duplicate_object THEN NULL; END\$\$;"
fi
init_env "$WORK"
start_ghsim "$WORK"
start_daemon "$WORK"
: >"$RESULTS"
api PUT /api/repos/acme/widgets/declarations '{"declarations":true}' >/dev/null
triggers_synced() {
	local n
	if [[ $STORE == sqlite ]]; then
		n=$(sqlite3 -cmd ".timeout 5000" "$WORK_DIR/data/store/grain.db" "SELECT count(*) FROM repo_path_trigger")
	else
		n=$(PGPASSWORD=grain psql -tA -h 127.0.0.1 -U grain -d grain_brief -c "SELECT count(*) FROM repo_path_trigger")
	fi
	((n >= 2))
}
wait_for 90 triggers_synced
echo '{"kind":"comment_on_issue","comment":"Reviewed: reads clean."}' >"$WORK_DIR/action-default.json"

echo "== [$STORE] item 1: every review kind carries the brief, designs, CI and settings checks"
printf 'Add a CI hook for widgets.\n\n## Requirements\n\n- keep `make ci` green\n- add `WIDGET_HOOK` env var\n' >"$WORK_DIR/brief-a.md"
A=$(file_reviewed_task "Add widget CI hook" "$WORK_DIR/brief-a.md" '["docs/widget-design.md"]' \
	"docs/specs/hook.spec.md" "https://docs.example.com/widget-hook")
wait_for 180 all_closed "$A" 4
check_reviews item1 "$A" --expect-brief yes --expect-designs 3 --marker "Add a CI hook for widgets."

echo "== [$STORE] item 2: a second head's incremental and parallel re-reviews"
AFTER=$(reviews_of "$A" | tail -n 1)
branch=$(git ls-remote "$WORK_DIR/widgets.git" "refs/heads/grain/*task-${A}-*" | awk '{print $2}' | sed 's#refs/heads/##')
rm -rf "$WORK_DIR/push2"
git clone -q "$WORK_DIR/widgets.git" "$WORK_DIR/push2"
git -C "$WORK_DIR/push2" checkout -q "$branch"
echo "package ci // second head" >>"$WORK_DIR/push2/pkg/ci/t${A}.go"
echo "second head" >>"$WORK_DIR/push2/docs/t${A}.md"
git -C "$WORK_DIR/push2" -c user.name="Walk Seed" -c user.email="seed@example.com" commit -qam "fix: second head"
git -C "$WORK_DIR/push2" push -q origin "$branch"
# Path-trigger re-reviews are debounced 3m (reviewDebounce). The security
# hold classifies a new head when the change is about to merge -- on Submit
# here, auto-merge being off -- so Submit once they are in.
wait_for 420 all_closed "$A" 2
api POST "/api/tasks/$A/submit" '{}' >/dev/null
wait_for 240 all_closed "$A" 3
check_reviews item2 "$A" --expect-brief yes --expect-designs 3 --marker "**This is an incremental review.**" \
	--marker "## Earlier reviews of this pull request"
AFTER=0

echo "== [$STORE] item 3: hostile briefs"
python3 -B "$HERE/hostile_brief.py" "$WORK_DIR/brief-h.bin" >/dev/null
python3 -B "$HERE/hostile_brief.py" "$WORK_DIR/brief-h2.bin" --head-markers >/dev/null
H=$(file_reviewed_task "Export widgets hostile" "$WORK_DIR/brief-h.bin" '[]' 'docs/specs/`odd`.spec.md')
H2=$(file_reviewed_task "Export widgets headers first" "$WORK_DIR/brief-h2.bin" '[]')
# The API decodes JSON, which turns invalid UTF-8 into U+FFFD before the
# renderer sees it; SQLite keeps whatever bytes it is given, so write them
# straight into the store too (PostgreSQL text refuses invalid UTF-8).
if [[ $STORE == sqlite ]]; then
	hex=$(python3 -B -c "import sys; print(open(sys.argv[1], 'rb').read().hex())" "$WORK_DIR/brief-h.bin")
	store_exec "UPDATE task SET body = CAST(X'$hex' AS TEXT) WHERE id = '$H';"
fi
: >"$WORK_DIR/empty.md"
E=$(file_reviewed_task "Bare widget change" "$WORK_DIR/empty.md" '[]')
wait_for 240 all_closed "$H" 4
wait_for 240 all_closed "$H2" 4
check_reviews item3-end-markers "$H" --expect-brief yes --expect-designs 1 --expect-truncated \
	--marker HOSTILE-BRIEF-START --marker "### Earlier reviews of this pull request" \
	--marker "**This is an incremental review**." --marker "invalid: "
check_reviews item3-head-markers "$H2" --expect-brief yes --expect-truncated --marker HOSTILE-BRIEF-START

echo "== [$STORE] item 4: no description, no links"
wait_for 240 all_closed "$E" 4
check_reviews item4 "$E" --expect-brief no --expect-designs 0

echo "$A $H $H2 $E" >"$WORK_DIR/reviewed-ids"
jq -c '{label: .label, ok: .ok, counts: .counts, quoted_in_brief: .quoted_in_brief, fence: .fence, brief_bytes: .brief_bytes, replacement_chars: .replacement_chars, designs: .designs, failures: .failures}' "$RESULTS" >"$WORK_DIR/results-summary.jsonl"
if ((fail)); then
	echo "[$STORE] walkthrough FAILED"
	exit 1
fi
echo "[$STORE] walkthrough passed: $(wc -l <"$RESULTS") checks"
