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

import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { buildDemoBinary } from "../../ui-shared/e2e/demo.js";

// Builds the one `grain` binary every worker's demo runs, once, before any
// worker starts (ui-shared/e2e/demo.js). It embeds whatever
// pkg/ui/static-next holds, so build the frontend first: `make
// test-e2e-ng` depends on `frontend` for that reason.
export default async function globalSetup() {
  // A fresh directory per run, so two runs on one machine never rewrite
  // the binary the other's workers are executing.
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "grain-e2e-ng-"));
  buildDemoBinary(dir);
  process.env["GRAIN_E2E_DEMO_BIN_DIR"] = dir;
}
