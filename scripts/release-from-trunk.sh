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


# The release train: release grain continuously from main
# (design/playbook/releases.playbook.md, which supersedes the staging soak
# of design/design/release-from-trunk.design.md). There is no
# stabilization branch, no back-merge and no port: every tag it makes is
# on a main commit -- except an rc.3, which is cut from a short-lived
# release/vX.Y.Z convergence branch -- and refs/heads/candidate is only
# ever a pointer it force-moves onto the newest release candidate.
# .github/workflows/release-train.yml runs it (.grain/workflows/release-train.yml
# by dispatch); by hand it is
#
#   scripts/release-from-trunk.sh
#
# It runs continuously (.grain/schedules/release-train.yml) and releases
# on its own: an rc that qualifies is tagged vX.Y.Z, and `latest` moved
# onto it, with no person's approval. Only a release the train may not
# make itself -- the first of a new RELEASE_MAJOR, one carrying breaking
# changes, any while RELEASE_AUTO_PROMOTE is false -- is staged as
# vX.Y.Z-pending for a person to make official. The person's answers to
# a staged release, and a rollback, are the other modes, which
# .grain/workflows/promote-release.yml (and promote.yml) run and nothing
# else should:
#
#   scripts/release-from-trunk.sh approve vX.Y.Z
#   scripts/release-from-trunk.sh reject vX.Y.Z
#   scripts/release-from-trunk.sh rollback --bad vX.Y.Z [--to vA.B.C] [--reason TEXT]
#
# Each refuses unless GRAIN_CI_REQUESTED_BY_KIND is "human" -- grain sets
# it only on a run its person-only routes started -- so neither an agent
# nor a schedule can promote or retract a release.
#   approve  tags vX.Y.Z and force-moves `latest` onto the pending commit,
#            retags the staged :vX.Y.Z-pending images as :vX.Y.Z and
#            :latest (release-images.sh promote), deletes the vX.Y.Z-pending
#            tag and the release/vX.Y.Z convergence branch, if there is one.
#            A -pending tag from before qualification (one that records a
#            soak, not "Qualification: PASS") still needs its 24h soak less
#            the slack, or short_soak_ok: true (RELEASE_SHORT_SOAK_OK).
#            It refuses a release carrying breaking changes since the
#            release its notes are written from (scripts/release-notes.sh
#            check-breaking) unless it is the major release RELEASE_MAJOR
#            asks for or RELEASE_ALLOW_BREAKING names it.
#   reject   the rc the release came from is tagged -discarded and its
#            images marked (discard-rc), the staged images are marked
#            rejected (discard-pending), any rc of a later version cut on
#            top of it is dropped the same way, and the -pending tag is
#            deleted, so the next run cuts rc.N+1 of the same version.
#   rollback (playbook section 11) never deletes or moves vX.Y.Z: it tags
#            vX.Y.Z-retracted on it (who, and why), marks its images
#            retracted (release-images.sh retract), and force-moves
#            `latest` -- the git tag and the images (release-images.sh
#            promote) -- back to --to, by default the newest earlier
#            release that is neither retracted nor in RELEASE_NEVER_PROMOTE.
#            pkg/upgrade's RetractedRelease then tells deployments on
#            vX.Y.Z to move. Adding vX.Y.Z to RELEASE_NEVER_PROMOTE and
#            fixing forward on main are for a person; the next release is
#            the next minor.
#
# Each train run is two idempotent phases.
#
# Phase 1 -- the release candidate in qualification, if there is one. The
# target is the next minor past the newest release (promoted or pending) of
# RELEASE_MAJOR; patch versions stay reserved for cherry-picked hotfixes on
# release/vX.Y.Z (scripts/release-patch.sh). Its newest rc, vM.Y.0-rc.N,
# qualifies unless it carries a vM.Y.0-rc.N-discarded tag:
# scripts/candidate-gate.sh check-qualification reads nightly.yml's jobs on
# it (Stage 1) and the grain/release-qualification check run on its commit
# (Stage 2):
#   PENDING  still running, or a job's first failure waits on its one
#            retry; INFRA  a job was cancelled, timed out or starved of
#            capacity and is to be re-run, never counted as red. Either way
#            the run stops here, and says which jobs to re-run
#            (qualifying_rc=<rc> and
#            qualification_rerun=<stage>:<job>:<flake|infra>,... in
#            RELEASE_TRAIN_OUTPUT) -- re-running them is the workflow's
#            (scripts/release-train-actions.sh);
#   PASS     it is released (playbook section 1, "promotion gate"):
#            vM.Y.Z is tagged on the rc's own commit ("Qualification:
#            PASS", "Promoted-From: <rc>") and `latest` force-moved onto it,
#            its images promoted (release-images.sh promote; from a task,
#            RELEASE_IMAGES=off and .github/workflows/publish-release.yml
#            does it on the tag push, with the GitHub Release) and the
#            release-notes task filed (from the newest release neither
#            RELEASE_NEVER_PROMOTE names nor is retracted). Not while
#            RELEASE_MIN_INTERVAL_MINUTES has not passed since the newest
#            release (the run waits, and a later one promotes). It is
#            staged for a person instead -- vM.Y.Z-pending tagged and
#            pushed, its images published as :vM.Y.Z-pending
#            (release-images.sh pending), the notes task filed and an
#            operator notification raised, vM.Y.Z and `latest` untouched
#            -- when RELEASE_AUTO_PROMOTE is false, when it is the first
#            release of RELEASE_MAJOR (a major bump), when it carries
#            breaking changes (release-notes.sh check-breaking), or when
#            its commit is on neither main nor release/vM.Y.Z;
#   FAIL     it is dropped: vM.Y.Z-rc.N-discarded is tagged on its commit
#            and pushed, its images marked (release-images.sh discard-rc),
#            and Phase 2 cuts rc.N+1 of the same version -- an rc that
#            fails never costs a version number.
#   An rc RELEASE_NEVER_PROMOTE names is dropped without qualifying.
#
#   While a release is pending, the train keeps going but never past it:
#   the pending release counts as the newest release for choosing the next
#   version, and an rc after it that qualifies waits -- it is never staged
#   over the pending release until a person approves or rejects it.
#
# Phase 2 -- with nothing qualifying, cut the next rc: the newest commit
# scripts/candidate-gate.sh select-lkg finds known-good (tests.yml and
# release.yml green) strictly after the floor (the dropped rc's commit, or
# the newest release's) is tagged vM.Y.Z-rc.K, refs/heads/candidate is
# force-moved onto it and its images are published (release-images.sh rc).
# Before an rc.(N+1) (next_n > 1), the grain/rc-hold check run on the
# dropped rc's commit (the floor) must be completed or absent: while open
# `[rc-blocker vM.Y.Z-rc.N]` tasks keep it in progress, the train cuts
# nothing (rc_held= in RELEASE_TRAIN_OUTPUT).
# No new known-good commit cuts nothing: main red for a week ships
# nothing and `latest` stands. Before an rc.1:
#   - at least RELEASE_MIN_INTERVAL_MINUTES must have passed since the
#     newest release (promoted or pending) was tagged; FORCE=true skips it;
#   - the commits since the newest release decide whether there is anything
#     to release (playbook section 7): every trunk release is the next
#     minor, vM.(P+1).0, whether it carries features or fixes alone --
#     patches (vM.P.Z, Z >= 1) stay scripts/release-patch.sh's cherry-picked
#     hotfixes on release/vM.P.Z -- while docs:, chore:, test:, ci:, style:
#     and build: alone release nothing and wait for a fix or a feature.
# rc.3 converges (playbook section 5): after rc.2 is dropped, the train
# makes release/vM.Y.Z from rc.2's commit and cuts rc.3 from that branch
# (on release.yml alone, which runs on release/v*) once fixes cherry-picked
# onto it from main are green. After RELEASE_MAX_RC_ATTEMPTS dropped rcs
# the train cuts nothing more of that version and exits 1, so a person
# decides (escalated_version= in RELEASE_TRAIN_OUTPUT).
#
# CI verdicts come from wherever the gate runs (RELEASE_CI_SOURCE). With
# "grain" they are grain CI's runs, GET /api/ci/runs, read through the
# release-gate-read capability (the api:release-gate-read preset:
# GRAIN_API_URL and GRAIN_API_TOKEN, sourced from ~/.grain-api/env when
# unset). With "github" they are GitHub's workflow runs, GET
# /repos/{RELEASE_GITHUB_REPO}/actions/workflows/<suite>.yml/runs (and, for
# an rc, the jobs of each nightly.yml run on it), read with GITHUB_TOKEN
# (actions: read). The grain/release-qualification and grain/rc-hold check
# runs are read from GitHub (checks: read) whenever there is a token, and
# count only when RELEASE_QUALIFICATION_APP, grain's own GitHub App, created
# them. The default is "github" inside GitHub Actions and "grain" anywhere else.
#
# Settings (environment):
#   RELEASE_REMOTE           the remote (origin)
#   RELEASE_NOW              the time, in seconds (now)
#   FORCE                    true cuts an rc.1 inside RELEASE_MIN_INTERVAL_MINUTES
#   RELEASE_SHORT_SOAK_OK    approve only, for a -pending tag from the soak
#                            era: true promotes one whose recorded soak is
#                            under RELEASE_SOAK_HOURS (24) less
#                            RELEASE_INTERVAL_SLACK_HOURS (2)
#   RELEASE_ALLOW_BREAKING   approve only: vX.Y.Z promotes that release
#                            although it carries breaking changes and is
#                            not the major release RELEASE_MAJOR asks for
#   RELEASE_CI_SOURCE        grain or github: where CI verdicts come from
#   RELEASE_GITHUB_REPO      the repository whose workflow runs count
#                            ($GITHUB_REPOSITORY, else ai-on-gke/grain)
#   RELEASE_GITHUB_API_URL   GitHub's API ($GITHUB_API_URL, else api.github.com)
#   RELEASE_GITHUB_PAGES     pages of 100 runs read per workflow (3)
#   RELEASE_RUNS_JSON        a file to read the cut's CI runs from instead
#   RELEASE_QUALIFICATION_RUNS_JSON, RELEASE_QUALIFICATION_CHECKS_JSON
#                            files to read the rc's nightly.yml jobs and
#                            check runs from instead
#   RELEASE_HOLD_CHECKS_JSON a file to read a dropped rc's grain/rc-hold
#                            check runs from instead
#   RELEASE_QUALIFICATION_APP
#                            the GitHub App (slug or numeric id) grain posts
#                            grain/release-qualification and grain/rc-hold as:
#                            only its check runs qualify an rc or lift a hold,
#                            never same-named ones from an Actions workflow or
#                            another app. "none", set explicitly here or in
#                            scripts/release.conf (a forge connection that
#                            is no GitHub App posts no check run), trusts
#                            neither: Stage 1 alone qualifies an rc and no
#                            grain/rc-hold holds the next. Empty here falls
#                            back to scripts/release.conf; unset in both, no
#                            check run is trusted and the train fails closed
#                            (candidate-gate.sh's CHECK_QUALIFICATION_APP)
#   RELEASE_IMAGES           the image publisher (bash scripts/release-images.sh);
#                            "off" publishes nothing
#   RELEASE_NOTES_TASK       "off" files no release-notes task
#   RELEASE_NOTIFY           "off" raises no operator notification
#   RELEASE_ROLLBACK_TO, RELEASE_ROLLBACK_REASON
#                            rollback's --to and --reason
#   RELEASE_TRAIN_OUTPUT     a file to append key=value outputs to, for the
#                            jobs after this one
#   RELEASE_PUSH_GRANT       "train": the run holds only the release train's
#                            own tag grant (.grain/templates/release-train.yml
#                            `push:`, playbook section 0.4), so the script
#                            pushes nothing outside it -- the v*-rc.*,
#                            v*-pending and v*-discarded tags, candidate,
#                            release/v*, and vM.Y.0 and `latest` -- deletes
#                            nothing, and refuses the approve, reject and
#                            rollback modes outright. In train mode, set or
#                            not, it pushes vX.Y.Z and `latest` only for the
#                            version it is promoting, each tagged on the
#                            exact commit of the rc that qualified this run
#   RELEASE_AUTO_PROMOTE     false stages every qualified rc as -pending for
#                            a person instead of releasing it (true)
#   DRY_RUN                  true pushes, publishes and files nothing, and
#                            leaves no tag it made behind in the checkout
#   GIT_USER_NAME, GIT_USER_EMAIL
#                            who the tags are made by (grain-ci[bot]), when
#                            GIT_COMMITTER_NAME and GIT_COMMITTER_EMAIL are
#                            unset; never written to the checkout's config
#   RELEASE_MAJOR, RELEASE_FLOOR_MINOR, RELEASE_NEVER_PROMOTE,
#   RELEASE_MIN_INTERVAL_MINUTES, RELEASE_MAX_RC_ATTEMPTS, RELEASE_AUTO_PROMOTE,
#   RELEASE_QUALIFICATION_APP
#                            override scripts/release.conf as main has it
set -euo pipefail

