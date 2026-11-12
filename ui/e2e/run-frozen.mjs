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

// The `ui e2e (quarantined)` lane: runs exactly the tests QUARANTINE and
// NONGATING take out of the gating job (frozen.js), and nothing else.
//
//   node e2e/run-frozen.mjs [playwright args…]     (from ui/)
//
// Each entry becomes a Playwright `file` or `file:line` filter, and
// GRAIN_E2E_LANE=quarantined turns playwright.config.js's grepInvert off
// so the filters are not removed again. With both lists empty there is
// nothing to run, and that has to be said and exit 0 rather than reach
// `playwright test` with no filter at all -- which is the whole suite.

import { spawnSync } from "node:child_process";
import { readFrozenEntries, LANE_ENV } from "./frozen.js";

const entries = readFrozenEntries();
if (entries.length === 0) {
  console.log(
    "ui/e2e/QUARANTINE and ui/e2e/NONGATING are empty: nothing to run.",
  );
  process.exit(0);
}

const filters = [
  ...new Set(
    entries.map(
      (e) => `e2e/${e.file}${e.line === undefined ? "" : `:${e.line}`}`,
    ),
  ),
];
console.log(
  `running ${filters.length} frozen entries:\n  ${filters.join("\n  ")}`,
);

const res = spawnSync(
  "npx",
  ["playwright", "test", ...filters, ...process.argv.slice(2)],
  { stdio: "inherit", env: { ...process.env, [LANE_ENV]: "quarantined" } },
);
process.exit(res.status ?? 1);
