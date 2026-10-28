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

# The release train: release grain from main after a staging soak
# (docs/design/release-from-trunk.md, §§2-5). It replaces the legacy
# candidate cut (scripts/release-candidate-cut.sh): there is no
# stabilization branch, no back-merge and no port; every tag it makes is
# on a main commit, and refs/heads/candidate is only ever a pointer it
# force-moves onto the release candidate staging (grain-candidate, :4280)
# runs. .grain/workflows/release-train.yml runs it daily; by hand it is
#
#   scripts/release-from-trunk.sh
#
# It does everything up to the release itself and stops there: a soak
# that passes stages the release as vX.Y.0-pending, and a person makes it
# official (docs/design/release-from-trunk.md, §4). The person's two
# answers are the other two modes, which .grain/workflows/promote-release.yml
# runs and nothing else should:
#
#   scripts/release-from-trunk.sh approve vX.Y.0
#   scripts/release-from-trunk.sh reject vX.Y.0
#
# Each refuses unless GRAIN_CI_REQUESTED_BY_KIND is "human" -- grain sets
# it only on a run its person-only approval route started -- so neither an
# agent nor a schedule can promote a release. approve tags vX.Y.0 and
# force-moves `latest` onto the pending commit, retags the staged
# :vX.Y.0-pending images as :vX.Y.0 and :latest (release-images.sh
# promote) and deletes the vX.Y.0-pending tag. reject takes the failed-soak
# path: the rc the release came from is tagged -discarded and its images
# marked (discard-rc), the staged images are marked rejected
# (discard-pending), any rc of the next minor cut on top of it is dropped
# the same way, and the -pending tag is deleted, so the next run cuts
# rc.N+1 of the same minor.
#
# Each train run is two idempotent phases.
#
# Phase 1 -- the release candidate soaking, if there is one. The target
# release is vM.(P+1).0, P the highest promoted minor of RELEASE_MAJOR
# (raised to RELEASE_FLOOR_MINOR, stepping over a vM.Y.0 in
# RELEASE_NEVER_PROMOTE -- the same rule scripts/build-version.sh versions
# main's builds by). Its newest rc, vM.Y.0-rc.N, soaks unless it carries
# a vM.Y.0-rc.N-discarded tag:
#   - younger than 24h less RELEASE_INTERVAL_SLACK_HOURS (2): still
#     soaking; the run stops here (FORCE=true skips the wait);
#   - otherwise scripts/candidate-gate.sh check-soak judges it:
#       PENDING  the run stops here, and the next one asks again;
#       PASS     it is staged for a person to approve: vM.Y.0-pending is
#                tagged on its commit and pushed (naming the rc and the
#                soak verdict), its images built and published as
#                :vM.Y.0-pending (release-images.sh pending), the
#                release-notes task filed (from the newest vX.Y.0
#                RELEASE_NEVER_PROMOTE does not name, so never from a
#                placeholder such as v2.5.0), and an operator notification
#                raised with Approve and Reject; vM.Y.0 and `latest` are
#                not touched. Phase 2 then cuts the next minor's rc.1;
#       FAIL     it is dropped: vM.Y.0-rc.N-discarded is tagged on its
#                commit and pushed, its images marked (release-images.sh
#                discard-rc), and Phase 2 cuts rc.N+1 of the same minor --
#                an rc that fails never costs a minor number. A failure
#                no filed defect accounts for (the supervisor, the HTTP
#                probe, a failed exploration run) also makes the run exit
#                1 once Phase 2 is done, so the scheduled workflow's
#                review files soak triage.
#     An rc RELEASE_NEVER_PROMOTE names is dropped without a soak.
#
#   While a release is pending, the train keeps going but never past it:
#   the pending release counts as the newest release for choosing the
#   next target (so the next rc is vM.(Y+1).0-rc.1, cut strictly after
#   the pending commit), each run raises its notification again, and an
#   rc of the next minor whose soak passes waits -- it is never staged,
#   and never supersedes the pending release, until a person approves or
#   rejects it. scripts/build-version.sh does not count -pending tags, so
#   main's builds keep versioning from the last promoted release.
#
# Phase 2 -- with nothing soaking, cut the next rc: the newest main commit
# scripts/candidate-gate.sh select-lkg finds known-good strictly after the
# floor (the dropped rc's commit, the promoted release's, or nothing) is
# tagged vM.Y.0-rc.K, refs/heads/candidate is force-moved onto it --
# grain-candidate refreshes onto it by itself, and the next soak checks it
# did -- and its images are published (release-images.sh rc). No new
# known-good commit cuts nothing: main red for a week ships nothing and
# `latest` stands.
#
# What it reads comes through the release-gate-read capability, the
# api:release-gate-read preset (GRAIN_API_URL and GRAIN_API_TOKEN, sourced
# from ~/.grain-api/env when unset): GET /api/ci/runs, /api/persistent-
# sandboxes and /api/tasks. RELEASE_RUNS_JSON, RELEASE_SANDBOXES_JSON and
# RELEASE_TASKS_JSON name files to read instead.
#
# Settings (environment):
#   RELEASE_REMOTE           the remote (origin)
#   RELEASE_NOW              the time, in seconds (now)
#   FORCE                    true promotes or drops an rc younger than the soak
#   RELEASE_SOAK_HOURS, RELEASE_INTERVAL_SLACK_HOURS
#                            24 and 2: a daily 04:15 run never misses the
#                            boundary by a minute of jitter
#   RELEASE_STAGING_URL      staging's UI (http://127.0.0.1:4280)
#   RELEASE_IMAGES           the image publisher (bash scripts/release-images.sh);
#                            "off" publishes nothing
#   RELEASE_NOTES_TASK       "off" files no release-notes task
#   RELEASE_NOTIFY           "off" raises no operator notification
#   RELEASE_TRAIN_OUTPUT     a file to append pending_version=, promoted_version=,
#                            rc_tag= and discarded_rc= lines to, for the jobs
#                            after this one
#   DRY_RUN                  true pushes, publishes and files nothing
#   RELEASE_DEPLOYED_AT      when the rc reached staging, in seconds (by
#                            default check-soak reads it off the supervisor)
#   RELEASE_MAJOR, RELEASE_FLOOR_MINOR, RELEASE_NEVER_PROMOTE
#                            override scripts/release.conf as main has it
set -euo pipefail