mode=train
version=""
rollback_to="${RELEASE_ROLLBACK_TO:-}"
rollback_reason="${RELEASE_ROLLBACK_REASON:-}"
case "${1:-}" in
  "") ;;
  approve | reject)
    mode="$1"
    version="${2:-${GRAIN_CI_RELEASE_VERSION:-}}"
    ;;
  verify)
    mode=verify
    version="${2:-}"
    ;;
  rollback)
    mode=rollback
    shift
    while [ $# -gt 0 ]; do
      case "$1" in
        --bad) version="${2:-}"; shift 2 ;;
        --to) rollback_to="${2:-}"; shift 2 ;;
        --reason) rollback_reason="${2:-}"; shift 2 ;;
        -*) echo "release-from-trunk: usage: release-from-trunk.sh rollback --bad vX.Y.Z [--to vA.B.C] [--reason TEXT]" >&2; exit 2 ;;
        *) version="$1"; shift ;;
      esac
    done
    version="${version:-${GRAIN_CI_RELEASE_VERSION:-}}"
    ;;
  *)
    echo "release-from-trunk: usage: release-from-trunk.sh [approve|reject|verify vX.Y.Z | rollback --bad vX.Y.Z [--to vA.B.C] [--reason TEXT]]" >&2
    exit 2
    ;;
esac

scripts_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
remote="${RELEASE_REMOTE:-origin}"
now="${RELEASE_NOW:-$(date +%s)}"
FORCE="${FORCE:-false}"
short_soak_ok="${RELEASE_SHORT_SOAK_OK:-false}"
DRY_RUN="${DRY_RUN:-false}"
soak_hours="${RELEASE_SOAK_HOURS:-24}"
slack_hours="${RELEASE_INTERVAL_SLACK_HOURS:-2}"
images_cmd="${RELEASE_IMAGES:-bash $scripts_dir/release-images.sh}"
push_grant="${RELEASE_PUSH_GRANT:-}"
case "$push_grant" in
  "" | train) ;;
  *) echo "release-from-trunk: RELEASE_PUSH_GRANT is $push_grant, not train" >&2; exit 2 ;;
esac
if [ "$push_grant" = train ] && [ "$mode" != train ]; then
  echo "release-from-trunk: $mode needs the approval's own run: RELEASE_PUSH_GRANT=train holds the release train's tag grant, which never reaches vX.Y.Z, latest or a deletion" >&2
  exit 2
fi
output="${RELEASE_TRAIN_OUTPUT:-}"

# Who makes the tags: through the environment, never the checkout's
# .git/config, so running the train by hand -- dry run or not -- leaves
# the person's own identity where it was.
export GIT_COMMITTER_NAME="${GIT_COMMITTER_NAME:-${GIT_USER_NAME:-grain-ci[bot]}}"
export GIT_COMMITTER_EMAIL="${GIT_COMMITTER_EMAIL:-${GIT_USER_EMAIL:-grain-ci[bot]@users.noreply.github.com}}"
export GIT_AUTHOR_NAME="${GIT_AUTHOR_NAME:-$GIT_COMMITTER_NAME}"
export GIT_AUTHOR_EMAIL="${GIT_AUTHOR_EMAIL:-$GIT_COMMITTER_EMAIL}"

work="$(mktemp -d)"
# A dry run puts every local tag back the way the fetch left it: the tags
# it made are deleted, and the ones it moved or deleted restored.
restore_tags() {
  [ "$DRY_RUN" = true ] && [ -f "$work/tags.before" ] || return 0
  local ref obj
  git for-each-ref --format='%(refname) %(objectname)' refs/tags > "$work/tags.after"
  while read -r ref obj; do
    grep -qxF "$ref $obj" "$work/tags.before" || git update-ref -d "$ref" 2>/dev/null || true
  done < "$work/tags.after"
  while read -r ref obj; do
    grep -qxF "$ref $obj" "$work/tags.after" || git update-ref "$ref" "$obj" 2>/dev/null || true
  done < "$work/tags.before"
}
trap 'restore_tags; rm -rf "$work"' EXIT

say() { echo "release-from-trunk: $*"; }
die() {
  echo "release-from-trunk: $*" >&2
  exit 1
}
emit() { # emit <key> <value>: an output for the jobs after this one
  say "output $1=$2"
  [ -z "$output" ] || printf '%s=%s\n' "$1" "$2" >> "$output"
}

