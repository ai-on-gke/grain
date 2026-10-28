#!/bin/bash
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
