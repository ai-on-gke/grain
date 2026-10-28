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

# Builds /tmp/walk/widgets.git: main (jobs a, b) and the walkthrough branches.
set -e
rm -rf /tmp/walk && mkdir -p /tmp/walk/markers
git init -q --bare -b main /tmp/walk/widgets.git
W=/tmp/walk/seed; git init -q -b main $W; cd $W
git config user.email walk@example.com; git config user.name walk
mkdir -p .grain/workflows
cat > .grain/workflows/ci.yml <<'EOF'
name: CI
on: [push, pull_request]
jobs:
  a:
    steps:
      - run: echo job a ok
  b:
    steps:
      - run: echo job b ok
EOF
echo widgets > README; git add -A; git commit -qm "main: jobs a and b"; git push -q /tmp/walk/widgets.git main

git checkout -qb walk/x main
cat > .grain/workflows/ci.yml <<'EOF'
name: CI
on: [push, pull_request]
jobs:
  a:
    steps:
      - run: echo job a ok
EOF
git commit -qam "x: drop job b"; git push -q /tmp/walk/widgets.git walk/x

git checkout -qb walk/y main
git rm -rq .grain/workflows; git commit -qm "y: delete .grain/workflows"; git push -q /tmp/walk/widgets.git walk/y

git checkout -qb walk/z main
cat >> .grain/workflows/ci.yml <<'EOF'
  c:
    steps:
      - run: echo job c ok
EOF
git commit -qam "z: add job c"; git push -q /tmp/walk/widgets.git walk/z

git checkout -qb walk/r main
cat > .grain/workflows/ci.yml <<'EOF'
name: CI
on: [push, pull_request]
jobs:
  a:
    retry: true
    steps:
      - name: fail the first attempt only
        run: |
          if [ ! -f /tmp/walk/markers/a-first-attempt ]; then
            touch /tmp/walk/markers/a-first-attempt
            echo "first attempt: failing on purpose"
            exit 1
          fi
          echo "second attempt: working for 40s"
          sleep 40
          echo job a ok
  b:
    steps:
      - run: echo job b ok
EOF
git commit -qam "r: job a fails on its first attempt"; git push -q /tmp/walk/widgets.git walk/r

git checkout -qb walk/q main
cat > .grain/quarantine.yml <<'EOF'
quarantined_tests:
  - test_name: "TestFoo.*"
    reason: walkthrough literal-match check
EOF
cat > .grain/workflows/ci.yml <<'EOF'
name: CI
on: [push, pull_request]
jobs:
  a:
    steps:
      - run: |
          echo "--- FAIL: TestFooBar (0.01s)"
          echo "FAIL"
          exit 1
  b:
    steps:
      - run: echo job b ok
EOF
git add -A; git commit -qm "q: quarantine TestFoo.* and fail TestFooBar"; git push -q /tmp/walk/widgets.git walk/q
git -C /tmp/walk/widgets.git branch -a
git checkout -qb walk/q2 walk/q
sed -i 's/TestFoo\.\*/TestFooBar/' .grain/quarantine.yml
git commit -qam "q2: quarantine TestFooBar literally (positive control)"; git push -q /tmp/walk/widgets.git walk/q2
