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

# (Re)start the walkthrough's real daemon: the one `grain init workstation
# -non-interactive -repo acme/widgets -sandbox-backend host -model-backend
# claude -ui-addr 127.0.0.1:18702 -start-service=false` wrote under
# HOME=/home/debian/live. Logs go to $LIVE/daemon.log.
LIVE=${LIVE:-/home/debian/live}
for pid in $(pgrep -f '^/home/debian/bin/grain daemon'); do kill "$pid"; done
sleep 3
cd "$LIVE" || exit 1
HOME=$LIVE setsid nohup "$LIVE/.grain/daemon.sh" -sandbox-port-base=0 \
  -github-host 127.0.0.1:1 -github-insecure-http >>"$LIVE/daemon.log" 2>&1 </dev/null &
for _ in $(seq 1 60); do
  curl -s -o /dev/null http://127.0.0.1:18702/ && exit 0
  sleep 1
done
echo "daemon did not answer" >&2
exit 1
