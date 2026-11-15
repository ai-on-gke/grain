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

// §5 item 4 in a real browser: Enter/Space selection on /next/agents (the
// real daemon's two live runs, cards and list) and ?pane= deep links on
// /next/agents and /next/workstreams/<key> (a `grain demo` store) through
// reload and back/forward, at 1440 and 390 px, light and dark. Prints one
// JSON line per check; "ok": false is a finding.
//
// Usage: node keyboard_probe.mjs <daemon-url> <daemon-code> <demo-url> <demo-code>
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../../..");
const { chromium } = await import(path.join(root, "ui-ng/node_modules/playwright/index.mjs"));
const { signIn } = await import(path.join(root, "ui-shared/e2e/accesscode.js"));
const [url, code, demoUrl, demoCode] = process.argv.slice(2);

const results = [];
const check = (where, name, ok, detail = {}) => {
  const r = { where, name, ok: !!ok, ...detail };
  results.push(r);
  console.log(JSON.stringify(r));
};
const rel = (page) => {
  const u = new URL(page.url());
  return u.pathname + u.search;
};
const q = (page, k) => new URL(page.url()).searchParams.get(k);

const browser = await chromium.launch();
const daemonCookies = (await signIn(url, code)).cookies;
const demoCookies = (await signIn(demoUrl, demoCode)).cookies;

async function open(cookies, viewport, theme, phone) {
  const ctx = await browser.newContext({ viewport, colorScheme: theme, isMobile: phone, hasTouch: phone });
  await ctx.addCookies(cookies);
  await ctx.addInitScript((t) => localStorage.setItem("grain.themeMode", t), theme);
  return ctx.newPage();
}

async function paneTab(page, scope) {
  return page
    .locator(`${scope} [data-testid^="task-tab-"][aria-current]`)
    .first()
    .getAttribute("data-testid")
    .catch(() => null);
}

