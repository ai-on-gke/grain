#!/bin/bash
# A copy with every conflict this command can meet seeded into it at
# once: the report names them all and writes nothing.
set -u
export PATH=$PATH:/home/debian/bin
name=$1
root=/home/debian/cases/$name
rm -rf "$root"; mkdir -p "$root"
data=$root/data; bare=$root/remote.git
cp -r /home/debian/store-base "$data"
git init -q --bare -b main "$bare"
grain state -data-dir "$data" adopt -remote "file://$bare" 2>&1 | tail -1
for c in conflict-release conflict-prefix conflict-base conflict-memory; do
  handrun -mode "$c" -dir "$data"
done
echo; echo "--- the report"
grain repo-case -data-dir "$data"
echo "report exit: $?"
echo; echo "--- -apply"
grain repo-case -data-dir "$data" -apply | tail -12
echo "apply exit: $?"
echo; echo "--- nothing was written: every divergent spelling is still there"
handrun -mode census -dir "$data" | grep -E "bwsalmon/Grain|BWSalmon/kontur"
echo; echo "--- and the state repository has no new commit"
git -C "$bare" log --oneline | head -3
