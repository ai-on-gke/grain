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

# Renders every option's mockups: tools/run-options.sh [screens]
set -e
cd "$(dirname "$0")"
export PLAYWRIGHT_BROWSERS_PATH=${PLAYWRIGHT_BROWSERS_PATH:-/var/cache/playwright}
SCREENS=${1:-tasks,board,task,new-task,ci,merge-queue,inbox,capabilities,metrics}
for n in 1 2 3; do
  node shoot.mjs --out "option-$n" --option "$n" --census \
    --css mui-remap.css --css "option-$n.css" --js mock-common.js \
    --mark-light "../option-$n/mark-light.svg" --mark-dark "../option-$n/mark-dark.svg" \
    --icons "../option-$n/nodal-icons.json" \
    --screens "$SCREENS"
done
