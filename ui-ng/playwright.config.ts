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

import { defineConfig } from "@playwright/test";

// ui-ng/e2e drives the Angular UI at /next/ in a real browser over a real
// `grain demo` -- one per worker, from the harness ui/e2e uses too
// (ui-shared/e2e/demo.js, wired up in e2e/fixtures.ts). The specs write
// paths under /next/ ("inbox", or "/next/inbox"); fixtures.ts points
// baseURL at the worker's server's /next/.
//
// Projects:
//   - desktop (1440x900) runs every spec.
//   - phone (390x844, touch) and dark (desktop, prefers-color-scheme: dark)
//     run only the tests tagged @phone or @dark: most specs pick their own
//     sizes and themes already (next-full-system.spec.js walks all four),
//     so running everything three times would mostly re-run the same pages.
//     Tag a test to opt it in.
//
// Chromium only, from the same @playwright/test version ui/ pins, so
// `make playwright-browsers` (run out of ui/) installs the one browser both
// suites drive.
const desktop = {
  browserName: "chromium" as const,
  viewport: { width: 1440, height: 900 },
};

export default defineConfig({
  testDir: "./e2e",
  // Serial within a file, files in parallel across workers, as in the
  // classic suite (ui/playwright.config.js): tests in one file share the
  // worker's store and may lean on what the test above them left.
  fullyParallel: false,
  retries: 0,
  // The github reporter writes each failure as a workflow annotation,
  // which is the part of a job anyone can read back afterwards.
  reporter: process.env["CI"] ? [["github"], ["list"]] : "list",
  // Builds the `grain` binary each worker's demo runs.
  globalSetup: "./e2e/global-setup.ts",
  globalTeardown: "./e2e/global-teardown.ts",
  use: {
    // No baseURL here: e2e/fixtures.ts sets it per worker.
    trace: "retain-on-failure",
  },
  projects: [
    { name: "desktop", use: desktop },
    {
      name: "phone",
      grep: /@phone/,
      use: {
        browserName: "chromium",
        viewport: { width: 390, height: 844 },
        hasTouch: true,
        isMobile: true,
      },
    },
    {
      name: "dark",
      grep: /@dark/,
      use: { ...desktop, colorScheme: "dark" },
    },
  ],
});