mode=train
version=""
case "${1:-}" in
  "") ;;
  approve | reject)
    mode="$1"
    version="${2:-${GRAIN_CI_RELEASE_VERSION:-}}"
    ;;
  *)
    echo "release-from-trunk: usage: release-from-trunk.sh [approve|reject vX.Y.0]" >&2
    exit 2
    ;;
esac

scripts_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
remote="${RELEASE_REMOTE:-origin}"
now="${RELEASE_NOW:-$(date +%s)}"
FORCE="${FORCE:-false}"
DRY_RUN="${DRY_RUN:-false}"
soak_hours="${RELEASE_SOAK_HOURS:-24}"
slack_hours="${RELEASE_INTERVAL_SLACK_HOURS:-2}"
staging_url="${RELEASE_STAGING_URL:-http://127.0.0.1:4280}"
images_cmd="${RELEASE_IMAGES:-bash $scripts_dir/release-images.sh}"
output="${RELEASE_TRAIN_OUTPUT:-}"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

say() { echo "release-from-trunk: $*"; }
die() {
  echo "release-from-trunk: $*" >&2
  exit 1
}
emit() { # emit <key> <value>: an output for the jobs after this one
  say "output $1=$2"
  [ -z "$output" ] || printf '%s=%s\n' "$1" "$2" >> "$output"
}

git config user.name "${GIT_USER_NAME:-grain-ci[bot]}"
git config user.email "${GIT_USER_EMAIL:-grain-ci[bot]@users.noreply.github.com}"

say "fetching $remote's main, candidate and tags"
git fetch -q --force --tags "$remote" "+refs/heads/main:refs/remotes/$remote/main" || die "could not fetch main from $remote"
main_ref="refs/remotes/$remote/main"

