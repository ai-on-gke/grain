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

# The release train's two gates (design/playbook/releases.playbook.md,
# "Lifecycle and state machine" and sections 2-4): which main commit is
# known-good enough to cut as the next release candidate, and whether the
# candidate in qualification has passed. scripts/release-from-trunk.sh
# calls both; each reads JSON the train fetched (from GitHub Actions, or
# from grain CI through the release-gate-read capability, the
# api:release-gate-read preset, pkg/capability/ciapi) and decides nothing
# it was not handed.
#
#   candidate-gate.sh select-lkg --runs-json FILE [--floor-sha SHA]
#       [--ref REF] [--max-commits N]
#
#     Stage 0: the newest commit on REF's first-parent history (default
#     origin/main), strictly after --floor-sha, whose required suites are
#     green: tests.yml and release.yml (CANDIDATE_GATE_REQUIRED).
#     nightly.yml is not among them: it is Stage 1 of qualification, run on
#     the release candidate after it is cut (check-qualification). FILE is
#     GET /api/ci/runs' answer (or a bare array of its runs), or -- for a
#     repository whose gate is GitHub Actions (ci_mode: github) -- GitHub's
#     answer to GET /repos/{owner}/{repo}/actions/runs (an object with
#     workflow_runs), each workflow run of which counts as one job of the
#     workflow its path names (.github/workflows/tests.yml is tests); only
#     push, schedule and workflow_dispatch runs of the repository itself
#     count, never a pull request's. A suite's verdict at a commit is the
#     latest result of each of its jobs across every run at that commit --
#     the same combination the merge gate reads, so a recheck that turned
#     one job green turns the suite green -- and is success (every job
#     passed or was skipped), failure, infra (cancelled or errored, and
#     nothing failed: a re-run's business, not a regression), pending or
#     missing.
#
#     The live-provider suites (CANDIDATE_GATE_ADVISORY: gcp-smoke and
#     live-*, post-reset-e2e) are reported and never block. Every
#     required suite found infra is reported as needing a re-run.
#     --tasks-json and --now are accepted and ignored, for callers from
#     before nightly.yml left LKG selection.
#
#     Prints one line per commit it looked at and a last line
#     "LKG <sha>", exiting 0; or "LKG none", exiting 1.
#
#   candidate-gate.sh check-qualification --rc-tag TAG --rc-sha SHA
#       --runs-json FILE [--checks-json FILE]
#
#     Whether release candidate TAG (vX.Y.Z-rc.N) on SHA has qualified:
#       Stage 1   every job of nightly.yml run on SHA. FILE is GET
#                 /api/ci/runs' answer for the RC (grain CI: each run at SHA
#                 is one attempt of the jobs in it), or GitHub's
#                 GET /repos/{owner}/{repo}/actions/runs/{id}/jobs?filter=all
#                 answer (an object with jobs, every attempt of every
#                 nightly.yml run at SHA concatenated into it). A job is
#                 known by its id (go-long), its name ("go, shuffled and
#                 repeated") or a matrix shard of it ("... (1)"); its shards
#                 are one job. Each of CHECK_QUALIFICATION_JOBS must succeed
#                 (skipped is not success); other jobs are judged when they
#                 ran and otherwise ignored, as are
#                 CHECK_QUALIFICATION_IGNORE (the stage-1 roll-up);
#       Stage 2   the grain/release-qualification check run on SHA
#                 (CHECK_QUALIFICATION_CHECK), from --checks-json, GitHub's
#                 GET /repos/{owner}/{repo}/commits/{sha}/check-runs answer
#                 (or a bare array of check runs). Only a check run the
#                 GitHub App CHECK_QUALIFICATION_APP names (its slug or
#                 numeric id: grain's own forge connection) created
#                 counts; one of the same name from any other app -- an
#                 Actions workflow with checks: write, another installed
#                 app -- is reported as IGNORED and never judged. With
#                 CHECK_QUALIFICATION_APP "none", set explicitly (a
#                 deployment whose forge connection is no GitHub App, and
#                 so posts no check run) Stage 2 is SKIPPED and no check
#                 run is read at all: Stage 1 alone qualifies the RC.
#                 With CHECK_QUALIFICATION_APP empty or unset no check run
#                 is trusted at all (exit 64): an unconfigured gate never
#                 passes. Otherwise only judged once Stage 1 passes;
#                 without --checks-json it is PENDING;
#       never     TAG, and the release it would become, are not in
#                 RELEASE_NEVER_PROMOTE (scripts/release.conf).
#     Every job (and the check run) is judged on its attempts in order:
#       infra   a cancelled, timed-out, stale, never-started or neutral
#               attempt is infrastructure, never red: it is re-run, and
#               counts against neither the flake retry nor the RC
#               (playbook section 4). CHECK_QUALIFICATION_INFRA_STALL (3)
#               infra attempts in a row are reported as stalled;
#       flake   the first failure gets exactly one retry on the same RC
#               (section 3): the job is re-run and the RC is PENDING; a
#               pass on the retry is a flake and the job passes;
#       red     a second failure is a confirmed failure, and so is any
#               failure once CHECK_QUALIFICATION_MAX_FLAKES (2) other
#               jobs have already failed once -- three independent red
#               jobs are systemic, not coincidence.
#     except the check run, whose newest attempt is its verdict: grain
#     posts failure only once an item has failed its own retry inside
#     Stage 2 (model.DecideReleaseQualification) and never re-runs a
#     completed failure, so a failure is confirmed red the first time; a
#     neutral conclusion is an infrastructure stall
#     (pipeline.release-infra-stalled), which grain re-qualifies as a new
#     check run on the same commit once the stall is acknowledged or
#     cleared.
#
#     Prints "<PASS|FAIL|PENDING|INFRA> <job>: ..." per job, a
#     "RERUN <stage> <job> <flake|infra>" line for each job the train is to
#     re-run, and a last line "Qualification: PASS|FAIL|PENDING|INFRA TAG
#     (...)", exiting 0, 1, 2 or 3. A FAIL anywhere wins, then INFRA, then
#     PENDING.
#
# Usage errors exit 64 for both, since check-qualification's 2 is PENDING.
set -euo pipefail

