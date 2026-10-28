#!/usr/bin/env bash
# Print the semver version a build of this checkout carries, from its git
# tags and the branch it is a build of -- the one place every build works
# it out: `make build` (the Makefile's RELEASE), the release job's build
# phase (scripts/release-images.sh) and so every image both daemon images
# are published as. docs/release-candidate-management.md, "Build
# versions", is the scheme and why it orders the way it does:
#
#   main (and any other branch)  v<M>.<P+1>.0-dev.<n>+<sha7>
#       v<M>.<P>.0 is the highest *promoted* release tag in the build's
#       history and n counts commits since it. An unpromoted
#       v<M>.<Y>.0-rc.<k> on main does not move it: under release from
#       trunk (docs/design/release-from-trunk.md, "Version Numbering
#       Rules") an rc is tagged on a main commit a day before it is
#       promoted, and one that fails its soak is followed by rc.<k+1> of
#       the same minor, so main goes on building v<M>.<P+1>.0-dev until
#       v<M>.<P+1>.0 itself is tagged. scripts/release.conf (as the
#       build's own commit has it) can raise the minor: RELEASE_FLOOR_MINOR
#       is the lowest minor RELEASE_MAJOR's next release may have, and a
#       minor in RELEASE_NEVER_PROMOTE is stepped over -- the same rules
#       scripts/release-from-trunk.sh picks the next rc tag by. A
#       RELEASE_MAJOR above M makes it v<RELEASE_MAJOR>.<floor>.0-dev.<n>.
#   candidate, rc/v*, release/vX.Y[.0]
#       v<M>.<m>.0-rc.<k>.<n>+<sha7>, n counting commits since the newest
#       rc tag v<M>.<m>.0-rc.<k> in its history; exactly that tag on the
#       tag's own commit, which is what candidate always is under release
#       from trunk (scripts/release-from-trunk.sh force-moves it onto the
#       rc's own commit).
#   release/vX.Y.Z (Z > 0, a patch branch)
#       vX.Y.Z-rc.0.<n>+<sha7>, n counting commits since the release the
#       patch was branched from, vX.Y.<Z-1> (or the newest vX.Y.* below
#       it in its history).
#
# Prints nothing, and still exits 0, when there is no version to give:
# no git, no tags in the history, a commit git does not have. A build
# with nothing stamped falls back to what Go reads off the module
# (pkg/version), which reports itself as unversioned. Why goes to stderr.
#
# A promotion does not come here: it rebuilds with RELEASE=vX.Y.Z, and
# every caller lets a RELEASE it was given win over this.
#
# Usage: build-version.sh [--branch B] [--commit C] [--fetch REMOTE] [--tag-prefix P]
#   --branch      the branch this is a build of; default $GRAIN_CI_REF, else
#                 the checked-out branch. A detached checkout with neither is
#                 versioned as main.
#   --commit      the commit; default $GRAIN_CI_COMMIT, else HEAD.
#   --fetch       fetch REMOTE's tags first (and the rest of the history, if
#                 the checkout is shallow), so a CI checkout that came down
#                 without them still counts right. A fetch that fails is
#                 reported and the tags already here are used. --force,
#                 because `latest` is a tag the cut moves, and a plain fetch
#                 refuses to move a tag it already has.
#   --tag-prefix  optional prefix before `vX.Y.Z` tags (for example `grit/`
#                 for cmd/grit's independent `grit/vX.Y.Z` release line).
set -euo pipefail

branch="${GRAIN_CI_REF:-}"
commit="${GRAIN_CI_COMMIT:-}"
fetch=""
prefix=""
while [ $# -gt 0 ]; do
  case "$1" in
    --branch) branch="${2:-}"; shift 2 ;;
    --commit) commit="${2:-}"; shift 2 ;;
    --fetch) fetch="${2:-}"; shift 2 ;;
    --tag-prefix) prefix="${2:-}"; shift 2 ;;
    *)
      echo "build-version.sh: usage: build-version.sh [--branch B] [--commit C] [--fetch REMOTE] [--tag-prefix P]" >&2
      exit 2
      ;;
  esac
done

unversioned() {
  echo "build-version.sh: unversioned: $*" >&2
  exit 0
}

command -v git >/dev/null 2>&1 || unversioned "no git"
git rev-parse --git-dir >/dev/null 2>&1 || unversioned "not a git checkout"

