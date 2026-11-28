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

# up.sh [extra daemon flags...]: a fresh environment (STORE=sqlite|postgres),
# with alice@example.com and bob@example.com on the allowlist, bob with a
# role scoped to another repository only.
set -euo pipefail
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/env.sh"
init_env "${WORK_DIR:-/tmp/walk-pitl}"
start_ghsim
start_hub
start_daemon "$GRAIN" "$WORK_DIR" "$@"
echo "daemon up on $DAEMON (store ${STORE:-sqlite}); access code in $WORK_DIR/data/local-api-token"
