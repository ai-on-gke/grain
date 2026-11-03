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

# start-daemon.sh [binary] [data-dir]: a real grain daemon with Docker
# sandboxes (image grain-sbx:walk) and grain CI, whose GitHub is githost on
# 127.0.0.1:4250. Containers reach grain at docker0's address.
bin=${1:-/home/debian/bin/grain}
data=${2:-/home/debian/wt/d}
proxyhost=$(ip -4 -o addr show docker0 | awk '{print $4}' | cut -d/ -f1)
mkdir -p "$data" /home/debian/wt/sbx
setsid "$bin" daemon -data-dir "$data" -sandbox-dir /home/debian/wt/sbx -ui-addr 127.0.0.1:7811 \
  -sandbox-port-base=0 -sandbox-backend container -container-sandbox-image grain-sbx:walk \
  -container-git-proxy-host "$proxyhost" -github-host 127.0.0.1:4250 -github-insecure-http \
  >>"$data.log" 2>&1 < /dev/null &
echo $! > "$data.pid"
