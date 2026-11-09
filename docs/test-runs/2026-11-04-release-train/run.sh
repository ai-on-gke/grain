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

# Re-runs both walks of docs/test-runs/2026-11-04-release-train.md:
#
#   bash run.sh <grain-checkout> <scratch-dir>
#
# 1. walk      §1 items 1, 2, 3 and 6: the train, Stage 2 and the Actions
#              helper over githubsim (walk.log, json/);
# 2. walk -proxy  §1 items 4 and 5: the release-train template as a task
#              through the scoped git proxy (proxy-walk.log, json-proxy/).
#
# Every release script runs in a scratch clone under <scratch-dir> whose
# origin is a local bare repository; nothing is pushed anywhere else, and
# the checkout itself is only read (walk -proxy pushes its HEAD into the
# scratch upstream, so commit first). Each walk prints OK/FAIL per check.
set -euo pipefail

src="$(cd "$1" && pwd)"
scratch="$2"
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
mkdir -p "$scratch/bin"

(cd "$src" && go build -o "$scratch/bin/grain" ./cmd/grain)
(cd "$here/walk" && go build -o "$scratch/bin/walk" .)

"$scratch/bin/walk" -src "$src" -dir "$scratch/walk" -out "$scratch/json" | tee "$scratch/walk.log"
"$scratch/bin/walk" -proxy -grain "$scratch/bin/grain" -src "$src" -dir "$scratch/pwalk" -out "$scratch/json-proxy" |
  tee "$scratch/proxy-walk.log"

echo
echo "walk:       $(grep -c '    OK ' "$scratch/walk.log") OK, $(grep -c '    FAIL' "$scratch/walk.log" || true) FAIL"
echo "walk -proxy: $(grep -c '    OK ' "$scratch/proxy-walk.log") OK, $(grep -c '    FAIL' "$scratch/proxy-walk.log" || true) FAIL"
