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

// docs/test-plans/2026-11-11.md §12 item 4: Settings -> Repositories' Skills
// column and a repository's Agents tab skills editor, measured in Playwright
// at 1440x900 and 390x844, light and dark, against env.sh's daemon.
//
//   cd ui && node ../docs/test-runs/2026-11-11-repo-skills/ui_check.mjs \
//     http://127.0.0.1:4281 "$(cat /var/tmp/grain-skills/data/local-api-token)" out.json
//
// For each combination it records the Skills cell's text, the editor's
// checkbox, folder field value and helper text, every bounding box that
// matters, whether either page scrolls sideways, and the page's background
// colour (so "dark" is proved to be dark), and fails on anything outside
// the viewport.
import { createRequire } from "node:module";
import fs from "node:fs";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("playwright");

const [url, code, out] = process.argv.slice(2);
const VIEWPORTS = { desktop: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };

async function signIn() {
  const res = await fetch(new URL("/api/auth/access-code", url), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code, name: "owner@example.com" }),
    redirect: "manual",
  });
  const set = res.headers.getSetCookie();
  const u = new URL(url);
  return set.map((c) => {
    const [pair] = c.split(";");
    const i = pair.indexOf("=");
    return { name: pair.slice(0, i), value: pair.slice(i + 1), domain: u.hostname, path: "/" };
  });
}

function inside(box, vw) {
  return box && box.x >= -1 && box.x + box.width <= vw + 1;
}

const results = [];
let failed = 0;
const browser = await chromium.launch();
const cookies = await signIn();
for (const [width, viewport] of Object.entries(VIEWPORTS)) {
  for (const theme of ["light", "dark"]) {
    const ctx = await browser.newContext({ viewport, colorScheme: theme, isMobile: width === "phone" });
    await ctx.addCookies(cookies);
    await ctx.addInitScript((t) => localStorage.setItem("grain.themeMode", t), theme);
    const page = await ctx.newPage();
    const r = { width, theme, checks: [] };
    const check = (ok, what) => {
      r.checks.push({ ok: !!ok, what });
      if (!ok) failed++;
    };

    await page.goto(new URL("/settings/repositories", url).href);
    const cell = page.locator('[data-testid="settings-repo-skills"]').first();
    await cell.waitFor({ timeout: 15000 });
    await cell.scrollIntoViewIfNeeded();
    r.settingsCell = (await cell.innerText()).trim();
    r.settingsCellBox = await cell.boundingBox();
    r.settingsScrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    r.background = await page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    check(r.settingsCell === ".claude/skills, skills", `Settings -> Repositories Skills cell reads "${r.settingsCell}"`);
    check(r.settingsScrollWidth <= viewport.width, `Settings -> Repositories does not scroll sideways (${r.settingsScrollWidth}px)`);

    await page.goto(new URL("/repos/acme/widgets/settings/agents", url).href);
    const section = page.locator('[data-testid="repo-skills-section"]');
    await section.waitFor({ timeout: 15000 });
    const field = section.getByLabel("Skill folders for acme/widgets");
    await field.waitFor({ timeout: 15000 });
    await section.scrollIntoViewIfNeeded();
    const checkbox = section.getByRole("checkbox").first();
    r.enabled = await checkbox.isChecked();
    r.folders = await field.inputValue();
    r.sectionText = (await section.innerText()).replace(/\s+/g, " ").slice(0, 600);
    r.sectionBox = await section.boundingBox();
    r.fieldBox = await field.boundingBox();
    r.checkboxBox = await checkbox.boundingBox();
    r.agentsScrollWidth = await page.evaluate(() => document.documentElement.scrollWidth);
    check(r.enabled === true, "the editor's discovery checkbox is on");
    check(r.folders === ".claude/skills, skills", `the folder field holds "${r.folders}"`);
    check(inside(r.sectionBox, viewport.width), "the skills section sits inside the viewport");
    check(inside(r.fieldBox, viewport.width), "the folder field sits inside the viewport");
    check(r.agentsScrollWidth <= viewport.width, `the Agents tab does not scroll sideways (${r.agentsScrollWidth}px)`);
    check(/layer\/declared/.test(r.sectionText), "the editor shows what .grain/settings.yml declares");
    const shot = path.join(path.dirname(out), `agents-skills-${width}-${theme}.png`);
    await section.screenshot({ path: shot });
    results.push(r);
    for (const c of r.checks) console.log(`${c.ok ? "ok  " : "FAIL"} ${width}/${theme}: ${c.what}`);
    await ctx.close();
  }
}
await browser.close();
fs.writeFileSync(out, JSON.stringify(results, null, 2));
console.log(`${failed} failed check(s)`);
process.exit(failed ? 1 : 0);