say "fetching $remote's main, candidate and tags"
git fetch -q --force --tags "$remote" "+refs/heads/main:refs/remotes/$remote/main" || die "could not fetch main from $remote"
main_ref="refs/remotes/$remote/main"
git for-each-ref --format='%(refname) %(objectname)' refs/tags > "$work/tags.before"

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
min_interval="${RELEASE_MIN_INTERVAL_MINUTES-$(conf_value RELEASE_MIN_INTERVAL_MINUTES)}"
min_interval="${min_interval:-120}"
max_attempts="${RELEASE_MAX_RC_ATTEMPTS-$(conf_value RELEASE_MAX_RC_ATTEMPTS)}"
max_attempts="${max_attempts:-3}"
auto_promote="${RELEASE_AUTO_PROMOTE-$(conf_value RELEASE_AUTO_PROMOTE)}"
auto_promote="${auto_promote:-true}"
highest_major="$(git tag -l 'v[0-9]*' | sed -nE 's/^v([0-9]+)\.[0-9]+\.[0-9]+(-rc\.[0-9]+)?$/\1/p' | sort -n | tail -n 1)"
major="${RELEASE_MAJOR:-$(conf_value RELEASE_MAJOR)}"
major="${major:-${highest_major:-0}}"
[[ "$major" =~ ^[0-9]+$ ]] || die "RELEASE_MAJOR is \"$major\", which is not a number; nothing was done"
[[ "$floor_minor" =~ ^[0-9]+$ ]] || die "RELEASE_FLOOR_MINOR is \"$floor_minor\", which is not a number; nothing was done"
[[ "$min_interval" =~ ^[0-9]+$ ]] || die "RELEASE_MIN_INTERVAL_MINUTES is \"$min_interval\", which is not a number; nothing was done"
[[ "$max_attempts" =~ ^[1-9][0-9]*$ ]] || die "RELEASE_MAX_RC_ATTEMPTS is \"$max_attempts\", which is not a positive number; nothing was done"
case "$auto_promote" in
  true | false) ;;
  *) die "RELEASE_AUTO_PROMOTE is \"$auto_promote\", not true or false; nothing was done" ;;
esac
# Version numbers never go backwards: a RELEASE_MAJOR behind the newest
# released (or pending) major is stale, so the train releases that major
# instead, as scripts/build-version.sh does. The floor is RELEASE_MAJOR's,
# so a major the conf does not name has none.
released_major="$(git tag -l 'v[0-9]*' | sed -nE 's/^v([0-9]+)\.[0-9]+\.[0-9]+(-pending)?$/\1/p' | sort -n | tail -n 1)"
if [ -n "$released_major" ] && [ "$major" -lt "$released_major" ]; then
  say "RELEASE_MAJOR $major is behind the released v$released_major; releasing major $released_major (raise RELEASE_MAJOR in scripts/release.conf)"
  major="$released_major"
  floor_minor=0
fi
export RELEASE_NEVER_PROMOTE="$never"

never_promotes() { # never_promotes <tag>
  case " $never " in *" $1 "*) return 0 ;; esac
  return 1
}

commit_of() { git rev-parse --verify -q "refs/tags/$1^{commit}"; }
retracted() { commit_of "$1-retracted" >/dev/null; } # retracted <vX.Y.Z>

# The promoted releases: vX.Y.Z tags, newest last.
promoted() { git tag -l 'v[0-9]*' | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V || true; }

# The releases waiting for a person: vX.Y.Z for each vX.Y.Z-pending tag
# whose vX.Y.Z does not exist, newest last.
pending_releases() {
  local t
  git tag -l 'v[0-9]*-pending' | sed -nE 's/^(v[0-9]+\.[0-9]+\.[0-9]+)-pending$/\1/p' | sort -V |
    while read -r t; do
      git rev-parse -q --verify "refs/tags/$t" >/dev/null || printf '%s\n' "$t"
    done
}

# version_gt <a> <b>: whether version a sorts after version b.
version_gt() { [ "$1" != "$2" ] && [ "$(printf '%s\n%s\n' "$1" "$2" | sort -V | tail -n 1)" = "$1" ]; }

rc_tags() { # rc_tags <version>: its rc tags, highest N last
  git tag -l "$1-rc.*" | grep -E "^${1//./\\.}-rc\\.[0-9]+$" | sort -t. -k4,4n || true
}

# target_release: sets released (the newest promoted release; empty if
# there is none), pending (the newest pending release, if any), base (the
# newest of either in RELEASE_MAJOR, which the next version follows),
# notes_from (the newest promoted release RELEASE_NEVER_PROMOTE does not
# name and that is not retracted, which release notes are written from),
# minor_target and target (vM.(P+1).0, raised to RELEASE_FLOOR_MINOR and
# stepping over a minor RELEASE_NEVER_PROMOTE names). Every trunk release
# increments minor (playbook section 7); patch versions (vM.P.Z, Z >= 1)
# are cherry-picked hotfixes cut by scripts/release-patch.sh on
# release/vM.P.Z. A vX.Y.0 tag the list names is a placeholder -- v2.5.0,
# tagged on main only so that the minors the legacy cut used up are never
# reused -- so it still counts for the next version and for the floor, but
# it was never released and there is nothing in it to write notes from.
target_release() {
  local p
  released="$(promoted | tail -n 1)"
  pending="$(pending_releases | tail -n 1)"
  notes_from="$(promoted | while read -r t; do never_promotes "$t" || retracted "$t" || printf '%s\n' "$t"; done | tail -n 1)"
  base="$({ promoted; pending_releases; } | grep -E "^v$major\\." | sort -V | tail -n 1 || true)"
  # A pending release holds its version: the next is the one after it,
  # whether or not a person has approved it yet.
  p="$({ promoted; pending_releases; } | sed -nE "s/^v$major\\.([0-9]+)\\.[0-9]+$/\\1/p" | sort -n | tail -n 1)"
  if [ -n "$p" ]; then
    target_minor=$((p + 1))
  else
    target_minor=0
  fi
  [ "$target_minor" -ge "$floor_minor" ] || target_minor="$floor_minor"
  while never_promotes "v$major.$target_minor.0"; do
    target_minor=$((target_minor + 1))
  done
  minor_target="v$major.$target_minor.0"
  target="$minor_target"
}

# bump_kind <from> <to>: whether the commits in from..to warrant a trunk
# release (playbook section 7) -- "minor" when any commit ships a feature,
# fix, perf, refactor, revert, breaking change or non-conventional subject,
# "none" when every commit is docs, chores, tests, ci, style or build alone.
bump_kind() {
  local record subject type
  local breaking=$'(^|\n)BREAKING[ -]CHANGE:' conventional='^([A-Za-z]+)(\([^)]*\))?(!)?: '
  [ -n "$1" ] || { echo minor; return; }
  while IFS= read -r -d $'\x1e' record; do
    record="${record#$'\n'}"
    [ -n "$record" ] || continue
    subject="${record%%$'\x1f'*}"
    if [[ "${record#*$'\x1f'}" =~ $breaking ]]; then
      echo minor
      return
    fi
    if [[ "$subject" =~ $conventional ]]; then
      type="${BASH_REMATCH[1],,}"
      if [ -n "${BASH_REMATCH[3]}" ]; then
        echo minor
        return
      fi
      case "$type" in
        docs | chore | test | tests | ci | style | build) ;;
        *) echo minor; return ;;
      esac
    else
      echo minor
      return
    fi
  done < <(git log --no-merges --format='%s%x1f%b%x1e' "$1..$2")
  echo none
}

soak_duration() { printf '%dh%02dm' "$(($1 / 3600))" "$(($1 % 3600 / 60))"; } # soak_duration <seconds>

# recorded_soak <release> <rc>: how long the rc behind a soak-era
# <release>-pending soaked, in seconds -- the Soak-Seconds the train
# wrote on the tag, or the time from the rc's tag to the -pending one;
# nothing if neither says.
recorded_soak() {
  local s cut staged
  s="$(git for-each-ref --format='%(contents)' "refs/tags/$1-pending" | sed -nE 's/^Soak-Seconds: ([0-9]+)$/\1/p' | tail -n 1)"
  if [ -z "$s" ] && [ -n "$2" ]; then
    cut="$(git for-each-ref --format='%(taggerdate:unix)' "refs/tags/$2")"
    staged="$(git for-each-ref --format='%(taggerdate:unix)' "refs/tags/$1-pending")"
    [ -z "$cut" ] || [ -z "$staged" ] || s=$((staged - cut))
  fi
  printf '%s' "$s"
}

# qualified <release>: whether <release>-pending records a qualification
# pass rather than a soak.
qualified() { git for-each-ref --format='%(contents)' "refs/tags/$1-pending" | grep -qx 'Qualification: PASS'; }

# promotion_ref <ref>: vX.Y.Z or latest, the refs only the approval moves.
promotion_ref() { [ "$1" = refs/tags/latest ] || [[ "$1" =~ ^refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$ ]]; }
# train_ref <ref>: one the release train's tag grant names (the
# `push:` list of .grain/templates/release-train.yml). The git proxy is
# what enforces it; this is the script refusing first, and saying why.
train_ref() {
  [[ "$1" != refs/tags/*/* ]] || return 1
  case "$1" in
    refs/tags/v*-rc.* | refs/tags/v*-pending | refs/tags/v*-discarded | refs/heads/candidate) return 0 ;;
    refs/tags/latest) return 0 ;;
    refs/tags/v*) [[ "$1" =~ ^refs/tags/v[0-9]{1,2}\.[0-9]{1,3}\.0$ ]] ;;
    refs/heads/release/v*) [[ "$1" != refs/heads/release/*/* ]] ;;
    *) return 1 ;;
  esac
}

