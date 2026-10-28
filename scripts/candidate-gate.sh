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

# The release train's two gates (docs/design/release-from-trunk.md, §§2-4):
# which main commit is known-good enough to cut as the next release
# candidate, and whether the candidate soaking on staging may be promoted.
# scripts/release-from-trunk.sh calls both; each reads JSON the train
# fetched through the release-gate-read capability (the api:release-gate-read
# preset, pkg/capability/ciapi) and decides nothing it was not handed.
#
#   candidate-gate.sh select-lkg --runs-json FILE [--floor-sha SHA]
#       [--ref REF] [--tasks-json FILE] [--now EPOCH] [--max-commits N]
#
#     The newest commit on REF's first-parent history (default
#     origin/main), strictly after --floor-sha, whose required suites are
#     green: .grain/workflows/tests.yml, release.yml and nightly.yml
#     (CANDIDATE_GATE_REQUIRED). FILE is GET /api/ci/runs' answer (or a
#     bare array of its runs). A suite's verdict at a commit is the latest
#     result of each of its jobs across every run at that commit -- the
#     same combination the merge gate reads, so a recheck that turned one
#     job green turns the suite green -- and is success (every job passed
#     or was skipped), failure, infra (cancelled or errored, and nothing
#     failed: a re-run's business, not a regression), pending or missing.
#
#     nightly.yml runs once a day, so a commit merged after it also
#     qualifies when its own tests.yml and release.yml are green and the
#     newest nightly.yml verdict on its first-parent ancestry finished
#     success within the last 24 hours, with no fixes_base_branch repair
#     open on main -- which needs --tasks-json (GET /api/tasks); without
#     it only a commit with a nightly.yml of its own qualifies.
#
#     The live-provider suites (CANDIDATE_GATE_ADVISORY: gcp-smoke and
#     live-*, post-reset-e2e) are reported and never block. Every
#     required suite found infra is reported as needing a re-run.
#
#     Prints one line per commit it looked at and a last line
#     "LKG <sha>", exiting 0; or "LKG none", exiting 1.
#
#   candidate-gate.sh check-soak --rc-tag TAG --rc-sha SHA
#       --sandbox-json FILE --tasks-json FILE [--staging-url URL]
#       [--sandbox NAME] [--deployed-at EPOCH]
#
#     The four soak signals of the candidate on staging, plus the
#     operator's veto:
#       supervisor   NAME (default grain-candidate, in FILE, GET
#                    /api/persistent-sandboxes' answer) is running SHA,
#                    with no refresh-crash or rollback event and no exit
#                    of the command (exit, sandbox-exit) since SHA was
#                    deployed. The status's own `restarts` counts every
#                    restart the entry ever made, refreshes included, so
#                    the restarts while running SHA are read off its
#                    events instead;
#       http         URL (default http://127.0.0.1:4280) serves the HTML
#                    shell at /, /api/config reports TAG (or SHA) as its
#                    version, and every one of CHECK_SOAK_ENDPOINTS answers
#                    200 with JSON;
#       exploration  the newest staging-ui-exploration task (by its title,
#                    CHECK_SOAK_EXPLORATION_TITLE) that started after SHA
#                    was deployed has finished, and did not fail; none yet
#                    is PENDING;
#       defects      no task filed from one of those exploration runs, or
#                    carrying TAG or "rc-blocker" in its title, since the
#                    deployment is still open;
#       never        TAG, and the release it would become, are not in
#                    RELEASE_NEVER_PROMOTE (scripts/release.conf).
#     SHA was deployed when the sandbox last refreshed onto it, or failing
#     that when TAG was made (--deployed-at overrides both).
#
#     Prints "<PASS|FAIL|PENDING> <signal>: ..." per signal and a last
#     line "Soak gate: PASS|FAIL|PENDING ...", exiting 0, 1 or 2. A FAIL
#     anywhere wins over a PENDING.
#
# Usage errors exit 64 for both, since check-soak's 2 is PENDING.
set -euo pipefail

