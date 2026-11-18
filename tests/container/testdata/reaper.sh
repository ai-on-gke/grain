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

# Run through the image's default Tini entrypoint as its unprivileged user.
set -eu
tini_pid=$PPID
stage=$(mktemp -d)
orphan=
trap '
  if [ -n "$orphan" ]; then
    kill "$orphan" 2>/dev/null || true
  fi
  rm -rf "$stage"
' EXIT

# The intermediate shell exits without waiting for its child. Keep the
# orphan alive until we have checked who adopted it, then make it a zombie
# and require Tini to reap it while the main process is still running.
sh -c 'sleep 30 & echo $! > "$1"' sh "$stage/pid"
orphan=$(cat "$stage/pid")
adopted=0
i=0
while [ "$i" -lt 100 ]; do
  parent=$(awk '/^PPid:/ {print $2}' "/proc/$orphan/status")
  if [ "$parent" = "$tini_pid" ]; then
    adopted=1
    break
  fi
  i=$((i + 1))
  sleep 0.05
done
if [ "$adopted" != 1 ]; then
  echo "orphan $orphan adopted by $parent, expected Tini $tini_pid" >&2
  exit 1
fi

kill "$orphan"
i=0
while [ "$i" -lt 100 ]; do
  if [ ! -e "/proc/$orphan" ]; then
    orphan=
    echo orphan-reaped
    exit 0
  fi
  i=$((i + 1))
  sleep 0.05
done
echo "Tini did not reap orphan $orphan" >&2
cat "/proc/$orphan/status" >&2
exit 1