if [ -n "$fetch" ]; then
  if [ "$(git rev-parse --is-shallow-repository 2>/dev/null)" = true ]; then
    git fetch -q --force --unshallow --tags "$fetch" >&2 || echo "build-version.sh: could not unshallow from $fetch; counting the history this checkout has" >&2
  else
    git fetch -q --force --tags "$fetch" >&2 || echo "build-version.sh: could not fetch tags from $fetch; using the tags this checkout has" >&2
  fi
fi

commit="${commit:-HEAD}"
sha="$(git rev-parse --verify -q "$commit^{commit}")" || unversioned "git does not have commit $commit"
short="${sha:0:7}"
if [ -z "$branch" ]; then
  branch="$(git branch --show-current 2>/dev/null || true)"
fi

# The tags in the build's history matching an extended regexp, oldest
# version first.
merged_tags() {
  git tag --merged "$sha" 2>/dev/null | grep -E "^($1)\$" | sort -V || true
}

count_since() {
  git rev-list --count "$1..$sha"
}

rc_line_build() {
  local tag n
  tag="$(merged_tags "${prefix}v[0-9]+\\.[0-9]+\\.0-rc\\.[0-9]+" | tail -n 1)"
  [ -n "$tag" ] || unversioned "no release-candidate tag in the history of $short"
  n="$(count_since "$tag")"
  if [ "$n" -eq 0 ]; then
    printf '%s\n' "$tag"
  else
    printf '%s.%s+%s\n' "$tag" "$n" "$short"
  fi
}

if [ "$branch" = candidate ] || [ "$branch" = "${prefix}candidate" ] || [[ "$branch" =~ ^rc/(${prefix})?v ]] || [[ "$branch" =~ ^release/(${prefix})?v[0-9]+\.[0-9]+(\.0)?$ ]]; then
  rc_line_build
  exit 0
fi

if [[ "$branch" =~ ^release/(${prefix})?v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]] && [ "${BASH_REMATCH[4]}" -gt 0 ]; then
  major="${BASH_REMATCH[2]}" minor="${BASH_REMATCH[3]}" patch="${BASH_REMATCH[4]}"
  base=""
  for tag in $(merged_tags "${prefix}v$major\\.$minor\\.[0-9]+"); do
    [ "${tag##*.}" -lt "$patch" ] && base="$tag"
  done
  [ -n "$base" ] || unversioned "no ${prefix}v$major.$minor.* release below ${prefix}v$major.$minor.$patch in the history of $short"
  printf '%sv%s.%s.%s-rc.0.%s+%s\n' "$prefix" "$major" "$minor" "$patch" "$(count_since "$base")" "$short"
  exit 0
fi

# main, and every branch that is not one of the above: the release after
# the highest promoted one in the history.
conf="$(git show "$sha:scripts/release.conf" 2>/dev/null || true)"
conf_value() {
  printf '%s\n' "$conf" | sed -nE "s/^$1=\"?([^\"#]*)\"?[[:space:]]*(#.*)?\$/\1/p" | tail -n 1 | sed -E 's/[[:space:]]+$//'
}
tag="$(merged_tags "${prefix}v[0-9]+\\.[0-9]+\\.0" | tail -n 1)"
[ -n "$tag" ] || unversioned "no promoted release tag (${prefix}vM.P.0) in the history of $short"
ver="${tag#"$prefix"}"
major="${ver#v}" major="${major%%.*}"
minor="${ver#v*.}" minor="${minor%%.*}"
if [ -z "$prefix" ]; then
  want_major="$(conf_value RELEASE_MAJOR)"
  floor="$(conf_value RELEASE_FLOOR_MINOR)"
  never=" $(conf_value RELEASE_NEVER_PROMOTE) "
else
  want_major=""
  floor=""
  never=""
fi
[[ "$want_major" =~ ^[0-9]+$ ]] || want_major="$major"
[[ "$floor" =~ ^[0-9]+$ ]] || floor=0
if [ "$want_major" -gt "$major" ]; then
  # A RELEASE_MAJOR above every promoted major makes the next release
  # that major's first.
  major="$want_major"
  next_minor=0
else
  next_minor=$((minor + 1))
fi
# The floor is RELEASE_MAJOR's: a major the conf does not name has none.
if [ "$major" = "$want_major" ] && [ "$next_minor" -lt "$floor" ]; then
  next_minor="$floor"
fi
while [[ "$never" == *" v$major.$next_minor.0 "* ]]; do
  next_minor=$((next_minor + 1))
done
printf '%sv%s.%s.0-dev.%s+%s\n' "$prefix" "$major" "$next_minor" "$(count_since "$tag")" "$short"
