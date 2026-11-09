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

// Element screenshots of what the fixes touched, for before/after
// comparison at desktop and phone, light and dark: the "What this files"
// preview on sched-2 (F4) and the Add grant… menu on task 2 (F3).
//   GRAIN_SERVER=... GRAIN_TOKEN=... node preview-shots.mjs <before|after>
import { chromium, VIEWPORTS, THEMES, signedIn, out, tag } from "./lib.mjs";

const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const label = process.argv[2] || "run";
const browser = await chromium.launch();
for (const vp of VIEWPORTS) {
  for (const theme of THEMES) {
    const ctx = await signedIn(browser, S, T, vp, theme);
    const page = await ctx.newPage();
    await page.goto("/processes/schedules/sched-2");
    const dl = page.locator("dl").first();
    await dl.waitFor({ timeout: 20000 });
    await dl.scrollIntoViewIfNeeded();
    await page.waitForTimeout(400);
    await dl.locator("xpath=..").screenshot({ path: `${out}preview-${label}-${tag(vp, theme)}.png` });
    await page.goto("/tasks/2");
    await page.getByTestId("task-2-grants").getByRole("button", { name: "Add grant…" }).click();
    const menu = page.getByRole("menu", { name: "Grants to add" });
    await menu.waitFor();
    await page.waitForTimeout(400);
    await page.screenshot({ path: `${out}addgrant-${label}-${tag(vp, theme)}.png` });
    await ctx.close();
  }
}
await browser.close();