# What this run is promoting, set only once its rc has qualified and every
# guard has passed (promote_rc): the release, the rc, and the rc's commit.
promoting=""
promoting_rc=""
promoting_sha=""

# promotion_allowed <ref>: whether a train-mode push of vX.Y.Z or latest is
# the promotion this run is making -- the release it is promoting (or
# latest), tagged locally on the very commit of the rc that qualified, an
# rc that is still not dropped. Anything else -- another version, another
# commit, a run that qualified nothing -- is refused before it is pushed.
promotion_allowed() {
  [ -n "$promoting" ] && [ -n "$promoting_sha" ] && [ -n "$promoting_rc" ] || return 1
  [ "$1" = "refs/tags/$promoting" ] || [ "$1" = refs/tags/latest ] || return 1
  [ "$(commit_of "${1#refs/tags/}" || true)" = "$promoting_sha" ] || return 1
  [ "$(commit_of "$promoting_rc" || true)" = "$promoting_sha" ] || return 1
  [ "${promoting_rc%-rc.*}" = "$promoting" ] || return 1
  ! commit_of "$promoting_rc-discarded" >/dev/null
}

# check_push <git push args...>: refuse, in train mode, a push of vX.Y.Z or
# latest that is not this run's promotion of a qualified rc, and under
# RELEASE_PUSH_GRANT=train anything outside the grant.
check_push() {
  [ "$mode" = train ] || return 0
  local arg dst src del=false
  for arg; do
    case "$arg" in --delete | -d) del=true ;; esac
  done
  for arg; do
    case "$arg" in -*) continue ;; esac
    dst="${arg#+}"
    dst="${dst##*:}"
    if promotion_ref "$dst"; then
      [ "$del" = false ] || die "the train never deletes $dst"
      src="${arg#+}"
      src="${src%:*}"
      if ! promotion_allowed "$dst" || [ "$(git rev-parse -q --verify "$src^{commit}" || true)" != "$promoting_sha" ]; then
        die "the train never pushes $dst except to release an rc that qualified, on that rc's own commit; not pushing it"
      fi
    fi
    [ "$push_grant" = train ] || continue
    train_ref "$dst" || die "$dst is outside the release train's tag grant (.grain/templates/release-train.yml push:); not pushing it"
    [ "$del" = false ] || die "the release train's tag grant deletes nothing; not deleting $dst"
  done
  return 0
}

push() { # push <git push args...>
  check_push "$@"
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

remote_branch() { git ls-remote --exit-code -q --heads "$remote" "refs/heads/$1" >/dev/null 2>&1; } # remote_branch <name>

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

ci_source="${RELEASE_CI_SOURCE:-}"
if [ -z "$ci_source" ]; then
  ci_source=grain
  [ "${GITHUB_ACTIONS:-}" != true ] || ci_source=github
fi
case "$ci_source" in
  grain | github) ;;
  *) die "RELEASE_CI_SOURCE is $ci_source, not grain or github" ;;
esac

github_token="${GITHUB_TOKEN:-${GH_TOKEN:-}}"
github_api="${RELEASE_GITHUB_API_URL:-${GITHUB_API_URL:-https://api.github.com}}"
github_repo="${RELEASE_GITHUB_REPO:-${GITHUB_REPOSITORY:-${GRAIN_CI_REPO:-ai-on-gke/grain}}}"
# The App trusted for grain/release-qualification and grain/rc-hold, from
# the environment or else scripts/release.conf as main has it. "none" --
# only ever an explicit setting, never a default -- trusts no check run:
# Stage 1 alone qualifies, and nothing holds. Empty in both trusts nothing
# and fails closed, so an unconfigured deployment never promotes on Stage 1.
qualification_app="${RELEASE_QUALIFICATION_APP:-$(conf_value RELEASE_QUALIFICATION_APP)}"
github_get() { # github_get <path> <out>
  curl -fsS --max-time 60 -H "Authorization: Bearer $github_token" -H "Accept: application/vnd.github+json" \
    "${github_api%/}/repos/$github_repo/$1" -o "$2"
}

# A branch's runs of each of CANDIDATE_GATE_REQUIRED on GitHub Actions,
# as one listing of workflow runs (scripts/candidate-gate.sh reads either
# shape).
github_runs() { # github_runs <out> <branch>
  if [ -n "${RELEASE_RUNS_JSON:-}" ]; then
    [ -r "$RELEASE_RUNS_JSON" ] || die "RELEASE_RUNS_JSON names $RELEASE_RUNS_JSON, which is not a readable file"
    cp "$RELEASE_RUNS_JSON" "$1"
    return 0
  fi
  local pages wf page n
  [ -n "$github_token" ] || die "no GitHub token to read $2's workflow runs with: set GITHUB_TOKEN (actions: read), RELEASE_RUNS_JSON, or RELEASE_CI_SOURCE=grain"
  pages="${RELEASE_GITHUB_PAGES:-3}"
  : > "$work/github-runs.jsonl"
  for wf in ${CANDIDATE_GATE_REQUIRED-tests release}; do
    for ((page = 1; page <= pages; page++)); do
      github_get "actions/workflows/$wf.yml/runs?branch=$2&per_page=100&page=$page" "$work/github-page.json" ||
        die "could not read $wf.yml's runs on $2 from $github_api"
      n="$(jq '.workflow_runs | length' "$work/github-page.json")" || die "$github_api's answer for $wf.yml is not a listing of workflow runs"
      cat "$work/github-page.json" >> "$work/github-runs.jsonl"
      [ "$n" -ge 100 ] || break
    done
  done
  jq -s '{workflow_runs: (map(.workflow_runs) | add // [])}' "$work/github-runs.jsonl" > "$1"
  say "read $(jq '.workflow_runs | length' "$1") GitHub Actions runs of $github_repo's ${CANDIDATE_GATE_REQUIRED-tests release} on $2"
}

# qualification_json <rc> <sha>: what check-qualification reads -- the
# jobs of every nightly.yml run on the rc ($work/qual-runs.json) and the
# check runs on its commit ($work/qual-checks.json, when there is a way to
# read them; qual_checks names it then).
qual_checks=""
qualification_json() {
  local id app_id=""
  [[ ! "$qualification_app" =~ ^[0-9]+$ ]] || app_id="&app_id=$qualification_app"
  if [ -n "${RELEASE_QUALIFICATION_RUNS_JSON:-}" ]; then
    gate_json RELEASE_QUALIFICATION_RUNS_JSON "" "$work/qual-runs.json"
  elif [ "$ci_source" = github ]; then
    [ -n "$github_token" ] || die "no GitHub token to read nightly.yml's runs on $1 with: set GITHUB_TOKEN (actions: read) or RELEASE_QUALIFICATION_RUNS_JSON"
    github_get "actions/workflows/nightly.yml/runs?head_sha=$2&per_page=100" "$work/qual-wf.json" ||
      die "could not read nightly.yml's runs on $1 from $github_api"
    : > "$work/qual-jobs.jsonl"
    for id in $(jq -r '.workflow_runs[].id' "$work/qual-wf.json"); do
      github_get "actions/runs/$id/jobs?filter=all&per_page=100" "$work/qual-page.json" ||
        die "could not read the jobs of nightly.yml run $id from $github_api"
      cat "$work/qual-page.json" >> "$work/qual-jobs.jsonl"
    done
    jq -s '{jobs: (map(.jobs) | add // [])}' "$work/qual-jobs.jsonl" > "$work/qual-runs.json"
  else
    gate_json RELEASE_QUALIFICATION_RUNS_JSON "/api/ci/runs?repo=${GRAIN_CI_REPO:-ai-on-gke/grain}&ref=$1&limit=200" "$work/qual-runs.json"
  fi
  if [ "$qualification_app" = none ]; then
    say "RELEASE_QUALIFICATION_APP=none, so no grain/release-qualification check run is read for $1: Stage 1 alone qualifies it"
  elif [ -n "${RELEASE_QUALIFICATION_CHECKS_JSON:-}" ]; then
    gate_json RELEASE_QUALIFICATION_CHECKS_JSON "" "$work/qual-checks.json"
    qual_checks="$work/qual-checks.json"
  elif [ -n "$github_token" ]; then
    # GitHub filters by app only by id; candidate-gate.sh checks the app
    # of every check run it judges either way.
    github_get "commits/$2/check-runs?check_name=grain%2Frelease-qualification&filter=all&per_page=100$app_id" "$work/qual-checks.json" ||
      die "could not read the check runs on $1 from $github_api"
    qual_checks="$work/qual-checks.json"
  else
    say "no GitHub token and no RELEASE_QUALIFICATION_CHECKS_JSON (a task writes it with \`grain ci checks $2 --name grain/release-qualification --format github\`), so no grain/release-qualification check run to read for $1"
  fi
}

