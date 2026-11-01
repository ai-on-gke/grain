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

# Stage the source tree a deployment carries for the self-debug
# capability: `export-source.sh <checkout> <destination>`.
#
# read_grain_source and list_grain_source are an agent's view of what the
# binary beside them was built from (pkg/capability/selfdebug, and
# pkg/cli/daemon.go's defaultSourceDir), which is why the tree travels
# inside the image rather than being mounted from a host checkout. Two
# callers stage it: the Dockerfile, into /src-export, and scripts/setup.sh
# for a host-mode install that named its own binary and its own checkout.
#
# What this replaces, and why it is an allowlist. Both callers used to
# `cp -a` the whole directory and then delete three names from the copy --
# .git, bin/ and ui/node_modules. That is a denylist written against one
# layout of one repository at one moment, and a build context is not only
# the repository: it is whatever is sitting in that directory on the
# machine running the build. The published image is what it cost. A
# release host's checkout still held the tree from before the Go module
# was promoted out of its old subdirectory, untracked and so untouched by
# every `git checkout` since, and the image carried all 3.1GB of it: the
# Makefile's own module and build caches under that directory's
# .container-cache, a 32MB grain binary under its bin/, and a node_modules
# under the UI path that layout used. Not one of the three deleted names
# matched, .dockerignore's patterns are anchored at the context root and
# did not match either, and what shipped was a 5.8GB image of which more
# than half was a build cache for a layout that no longer exists --
# pulled by every deployment, `docker cp`'d back out of the image on
# every single daemon start in host mode (setup.sh's own extractor, which
# runs as an ExecStartPre), and handed to an agent as though it were the
# source the binary was built from.
#
# So the rule is inverted and git keeps it: what gets exported is what is
# *tracked* in the checkout, with the working tree's content, since the
# working tree is what was built. A cache, an output directory, a package
# manager's tree, a leftover from a layout two promotions ago -- none of
# them can be exported by a rule that never names them, whatever they are
# called and wherever they turn up.
set -euo pipefail

src="${1:-}"
dest="${2:-}"
if [ -z "$src" ] || [ -z "$dest" ]; then
  echo "export-source.sh: usage: export-source.sh <checkout> <destination>" >&2
  exit 2
fi
if [ ! -d "$src" ]; then
  echo "export-source.sh: $src is not a directory" >&2
  exit 2
fi

# The ceiling, in MB, on what may be staged -- a backstop for the
# fallback below, which is the only path that can still copy something
# nobody chose. grain's tracked tree was ~27MB when this was written and
# is ~113MB now (docs/ screenshots, third_party/granule), which du counts
# as 127-129MB depending on the filesystem's blocks -- so 128 stopped
# every installer build at the edge. 256 is twice the tree again, so only
# a leak reaches it; raise it here (or with the environment variable,
# for a one-off) if the repository itself ever legitimately grows into it.
#
# It fails the build that would publish the leak, which is the whole
# point of it being here rather than only in tests/container: the image
# tests gate GitHub Actions' publish job, and the release path
# (scripts/release-images.sh, run from a Grain CI release job) builds and
# pushes without them. A failed release is cheap; an image whose size
# makes grain-daemon.service miss its start timeout is not.
limit="${GRAIN_SOURCE_EXPORT_MAX_MB:-256}"

mkdir -p "$dest"

list="$(mktemp)"
staged_files="$(mktemp)"
trap 'rm -f "$list" "$staged_files"' EXIT

if git -C "$src" ls-files -z >"$list" 2>/dev/null && [ -s "$list" ]; then
  # An index entry whose file is not in the working tree -- a deletion
  # that has been staged and not committed -- is not something tar can
  # archive, and is not a reason to fail a build either.
  while IFS= read -r -d '' path; do
    [ -e "$src/$path" ] || continue
    printf '%s\0' "$path"
  done <"$list" >"$staged_files"

  # tar rather than `cp --parents`: one pass, and it creates the parent
  # directories of every path for us. --no-recursion because the list is
  # already every file; a directory in it (a submodule's gitlink) should
  # arrive as itself and not as whatever a host left inside it.
  tar -C "$src" --null --no-recursion --files-from="$staged_files" -cf - |
    tar -C "$dest" -xf -
