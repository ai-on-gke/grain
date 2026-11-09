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

# Write the release notes for one release, and gate its promotion on the
# breaking changes in it (design/playbook/releases.playbook.md, sections 7
# and 8).
#
#   scripts/release-notes.sh <version> <from-ref> <to-ref> [highlights-file]
#   scripts/release-notes.sh write [--notes <file>] [--body <file>] \
#       <version> <from-ref> <to-ref> [highlights-file]
#   scripts/release-notes.sh check-breaking <version> <from-ref> <to-ref>
#
# The first form writes the notes as markdown on stdout.
#
# write writes them where a promotion publishes them: docs/releases/<version>.md
# in the checkout (or --notes), which the train commits, and the GitHub
# Release body (--body; by default release-body-<version>.md in $RUNNER_TEMP,
# else $TMPDIR, else /tmp), which is the same notes without their "# <version>"
# title, since a Release carries its own. A notes file already there is
# never rewritten -- it is what was committed, perhaps with highlights a
# person wrote -- and the body is taken from it. write prints the two paths,
# one per line.
#
# check-breaking lists the breaking changes in the range on stdout and
# exits 0 when the release may carry them: when there are none; when
# <version> is a major bump over <from-ref> that RELEASE_MAJOR in
# scripts/release.conf (as <to-ref> has it; the RELEASE_MAJOR variable
# overrides it, as for scripts/release-from-trunk.sh) asked for; or when a
# person has overridden the gate for this one release with
# RELEASE_ALLOW_BREAKING=<version>. Otherwise it exits 3, saying why and
# what would let the release through, and the train must not promote.
#
# The notes list what landed between <from-ref> (the previous release) and
# <to-ref> (this one): every pull request merged onto the first-parent line,
# named by its title, and every commit pushed there directly, grouped by its
# Conventional Commits type -- features, fixes, and everything else. The
# release cut's own back-merges are not listed themselves, and neither is
# what they carry that the previous release already had; what they carry
# that it did not -- a candidate the candidate gate held, whose fixes reach
# main only through the back-merge -- is listed. A title listed once is not
# listed again, so a change ported between the branches appears once.
#
# A change is breaking when its title, or the subject of any commit a
# merged pull request brings, carries the Conventional Commits "!" marker
# ("feat!:", "fix(api)!:"), or when any of those commits' messages has a
# "BREAKING CHANGE:" (or "BREAKING-CHANGE:") trailer. Breaking changes are
# listed first, under "Breaking changes" with each trailer's text beneath
# its change, and in their type's section as well; the section is left out
# when there are none.
#
# A highlights file, if given and non-empty, is prose written by whoever cut
# the release -- the few things in it worth a reader's attention -- and goes
# in first, under "Highlights". The lists below it are mechanical; the
# highlights are not.
#
# The release-notes task scripts/release-from-trunk.sh files when it
# promotes a release (.grain/templates/release-notes.yml) runs the first
# form, and commits the result as docs/releases/<version>.md through a pull
# request (docs/releases/README.md).
set -euo pipefail

usage() {
  echo "usage: $0 <version> <from-ref> <to-ref> [highlights-file]" >&2
  echo "       $0 write [--notes <file>] [--body <file>] <version> <from-ref> <to-ref> [highlights-file]" >&2
  echo "       $0 check-breaking <version> <from-ref> <to-ref>" >&2
  exit 2
}

mode="notes"
notes_file=""
body_file=""
case "${1:-}" in
  write)
    mode="write"
    shift
    while [ "$#" -gt 0 ]; do
      case "$1" in
        --notes) [ "$#" -ge 2 ] || usage; notes_file="$2"; shift 2 ;;
        --body) [ "$#" -ge 2 ] || usage; body_file="$2"; shift 2 ;;
        --) shift; break ;;
        -*) usage ;;
        *) break ;;
      esac
    done
    ;;
  check-breaking)
    mode="check"
    shift
    [ "$#" -eq 3 ] || usage
    ;;
esac

if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then
  usage
fi
version="$1"
from_ref="$2"
to_ref="$3"
highlights_file="${4:-}"
# A highlights file that was named and is not there is a mistake --
# usually a mistyped path -- and is refused rather than read as no
# highlights: the notes file it would have gone into is never rewritten
# once committed.
if [ -n "$highlights_file" ] && [ ! -f "$highlights_file" ]; then
  echo "$0: highlights file ${highlights_file} does not exist" >&2
  exit 2
fi

repo="${GRAIN_CI_REPO:-ai-on-gke/grain}"

features=()
fixes=()
other=()
breaking=()

# breaking_subject <subject>: whether a subject carries the Conventional
# Commits breaking marker, "<type>!:" or "<type>(<scope>)!:".
breaking_subject() {
  [[ "$1" =~ ^[A-Za-z]+(\([^\)]*\))?!: ]]
}

