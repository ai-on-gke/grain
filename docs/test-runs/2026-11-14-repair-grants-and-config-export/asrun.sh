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

# asrun.sh DATADIR TASK [driver dsn] -- then commands on stdin, one per line
dd=$1; task=$2; driver=${3:-sqlite}; dsn=${4:-}
rm -rf /home/debian/wt/px$task; mkdir -p /home/debian/wt/h$task
/home/debian/bin/runproxy -db-driver $driver -db-dsn "$dsn" -data-dir $dd -scratch /home/debian/wt/px$task -task $task > /home/debian/wt/px$task.log 2>&1 &
pid=$!
for _ in $(seq 20); do grep -q "proxy http" /home/debian/wt/px$task.log && break; sleep 1; done
url=$(grep -o "proxy http[^ ]*" /home/debian/wt/px$task.log | cut -d' ' -f2)
[ -z "$url" ] && { cat /home/debian/wt/px$task.log; kill $pid; exit 1; }
while read -r cmd; do echo "--- [task $task] grain $cmd"; HOME=/home/debian/wt/h$task GRAIN_SANDBOX_PROXY=$url GRAIN_SANDBOX_TOKEN=walk-token eval "timeout 20 /home/debian/bin/grain $cmd" 2>&1 | head -14; done
kill -INT $pid; wait $pid
