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

// Item 5: the heading outline of /next/tasks, /next/workstreams and
// /next/agents with a task selected, its edit panel open and the Director
// shaper open in it, and of the standalone /next/task/<id>: exactly one
// h1, no level skipped, and axe's heading-order and page-has-heading-one
// rules clean. Run from the repository root; @axe-core/playwright comes
// from ui-ng/node_modules.
import { createRequire } from "node:module";
import path from "node:path";
import { chromium, session, watch } from "./lib.mjs";

const { default: AxeBuilder } = await import(createRequire(path.resolve("ui-ng/package.json")).resolve("@axe-core/playwright"));
const base = process.env.BASE, code = process.env.CODE, tid = process.env.TID || "3";
const browser = await chromium.launch();
const ctx = await session(browser, base, code, { width: Number(process.env.WIDTH || 1440) });
const page = await ctx.newPage();
const log = watch(page);
let fails = 0;

async function outline(label) {
  const hs = await page.evaluate(() =>
    [...document.querySelectorAll("h1,h2,h3,h4,h5,h6,[role=heading]")]
      .filter((h) => h.getClientRects().length > 0 && getComputedStyle(h).visibility !== "hidden" && !h.closest("[aria-hidden=true],[inert]"))
      .map((h) => ({ level: Number(h.getAttribute("aria-level") || (/^H\d$/.test(h.tagName) ? h.tagName[1] : 2)), text: h.textContent.replace(/\s+/g, " ").trim().slice(0, 48) })),
  );
  const h1s = hs.filter((h) => h.level === 1).length;
  const skips = [];
  for (let i = 1; i < hs.length; i++) if (hs[i].level > hs[i - 1].level + 1) skips.push(`${hs[i - 1].level}→${hs[i].level} at "${hs[i].text}"`);
  const axe = await new AxeBuilder({ page }).withRules(["heading-order", "page-has-heading-one", "empty-heading"]).analyze();
  const violations = axe.violations.map((v) => `${v.id}: ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`);
  const ok = h1s === 1 && skips.length === 0 && violations.length === 0;
  if (!ok) fails++;
  console.log(`${ok ? "PASS" : "FAIL"} ${label}: h1=${h1s} skips=${JSON.stringify(skips)} axe=${JSON.stringify(violations)}`);
  console.log(`    outline: ${hs.map((h) => `h${h.level} ${h.text}`).join(" | ").slice(0, 900)}`);
}

for (const [name, url] of [["tasks", `/next/tasks?task=${tid}`], ["workstreams", "/next/workstreams"], ["agents", `/next/agents?task=${tid}&pane=details`], ["task page", `/next/task/${tid}`]]) {
  await page.goto(`${base}${url}`);
  await page.waitForTimeout(2500);
  if (name === "workstreams") {
    // Open the first stream, then pick a task node in it.
    await page.locator("[data-testid^=ws-card-],[data-testid^=ws-row-]").first().click();
    await page.getByTestId("ws-detail").waitFor({ timeout: 10000 });
    await page.locator("[data-testid^=ws-tree-node-],[data-testid^=ws-gnode-]").first().click();
    await page.waitForTimeout(2000);
  }
  await outline(`${name} selected`);
  const edit = page.getByTestId("act-edit").first();
  if (!(await edit.isVisible().catch(() => false))) { console.log(`  (${name}: no Edit action visible)`); continue; }
  await edit.click();
  await page.getByTestId("task-edit-panel").waitFor({ timeout: 10000 });
  await outline(`${name} edit panel`);
  await page.getByTestId("edit-shape-director").click();
  await page.getByTestId("director-shaper-pane").waitFor({ timeout: 10000 });
  await page.waitForTimeout(500);
  await outline(`${name} Director shaper`);
}
console.log(`summary: ${fails} failing; errors=${JSON.stringify(log.pageErrors)}`);
await browser.close();