# The operator's settings, as main has them: read with git rather than
# sourced, so they are the default branch's whatever this run checked
# out, and only these keys are read.
release_conf="$(git show "$main_ref:scripts/release.conf" 2>/dev/null || true)"
conf_value() {
  printf '%s\n' "$release_conf" | sed -nE "s/^$1=\"?([^\"#]*)\"?[[:space:]]*(#.*)?$/\1/p" | tail -n 1 | sed -E 's/[[:space:]]+$//'
}
never="${RELEASE_NEVER_PROMOTE-$(conf_value RELEASE_NEVER_PROMOTE)}"
floor_minor="${RELEASE_FLOOR_MINOR-$(conf_value RELEASE_FLOOR_MINOR)}"
floor_minor="${floor_minor:-0}"
highest_major="$(git tag -l 'v[0-9]*' | sed -nE 's/^v([0-9]+)\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$/\1/p' | sort -n | tail -n 1)"
major="${RELEASE_MAJOR:-$(conf_value RELEASE_MAJOR)}"
major="${major:-${highest_major:-0}}"
[[ "$major" =~ ^[0-9]+$ ]] || die "RELEASE_MAJOR is \"$major\", which is not a number; nothing was done"
[[ "$floor_minor" =~ ^[0-9]+$ ]] || die "RELEASE_FLOOR_MINOR is \"$floor_minor\", which is not a number; nothing was done"
export RELEASE_NEVER_PROMOTE="$never"

never_promotes() { # never_promotes <tag>
  case " $never " in *" $1 "*) return 0 ;; esac
  return 1
}

# The promoted releases: vX.Y.0 tags, newest last.
promoted() { git tag -l 'v[0-9]*' | grep -E '^v[0-9]+\.[0-9]+\.0$' | sort -V || true; }

# The releases waiting for a person: vX.Y.0 for each vX.Y.0-pending tag
# whose vX.Y.0 does not exist, newest last.
pending_releases() {
  local t
  git tag -l 'v[0-9]*-pending' | sed -nE 's/^(v[0-9]+\.[0-9]+\.0)-pending$/\1/p' | sort -V |
    while read -r t; do
      git rev-parse -q --verify "refs/tags/$t" >/dev/null || printf '%s\n' "$t"
    done
}

# target: sets target (vM.Y.0), target_minor, released (the newest
# promoted release, the one target follows; empty if there is none),
# pending (the newest pending release, if any) and notes_from (the newest
# one RELEASE_NEVER_PROMOTE does not name, which target's release notes
# are written from). A vX.Y.0 tag the list names is a placeholder --
# v2.5.0, tagged on main only so that the minors the legacy cut used up
# are never reused -- so it still counts for the next minor and for the
# floor below, but it was never released and there is nothing in it to
# write notes from.
target_release() {
  local p
  released="$(promoted | tail -n 1)"
  pending="$(pending_releases | tail -n 1)"
  notes_from="$(promoted | while read -r t; do never_promotes "$t" || printf '%s\n' "$t"; done | tail -n 1)"
  # A pending release holds its minor: the next target is the one after
  # it, whether or not a person has approved it yet.
  p="$({ promoted; pending_releases; } | sed -nE "s/^v$major\\.([0-9]+)\\.0$/\\1/p" | sort -n | tail -n 1)"
  if [ -n "$p" ]; then
    target_minor=$((p + 1))
  else
    target_minor=0
  fi
  [ "$target_minor" -ge "$floor_minor" ] || target_minor="$floor_minor"
  while never_promotes "v$major.$target_minor.0"; do
    target_minor=$((target_minor + 1))
  done
  target="v$major.$target_minor.0"
}

commit_of() { git rev-parse --verify -q "refs/tags/$1^{commit}"; }

push() { # push <git push args...>
  if [ "$DRY_RUN" = true ]; then
    say "[dry-run] would git push $remote $*"
    return 0
  fi
  git push -q "$remote" "$@"
}

images() { # images <release-images.sh args...>
  if [ "$images_cmd" = off ]; then
    say "RELEASE_IMAGES=off: not running release-images.sh $*"
    return 0
  fi
  if [ "$DRY_RUN" = true ]; then
    say "[dry-run] would run release-images.sh $*"
    return 0
  fi
  say "release-images.sh $*"
  # shellcheck disable=SC2086 # the command is words on purpose
  $images_cmd "$@"
}

# Grain's CI API address and token (the job's api grants, release-gate-read
# among them), from the env file the token is placed in when the
# environment does not have them. A daemon from before the release gate
# became an api preset placed them in ~/.release-gate/env, under the
# GRAIN_RELEASE_GATE_* names, and still reads the same.
if [ -z "${GRAIN_API_URL:-}" ] && [ -r "${HOME:-/home/debian}/.grain-api/env" ]; then
  # shellcheck source=/dev/null
  . "${HOME:-/home/debian}/.grain-api/env"
fi
if [ -z "${GRAIN_API_URL:-}" ] && [ -z "${GRAIN_RELEASE_GATE_URL:-}" ] && [ -r "${HOME:-/home/debian}/.release-gate/env" ]; then
  # shellcheck source=/dev/null
  . "${HOME:-/home/debian}/.release-gate/env"
