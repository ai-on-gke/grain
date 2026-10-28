// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

import api from "./api.js";

// fetchUpgradeTargets reads GET /api/upgrade/targets: the standard
// branches and released versions the upgrade pane's branch field offers
// as a drop-down, each marked with whether this deployment may upgrade
// to it (pkg/upgrade/targets.go). A module of its own so the pane's tests
// can answer it apart from the status reads and posts they queue up for
// api() in order.
export function fetchUpgradeTargets() {
  return api("/api/upgrade/targets");
}
