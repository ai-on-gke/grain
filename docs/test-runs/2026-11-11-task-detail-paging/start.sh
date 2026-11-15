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

# start.sh <grain> <data-dir> <port> [daemon flags...]: a real `grain daemon`
# on host sandboxes with -sandbox-port-base=0, waiting until its API answers.
bin=$1 dir=$2 port=$3; shift 3
mkdir -p "$dir"
setsid "$bin" daemon -data-dir "$dir" -ui-addr "127.0.0.1:$port" \
  -sandbox-backend host -sandbox-dir "$dir-sbx" -sandbox-port-base=0 -poll-interval 2s "$@" >>"$dir.log" 2>&1 </dev/null &
echo $! >"$dir.pid"
for _ in $(seq 90); do
  kill -0 "$(cat "$dir.pid")" 2>/dev/null || { echo "daemon on $port exited; see $dir.log" >&2; exit 1; }
  [ -s "$dir/local-api-token" ] && curl -fs -o /dev/null -H "Authorization: Bearer $(cat "$dir/local-api-token")" "http://127.0.0.1:$port/api/config" && exit 0
  sleep 1
done
echo "daemon on $port did not come up; see $dir.log" >&2; exit 1
