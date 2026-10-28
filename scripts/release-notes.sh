#!/usr/bin/env bash
# Write the release notes for one release as markdown on stdout.
#
#   scripts/release-notes.sh <version> <from-ref> <to-ref> [highlights-file]
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
# A highlights file, if given and non-empty, is prose written by whoever cut
# the release -- the few things in it worth a reader's attention -- and goes
# in first, under "Highlights". The lists below it are mechanical; the
# highlights are not.
#
# The release-notes task scripts/release-from-trunk.sh files when it
# promotes a release (.grain/templates/release-notes.yml) runs this, and
# commits the result as docs/releases/<version>.md through a pull request
# (docs/releases/README.md).
set -euo pipefail

if [ "$#" -lt 3 ] || [ "$#" -gt 4 ]; then
  echo "usage: $0 <version> <from-ref> <to-ref> [highlights-file]" >&2
  exit 2
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

repo="${GRAIN_CI_REPO:-bwsalmon/grain}"

features=()
fixes=()
other=()

# collect <rev-list args>: one record per first-parent commit in the range
# -- sha, parents, subject, and the first line of the body, which, on a
# GitHub merge commit, is the pull request's title. Fields are split on US
# (0x1f), records on RS (0x1e).
declare -A listed_titles=()
collect() {
  local sha parents subject body title ref line
  while IFS=$'\x1f' read -r -d $'\x1e' sha parents subject body; do
    sha="${sha#$'\n'}"
    [ -n "$sha" ] || continue
    title="$subject"
    ref=""
    if [[ "$subject" =~ ^Merge\ pull\ request\ \#([0-9]+)\  ]]; then
      ref="#${BASH_REMATCH[1]}"
      title="${body%%$'\n'*}"
      [ -n "$title" ] || title="$subject"
    elif [ "$(wc -w <<<"$parents")" -gt 1 ]; then
      # The cut's back-merge of candidate into main, or any other merge
      # that is not a pull request. What it brings that <from-ref> already
      # carries was released already; the rest -- a candidate whose
      # promotion the candidate gate held, so that <from-ref> is an older
      # release -- was not, and is listed from the merged branch's own
      # first-parent line.
      local -a p
      read -r -a p <<< "$parents"
      collect "${p[1]}" "^${p[0]}" "^${from_ref}"
      continue
    fi
    # A port lands on both branches under the same title; list it once.
    [ -z "${listed_titles[$title]:-}" ] || continue
    listed_titles[$title]=1
    [ -n "$ref" ] || ref="${sha:0:9}"
    line="- ${title} (${ref})"
    case "$title" in
      feat:*|feat\(*|feat!*) features+=("$line") ;;
      fix:*|fix\(*|fix!*) fixes+=("$line") ;;
      *) other+=("$line") ;;
    esac
  done < <(git log --first-parent --reverse --format='%H%x1f%P%x1f%s%x1f%b%x1e' "$@")
}
collect "${from_ref}..${to_ref}"

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

section "Features" "${features[@]+"${features[@]}"}"
section "Fixes" "${fixes[@]+"${fixes[@]}"}"
section "Other changes" "${other[@]+"${other[@]}"}"
