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

// Probe for F2: what the closed list asks for, and draws, when a search
// is typed and then cleared at a given width. Usage:
//   GRAIN_SERVER=... GRAIN_TOKEN=... node clear-probe.mjs [width] [height]
import { createRequire } from "node:module";
const require = createRequire(new URL("../../../ui/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const width = Number(process.argv[2] || 390);
const height = Number(process.argv[3] || 844);
const browser = await chromium.launch();
const ctx = await browser.newContext({ baseURL: S, viewport: { width, height } });
await ctx.request.post("/api/auth/access-code", { data: { code: T } });
const page = await ctx.newPage();
const t0 = Date.now();
page.on("request", (r) => {
  const u = new URL(r.url());
  if (u.pathname === "/api/tasks" && u.searchParams.get("state") === "closed")
    console.log(Date.now() - t0, "GET", u.search.slice(0, 90));
});
const rows = () => page.locator(".task-row").count();
const where = () =>
  page.evaluate(() => {
    const d = document.scrollingElement;
    const more = document.querySelector(".closed-tasks-more");
    return { scrollTop: d.scrollTop, more: more ? Math.round(more.getBoundingClientRect().top) : null };
  });
await page.goto("/?f=state:any:closed");
await page.locator(".task-row").first().waitFor();
await page.waitForTimeout(1500);
console.log(Date.now() - t0, "loaded", await rows(), JSON.stringify(await where()));
const box = page.locator('input[type="search"], input[placeholder*="Search" i]').first();
for (const q of (process.env.QUERIES || "zephyrquill,CA-9012").split(",")) {
  await box.fill("");
  await page.waitForTimeout(500);
  console.log(Date.now() - t0, "emptied", await rows(), JSON.stringify(await where()));
  await box.pressSequentially(q, { delay: 40 });
  await page.waitForTimeout(1200);
  console.log(Date.now() - t0, `typed ${q}`, await rows(), JSON.stringify(await where()));
}
await box.fill("");
for (let i = 0; i < 6; i++) {
  await page.waitForTimeout(500);
  console.log(Date.now() - t0, "cleared", await rows(), JSON.stringify(await where()));
}
await browser.close();