fi
if [ -z "${GRAIN_API_URL:-}" ] && [ -n "${GRAIN_RELEASE_GATE_URL:-}" ]; then
  GRAIN_API_URL="$GRAIN_RELEASE_GATE_URL"
  GRAIN_API_TOKEN="${GRAIN_RELEASE_GATE_TOKEN:-}"
fi

gate_json() { # gate_json <override-var> <path> <out>
  local file="${!1:-}"
  if [ -n "$file" ]; then
    [ -r "$file" ] || die "$1 names $file, which is not a readable file"
    cp "$file" "$3"
    return 0
  fi
  if [ -z "${GRAIN_API_URL:-}" ] || [ -z "${GRAIN_API_TOKEN:-}" ]; then
    die "no release gate to read $2 from: grant the job release-gate-read (GRAIN_API_URL, GRAIN_API_TOKEN) or set $1"
  fi
  curl -fsS --max-time 60 -H "Authorization: Bearer $GRAIN_API_TOKEN" \
    "${GRAIN_API_URL%/}$2" -o "$3" || die "could not read $2 from the release gate"
}

tasks_json="$work/tasks.json"
have_tasks=""
fetch_tasks() {
  [ -n "$have_tasks" ] || gate_json RELEASE_TASKS_JSON "/api/tasks?limit=500" "$tasks_json"
  have_tasks=yes
}

# File the release-notes task for a promotion, through the gate's one
# POST; a deployment without the template, or without the gate, only
# loses the task, which an operator can file by hand.
file_release_notes() { # file_release_notes <release> <previous>
  local body
  if [ "${RELEASE_NOTES_TASK:-}" = off ]; then
    say "RELEASE_NOTES_TASK=off: no release-notes task for $1 (notes from ${2:-the start})"
    return 0
  fi
  if [ -z "${GRAIN_API_URL:-}" ] || [ -z "${GRAIN_API_TOKEN:-}" ]; then
    say "no release gate, so no release-notes task for $1: file one from .grain/templates/release-notes.yml by hand"
    return 0
  fi
  if [ "$DRY_RUN" = true ]; then
    say "[dry-run] would file the release-notes task for $1 (notes from ${2:-the start})"
    return 0
  fi
  body="$(jq -cn --arg v "$1" --arg p "$2" '{template: "release-notes", fields: {version: $v, previous_version: $p}}')"
  if curl -fsS --max-time 60 -H "Authorization: Bearer $GRAIN_API_TOKEN" -H "Content-Type: application/json" \
      -X POST -d "$body" "${GRAIN_API_URL%/}/api/tasks/from-template" -o "$work/notes-task.json"; then
    notes_task="$(jq -r '.id // empty' "$work/notes-task.json" 2>/dev/null || true)"
    [ -z "$notes_task" ] || notes_task="[task $notes_task](/tasks/$notes_task), which writes docs/releases/$1.md; merge its pull request before approving"
    say "filed the release-notes task for $1 (notes from ${2:-the start})"
  else
    echo "release-from-trunk: WARNING: could not file the release-notes task for $1; file one from .grain/templates/release-notes.yml by hand" >&2
  fi
}

# Raise (or raise again) the operator notification that release $1 is
# pending, through the gate's narrow notification request; a deployment
# without the gate only loses the notification -- the -pending tag is the
# state, and `grain release pending` reads it.
raise_pending() { # raise_pending <release> <rc> <body>
  local body
  if [ "${RELEASE_NOTIFY:-}" = off ]; then
    say "RELEASE_NOTIFY=off: no notification that $1 is ready to promote"
    return 0
  fi
  if [ -z "${GRAIN_API_URL:-}" ] || [ -z "${GRAIN_API_TOKEN:-}" ]; then
    say "no release gate, so no notification that $1 is ready to promote: approve it with \`grain release approve $1\`"
    return 0
  fi
  if [ "$DRY_RUN" = true ]; then
    say "[dry-run] would raise the notification that $1 is ready to promote"
    return 0
  fi
  body="$(jq -cn --arg v "$1" --arg rc "$2" --arg b "$3" '{version: $v, rc: $rc, body: $b}')"
  if curl -fsS --max-time 60 -H "Authorization: Bearer $GRAIN_API_TOKEN" -H "Content-Type: application/json" \
      -X POST -d "$body" "${GRAIN_API_URL%/}/api/notifications/release-pending" -o /dev/null; then
    say "raised the notification that $1 is ready to promote"
  else
    echo "release-from-trunk: WARNING: could not raise the notification that $1 is ready to promote; approve it with \`grain release approve $1\`" >&2
  fi
}

