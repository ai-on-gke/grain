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

// Renders each option's marks and icon sheets to one PNG (marks and icons
// at 16/20/32/64/128px on the option's light and dark grounds), so the
// README does not depend on SVG rendering to show them.
import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const uiDir = path.resolve(here, "../../../../ui");
const require = createRequire(path.join(uiDir, "package.json"));
const { chromium } = require("@playwright/test");

const GROUNDS = {
  1: { light: "#ffffff", dark: "#15171b", ink: "#24221d", paper: "#ebe8e1" },
  2: { light: "#f3f3f1", dark: "#121417", ink: "#1b1c1e", paper: "#e7e8ea" },
  3: { light: "#1b1916", dark: "#0c0d10", ink: "#ece7dc", paper: "#ece7dc" },
};

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1200, height: 800 }, deviceScaleFactor: 2 });
for (const n of [1, 2, 3]) {
  const dir = path.join(here, "..", `option-${n}`);
  const read = async (f) => fs.readFile(path.join(dir, f), "utf8");
  const marks = { light: await read("mark-light.svg"), dark: await read("mark-dark.svg") };
  const nodal = JSON.parse(await read("nodal-icons.json"));
  const files = (await fs.readdir(dir)).filter((f) => f.endsWith(".svg"));
  const status = {};
  const nav = {};
  for (const f of files) {
    if (/^status-[a-z_]+\.svg$/.test(f)) status[f.slice(7, -4)] = await read(f);
    if (/^nav-[a-z]+\.svg$/.test(f)) nav[f.slice(4, -4)] = await read(f);
  }
  const g = GROUNDS[n];
  const uri = (s) => "data:image/svg+xml;utf8," + encodeURIComponent(s);
  const row = (label, svgs, color, size) =>
    `<div class="row"><span class="lab">${label}</span>${Object.entries(svgs)
      .map(([k, v]) => `<figure style="color:${color}"><span class="ic" style="width:${size}px;height:${size}px">${v}</span><figcaption>${k}</figcaption></figure>`)
      .join("")}</div>`;
  const marksRow = (theme) =>
    [16, 20, 32, 64, 128]
      .map((s) => `<figure><img src="${uri(marks[theme])}" width="${s}" height="${s}"><figcaption>${s}px</figcaption></figure>`)
      .join("");
  const panel = (theme) => {
    const bg = g[theme];
    const fg = n === 3 ? "#ece7dc" : theme === "light" ? g.ink : g.paper;
    return `<section style="background:${bg};color:${fg}">
      <h2>option ${n} — ${theme === "light" ? "light theme" : "dark theme"}${n === 3 ? " (rail is ink in both)" : ""}</h2>
      <div class="row"><span class="lab">mark</span>${marksRow(theme)}</div>
      ${row("nodal-line navigation icons (16px)", nodal, fg, 16)}
      ${row("nodal-line navigation icons (24px)", nodal, fg, 24)}
      ${row("generic line navigation icons (16px)", nav, fg, 16)}
      ${row("status icons (16px, in their state colour when implemented)", status, fg, 16)}
    </section>`;
  };
  const html = `<!doctype html><style>
    body{margin:0;font:12px Inter,system-ui,sans-serif}
    section{padding:18px 22px}
    h2{font-size:13px;font-weight:600;margin:0 0 10px;letter-spacing:.02em}
    .row{display:flex;align-items:flex-end;gap:14px;flex-wrap:wrap;margin:0 0 14px}
    .lab{width:150px;font-size:11px;opacity:.7;align-self:center}
    figure{margin:0;display:flex;flex-direction:column;align-items:center;gap:4px}
    figcaption{font-size:10px;opacity:.75}
    .ic svg{width:100%;height:100%;display:block}
    img{display:block}
  </style>${panel("light")}${panel("dark")}`;
  await page.setContent(html);
  await page.waitForTimeout(300);
  const out = path.join(dir, "icons-preview.png");
  await page.screenshot({ path: out, fullPage: true });
  console.log(path.relative(path.resolve(here, "../../../.."), out));
}
await browser.close();