CANDIDATE_GATE_REQUIRED="${CANDIDATE_GATE_REQUIRED-tests release}"
CANDIDATE_GATE_ADVISORY="${CANDIDATE_GATE_ADVISORY-gcp-smoke live-* post-reset-e2e}"
# Stage 1's required nightly.yml jobs, each "id=name" (the job's key in
# the workflow and the name GitHub reports it by): the playbook's Stage 1
# table, as nightly.yml's own stage-1 job needs them.
CHECK_QUALIFICATION_JOBS="${CHECK_QUALIFICATION_JOBS-go-long=go, shuffled and repeated
staterepo-soak=state repository soak
loadtest=sustained load
scalability=metadata and model scalability
merge-stress=merge liveness stress
long-term-test=end-to-end long term tests
long-sim=long randomized cluster simulation
postgres-stress=postgres concurrency and stress
tunnel-stress=reverse tunnel multiplexer stress
ui-e2e-long=ui e2e, repeated
installer-variants=installer trixie and hostmode
vulns=govulncheck
image-vulns=release image vulnerabilities
cmd-grain-stress=pkg/cli stress under load
upgrade-test=upgrade from previous versions}"
# Jobs that carry no verdict of their own: nightly.yml's roll-up of the
# jobs above, which on a retry judges only the job retried.
CHECK_QUALIFICATION_IGNORE="${CHECK_QUALIFICATION_IGNORE-stage-1=stage 1 qualification verdict}"
CHECK_QUALIFICATION_CHECK="${CHECK_QUALIFICATION_CHECK-grain/release-qualification}"
# The GitHub App (slug or numeric id) whose CHECK_QUALIFICATION_CHECK is
# trusted: grain's own forge connection, which posts it. Anything else
# that can write check runs on the repository can post one by that name.
# "none" -- only ever as an explicit setting, never a default -- skips
# Stage 2: no check run is trusted, so none is read, and Stage 1 alone
# qualifies the RC. Empty or unset trusts nothing and fails closed.
CHECK_QUALIFICATION_APP="${CHECK_QUALIFICATION_APP-}"
CHECK_QUALIFICATION_MAX_FLAKES="${CHECK_QUALIFICATION_MAX_FLAKES:-2}"
CHECK_QUALIFICATION_INFRA_STALL="${CHECK_QUALIFICATION_INFRA_STALL:-3}"

