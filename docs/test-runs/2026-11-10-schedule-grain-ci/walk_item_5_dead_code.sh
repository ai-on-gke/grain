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

# Walkthrough §4 Item 5: dry-run every command and check from
# .grain/templates/weekly-dead-code.yml in an isolated /tmp scratch clone.
set -euo pipefail

SRC_REPO=${SRC_REPO:-/home/debian/work}
SCRATCH=/tmp/deadcode-scratch-verify
OUT_DIR=/tmp/deadcode-out-verify
export PATH="/home/debian/bin:$HOME/go/bin:$PATH"

rm -rf "$SCRATCH" "$OUT_DIR"
mkdir -p "$OUT_DIR"
git clone -q "$SRC_REPO" "$SCRATCH"

command -v deadcode >/dev/null || go install golang.org/x/tools/cmd/deadcode@v0.38.0
command -v staticcheck >/dev/null || go install honnef.co/go/tools/cmd/staticcheck@2025.1.1

echo "=== Go deadcode & staticcheck U1000 ==="
(
  cd "$SCRATCH"
  deadcode ./cmd/... >"$OUT_DIR/root-prod.txt"
  deadcode -test ./cmd/... >"$OUT_DIR/root-cmd-test.txt"
  deadcode -test ./... >"$OUT_DIR/root-all-test.txt"
  staticcheck -checks U1000 ./... >"$OUT_DIR/root-u1000.txt" || true
)
(
  cd "$SCRATCH/grain-plc"
  deadcode ./cmd/... >"$OUT_DIR/plc-prod.txt"
  deadcode -test ./... >"$OUT_DIR/plc-all-test.txt"
  staticcheck -checks U1000 ./... >"$OUT_DIR/plc-u1000.txt" || true
)

for f in "$OUT_DIR"/*.txt; do
  printf "%-30s %5d lines\n" "$(basename "$f")" "$(wc -l <"$f")"
done

echo "=== TypeScript knip ==="
for d in ui ui-shared ui-hub; do
  if [ -d "$SCRATCH/$d" ]; then
    echo "--- knip $d ---"
    if [ ! -d "$SCRATCH/$d/node_modules" ] && [ -d "$SRC_REPO/$d/node_modules" ]; then
      ln -s "$SRC_REPO/$d/node_modules" "$SCRATCH/$d/node_modules"
    fi
    (cd "$SCRATCH/$d" && npx -y knip@5.70.0 --no-progress --reporter compact >"$OUT_DIR/knip-$d.txt" 2>&1 || true)
    cat "$OUT_DIR/knip-$d.txt"
  fi
done

echo "=== Allowlist audit ==="
ALLOWLIST="$SCRATCH/.grain/baselines/dead-code-allowlist.txt"
STALE=0
while IFS= read -r line; do
  trimmed="${line#"${line%%[![:space:]]*}"}"
  [ -z "$trimmed" ] && continue
  [[ "$trimmed" == \#* ]] && continue
  sym="${trimmed%% *}"
  pkg_or_path="${sym%:*}"
  if [[ "$pkg_or_path" == github.com/ai-on-gke/grain/* ]]; then
    rel="${pkg_or_path#github.com/ai-on-gke/grain/}"
    if [ ! -d "$SCRATCH/$rel" ]; then
      echo "STALE PACKAGE: $sym"
      STALE=$((STALE + 1))
    fi
  elif [ ! -e "$SCRATCH/$pkg_or_path" ]; then
    echo "STALE PATH: $sym"
    STALE=$((STALE + 1))
  fi
done <"$ALLOWLIST"
echo "Allowlist stale entries: $STALE"
[ "$STALE" -eq 0 ]

rm -rf "$SCRATCH"
echo "=== ITEM 5 DRY-RUN PASSED ==="
