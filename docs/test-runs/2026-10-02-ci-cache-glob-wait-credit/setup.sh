#!/bin/sh
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

# Builds /tmp/w7/widgets.git (acme/widgets) and /tmp/w7/gadgets.git
# (acme/gadgets) for docs/test-runs/2026-10-02-ci-cache-glob-wait-credit.md.
set -e
rm -rf /tmp/w7 && mkdir -p /tmp/w7
git init -q --bare -b main /tmp/w7/widgets.git
git init -q --bare -b main /tmp/w7/gadgets.git
W=/tmp/w7/seed; git init -q -b main $W; cd $W
git config user.email walk@example.com; git config user.name walk
P() { git push -q /tmp/w7/widgets.git "$1"; }

# --- main: the Actions-cache workflow --------------------------------------
# ci/cache.sh speaks the Actions cache protocol exactly as actions/cache's
# toolkit does (GET .../cache?keys=&version=, POST .../caches, PATCH with a
# Content-Range, POST .../caches/{id}, GET the archiveLocation), against the
# ACTIONS_CACHE_URL grain hands the job. It restores key k, prints what it
# got, then saves k with a body naming the ref and event that wrote it.
mkdir -p .grain/workflows ci
cat > ci/cache.sh <<'EOF'
#!/bin/sh
set -u
U="${ACTIONS_CACHE_URL:?no ACTIONS_CACHE_URL}_apis/artifactcache"
V=walk7-v1
code=$(curl -s -o /tmp/find.$$ -w '%{http_code}' "$U/cache?keys=k&version=$V")
if [ "$code" = 200 ]; then
  loc=$(sed 's/.*"archiveLocation":"\([^"]*\)".*/\1/' /tmp/find.$$)
  echo "RESTORE k: HIT -> $(curl -s "$loc")"
else
  echo "RESTORE k: MISS (find answered $code)"
fi
body="written by ref=$GRAIN_CI_REF event=$GRAIN_CI_EVENT commit=$GRAIN_CI_COMMIT"
size=$(printf %s "$body" | wc -c)
res=$(curl -s -X POST -H 'Content-Type: application/json' -d "{\"key\":\"k\",\"version\":\"$V\",\"cacheSize\":$size}" "$U/caches")
id=$(echo "$res" | sed -n 's/.*"cacheId":\([0-9]*\).*/\1/p')
if [ -z "$id" ]; then echo "SAVE k: reserve refused: $res"; exit 0; fi
printf %s "$body" | curl -s -X PATCH -H "Content-Range: bytes 0-$((size-1))/*" --data-binary @- "$U/caches/$id" >/dev/null
echo "SAVE k: entry $id committed: $(curl -s -X POST -H 'Content-Type: application/json' -d "{\"size\":$size}" "$U/caches/$id") body=[$body]"
EOF
chmod +x ci/cache.sh
cat > .grain/workflows/cache.yml <<'EOF'
name: Cache
on: [push, release]
jobs:
  cache:
    cache: false
    steps:
      - run: sh ci/cache.sh
EOF
echo widgets > README; git add -A; git commit -qm "main: actions cache restore/save of key k"; P main

# Task branch: same workflow, so it saves k under walk/cache.
git checkout -qb walk/cache main
echo "task branch" > TASK; git add -A; git commit -qm "task branch walk/cache"; P walk/cache
git checkout -qb walk/cache2 main
echo "second branch" > TASK; git add -A; git commit -qm "second branch walk/cache2"; P walk/cache2

# --- walk/paths: a job keyed on docs/**/*.md --------------------------------
git checkout -qb walk/paths main
git rm -rq .grain/workflows/cache.yml; mkdir -p .grain/workflows
mkdir -p docs/x/y
echo one > docs/a.md; echo one > docs/x/y/a.md; echo notes > docs/notes.txt
cat > .grain/workflows/docs.yml <<'EOF'
name: Docs
on: [push]
jobs:
  docs:
    paths: ['docs/**/*.md']
    steps:
      - run: echo "docs job ran at $GRAIN_CI_COMMIT"; cat docs/a.md docs/x/y/a.md
EOF
git add -A; git commit -qm "paths: docs job keyed on docs/**/*.md"; P walk/paths

# A trigger-level paths filter, GitHub's spelling.
git checkout -qb walk/paths-trigger walk/paths
cat > .grain/workflows/docs.yml <<'EOF'
name: Docs
on:
  push:
    paths: ['docs/**/*.md']
jobs:
  docs:
    steps:
      - run: echo ran
EOF
git commit -qam "paths-trigger: on.push.paths"; P walk/paths-trigger

# --- quarantine: a quarantined test inside a job that does not finish -------
git checkout -qb walk/q-control main
git rm -rq .grain/workflows/cache.yml; mkdir -p .grain/workflows
cat > .grain/quarantine.yml <<'EOF'
quarantined_tests:
  - test_name: "TestFoo"
    reason: walkthrough quarantine
EOF
qwf() { # $1 job-level extras, $2 step-level extras, $3 script tail
cat > .grain/workflows/q.yml <<EOF
name: Q
on: [push]
jobs:
  q:
    cache: false
$1
    steps:
      - name: tests
$2
        run: |
          echo "=== RUN   TestFoo"
          echo "--- FAIL: TestFoo (0.00s)"
          echo "FAIL"
          $3
EOF
}
qwf "" "" "exit 1"
git add -A; git commit -qm "q-control: quarantined TestFoo fails and exits"; P walk/q-control
git checkout -qb walk/q-cancel walk/q-control
qwf "" "" "sleep 300; exit 1"
git commit -qam "q-cancel: quarantined failure, then hang (cancelled from the UI)"; P walk/q-cancel
git checkout -qb walk/q-jobtimeout walk/q-control
qwf "    timeout-minutes: 1" "" "sleep 300; exit 1"
git commit -qam "q-jobtimeout: job timeout-minutes 1"; P walk/q-jobtimeout
git checkout -qb walk/q-steptimeout walk/q-control
qwf "" "        timeout-minutes: 1" "sleep 300; exit 1"
git commit -qam "q-steptimeout: step timeout-minutes 1"; P walk/q-steptimeout

# --- walk/wait: a job that blocks for a while, never cached -----------------
git checkout -qb walk/wait main
# Keeps main's cache.yml: the base-branch gate refuses a branch dropping it.
cat > .grain/workflows/slow.yml <<'EOF'
name: Slow
on: [push]
jobs:
  slow:
    cache: false
    steps:
      - run: sleep 12; echo slow ok
EOF
git add -A; git commit -qm "wait: a 12s uncached job"; P walk/wait

# --- acme/gadgets: the reads repository -------------------------------------
git checkout -q --orphan gadgets
git rm -rqf .
mkdir -p .grain/workflows
cat > .grain/workflows/g.yml <<'EOF'
name: G
on: [push]
jobs:
  g:
    steps:
      - run: echo gadgets ok
EOF
git add -A; git commit -qm "gadgets main"; git push -q /tmp/w7/gadgets.git gadgets:main
git -C /tmp/w7/widgets.git branch -a
