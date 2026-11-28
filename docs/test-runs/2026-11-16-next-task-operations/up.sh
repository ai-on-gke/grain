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

# Brings up the §7 environment and leaves it running:
#   STORE=sqlite ./up.sh /var/tmp/walk-ops
set -euo pipefail
HERE7=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=env.sh
source "$HERE7/env.sh"
init_ops_env "${1:-/var/tmp/walk-ops-${STORE:-sqlite}}"
start_ghsim
start_iapsim
shift || true
start_daemon "$WORK_DIR" "${IAP_FLAGS[@]}" "$@"
echo "WORK_DIR=$WORK_DIR DAEMON=$DAEMON CODE=$CODE"