for (const [size, viewport] of [["1440", { width: 1440, height: 900 }], ["390", { width: 390, height: 844 }]]) {
  const phone = size === "390";
  for (const theme of ["light", "dark"]) {
    const where = `agents@${size}/${theme}`;
    const page = await open(daemonCookies, viewport, theme, phone);
    await page.goto(`${url}/next/agents`);
    const cards = page.locator("article.agent-card");
    await cards.nth(1).waitFor({ timeout: 20000 });
    const ids = await cards.evaluateAll((els) => els.map((e) => e.dataset.testid.replace("agent-", "")));
    const [first, second] = ids;

    if (!phone) {
      check(where, "first card selected by default", (await cards.nth(0).getAttribute("aria-current")) === "true");
      // Enter on the second card selects it and stays on the page.
      await cards.nth(1).focus();
      await page.keyboard.press("Enter");
      await page.waitForTimeout(400);
      check(where, "Enter on a card selects it, page stays", q(page, "task") === second && rel(page).startsWith("/next/agents"), { url: rel(page) });
      // Space on the first card selects it back.
      await cards.nth(0).focus();
      await page.keyboard.press(" ");
      await page.waitForTimeout(400);
      check(where, "Space on a card selects it", q(page, "task") === first && rel(page).startsWith("/next/agents"), { url: rel(page) });
      // Enter on a focusable control inside the other card (its CPU
      // reading, tabindex 0) must not open the previously selected task.
      await cards.nth(1).locator('[data-testid="agent-cpu"]').focus();
      await page.keyboard.press("Enter");
      await page.waitForTimeout(600);
      const after = rel(page);
      check(where, "Enter on the CPU reading inside another card does not open the selected task", !after.startsWith(`/next/task/${first}`), { url: after });
      if (!after.startsWith("/next/agents")) await page.goBack();
      await page.waitForTimeout(400);
      // List layout rows.
      await page.locator('[data-testid="layout-list"]').click();
      const row2 = page.locator(`[data-testid="agent-row-${second}"]`);
      await row2.waitFor({ timeout: 10000 });
      await row2.focus();
      await page.keyboard.press("Enter");
      await page.waitForTimeout(400);
      check(where, "Enter on a list row selects it", q(page, "task") === second && rel(page).startsWith("/next/agents"), { url: rel(page) });
      await page.locator(`[data-testid="agent-row-${first}"]`).focus();
      await page.keyboard.press(" ");
      await page.waitForTimeout(400);
      check(where, "Space on a list row selects it", q(page, "task") === first, { url: rel(page) });
      await page.locator('[data-testid="layout-cards"]').click();
      // With nothing focused, Enter opens the selected task (by design).
      await page.locator("body").click({ position: { x: 5, y: 5 } }).catch(() => {});
      await page.evaluate(() => document.activeElement?.blur());
      await page.keyboard.press("Enter");
      await page.waitForTimeout(600);
      check(where, "Enter with nothing focused opens the selected task", rel(page).startsWith(`/next/task/${first}`), { url: rel(page) });

      // ?pane= deep link, reload, tab click, back/forward.
      await page.goto(`${url}/next/agents?task=${second}&pane=runs`);
      await page.locator('[data-testid="agents-pane"] [data-testid="task-tab-runs"]').waitFor({ timeout: 20000 });
      await page.waitForTimeout(800);
      check(where, "?pane=runs opens the Runs tab", (await paneTab(page, '[data-testid="agents-pane"]')) === "task-tab-runs");
      await page.reload();
      await page.locator('[data-testid="agents-pane"] [data-testid="task-tab-runs"]').waitFor({ timeout: 20000 });
      await page.waitForTimeout(800);
      check(where, "?pane=runs survives reload", (await paneTab(page, '[data-testid="agents-pane"]')) === "task-tab-runs" && q(page, "task") === second);
      await page.locator('[data-testid="agents-pane"] [data-testid="task-tab-details"]').click();
      await page.waitForTimeout(500);
      check(where, "clicking Details writes ?pane=details", q(page, "pane") === "details" && q(page, "task") === second && (await paneTab(page, '[data-testid="agents-pane"]')) === "task-tab-details", { url: rel(page) });
      await page.goBack();
      await page.waitForTimeout(700);
      check(where, "back returns to Runs", q(page, "pane") === "runs" && (await paneTab(page, '[data-testid="agents-pane"]')) === "task-tab-runs", { url: rel(page) });
      await page.goForward();
      await page.waitForTimeout(700);
      check(where, "forward returns to Details", q(page, "pane") === "details" && (await paneTab(page, '[data-testid="agents-pane"]')) === "task-tab-details", { url: rel(page) });
    } else {
      // Phone: no side pane, so a card opens its task full screen.
      await cards.nth(1).focus();
      await page.keyboard.press("Enter");
      await page.waitForTimeout(700);
      check(where, "Enter on a card opens that card's task", rel(page).startsWith(`/next/task/${second}`), { url: rel(page) });
      await page.goBack();
      await cards.nth(0).waitFor();
      await cards.nth(0).focus();
      await page.keyboard.press(" ");
      await page.waitForTimeout(700);
      check(where, "Space on a card opens that card's task", rel(page).startsWith(`/next/task/${first}`), { url: rel(page) });
      await page.goBack();
      await cards.nth(1).waitFor();
      await cards.nth(1).locator('[data-testid="agent-cpu"]').focus();
      await page.keyboard.press("Enter");
      await page.waitForTimeout(700);
      check(where, "Enter on the CPU reading opens nothing else", !rel(page).startsWith(`/next/task/${first}`), { url: rel(page) });
      // A phone deep link with ?pane= lands on the agents page.
      await page.goto(`${url}/next/agents?task=${second}&pane=runs`);
      await page.waitForTimeout(1500);
      check(where, "?task=&pane= deep link loads", rel(page).startsWith("/next/agents") || rel(page).startsWith(`/next/task/${second}`), { url: rel(page) });
    }
    await page.context().close();

    // Workstreams over the demo store.
    const wwhere = `workstreams@${size}/${theme}`;
    const w = await open(demoCookies, viewport, theme, phone);
    await w.goto(`${demoUrl}/next/workstreams`);
    const card = w.locator('[data-testid^="ws-row-"], [data-testid^="ws-card-"]').first();
    await card.waitFor({ timeout: 20000 });
    const key = (await card.getAttribute("data-testid")).replace(/^ws-(row|card)-/, "");
    await w.goto(`${demoUrl}/next/workstreams/${encodeURIComponent(key)}`);
    await w.locator('[data-testid^="ws-tree-node-"]').first().waitFor({ timeout: 20000 }).catch(() => {});
    const treeIds = await w.locator('[data-testid^="ws-tree-node-"]').evaluateAll((els) => els.map((e) => e.dataset.testid.replace("ws-tree-node-", "")));
    const taskIds = treeIds.filter((id) => /^[0-9]+$/.test(id));
    const nodeId = taskIds[0];
    const node = w.locator(`[data-testid="ws-tree-node-${nodeId}"]`);
    if (!nodeId) {
      check(wwhere, "workstream has tree nodes", false, { url: rel(w) });
      await w.context().close();
      continue;
    }
    const base = `${demoUrl}/next/workstreams/${encodeURIComponent(key)}?node=${nodeId}`;
    await w.goto(`${base}&pane=runs`);
    await w.waitForTimeout(1500);
    const scope = "g-workstream-detail";
    const tab0 = await paneTab(w, scope);
    check(wwhere, "?pane=runs opens the Runs tab", tab0 === "task-tab-runs", { tab: tab0, url: rel(w) });
    await w.reload();
    await w.waitForTimeout(1500);
    const tab1 = await paneTab(w, scope);
    check(wwhere, "?pane=runs survives reload", tab1 === "task-tab-runs" && q(w, "node") === nodeId, { tab: tab1, url: rel(w) });
    const det = w.locator(`${scope} [data-testid="task-tab-details"]`).first();
    if (phone && !(await det.isVisible().catch(() => false))) {
      // The phone inspector is a sheet; "o" opens it on the selected node.
      await w.evaluate(() => document.activeElement?.blur());
      await w.keyboard.press("o");
      await w.waitForTimeout(800);
      const tabO = await paneTab(w, scope);
      check(wwhere, "phone inspector opened with o keeps ?pane=runs", tabO === "task-tab-runs" && q(w, "pane") === "runs", { tab: tabO, url: rel(w) });
    }
    if (await det.isVisible().catch(() => false)) {
      await det.click();
      await w.waitForTimeout(600);
      check(wwhere, "clicking Details writes ?pane=details", q(w, "pane") === "details" && (await paneTab(w, scope)) === "task-tab-details", { url: rel(w) });
      await w.goBack();
      await w.waitForTimeout(800);
      check(wwhere, "back returns to Runs", q(w, "pane") === "runs" && (await paneTab(w, scope)) === "task-tab-runs", { url: rel(w) });
      await w.goForward();
      await w.waitForTimeout(800);
      check(wwhere, "forward returns to Details", q(w, "pane") === "details" && (await paneTab(w, scope)) === "task-tab-details", { url: rel(w) });
    } else {
      check(wwhere, "Details tab visible in the inspector", false, { url: rel(w), note: "inspector not shown at this width" });
    }
    // Enter on a tree node selects that node.
    await w.goto(`${demoUrl}/next/workstreams/${encodeURIComponent(key)}`);
    await node.waitFor({ timeout: 20000 });
    await node.click();
    await w.waitForTimeout(500);
    const thirdId = treeIds.find((id) => id !== nodeId);
    const third = w.locator(`[data-testid="ws-tree-node-${thirdId}"]`);
    if (thirdId && (await third.count())) {
      await third.focus();
      await w.keyboard.press("Enter");
      await w.waitForTimeout(600);
      check(wwhere, "Enter on a tree node selects it, not the previous one", q(w, "node") === thirdId || rel(w).includes(thirdId), { url: rel(w) });
    }
    await w.context().close();
  }
}
await browser.close();
const bad = results.filter((r) => !r.ok);
console.log(JSON.stringify({ checks: results.length, failed: bad.length }));