# pending_body <release> <rc> <soak summary> <notes task>: what the
# notification says.
pending_body() {
  printf '%s passed its %sh staging soak and is staged as %s-pending: images built and published as :%s-pending, nothing left to build or test.\n\n' \
    "$2" "$soak_hours" "$1" "$1"
  printf 'Soak: %s\n' "${3:-PASS}"
  printf 'Release notes: %s\n\n' "${4:-the release-notes task for $1 (docs/releases/$1.md); merge it before approving}"
  printf 'Approve tags %s and moves latest onto it; Reject drops %s and the next run cuts its next rc. ' "$1" "$2"
  printf 'Meanwhile the daily train may cut and soak the next minor'"'"'s rc, but it never promotes or supersedes %s: it waits for your answer.\n' "$1"
}

# --- approve / reject: a person's answer to a pending release ---

if [ "$mode" != train ]; then
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.0$ ]] || die "$mode needs the pending release, vX.Y.0 (got \"$version\")"
  if [ "${GRAIN_CI_REQUESTED_BY_KIND:-}" != human ]; then
    die "only a person may $mode $version: GRAIN_CI_REQUESTED_BY_KIND is \"${GRAIN_CI_REQUESTED_BY_KIND:-}\", not \"human\" -- use \`grain release $mode $version\` or the Upgrade pane, which run .grain/workflows/promote-release.yml for a person; nothing was done"
  fi
  by="${GRAIN_CI_REQUESTED_BY:-a person}"
  pending_sha="$(commit_of "$version-pending" || true)"
  [ -n "$pending_sha" ] || die "$version is not pending: $remote has no $version-pending tag; nothing was done"
  if commit_of "$version" >/dev/null; then
    die "$version is already promoted; nothing was done"
  fi
  from_rc="$(git tag --points-at "$pending_sha" -l "$version-rc.*" | grep -E "^${version//./\\.}-rc\\.[0-9]+$" | sort -t. -k4,4n | tail -n 1 || true)"
  if [ "$mode" = approve ]; then
    prev="$(promoted | tail -n 1)"
    say "approve: $by approved $version (${from_rc:-?}, ${pending_sha:0:12}); promoting it to $version and latest"
    git tag -a "$version" "$pending_sha" -m "Release $version" -m "Promoted from ${from_rc:-$version-pending}, approved by $by"
    git tag -fa latest "$pending_sha" -m "Latest release $version"
    push "refs/tags/$version" || die "could not push $version; latest was not moved and $version-pending stands"
    push --force "refs/tags/latest" || die "pushed $version, but could not move latest onto it"
    emit promoted_version "$version"
    images promote "$version" || die "tagged $version and latest, but could not publish their images: re-run .grain/workflows/promote.yml"
    push --delete "refs/tags/$version-pending" || die "promoted $version, but could not delete $version-pending"
    git tag -d "$version-pending" >/dev/null
    say "approve: $version is released (after ${prev:-nothing}); $version-pending is gone"
    exit 0
  fi
  say "reject: $by rejected $version (${from_rc:-?}, ${pending_sha:0:12}); dropping it"
  if [ -n "$from_rc" ] && ! commit_of "$from_rc-discarded" >/dev/null; then
    git tag -fa "$from_rc-discarded" "$pending_sha" -m "Release candidate $from_rc rejected at approval by $by"
    push "refs/tags/$from_rc-discarded" || die "could not push $from_rc-discarded"
    images discard-rc "$from_rc" || die "could not mark $from_rc's images discarded (its -discarded tag is pushed)"
    emit discarded_rc "$from_rc"
  fi
  images discard-pending "$version" || die "could not mark $version-pending's images rejected"
  # An rc of the next minor was cut on top of the release just rejected,
  # and would be versioned past a release that never shipped: drop it too.
  next="${version%.*.*}.$(( $(printf '%s' "$version" | cut -d. -f2) + 1 )).0"
  for later in $(git tag -l "$next-rc.*" | grep -E "^${next//./\\.}-rc\\.[0-9]+$" | sort -t. -k4,4n || true); do
    commit_of "$later-discarded" >/dev/null && continue
    git tag -fa "$later-discarded" "$(commit_of "$later")" -m "Release candidate $later dropped: $version, which it follows, was rejected by $by"
    push "refs/tags/$later-discarded" || die "could not push $later-discarded"
    images discard-rc "$later" || die "could not mark $later's images discarded (its -discarded tag is pushed)"
    emit discarded_rc "$later"
  done
  push --delete "refs/tags/$version-pending" || die "could not delete $version-pending"
  git tag -d "$version-pending" >/dev/null
  say "reject: $version is dropped; the next run cuts its next rc"
  exit 0
fi

target_release
say "major $major (floor minor $floor_minor); newest promoted release ${released:-none}${pending:+; $pending pending approval}; target $target; its notes are from ${notes_from:-the start}"
if [ -n "$pending" ]; then
  pending_sha="$(commit_of "$pending-pending")"
  pending_rc="$(git tag --points-at "$pending_sha" -l "$pending-rc.*" | sort -t. -k4,4n | tail -n 1 || true)"
  say "$pending (${pending_sha:0:12}, from ${pending_rc:-?}) is pending a person's approval; this run neither promotes nor supersedes it"
  raise_pending "$pending" "$pending_rc" "$(pending_body "$pending" "$pending_rc" "" "")"
fi

# --- Phase 1 ---

rc_tags() { # the rc tags of $target, highest N last
  git tag -l "$target-rc.*" | grep -E "^${target//./\\.}-rc\\.[0-9]+$" | sort -t. -k4,4n || true
}
floor=""
next_n=1
triage=""
rc="$(rc_tags | tail -n 1)"
if [ -n "$rc" ]; then
  rc_sha="$(commit_of "$rc")"
  n="${rc##*.}"
  if commit_of "$rc-discarded" >/dev/null; then
    say "phase 1: $rc (${rc_sha:0:12}) was dropped; the next rc of $target is rc.$((n + 1))"
    floor="$rc_sha"
    next_n=$((n + 1))
  elif never_promotes "$rc"; then
    say "phase 1: RELEASE_NEVER_PROMOTE names $rc; dropping it without a soak"
    git tag -fa "$rc-discarded" "$rc_sha" -m "Release candidate $rc dropped: RELEASE_NEVER_PROMOTE names it"
    push "refs/tags/$rc-discarded"
    images discard-rc "$rc" || die "could not mark $rc's images discarded (its -discarded tag is pushed)"
    emit discarded_rc "$rc"
    floor="$rc_sha"
    next_n=$((n + 1))
  else
    cut_ts="$(git for-each-ref --format='%(taggerdate:unix)' "refs/tags/$rc")"
    [ -n "$cut_ts" ] || cut_ts="$(git log -1 --format=%ct "$rc_sha")"
    age=$((now - cut_ts))
    min_age=$(((soak_hours - slack_hours) * 3600))
    if [ "$age" -lt "$min_age" ] && [ "$FORCE" != true ]; then
      say "phase 1: $rc (${rc_sha:0:12}) soak in progress ($((age / 3600))h / ${soak_hours}h); nothing to cut while it soaks"
      exit 0
    fi
    say "phase 1: $rc (${rc_sha:0:12}) has soaked $((age / 3600))h; asking the soak gate"
    gate_json RELEASE_SANDBOXES_JSON "/api/persistent-sandboxes" "$work/sandboxes.json"
    fetch_tasks
    soak_status=0
    soak_out="$(bash "$scripts_dir/candidate-gate.sh" check-soak --rc-tag "$rc" --rc-sha "$rc_sha" \
      --sandbox-json "$work/sandboxes.json" --tasks-json "$tasks_json" --staging-url "$staging_url" \
      --deployed-at "${RELEASE_DEPLOYED_AT:-}" 2>&1)" || soak_status=$?
    printf '%s\n' "$soak_out" | sed 's/^/  /'
    case "$soak_status" in
      0)
        if [ -n "$pending" ]; then
          say "phase 1: $rc passed its soak, but $pending is still pending approval; $rc waits behind it and is never staged over it"
          exit 0
        fi
        prev="$notes_from"
        verdict="$(printf '%s\n' "$soak_out" | sed -n 's/^PASS //p' | paste -sd';' - | sed 's/;/; /g')"
        say "phase 1: $rc passed its soak; staging $target-pending for a person to approve ($target and latest are not touched)"
        git tag -a "$target-pending" "$rc_sha" -m "Release $target pending approval" \
          -m "From $rc, whose ${soak_hours}h staging soak passed: ${verdict:-PASS}" \
          -m "A person approves it (grain release approve $target) or rejects it (grain release reject $target)."
        push "refs/tags/$target-pending" || die "could not push $target-pending"
        emit pending_version "$target"
        images pending "$target" || die "tagged $target-pending, but could not stage its images: run \`release-images.sh pending $target\`"
        notes_task=""
        file_release_notes "$target" "$prev"
        raise_pending "$target" "$rc" "$(pending_body "$target" "$rc" "${verdict:-PASS}" "$notes_task")"
        floor="$rc_sha"
        next_n=1
        target_release
        say "the next release is $target; it is cut and soaked, but never promoted past $pending"
        ;;
      2)
        say "phase 1: $rc's soak is pending; the next run asks again"
        exit 0
        ;;
      1)
        summary="$(printf '%s\n' "$soak_out" | sed -n 's/^FAIL //p' | paste -sd';' - | sed 's/;/; /g')"
        say "phase 1: $rc failed its soak; dropping it"
        git tag -fa "$rc-discarded" "$rc_sha" -m "Release candidate $rc failed its staging soak" -m "$summary"
        push "refs/tags/$rc-discarded" || die "could not push $rc-discarded"
        images discard-rc "$rc" || die "could not mark $rc's images discarded (its -discarded tag is pushed)"
        emit discarded_rc "$rc"
        if printf '%s\n' "$soak_out" | grep -qE '^FAIL (supervisor|http|exploration):'; then
          triage="$rc failed its soak on staging itself, not on a filed defect: $summary"
        fi
        floor="$rc_sha"
        next_n=$((n + 1))
        ;;
      *)
        die "the soak gate could not judge $rc (exit $soak_status)"
        ;;
    esac
  fi