CANDIDATE_GATE_REQUIRED="${CANDIDATE_GATE_REQUIRED-tests release nightly}"
CANDIDATE_GATE_ADVISORY="${CANDIDATE_GATE_ADVISORY-gcp-smoke live-* post-reset-e2e}"
CHECK_SOAK_ENDPOINTS="${CHECK_SOAK_ENDPOINTS-/api/tasks /api/persistent-sandboxes}"
CHECK_SOAK_EXPLORATION_TITLE="${CHECK_SOAK_EXPLORATION_TITLE-Daily exploratory UI testing and defect discovery on staging}"
# How long a nightly.yml verdict on an ancestor vouches for the commits
# after it.
NIGHTLY_WINDOW_SECONDS="${NIGHTLY_WINDOW_SECONDS:-86400}"

usage() {
  cat >&2 <<'EOF'
usage: candidate-gate.sh select-lkg --runs-json FILE [--floor-sha SHA] [--ref REF] [--tasks-json FILE] [--now EPOCH] [--max-commits N]
       candidate-gate.sh check-soak --rc-tag TAG --rc-sha SHA --sandbox-json FILE --tasks-json FILE [--staging-url URL] [--sandbox NAME] [--deployed-at EPOCH]
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

listed() { # listed <word> <space-separated globs>
  local glob
  for glob in $2; do
    # shellcheck disable=SC2053 # the list holds globs on purpose
    [[ "$1" == $glob ]] && return 0
  done
  return 1
}

# A file of tasks (GET /api/tasks) as an array, whatever it was wrapped
# in.
tasks_array() {
  jq -c 'if type == "array" then . else (.tasks // []) end' "$1"
}

# --- select-lkg -------------------------------------------------------------

select_lkg() {
  local runs="" floor="" ref="origin/main" tasks="" now max=200
  now="$(date +%s)"
  while [ $# -gt 0 ]; do
    case "$1" in
      --runs-json) runs="${2:-}"; shift 2 ;;
      --floor-sha) floor="${2:-}"; shift 2 ;;
      --ref) ref="${2:-}"; shift 2 ;;
      --tasks-json) tasks="${2:-}"; shift 2 ;;
      --now) now="${2:-}"; shift 2 ;;
      --max-commits) max="${2:-}"; shift 2 ;;
      *) usage ;;
    esac
  done
  [ -n "$runs" ] || usage
  [ -f "$runs" ] || die "--runs-json $runs is not a file"
  [ -z "$tasks" ] || [ -f "$tasks" ] || die "--tasks-json $tasks is not a file"
  [[ "$now" =~ ^[0-9]+$ ]] || die "--now $now is not seconds since the epoch"
  local head floor_sha=""
  head="$(git rev-parse --verify -q "$ref^{commit}")" || die "$ref does not resolve"
  if [ -n "$floor" ]; then
    floor_sha="$(git rev-parse --verify -q "$floor^{commit}")" || die "--floor-sha $floor does not resolve"
  fi

  # One line per (commit, workflow): commit, workflow, verdict, and when
  # the newest of its jobs finished.
  local table
  table="$(jq -r "$JQ_EPOCH"'
    def wf: ((.workflowPath // "") | split("/") | last | sub("\\.ya?ml$"; "")) as $p
      | if $p != "" then $p else (.workflow // "") end;
    (if type == "array" then . else (.runs // []) end)
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
  declare -A verdict=() finished=()
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
    finished["$full $w"]="$t"
  done <<< "$table"

  # Open repairs of main: what keeps an ancestor's nightly from vouching.
  local repairs=""
  if [ -n "$tasks" ]; then
    repairs="$(tasks_array "$tasks" | jq -r '.[]
      | select(.fixesBaseBranch == true)
      | select((.base // "main") == "main" or (.redBaseBranch // "") == "main")
      | select(.state as $s | ["completed", "closed", "failed"] | index($s) | not)
      | .id' | paste -sd' ' -)"
  fi

  echo "Selecting the newest known-good commit on $ref${floor_sha:+ after ${floor_sha:0:12}} (required: $CANDIDATE_GATE_REQUIRED)."

  local -a commits=()
  mapfile -t commits < <(git rev-list --first-parent --max-count="$max" "$head" ${floor_sha:+"^$floor_sha"})
  local lkg="" sha short reasons wf vv ok anc nv nt infra=()
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
      if [ "$vv" = infra ]; then
        infra+=("$wf.yml at $short")
      fi
      if [ "$wf" = nightly ] && [ "$vv" != success ]; then
        continue
      fi
      reasons+=("$wf.yml $vv")
      [ "$vv" = success ] || ok=""
    done
    if [ -z "$ok" ]; then
      echo "  $short: ${reasons[*]}; not known-good."
      continue
    fi
    if [[ " $CANDIDATE_GATE_REQUIRED " != *" nightly "* ]] || [ "${verdict["$sha nightly"]:-}" = success ]; then
      echo "  $short: ${reasons[*]}; known-good."
      lkg="$sha"
      break
    fi
    # The fast-forward allowance: the newest decided nightly.yml on this
    # commit's own first-parent ancestry.
    nv="" nt="" anc=""
    while read -r anc; do
      nv="${verdict["$anc nightly"]:-}"
      case "$nv" in
        success | failure | infra) nt="${finished["$anc nightly"]:-0}"; break ;;
      esac
      nv=""
    done < <(git rev-list --first-parent --max-count="$max" "$sha")
    if [ -z "$nv" ]; then
      echo "  $short: ${reasons[*]}, and no nightly.yml verdict on it or its ancestry; not known-good."
      continue
    fi
    if [ "$anc" = "$sha" ]; then
      echo "  $short: ${reasons[*]}, nightly.yml $nv; not known-good."
      continue
    fi
    if [ "$nv" != success ]; then
      echo "  $short: ${reasons[*]}; the newest nightly.yml on its ancestry (${anc:0:12}) is $nv; not known-good."
      continue
    fi
    if [ $((now - nt)) -gt "$NIGHTLY_WINDOW_SECONDS" ]; then
      echo "  $short: ${reasons[*]}; the newest nightly.yml on its ancestry (${anc:0:12}) passed $(((now - nt) / 3600))h ago, more than $((NIGHTLY_WINDOW_SECONDS / 3600))h; not known-good."
      continue
    fi
    if [ -z "$tasks" ]; then
      echo "  $short: ${reasons[*]}; nightly.yml passed on ${anc:0:12}, but with no task list to show no repair of main is open, only a commit with a nightly.yml of its own qualifies; not known-good."
      continue
    fi
    if [ -n "$repairs" ]; then
      echo "  $short: ${reasons[*]}; nightly.yml passed on ${anc:0:12}, but a repair of main is open (task $repairs); not known-good."
      continue
    fi
    echo "  $short: ${reasons[*]}; nightly.yml passed on its ancestor ${anc:0:12} $(((now - nt) / 3600))h ago and no repair of main is open; known-good."
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

# --- check-soak -------------------------------------------------------------

check_soak() {
  local tag="" rc="" sandboxes="" tasks="" url="http://127.0.0.1:4280" name="grain-candidate" deployed=""
  while [ $# -gt 0 ]; do
    case "$1" in
      --rc-tag) tag="${2:-}"; shift 2 ;;
      --rc-sha) rc="${2:-}"; shift 2 ;;
      --sandbox-json) sandboxes="${2:-}"; shift 2 ;;
      --tasks-json) tasks="${2:-}"; shift 2 ;;
      --staging-url) url="${2:-}"; shift 2 ;;
      --sandbox) name="${2:-}"; shift 2 ;;
      --deployed-at) deployed="${2:-}"; shift 2 ;;
      *) usage ;;
    esac
  done
  if [ -z "$tag" ] || [ -z "$rc" ] || [ -z "$sandboxes" ] || [ -z "$tasks" ]; then
    usage
  fi
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.0-rc\.[0-9]+$ ]] || die "--rc-tag $tag is not a release candidate tag (vX.Y.0-rc.N)"
  [[ "$rc" =~ ^[0-9a-fA-F]{7,40}$ ]] || die "--rc-sha $rc is not a commit sha"
  [ -f "$sandboxes" ] || die "--sandbox-json $sandboxes is not a file"
  [ -f "$tasks" ] || die "--tasks-json $tasks is not a file"
  [ -z "$deployed" ] || [[ "$deployed" =~ ^[0-9]+$ ]] || die "--deployed-at $deployed is not seconds since the epoch"
  url="${url%/}"
  rc="${rc,,}"

  local failed=() pending=()
  pass() { echo "PASS $1: $2"; }
  fail() { echo "FAIL $1: $2"; failed+=("$1"); }
  wait_for() { echo "PENDING $1: $2"; pending+=("$1"); }

  local status
  status="$(jq -c --arg name "$name" '(if type == "array" then . else (.sandboxes // []) end)
    | map(select(.name == $name)) | first // empty' "$sandboxes")" || die "--sandbox-json $sandboxes is not a persistent sandbox listing"

  # When the candidate went onto staging: the supervisor's own refresh
  # onto it, else the tag.
  if [ -z "$deployed" ]; then
    deployed="$(jq -r --arg rc "$rc" "$JQ_EPOCH"'
      (.lastRefresh? // {}) | select(((.to // "") | ascii_downcase) as $to | $to != "" and ($rc | startswith($to) or ($to | startswith($rc)))) | .at | epoch' <<< "${status:-null}" 2>/dev/null || true)"
    deployed="${deployed%%.*}"
  fi
  if [ -z "$deployed" ] || [ "$deployed" = 0 ]; then
    deployed="$(git for-each-ref --format='%(taggerdate:unix)' "refs/tags/$tag" 2>/dev/null || true)"
  fi
  if [ -z "$deployed" ]; then
    deployed="$(git log -1 --format=%ct "$rc" 2>/dev/null || echo 0)"
  fi
  echo "Soak of $tag (${rc:0:12}) on $name, deployed at $(date -u -d "@$deployed" +%Y-%m-%dT%H:%MZ 2>/dev/null || echo "$deployed")."

  # 1. supervisor
  if [ -z "$status" ]; then
    fail supervisor "$name is not in the persistent sandbox listing"
  else
    local state commit crashes exits
    state="$(jq -r '.state // ""' <<< "$status")"
    commit="$(jq -r '(.commit // "") | ascii_downcase' <<< "$status")"
    crashes="$(jq -r --argjson since "$deployed" "$JQ_EPOCH"'[(.events // [])[] | select((.at | epoch) >= $since) | select(.kind == "refresh-crash" or .kind == "rollback") | .kind + " (" + (.message // "") + ")"] | join("; ")' <<< "$status")"
    exits="$(jq -r --argjson since "$deployed" "$JQ_EPOCH"'[(.events // [])[] | select((.at | epoch) >= $since) | select(.kind == "exit" or .kind == "sandbox-exit")] | length' <<< "$status")"
    if [ "$state" != running ]; then
      fail supervisor "$name is ${state:-in no state}, not running${commit:+ (at ${commit:0:12})}"
    elif [ -z "$commit" ] || ! { [[ "$rc" == "$commit"* ]] || [[ "$commit" == "$rc"* ]]; }; then
      fail supervisor "$name is running $([ -n "$commit" ] && echo "${commit:0:12}" || echo "no recorded commit"), not ${rc:0:12} (a rollback, or a refresh that never happened)"
    elif [ -n "$crashes" ]; then
      fail supervisor "$name recorded $crashes since ${rc:0:12} was deployed"
    elif [ "$exits" -gt 0 ]; then
      fail supervisor "$name's command exited $exits time(s) since ${rc:0:12} was deployed"
    else
      pass supervisor "$name is running ${rc:0:12} with no crash, rollback or restart since it was deployed"
    fi
  fi

  # 2. http
  local body code problems=() endpoint
  if ! command -v curl >/dev/null 2>&1; then
    fail http "curl is not installed, so $url cannot be probed"
  else
    body="$(mktemp)"
    code="$(curl -s -o "$body" -w '%{http_code}' --max-time 20 "$url/" || true)"
    if [ "$code" != 200 ]; then
      problems+=("/ answered ${code:-nothing}")
    elif ! grep -qi '<html\|<!doctype html' "$body"; then
      problems+=("/ is not an HTML page")
    fi
    code="$(curl -s -o "$body" -w '%{http_code}' --max-time 20 "$url/api/config" || true)"
    if [ "$code" != 200 ]; then
      problems+=("/api/config answered ${code:-nothing}")
    elif ! jq -e --arg tag "$tag" --arg rc "$rc" '
        (.version.release // "") == $tag or ((.version.commit // "" | ascii_downcase) as $c | $c != "" and ($rc | startswith($c) or ($c | startswith($rc))))' "$body" >/dev/null 2>&1; then
      problems+=("/api/config reports version $(jq -r '(.version.release // "none") + " at " + (.version.commit // "no commit")' "$body" 2>/dev/null || echo "that is not JSON"), not $tag")
    fi
    for endpoint in $CHECK_SOAK_ENDPOINTS; do
      code="$(curl -s -o "$body" -w '%{http_code}' --max-time 20 "$url$endpoint" || true)"
      if [ "$code" != 200 ]; then
        problems+=("$endpoint answered ${code:-nothing}")
      elif ! jq -e . "$body" >/dev/null 2>&1; then
        problems+=("$endpoint is not JSON")
      fi
    done
    rm -f "$body"
    if [ "${#problems[@]}" -gt 0 ]; then
      local joined
      joined="$(printf '%s; ' "${problems[@]}")"
      fail http "$url: ${joined%; }"
    else
      pass http "$url serves the UI, reports $tag, and answers $CHECK_SOAK_ENDPOINTS"
    fi
  fi

  # 3. exploration and 4. defects
  local all explore latest lstate lid ids defects
  all="$(tasks_array "$tasks")" || die "--tasks-json $tasks is not a task listing"
  explore="$(jq -c --arg title "$CHECK_SOAK_EXPLORATION_TITLE" --argjson since "$deployed" "$JQ_EPOCH"'
    [ .[] | select(.title == $title) | select(((.startedAt // .createdAt) | epoch) >= $since) ]
    | sort_by((.startedAt // .createdAt) | epoch)' <<< "$all")"
  latest="$(jq -c 'last // empty' <<< "$explore")"
  if [ -z "$latest" ]; then
    wait_for exploration "no staging-ui-exploration run has started since ${rc:0:12} was deployed"
  else
    lstate="$(jq -r '.state // ""' <<< "$latest")"
    lid="$(jq -r '.id // ""' <<< "$latest")"
    case "$lstate" in
      completed | closed) pass exploration "task $lid finished ($lstate) against ${rc:0:12}" ;;
      failed) fail exploration "task $lid, the newest staging-ui-exploration run, failed" ;;
      *) wait_for exploration "task $lid is still ${lstate:-unfinished}" ;;
    esac
  fi
  ids="$(jq -c '[.[].id]' <<< "$explore")"
  defects="$(jq -r --argjson ids "$ids" --arg tag "$tag" --argjson since "$deployed" "$JQ_EPOCH"'
    [ .[]
      | select(((.generatedFrom // "") as $g | $ids | index($g))
          or (((.createdAt | epoch) >= $since) and ((.title // "") | test("rc-blocker|" + ($tag | gsub("\\."; "\\.")); "i"))))
      | select(.state as $s | ["completed", "closed"] | index($s) | not)
      | "task " + .id + " (" + (.title // "") + ")" ] | join("; ")' <<< "$all")"
  if [ -n "$defects" ]; then
    fail defects "defects filed against $tag are open: $defects"
  else
    pass defects "no open defect filed against $tag"
  fi

  # 5. the operator's veto
  local never release="${tag%-rc.*}"
  never="${RELEASE_NEVER_PROMOTE-$(git show origin/main:scripts/release.conf 2>/dev/null | sed -nE 's/^RELEASE_NEVER_PROMOTE="?([^"#]*)"?[[:space:]]*(#.*)?$/\1/p' | tail -n 1 || true)}"
  if listed "$tag" "$never" || listed "$release" "$never"; then
    fail never "RELEASE_NEVER_PROMOTE ($never) names $(listed "$tag" "$never" && echo "$tag" || echo "$release")"
  else
    pass never "RELEASE_NEVER_PROMOTE does not name $tag or $release"
  fi

  if [ "${#failed[@]}" -gt 0 ]; then
    echo "Soak gate: FAIL $tag (${failed[*]})."
    return 1
  fi
  if [ "${#pending[@]}" -gt 0 ]; then
    echo "Soak gate: PENDING $tag (${pending[*]})."
    return 2
  fi
  echo "Soak gate: PASS $tag."
}

[ $# -gt 0 ] || usage
cmd="$1"
shift
case "$cmd" in
  select-lkg) select_lkg "$@" ;;
  check-soak) check_soak "$@" ;;
  -h | --help) usage ;;
  *) usage ;;
esac
