#!/bin/bash
# drive.sh -- the procedure docs/test-plans/2026-09-06.md section 5 asks
# for, run against a copy: adopt a state repository, report, apply, start
# a daemon on the copy, read the diff the export left behind.
#
# Usage: drive.sh <case-name> [conflict-mode]
#   drive.sh apply                  the happy path
#   drive.sh memory conflict-memory a refusal, and what it leaves behind
set -u
export PATH=$PATH:/home/debian/bin
name=$1
conflict=${2:-}
root=/home/debian/cases/$name
rm -rf "$root"
mkdir -p "$root"
data=$root/data
bare=$root/remote.git

timed() { local label=$1; shift; local t0=$(date +%s%3N); "$@"; local rc=$?; local t1=$(date +%s%3N); echo "[$label: $(( (t1 - t0) / 1000 )).$(printf %03d $(( (t1 - t0) % 1000 ))) s]" >&2; return $rc; }

say() { echo; echo "=== $*"; echo; }

say "copy the data directory (never the live one)"
timed "copy" cp -r /home/debian/store-base "$data"
du -sh "$data/store"

git init -q --bare -b main "$bare"

say "adopt the state repository"
timed "adopt" grain state -data-dir "$data" adopt -remote "file://$bare" 2>&1 | tail -5
du -sh "$data/state" "$bare"
git -C "$bare" log --oneline | head -3
echo "dump, largest files:"
ls -S "$data/state/tables" 2>/dev/null | head -5 | while read -r f; do
  printf '  %-28s %s\n' "$f" "$(du -h "$data/state/tables/$f" | cut -f1)"
done

if [ -n "$conflict" ]; then
  say "seed the conflict: $conflict"
  handrun -mode "$conflict" -dir "$data"
fi

say "the report"
timed "report" grain repo-case -data-dir "$data"
echo "report exit: $?"

say "-apply"
timed "apply" grain repo-case -data-dir "$data" -apply
echo "apply exit: $?"

say "what the export committed"
git -C "$bare" log --oneline | head -3
head=$(git -C "$bare" rev-parse HEAD)
git -C "$data/state" log -1 --format='%s%n%n%b' | head -20
echo "--- diffstat"
git -C "$data/state" show --stat --format= HEAD | tail -30
echo "--- repository size now"
du -sh "$bare" "$data/state"