elif [ -n "$pending" ]; then
  floor="$(commit_of "$pending-pending")"
  say "phase 1: nothing soaking; $target's rc.1 must be after the pending $pending (${floor:0:12})"
elif [ -n "$released" ]; then
  floor="$(commit_of "$released")"
  say "phase 1: nothing soaking; $target's rc.1 must be after $released (${floor:0:12})"
else
  say "phase 1: nothing soaking and nothing promoted yet"
fi

# --- Phase 2 ---

gate_json RELEASE_RUNS_JSON "/api/ci/runs?repo=${GRAIN_CI_REPO:-bwsalmon/grain}&ref=main&limit=500" "$work/runs.json"
fetch_tasks
lkg_status=0
lkg_out="$(bash "$scripts_dir/candidate-gate.sh" select-lkg --runs-json "$work/runs.json" ${floor:+--floor-sha "$floor"} \
  --ref "$main_ref" --tasks-json "$tasks_json" --now "$now" 2>&1)" || lkg_status=$?
printf '%s\n' "$lkg_out" | sed 's/^/  /'
lkg="$(printf '%s\n' "$lkg_out" | sed -n 's/^LKG \([0-9a-f]\{40\}\)$/\1/p' | tail -n 1)"
if [ -z "$lkg" ]; then
  [ "$lkg_status" -le 1 ] || die "the LKG selector failed (exit $lkg_status)"
  say "phase 2: no new LKG commit on main${floor:+ since ${floor:0:12}}; nothing to cut"
else
  new_rc="$target-rc.$next_n"
  while commit_of "$new_rc" >/dev/null; do
    # Never re-tag: an rc of this number already exists (one cut by hand).
    next_n=$((next_n + 1))
    new_rc="$target-rc.$next_n"
  done
  say "phase 2: cutting $new_rc on ${lkg:0:12}"
  git tag -a "$new_rc" "$lkg" -m "Release candidate $new_rc"
  push "refs/tags/$new_rc" || die "could not push $new_rc; candidate was not moved"
  push --force "$lkg:refs/heads/candidate" || die "pushed $new_rc, but could not move candidate onto it"
  emit rc_tag "$new_rc"
  images rc "$new_rc" || die "cut $new_rc and moved candidate, but could not publish its images: run \`release-images.sh rc $new_rc\`"
  say "phase 2: $new_rc is cut and candidate is on it; grain-candidate refreshes onto it by itself, and the next run's soak checks that it did"
fi

if [ -n "$triage" ]; then
  echo "release-from-trunk: $triage" >&2
  exit 1
fi
