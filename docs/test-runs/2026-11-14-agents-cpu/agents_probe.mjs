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

// §5 items 1 and 3 in a real browser: /next/agents over the real daemon,
// open in `tabs` pages of one browser for `secs` seconds (the polling the
// sweep counter in item3_overhead.sh measures), then each card's CPU text
// read off the page and compared with /api/sandboxes, and the page shot at
// 1440 and 390 px, light and dark, into /tmp/shots/.
//
// Usage: node agents_probe.mjs <url> <code> [tabs=3] [secs=60]
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(here, "../../..");
const { chromium } = await import(
  path.join(root, "ui-ng/node_modules/playwright/index.mjs")
);
const { signIn } = await import(path.join(root, "ui-shared/e2e/accesscode.js"));

const [url, code, tabsArg = "3", secsArg = "60"] = process.argv.slice(2);
const tabs = Number(tabsArg);
const secs = Number(secsArg);
const api = async (p) =>
  (await fetch(url + p, { headers: { Authorization: `Bearer ${code}` } })).json();
const { cookies } = await signIn(url, code);
const browser = await chromium.launch();
const out = { tabs: [], cards: [], shots: [] };

const poll = await browser.newContext({ viewport: { width: 1440, height: 900 } });
await poll.addCookies(cookies);
const requests = [];
const pages = [];
for (let i = 0; i < tabs; i++) {
  const p = await poll.newPage();
  p.on("request", (r) => {
    if (r.url().endsWith("/api/sandboxes")) requests.push({ tab: i, at: Date.now() });
  });
  await p.goto(`${url}/next/agents`);
  pages.push(p);
  await p.waitForTimeout(1500);
}
await pages[0].waitForTimeout(secs * 1000);
out.tabs = { count: tabs, secs, sandboxRequests: requests.length };

// Each card's CPU text against the API row for the same run.
const rows = (await api("/api/sandboxes")).sandboxes ?? [];
await pages[0].reload();
await pages[0].locator('[data-testid^="agent-"] [data-testid="agent-cpu"]').first().waitFor({ timeout: 20000 });
await pages[0].waitForTimeout(16000);
const rowsAfter = (await api("/api/sandboxes")).sandboxes ?? [];
out.cards = await pages[0].evaluate(() =>
  [...document.querySelectorAll("article.agent-card")].map((c) => ({
    id: c.getAttribute("data-testid"),
    title: c.querySelector(".agent-title")?.textContent.trim(),
    cpu: c.querySelector('[data-testid="agent-cpu"]')?.textContent.replace(/\s+/g, " ").trim(),
  })),
);
out.api = rowsAfter.map((r) => ({ sandbox: r.sandbox, cpuCores: r.cpuCores, backend: r.backend }));
out.apiBefore = rows.map((r) => ({ sandbox: r.sandbox, cpuCores: r.cpuCores }));
await poll.close();

fs.mkdirSync("/tmp/shots", { recursive: true });
for (const [name, viewport] of [["desktop", { width: 1440, height: 900 }], ["phone", { width: 390, height: 844 }]]) {
  for (const theme of ["light", "dark"]) {
    const ctx = await browser.newContext({ viewport, colorScheme: theme, isMobile: name === "phone" });
    await ctx.addCookies(cookies);
    await ctx.addInitScript((t) => localStorage.setItem("grain.themeMode", t), theme);
    const p = await ctx.newPage();
    await p.goto(`${url}/next/agents`);
    await p.locator('[data-testid="agent-cpu"]').first().waitFor({ timeout: 20000 });
    await p.waitForTimeout(2500);
    const file = `/tmp/shots/agents-${name}-${theme}.png`;
    await p.screenshot({ path: file, fullPage: false });
    const overflow = await p.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
    out.shots.push({ file, overflowPx: overflow });
    await ctx.close();
  }
}
await browser.close();
console.log(JSON.stringify(out, null, 2));
