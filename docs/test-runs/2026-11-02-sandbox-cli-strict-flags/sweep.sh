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

# sweep.sh <home> <cmds-file> <transcript>: each line of cmds-file run as
# `grain <line>` with the sandbox grain, recording its exit status, its
# output, and every request the logging forwarder in front of the git
# proxy saw while it ran.
home=$1; cmds=$2; out=$3; : > "$out"
while IFS= read -r line; do
  [ -z "$line" ] && continue; case "$line" in \#*) echo "$line" >> "$out"; continue;; esac
  before=$(wc -l < /home/debian/wt/proxy.log)
  eval "set -- $line"
  res=$(/home/debian/wt/sb.sh "$home" "$@" 2>&1 < /dev/null); code=$?
  sleep 0.05
  reqs=$(tail -n +$((before+1)) /home/debian/wt/proxy.log | cut -d' ' -f3- | tr '\n' ';')
  { echo "\$ grain $line"; echo "  exit=$code proxy=[${reqs:-none}]"; echo "$res" | head -${LINES_KEEP:-6} | sed 's/^/  | /'; } >> "$out"
done < "$cmds"
