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

// Seeds the observability demo with CI runs: starts one through the CI
// page's own form, finishes it with the demo's /api/e2e/ci/finish pacing
// control, then starts a second that stays running. Throwaway data only.
import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const uiDir = path.resolve(here, "../../../../ui");
const require = createRequire(path.join(uiDir, "package.json"));
const { chromium } = require("@playwright/test");
const { signIn } = await import(path.join(uiDir, "e2e/accesscode.js"));

const url = process.argv[2] || "http://127.0.0.1:4290";
const code = (await fs.readFile("/home/debian/demo/code.txt", "utf8")).trim();
const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
await ctx.addCookies(cookies);
const page = await ctx.newPage();

async function startRun(branch) {
  await page.goto(url + "/ci");
  await page.getByRole("button", { name: "Start a run" }).click();
  const repoSelect = page.locator('select[name="repo"]');
  await repoSelect.waitFor();
  await repoSelect.selectOption("acme/widgets");
  await page.getByLabel("Branch").fill(branch);
  await page.getByRole("button", { name: "Load workflows" }).click();
  await page.getByLabel("Workflow").waitFor();
  await page.getByRole("button", { name: "Start run", exact: true }).click();
  await page.getByRole("button", { name: "Cancel run" }).waitFor({ timeout: 15000 });
}

const n = Number(process.argv[3] || 2);
for (let i = 0; i < n; i++) {
  await startRun(i === 0 ? "main" : `grain/task-${100 + i}`);
  if (i < n - 1) {
    await page.waitForTimeout(1500);
    const r = await page.request.post(url + "/api/e2e/ci/finish");
    console.log("finish:", r.status(), await r.text());
    await page.waitForTimeout(1500);
  }
}
await browser.close();
console.log("seeded", n, "runs");
