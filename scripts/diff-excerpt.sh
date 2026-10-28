#!/usr/bin/env bash
# diff-excerpt.sh -- the first few changed lines of a unified diff, as one
# escaped string a GitHub check annotation can carry.
#
# Written for .github/workflows/nightly.yml's guest-cache-drift job, which
# reports each of its two comparisons three ways -- an annotation, a step
# summary section and the `guest-fingerprints` artifact -- and can only
# count on one of the three reaching its reader. docs/nightly-test-review.md
# is written for an agent going through the night's runs, and what an agent
# holding no credential can actually read is:
#
#   the job log      403 without admin on the repository
#   the artifact     401 without a token carrying actions:read
#   the step summary rendered on the run page for a human in a browser,
#                    but not in that page's own HTML and not in any REST
#                    endpoint
#   the annotation   readable by anyone who can see the repository
#
# So the annotation is the channel that survives, and a count is not what
# that reader is asked to judge: "is a security update the guest should
# carry in the list" and "which input escaped the cache key" are both
# questions about the values. This puts the values in the annotation. The
# summary and the artifact keep the whole diff, which is what the reader
# goes to next and what the "... and N more" tail points at.
#
# Prints nothing, and exits 0, for a diff with no changed lines in it: a
# job with nothing to excerpt still has its own sentence to annotate.
#
# Usage:
#   ./scripts/diff-excerpt.sh <diff-file> [max-lines]
#
# The output is escaped the way GitHub's own toolkit escapes the data half
# of a workflow command -- `%` as `%25`, a newline as `%0A` -- so it can be
# interpolated straight into a `::warning::` or `::error::` message and
# comes out as separate lines in the annotation. Do not escape it again.
set -euo pipefail

if [ "$#" -lt 1 ] || [ "$#" -gt 2 ] || [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  cat >&2 <<'USAGE'
usage: diff-excerpt.sh <diff-file> [max-lines]

  <diff-file>  a unified diff (`diff -u a b`)
  <max-lines>  how many changed lines to name before summarising the
               rest as "... and N more" (default 8)
USAGE
  exit 2
fi

diff_file="$1"
max="${2:-8}"

if [ ! -r "$diff_file" ]; then
  echo "diff-excerpt.sh: cannot read ${diff_file}" >&2
  exit 1
fi
case "$max" in
  '' | *[!0-9]*)
    echo "diff-excerpt.sh: max-lines must be a number, got ${max}" >&2
    exit 2
    ;;
esac

# A changed line is `^[+-][^+-]`, not the `---`/`+++` headers a unified
# diff opens with -- the same expression the workflow counts with, so the
# excerpt and the count in the sentence above it are about one set of
# lines.
#
# Interleaved rather than taken in file order, which is the difference
# between an excerpt and a useless one: `diff -u` emits a hunk's removals
# and then its additions, so the first eight lines of a hunk that moved
# thirty package versions are eight *cached* values with nothing to compare
# them against. Each run of removals is zipped with the run of additions
# that follows it, so what the reader gets is before/after pairs -- and a
# truncated excerpt is four values that moved rather than eight halves of
# values.
#
# Each line is then squeezed onto one line of its own and cut to a length
# an annotation renders without a scrollbar. A fingerprint line is
# `key: value`, so the cut keeps the key and the head of the value, which
# is the half that says *what* moved; the artifact has the whole of it.
awk -v max="$max" '
  function keep(line) {
    n++
    if (n > max) return
    gsub(/[[:space:]]+/, " ", line)
    sub(/ $/, "", line)
    if (length(line) > 200) line = substr(line, 1, 197) "..."
    gsub(/%/, "%25", line)
    kept[n] = line
  }
  function zip(   i, most) {
    most = (removed > added) ? removed : added
    for (i = 1; i <= most; i++) {
      if (i <= removed) keep(minus[i])
      if (i <= added) keep(plus[i])
    }
    removed = 0
    added = 0
  }
  /^-[^-]/  { minus[++removed] = $0; next }
  /^\+[^+]/ { plus[++added] = $0;    next }
  # Anything else -- a context line, a hunk header, the file headers --
  # ends the run, so removals are only ever paired with the additions that
  # actually followed them.
  { zip() }
  END {
    zip()
    if (n == 0) exit 0
    out = kept[1]
    for (i = 2; i <= n && i <= max; i++) out = out "%0A" kept[i]
    if (n > max) out = out "%0A... and " (n - max) " more"
    print out
  }
' "$diff_file"
