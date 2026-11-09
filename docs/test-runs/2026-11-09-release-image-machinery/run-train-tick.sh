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

# run-train-tick.sh: 2026-11-09 §6 item 3. Copies the real repository's
# main, release/v3.2.0, candidate and tags into a scratch bare remote under
# <out>, and runs main's scripts/release-from-trunk.sh with DRY_RUN=true
# from a scratch clone of it -- never in the checkout it was given -- with
# CI, qualification and rc-hold answers served from local JSON files the
# way the hourly rc-qualification.yml job's token would serve them.
#
# Ticks (each writes <out>/tick-<name>.log):
#   cut-none        remote rewound to before v3.2.0-rc.3 (rc.2 discarded),
#                   release.conf as main has it (RELEASE_QUALIFICATION_APP=none),
#                   the workflow's empty vars.RELEASE_QUALIFICATION_APP
#   cut-unset       the same, with that line removed from main's release.conf
#   qualify-none    v3.2.0-rc.3 cut and Stage 1 (nightly.yml) green on it,
#                   v3.2.0-pending not yet tagged
#   qualify-unset   the same, with release.conf's line removed
#   now-none        the remote exactly as fetched (rc.3 and v3.2.0-pending)
#
# TRAIN_SCRIPT=<path> runs that release-from-trunk.sh (a branch's edit of
# it) instead of the scratch clone's own copy of main's.
#
# Usage: run-train-tick.sh <repo checkout> <out dir>
set -euo pipefail

repo=$(cd "${1:?repo checkout}" && pwd)
out=$(mkdir -p "${2:?out dir}" && cd "$2" && pwd)
case "$out" in "$repo" | "$repo"/*) echo "refusing to run the train inside $repo" >&2; exit 2 ;; esac

git -C "$repo" fetch -q origin '+refs/heads/main:refs/remotes/origin/main' \
	'+refs/heads/release/v3.2.0:refs/remotes/origin/release/v3.2.0' \
	'+refs/heads/candidate:refs/remotes/origin/candidate' --tags

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
export GIT_AUTHOR_NAME=walkthrough GIT_AUTHOR_EMAIL=walkthrough@example.com
export GIT_COMMITTER_NAME=walkthrough GIT_COMMITTER_EMAIL=walkthrough@example.com
unset GITHUB_ACTIONS GITHUB_TOKEN GH_TOKEN GITHUB_API_URL GITHUB_REPOSITORY RELEASE_CI_SOURCE GRAIN_API_URL GRAIN_API_TOKEN

now=$(date +%s)
iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%SZ; }
rel_tip=$(git -C "$repo" rev-parse origin/release/v3.2.0)
main_tip=$(git -C "$repo" rev-parse origin/main)

printf 'echo "$*" >> %q\n' "$out/images.log" >"$out/images.sh"
chmod +x "$out/images.sh"

# tests and release green on release/v3.2.0's tip and main's, in grain CI's shape.
{
	printf '{"enabled":true,"runs":['
	first=1
	for sha in "$rel_tip" "$main_tip"; do
		for wf in tests release nightly; do
			[ $first = 1 ] || printf ','
			first=0
			printf '{"id":"%s-%s","commit":"%s","ref":"main","startedAt":"%s","finishedAt":"%s","conclusion":"success","jobs":[{"workflowPath":".grain/workflows/%s.yml","jobId":"%s","conclusion":"success","finishedAt":"%s"}]}' \
				"$sha" "$wf" "$sha" "$(iso $((now - 7200)))" "$(iso $((now - 3600)))" "$wf" "$wf" "$(iso $((now - 3600)))"
		done
	done
	printf ']}'
} >"$out/runs.json"

# Stage 1: every nightly.yml job green on the rc's commit.
jobs=""
for id in go-long staterepo-soak loadtest scalability merge-stress long-term-test long-sim postgres-stress \
	tunnel-stress ui-e2e-long installer-variants vulns cmd-grain-stress upgrade-test; do
	jobs+="${jobs:+,}{\"workflowPath\":\".github/workflows/nightly.yml\",\"jobId\":\"$id\",\"conclusion\":\"success\"}"
done
printf '{"enabled":true,"runs":[{"id":"n1","commit":"%s","ref":"v3.2.0-rc.3","startedAt":"%s","jobs":[%s]}]}' \
	"$rel_tip" "$(iso $((now - 7200)))" "$jobs" >"$out/qual-runs.json"
echo '{"check_runs":[]}' >"$out/empty-checks.json"

# fresh <unset-conf> <tag to delete...>: a new scratch remote and clone.
fresh() {
	local unset_conf=$1
	shift
	rm -rf "$out/remote.git" "$out/clone"
	git init -q --bare -b main "$out/remote.git"
	git -C "$repo" push -q "$out/remote.git" "origin/main:refs/heads/main" \
		"origin/release/v3.2.0:refs/heads/release/v3.2.0" "origin/candidate:refs/heads/candidate" 'refs/tags/*:refs/tags/*'
	local t
	for t in "$@"; do git -C "$out/remote.git" tag -d "$t" >/dev/null; done
	git clone -q "$out/remote.git" "$out/clone"
	if [ "$unset_conf" = true ]; then
		sed -i '/^RELEASE_QUALIFICATION_APP=/d' "$out/clone/scripts/release.conf"
		git -C "$out/clone" commit -q -am "walkthrough: RELEASE_QUALIFICATION_APP unset in release.conf"
		git -C "$out/clone" push -q origin main
	fi
}

# tick <name> [VAR=value...]
tick() {
	local name=$1 status=0
	shift
	: >"$out/images.log"
	(cd "$out/clone" && env RELEASE_QUALIFICATION_APP= DRY_RUN=true RELEASE_NOW="$now" \
		RELEASE_RUNS_JSON="$out/runs.json" RELEASE_QUALIFICATION_RUNS_JSON="$out/qual-runs.json" \
		RELEASE_HOLD_CHECKS_JSON="$out/empty-checks.json" RELEASE_QUALIFICATION_CHECKS_JSON="$out/empty-checks.json" \
		RELEASE_IMAGES="$out/images.sh" RELEASE_NOTES_TASK=off "$@" \
		bash "${TRAIN_SCRIPT:-$out/clone/scripts/release-from-trunk.sh}") >"$out/tick-$name.log" 2>&1 || status=$?
	echo "exit=$status images=[$(paste -sd'|' "$out/images.log")] remote-tags-after=[$(git -C "$out/remote.git" tag -l 'v3.2.0*' | paste -sd' ')]" >>"$out/tick-$name.log"
	printf '%-14s exit=%d  %s\n' "$name" "$status" "$(grep -E 'phase [12]:|Qualification:|SKIPPED|RELEASE_QUALIFICATION_APP|CHECK_QUALIFICATION_APP|dry run|DRY_RUN|would' "$out/tick-$name.log" | tr '\n' ' ' | cut -c1-900)"
}

fresh false v3.2.0-rc.3 v3.2.0-pending
tick cut-none
fresh true v3.2.0-rc.3 v3.2.0-pending
tick cut-unset
fresh false v3.2.0-pending
tick qualify-none
fresh true v3.2.0-pending
tick qualify-unset
fresh false
tick now-none
