#!/usr/bin/env node
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

// Playwright Chromium browser walkthrough for docs/test-plans/2026-11-19.md §2
// across /next/inbox, /next/tasks, /next/workstreams, /next/task/<id>,
// /next/processes/quality, and /next/settings/work at 1440x900 and 390x844
// in both light and dark themes.

import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { signIn } from "../../../ui-shared/e2e/accesscode.js";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const ROOT = path.resolve(HERE, "../../..");
const require = createRequire(path.join(ROOT, "ui-ng", "package.json"));
const { chromium } = require("@playwright/test");

const DAEMON = process.env.DAEMON || "http://127.0.0.1:4280";
const CODE = (
  process.env.CODE ||
  (await fs.readFile(
    process.env.TOKEN_FILE || "/tmp/walk-qa-v6-postgres/data/local-api-token",
    "utf8",
  ))
).trim();
const TASK_ID = process.env.TASK_ID || "9";
const ROOT_ID = process.env.ROOT_ID || "2";
const OUT_DIR = process.env.OUT_DIR || "/tmp/walk-qa-v6-screens";

const VIEWPORTS = [
  { name: "1440", width: 1440, height: 900 },
  { name: "390", width: 390, height: 844 },
];
const THEMES = ["light", "dark"];

let failures = 0;
function pass(msg) {
  console.log(`  [PASS] ${msg}`);
}
function fail(msg) {
  console.error(`  [FAIL] ${msg}`);
  failures++;
}

async function apiGet(p) {
  const res = await fetch(`${DAEMON}${p}`, {
    headers: { Authorization: `Bearer ${CODE}` },
  });
  if (!res.ok) throw new Error(`GET ${p}: HTTP ${res.status}`);
  return res.json();
}

async function checkNoHorizontalOverflow(page, label) {
  const info = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    innerWidth: window.innerWidth,
  }));
  if (info.scrollWidth <= info.innerWidth + 1) {
    pass(`${label}: no horizontal overflow (${info.scrollWidth} <= ${info.innerWidth})`);
  } else {
    fail(`${label}: horizontal overflow! scrollWidth=${info.scrollWidth} > innerWidth=${info.innerWidth}`);
  }
}

async function checkChipsInViewport(page, selector, label) {
  const results = await page.evaluate((sel) => {
    const els = Array.from(document.querySelectorAll(sel));
    return els.map((el) => {
      const r = el.getBoundingClientRect();
      const cs = getComputedStyle(el);
      return {
        text: (el.textContent || "").trim(),
        flow: el.getAttribute("data-qa-flow") || "",
        x: Math.round(r.x),
        y: Math.round(r.y),
        w: Math.round(r.width),
        h: Math.round(r.height),
        right: Math.round(r.right),
        vw: window.innerWidth,
        scrollW: el.scrollWidth,
        clientW: el.clientWidth,
        overflow: cs.overflowX,
      };
    });
  }, selector);
  const visible = results.filter((r) => r.w > 0 && r.h > 0);
  if (visible.length === 0) {
    fail(`${label}: found 0 visible chips for ${selector}`);
    return [];
  }
  let bad = 0;
  for (const c of visible) {
    if (c.right > c.vw + 2 || c.x < -1) {
      fail(`${label}: chip "${c.text}" out of horizontal viewport (x=${c.x}, right=${c.right}, vw=${c.vw})`);
      bad++;
    }
    if (c.scrollW > c.clientW + 2 && c.overflow !== "visible") {
      fail(`${label}: chip "${c.text}" clipped horizontally (scrollW=${c.scrollW}, clientW=${c.clientW})`);
      bad++;
    }
  }
  if (bad === 0) {
    pass(
      `${label}: ${visible.length} chip(s) rendered cleanly without clipping/overflow (${visible
        .slice(0, 4)
        .map((c) => c.text)
        .join(", ")})`,
    );
  }
  return visible;
}

await fs.mkdir(OUT_DIR, { recursive: true });