# has_breaking_trailer <message>: whether a commit message carries a
# BREAKING CHANGE (or BREAKING-CHANGE) trailer line, even when empty.
has_breaking_trailer() {
  grep -qE '^BREAKING[ -]CHANGE:' <<<"$1"
}

# breaking_trailers <message>: the text of each BREAKING CHANGE trailer in
# a commit message, one per line (joining wrapped continuation lines up to
# the next blank line or trailer).
breaking_trailers() {
  awk '
    function flush() {
      if (active) {
        sub(/^[[:space:]]+/, "", cur)
        sub(/[[:space:]]+$/, "", cur)
        if (cur != "") print cur
        active = 0
        cur = ""
      }
    }
    /^BREAKING[ -]CHANGE:[[:space:]]*/ {
      flush()
      line = $0
      sub(/^BREAKING[ -]CHANGE:[[:space:]]*/, "", line)
      sub(/[[:space:]]+$/, "", line)
      cur = line
      active = 1
      next
    }
    active {
      if ($0 ~ /^[[:space:]]*$/ || $0 ~ /^[A-Za-z][A-Za-z0-9-]*:[[:space:]]/) {
        flush()
        next
      }
      line = $0
      sub(/^[[:space:]]+/, "", line)
      sub(/[[:space:]]+$/, "", line)
      cur = (cur == "" ? line : cur " " line)
    }
    END { flush() }
  ' <<<"$1"
}

