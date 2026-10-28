#!/bin/bash
# The rest of the procedure: the refusals, the import-pending refusal and
# its way out, and what an export costs when one row changed.
set -u
export PATH=$PATH:/home/debian/bin
say() { echo; echo "############ $*"; echo; }

# ---------------------------------------------------------------- conflicts
say "CONFLICTS: four decisions grain has no basis to make, on one store"
bash "$(dirname "$0")/refusals.sh" conflicts

# ---------------------------------------------------------------- pending
say "PENDING: a settings change merged into the repository between report and apply"
root=/home/debian/cases/pending
rm -rf "$root"; mkdir -p "$root"
data=$root/data; bare=$root/remote.git
cp -r /home/debian/store-base "$data"
git init -q --bare -b main "$bare"
grain state -data-dir "$data" adopt -remote "file://$bare" 2>&1 | tail -2

say "somebody merges an ordinary settings change into the repository"
clone=$root/clone
git clone -q "$bare" "$clone"
python3 - "$clone/tables/grain_config.json" <<'EOF'
import json,sys
p=sys.argv[1]
rows=json.load(open(p))
row = rows[0] if isinstance(rows,list) else rows
if isinstance(rows,list):
    rows[0]["prompt_extension"]="Say what you measured, not what you expect."
else:
    rows["prompt_extension"]="Say what you measured, not what you expect."
json.dump(rows,open(p,"w"),indent=2)
EOF
git -C "$clone" commit -qam "Settings: a deployment-wide prompt extension" && git -C "$clone" push -q origin main
echo "merged: $(git -C "$bare" log --oneline -1)"

say "-apply, with that commit waiting"
grain repo-case -data-dir "$data" -apply
echo "exit: $?"

say "did it write anything?"
handrun -mode census -dir "$data" | grep -E "bwsalmon/Grain|BWSalmon/kontur" | head -20

say "the way out: start the daemon so it takes the commit up, stop it, run it again"
timeout 60 grain daemon -data-dir "$data" -ui-addr 127.0.0.1:8432 -ci=false \
  -sandbox-dir /home/debian/sandboxes > "$root/daemon.log" 2>&1
grep -iE "state|import|prompt" "$root/daemon.log" | head -8
grain repo-case -data-dir "$data" -apply | tail -8
echo "exit: $?"

say "and the prompt extension that was merged is still there"
python3 - "$data/state-repo/tables/grain_config.json" <<'EOF'
import json,sys
rows=json.load(open(sys.argv[1]))
row = rows[0] if isinstance(rows,list) else rows
print("prompt_extension:", repr(row.get("prompt_extension")))
EOF

say "and a second start does not put the old spellings back"
timeout 60 grain daemon -data-dir "$data" -ui-addr 127.0.0.1:8432 -ci=false \
  -sandbox-dir /home/debian/sandboxes > "$root/daemon2.log" 2>&1
grain repo-case -data-dir "$data" | tail -3

# ---------------------------------------------------------------- one row
say "ONE ROW: what the export costs when a single task_run row moved"
root=/home/debian/cases/onerun
rm -rf "$root"; mkdir -p "$root"
cp -r /home/debian/cases/apply/data "$root/data"
cp -r /home/debian/cases/apply/remote.git "$root/remote.git"
git -C "$root/data/state-repo" remote set-url origin "file://$root/remote.git"
python3 - "$root/data/state-repo.json" <<'EOF'
import json,sys,os
p=sys.argv[1]
s=json.load(open(p))
for k,v in list(s.items()):
    if isinstance(v,str) and "cases/apply" in v:
        s[k]=v.replace("cases/apply","cases/onerun")
json.dump(s,open(p,"w"),indent=2)
EOF
handrun -mode one-run -dir "$root/data"
before=$(du -sk "$root/remote.git" | cut -f1)
grain repo-case -data-dir "$root/data" -apply | tail -6
after=$(du -sk "$root/remote.git" | cut -f1)
echo "bare repository: ${before}K -> ${after}K"
echo "--- what that commit touched"
git -C "$root/data/state-repo" show --stat --format= HEAD | tail -10
