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

# docs/test-plans/2026-11-15.md §7 item 4: a real `grain daemon` that
# reports an unmigrated host-mode install (pkg/daemon/installnotice.go).
# hostModeInstall wants the bare machine (no container marker; said
# outright with GRAIN_IN_CONTAINER=0), an -upgrade-image deployment, and a
# unit that says host mode -- here systemd's INVOCATION_ID with
# GRAIN_INSTALL_MODE=host, as the pre-v2.3.0 unit set them. A wrapper puts
# those three variables in front of the real binary, because env.sh's
# start_daemon starts it with `env -i`.
#
#   ./host_mode.sh            # leaves the daemon on 127.0.0.1:4290
#
# Writes $WORK_DIR/captures/{config.json,upgrade.json,daemon-notice.log}.

set -euo pipefail

ME=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
export DAEMON_PORT=${DAEMON_PORT:-4290} GHSIM_PORT=${GHSIM_PORT:-4294}
REAL_GRAIN=${GRAIN:-/home/debian/bin/grain}
# shellcheck source=../2026-11-13-grain-health-exit-codes/env.sh
source "$ME/../2026-11-13-grain-health-exit-codes/env.sh"
export STORE=${STORE:-sqlite}

work=/tmp/walk-hostmode-${STORE}
init_env "$work"
cat >"$work/bin/grain-hostmode" <<EOF
#!/bin/sh
exec env INVOCATION_ID=walkthrough-host-mode GRAIN_INSTALL_MODE=host GRAIN_IN_CONTAINER=0 "$REAL_GRAIN" "\$@"
EOF
chmod +x "$work/bin/grain-hostmode"
GRAIN="$work/bin/grain-hostmode"
start_ghsim "$work"
start_daemon "$work" -upgrade-image docker.io/grainai/grain -upgrade-image-ref-file "$work/upgrade-image-ref"

api GET /api/config | jq '.installNotice' >"$work/captures/config.json"
grep 'condition=host_mode_install' "$work/daemon.log" >"$work/captures/daemon-notice.log" || true
api GET /api/upgrade >"$work/captures/upgrade.json" 2>/dev/null || true
echo "installNotice:"
cat "$work/captures/config.json"
echo "log:"
cat "$work/captures/daemon-notice.log"
echo "daemon left running at $DAEMON (work $work, token $work/data/local-api-token)"
