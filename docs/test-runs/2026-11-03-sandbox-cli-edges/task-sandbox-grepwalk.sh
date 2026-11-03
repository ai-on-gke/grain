#!/bin/bash
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

# grepwalk.sh <symbols.txt> <grain>: for each "pkg symbol file" line, grep the
# symbol's definition under src://pkg and src://, timing each, and print one
# table row per grep: root, package, symbol, exit, hits in pkg/<pkg>/, secs.
syms=$1; grain=${2:-grain}
while read -r pkg sym file; do
  for root in src://pkg src://; do
    start=$(date +%s.%N)
    out=$($grain grep "^(func|type) (\([^)]*\) )?$sym[ (\[]" "$root" 2>/tmp/grepwalk.err); rc=$?
    end=$(date +%s.%N)
    hits=$(printf '%s\n' "$out" | grep -c "pkg/$pkg/\|^$pkg/")
    trunc=$(printf '%s\n%s\n' "$out" "$(cat /tmp/grepwalk.err)" | grep -c truncated)
    printf '%s\t%s\t%s\texit=%s\thits=%s\ttrunc=%s\t%.2fs\t%s\n' "$root" "$pkg" "$sym" "$rc" "$hits" "$trunc" "$(echo "$end - $start" | bc)" "$(printf '%s' "$out" | head -1 | cut -c1-90)"
  done
done < "$syms"
