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

# Item 4 of §3 (docs/test-plans/2026-11-09.md): the edge cases of
# `grain demo -config-change`. Run it with a built grain:
#
#   GRAIN=/home/debian/bin/grain bash docs/test-runs/2026-11-09-demo-config-change/flags.sh
#
# It checks -config-change -external-config-repo is refused with exit 2,
# that -config-change starts beside -github-sim -fake-ci -git-proxy
# -secrets-store -reconcile 2s, and that SIGINT and SIGTERM each take the
# demo's process, ports and throwaway directory with them.
set -u
GRAIN=${GRAIN:-grain}
PORT=${PORT:-4296}
fail=0
check() { if eval "$2"; then echo "PASS $1"; else echo "FAIL $1"; fail=1; fi; }

out=$("$GRAIN" demo -config-change -external-config-repo https://example.com/acme/config.git -addr 127.0.0.1:$PORT 2>&1)
code=$?
echo "exclusive: exit $code: $out"
check "exclusive flags exit 2" '[ $code -eq 2 ]'
check "exclusive flags say why" 'grep -q "mutually exclusive" <<<"$out"'

# Job control on, so the demo is started with SIGINT at its default rather
# than ignored, as a terminal's Ctrl-C reaches it.
set -m
for sig in INT TERM; do
  log=$(mktemp)
  "$GRAIN" demo -config-change -github-sim -fake-ci -git-proxy -secrets-store -reconcile 2s \
    -addr 127.0.0.1:$PORT >"$log" 2>&1 </dev/null &
  pid=$!
  for _ in $(seq 60); do grep -q "serving fake tasks" "$log" && break; sleep 0.5; done
  check "$sig: started with every flag" 'grep -q "serving fake tasks" "$log"'
  dir=$(grep -o '/tmp/grain-demo-[0-9]*' "$log" | head -1)
  proxy=$(grep -o 'git proxy listens on http://[0-9.:]*' "$log" | grep -o '[0-9]*$')
  check "$sig: throwaway dir exists while serving ($dir)" '[ -d "$dir" ]'
  kill -$sig $pid
  wait $pid
  echo "$sig: exit status $?"
  check "$sig: process gone" '! kill -0 $pid 2>/dev/null'
  check "$sig: throwaway dir removed" '[ ! -e "$dir" ]'
  check "$sig: UI port closed" '! (exec 3<>/dev/tcp/127.0.0.1/$PORT) 2>/dev/null'
  check "$sig: git proxy port $proxy closed" '! (exec 3<>/dev/tcp/127.0.0.1/$proxy) 2>/dev/null'
  rm -f "$log"
done
exit $fail
