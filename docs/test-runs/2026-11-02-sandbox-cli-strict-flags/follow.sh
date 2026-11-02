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

# follow.sh <out-prefix> [first-read args...]: read deploy://pipeline-changes,
# then run each page's `next:` line until a page says no more follow.
pre=$1; shift; i=1
args=("read" "deploy://pipeline-changes" "$@")
while :; do
  ./sb.sh ~/wt/h2 "${args[@]}" > "$pre-$i.txt" 2>&1; code=$?
  n=$(grep -cv '^next:\|^(more\|^Nothing' "$pre-$i.txt")
  echo "page $i: grain ${args[*]} -> exit $code, $n changes, $(grep -c '^(more' "$pre-$i.txt") more-marker; $(tail -1 "$pre-$i.txt")"
  grep -q '^(more' "$pre-$i.txt" || break
  read -ra args <<< "$(tail -1 "$pre-$i.txt" | sed 's/^next: grain //')"
  i=$((i+1)); [ $i -gt 60 ] && break
done
