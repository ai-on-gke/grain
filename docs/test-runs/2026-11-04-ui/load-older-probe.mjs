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

// Probe: what does "Load older closed tasks" do on a server whose live
// list keeps changing (consolerestserver's orchestrator running the seed)?
import { createRequire } from "node:module";
const require = createRequire(new URL("../../../ui/package.json", import.meta.url));
const { chromium } = require("@playwright/test");
const S = process.env.GRAIN_SERVER;
const T = process.env.GRAIN_TOKEN;
const browser = await chromium.launch();
const ctx = await browser.newContext({ baseURL: S, viewport: { width: 1440, height: 900 } });
await ctx.request.post("/api/auth/access-code", { data: { code: T } });
const page = await ctx.newPage();
const log = [];
const t0 = Date.now();
page.on("request", (r) => {
  if (r.url().includes("/api/tasks")) log.push(`${Date.now() - t0}ms REQ ${r.url().replace(S, "")}`);
});
page.on("console", (m) => log.push(`${Date.now() - t0}ms console.${m.type()} ${m.text().slice(0, 200)}`));
await page.goto("/?f=state:any:closed");
await page.locator(".task-row").first().waitFor();
await page.waitForTimeout(1500);
const more = page.getByRole("button", { name: /closed tasks/ });
log.push(`button count=${await more.count()} text=${await more.first().innerText().catch(() => "-")}`);
for (let i = 0; i < 3; i++) {
  log.push(`${Date.now() - t0}ms click ${i}`);
  await more.first().click({ timeout: 5000 }).catch((e) => log.push("click failed " + e.message.split("\n")[0]));
  await page.waitForTimeout(2500);
  log.push(`${Date.now() - t0}ms after click ${i}: buttons=${await more.count()} text=${await more.first().innerText().catch(() => "-")} rows=${await page.locator(".task-row").count()}`);
}
console.log(log.filter((l) => !l.includes("REQ /api/tasks/") || l.includes("state=closed")).join("\n"));
await browser.close();
