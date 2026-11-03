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

// Items 4 and 7 in a browser (../2026-11-03-config-state.md):
//   - /repos/acme/widgets after a baseless suite schedule was declared: is
//     the refusal on the page?
//   - every Settings section: any control or text left about merge
//     batching or gathering, and how re-checks are labelled.
//
//   node pages.mjs <base-url> <bearer-token> <out-dir>
//
// Run from ui/ with PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright.
import { chromium } from "playwright";
import fs from "node:fs";

const [BASE, TOK, OUT] = process.argv.slice(2);
fs.mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch();
const ctx = await browser.newContext({
  extraHTTPHeaders: { Authorization: `Bearer ${TOK}` },
  viewport: { width: 1440, height: 1000 },
});
const page = await ctx.newPage();
const report = {};

await page.goto(`${BASE}/repos/acme/widgets`);
await page.waitForLoadState("networkidle");
// The declarations status lives on the Advanced tab.
await page.getByRole("tab", { name: "Advanced" }).click();
await page.waitForTimeout(1500);
report.repoUrl = page.url();
const repoText = await page.locator("body").innerText();
report.repoPage = {
  showsRefusal: repoText.includes("needs `base:`") || repoText.includes("needs base:"),
  showsFile: repoText.includes(".grain/schedules/sweep.yml"),
  lines: repoText.split("\n").filter((l) => /declar|sweep|base/i.test(l)).slice(0, 12),
};
await page.getByText("Last read failed").first().scrollIntoViewIfNeeded().catch(() => {});
await page.screenshot({ path: `${OUT}/repo-acme-widgets-advanced.png` });

const sections = ["engine", "general", "repositories", "config-repo", "models", "sandboxes"];
report.settings = {};
for (const sec of sections) {
  await page.goto(`${BASE}/settings/${sec}`);
  await page.waitForLoadState("networkidle");
  await page.waitForTimeout(800);
  const text = await page.locator("main").innerText().catch(() => page.locator("body").innerText());
  report.settings[sec] = {
    batching: text.split("\n").filter((l) => /batch|gather/i.test(l)),
    recheck: text.split("\n").filter((l) => /re-?check/i.test(l)),
  };
  if (sec === "engine") await page.screenshot({ path: `${OUT}/settings-engine.png`, fullPage: true });
}
fs.writeFileSync(`${OUT}/pages.json`, JSON.stringify(report, null, 2));
console.log(JSON.stringify(report, null, 2));
await browser.close();