else
  # No repository here that git will talk about: an export, a tarball fed
  # to `docker build`, a vendored copy, or one of the ways git refuses a
  # checkout it can see (scripts/buildvcs.sh's own list -- ownership, a
  # builder git older than the one that wrote the index, a .git that
  # names a gitdir outside the context). None of those is a reason to
  # refuse to build, so this falls back to the copy that was here before
  # -- and says so, because what it stages is no longer a list anybody
  # chose and the ceiling below is all that is between it and the image.
  {
    echo "export-source.sh: $src is not a checkout git can enumerate: staging it by copy instead"
    echo "export-source.sh:   everything in that directory is exported except .git, bin/ and"
    echo "export-source.sh:   ui/node_modules, including anything untracked sitting in it"
  } >&2
  cp -a "$src/." "$dest/"
  # ${dest:?} rather than $dest: an empty one would make these /.git,
  # /bin and /ui/node_modules, and both callers run as root.
  rm -rf "${dest:?}/.git" "${dest:?}/bin" "${dest:?}/ui/node_modules"
fi

# A shallow history beside the tree, so a self-debug run can `git log`,
# `git blame` and `git show` in the grain-src/ it is given
# (orchestrator's prepareGrainSource) without a clone of bwsalmon/grain:
# the deployment's own copy is the only source a sandbox gets, and this is
# the only history it has.
#
# GRAIN_SOURCE_HISTORY_DEPTH commits of it (the Dockerfile's
# SOURCE_HISTORY_DEPTH build arg), with every object of each, so blame
# reaches back that far. 0 bakes no history at all. What is written is a
# repository that knows nothing but this build: HEAD detached at the
# commit the checkout is on, no branches, no tags, no remote-tracking
# refs, no reflog, no hooks, and an origin that can be neither fetched
# nor pushed -- the config is written here line by line, so nothing the
# building machine's own .git/config carried (a credential helper, a
# token in a URL) can come along.
#
# Everything here ships in every published image, deleted files
# included, which is why GRAIN_SOURCE_SECRET_SCAN=require (the
# Dockerfile's setting) refuses to export a history gitleaks finds a
# secret in. A host-mode install staging its own checkout publishes
# nothing and leaves it unset.
depth="${GRAIN_SOURCE_HISTORY_DEPTH:-500}"
case "$depth" in
  ''|*[!0-9]*)
    echo "export-source.sh: GRAIN_SOURCE_HISTORY_DEPTH must be a number of commits, not '$depth'" >&2
    exit 2 ;;
esac
history=""
head=""
if [ "$depth" -gt 0 ] && head="$(git -C "$src" rev-parse --verify -q 'HEAD^{commit}' 2>/dev/null)"; then
  rm -rf "${dest:?}/.git"
  git init -q --template= "$dest"
  # upload-pack only serves a want it has a ref for unless told
  # otherwise, and HEAD may be detached; allowing any reachable object on
  # the serving side touches nothing in $src.
  #
  # The cut is the newest $depth commits by date, not git's --depth: that
  # counts generations along every parent, and grain's history is merge
  # upon merge, so --depth=500 reaches back more than ten thousand
  # commits. --shallow-since at the date of the $depth-th newest commit
  # keeps that many, give or take commits sharing its timestamp.
  oldest="$(git -C "$src" rev-list --date-order --max-count="$depth" "$head" | tail -n 1)"
  since="$(git -C "$src" show -s --format=%ct "$oldest")"
  git -C "$dest" fetch -q --no-tags --shallow-since="@$since" \
    --upload-pack='git -c uploadpack.allowAnySHA1InWant=true upload-pack' \
    "file://$(cd "$src" && pwd)" "$head"
  printf '%s\n' "$head" >"$dest/.git/HEAD"
  rm -f "$dest/.git/FETCH_HEAD"
  rm -rf "$dest/.git/logs" "$dest/.git/hooks"
  cat >"$dest/.git/config" <<'EOF'
[core]
	repositoryformatversion = 0
	filemode = true
	bare = false
