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


# What the release train asks of GitHub Actions after a run of
# scripts/release-from-trunk.sh (design/playbook/releases.playbook.md,
# sections 3, 4, 6 and 14). The train script decides; this does what it
# decided, from the key=value lines it wrote to RELEASE_TRAIN_OUTPUT.
# .github/workflows/rc-qualification.yml runs it after a dry run of the
# train, since the train itself is a grain task with no GitHub token:
#
#   scripts/release-train-actions.sh <train-output-file>
#
#   discarded_rc=<rc>        the rc was dropped (failed qualification, or
#                            RELEASE_NEVER_PROMOTE names it): every nightly.yml
#                            run still queued or running on its commit is
#                            cancelled (the runs on its tag, not main's),
#                            so a superseded rc burns no more runner time
#                            (section 6);
#   qualifying_rc=<rc> and
#   qualification_rerun=<stage>:<job>:<flake|infra>,...
#                            the nightly jobs to re-run on the rc -- a first
#                            failure's one retry (section 3) or an infra
#                            re-run (section 4) -- are dispatched together
#                            as one nightly.yml run on the rc's tag with
#                            inputs.jobs. Not while a nightly.yml run on the
#                            rc is still queued or running: that run (or the
#                            retry already dispatched) is waited for, and the
#                            next wake asks again. The qualification stage
#                            (grain/release-qualification) is re-run by the
#                            deployment that posts it, never from here;
#   rc_tag=<rc>              an rc was cut. A tag pushed with the default
#                            GITHUB_TOKEN fires no workflow, so unless
#                            RELEASE_TAGS_FIRE is true (the tags were pushed
#                            with a GitHub App token or REPO_WRITE_TOKEN,
#                            and nightly.yml's `push: tags` starts Stage 1
#                            itself) Stage 1 is dispatched on the tag;
#   escalated_version=<v>, qualification_stalled=<rc>
#                            reported, for a person or the steward.
#
# It never tags, pushes or promotes anything: every call it makes is to
# the Actions API (actions: write), and none of them moves a ref.
#
# Settings (environment):
#   GITHUB_TOKEN             the token the Actions API is called with
#   RELEASE_GITHUB_REPO      owner/name ($GITHUB_REPOSITORY)
#   RELEASE_GITHUB_API_URL   GitHub's API ($GITHUB_API_URL, else api.github.com)
#   RELEASE_TAGS_FIRE        true: the train's tag pushes start workflows
#   RELEASE_QUALIFICATION_WORKFLOW
#                            Stage 1's workflow file (nightly.yml)
#   DRY_RUN                  true reads, but dispatches and cancels nothing
set -euo pipefail

out_file="${1:-}"
[ -n "$out_file" ] || { echo "release-train-actions: usage: release-train-actions.sh <train-output-file>" >&2; exit 2; }
DRY_RUN="${DRY_RUN:-false}"
api="${RELEASE_GITHUB_API_URL:-${GITHUB_API_URL:-https://api.github.com}}"
repo="${RELEASE_GITHUB_REPO:-${GITHUB_REPOSITORY:-}}"
token="${GITHUB_TOKEN:-}"
workflow="${RELEASE_QUALIFICATION_WORKFLOW:-nightly.yml}"
tags_fire="${RELEASE_TAGS_FIRE:-false}"

say() { echo "release-train-actions: $*"; }
warn() { echo "release-train-actions: WARNING: $*" >&2; }

if ! grep -qE '^[a-z_]+=' "$out_file" 2>/dev/null; then
  say "the train asked for nothing"
  exit 0
fi

