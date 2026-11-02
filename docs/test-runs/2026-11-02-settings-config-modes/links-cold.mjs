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

// Follows every in-app link on System → Health, Settings → Agents and
// Settings → Git cold -- each in a fresh browser context, as a new tab
// opened from a bookmark would -- at 1440 and 390 px, and checks that
// what the link names is on screen when the page settles
// (../2026-11-02-settings-config-modes.md, "What is NOT proved" 2).
//
//   node links-cold.mjs <mode> <base-url> <bearer-token> <out.json>
import { chromium } from "playwright";
import fs from "node:fs";

const [mode, BASE, TOK, OUT] = process.argv.slice(2);
const PAGES = ["/system/health", "/settings/agents", "/settings/git"];
const WIDTHS = [1440, 390];

const browser = await chromium.launch();
const newPage = async (width) => {
  const ctx = await browser.newContext({
    extraHTTPHeaders: { Authorization: `Bearer ${TOK}` },
    viewport: { width, height: width > 600 ? 900 : 844 },
  });
  return ctx.newPage();
};
const settle = async (page) => {
  await page.waitForLoadState("networkidle", { timeout: 5000 }).catch(() => {});
  await page.waitForTimeout(1200);
};

const rows = [];
for (const width of WIDTHS) {
  for (const from of PAGES) {
    const page = await newPage(width);
    await page.goto(BASE + from);
    await settle(page);
    const links = await page.evaluate(() => {
      const main = document.querySelector('[role="main"]') || document.body;
      return [...main.querySelectorAll("a[href]")]
        .filter((a) => {
          const r = a.getBoundingClientRect();
          return r.width > 0 && r.height > 0;
        })
        .map((a) => ({
          text: (a.innerText || "").replace(/\s+/g, " ").trim().slice(0, 60),
          href: a.getAttribute("href"),
        }));
    });
    await page.context().close();
    const seen = new Set();
    for (const link of links) {
      if (seen.has(link.href)) continue;
      seen.add(link.href);
      const row = { mode, width, from, ...link };
      if (!link.href.startsWith("/")) {
        row.result = "external (not followed)";
        rows.push(row);
        continue;
      }
      const p = await newPage(width);
      await p.goto(BASE + link.href);
      await settle(p);
      const url = new URL(p.url());
      row.landed = url.pathname + url.search + url.hash;
      // What the link names: its #anchor (a card id or a field's
      // data-setting-id), the file a /config?path= link opens, else the
      // page's main heading.
      const target = new URL(BASE + link.href);
      const hash = decodeURIComponent(target.hash.slice(1));
      const file = target.pathname === "/config" ? (target.searchParams.get("path") || "").split("/").pop() : "";
      row.check = await p.evaluate(([hash, file]) => {
        const vh = window.innerHeight;
        const vw = window.innerWidth;
        const onScreen = (el) => {
          if (!el) return false;
          const r = el.getBoundingClientRect();
          return r.height > 0 && r.top < vh && r.bottom > 0 && r.left < vw && r.right > 0;
        };
        if (hash) {
          const el =
            document.getElementById(hash) ||
            document.querySelector(`[data-setting-id="${CSS.escape(hash)}"]`);
          return { target: `#${hash}`, found: Boolean(el), visible: onScreen(el), top: el ? Math.round(el.getBoundingClientRect().top) : null };
        }
        if (file) {
          const els = [...document.querySelectorAll('[role="main"] *')].filter(
            (e) => !e.children.length && (e.innerText || "").includes(file),
          );
          return { target: file, found: els.length > 0, visible: els.some(onScreen) };
        }
        const notFound = /not found|no such|unknown (page|route)/i.test(document.body.innerText.slice(0, 2000));
        const h = [
          '[data-testid="settings-section"] h2',
          '[role="main"] h2',
          '[role="main"] h1',
          "h1",
        ]
          .map((sel) => document.querySelector(sel))
          .find(Boolean);
        return { target: h ? h.innerText.trim().slice(0, 60) : "(no heading)", found: Boolean(h), visible: onScreen(h), notFound };
      }, [hash, file]);
      row.result = row.check.found && row.check.visible && !row.check.notFound ? "ok" : "FAIL";
      rows.push(row);
      await p.context().close();
      console.log(`${row.result} ${mode} ${width} ${from} -> ${link.href} (${link.text}) => ${row.landed} ${JSON.stringify(row.check)}`);
    }
  }
}
await browser.close();
fs.writeFileSync(OUT, JSON.stringify(rows, null, 1) + "\n");
console.log("FAILS:", rows.filter((r) => r.result === "FAIL").length, "of", rows.length);