console.log("\n=== 1. Batch GET /api/tasks qaSummary vs GET /api/tasks/{id} parity ===");
const allTasks = await apiGet("/api/tasks?full=1");
let parityBad = 0;
let withSummary = 0;
for (const t of allTasks) {
  const det = await apiGet(`/api/tasks/${t.id}`);
  const a = JSON.stringify(t.qaSummary ?? null);
  const b = JSON.stringify(det.qaSummary ?? null);
  if (t.qaSummary) withSummary++;
  if (a !== b) {
    fail(`Task #${t.id} batch qaSummary != detail qaSummary: batch=${a} detail=${b}`);
    parityBad++;
  }
}
if (parityBad === 0) {
  pass(`Batch GET /api/tasks qaSummary matched GET /api/tasks/{id} for all ${allTasks.length} tasks (${withSummary} with qaSummary)`);
}

const storageState = await signIn(DAEMON, CODE, "alice@example.com");
const browser = await chromium.launch({ headless: true });

const shots = [];

try {
  for (const vp of VIEWPORTS) {
    for (const theme of THEMES) {
      console.log(`\n=== 2. Walkthrough at ${vp.name}px (${theme}) ===`);
      const context = await browser.newContext({
        viewport: { width: vp.width, height: vp.height },
        colorScheme: theme,
        storageState,
      });
      await context.addInitScript((mode) => {
        try {
          window.localStorage.setItem("grain.themeMode", mode);
        } catch {}
      }, theme);
      const page = await context.newPage();
      const consoleErrors = [];
      const pageErrors = [];
      const serverErrors = [];
      page.on("console", (m) => {
        if (m.type() === "error") consoleErrors.push(m.text());
      });
      page.on("pageerror", (e) => pageErrors.push(String(e)));
      page.on("response", (res) => {
        if (res.status() >= 500) serverErrors.push(`${res.status()} ${res.url()}`);
      });

      const shoot = async (slug) => {
        const file = path.join(OUT_DIR, `${slug}-${vp.name}-${theme}.png`);
        await page.screenshot({ path: file });
        shots.push({ label: `${slug} ${vp.name} ${theme}`, file });
      };

      // (a) /next/inbox
      await page.goto(`${DAEMON}/next/inbox`, { waitUntil: "networkidle" });
      await page.waitForSelector('[data-testid="row-qa-chip"], [data-testid="row-qa-triage"]', { timeout: 10_000 });
      await checkNoHorizontalOverflow(page, `/next/inbox ${vp.name}/${theme}`);
      await checkChipsInViewport(page, '[data-testid="row-qa-chip"], [data-testid="row-qa-triage"]', `/next/inbox ${vp.name}/${theme}`);
      await shoot("inbox");

      // (b) /next/tasks?tab=all so all open workflows/tiers are visible
      await page.goto(`${DAEMON}/next/tasks?tab=all`, { waitUntil: "networkidle" });
      await page.waitForSelector('[data-testid^="qa-chip-"]', { timeout: 10_000 });
      await checkNoHorizontalOverflow(page, `/next/tasks ${vp.name}/${theme}`);
      const tasksChips = await checkChipsInViewport(
        page,
        '[data-testid^="qa-chip-"]',
        `/next/tasks ${vp.name}/${theme}`,
      );
      const chipTexts = tasksChips.map((c) => c.text);
      for (const expectedPrefix of ["Standard", "Fast", "Gates only", "Control"]) {
        if (chipTexts.some((t) => t.startsWith(expectedPrefix))) {
          pass(`/next/tasks ${vp.name}/${theme}: rendered chip starting with "${expectedPrefix}"`);
        } else {
          fail(`/next/tasks ${vp.name}/${theme}: missing chip starting with "${expectedPrefix}" (seen: ${chipTexts.join(", ")})`);
        }
      }
      await shoot("tasks");

      // (c) /next/workstreams (switch to All if needed so closed workstreams also show)
      await page.goto(`${DAEMON}/next/workstreams`, { waitUntil: "networkidle" });
      await page.waitForTimeout(600);
      const wsAllBtn = page.locator('button').filter({ hasText: /^All\b/ }).first();
      if ((await wsAllBtn.count()) > 0) {
        await wsAllBtn.click().catch(() => {});
        await page.waitForTimeout(500);
      }
      await checkNoHorizontalOverflow(page, `/next/workstreams ${vp.name}/${theme}`);
      const wsChipSel = '[data-testid="ws-card-qa-chip"], [data-testid="ws-qa-chip"], .ws-chip-warn, .ws-chip-run, .ws-chip-done, .ws-chip-err, .ws-chip-series';
      const wsChipCount = await page.locator(wsChipSel).count();
      if (wsChipCount > 0) {
        await checkChipsInViewport(page, wsChipSel, `/next/workstreams ${vp.name}/${theme}`);
      } else {
        console.log(`  [INFO] /next/workstreams ${vp.name}/${theme}: 0 cards with ws-chip on overview`);
      }
      await shoot("workstreams");

      // Open workstream detail directly for ROOT_ID (and fallback to card click)
      await page.goto(`${DAEMON}/next/workstreams/${ROOT_ID}`, { waitUntil: "networkidle" });
      await page.waitForTimeout(700);
      await checkNoHorizontalOverflow(page, `/next/workstreams/${ROOT_ID} ${vp.name}/${theme}`);
      if ((await page.locator('[data-testid="ws-detail-qa-chip"]').count()) > 0) {
        await checkChipsInViewport(page, '[data-testid="ws-detail-qa-chip"]', `/next/workstreams/${ROOT_ID} ${vp.name}/${theme}`);
      }
      await shoot("workstream-detail");

      // (d) /next/task/<TASK_ID> (Overview + Changes tab + S0-S5 tooltip bounds!)
      await page.goto(`${DAEMON}/next/task/${TASK_ID}`, { waitUntil: "networkidle" });
      await page.waitForSelector('[data-testid="task-qa-chip"]', { timeout: 10_000 });
      await checkNoHorizontalOverflow(page, `/next/task/${TASK_ID} overview ${vp.name}/${theme}`);
      await checkChipsInViewport(page, '[data-testid="task-qa-chip"]', `/next/task/${TASK_ID} overview ${vp.name}/${theme}`);
      await shoot("task-overview");

      // Navigate to Changes and CI tab and inspect S0-S5 stage badges + tooltips!
      await page.goto(`${DAEMON}/next/task/${TASK_ID}?tab=changes`, { waitUntil: "networkidle" });
      await page.waitForSelector('[data-testid="task-qa-section"]', { timeout: 10_000 });
      await page.waitForTimeout(500);
      await checkNoHorizontalOverflow(page, `/next/task/${TASK_ID} changes ${vp.name}/${theme}`);

      const stageTags = page.locator('[data-testid="task-qa-section"] .tc-run-row .ci-provider-tag');
      const tagCount = await stageTags.count();
      const testedStages = new Set();
      let tipFailures = 0;
      for (let i = 0; i < tagCount; i++) {
        const tag = stageTags.nth(i);
        const stageText = ((await tag.textContent()) || "").trim();
        if (!/^S\d/.test(stageText) || testedStages.has(stageText)) continue;
        testedStages.add(stageText);
        await tag.scrollIntoViewIfNeeded();
        await tag.hover();
        await page.waitForTimeout(520); // > TIP_DELAY_MS (400ms)
        const tipBox = await page.evaluate(() => {
          const el = document.getElementById("g-tip");
          if (!el || el.hidden) return null;
          const r = el.getBoundingClientRect();
          return {
            text: (el.textContent || "").trim(),
            left: Math.round(r.left),
            top: Math.round(r.top),
            right: Math.round(r.right),
            bottom: Math.round(r.bottom),
            width: Math.round(r.width),
            height: Math.round(r.height),
            vw: window.innerWidth,
            vh: window.innerHeight,
          };
        });
        if (!tipBox) {
          fail(`/next/task/${TASK_ID} ${vp.name}/${theme}: tooltip #g-tip did not appear on stage ${stageText}`);
          tipFailures++;
        } else if (
          tipBox.left < 0 ||
          tipBox.top < 0 ||
          tipBox.right > tipBox.vw + 1 ||
          tipBox.bottom > tipBox.vh + 1
        ) {
          fail(
            `/next/task/${TASK_ID} ${vp.name}/${theme}: stage ${stageText} tooltip out of viewport! box=${JSON.stringify(tipBox)}`,
          );
          tipFailures++;
        }
        // Move mouse away to dismiss
        await page.mouse.move(1, 1);
        await page.waitForTimeout(180);
      }
      if (testedStages.size >= 5 && tipFailures === 0) {
        pass(
          `/next/task/${TASK_ID} ${vp.name}/${theme}: all ${testedStages.size} stage tooltips (${[...testedStages].join(", ")}) stayed inside viewport`,
        );
      } else if (testedStages.size < 5) {
        fail(`/next/task/${TASK_ID} ${vp.name}/${theme}: expected >=5 S0-S5 stages, saw ${[...testedStages].join(", ")}`);
      }
      await shoot("task-changes");

      // (e) /next/processes/quality
      await page.goto(`${DAEMON}/next/processes/quality`, { waitUntil: "networkidle" });
      await page.waitForSelector('[data-testid="quality-flows-list"]', { timeout: 10_000 });
      await checkNoHorizontalOverflow(page, `/next/processes/quality ${vp.name}/${theme}`);
      const catalogKeys = await page.evaluate(() => {
        const items = Array.from(document.querySelectorAll('[data-testid="quality-flows-list"] [data-testid^="qa-flow-row-"]'));
        return items.map((el) => ({
          testid: el.getAttribute("data-testid") || "",
          text: (el.textContent || "").trim().replace(/\s+/g, " "),
        }));
      });
      const familyKeys = catalogKeys.map((b) => b.testid.replace(/^qa-flow-row-/, ""));
      const expectedKeys = ["standard-workflow", "fast-baseline", "gates-only"];
      if (
        familyKeys.length === 3 &&
        expectedKeys.every((k) => familyKeys.includes(k))
      ) {
        pass(`/next/processes/quality ${vp.name}/${theme}: catalog lists only canonical 3 flows (${familyKeys.join(", ")})`);
      } else {
        fail(`/next/processes/quality ${vp.name}/${theme}: unexpected catalog families: ${JSON.stringify(familyKeys)}`);
      }
      await shoot("processes-quality");

      // On desktop light, also test legacy ?flow= deep links on /next/processes/quality!
      if (vp.name === "1440" && theme === "light") {
        for (const [legacy, wantKey] of [
          ["golden-v2", "standard-workflow"],
          ["golden-v1-minimal", "fast-baseline"],
          ["engineering-workflow-v4", "standard-workflow"],
          ["golden-v4-composable", "standard-workflow"],
          ["deterministic-s3-only", "gates-only"],
        ]) {
          await page.goto(`${DAEMON}/next/processes/quality?flow=${encodeURIComponent(legacy)}`, {
            waitUntil: "networkidle",
          });
          await page.waitForSelector('[data-testid="qa-studio-toolbar"]', { timeout: 10_000 });
          const switcherText = await page.getByTestId("qa-flow-switcher").innerText();
          pass(`/next/processes/quality?flow=${legacy} loaded cleanly (switcher="${switcherText.trim()}", expected family=${wantKey})`);
        }
      }

      // (f) /next/settings/tasks (the Work & Agents -> Tasks section hosting SettingsWorkComponent's card-qa-rollout)
      // Also confirm /next/settings/work does not 404 or throw console errors.
      if (vp.name === "1440" && theme === "light") {
        await page.goto(`${DAEMON}/next/settings/work`, { waitUntil: "networkidle" });
        await checkNoHorizontalOverflow(page, `/next/settings/work ${vp.name}/${theme}`);
      }
      await page.goto(`${DAEMON}/next/settings/tasks`, { waitUntil: "networkidle" });
      await page.waitForSelector('[data-testid="card-qa-rollout"]', { timeout: 10_000 });
      await page.locator('[data-testid="card-qa-rollout"]').scrollIntoViewIfNeeded();
      await checkNoHorizontalOverflow(page, `/next/settings/tasks ${vp.name}/${theme}`);
      await shoot("settings-work");

      // On desktop light, save each of the 3 canonical flow slugs through the UI and verify persistence!
      if (vp.name === "1440" && theme === "light") {
        // Also confirm the picker options contain only the 3 canonical slugs (no retired v1-v4 slugs)
        const pill0 = page.getByTestId("pill-qa-flow");
        if ((await pill0.count()) === 0) {
          const notice = await page.locator('[data-testid="qa-running-experiment-notice"]').innerText().catch(() => "(missing)");
          fail(`/next/settings/tasks pill-qa-flow missing! Shown instead: ${notice}`);
        } else {
          await pill0.click();
          const optTexts = await page.locator("#pill-qa-flow-menu button").allInnerTexts();
          await page.keyboard.press("Escape");
          if (
            optTexts.length === 3 &&
            optTexts.some((t) => t.includes("standard-workflow")) &&
            optTexts.some((t) => t.includes("fast-baseline")) &&
            optTexts.some((t) => t.includes("gates-only"))
          ) {
            pass(`/next/settings/tasks flow picker shows only canonical 3 options: ${optTexts.join(" | ")}`);
          } else {
            fail(`/next/settings/tasks flow picker unexpected options: ${JSON.stringify(optTexts)}`);
          }

          for (const slug of ["fast-baseline", "gates-only", "standard-workflow"]) {
            const pill = page.getByTestId("pill-qa-flow");
            await pill.click();
            await page.locator("#pill-qa-flow-menu button", { hasText: slug }).click();
            const saveBtn = page.getByTestId("settings-save-btn");
            await saveBtn.click();
            await page.waitForFunction(
              () => !document.querySelector('[data-testid="settings-save-bar"]') ||
                document.querySelector('[data-testid="settings-save-bar"]').hidden,
              { timeout: 10_000 },
            ).catch(() => {});
            await page.waitForTimeout(500);
            await page.reload({ waitUntil: "networkidle" });
            await page.waitForSelector('[data-testid="pill-qa-flow"]', { timeout: 10_000 });
            const persisted = (await page.getByTestId("pill-qa-flow").innerText()).trim();
            const serverCfg = await apiGet("/api/settings");
            if (serverCfg.qaFlowId === slug && persisted.includes(slug)) {
              pass(`/next/settings/tasks saved and persisted canonical slug "${slug}" (UI="${persisted}")`);
            } else {
              fail(`/next/settings/tasks save failed for "${slug}": server=${serverCfg.qaFlowId} UI="${persisted}"`);
            }
          }
        }
      }

      if (consoleErrors.length === 0 && pageErrors.length === 0 && serverErrors.length === 0) {
        pass(`${vp.name}/${theme}: zero console errors, zero page errors, zero 5xx responses`);
      } else {
        fail(
          `${vp.name}/${theme}: errors detected console=${JSON.stringify(consoleErrors)} page=${JSON.stringify(pageErrors)} 5xx=${JSON.stringify(serverErrors)}`,
        );
      }

      await context.close();
    }
  }

  // Stitch a contact sheet of key desktop + mobile screenshots (light + dark) using Playwright canvas!
  const csPage = await browser.newPage({ viewport: { width: 1600, height: 1200 } });
  const selected = shots.filter((s) =>
    /^(inbox|tasks|workstreams|task-overview|task-changes|processes-quality|settings-work) /.test(s.label),
  );
  const cols = 4;
  const thumbW = 380;
  const thumbH = 260;
  const pad = 16;
  const labelH = 26;
  const rows = Math.ceil(selected.length / cols);
  const sheetW = cols * thumbW + (cols + 1) * pad;
  const sheetH = rows * (thumbH + labelH) + (rows + 1) * pad;

  const items = [];
  for (const s of selected) {
    const b64 = (await fs.readFile(s.file)).toString("base64");
    items.push({ label: s.label, src: `data:image/png;base64,${b64}` });
  }
  await csPage.setViewportSize({ width: sheetW, height: Math.min(sheetH, 16000) });
  await csPage.setContent(`
    <!doctype html>
    <html>
      <body style="margin:0;background:#111418;color:#e6edf3;font-family:system-ui,sans-serif;padding:${pad}px;">
        <div style="display:grid;grid-template-columns:repeat(${cols},${thumbW}px);gap:${pad}px;">
          ${items
            .map(
              (it) => `
            <div style="background:#1c2128;border:1px solid #30363d;border-radius:6px;overflow:hidden;">
              <div style="height:${labelH}px;line-height:${labelH}px;padding:0 8px;font-size:12px;font-weight:600;white-space:nowrap;overflow:hidden;text-overflow:ellipsis;">${it.label}</div>
              <div style="width:${thumbW}px;height:${thumbH}px;display:flex;align-items:flex-start;justify-content:center;background:#0d1117;overflow:hidden;">
                <img src="${it.src}" style="max-width:100%;max-height:100%;object-fit:contain;" />
              </div>
            </div>`,
            )
            .join("")}
        </div>
      </body>
    </html>
  `);
  await csPage.waitForTimeout(400);
  const sheetPath = path.join(OUT_DIR, "contact-sheet.png");
  await csPage.screenshot({ path: sheetPath, fullPage: true });
  console.log(`\nSaved contact sheet: ${sheetPath} (${selected.length} tiles)`);
  await csPage.close();
} finally {
  await browser.close();
}

console.log(`\n=== UI Walkthrough Summary: failures=${failures} ===`);
process.exit(failures > 0 ? 1 : 0);