# value <key>: the last value the train wrote for key, if any.
value() { sed -n "s/^$1=//p" "$out_file" | tail -n 1; }
values() { sed -n "s/^$1=//p" "$out_file"; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

can_call() {
  if [ -z "$repo" ] || [ -z "$token" ]; then
    warn "no GITHUB_TOKEN or repository to call the Actions API with; $1 is left undone"
    return 1
  fi
}
gh_get() { # gh_get <path> <out>
  curl -fsS --max-time 60 -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" \
    "${api%/}/repos/$repo/$1" -o "$2"
}
gh_post() { # gh_post <path> [json body]
  if [ "$DRY_RUN" = true ]; then
    say "[dry-run] would POST $1${2:+ $2}"
    return 0
  fi
  curl -fsS --max-time 60 -X POST -H "Authorization: Bearer $token" -H "Accept: application/vnd.github+json" \
    ${2:+-H "Content-Type: application/json" -d "$2"} "${api%/}/repos/$repo/$1" -o /dev/null
}

scripts_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

rc_commit() { git rev-parse --verify -q "refs/tags/$1^{commit}" || true; }

# workflow_job_ids <rc>: the job keys in $workflow that a selective
# workflow_dispatch (inputs.jobs) can run -- read from the rc tag when it
# carries the workflow file, else from this checkout or beside scripts/.
workflow_job_ids() {
  local raw=""
  if git cat-file -e "refs/tags/$1:.github/workflows/$workflow" 2>/dev/null; then
    raw="$(git show "refs/tags/$1:.github/workflows/$workflow")"
  elif [ -f ".github/workflows/$workflow" ]; then
    raw="$(cat ".github/workflows/$workflow")"
  elif [ -f "$scripts_dir/../.github/workflows/$workflow" ]; then
    raw="$(cat "$scripts_dir/../.github/workflows/$workflow")"
  else
    return 1
  fi
  printf '%s\n' "$raw" | awk '
    /^jobs:/ { in_jobs = 1; next }
    in_jobs && /^[^ #]/ { in_jobs = 0 }
    in_jobs && /^  [A-Za-z0-9_-]+:[[:space:]]*(#.*)?$/ {
      id = $1; sub(/:$/, "", id)
      if (id != "stage-1") print id
    }
  '
}

# open_runs <rc> <sha>: the ids of the Stage 1 runs on the rc's tag not
# yet completed -- never a run of main's own schedule that happens to be
# on the same commit.
open_runs() {
  gh_get "actions/workflows/$workflow/runs?head_sha=$2&per_page=100" "$work/runs.json" || return 1
  jq -r --arg rc "$1" '.workflow_runs[] | select(.head_branch == $rc and .status != "completed") | .id' "$work/runs.json"
}

dispatch() { # dispatch <ref> <jobs>
  gh_post "actions/workflows/$workflow/dispatches" "$(jq -cn --arg r "$1" --arg j "$2" '{ref: $r, inputs: {jobs: $j}}')"
}

status=0

# Superseded rcs: cancel what is still running on them.
for rc in $(values discarded_rc); do
  can_call "cancelling $rc's $workflow runs" || { status=1; continue; }
  sha="$(rc_commit "$rc")"
  if [ -z "$sha" ]; then
    warn "no tag $rc in this checkout to find its $workflow runs by"
    status=1
    continue
  fi
  ids="$(open_runs "$rc" "$sha")" || { warn "could not read $workflow's runs on $rc"; status=1; continue; }
  if [ -z "$ids" ]; then
    say "$rc was dropped; no $workflow run is still running on it"
    continue
  fi
  for id in $ids; do
    say "$rc was dropped; cancelling its $workflow run $id"
    gh_post "actions/runs/$id/cancel" || { warn "could not cancel $workflow run $id on $rc"; status=1; }
  done
done

# The rc in qualification: re-run the jobs the gate named.
rc="$(value qualifying_rc)"
reruns="$(value qualification_rerun)"
if [ -n "$rc" ] && [ -n "$reruns" ]; then
  entries="$(printf '%s\n' "$reruns" | sed -E 's/:(flake|infra),/:\1\n/g')"
  mapfile -t requested < <(printf '%s\n' "$entries" | sed -nE 's/^nightly:(.+):(flake|infra)$/\1/p' | awk 'NF' | sort -u)
  others="$(printf '%s\n' "$entries" | grep -v '^nightly:' | paste -sd, - || true)"
  [ -z "$others" ] || say "$rc: $others is the deployment's to re-run (it posts grain/release-qualification); nothing to do here"
  valid_jobs=()
  if [ "${#requested[@]}" -gt 0 ]; then
    if known_ids="$(workflow_job_ids "$rc")"; then
      for j in "${requested[@]}"; do
        if grep -qxF "$j" <<< "$known_ids"; then
          valid_jobs+=("$j")
        else
          warn "$rc: '$j' is not a dispatchable job key in $workflow; refusing to dispatch it (add id=$j to scripts/candidate-gate.sh)"
          echo "::error::release-train: $rc asked to re-run '$j', which is not a job key in $workflow"
          status=1
        fi
      done
    else
      valid_jobs=("${requested[@]}")
    fi
  fi
  jobs="$(IFS=,; echo "${valid_jobs[*]}")"
  if [ -n "$jobs" ] && can_call "re-running $jobs on $rc"; then
    sha="$(rc_commit "$rc")"
    open=""
    if [ -n "$sha" ]; then
      open="$(open_runs "$rc" "$sha")" || { warn "could not read $workflow's runs on $rc"; open=unknown; }
    fi
    if [ -n "$open" ]; then
      say "$rc: $workflow is still running on it ($(printf '%s' "$open" | paste -sd' ' -)); re-running $jobs waits for it"
    else
      say "$rc: re-running $jobs ($reruns)"
      dispatch "$rc" "$jobs" || { warn "could not dispatch $workflow on $rc for $jobs"; status=1; }
    fi
  fi
fi

# A new rc: start Stage 1 unless its tag push already did.
new_rc="$(value rc_tag)"
if [ -n "$new_rc" ]; then
  if [ "$tags_fire" = true ]; then
    say "$new_rc was pushed with a token whose tag pushes start workflows; $workflow's push trigger starts Stage 1"
  elif can_call "starting Stage 1 on $new_rc"; then
    say "$new_rc was pushed with GITHUB_TOKEN, whose tag pushes start no workflow; dispatching $workflow on it"
    dispatch "$new_rc" "" || { warn "could not dispatch $workflow on $new_rc"; status=1; }
  else
    status=1
  fi
fi

stalled="$(value qualification_stalled)"
[ -z "$stalled" ] || echo "::warning::release-train: $stalled has been re-run on infra failures repeatedly and is stalled; the runner, quota or credential behind it needs repair (playbook section 4)"
escalated="$(value escalated_version)"
[ -z "$escalated" ] || echo "::error::release-train: $escalated has used RELEASE_MAX_RC_ATTEMPTS release candidates; the train keeps cutting it from main, but a person should look at why it keeps failing (playbook section 5)"

exit "$status"
