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

# start-daemon.sh ROOT [PORT]: shared step 3 for §2 items 4-5 -- a real
# `grain daemon` on loopback in image-upgrade mode against the local OCI
# registry at localhost:5000 (`docker run -d -p 127.0.0.1:5000:5000
# registry:2`), under a supervisor loop that starts it again whenever it
# exits. The restart command (restart.sh) is what a systemd unit's
# `systemctl restart` would be: it logs the GRAIN_IMAGE the upgrade wrote
# to ROOT/image.env and stops the daemon, and the loop brings it back --
# so a cut-over is a real process restart on the same data dir.
#
# ROOT/image.env must already hold GRAIN_IMAGE=localhost:5000/grain@<digest>
# of the release the deployment "runs".
set -euo pipefail
root="${1:?root}"
port="${2:-18090}"
grain="${GRAIN:-/home/debian/bin/grain}"
mkdir -p "$root/data"
cat >"$root/restart.sh" <<EOF
#!/bin/sh
echo "\$(date -u +%FT%TZ) restart requested; \$(cat '$root/image.env')" >>'$root/restarts.log'
kill -TERM "\$(cat '$root/daemon.pid')"
EOF
chmod +x "$root/restart.sh"
cat >"$root/supervise.sh" <<EOF
#!/bin/sh
while [ ! -f '$root/stop' ]; do
  '$grain' daemon -data-dir '$root/data' -ui-addr 127.0.0.1:$port -sandbox-port-base=0 -disable-native-sandboxes \\
    -upgrade-image localhost:5000/grain -upgrade-image-source registry \\
    -upgrade-image-ref-file '$root/image.env' -upgrade-restart-cmd '$root/restart.sh' \\
    -upgrade-allowed-branches 'latest,v[0-9]*.[0-9]*.[0-9]*' >>'$root/daemon.log' 2>&1 &
  echo \$! >'$root/daemon.pid'
  wait \$!
  echo "\$(date -u +%FT%TZ) daemon exited (\$?)" >>'$root/daemon.log'
  sleep 1
done
EOF
chmod +x "$root/supervise.sh"
rm -f "$root/stop"
nohup "$root/supervise.sh" >/dev/null 2>&1 </dev/null &
echo $! >"$root/supervise.pid"
for _ in $(seq 1 120); do
  curl -fsS -o /dev/null "http://127.0.0.1:$port/healthz" 2>/dev/null && break
  sleep 0.5
done
echo "daemon on 127.0.0.1:$port (supervisor $(cat "$root/supervise.pid"), daemon $(cat "$root/daemon.pid"))"
