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

// Removes the temp directory global-setup.js built the demo binary
// into. Its own doc comment says why that directory is a fresh one per
// run; this is the other half of that, so a machine somebody runs the
// suite on all day does not collect a ~40MB binary per run in /tmp.
export default async function globalTeardown() {
  const dir = process.env.GRAIN_E2E_DEMO_BIN_DIR;
  if (dir) {
    await fs.rm(dir, { recursive: true, force: true });
  }
}