[remote "origin"]
	url = https://github.com/ai-on-gke/grain.git
	pushurl = grain-baked-source-is-read-only
	fetch = +refs/heads/*:refs/remotes/origin/*
EOF
  # The index, so `git status` and `git diff` compare the tree with
  # HEAD rather than calling every file new. A working tree that differs
  # from HEAD (a build of uncommitted changes) shows as just that.
  git -C "$dest" read-tree HEAD
  git -C "$dest" update-index -q --refresh >/dev/null || true
  git -C "$dest" gc -q --prune=now
  history="$(git -C "$dest" rev-list --count HEAD)"
  # What the orchestrator reads back to tell an agent which commit it is
  # reading and how far back it can see (orchestrator's
  # grainSourceSection), without needing git on the daemon side.
  printf '%s %s\n' "$head" "$history" >"$dest/.git/grain-baked-history"
  echo "export-source.sh: baked ${history} commits of history ending at $head"
elif [ "$depth" -gt 0 ]; then
  echo "export-source.sh: $src has no commit git can read: exporting without history" >&2
fi

if [ -n "$history" ] && [ "${GRAIN_SOURCE_SECRET_SCAN:-}" = require ]; then
  if ! command -v gitleaks >/dev/null 2>&1; then
    echo "export-source.sh: GRAIN_SOURCE_SECRET_SCAN=require, but gitleaks is not installed -- refusing to export history unscanned." >&2
    exit 1
  fi
  # Every commit of exactly what was baked, deleted files included: the
  # shallow repository above, not the checkout it came from.
  if ! gitleaks git --no-banner --redact --exit-code 1 "$dest" >&2; then
    {
      echo "export-source.sh: gitleaks found what looks like a secret in the history being baked -- refusing to export it."
      echo "export-source.sh:   Everything in that history ships in every published image. Rotate the secret,"
      echo "export-source.sh:   then either build with SOURCE_HISTORY_DEPTH below the commit that carries it or,"
      echo "export-source.sh:   for a false positive, record its fingerprint in .gitleaksignore."
    } >&2
    exit 1
  fi
fi

# Modes, set here rather than inherited from whoever built this.
#
# The account that reads this tree is never the account that staged it. In
# container mode the daemon is a `docker run --user <host uid>:<gid>`
# against an image whose files belong to root (scripts/setup.sh's
# docker_run_args); in host mode it is the unprivileged $GRAIN_USER
# reading a tree root installed. So "readable" here means world-readable,
# and nothing about it may depend on the umask of the machine that ran the
# build or on the modes that machine's own checkout happened to carry --
# both of which travel into the staged copy otherwise. The tar above
# copies each file's mode out of the checkout verbatim, so a clone made
# under a umask of 0077 publishes an image of 0600 files; the fallback
# below is a `cp -a`, which brings the directories' modes too.
#
# That is the bug in grain/task-160 and grain/task-250, and it is worth
# knowing why it looked like a bad path rather than like a broken install:
# every caller creates $dest itself, with a mode of its own, and then
# fills it with content whose modes come from somewhere else. The root
# lists fine and nothing below it can be opened.
#
# The exec bit is the one thing kept, because it is the one thing git
# records and scripts in this tree are read by people who may want to run
# them. Symlinks are skipped: chmod follows them, and a tracked symlink
# may point anywhere.
find "$dest" -type d -exec chmod 0755 {} +
find "$dest" ! -type d ! -type l -perm -u+x -exec chmod 0755 {} +
find "$dest" ! -type d ! -type l ! -perm -u+x -exec chmod 0644 {} +

# ...and then checked, rather than assumed. A chmod that missed something
# -- a path find could not reach, a filesystem that does not carry these
# bits -- is a published image whose self-debug capability answers
# "permission denied" for every path in it, and the deployment that finds
# out is somebody else's.
# `|| true` for the SIGPIPE `head` hands find once it has its five, which
# `set -o pipefail` would otherwise turn into the failure of this whole
# script -- failing the build with find's death rather than with the
# sentence below it.
unreadable="$(find "$dest" \( -type d ! -perm -0005 \) -o \( ! -type d ! -type l ! -perm -0004 \) -print 2>/dev/null | head -5 || true)"
if [ -n "$unreadable" ]; then
  {
    echo "export-source.sh: the staged source is not readable by an unprivileged account -- refusing to export it."
    echo "export-source.sh:   The daemon reads this tree as the account it runs as, never as the one that built"
    echo "export-source.sh:   it, so a path without o+r (o+x for a directory) is a path read_grain_source and"
    echo "export-source.sh:   list_grain_source cannot answer. Up to five of them:"
    printf '%s\n' "$unreadable" | sed 's/^/export-source.sh:     /'
  } >&2
  exit 1
fi

# The history is left out of the ceiling: its size is set by
# GRAIN_SOURCE_HISTORY_DEPTH, not by whatever was lying in the checkout,
# which is all this guards against.
used="$(du -sm --exclude=.git "$dest" | cut -f1)"
if [ "$used" -gt "$limit" ]; then
  {
    echo "export-source.sh: the staged source is ${used}MB, over the ${limit}MB ceiling -- refusing to export it."
    echo "export-source.sh:   This is a build or deployment artifact being carried as though it were source."
    echo "export-source.sh:   The largest things in it:"
    du -sm "$dest"/* 2>/dev/null | sort -rn | head -10 | sed 's/^/export-source.sh:     /'
    echo "export-source.sh:   Clean the checkout ('git clean -xdff' removes what is not tracked), or raise"
    echo "export-source.sh:   GRAIN_SOURCE_EXPORT_MAX_MB if this repository has honestly grown this large."
  } >&2
  exit 1
fi

if [ -n "$history" ]; then
  echo "export-source.sh: staged ${used}MB of source and $(du -sm "$dest/.git" | cut -f1)MB of history from $src into $dest"
else
  echo "export-source.sh: staged ${used}MB of source from $src into $dest"
fi
