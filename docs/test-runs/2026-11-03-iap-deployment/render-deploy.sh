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

# Hand-renders deploy.sh's cfg -> setup.sh's resolve_* -> daemon flags for the
# two grain_config shapes the mocked terraform test produces (on/off). Uses the
# real functions out of the tree, no host changes. Run from the repo root.
set -euo pipefail
tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
fn() { awk -v n="$2" '$0 ~ "^"n"\\(\\) \\{"{p=1} p{print} p&&/^\}/{exit}' "$1"; }
{
  fn deploy/terraform/gcp/files/deploy.sh cfg
  fn scripts/setup.sh installed_unit_flag
  for f in normalize_allowed_principals resolve_allowed_principals normalize_iap_audience resolve_iap_audience; do
    fn scripts/setup.sh "$f"
  done
} >"$tmp/lib.sh"
render() {
  local json="$1"
  CONFIG_JSON="$json" bash -c '
    set -euo pipefail
    log() { echo "==> $*"; }
    source "$1"
    IAP_AUDIENCE="$(cfg iap_audience)"; ALLOWED_PRINCIPALS="$(cfg allowed_principals)"
    export GRAIN_IAP_AUDIENCE="$IAP_AUDIENCE" GRAIN_ALLOWED_PRINCIPALS="$ALLOWED_PRINCIPALS"
    GRAIN_IAP_AUDIENCE_GIVEN=1 GRAIN_ALLOWED_PRINCIPALS_GIVEN=1
    resolve_allowed_principals "$2/none.service"
    resolve_iap_audience "$2/none.service"
    args=()
    [ -n "$GRAIN_IAP_AUDIENCE" ] && args+=(-ui-iap-audience "$GRAIN_IAP_AUDIENCE")
    [ -n "$GRAIN_ALLOWED_PRINCIPALS" ] && args+=(-ui-allowed-principals "$GRAIN_ALLOWED_PRINCIPALS")
    echo "daemon flags: ${args[*]:-<none>}"
  ' bash "$tmp/lib.sh" "$tmp"
}
echo "### expose_ui_publicly = true"
render '{"iap_audience":"/projects/123456789012/global/backendServices/987654321098765","allowed_principals":"user:owner@example.com"}'
echo "### expose_ui_publicly = false"
render '{"iap_audience":"","allowed_principals":"user:owner@example.com"}'
