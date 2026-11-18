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

// Checks the task detail's gate observation panel on /next/tasks/<id>
// against GET /api/tasks/<id> on a running daemon, at 1440 px and 390 px,
// light and dark: the panel is present exactly when the API carries
// gate_observations, it says legacy keeps authority, it holds no
// actionable control, each article's heading names the type and status
// of the API result in the same position, nothing repeats
// inside one article, and nothing overflows the phone width.
//   UI_DIR=work/ui URL=http://127.0.0.1:4280 CODE=<access code> TASKS=2,16 node panel-check.mjs
import { createRequire } from "node:module";
import path from "node:path";

const uiDir = path.resolve(process.env.UI_DIR || "../../../ui");
const require = createRequire(path.join(uiDir, "package.json"));
const { chromium } = require("@playwright/test");
const { signIn } = await import(path.join(uiDir, "e2e/accesscode.js"));
const url = process.env.URL;
const code = process.env.CODE;
const tasks = (process.env.TASKS || "2").split(",");
const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
let failures = 0;
const fail = (msg) => {
  failures++;
  console.log(`FAIL ${msg}`);
};
for (const id of tasks) {
  const api = await (
    await fetch(`${url}/api/tasks/${id}`, { headers: { Authorization: `Bearer ${code}` } })
  ).json();
  const view = api.gate_observations;
  for (const [w, h] of [
    [1440, 900],
    [390, 844],
  ]) {
    for (const theme of ["light", "dark"]) {
      const ctx = await browser.newContext({ viewport: { width: w, height: h }, colorScheme: theme });
      await ctx.addCookies(cookies);
      await ctx.addInitScript((t) => localStorage.setItem("grain.themeMode", t), theme);
      const page = await ctx.newPage();
      await page.goto(`${url}/next/tasks/${id}`);
      await page.waitForLoadState("networkidle");
      const tag = `task ${id} ${w}px ${theme}`;
      const before = failures;
      const panel = page.locator('[data-testid="gate-observations"]');
      const present = (await panel.count()) > 0;
      if (present !== !!view) fail(`${tag}: panel present=${present}, API view=${!!view}`);
      if (!present) {
        if (failures === before) console.log(`ok ${tag}: no panel, API has none`);
        await ctx.close();
        continue;
      }
      await panel.scrollIntoViewIfNeeded();
      if (!(await panel.isVisible())) fail(`${tag}: panel is in the DOM but not visible`);
      const box = await panel.boundingBox();
      if (box && (box.x < 0 || box.x + box.width > w + 1)) fail(`${tag}: panel spans x=${box.x}..${box.x + box.width} outside ${w}px`);
      const text = await panel.innerText();
      if (!text.includes("Legacy services remain authoritative")) fail(`${tag}: no legacy-authority notice`);
      const controls = await panel.locator("button, input, select, textarea, a[href]").count();
      if (controls) fail(`${tag}: ${controls} actionable controls inside the panel`);
      const headings = await panel.locator("article > h4").allInnerTexts();
      if (headings.length !== view.results.length) fail(`${tag}: ${headings.length} articles for ${view.results.length} results`);
      const mismatches = [];
      view.results.forEach((entry, i) => {
        const r = entry.result;
        const hd = headings[i] || "";
        if (!hd.startsWith(`${r.type} v${r.version}`) || !hd.includes(`· ${r.status} ·`)) mismatches.push(`#${i} ${r.binding_id}: "${hd}" wants ${r.type}/${r.status}`);
      });
      if (mismatches.length) fail(`${tag}: headings disagree with API pairing: ${mismatches.slice(0, 3).join("; ")}`);
      const repeats = await panel.locator("article").evaluateAll((arts) =>
        arts.flatMap((a) => {
          const ps = [...a.querySelectorAll(":scope > p")].map((p) => p.textContent.trim()).filter(Boolean);
          return ps.filter((p, i) => ps.indexOf(p) !== i);
        }),
      );
      if (repeats.length) fail(`${tag}: repeated paragraph in one article: ${repeats[0].slice(0, 80)}`);
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
      if (overflow > 1) fail(`${tag}: page overflows the viewport by ${overflow}px`);
      if (failures === before) console.log(`ok ${tag}: ${headings.length} observations, statuses ${[...new Set(view.results.map((e) => e.result.status))].join("/")}`);
      await ctx.close();
    }
  }
}
await browser.close();
console.log(failures ? `${failures} failure(s)` : "all panel checks passed");
process.exitCode = failures ? 1 : 0;