# rc_hold_summary <floor_sha>: if the newest grain/rc-hold check run on
# floor_sha from RELEASE_QUALIFICATION_APP is not completed, prints its
# summary; otherwise prints nothing. No check at all means no hold, and so
# does an explicit RELEASE_QUALIFICATION_APP=none: no app is trusted to post
# one. An empty one refuses rather than trust another app's check run.
rc_hold_summary() {
  local app_id="" hold_checks="" pages page n flooded=false
  if [ "$qualification_app" = none ]; then
    say "RELEASE_QUALIFICATION_APP=none, so no grain/rc-hold check run on ${1:0:12} is trusted; not waiting on one" >&2
    return 0
  fi
  [[ ! "$qualification_app" =~ ^[0-9]+$ ]] || app_id="&app_id=$qualification_app"
  if [ -n "${RELEASE_HOLD_CHECKS_JSON:-}" ]; then
    [ -n "$qualification_app" ] ||
      die "RELEASE_QUALIFICATION_APP is unset, so no grain/rc-hold check run can be trusted: set it to the slug or id of the GitHub App grain posts it as, or to none in scripts/release.conf"
    gate_json RELEASE_HOLD_CHECKS_JSON "" "$work/hold-checks.json"
    hold_checks="$work/hold-checks.json"
  elif [ -n "$github_token" ]; then
    [ -n "$qualification_app" ] ||
      die "RELEASE_QUALIFICATION_APP is unset, so no grain/rc-hold check run can be trusted: set it to the slug or id of the GitHub App grain posts it as, or to none in scripts/release.conf"
    pages="${RELEASE_GITHUB_PAGES:-3}"
    : > "$work/hold-checks.jsonl"
    flooded=true
    for ((page = 1; page <= pages; page++)); do
      github_get "commits/$1/check-runs?check_name=grain%2Frc-hold&filter=all&per_page=100&page=$page$app_id" "$work/hold-page.json" ||
        die "could not read the grain/rc-hold check runs on ${1:0:12} from $github_api"
      n="$(jq 'if type == "object" and (.check_runs | type == "array") then .check_runs | length else error("not a check run listing") end' "$work/hold-page.json" 2>/dev/null)" ||
        die "$github_api's answer for grain/rc-hold on ${1:0:12} is not a listing of check runs"
      cat "$work/hold-page.json" >> "$work/hold-checks.jsonl"
      if [ "$n" -lt 100 ]; then
        flooded=false
        break
      fi
    done
    jq -s '{check_runs: (map(.check_runs) | add // [])}' "$work/hold-checks.jsonl" > "$work/hold-checks.json"
    hold_checks="$work/hold-checks.json"
  else
    say "no GitHub token, so no grain/rc-hold check run to read on ${1:0:12}; not waiting on one" >&2
    return 0
  fi
  # shellcheck disable=SC2016 # jq, not shell
  jq -r --arg sha "${1,,}" --arg app "$qualification_app" --argjson flooded "$flooded" '
    def epoch:
      if . == null or . == "" then 0
      else sub("\\.[0-9]+"; "")
        | if test("Z$") then fromdateiso8601
          else (capture("^(?<d>.*)(?<s>[+-])(?<h>[0-9]{2}):?(?<m>[0-9]{2})$") as $c
            | ($c.d + "Z" | fromdateiso8601) - (($c.s + "1" | tonumber) * (($c.h | tonumber) * 3600 + ($c.m | tonumber) * 60)))
          end
      end;
    def from_app: $app != "" and (.app | type == "object")
      and ((.app.slug // "") == $app or ((.app.id // "") | tostring) == $app);
    def sha_ok: (. // "" | ascii_downcase) as $h | $h == "" or ($sha | startswith($h)) or ($h | startswith($sha));
    (if type == "array" then .
     elif type == "object" and (.check_runs | type == "array") then .check_runs
     else error("not a check run listing") end) as $runs
    | [ $runs[]
        | select(.name == "grain/rc-hold")
        | select(.head_sha | sha_ok)
        | select(from_app) ]
    | if length == 0 and $flooded then error("all fetched grain/rc-hold pages were from other apps") else . end
    | sort_by([(.id // 0), ((.started_at // .startedAt) | epoch)])
    | last // empty
    | select((.status // "") != "completed")
    | ([.output.summary, .output.title, .status]
        | map(select(type == "string") | gsub("[\\r\\n]+"; " ") | sub("^\\s+"; "") | sub("\\s+$"; "") | select(length > 0))
        | first // "in_progress")
  ' "$hold_checks" || die "could not parse grain/rc-hold check runs on ${1:0:12}"
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

# pending_body <release> <rc> <verdict> <notes task>: what the
# notification says.
pending_body() {
  printf '%s passed qualification (Stage 1 nightly.yml, Stage 2 grain/release-qualification) and is staged as %s-pending: images built and published as :%s-pending, nothing left to build or test.\n\n' \
    "$2" "$1" "$1"
  printf 'Qualification: %s\n' "${3:-PASS}"
  printf 'Release notes: %s\n\n' "${4:-the release-notes task for $1 (docs/releases/$1.md); merge it before approving}"
  printf 'Approve tags %s and moves latest onto it; Reject drops %s and the next run cuts its next rc. ' "$1" "$2"
  printf 'Meanwhile the train may cut and qualify the next version'"'"'s rc, but it never promotes or supersedes %s: it waits for your answer.\n' "$1"
}

# auto_promote_blocker <release> <rc> <sha>: why the train may not release
# <rc> as <release> itself, so a person has to (it is staged as -pending);
# prints nothing when it may.
auto_promote_blocker() {
  local status=0 out
  if [ "$auto_promote" != true ]; then
    echo "RELEASE_AUTO_PROMOTE is false"
    return 0
  fi
  if never_promotes "$1" || never_promotes "$2"; then
    echo "RELEASE_NEVER_PROMOTE names it"
    return 0
  fi
  if [ -z "$base" ]; then
    echo "it is the first release of major $major, and a major bump is a person's to release"
    return 0
  fi
  if ! git merge-base --is-ancestor "$3" "$main_ref" &&
    ! { remote_branch "release/$1" &&
      git fetch -q --force "$remote" "+refs/heads/release/$1:refs/remotes/$remote/release/$1" &&
      git merge-base --is-ancestor "$3" "refs/remotes/$remote/release/$1"; }; then
    echo "${3:0:12} is on neither main nor release/$1"
    return 0
  fi
  [ -n "$notes_from" ] || return 0
  out="$(bash "$scripts_dir/release-notes.sh" check-breaking "$1" "$notes_from" "$3" 2>&1)" || status=$?
  printf '%s\n' "$out" | sed 's/^/  /' >&2
  case "$status" in
    0) ;;
    3) echo "it carries breaking changes since $notes_from and is not the major release RELEASE_MAJOR asks for" ;;
    *) echo "its breaking changes since $notes_from could not be checked (release-notes.sh check-breaking exited $status)" ;;
  esac
}

# promote_rc <release> <rc> <sha>: release a qualified rc -- vX.Y.Z on its
# commit, latest moved onto it, its images promoted and its notes filed.
promote_rc() {
  promoting="$1" promoting_rc="$2" promoting_sha="$3"
  say "phase 1: $2 qualified; releasing it as $1 and moving latest onto it (${3:0:12})"
  git tag -a "$1" "$3" -m "Release $1" \
    -m "Promoted from $2, which passed Stage 1 (nightly.yml) and Stage 2 (grain/release-qualification), by the release train with no person's approval (design/playbook/releases.playbook.md, section 1)." \
    -m "Qualification: PASS" -m "Promoted-From: $2"
  git tag -fa latest "$3" -m "Latest release $1"
  # Both are checked before either is pushed: never a release without latest.
  check_push "refs/tags/$1" "+refs/tags/latest"
  push "refs/tags/$1" || die "could not push $1; latest was not moved"
  push --force "refs/tags/latest" || die "pushed $1, but could not move latest onto it"
  emit promoted_version "$1"
  images promote "$1" || die "tagged $1 and latest, but could not publish their images: re-run .github/workflows/publish-release.yml"
  notes_task=""
  file_release_notes "$1" "$notes_from"
  say "phase 1: $1 is released (after ${released:-nothing}); publish-release.yml publishes its images and GitHub Release from the tag"
}

# --- verify: is vX.Y.Z a release the train made from a qualified rc? ---

# .github/workflows/publish-release.yml runs this on every vX.Y.0 tag push
# before it publishes anything: exit 0 when the tag says the train released
# it (Promoted-From: <rc>, Qualification: PASS), the rc is on the same
# commit and not dropped, the commit is on main or release/vX.Y.Z,
# check-qualification still passes the rc, and none of the guards that make
# the train stage an rc for a person instead (auto_promote_blocker: a major
# bump, breaking changes, RELEASE_NEVER_PROMOTE, RELEASE_AUTO_PROMOTE=false)
# applies; 4 when the tag is not the
# train's (a person's approval, which promote-release.yml publishes); 1
# when it claims to be the train's and any of that does not hold.
if [ "$mode" = verify ]; then
  [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "verify needs a release, vX.Y.Z (got \"$version\")"
  sha="$(commit_of "$version" || true)"
  [ -n "$sha" ] || die "$remote has no $version tag"
  contents="$(git for-each-ref --format='%(contents)' "refs/tags/$version")"
  from_rc="$(printf '%s\n' "$contents" | sed -n 's/^Promoted-From: //p' | tail -n 1)"
  if [ -z "$from_rc" ]; then
    say "verify: $version names no Promoted-From rc: it was not released by the train"
    exit 4
  fi
  [[ "$from_rc" =~ ^${version//./\\.}-rc\.[0-9]+$ ]] || die "verify: $version names $from_rc, which is not one of its rcs"
  printf '%s\n' "$contents" | grep -qx 'Qualification: PASS' || die "verify: $version records no qualification pass"
  [ "$(commit_of "$from_rc" || true)" = "$sha" ] || die "verify: $version (${sha:0:12}) is not on $from_rc's commit"
  ! commit_of "$from_rc-discarded" >/dev/null || die "verify: $from_rc was dropped ($from_rc-discarded)"
  if ! git merge-base --is-ancestor "$sha" "$main_ref" &&
    ! { remote_branch "release/$version" &&
      git fetch -q --force "$remote" "+refs/heads/release/$version:refs/remotes/$remote/release/$version" &&
      git merge-base --is-ancestor "$sha" "refs/remotes/$remote/release/$version"; }; then
    die "verify: ${sha:0:12} is on neither main nor release/$version"
  fi
  qualification_json "$from_rc" "$sha"
  q_status=0
  q_out="$(CHECK_QUALIFICATION_APP="$qualification_app" bash "$scripts_dir/candidate-gate.sh" check-qualification --rc-tag "$from_rc" --rc-sha "$sha" \
    --runs-json "$work/qual-runs.json" ${qual_checks:+--checks-json "$qual_checks"} 2>&1)" || q_status=$?
  printf '%s\n' "$q_out" | sed 's/^/  /'
  [ "$q_status" = 0 ] || die "verify: $from_rc does not pass qualification now (check-qualification exited $q_status); $version is not published"
  # The tag only says the train made it; the train's tag grant reaches any
  # vM.Y.0 on main, so check it is a release the train would have made
  # itself rather than staged for a person: the guards promote_rc runs
  # behind (auto_promote_blocker), judged as they were before the tag
  # existed -- so with it hidden, and RELEASE_MAJOR recomputed without it.
  tag_obj="$(git rev-parse "refs/tags/$version")"
  git update-ref -d "refs/tags/$version"
  major="${RELEASE_MAJOR:-$(conf_value RELEASE_MAJOR)}"
  major="${major:-${highest_major:-0}}"
  released_major="$(git tag -l 'v[0-9]*' | sed -nE 's/^v([0-9]+)\.[0-9]+\.[0-9]+(-pending)?$/\1/p' | sort -n | tail -n 1)"
  if [ -n "$released_major" ] && [ "$major" -lt "$released_major" ]; then
    major="$released_major"
  fi
  target_release
  blocker=""
  if [ "${version%%.*}" != "v$major" ]; then
    blocker="it is not in major $major, the one RELEASE_MAJOR has the train release"
  else
    blocker="$(auto_promote_blocker "$version" "$from_rc" "$sha")"
  fi
  git update-ref "refs/tags/$version" "$tag_obj"
  [ -z "$blocker" ] || die "verify: the train may not release $from_rc as $version itself ($blocker); $version is not published"
  say "verify: $version is the train's release of $from_rc (${sha:0:12}), which qualified"
  emit verified_release "$version"
  exit 0
fi

# --- approve / reject / rollback: a person's answer ---

if [ "$mode" != train ]; then
  if [ "$mode" = rollback ]; then
    [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "rollback needs the release to retract, --bad vX.Y.Z (got \"$version\")"
  else
    [[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "$mode needs the pending release, vX.Y.Z (got \"$version\")"
  fi
  if [ "${GRAIN_CI_REQUESTED_BY_KIND:-}" != human ]; then
    die "only a person may $mode $version: GRAIN_CI_REQUESTED_BY_KIND is \"${GRAIN_CI_REQUESTED_BY_KIND:-}\", not \"human\" -- use \`grain release $mode $version\` or the Upgrade pane, which run .grain/workflows/promote-release.yml for a person; nothing was done"
  fi
  by="${GRAIN_CI_REQUESTED_BY:-a person}"
fi

if [ "$mode" = rollback ]; then
  bad_sha="$(commit_of "$version" || true)"
  [ -n "$bad_sha" ] || die "$version is not a release: $remote has no $version tag; nothing was done"
  if [ -z "$rollback_to" ]; then
    rollback_to="$(promoted | while read -r t; do
      version_gt "$version" "$t" || continue
      never_promotes "$t" || retracted "$t" || printf '%s\n' "$t"
    done | tail -n 1)"
    [ -n "$rollback_to" ] || die "there is no release before $version to roll latest back to; nothing was done"
  fi
  [[ "$rollback_to" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "--to $rollback_to is not a release, vX.Y.Z; nothing was done"
  to_sha="$(commit_of "$rollback_to" || true)"
  [ -n "$to_sha" ] || die "--to $rollback_to is not a release: $remote has no $rollback_to tag; nothing was done"
  version_gt "$version" "$rollback_to" || die "--to $rollback_to is not older than $version; rollback only goes back; nothing was done"
  ! retracted "$rollback_to" || die "--to $rollback_to is itself retracted; nothing was done"
  ! never_promotes "$rollback_to" || die "--to $rollback_to is named by RELEASE_NEVER_PROMOTE; nothing was done"
  latest_sha="$(commit_of latest || true)"
  if retracted "$version" && [ "$latest_sha" = "$to_sha" ]; then
    say "rollback: $version is already retracted and latest is on $rollback_to; nothing to do"
    exit 0
  fi
  say "rollback: $by retracts $version (${bad_sha:0:12}) and moves latest back to $rollback_to (${to_sha:0:12})${rollback_reason:+: $rollback_reason}"
  if ! retracted "$version"; then
    git tag -a "$version-retracted" "$bad_sha" -m "Release $version retracted" \
      -m "Retracted by $by${rollback_reason:+: $rollback_reason}" \
      -m "$version stays tagged: a release is never deleted or moved. latest goes back to $rollback_to." \
      -m "Rolled-Back-To: $rollback_to"
    push "refs/tags/$version-retracted" || die "could not push $version-retracted; latest was not moved"
  fi
  emit retracted_version "$version"
  git tag -fa latest "$to_sha" -m "Latest release $rollback_to" -m "Rolled back from $version, retracted by $by"
  push --force "refs/tags/latest" || die "pushed $version-retracted, but could not move latest back to $rollback_to"
  emit latest_version "$rollback_to"
  images retract "$version" || die "moved latest back to $rollback_to, but could not mark $version's images retracted: run \`release-images.sh retract $version\`"
  images promote "$rollback_to" || die "moved latest back to $rollback_to, but could not move the :latest images: run \`release-images.sh promote $rollback_to\`"
  say "rollback: $version is retracted and latest is $rollback_to again. Still for a person: add $version to RELEASE_NEVER_PROMOTE in scripts/release.conf, mark its GitHub Release a pre-release, and fix forward on main -- the next release is the next minor"
  exit 0
fi

if [ "$mode" != train ]; then
  pending_sha="$(commit_of "$version-pending" || true)"
  [ -n "$pending_sha" ] || die "$version is not pending: $remote has no $version-pending tag; nothing was done"
  if commit_of "$version" >/dev/null; then
    die "$version is already promoted; nothing was done"
  fi
  from_rc="$(git tag --points-at "$pending_sha" -l "$version-rc.*" | grep -E "^${version//./\\.}-rc\\.[0-9]+$" | sort -t. -k4,4n | tail -n 1 || true)"
  if [ "$mode" = approve ]; then
    # A person approves a version name, not a commit, so what it releases
    # has to be a commit the train could have staged: one on main, or on
    # the release's own convergence branch -- never a -pending tag pushed
    # onto anything else.
    on_branch=""
    if git merge-base --is-ancestor "$pending_sha" "$main_ref"; then
      on_branch=main
    elif remote_branch "release/$version"; then
      git fetch -q --force "$remote" "+refs/heads/release/$version:refs/remotes/$remote/release/$version" ||
        die "could not fetch release/$version from $remote; nothing was done"
      if git merge-base --is-ancestor "$pending_sha" "refs/remotes/$remote/release/$version"; then
        on_branch="release/$version"
      fi
    fi
    [ -n "$on_branch" ] || die "$version-pending is on ${pending_sha:0:12}, which is on neither main nor release/$version: it is no commit the train stages, so it is not promoted; reject it; nothing was done"
    prev="$(promoted | tail -n 1)"
    override=""
    said=""
    if qualified "$version"; then
      said="qualified"
    else
      # A -pending tag staged by the soak-era train: its soak still counts.
      min_soak=$(((soak_hours - slack_hours) * 3600))
      soaked="$(recorded_soak "$version" "$from_rc")"
      said="${soaked:+soaked $(soak_duration "$soaked") of ${soak_hours}h}"
      if [ -z "$soaked" ] || [ "$soaked" -lt "$min_soak" ]; then
        if [ "$short_soak_ok" != true ]; then
          die "$version ${said:-has no recorded soak or qualification}, under the $((soak_hours - slack_hours))h minimum: approve it with short_soak_ok: true if that is meant, or reject it; nothing was done"
        fi
        override="; short soak (${said:-no recorded soak}, under the $((soak_hours - slack_hours))h minimum) accepted with short_soak_ok: true"
        say "approve: $version ${said:-has no recorded soak}, under the $((soak_hours - slack_hours))h minimum; $by passed short_soak_ok: true"
      fi
    fi
    # Breaking changes ship only in the major release RELEASE_MAJOR asks
    # for, or with a person's RELEASE_ALLOW_BREAKING=<version> (playbook
    # section 8): counted from the release its notes are written from.
    notes_base="$(promoted | while read -r t; do never_promotes "$t" || retracted "$t" || printf '%s\n' "$t"; done | tail -n 1)"
    if [ -n "$notes_base" ]; then
      breaking_status=0
      breaking_out="$(bash "$scripts_dir/release-notes.sh" check-breaking "$version" "$notes_base" "$pending_sha" 2>&1)" || breaking_status=$?
      printf '%s\n' "$breaking_out" | sed 's/^/  /'
      case "$breaking_status" in
        0) ;;
        3) die "$version carries breaking changes since $notes_base and is not the major release RELEASE_MAJOR asks for: reject it and raise RELEASE_MAJOR in scripts/release.conf, or approve it with RELEASE_ALLOW_BREAKING=$version; nothing was done" ;;
        *) die "could not check $version for breaking changes since $notes_base (release-notes.sh check-breaking exited $breaking_status); nothing was done" ;;
      esac
      [ "${RELEASE_ALLOW_BREAKING:-}" != "$version" ] || ! printf '%s\n' "$breaking_out" | grep -q 'RELEASE_ALLOW_BREAKING=.* overrides' ||
        override="$override; breaking changes since $notes_base accepted with RELEASE_ALLOW_BREAKING=$version"
    fi
    say "approve: $by approved $version (${from_rc:-?}, ${pending_sha:0:12}${said:+, $said}); promoting it to $version and latest"
    git tag -a "$version" "$pending_sha" -m "Release $version" -m "Promoted from ${from_rc:-$version-pending}, approved by $by$override"
    git tag -fa latest "$pending_sha" -m "Latest release $version"
    push "refs/tags/$version" || die "could not push $version; latest was not moved and $version-pending stands"
    push --force "refs/tags/latest" || die "pushed $version, but could not move latest onto it"
    emit promoted_version "$version"
    images promote "$version" || die "tagged $version and latest, but could not publish their images: re-run .grain/workflows/promote.yml"
    push --delete "refs/tags/$version-pending" || die "promoted $version, but could not delete $version-pending"
    git tag -d "$version-pending" >/dev/null
    # The convergence branch has done its job: main carries every fix.
    # Patch branches (release/vX.Y.Z, Z >= 1) stay on the remote for
    # patch-tag-guard.sh (docs/release-candidate-management.md, section 9).
    if [[ "$version" == *.0 ]] && remote_branch "release/$version"; then
      push --delete "refs/heads/release/$version" || die "promoted $version, but could not delete release/$version"
      say "approve: deleted the convergence branch release/$version"
    fi
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
  # An rc of a later version was cut on top of the release just rejected,
  # and would be versioned past a release that never shipped: drop it too.
  vmajor="$(printf '%s' "$version" | cut -d. -f1)"
  for later in $(git tag -l "$vmajor.*-rc.*" | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+-rc\.[0-9]+$' | sort -V || true); do
    version_gt "${later%-rc.*}" "$version" || continue
    commit_of "${later%-rc.*}" >/dev/null && continue
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
say "major $major (floor minor $floor_minor); newest promoted release ${released:-none}${pending:+; $pending pending approval}; next minor $minor_target; notes from ${notes_from:-the start}"
if [ -n "$pending" ]; then
  pending_sha="$(commit_of "$pending-pending")"
  pending_rc="$(git tag --points-at "$pending_sha" -l "$pending-rc.*" | sort -t. -k4,4n | tail -n 1 || true)"
  say "$pending (${pending_sha:0:12}, from ${pending_rc:-?}) is pending a person's approval; this run neither promotes nor supersedes it"
  raise_pending "$pending" "$pending_rc" "$(pending_body "$pending" "$pending_rc" "" "")"
fi

# --- Phase 1 ---

floor=""
next_n=1
rc="$(rc_tags "$target" | tail -n 1)"
if [ -n "$rc" ]; then
  rc_sha="$(commit_of "$rc")"
  n="${rc##*.}"
  if commit_of "$rc-discarded" >/dev/null; then
    say "phase 1: $rc (${rc_sha:0:12}) was dropped; the next rc of $target is rc.$((n + 1))"
    floor="$rc_sha"
    next_n=$((n + 1))
  elif never_promotes "$rc"; then
    say "phase 1: RELEASE_NEVER_PROMOTE names $rc; dropping it without qualifying it"
    git tag -fa "$rc-discarded" "$rc_sha" -m "Release candidate $rc dropped: RELEASE_NEVER_PROMOTE names it"
    push "refs/tags/$rc-discarded"
    images discard-rc "$rc" || die "could not mark $rc's images discarded (its -discarded tag is pushed)"
    emit discarded_rc "$rc"
    floor="$rc_sha"
    next_n=$((n + 1))
  else
    say "phase 1: $rc (${rc_sha:0:12}) is in qualification; asking the qualification gate"
    qualification_json "$rc" "$rc_sha"
    q_status=0
    q_out="$(CHECK_QUALIFICATION_APP="$qualification_app" bash "$scripts_dir/candidate-gate.sh" check-qualification --rc-tag "$rc" --rc-sha "$rc_sha" \
      --runs-json "$work/qual-runs.json" ${qual_checks:+--checks-json "$qual_checks"} 2>&1)" || q_status=$?
    printf '%s\n' "$q_out" | sed 's/^/  /'
    case "$q_status" in
      0)
        if [ -n "$pending" ]; then
          say "phase 1: $rc qualified, but $pending is still pending approval; $rc waits behind it and is never staged over it"
          exit 0
        fi
        blocker="$(auto_promote_blocker "$target" "$rc" "$rc_sha")"
        if [ -z "$blocker" ]; then
          # A minimum interval between releases (playbook section 7): the
          # rc stays qualified, and a later run releases it.
          if [ -n "$released" ] && [ "$min_interval" -gt 0 ]; then
            last_ts="$(git for-each-ref --format='%(taggerdate:unix)' "refs/tags/$released")"
            [ -n "$last_ts" ] || last_ts="$(git log -1 --format=%ct "$(commit_of "$released")")"
            if [ $((now - last_ts)) -lt $((min_interval * 60)) ]; then
              say "phase 1: $rc qualified, but $released was released $(((now - last_ts) / 60))m ago, under RELEASE_MIN_INTERVAL_MINUTES ($min_interval); a later run releases it"
              emit promotion_waits "$target"
              exit 0
            fi
          fi
          promote_rc "$target" "$rc" "$rc_sha"
        else
          say "phase 1: $rc qualified, but the train does not release it itself: $blocker; staging $target-pending for a person to approve ($target and latest are not touched)"
          emit promotion_blocked "$blocker"
          git tag -a "$target-pending" "$rc_sha" -m "Release $target pending approval" \
            -m "From $rc, which passed Stage 1 (nightly.yml) and Stage 2 (grain/release-qualification)." \
            -m "A person approves it (grain release approve $target) or rejects it (grain release reject $target)." \
            -m "Qualification: PASS"
          push "refs/tags/$target-pending" || die "could not push $target-pending"
          emit pending_version "$target"
          images pending "$target" || die "tagged $target-pending, but could not stage its images: run \`release-images.sh pending $target\`"
          notes_task=""
          file_release_notes "$target" "$notes_from"
          raise_pending "$target" "$rc" "$(pending_body "$target" "$rc" PASS "$notes_task")"
        fi
        floor="$rc_sha"
        next_n=1
        target_release
        ;;
      2 | 3)
        reruns="$(printf '%s\n' "$q_out" | sed -nE 's/^RERUN ([^ ]+) (.+) (flake|infra)$/\1:\2:\3/p' | paste -sd, -)"
        verdict=pending
        [ "$q_status" = 2 ] || verdict=infra
        emit qualification "$verdict"
        emit qualifying_rc "$rc"
        [ -z "$reruns" ] || emit qualification_rerun "$reruns"
        if printf '%s\n' "$q_out" | grep -q 'stalled after'; then
          emit qualification_stalled "$rc"
        fi
        say "phase 1: $rc's qualification is $verdict${reruns:+; to re-run: $reruns}; one rc qualifies at a time, so nothing is cut"
        exit 0
        ;;
      1)
        summary="$(printf '%s\n' "$q_out" | sed -n 's/^FAIL //p' | paste -sd';' - | sed 's/;/; /g')"
        say "phase 1: $rc failed qualification; dropping it"
        git tag -fa "$rc-discarded" "$rc_sha" -m "Release candidate $rc failed qualification" -m "$summary"
        push "refs/tags/$rc-discarded" || die "could not push $rc-discarded"
        images discard-rc "$rc" || die "could not mark $rc's images discarded (its -discarded tag is pushed)"
        emit discarded_rc "$rc"
        emit qualification fail
        floor="$rc_sha"
        next_n=$((n + 1))
        ;;
      *)
        die "the qualification gate could not judge $rc (exit $q_status)"
        ;;
    esac
  fi
fi
if [ -z "$floor" ]; then
  if [ -n "$pending" ]; then
    floor="$(commit_of "$pending-pending")"
    say "phase 1: nothing in qualification; the next rc must be after the pending $pending (${floor:0:12})"
  elif [ -n "$released" ]; then
    floor="$(commit_of "$released")"
    say "phase 1: nothing in qualification; the next rc must be after $released (${floor:0:12})"
  else
    say "phase 1: nothing in qualification and nothing promoted yet"
  fi
fi

# --- Phase 2 ---

# Bounded attempts (playbook section 5): past the last, a person decides.
if [ "$next_n" -gt "$max_attempts" ]; then
  emit escalated_version "$target"
  die "$target has had $((next_n - 1)) release candidates dropped, RELEASE_MAX_RC_ATTEMPTS ($max_attempts); the train cuts no more of it until a person resets or advances it"
fi

# Where the rc is cut from: main, or -- for rc.3 and after -- the
# release/vX.Y.Z convergence branch made from the rc before it.
cut_ref="$main_ref"
cut_branch=main
required="${CANDIDATE_GATE_REQUIRED-tests release}"
if [ "$next_n" -ge 3 ]; then
  cut_branch="release/$target"
  required="${RELEASE_BRANCH_REQUIRED-release}"
  if remote_branch "$cut_branch"; then
    git fetch -q --force "$remote" "+refs/heads/$cut_branch:refs/remotes/$remote/$cut_branch" || die "could not fetch $cut_branch from $remote"
    cut_ref="refs/remotes/$remote/$cut_branch"
    say "phase 2: rc.$next_n of $target converges on $cut_branch (${floor:0:12} and the fixes cherry-picked onto it)"
  else
    say "phase 2: rc.$((next_n - 1)) of $target failed too; making $cut_branch from it (${floor:0:12}): cherry-pick fix: commits onto it from main (Backported-from: <sha>), and rc.$next_n is cut from it once release.yml is green there"
    push "$floor:refs/heads/$cut_branch" || die "could not make $cut_branch"
    emit convergence_branch "$cut_branch"
    exit 0
  fi
elif [ -n "$floor" ] && [ "$next_n" = 1 ] && ! git merge-base --is-ancestor "$floor" "$main_ref"; then
  # The newest release was cut from a convergence branch: main carries
  # its fixes, so the next rc is after where the branch left main.
  floor="$(git merge-base "$floor" "$main_ref" || true)"
  say "phase 2: the newest release is off main's history; the next rc must be after where it left main (${floor:0:12})"
fi

# Gate next RC (playbook section 12): while any `[rc-blocker vX.Y.Z-rc.N]`
# task is open, grain/rc-hold on the dropped rc's commit stays in progress.
# It reads next_n before the re-tag skip below: a hand-cut rc.1 must not
# make the next rc.1 (whose floor is a release, not a dropped rc) look like
# an rc.2, here or at the rc.1 gates after it.
if [ -n "$floor" ] && [ "$next_n" -gt 1 ]; then
  hold_summary="$(rc_hold_summary "$floor")"
  if [ -n "$hold_summary" ]; then
    held_n="$next_n"
    while commit_of "$target-rc.$held_n" >/dev/null; do held_n=$((held_n + 1)); done
    new_rc="$target-rc.$held_n"
    say "phase 2: $new_rc is held by grain/rc-hold on ${floor:0:12}: $hold_summary"
    emit rc_held "$new_rc"
    exit 0
  fi
fi

# A minimum interval between releases (playbook section 7).
if [ "$next_n" = 1 ] && [ "$FORCE" != true ] && [ "$min_interval" -gt 0 ]; then
  last="${pending:+$pending-pending}"
  last="${last:-$released}"
  if [ -n "$last" ]; then
    last_ts="$(git for-each-ref --format='%(taggerdate:unix)' "refs/tags/$last")"
    [ -n "$last_ts" ] || last_ts="$(git log -1 --format=%ct "$(commit_of "$last")")"
    if [ $((now - last_ts)) -lt $((min_interval * 60)) ]; then
      say "phase 2: ${last%-pending} was tagged $(((now - last_ts) / 60))m ago, under RELEASE_MIN_INTERVAL_MINUTES ($min_interval); nothing to cut yet (FORCE=true cuts anyway)"
      exit 0
    fi
  fi
fi

if [ "$ci_source" = github ]; then
  CANDIDATE_GATE_REQUIRED="$required" github_runs "$work/runs.json" "$cut_branch"
else
  gate_json RELEASE_RUNS_JSON "/api/ci/runs?repo=${GRAIN_CI_REPO:-ai-on-gke/grain}&ref=$cut_branch&limit=500" "$work/runs.json"
fi
lkg_status=0
lkg_out="$(CANDIDATE_GATE_REQUIRED="$required" bash "$scripts_dir/candidate-gate.sh" select-lkg --runs-json "$work/runs.json" \
  ${floor:+--floor-sha "$floor"} --ref "$cut_ref" 2>&1)" || lkg_status=$?
printf '%s\n' "$lkg_out" | sed 's/^/  /'
lkg="$(printf '%s\n' "$lkg_out" | sed -n 's/^LKG \([0-9a-f]\{40\}\)$/\1/p' | tail -n 1)"
if [ -z "$lkg" ]; then
  [ "$lkg_status" -le 1 ] || die "the LKG selector failed (exit $lkg_status)"
  say "phase 2: no new LKG commit on $cut_branch${floor:+ since ${floor:0:12}}; nothing to cut"
  exit 0
fi

# A new version's first rc: every trunk release increments minor (playbook
# section 7; patches stay release-patch.sh's cherry-picked hotfixes), while
# docs, chores and tests alone wait.
if [ "$next_n" = 1 ]; then
  kind="$(bump_kind "$floor" "$lkg")"
  if [ "$kind" = none ]; then
    say "phase 2: ${floor:0:12}..${lkg:0:12} holds only docs, chores, tests and the like; nothing to release until a fix or a feature lands"
    emit release_waits "$lkg"
    exit 0
  fi
  say "phase 2: the commits since ${base:-the start} make a minor release: $target"
fi

new_rc="$target-rc.$next_n"
while commit_of "$new_rc" >/dev/null; do
  # Never re-tag: an rc of this number already exists (one cut by hand).
  next_n=$((next_n + 1))
  new_rc="$target-rc.$next_n"
done
say "phase 2: cutting $new_rc on ${lkg:0:12}"
git tag -a "$new_rc" "$lkg" -m "Release candidate $new_rc" ${cut_branch:+-m "Cut from $cut_branch."}
push "refs/tags/$new_rc" || die "could not push $new_rc; candidate was not moved"
push --force "$lkg:refs/heads/candidate" || die "pushed $new_rc, but could not move candidate onto it"
emit rc_tag "$new_rc"
images rc "$new_rc" || die "cut $new_rc and moved candidate, but could not publish its images: run \`release-images.sh rc $new_rc\`"
if [ "$qualification_app" = none ]; then
  say "phase 2: $new_rc is cut and candidate is on it; nightly.yml qualifies it (Stage 1) alone: RELEASE_QUALIFICATION_APP=none skips grain/release-qualification (Stage 2)"
else
  say "phase 2: $new_rc is cut and candidate is on it; nightly.yml qualifies it (Stage 1), then grain/release-qualification (Stage 2)"
fi
