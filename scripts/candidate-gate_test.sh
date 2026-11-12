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

# Unit tests for scripts/candidate-gate.sh's select-lkg and check-soak,
# against a throwaway repository and JSON shaped the way grain's API
# answers (GET /api/ci/runs, /api/persistent-sandboxes, /api/tasks).
# tests/deploy/release_trunk_scripts_test.go runs this in CI; by hand:
#
#   bash scripts/candidate-gate_test.sh
#
# Exits non-zero, naming each failing case, if any case fails.
set -euo pipefail

gate="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/candidate-gate.sh"
work="$(mktemp -d)"
server_pid=""
cleanup() {
  [ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.com
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null

failures=0
cases=0
out=""
status=0

# run <args...>: the gate's output in $out and its exit status in $status.
run() {
  status=0
  out="$(cd "$work/repo" && bash "$gate" "$@" 2>&1)" || status=$?
}

check() { # check <case> <want-status> <want-substring>...
  local name="$1" want="$2" s
  shift 2
  cases=$((cases + 1))
  if [ "$status" != "$want" ]; then
    echo "FAIL $name: exit $status, want $want"
    printf '%s\n' "$out" | sed 's/^/    /'
    failures=$((failures + 1))
    return
  fi
  for s in "$@"; do
    if [[ "$out" != *"$s"* ]]; then
      echo "FAIL $name: output lacks \"$s\""
      printf '%s\n' "$out" | sed 's/^/    /'
      failures=$((failures + 1))
      return
    fi
  done
  echo "ok   $name"
}

lacks() { # lacks <case> <substring>
  cases=$((cases + 1))
  if [[ "$out" == *"$2"* ]]; then
    echo "FAIL $1: output has \"$2\""
    printf '%s\n' "$out" | sed 's/^/    /'
    failures=$((failures + 1))
    return
  fi
  echo "ok   $1"
}

iso() { date -u -d "@$1" +%Y-%m-%dT%H:%M:%S.123456789Z; }

# The repository: main's first-parent line c1..c5, each commit a merge's
# result, with c0 the floor (the previous rc).
git init -q -b main "$work/repo"
cd "$work/repo"
c=()
for i in 0 1 2 3 4 5; do
  git commit -q --allow-empty -m "c$i"
  c[i]="$(git rev-parse HEAD)"
done
c0="${c[0]}" c2="${c[2]}" c3="${c[3]}" c4="${c[4]}" c5="${c[5]}"
git update-ref refs/remotes/origin/main "$c5"
cd - >/dev/null

now=1790000000
recent=$((now - 3600))

# run_json <commit> <workflow> <conclusion> [<started-epoch>] [<job>]: one
# run with one job.
run_json() {
  local t="${4:-$recent}"
  printf '{"id":"r-%s-%s-%s","repo":"o/r","ref":"main","commit":"%s","event":"push","startedAt":"%s","finishedAt":"%s","conclusion":"%s","jobs":[{"workflow":"%s","workflowPath":".grain/workflows/%s.yml","jobId":"%s","name":"%s","startedAt":"%s","finishedAt":"%s","conclusion":"%s"}]}' \
    "$1" "$2" "$t" "$1" "$(iso "$t")" "$(iso $((t + 600)))" "$3" "$2" "$2" "${5:-$2-job}" "${5:-$2-job}" "$(iso "$t")" "$(iso $((t + 600)))" "$3"
}

runs_file() { # runs_file <name> <run-json>...: a GET /api/ci/runs answer
  local f="$work/$1.json" first=1 r
  {
    printf '{"enabled":true,"runs":['
    for r in "${@:2}"; do
      [ $first = 1 ] || printf ','
      first=0
      printf '%s' "$r"
    done
    printf ']}'
  } > "$f"
  echo "$f"
}

green() { # both required suites, and a nightly.yml, green on $1
  run_json "$1" tests success "${2:-$recent}"
  echo
  run_json "$1" release success "${2:-$recent}"
  echo
  run_json "$1" nightly success "${2:-$recent}"
}

echo '[]' > "$work/no-tasks.json"

# --- select-lkg ---

mapfile -t r < <(green "$c3")
f="$(runs_file exact "${r[@]}" "$(run_json "$c5" tests success)" "$(run_json "$c5" release failure)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: the newest commit with its required suites green on its own sha" 0 "LKG $c3" "${c5:0:12}: tests.yml success release.yml failure; not known-good."

f="$(runs_file floor "${r[@]}")"
run select-lkg --runs-json "$f" --floor-sha "$c3" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: nothing at or before the floor qualifies" 1 "LKG none"

f="$(runs_file nonightly "$(run_json "$c2" nightly failure)" "$(run_json "$c4" tests success)" "$(run_json "$c4" release success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0"
check "select-lkg: tests.yml and release.yml alone qualify; nightly.yml is Stage 1, after the cut" 0 "LKG $c4" \
  "${c4:0:12}: tests.yml success release.yml success; known-good."
lacks "select-lkg: nightly.yml is not read" "nightly"

f="$(runs_file recheck "$(run_json "$c4" tests failure "$((recent - 900))" go)" "$(run_json "$c4" tests success "$recent" go)" \
  "$(run_json "$c4" release success)" "$(run_json "$c4" nightly cancelled "$((recent - 900))")" "$(run_json "$c4" nightly success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: each job's latest result wins, so a recheck turns a suite green" 0 "LKG $c4"

f="$(runs_file infra "$(run_json "$c5" tests success)" "$(run_json "$c5" release cancelled)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: a cancelled required suite is infra, reported for a re-run" 1 "LKG none" "infra: release.yml at ${c5:0:12} was cancelled or errored"

mapfile -t r < <(green "$c5")
f="$(runs_file advisory "${r[@]}" "$(run_json "$c5" gcp-smoke failure)" "$(run_json "$c4" live-agent errored)" "$(run_json "$c5" post-reset-e2e failure)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: advisory suites are reported and never block" 0 "LKG $c5" \
  "advisory gcp-smoke.yml: failure at ${c5:0:12} (does not block)" \
  "advisory live-agent.yml: infra at ${c4:0:12} (does not block)" \
  "advisory post-reset-e2e.yml: failure at ${c5:0:12}"

# The live real-model suites' freshness policy: warns, never blocks.
lacks "select-lkg: a live suite with a verdict is not reported as having none" "WARNING live live-agent.yml: no verdict"
check "select-lkg: a live suite that only errored is warned about" 0 "LKG $c5" \
  "WARNING live live-agent.yml: newest is infra at ${c4:0:12} and it has never passed here (does not block)." \
  "WARNING live live-frameworks.yml: no verdict on any commit of this ref"

old_pass=$((now - 5 * 86400))
f="$(runs_file live-fresh "${r[@]}" "$(run_json "$c3" live-agent success "$old_pass")" "$(run_json "$c4" live-agent failure)" \
  "$(run_json "$c3" live-frameworks success "$old_pass")" "$(run_json "$c5" live-frameworks success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: a live suite whose newest run is red names its newest pass, and a fresh pass is reported" 0 "LKG $c5" \
  "WARNING live live-agent.yml: newest is failure at ${c4:0:12}; newest pass at ${c3:0:12}, 119h old (does not block)." \
  "live live-frameworks.yml: passed at ${c5:0:12}, 0h old."

f="$(runs_file live-stale "${r[@]}" "$(run_json "$c3" live-agent success "$old_pass")" "$(run_json "$c5" live-frameworks success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: a live suite with no pass in CANDIDATE_GATE_LIVE_MAX_AGE_DAYS is warned about" 0 "LKG $c5" \
  "WARNING live live-agent.yml: newest pass at ${c3:0:12} is 119h old, past 3d -- is its schedule still firing? (does not block)."

f="$(runs_file pending "$(run_json "$c5" tests "")" "$(run_json "$c5" release success)" "$(run_json "$c5" nightly success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg: a suite still running is not green" 1 "tests.yml pending"

echo '{"enabled":true,"runs":[]}' > "$work/empty.json"
run select-lkg --runs-json "$work/empty.json" --now "$now"
check "select-lkg: no runs, no candidate" 1 "LKG none" "tests.yml missing"

run select-lkg
check "select-lkg: --runs-json is required" 64 "usage:"

# gh_run <commit> <workflow> <conclusion|in_progress> [<started-epoch>] [<event>] [<head-repo>]:
# one GitHub Actions workflow run, as GET /repos/{owner}/{repo}/actions/runs lists it.
gh_run() {
  local t="${4:-$recent}" status=completed conclusion="$3"
  if [ "$3" = in_progress ]; then status=in_progress conclusion=null; else conclusion="\"$3\""; fi
  printf '{"id":%s,"name":"%s","path":".github/workflows/%s.yml","head_branch":"main","head_sha":"%s","event":"%s","status":"%s","conclusion":%s,"run_attempt":1,"created_at":"%s","run_started_at":"%s","updated_at":"%s","repository":{"full_name":"o/r"},"head_repository":{"full_name":"%s"}}' \
    "$RANDOM" "$2" "$2" "$1" "${5:-push}" "$status" "$conclusion" "$(date -u -d "@$t" +%Y-%m-%dT%H:%M:%SZ)" "$(date -u -d "@$t" +%Y-%m-%dT%H:%M:%SZ)" "$(date -u -d "@$((t + 600))" +%Y-%m-%dT%H:%M:%SZ)" "${6:-o/r}"
}
gh_file() { # gh_file <name> <run-json>...: a GET .../actions/runs answer
  local f="$work/$1.json" IFS=,
  printf '{"total_count":%s,"workflow_runs":[%s]}' "$(($# - 1))" "${*:2}" > "$f"
  echo "$f"
}

f="$(gh_file gh-exact "$(gh_run "$c3" tests success)" "$(gh_run "$c3" release success)" "$(gh_run "$c3" nightly success "$recent" schedule)" \
  "$(gh_run "$c5" tests success)" "$(gh_run "$c5" release failure)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg (GitHub Actions): workflow runs are read by their workflow file" 0 "LKG $c3" "${c5:0:12}: tests.yml success release.yml failure; not known-good."

f="$(gh_file gh-pr "$(gh_run "$c5" tests success "$recent" pull_request)" "$(gh_run "$c5" release success)" "$(gh_run "$c5" nightly success)" \
  "$(gh_run "$c4" tests success "$recent" push someone/fork)" "$(gh_run "$c4" release success)" "$(gh_run "$c4" nightly success)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg (GitHub Actions): a pull request's or a fork's run vouches for nothing" 1 "LKG none" \
  "${c5:0:12}: tests.yml missing" "${c4:0:12}: tests.yml missing"

f="$(gh_file gh-rerun "$(gh_run "$c4" tests failure "$((recent - 900))")" "$(gh_run "$c4" tests success)" "$(gh_run "$c4" release success)" \
  "$(gh_run "$c5" tests in_progress)" "$(gh_run "$c5" release timed_out)")"
run select-lkg --runs-json "$f" --floor-sha "$c0" --now "$now" --tasks-json "$work/no-tasks.json"
check "select-lkg (GitHub Actions): the newest run wins; in progress is pending, timed out is infra" 0 "LKG $c4" \
  "${c5:0:12}: tests.yml pending" "infra: release.yml at ${c5:0:12} was cancelled or errored"

# --- check-qualification ---

rc="$c3"
tag=v3.1.0-rc.1
# Stage 1's jobs as GitHub names them: nightly.yml's job names, go-long
# and ui-e2e-long as matrix shards.
stage1_names=("go, shuffled and repeated (1)" "go, shuffled and repeated (2)" "state repository soak" "sustained load"
  "metadata and model scalability" "merge liveness stress" "end-to-end long term tests" "long randomized cluster simulation"
  "postgres concurrency and stress" "reverse tunnel multiplexer stress" "ui e2e, repeated (1)" "ui e2e, repeated (2)"
  "installer trixie and hostmode" "govulncheck" "release image vulnerabilities" "pkg/cli stress under load" "upgrade from previous versions"
  "real KVM sandboxes" "twenty two-VM boots" "real kubernetes")
stage1_ids=(go-long staterepo-soak loadtest scalability merge-stress long-term-test long-sim postgres-stress
  tunnel-stress ui-e2e-long installer-variants vulns image-vulns cmd-grain-stress upgrade-test kvm-sandboxes kvm-route-stress real-kubernetes)

# gh_job <name> <conclusion|in_progress> [<run-id>] [<attempt>] [<created-epoch>] [<sha>]: one
# job, as GET /repos/{owner}/{repo}/actions/runs/{id}/jobs?filter=all lists it.
gh_job() {
  local status=completed conclusion="\"$2\""
  if [ "$2" = in_progress ]; then status=in_progress conclusion=null; fi
  printf '{"id":%s,"run_id":%s,"run_attempt":%s,"workflow_name":"nightly","head_sha":"%s","name":"%s","status":"%s","conclusion":%s,"created_at":"%s","started_at":"%s"}' \
    "$RANDOM" "${3:-100}" "${4:-1}" "${6:-$rc}" "$1" "$status" "$conclusion" "$(iso "${5:-$recent}")" "$(iso "${5:-$recent}")"
}
# stage1_run [<conclusion> [<run-id> [<attempt> [<created-epoch>]]]] [<name>=<conclusion>...]:
# every Stage 1 job in one run, each <conclusion> (success) unless named.
stage1_run() {
  local concl="${1:-success}" run="${2:-100}" attempt="${3:-1}" t="${4:-$recent}" name want o
  shift $(($# < 4 ? $# : 4))
  for name in "${stage1_names[@]}" "stage 1 qualification verdict"; do
    want="$concl"
    for o in "$@"; do
      [ "${o%=*}" != "$name" ] || want="${o#*=}"
    done
    gh_job "$name" "$want" "$run" "$attempt" "$t"
    echo
  done
}
jobs_file() { # jobs_file <name> <job-json>...: the jobs of every nightly.yml run on the RC
  local f="$work/$1.json" IFS=,
  printf '{"total_count":%s,"jobs":[%s]}' "$(($# - 1))" "${*:2}" > "$f"
  echo "$f"
}
# checks_file <name> [[<app>:]<conclusion|in_progress>...]: the
# grain/release-qualification check runs on the RC, oldest first, as GET
# .../commits/{sha}/check-runs lists them. Each is grain's own (app
# grain-test, id 42) unless <app> names another: a slug ("actions" is
# github-actions, id 15368), "noapp" for one with no app at all.
checks_file() {
  local f="$work/$1.json" i=0 c a app
  shift
  {
    printf '{"check_runs":['
    printf '{"id":1,"name":"other","head_sha":"%s","status":"completed","conclusion":"failure","started_at":"%s","app":{"id":42,"slug":"grain-test"}}' "$rc" "$(iso "$recent")"
    for c in "$@"; do
      i=$((i + 1))
      a=grain-test
      [[ "$c" != *:* ]] || { a="${c%%:*}"; c="${c#*:}"; }
      case "$a" in
        grain-test) app=',"app":{"id":42,"slug":"grain-test"}' ;;
        actions) app=',"app":{"id":15368,"slug":"github-actions"}' ;;
        noapp) app='' ;;
        *) app=",\"app\":{\"id\":7,\"slug\":\"$a\"}" ;;
      esac
      if [ "$c" = in_progress ]; then
        printf ',{"id":%s,"name":"grain/release-qualification","head_sha":"%s","status":"in_progress","conclusion":null,"started_at":"%s"%s}' "$((i + 1))" "$rc" "$(iso $((recent + i * 60)))" "$app"
      else
        printf ',{"id":%s,"name":"grain/release-qualification","head_sha":"%s","status":"completed","conclusion":"%s","started_at":"%s"%s}' "$((i + 1))" "$rc" "$c" "$(iso $((recent + i * 60)))" "$app"
      fi
    done
    printf ']}'
  } > "$f"
  echo "$f"
}
qualify() { # qualify <runs-file> [<checks-file>] [<more args>...]
  local runs="$1" checks="${2:-}"
  shift $(($# < 2 ? $# : 2))
  run check-qualification --rc-tag "$tag" --rc-sha "$rc" --runs-json "$runs" ${checks:+--checks-json "$checks"} "$@"
}
export RELEASE_NEVER_PROMOTE=""
export CHECK_QUALIFICATION_APP=grain-test

mapfile -t j < <(stage1_run)
green1="$(jobs_file q-green "${j[@]}" "$(gh_job "govulncheck" failure 90 1 "$recent" "$c2")")"
qualify "$green1" "$(checks_file q-checks-pass success)"
check "check-qualification: Stage 1 and Stage 2 green pass" 0 "PASS go-long: passed" "PASS upgrade-test: passed" \
  "PASS grain/release-qualification: passed" "Qualification: PASS $tag."
lacks "check-qualification: the stage-1 roll-up and another commit's runs are not judged" "stage 1 qualification"

qualify "$green1"
check "check-qualification: Stage 2 with no check run listing is pending" 2 \
  "PENDING grain/release-qualification: no check run listing was given" "Qualification: PENDING $tag"

qualify "$green1" "$(checks_file q-checks-none)"
check "check-qualification: Stage 2 not reported yet is pending" 2 "no grain/release-qualification check run on ${rc:0:12} yet"

qualify "$green1" "$(checks_file q-checks-running in_progress)"
check "check-qualification: Stage 2 still running is pending" 2 "PENDING grain/release-qualification: still running"

qualify "$(jobs_file q-none)" "$(checks_file q-checks-pass2 success)"
check "check-qualification: nightly.yml not run on the RC yet is pending" 2 "nightly.yml has not run on $tag" \
  "waits for Stage 1"

mapfile -t j < <(stage1_run success 100 1 "$recent" "upgrade from previous versions=in_progress")
qualify "$(jobs_file q-running "${j[@]}")" "$(checks_file q-checks-pass3 success)"
check "check-qualification: a Stage 1 job still running holds Stage 2" 2 "PENDING upgrade-test: still running" \
  "PENDING grain/release-qualification: waits for Stage 1"

mapfile -t j < <(stage1_run success 100 1 "$recent" "upgrade from previous versions=failure")
first_fail=("${j[@]}")
qualify "$(jobs_file q-flake1 "${j[@]}")" "$(checks_file q-checks-pass4 success)"
check "check-qualification: a job's first failure gets one retry" 2 \
  "PENDING upgrade-test: failed for the first time (failure); it gets one retry on $tag" \
  "RERUN nightly upgrade-test flake" "Qualification: PENDING $tag (upgrade-test)"

# The retry: nightly.yml dispatched again on the RC with jobs: upgrade-test.
mapfile -t j < <(stage1_run skipped 101 1 $((recent + 600)) "upgrade from previous versions=success")
qualify "$(jobs_file q-flake2 "${first_fail[@]}" "${j[@]}")" "$(checks_file q-checks-pass5 success)"
check "check-qualification: a pass on the retry is a flake, and the RC qualifies" 0 \
  "PASS upgrade-test: passed on its one retry (failure,success): a flake" "Qualification: PASS $tag."

mapfile -t j < <(stage1_run skipped 101 1 $((recent + 600)) "upgrade from previous versions=failure")
qualify "$(jobs_file q-red "${first_fail[@]}" "${j[@]}")" "$(checks_file q-checks-pass6 success)"
check "check-qualification: a failure on the retry is confirmed red" 1 \
  "FAIL upgrade-test: failed again on its retry (failure,failure): confirmed red" "Qualification: FAIL $tag (upgrade-test)"
lacks "check-qualification: a red RC asks for no re-run" "RERUN"

# rerun-failed-jobs: the same run, attempt 2.
qualify "$(jobs_file q-red2 "${first_fail[@]}" "$(gh_job "upgrade from previous versions" failure 100 2 $((recent + 600)))")"
check "check-qualification: a failed re-run attempt of the same run is the retry" 1 "confirmed red"

mapfile -t j < <(stage1_run success 100 1 "$recent" "sustained load=cancelled")
qualify "$(jobs_file q-infra "${j[@]}")" "$(checks_file q-checks-pass7 success)"
check "check-qualification: a cancelled job is infra, re-run and never red" 3 \
  "INFRA loadtest: cancelled, timed out or starved of capacity (infra); re-run, not red" \
  "RERUN nightly loadtest infra" "Qualification: INFRA $tag (loadtest)"
lacks "check-qualification: infra is not a failure" "FAIL"

qualify "$(jobs_file q-infra-retry "${first_fail[@]}" "$(gh_job "upgrade from previous versions" timed_out 101 1 $((recent + 600)))")"
check "check-qualification: an infra retry does not use up the flake retry" 3 "INFRA upgrade-test" "RERUN nightly upgrade-test infra"

qualify "$(jobs_file q-infra-flake "${first_fail[@]}" "$(gh_job "upgrade from previous versions" timed_out 101 1 $((recent + 600)))" \
  "$(gh_job "upgrade from previous versions" success 102 1 $((recent + 1200)))")" "$(checks_file q-checks-pass8 success)"
check "check-qualification: infra between the failure and its passing retry is still a flake" 0 \
  "passed on its one retry (failure,infra,success): a flake"

mapfile -t j < <(stage1_run success 100 1 "$recent" "govulncheck=startup_failure")
qualify "$(jobs_file q-stall "${j[@]}" "$(gh_job govulncheck cancelled 101 1 $((recent + 600)))" "$(gh_job govulncheck timed_out 102 1 $((recent + 1200)))")"
check "check-qualification: three infra attempts in a row are stalled, still not red" 3 "stalled after 3 infra attempts in a row"

mapfile -t j < <(stage1_run success 100 1 "$recent" "go, shuffled and repeated (2)=failure")
qualify "$(jobs_file q-shard "${j[@]}")"
check "check-qualification: a matrix shard's failure is its job's" 2 "RERUN nightly go-long flake"

# v3.6.0-rc.12: grant-matrix-ui is in nightly.yml but not Stage 1's table.
# Its failure once asked for a retry by display name, which ran nothing.
mapfile -t j < <(stage1_run success 100 1 "$recent")
qualify "$(jobs_file q-advisory "${j[@]}" "$(gh_job "grant matrix through the UI" failure 100 1 "$recent")" \
  "$(gh_job "grant matrix through the UI" failure 101 1 $((recent + 600)))")" "$(checks_file q-checks-advisory success)"
check "check-qualification: a job outside Stage 1's table carries no verdict" 0 \
  "ADVISORY grant matrix through the UI: not a Stage 1 job, so no verdict and no retry (failure,failure)" "Qualification: PASS $tag."
lacks "check-qualification: a job outside Stage 1's table is not re-run" "RERUN"

mapfile -t j < <(stage1_run success 100 1 "$recent" "govulncheck=failure" "sustained load=failure")
qualify "$(jobs_file q-two "${j[@]}")"
check "check-qualification: two jobs may each use a flake retry" 2 "RERUN nightly loadtest flake" "RERUN nightly vulns flake"

mapfile -t j < <(stage1_run success 100 1 "$recent" "govulncheck=failure" "sustained load=failure" "merge liveness stress=failure")
qualify "$(jobs_file q-systemic "${j[@]}")"
check "check-qualification: three independent red jobs are systemic, with no retry" 1 \
  "FAIL systemic: 3 jobs failed on $tag" "Qualification: FAIL $tag"

mapfile -t j < <(stage1_run success 100 1 "$recent" "govulncheck=skipped")
qualify "$(jobs_file q-skipped "${j[@]}")" "$(checks_file q-checks-pass9 success)"
check "check-qualification: a required job that was skipped is not a pass" 1 "FAIL vulns: never ran (skipped)"

mapfile -t j < <(stage1_run success | grep -v '"govulncheck"')
qualify "$(jobs_file q-missing "${j[@]}")" "$(checks_file q-checks-pass10 success)"
check "check-qualification: a required job missing from a finished run fails" 1 "FAIL vulns: did not run on $tag"
mapfile -t j < <(stage1_run success 100 1 "$recent" "upgrade from previous versions=skipped")
qualify "$(jobs_file q-missing2 "${j[@]}" "$(gh_job "an optional extra" skipped)")" "$(checks_file q-checks-pass11 success)"
check "check-qualification: an optional job that was skipped is ignored, a required one is not" 1 \
  "FAIL upgrade-test: never ran (skipped)"
lacks "check-qualification: the skipped optional job is not judged" "optional extra"

qualify "$green1" "$(checks_file q-c-flake failure)"
# grain concludes grain/release-qualification failure only once an item
# has failed its own retry, and never re-runs a completed candidate: its
# first failure is confirmed red, not a flake to wait on
# (docs/test-runs/2026-11-04-release-train.md, finding 1).
check "check-qualification: Stage 2's failure is confirmed red at once" 1 \
  "FAIL grain/release-qualification: failed (failure): grain concludes failure only once an item has failed its own retry" \
  "Qualification: FAIL"
lacks "check-qualification: Stage 2's failure asks for no re-run" "RERUN qualification"
qualify "$green1" "$(checks_file q-c-flake2 failure success)"
check "check-qualification: Stage 2's newest attempt is its verdict" 0 "PASS grain/release-qualification: passed (failure,success)" "Qualification: PASS"
qualify "$green1" "$(checks_file q-c-red failure failure)"
check "check-qualification: Stage 2 failing twice is red" 1 "FAIL grain/release-qualification: failed (failure,failure)"
qualify "$green1" "$(checks_file q-c-infra neutral)"
check "check-qualification: a neutral (INFRA) Stage 2 verdict is re-run" 3 \
  "stalled after Stage 2's infra re-runs" \
  "RERUN qualification grain/release-qualification infra" "Qualification: INFRA"
qualify "$green1" "$(checks_file q-c-infra-retry neutral in_progress)"
check "check-qualification: a fresh Stage 2 attempt in progress after neutral is pending" 2 \
  "PENDING grain/release-qualification: still running (infra,pending)" "Qualification: PENDING"
lacks "check-qualification: a running Stage 2 retry is neither stalled nor asking for a re-run" "RERUN qualification"
qualify "$green1" "$(checks_file q-c-infra-pass neutral success)"
check "check-qualification: a fresh Stage 2 attempt passing after neutral qualifies the RC" 0 \
  "PASS grain/release-qualification: passed (infra,success)" "Qualification: PASS"
qualify "$green1" "$(checks_file q-c-infra-fail neutral failure)"
check "check-qualification: a fresh Stage 2 attempt failing after neutral is confirmed red" 1 \
  "FAIL grain/release-qualification: failed (infra,failure)" "Qualification: FAIL"
qualify "$green1" "$(checks_file q-c-prior-red-pending failure failure in_progress)"
check "check-qualification: Stage 2's newest attempt is pending even after earlier check runs on the commit failed" 2 \
  "PENDING grain/release-qualification: still running (failure,failure,pending)" "Qualification: PENDING"

# Only grain's own app's check run qualifies: a same-named one from an
# Actions workflow (checks: write), another app, or no app at all is
# ignored, whatever it concludes.
qualify "$green1" "$(checks_file q-c-foreign actions:success)"
check "check-qualification: a same-named check run from GitHub Actions does not qualify" 2 \
  "IGNORED grain/release-qualification: 1 from github-actions, not grain-test" \
  "no grain/release-qualification check run on ${rc:0:12} yet" "Qualification: PENDING"
lacks "check-qualification: the foreign check run is not judged" "PASS grain/release-qualification"
qualify "$green1" "$(checks_file q-c-foreign2 in_progress actions:success intruder:success noapp:success)"
check "check-qualification: foreign successes do not override grain's own pending run" 2 \
  "IGNORED grain/release-qualification: 3 from github-actions, intruder, no app" \
  "PENDING grain/release-qualification: still running"
qualify "$green1" "$(checks_file q-c-foreign3 failure actions:success failure)"
check "check-qualification: a foreign success between grain's failures is no retry pass" 1 \
  "FAIL grain/release-qualification: failed (failure,failure)"
CHECK_QUALIFICATION_APP=42 qualify "$green1" "$(checks_file q-c-appid actions:failure success)"
check "check-qualification: CHECK_QUALIFICATION_APP may name the app by id" 0 \
  "PASS grain/release-qualification: passed" "Qualification: PASS"
CHECK_QUALIFICATION_APP=42 qualify "$green1" "$(checks_file q-c-numslug 42:success)"
check "check-qualification: an app id is never matched against another app's slug" 2 \
  "IGNORED grain/release-qualification: 1 from 42, not 42" "Qualification: PENDING"
# With no app configured (empty or unset) no check run is trusted, and the
# gate fails closed rather than qualify on Stage 1 alone.
CHECK_QUALIFICATION_APP='' qualify "$green1" "$(checks_file q-c-noapp success)"
check "check-qualification: with no app configured no check run is trusted" 64 "CHECK_QUALIFICATION_APP is unset"
lacks "check-qualification: an unconfigured gate never passes" "Qualification: PASS"
lacks "check-qualification: an unconfigured gate never skips Stage 2" "SKIPPED grain/release-qualification"
unset CHECK_QUALIFICATION_APP
qualify "$green1" "$(checks_file q-c-unset success)"
export CHECK_QUALIFICATION_APP=grain-test
check "check-qualification: an unset CHECK_QUALIFICATION_APP trusts no check run" 64 "CHECK_QUALIFICATION_APP is unset"
lacks "check-qualification: an unset gate never passes" "Qualification: PASS"
# With CHECK_QUALIFICATION_APP=none, set explicitly (a forge connection that
# is no GitHub App posts no check run), Stage 2 is skipped and no check run --
# grain's-looking, foreign or app-less -- is read: Stage 1 qualifies alone.
CHECK_QUALIFICATION_APP=none qualify "$green1" "$(checks_file q-c-none failure actions:success noapp:success)"
check "check-qualification: CHECK_QUALIFICATION_APP=none skips Stage 2" 0 \
  "SKIPPED grain/release-qualification: CHECK_QUALIFICATION_APP=none (qualified by Stage 1 nightly.yml)" "Qualification: PASS $tag."
lacks "check-qualification: CHECK_QUALIFICATION_APP=none judges no check run" "grain/release-qualification: failed"
CHECK_QUALIFICATION_APP=none qualify "$green1"
check "check-qualification: CHECK_QUALIFICATION_APP=none needs no check run listing" 0 \
  "SKIPPED grain/release-qualification: CHECK_QUALIFICATION_APP=none" "Qualification: PASS $tag."
mapfile -t red < <(stage1_run success 100 1 "$recent" "state repository soak=failure")
CHECK_QUALIFICATION_APP=none qualify "$(jobs_file q-none-red "${red[@]}" "$(gh_job "state repository soak" failure 101 1 $((recent + 600)))")"
check "check-qualification: CHECK_QUALIFICATION_APP=none still needs Stage 1" 1 "Qualification: FAIL"

RELEASE_NEVER_PROMOTE="v1.14.0 v3.1.0" qualify "$green1" "$(checks_file q-never success)"
check "check-qualification: RELEASE_NEVER_PROMOTE fails an otherwise green RC" 1 "FAIL never: RELEASE_NEVER_PROMOTE (v1.14.0 v3.1.0) names v3.1.0"

# grain CI: each nightly run at the RC is one attempt of its jobs.
grain_nightly() { # grain_nightly <commit> <started-epoch> <id>=<conclusion>...: one nightly run
  local first=1 o
  printf '{"id":"n-%s","repo":"o/r","ref":"%s","commit":"%s","startedAt":"%s","jobs":[' "$2" "$tag" "$1" "$(iso "$2")"
  for o in "${@:3}"; do
    [ $first = 1 ] || printf ','
    first=0
    printf '{"workflowPath":".grain/workflows/nightly.yml","jobId":"%s","conclusion":"%s"}' "${o%=*}" "${o#*=}"
  done
  printf ']}'
}
all_ids=()
for id in "${stage1_ids[@]}"; do all_ids+=("$id=success"); done
f="$(runs_file q-grain "$(grain_nightly "$rc" "$recent" "${all_ids[@]/#upgrade-test=success/upgrade-test=failure}")" \
  "$(grain_nightly "$rc" $((recent + 600)) upgrade-test=success)" "$(grain_nightly "$c2" "$recent" vulns=failure)" \
  "$(run_json "$rc" tests failure)")"
qualify "$f" "$(checks_file q-checks-grain success)"
check "check-qualification (grain CI): runs at the RC are attempts; other commits and workflows are ignored" 0 \
  "PASS upgrade-test: passed on its one retry (failure,success): a flake" "Qualification: PASS $tag."

f="$(runs_file q-grain-infra "$(grain_nightly "$rc" "$recent" "${all_ids[@]/#vulns=success/vulns=errored}")")"
qualify "$f"
check "check-qualification (grain CI): an errored job is infra" 3 "RERUN nightly vulns infra"

run check-qualification --rc-tag v3.1.0 --rc-sha "$rc" --runs-json "$green1"
check "check-qualification: a tag that is not an rc is a usage error" 64 "not a release candidate tag"
run check-qualification --rc-tag v3.1.2-rc.1 --rc-sha "$rc" --runs-json "$green1"
check "check-qualification: a patch rc is an rc" 2 "Qualification: PENDING v3.1.2-rc.1"
run check-qualification --rc-tag "$tag" --rc-sha "$rc"
check "check-qualification: --runs-json is required" 64 "usage:"
run check-soak --rc-tag "$tag" --rc-sha "$rc"
check "check-soak is gone" 64 "usage:"

echo
echo "$((cases - failures))/$cases passed"
[ "$failures" -eq 0 ]