# collect <rev-list args>: one record per first-parent commit in the range
# -- sha, parents, subject, and the body, whose first line, on a GitHub
# merge commit, is the pull request's title. Fields are split on US
# (0x1f), records on RS (0x1e).
declare -A listed_titles=()
collect() {
  local sha parents subject body title ref line is_breaking notes
  while IFS=$'\x1f' read -r -d $'\x1e' sha parents subject body; do
    sha="${sha#$'\n'}"
    [ -n "$sha" ] || continue
    title="$subject"
    ref=""
    local -a p
    read -r -a p <<< "$parents"
    if [[ "$subject" =~ ^Merge\ pull\ request\ \#([0-9]+)\  ]]; then
      ref="#${BASH_REMATCH[1]}"
      title="${body%%$'\n'*}"
      [ -n "$title" ] || title="$subject"
    elif [ "${#p[@]}" -gt 1 ]; then
      # The cut's back-merge of candidate into main, or any other merge
      # that is not a pull request. What it brings that <from-ref> already
      # carries was released already; the rest -- a candidate whose
      # promotion the candidate gate held, so that <from-ref> is an older
      # release -- was not, and is listed from the merged branch's own
      # first-parent line.
      collect "${p[1]}" "^${p[0]}" "^${from_ref}"
      continue
    fi
    # A port lands on both branches under the same title; list it once.
    [ -z "${listed_titles[$title]:-}" ] || continue
    listed_titles[$title]=1
    [ -n "$ref" ] || ref="${sha:0:9}"
    line="- ${title} (${ref})"

    # Whether it breaks anything: its title, its own message, and, for a
    # pull request, every commit it brings.
    is_breaking=""
    breaking_subject "$title" && is_breaking=1
    if has_breaking_trailer "$body"; then is_breaking=1; fi
    notes="$(breaking_trailers "$body")"
    if [ "${#p[@]}" -gt 1 ]; then
      local s m
      while IFS=$'\x1f' read -r -d $'\x1e' s m; do
        s="${s#$'\n'}"
        breaking_subject "$s" && is_breaking=1
        if has_breaking_trailer "$m"; then is_breaking=1; fi
        m="$(breaking_trailers "$m")"
        [ -z "$m" ] || notes="${notes:+${notes}$'\n'}${m}"
      done < <(git log --format='%s%x1f%b%x1e' "${p[1]}" "^${p[0]}" "^${from_ref}")
    fi
    [ -z "$notes" ] || is_breaking=1
    if [ -n "$is_breaking" ]; then
      breaking+=("$line")
      if [ -n "$notes" ]; then
        local n
        while IFS= read -r n; do
          [ -z "$n" ] || breaking+=("  - ${n}")
        done < <(awk '!seen[$0]++' <<<"$notes")
      fi
    fi

    case "$title" in
      feat:*|feat\(*|feat!*) features+=("$line") ;;
      fix:*|fix\(*|fix!*) fixes+=("$line") ;;
      *) other+=("$line") ;;
    esac
  done < <(git log --first-parent --reverse --format='%H%x1f%P%x1f%s%x1f%b%x1e' "$@")
}
collect "${from_ref}..${to_ref}"

# major <version>: a vM.m.p version's M, or nothing.
major() {
  [[ "$1" =~ ^v?([0-9]+)\.[0-9]+\.[0-9]+ ]] && echo "${BASH_REMATCH[1]}"
}

if [ "$mode" = check ]; then
  if [ "${#breaking[@]}" -eq 0 ]; then
    echo "${version}: no breaking changes since ${from_ref}"
    exit 0
  fi
  echo "${version}: breaking changes since ${from_ref}:"
  printf '%s\n' "${breaking[@]}"
  if [ "${RELEASE_ALLOW_BREAKING:-}" = "$version" ]; then
    echo "${version}: promoting with breaking changes: RELEASE_ALLOW_BREAKING=${version} overrides the gate"
    exit 0
  fi
  release_conf="$(git show "${to_ref}:scripts/release.conf" 2>/dev/null || true)"
  conf_major="${RELEASE_MAJOR:-$(printf '%s\n' "$release_conf" | sed -nE 's/^RELEASE_MAJOR="?([0-9]+)"?[[:space:]]*(#.*)?$/\1/p' | tail -n 1)}"
  new_major="$(major "$version" || true)"
  # The previous release's major, from its name, or from the newest
  # release tag on its commit when it was named by commit or by another
  # tag (`latest`, which git describe would pick over vX.Y.Z on the same
  # commit).
  prev_major="$(major "$from_ref" || true)"
  [ -n "$prev_major" ] ||
    prev_major="$(major "$(git tag --points-at "${from_ref}^{commit}" 2>/dev/null |
      grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V | tail -n 1 || true)" || true)"
  if [ -n "$new_major" ] && [ -n "$prev_major" ] && [ -n "$conf_major" ] &&
    [ "$new_major" -gt "$prev_major" ] && [ "$conf_major" -ge "$new_major" ]; then
    echo "${version}: promoting with breaking changes: a major release over ${from_ref}, as RELEASE_MAJOR=${conf_major} asks"
    exit 0
  fi
  {
    echo "$0: refusing to promote ${version}: it carries breaking changes, and it is not a major release."
    echo "  ${from_ref} is major ${prev_major:-?}, ${version} is major ${new_major:-?}, and scripts/release.conf at ${to_ref} has RELEASE_MAJOR=${conf_major:-unset}."
    echo "  To release them, raise RELEASE_MAJOR in scripts/release.conf above ${prev_major:-the previous major} and cut again,"
    echo "  or have a person set RELEASE_ALLOW_BREAKING=${version} for this one promotion."
  } >&2
  exit 3
fi

to_sha=$(git rev-parse --short=9 "${to_ref}^{commit}")
from_sha=$(git rev-parse --short=9 "${from_ref}^{commit}")
# The day the release was tagged, where it has been; otherwise the day its
# commit was made.
date=$(git for-each-ref --format='%(creatordate:short)' "refs/tags/${version}")
[ -n "$date" ] || date=$(git log -1 --format=%cs "${to_ref}^{commit}")

section() {
  local heading="$1"
  shift
  echo
  echo "## ${heading}"
  echo
  if [ "$#" -eq 0 ]; then
    echo "None."
  else
    printf '%s\n' "$@"
  fi
}

render() {
  echo "# ${version}"
  echo
  echo "Released ${date} from commit \`${to_sha}\`. Changes since \`${from_ref}\` (\`${from_sha}\`):"
  echo "https://github.com/${repo}/compare/${from_ref}...${version}"

  if [ -n "$highlights_file" ] && [ -s "$highlights_file" ]; then
    echo
    echo "## Highlights"
    echo
    cat "$highlights_file"
  fi

  if [ "${#breaking[@]}" -gt 0 ]; then
    section "Breaking changes" "${breaking[@]}"
  fi
  section "Features" "${features[@]+"${features[@]}"}"
  section "Fixes" "${fixes[@]+"${fixes[@]}"}"
  section "Other changes" "${other[@]+"${other[@]}"}"
}

if [ "$mode" = notes ]; then
  render
  exit 0
fi

if [ -z "$notes_file" ]; then
  root="$(git rev-parse --show-toplevel)"
  notes_file="${root}/docs/releases/${version}.md"
fi
[ -n "$body_file" ] || body_file="${RUNNER_TEMP:-${TMPDIR:-/tmp}}/release-body-${version}.md"
if [ -e "$notes_file" ]; then
  echo "$0: ${notes_file} is already there; keeping it, and taking the release body from it" >&2
else
  mkdir -p "$(dirname "$notes_file")"
  render >"${notes_file}.tmp"
  mv "${notes_file}.tmp" "$notes_file"
fi
mkdir -p "$(dirname "$body_file")"
# The body is the notes without their title line and the blank line after
# it: the Release is titled by its tag already.
awk 'NR == 1 && /^# / { skip = 1; next } skip && /^$/ { skip = 0; next } { skip = 0; print }' \
  "$notes_file" >"$body_file"
echo "$notes_file"
echo "$body_file"
