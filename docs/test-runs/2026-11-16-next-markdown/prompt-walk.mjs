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

// §6 item 4: a task with a long Markdown prompt (headings, nested list,
// a fenced block with a 200-character line, a table, a diagram, a long
// unbroken URL) opened on /next/task/<id>, /next/tasks?task=<id>&pane=details,
// /next/agents?task=<id>&pane=details and the demo's seeded schedule and
// template on /next/processes, at 1440 px with the split at its widest
// list (the inspector at its 320 px floor), at 1440 px default, and at
// 390 px. For each it checks the Prompt pane sits above the Attributes /
// Configuration pane without overlap and that nothing runs out of either.
//
// GRAIN_URL=... GRAIN_CODE=... OUT=... node prompt-walk.mjs (from ui-ng/)

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");
const BASE = process.env.GRAIN_URL;
const CODE = process.env.GRAIN_CODE;
const OUT = process.env.OUT || "/tmp/md-shots";
fs.mkdirSync(OUT, { recursive: true });

const FENCE = "```";
const PROMPT = [
  "## Goal",
  "",
  "Move the **cache client** off `cache-a` without downtime.",
  "",
  "1. Drain writes",
  "   - confirm the rate is zero on https://dashboards.example.com/d/cache-a/writes?orgId=1&from=now-6h&to=now&var-tier=cache-a&refresh=30s",
  "2. Migrate",
  "",
  `${FENCE}sh`,
  `./scripts/migrate-cache.sh --from cache-a --to cache-b ${"--flag ".repeat(25)}`,
  FENCE,
  "",
  "| Step | Owner | Notes |",
  "|---|---|---|",
  "| drain | alice@example.com | waits for the dashboard to read zero writes for ten minutes |",
  "",
  `${FENCE}mermaid Plan`,
  "flowchart LR",
  "  A[drain] --> B[migrate] --> C[flip]",
  FENCE,
  "",
  ...Array.from(
    { length: 30 },
    (_, i) => `- constraint ${i + 1}: keep reads on`,
  ),
].join("\n");

const res = await fetch(`${BASE}/api/tasks`, {
  method: "POST",
  headers: {
    Authorization: `Bearer ${CODE}`,
    "Content-Type": "application/json",
  },
  body: JSON.stringify({ title: "Long Markdown prompt", description: PROMPT }),
});
const task = await res.json();
console.log("task", task.id);

const views = [
  {
    name: "task-page",
    route: `next/task/${task.id}`,
    prompt: "task-prompt-pane",
    attrs: "task-attributes-pane",
    tab: true,
  },
  {
    name: "tasks-inspector",
    route: `next/tasks?task=${task.id}&pane=details`,
    prompt: "task-prompt-pane",
    attrs: "task-attributes-pane",
    tab: true,
  },
  {
    name: "agents-inspector",
    route: `next/agents?task=${task.id}&pane=details`,
    prompt: "task-prompt-pane",
    attrs: "task-attributes-pane",
    tab: true,
  },
  {
    name: "schedule",
    route: "next/processes/schedules",
    prompt: "sched-prompt-pane",
    attrs: "sched-config-pane",
    open: true,
  },
  {
    name: "template",
    route: "next/processes/templates",
    prompt: "tmpl-prompt-pane",
    attrs: "tmpl-config-pane",
    open: true,
  },
];
const layouts = [
  { name: "1440-narrow", width: 1440, ratio: "0.95" },
  { name: "1440", width: 1440, ratio: null },
  { name: "390", width: 390, ratio: null },
];

const browser = await chromium.launch();
for (const lay of layouts) {
  for (const v of views) {
    const ctx = await browser.newContext({
      viewport: { width: lay.width, height: lay.width > 600 ? 900 : 844 },
    });
    await ctx.addInitScript((r) => {
      if (!r) return;
      for (const k of [
        "grain.tasks.splitRatio",
        "grain.agents.splitRatio",
        "grain-ng.processes.split-ratio",
      ])
        localStorage.setItem(k, r);
    }, lay.ratio);
    await ctx.setExtraHTTPHeaders({ Authorization: `Bearer ${CODE}` });
    const page = await ctx.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    await page.goto(`${BASE}/${v.route}`);
    await page.waitForTimeout(2500);
    if (v.open) {
      await page
        .locator('[data-testid^="open-"], [role="row"], .row-main')
        .first()
        .click({ timeout: 10_000 })
        .catch(() => {});
      await page.waitForTimeout(1500);
    }
    const tab = page.getByTestId("task-tab-details").filter({ visible: true });
    if (v.tab && (await tab.count()))
      await tab
        .first()
        .click()
        .catch(() => {});
    await page.waitForTimeout(1500);
    const prompt = page.getByTestId(v.prompt).filter({ visible: true }).first();
    const attrs = page.getByTestId(v.attrs).filter({ visible: true }).first();
    const m = await page
      .evaluate(
        ([pid, aid]) => {
          const vis = (id) =>
            Array.from(document.querySelectorAll(`[data-testid="${id}"]`)).find(
              (e) => e.getBoundingClientRect().width > 0,
            );
          const p = vis(pid);
          const a = vis(aid);
          if (!p || !a) return { missing: !p ? pid : aid };
          const pr = p.getBoundingClientRect();
          const ar = a.getBoundingClientRect();
          const escape = (pane) => {
            const r = pane.getBoundingClientRect();
            return Array.from(pane.querySelectorAll("*"))
              .filter((e) => {
                const b = e.getBoundingClientRect();
                if (!b.width) return false;
                // Inside a scroller of its own (a code card, a table, a
                // diagram canvas) running wide is how it is meant to read.
                let s = e.parentElement;
                while (s && s !== pane) {
                  const o = getComputedStyle(s).overflowX;
                  if (o === "auto" || o === "scroll" || o === "hidden")
                    return false;
                  s = s.parentElement;
                }
                return b.right > r.right + 1 || b.left < r.left - 1;
              })
              .map(
                (e) =>
                  `${e.tagName.toLowerCase()}.${(e.className || "").toString().split(" ")[0]}`,
              )
              .slice(0, 5);
          };
          return {
            promptW: Math.round(pr.width),
            overlap: ar.top < pr.bottom - 0.5,
            promptEscapes: escape(p),
            attrsEscapes: escape(a),
            dlCols: getComputedStyle(a.querySelector("dl") ?? a)
              .gridTemplateColumns,
            md: {
              h2: p.querySelectorAll("h2").length,
              li: p.querySelectorAll("li").length,
              pre: p.querySelectorAll("pre").length,
              table: p.querySelectorAll("table").length,
              svg: p.querySelectorAll(".mermaid-canvas svg").length,
            },
            pageOverflow:
              document.documentElement.scrollWidth - window.innerWidth,
          };
        },
        [v.prompt, v.attrs],
      )
      .catch((e) => ({ error: e.message }));
    await page.screenshot({ path: `${OUT}/prompt-${v.name}-${lay.name}.png` });
    void prompt;
    void attrs;
    console.log(
      `${v.name} ${lay.name}: ${JSON.stringify(m)} errors=${JSON.stringify(errors.slice(0, 3))}`,
    );
    await ctx.close();
  }
}
await browser.close();
