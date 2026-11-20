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

// Before / after screenshots for the three fixes in
// docs/test-runs/2026-11-16-next-markdown.md, taken the same way against a
// `grain demo` built from the base and one built with the fixes: seeds a
// long access request (the demo's loopback pending-access hook), a long
// alert, a diagram whose %%{init}%% pins its palette, and a task for the
// Agents inspector under a stored 0.95 list share; shoots each at 1440 and
// 390 px, light and dark, and lays them out in OUT/contact-sheet.png.
//
// GRAIN_URL=... GRAIN_CODE=... OUT=... node shots.mjs (from ui-ng/)

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");
const BASE = process.env.GRAIN_URL;
const CODE = process.env.GRAIN_CODE;
const OUT = process.env.OUT;
const HERE = path.dirname(new URL(import.meta.url).pathname);
fs.mkdirSync(OUT, { recursive: true });

async function api(method, p, body) {
  const res = await fetch(BASE + p, {
    method,
    headers: {
      Authorization: `Bearer ${CODE}`,
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const txt = await res.text();
  if (!res.ok) throw new Error(`${method} ${p}: ${res.status} ${txt}`);
  return txt ? JSON.parse(txt) : null;
}

const acts = JSON.parse(
  fs.readFileSync(path.join(HERE, "daemon-actions.json")),
);
const stamp = Date.now();
const access = await api("POST", "/api/tasks", {
  title: `Cache client access request ${stamp}`,
  approved: true,
});
await api("POST", `/api/e2e/tasks/${access.id}/pending-access`, {
  summary: "Grant write to acme/gadgets so the cache client can move",
  reason: acts.reason,
  items: [{ kind: "write", repo: "acme/gadgets" }],
});
const alertTitle = `Cache tier degraded ${stamp}`;
await api("POST", "/api/notifications", {
  subject: `cache-tier-${stamp}`,
  title: alertTitle,
  severity: "warning",
  body: acts.reason.replace(
    "write access to `acme/gadgets`",
    "to page someone: the cache tier is degraded",
  ),
});
const pin = await api("POST", "/api/tasks", {
  title: `Pinned diagram ${stamp}`,
});
await api("POST", `/api/tasks/${pin.id}/comments`, {
  body: [
    "```mermaid Plain",
    "flowchart LR",
    "  A[drain] --> B[migrate]",
    "```",
    "",
    "```mermaid Pinned",
    "%%{init: {'themeVariables': {'primaryColor': '#ff0000', 'mainBkg': '#ff0000'}, 'themeCSS': 'text { font-size: 40px; }'}}%%",
    "flowchart LR",
    "  A[drain] --> B[migrate]",
    "```",
  ].join("\n"),
});
const agent = await api("POST", "/api/tasks", {
  title: `Agents split ${stamp}`,
  description:
    "## Goal\n\nMove the **cache client** off `cache-a`.\n\n1. Drain\n2. Migrate\n3. Flip",
});

const shots = [];
const browser = await chromium.launch();
async function shoot(name, width, theme, go) {
  const ctx = await browser.newContext({
    viewport: { width, height: width > 600 ? 900 : 844 },
    colorScheme: theme,
  });
  await ctx.addInitScript((t) => {
    localStorage.setItem("grain.themeMode", t);
    localStorage.setItem("grain.agents.splitRatio", "0.95");
  }, theme);
  await ctx.setExtraHTTPHeaders({ Authorization: `Bearer ${CODE}` });
  const page = await ctx.newPage();
  await go(page);
  const file = `${name}-${width}-${theme}.png`;
  await page.screenshot({ path: path.join(OUT, file) });
  shots.push({ file, label: `${name} · ${width}px · ${theme}` });
  await ctx.close();
}
const inboxPick = (title) => async (page) => {
  await page.goto(`${BASE}/next/inbox`);
  await page.getByText(title).first().click({ timeout: 15_000 });
  await page.waitForTimeout(2500);
};
for (const theme of ["light", "dark"]) {
  for (const width of [1440, 390]) {
    await shoot(
      "inbox-access",
      width,
      theme,
      inboxPick(access.title ?? `Cache client access request ${stamp}`),
    );
    await shoot("inbox-alert", width, theme, inboxPick(alertTitle));
    await shoot("task-diagram", width, theme, async (page) => {
      await page.goto(`${BASE}/next/task/${pin.id}`);
      await page.waitForTimeout(5000);
      await page
        .getByRole("region", { name: "Pinned" })
        .scrollIntoViewIfNeeded()
        .catch(() => {});
    });
  }
  await shoot("agents-inspector", 1440, theme, async (page) => {
    await page.goto(`${BASE}/next/agents?task=${agent.id}&pane=details`);
    await page.waitForTimeout(3500);
  });
}

// Contact sheet: every shot scaled into a labelled grid.
const cells = shots
  .map(
    (s) =>
      `<figure><img src="data:image/png;base64,${fs.readFileSync(path.join(OUT, s.file)).toString("base64")}"><figcaption>${s.label}</figcaption></figure>`,
  )
  .join("");
const sheet = await browser.newPage({ viewport: { width: 1800, height: 800 } });
await sheet.setContent(
  `<style>body{margin:8px;font:13px sans-serif;display:grid;grid-template-columns:repeat(4,1fr);gap:8px;align-items:start}figure{margin:0}img{width:100%;border:1px solid #888}figcaption{padding:2px 0}</style>${cells}`,
);
await sheet.screenshot({
  path: path.join(OUT, "contact-sheet.png"),
  fullPage: true,
});
await browser.close();
console.log(shots.length, "shots");