usage() {
  cat >&2 <<'EOF'
usage: candidate-gate.sh select-lkg --runs-json FILE [--floor-sha SHA] [--ref REF] [--max-commits N]
       candidate-gate.sh check-qualification --rc-tag TAG --rc-sha SHA --runs-json FILE [--checks-json FILE]
EOF
  exit 64
}

die() {
  echo "candidate-gate.sh: $*" >&2
  exit 64
}

command -v jq >/dev/null 2>&1 || die "jq is required"

# jq's own date parsing takes neither fractional seconds nor a numeric
# offset, and Go writes both (RFC 3339 with nanoseconds, in the daemon's
# zone). epoch turns any of them, or null, into seconds.
# shellcheck disable=SC2016 # jq, not shell
JQ_EPOCH='def epoch:
  if . == null or . == "" then 0
  else sub("\\.[0-9]+"; "")
    | if test("Z$") then fromdateiso8601
      else (capture("^(?<d>.*)(?<s>[+-])(?<h>[0-9]{2}):?(?<m>[0-9]{2})$") as $c
        | ($c.d + "Z" | fromdateiso8601) - (($c.s + "1" | tonumber) * (($c.h | tonumber) * 3600 + ($c.m | tonumber) * 60)))
      end
  end;'

# GitHub Actions' workflow runs in the shape GET /api/ci/runs gives them.
# Each run is one job: GitHub re-runs a run's jobs in place, so the run's
# conclusion is already the latest of each of its jobs. A run in progress
# is pending; cancelled, stale, timed-out and never-started runs are
# infra; anything else that is not a pass is a failure.
# shellcheck disable=SC2016 # jq, not shell
JQ_GITHUB_RUNS='def github_runs:
  if type == "object" and has("workflow_runs") then
    {runs: [ .workflow_runs[]
      | select(.event == "push" or .event == "schedule" or .event == "workflow_dispatch")
      | select((.head_repository.full_name // .repository.full_name // "") == (.repository.full_name // ""))
      | (if .status == "completed" then .updated_at else null end) as $done
      | {commit: (.head_sha // ""), ref: .head_branch, startedAt: (.run_started_at // .created_at), finishedAt: $done,
         jobs: [{workflowPath: ((.path // "") | sub("@.*$"; "")), jobId: "run", finishedAt: $done,
                 conclusion: (if .status != "completed" then ""
                   else ({success: "success", skipped: "skipped", neutral: "success",
                          cancelled: "cancelled", stale: "cancelled",
                          timed_out: "errored", startup_failure: "errored"}[.conclusion // ""] // "failure") end)}]} ]}
  else . end;'

listed() { # listed <word> <space-separated globs>
  local glob
  for glob in $2; do
    # shellcheck disable=SC2053 # the list holds globs on purpose
    [[ "$1" == $glob ]] && return 0
  done
  return 1
}

# --- select-lkg -------------------------------------------------------------

select_lkg() {
  local runs="" floor="" ref="origin/main" max=200
  while [ $# -gt 0 ]; do
    case "$1" in
      --runs-json) runs="${2:-}"; shift 2 ;;
      --floor-sha) floor="${2:-}"; shift 2 ;;
      --ref) ref="${2:-}"; shift 2 ;;
      --tasks-json | --now) shift 2 ;;
      --max-commits) max="${2:-}"; shift 2 ;;
      *) usage ;;
    esac
  done
  [ -n "$runs" ] || usage
  [ -f "$runs" ] || die "--runs-json $runs is not a file"
  local head floor_sha=""
  head="$(git rev-parse --verify -q "$ref^{commit}")" || die "$ref does not resolve"
  if [ -n "$floor" ]; then
    floor_sha="$(git rev-parse --verify -q "$floor^{commit}")" || die "--floor-sha $floor does not resolve"
  fi

  # One line per (commit, workflow): commit, workflow, verdict, and when
  # the newest of its jobs finished.
  local table
  table="$(jq -r "$JQ_EPOCH$JQ_GITHUB_RUNS"'
    def wf: ((.workflowPath // "") | split("/") | last | sub("\\.ya?ml$"; "")) as $p
      | if $p != "" then $p else (.workflow // "") end;
    github_runs
    | (if type == "array" then . else (.runs // []) end)
    | [ .[] | select((.commit // "") != "") | . as $r | ($r.startedAt | epoch) as $t
        | ($r.jobs // [])[]
        | {commit: $r.commit, wf: wf, job: (.jobId // .name // ""), c: (.conclusion // ""),
           t: $t, ft: ((.finishedAt // $r.finishedAt) | epoch)} ]
    | group_by([.commit, .wf])[]
    | (group_by(.job) | map(sort_by(.t) | last)) as $latest
    | [ $latest[0].commit, $latest[0].wf,
        (if any($latest[]; .c == "") then "pending"
         elif any($latest[]; .c == "failure") then "failure"
         elif any($latest[]; .c == "cancelled" or .c == "errored") then "infra"
         else "success" end),
        ($latest | map(.ft) | max | floor | tostring) ]
    | @tsv' "$runs")" || die "--runs-json $runs is not a CI run listing"

  # Keyed by the full commit, whatever length the listing gave it in.
  declare -A verdict=()
  local c w v t full
  declare -A resolved=()
  while IFS=$'\t' read -r c w v t; do
    [ -n "$c" ] || continue
    if [ -z "${resolved[$c]+x}" ]; then
      resolved[$c]="$(git rev-parse --verify -q "$c^{commit}" 2>/dev/null || true)"
    fi
    full="${resolved[$c]}"
    [ -n "$full" ] || continue
    verdict["$full $w"]="$v"
  done <<< "$table"

  echo "Selecting the newest known-good commit on $ref${floor_sha:+ after ${floor_sha:0:12}} (required: $CANDIDATE_GATE_REQUIRED)."

  local -a commits=()
  mapfile -t commits < <(git rev-list --first-parent --max-count="$max" "$head" ${floor_sha:+"^$floor_sha"})
  local lkg="" sha short reasons wf vv ok infra=()
  for sha in "${commits[@]}"; do
    short="${sha:0:12}"
    if [ -n "$floor_sha" ] && ! git merge-base --is-ancestor "$floor_sha" "$sha"; then
      echo "  $short: not a descendant of ${floor_sha:0:12}; skipped."
      continue
    fi
    ok=yes
    reasons=()
    for wf in $CANDIDATE_GATE_REQUIRED; do
      vv="${verdict["$sha $wf"]:-missing}"
      [ "$vv" != infra ] || infra+=("$wf.yml at $short")
      reasons+=("$wf.yml $vv")
      [ "$vv" = success ] || ok=""
    done
    if [ -z "$ok" ]; then
      echo "  $short: ${reasons[*]}; not known-good."
      continue
    fi
    echo "  $short: ${reasons[*]}; known-good."
    lkg="$sha"
    break
  done

  local item
  for item in ${infra[@]+"${infra[@]}"}; do
    echo "  infra: $item was cancelled or errored -- a re-run's business, not a regression."
  done

  # The advisory suites, each at the newest commit of $ref it ran on.
  local adv
  adv="$(
    while IFS=$'\t' read -r c w v t; do
      [ -n "$c" ] || continue
      listed "$w" "$CANDIDATE_GATE_ADVISORY" || continue
      full="${resolved[$c]:-}"
      [ -n "$full" ] || continue
      git merge-base --is-ancestor "$full" "$head" 2>/dev/null || continue
      printf '%s\t%s\t%s\t%s\n' "$t" "$w" "$v" "${full:0:12}"
    done <<< "$table" | sort -t$'\t' -k2,2 -k1,1nr | awk -F'\t' '!seen[$2]++'
  )"
  while IFS=$'\t' read -r t w v c; do
    [ -n "$w" ] || continue
    echo "  advisory $w.yml: $v at $c (does not block)."
  done <<< "$adv"

  if [ -z "$lkg" ]; then
    echo "LKG none"
    return 1
  fi
  echo "LKG $lkg"
}

# --- check-qualification ----------------------------------------------------

# "id=name" lines as a JSON array of {id, name}.
job_names_json() {
  printf '%s\n' "$1" | jq -Rn '[inputs | select(test("\\S")) | capture("^\\s*(?<id>[^=]+?)\\s*=\\s*(?<name>.*?)\\s*$")]'
}

# from_app: a check run posted by the app $app names -- its id when $app
# is all digits, so an app whose slug is that number cannot pass for it,
# and its slug otherwise; sha_ok: a sha that is $rc's.
# shellcheck disable=SC2016 # jq, not shell
JQ_CHECK_RUN='
  def from_app: $app != "" and (.app | type == "object")
    and (if ($app | test("^[0-9]+$")) then ((.app.id // "") | tostring) == $app
         else (.app.slug // "") == $app end);
  def sha_ok: (. // "" | ascii_downcase) as $h | $h == "" or ($rc | startswith($h)) or ($h | startswith($rc));
'
# The attempts of every job in a listing, one line per job: stage, job
# and the class of each attempt in order (pending, success, failure,
# infra; skipped only when the job never ran at all), comma-separated.
# shellcheck disable=SC2016 # jq, not shell
JQ_ATTEMPTS="$JQ_CHECK_RUN"'
  def known($n): ($names + $ignore | map(. as $e | select($e.id == $n or $e.name == $n or ($n | startswith($e.name + " (")) or ($n | startswith($e.id + " ("))))
    | first | .id) // ($n | sub(" \\(.*\\)$"; ""));
  def wf: ((.workflowPath // "") | split("/") | last | sub("\\.ya?ml$"; "")) as $p
    | if $p != "" then $p else (.workflow // .workflow_name // "") end;
  def stage1:
    if type == "object" and has("jobs") then
      [ .jobs[] | select(.head_sha | sha_ok)
        | select((.workflow_name // "nightly") == "nightly")
        | {stage: "nightly", job: known(.name // ""),
           key: [((.created_at // .started_at) | epoch), (.run_id // 0), (.run_attempt // 1)],
           c: (if (.status // "completed") != "completed" then "pending"
               else ({success: "success", skipped: "skipped", failure: "failure", action_required: "failure",
                      cancelled: "infra", timed_out: "infra", startup_failure: "infra", stale: "infra",
                      neutral: "infra"}[.conclusion // ""] // "failure") end)} ]
    else
      [ (if type == "array" then . else (.runs // []) end)[]
        | select(.commit | sha_ok) | . as $r
        | ($r.jobs // [])[] | select(wf == "nightly")
        | {stage: "nightly", job: known(.jobId // .name // ""),
           key: [($r.startedAt | epoch), ($r.id // "" | tostring), 1],
           c: ({"": "pending", success: "success", skipped: "skipped", failure: "failure",
                cancelled: "infra", errored: "infra"}[.conclusion // ""] // "failure")} ]
    end;
  def stage2:
    [ (if type == "array" then . else (.check_runs // []) end)[]
      | select(.name == $check) | select(.head_sha | sha_ok) | select(from_app)
      | {stage: "qualification", job: .name,
         key: [((.started_at // .startedAt) | epoch), (.id // 0), 1],
         c: (if (.status // "completed") != "completed" then "pending"
             else ({success: "success", failure: "failure", action_required: "failure",
                    neutral: "infra", cancelled: "infra", timed_out: "infra", stale: "infra",
                    skipped: "infra"}[.conclusion // ""] // "failure") end)} ];
  (if $which == "nightly" then stage1 else stage2 end)
  | map(select(.job as $j | $ignore | map(.id) | index($j) | not))
  | group_by(.job)[]
  | (group_by(.key) | map(map(.c) as $cs
      | if any($cs[]; . == "pending") then "pending"
        elif any($cs[]; . == "failure") then "failure"
        elif any($cs[]; . == "infra") then "infra"
        elif any($cs[]; . == "success") then "success"
        else "skipped" end)) as $attempts
  | ($attempts | map(select(. != "skipped"))) as $ran
  | [.[0].stage, .[0].job, ((if ($ran | length) > 0 then $ran else ["skipped"] end) | join(","))]
  | @tsv'

check_qualification() {
  local tag="" rc="" runs="" checks=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --rc-tag) tag="${2:-}"; shift 2 ;;
      --rc-sha) rc="${2:-}"; shift 2 ;;
      --runs-json) runs="${2:-}"; shift 2 ;;
      --checks-json) checks="${2:-}"; shift 2 ;;
      *) usage ;;
    esac
  done
  if [ -z "$tag" ] || [ -z "$rc" ] || [ -z "$runs" ]; then
    usage
  fi
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$ ]] || die "--rc-tag $tag is not a release candidate tag (vX.Y.Z-rc.N)"
  [[ "$rc" =~ ^[0-9a-fA-F]{7,40}$ ]] || die "--rc-sha $rc is not a commit sha"
  [ -f "$runs" ] || die "--runs-json $runs is not a file"
  [ -z "$checks" ] || [ -f "$checks" ] || die "--checks-json $checks is not a file"
  rc="${rc,,}"

  local names ignore
  names="$(job_names_json "$CHECK_QUALIFICATION_JOBS")" || die "CHECK_QUALIFICATION_JOBS is not id=name lines"
  ignore="$(job_names_json "$CHECK_QUALIFICATION_IGNORE")" || die "CHECK_QUALIFICATION_IGNORE is not id=name lines"

  local failed=() infra=() pending=() reruns=() flaked=()
  pass() { echo "PASS $1: $2"; }
  fail() { echo "FAIL $1: $2"; failed+=("$1"); }
  wait_for() { echo "PENDING $1: $2"; pending+=("$1"); }
  rerun() { reruns+=("RERUN $1 $2 $3"); }

  # judge <stage> <job> <attempts>: one job's verdict from its attempts.
  # Sets nothing but the arrays above.
  judge() {
    local stage="$1" job="$2" attempts="$3" last fails=0 tail=0 a
    local -a list=()
    IFS=, read -r -a list <<< "$attempts"
    for a in "${list[@]}"; do
      [ "$a" != failure ] || fails=$((fails + 1))
      if [ "$a" = infra ]; then tail=$((tail + 1)); else tail=0; fi
    done
    last="${list[${#list[@]} - 1]}"
    [ "$fails" -eq 0 ] || flaked+=("$job")
    if [ "$fails" -ge 2 ]; then
      fail "$job" "failed again on its retry ($attempts): confirmed red"
      return
    fi
    case "$last" in
      success)
        if [ "$fails" -eq 1 ]; then
          pass "$job" "passed on its one retry ($attempts): a flake"
        else
          pass "$job" "passed${attempts:+ ($attempts)}"
        fi
        ;;
      pending) wait_for "$job" "still running ($attempts)" ;;
      failure)
        echo "PENDING $job: failed for the first time ($attempts); it gets one retry on $tag"
        pending+=("$job")
        rerun "$stage" "$job" flake
        ;;
      infra)
        echo "INFRA $job: cancelled, timed out or starved of capacity ($attempts); re-run, not red$([ "$tail" -lt "$CHECK_QUALIFICATION_INFRA_STALL" ] || echo "; stalled after $tail infra attempts in a row")"
        infra+=("$job")
        rerun "$stage" "$job" infra
        ;;
      skipped)
        # Only a required job has to run: one whose `if:` keeps it off
        # hosted runners (kvm-sandboxes) carries no verdict.
        if jq -e --arg j "$job" 'any(.[]; .id == $j)' <<< "$names" >/dev/null; then
          fail "$job" "never ran (skipped), and qualification requires success"
        fi
        ;;
      *) fail "$job" "has no verdict ($attempts)" ;;
    esac
  }

  echo "Qualification of $tag (${rc:0:12})."

  # Stage 1: nightly.yml on the RC.
  local table stage job attempts id
  table="$(jq -r --arg rc "$rc" --arg which nightly --arg check "$CHECK_QUALIFICATION_CHECK" \
    --arg app "$CHECK_QUALIFICATION_APP" --argjson names "$names" --argjson ignore "$ignore" "$JQ_EPOCH$JQ_ATTEMPTS" "$runs")" ||
    die "--runs-json $runs is not a CI run or job listing"
  declare -A seen=()
  if [ -z "$table" ]; then
    wait_for nightly "nightly.yml has not run on $tag (${rc:0:12}) yet"
  else
    while IFS=$'\t' read -r stage job attempts; do
      [ -n "$job" ] || continue
      seen["$job"]=1
      judge "$stage" "$job" "$attempts"
    done <<< "$table"
    while IFS= read -r id; do
      [ -n "$id" ] || continue
      [ -z "${seen["$id"]+x}" ] || continue
      if [ "${#pending[@]}" -gt 0 ] || [ "${#infra[@]}" -gt 0 ]; then
        wait_for "$id" "has not run on $tag yet"
      else
        fail "$id" "did not run on $tag, and Stage 1 requires it"
      fi
    done < <(jq -r '.[].id' <<< "$names")
  fi

  # Stage 2: the agent suite's check run, once Stage 1 is clean -- unless
  # no app is trusted to post it, when no check run is read at all.
  if [ "$CHECK_QUALIFICATION_APP" = none ]; then
    echo "SKIPPED $CHECK_QUALIFICATION_CHECK: CHECK_QUALIFICATION_APP=none (qualified by Stage 1 nightly.yml)"
  elif [ "${#failed[@]}" -gt 0 ] || [ "${#pending[@]}" -gt 0 ] || [ "${#infra[@]}" -gt 0 ]; then
    echo "PENDING $CHECK_QUALIFICATION_CHECK: waits for Stage 1 (nightly.yml) to pass"
  elif [ -z "$checks" ]; then
    wait_for "$CHECK_QUALIFICATION_CHECK" "no check run listing was given"
  elif [ -z "$CHECK_QUALIFICATION_APP" ]; then
    die "CHECK_QUALIFICATION_APP is unset, so no $CHECK_QUALIFICATION_CHECK check run can be trusted: set it to the slug or id of the GitHub App grain posts it as, or to none to qualify on Stage 1 alone"
  else
    local foreign
    foreign="$(jq -r --arg rc "$rc" --arg check "$CHECK_QUALIFICATION_CHECK" --arg app "$CHECK_QUALIFICATION_APP" "$JQ_CHECK_RUN"'
      [ (if type == "array" then . else (.check_runs // []) end)[]
        | select(.name == $check) | select(.head_sha | sha_ok) | select(from_app | not)
        | .app.slug // (.app.id // "no app" | tostring) ]
      | select(length > 0) | "\(length) from \(unique | join(", "))"' "$checks")" ||
      die "--checks-json $checks is not a check run listing"
    [ -z "$foreign" ] ||
      echo "IGNORED $CHECK_QUALIFICATION_CHECK: $foreign, not $CHECK_QUALIFICATION_APP; only grain's own app is trusted"
    table="$(jq -r --arg rc "$rc" --arg which qualification --arg check "$CHECK_QUALIFICATION_CHECK" \
      --arg app "$CHECK_QUALIFICATION_APP" --argjson names "[]" --argjson ignore "[]" "$JQ_EPOCH$JQ_ATTEMPTS" "$checks")" ||
      die "--checks-json $checks is not a check run listing"
    if [ -z "$table" ]; then
      wait_for "$CHECK_QUALIFICATION_CHECK" "no $CHECK_QUALIFICATION_CHECK check run on ${rc:0:12} yet"
    else
      while IFS=$'\t' read -r stage job attempts; do
        [ -n "$job" ] || continue
        case "${attempts##*,}" in
          failure)
            # grain already re-ran each failing item once before it
            # concluded failure, and re-runs nothing after: confirmed.
            fail "$job" "failed ($attempts): grain concludes failure only once an item has failed its own retry, so it is confirmed red"
            ;;
          success) pass "$job" "passed ($attempts)" ;;
          pending) wait_for "$job" "still running ($attempts)" ;;
          infra)
            # grain concludes neutral only once a Stage 2 item has used all
            # of its own infrastructure re-runs (pipeline.release-infra-stalled).
            echo "INFRA $job: cancelled, timed out or starved of capacity ($attempts); re-run, not red; stalled after Stage 2's infra re-runs"
            infra+=("$job")
            rerun "$stage" "$job" infra
            ;;
          *) judge "$stage" "$job" "$attempts" ;;
        esac
      done <<< "$table"
    fi
  fi

  # Three independent jobs failing on one RC is systemic: no more retries.
  if [ "${#flaked[@]}" -gt "$CHECK_QUALIFICATION_MAX_FLAKES" ]; then
    fail systemic "${#flaked[@]} jobs failed on $tag (${flaked[*]}), more than the $CHECK_QUALIFICATION_MAX_FLAKES that may use a flake retry"
  fi

  # The operator's veto.
  local never release="${tag%-rc.*}"
  never="${RELEASE_NEVER_PROMOTE-$(git show origin/main:scripts/release.conf 2>/dev/null | sed -nE 's/^RELEASE_NEVER_PROMOTE="?([^"#]*)"?[[:space:]]*(#.*)?$/\1/p' | tail -n 1 || true)}"
  if listed "$tag" "$never" || listed "$release" "$never"; then
    fail never "RELEASE_NEVER_PROMOTE ($never) names $(listed "$tag" "$never" && echo "$tag" || echo "$release")"
  fi

  if [ "${#failed[@]}" -gt 0 ]; then
    echo "Qualification: FAIL $tag (${failed[*]})."
    return 1
  fi
  [ "${#reruns[@]}" -eq 0 ] || printf '%s\n' "${reruns[@]}"
  if [ "${#infra[@]}" -gt 0 ]; then
    echo "Qualification: INFRA $tag (${infra[*]})."
    return 3
  fi
  if [ "${#pending[@]}" -gt 0 ]; then
    echo "Qualification: PENDING $tag (${pending[*]})."
    return 2
  fi
  echo "Qualification: PASS $tag."
}

[ $# -gt 0 ] || usage
cmd="$1"
shift
case "$cmd" in
  select-lkg) select_lkg "$@" ;;
  check-qualification) check_qualification "$@" ;;
  -h | --help) usage ;;
  *) usage ;;
esac
