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

import { test as base, expect } from "@playwright/test";
import { demoFixtures } from "../../ui-shared/e2e/demo.js";
import { pageErrors } from "./pageerrors.js";

// One `grain demo` per Playwright worker, on a port of its own
// (grain/task-292). The harness itself -- starting the server, signing
// in, the baseURL and storageState it overrides -- lives in
// ui-shared/e2e/demo.js, which ui-ng/e2e/fixtures.ts uses too; its own
// comment says why there is one per worker and what that does not fix.
export const test = base.extend({
  ...pageErrors,
  ...demoFixtures,
});

export { expect };
