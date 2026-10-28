#!/usr/bin/env bash
# Resolve what `go build -buildvcs=` should be for a build in this
# checkout, and print it -- `auto`, `true` or `false` -- for the Makefile
# and the Dockerfiles to pass straight on to `go build`.
#
# Only `auto`, the default, is ever resolved to anything else. `true`
# ("stamp the commit or fail") and `false` ("do not stamp it") are
# decisions somebody has already taken on the command line, and are
# printed back untouched.
#
# Why `auto` needs resolving at all. It reads as "stamp the commit if you
# can", and in a tree with no repository in it that is exactly what it
# does. In a tree holding a repository `go build` cannot *read*, it does
# the opposite of what it reads as -- the build stops, in full, with
#
#   error obtaining VCS status: exit status 128
#   Use -buildvcs=false to disable VCS stamping.
#
# Every failure of the git `go build` shells out to arrives under that one
# message with git's own explanation swallowed, and all that is actually
# at stake is the commit stamp: `go version -m bin/grain`'s vcs.revision,
# which pkg/version reads back for the version line in the UI. The ways a
# checkout gets there are environmental, never anything about the source
# being built:
#
#   * ownership -- the uid running the build does not own the tree and
#     git refuses a repository it reads as someone else's ("detected
#     dubious ownership"). The Makefile's container-build sets
#     safe.directory for the mount, and the Dockerfile sets it for the
#     copy, because that is the fix that *keeps* the stamp; neither
#     covers a tree a host has arranged some third way.
#   * a git inside the builder older than the git that wrote the
#     checkout. An index or a repository extension it does not recognise
#     -- a workstation git writing `index.skipHash`, a repository created
#     with reftables -- is fatal to the Debian 12 git in the build image
#     and perfectly readable to the git on the host that cloned it.
#   * a .git that does not describe a repository from where the build
#     runs: a linked worktree or a submodule whose real gitdir is outside
#     the mount or the build context, or an export carrying a .git
#     without its objects.
#
# None of those is a reason to refuse to build grain at all, and every
# one of them costs the stamp and nothing else -- so this probes first
# and falls back to `false`, saying so, with git's own message. That
# message is the one thing an operator could act on and the one thing
# `go build` does not print.
set -euo pipefail

setting="${1:-auto}"
if [ "$setting" != auto ]; then
  printf '%s\n' "$setting"
  exit 0
fi

# The same two questions `go build` puts to git -- which commit is this,
# and has the tree been modified since -- asked here so that their
# failure is a fallback with an explanation rather than a build that
# stops.
if git rev-parse --verify --quiet HEAD >/dev/null 2>&1 &&
    git status --porcelain >/dev/null 2>&1; then
  printf 'auto\n'
  exit 0
fi

# No repository here at all: an export, a tarball fed to `docker build`,
# a vendored copy. `auto` already produces an unstamped binary for that,
# so `false` is the same build by another name and there is nothing to
# report.
if [ ! -e .git ] && ! git rev-parse --git-dir >/dev/null 2>&1; then
  printf 'false\n'
  exit 0
fi

# There is a repository and git cannot use it. Pass on whatever git said,
# because that names the cause; on one line, since a build log is where
# it lands.
reason="$(git status --porcelain 2>&1 >/dev/null | tr '\n' ' ' || true)"
if [ -z "$reason" ]; then
  reason="$(git rev-parse --verify HEAD 2>&1 >/dev/null | tr '\n' ' ' || true)"
fi
{
  echo "buildvcs.sh: building without a commit stamp: git cannot read the repository in $PWD"
  if [ -n "$reason" ]; then
    echo "buildvcs.sh:   git says: $reason"
  fi
  echo "buildvcs.sh:   the binary is otherwise identical; only 'go version -m' loses vcs.revision"
  echo "buildvcs.sh:   pass BUILDVCS=true to make this a build failure instead"
} >&2
printf 'false\n'
