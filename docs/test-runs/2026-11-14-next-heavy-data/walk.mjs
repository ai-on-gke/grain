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

// docs/test-plans/2026-11-14.md §3 item 3: walk the /next/ pages over a
// real daemon seeded by seed/main.go, at 1440 and 390 px in light and
// dark, recording console errors and 4xx responses, and checking the
// heavy Workstreams layout rules on the real data.
//
//   BASE=http://127.0.0.1:4284 TOKEN_FILE=/tmp/heavy-ui/data/local-api-token \
//     OUT=/tmp/heavy-shots node docs/test-runs/2026-11-14-next-heavy-data/walk.mjs
//   BASE=http://127.0.0.1:4296 IAP_EMAIL=wv@example.com LABEL=repo-viewer ... walk.mjs
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { createRequire } from "node:module";

const require = createRequire(join(process.cwd(), "ui-ng", "package.json"));
const { chromium } = require("@playwright/test");
const base = process.env.BASE ?? "http://127.0.0.1:4284";
// IAP_EMAIL signs in through tests/iap/iapsim.go's proxy (BASE is then
// the proxy) as that person instead of presenting the access code.
const iapEmail = process.env.IAP_EMAIL ?? "";
const token = iapEmail ? "" : readFileSync(process.env.TOKEN_FILE, "utf8").trim();
const out = process.env.OUT ?? "/tmp/heavy-shots";
const label = process.env.LABEL ?? "admin";
mkdirSync(out, { recursive: true });

const pages = ["/next/workstreams", "WS_DETAIL", "/next/inbox", "/next/tasks", "/next/telemetry", "/next/settings", "/next/context"];
const results = [];
const browser = await chromium.launch();
let wsKey = process.env.WS_KEY ?? "";

for (const width of [1440, 390]) {
  for (const theme of ["light", "dark"]) {
    const ctx = await browser.newContext({
      viewport: { width, height: width > 500 ? 900 : 844 },
      colorScheme: theme,
      extraHTTPHeaders: token ? { Authorization: `Bearer ${token}` } : {},
    });
    if (iapEmail) await ctx.addCookies([{ name: "iapsim_email", value: iapEmail, url: base }]);
    await ctx.addInitScript((t) => localStorage.setItem("grain.themeMode", t), theme);
    const page = await ctx.newPage();
    const problems = [];
    page.on("console", (m) => m.type() === "error" && problems.push(`console: ${m.text().slice(0, 200)}`));
    page.on("pageerror", (e) => problems.push(`pageerror: ${e.message.slice(0, 200)}`));
    page.on("response", (r) => r.status() >= 400 && problems.push(`${r.status()} ${r.request().method()} ${new URL(r.url()).pathname}${new URL(r.url()).search}`));

    for (const p of pages) {
      const before = problems.length;
      const checks = {};
      let path = p;
      if (p === "WS_DETAIL") {
        if (!wsKey) continue;
        path = `/next/workstreams/${encodeURIComponent(wsKey)}`;
      }
      await page.goto(base + path, { waitUntil: "domcontentloaded" });
      await page.waitForLoadState("networkidle", { timeout: 15_000 }).catch(() => {});
      await page.waitForTimeout(1_500);
      checks.sideways = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);

      if (p === "/next/workstreams") {
        const row = page.locator("[data-testid^='ws-row-']", { hasText: "Harden the Workstreams" }).first();
        checks.rowVisible = await row.isVisible().catch(() => false);
        if (!wsKey && checks.rowVisible) {
          wsKey = ((await row.getAttribute("data-testid")) ?? "").replace(/^ws-row-/, "");
        }
        checks.rows = await page.evaluate(() => {
          const res = [];
          for (const r of document.querySelectorAll(".ws-row, .ws-card")) {
            const pc = r.querySelector(".ws-pcell")?.getBoundingClientRect();
            const pills = [...r.querySelectorAll(".ws-pills > *")].map((e) => e.getBoundingClientRect());
            let overlap = false;
            for (let i = 0; i < pills.length; i++) {
              for (let j = i + 1; j < pills.length; j++) {
                const a = pills[i], b = pills[j];
                if (a.left < b.right - 1 && b.left < a.right - 1 && a.top < b.bottom - 1 && b.top < a.bottom - 1) overlap = true;
              }
              if (pc && pc.width > 0 && pills[i].left < pc.right - 1 && pills[i].right > pc.left + 1 && pills[i].top < pc.bottom - 1 && pills[i].bottom > pc.top + 1) overlap = true;
            }
            const repoBits = [...r.querySelectorAll(".ws-repo, .repo-badge, [class*='repo-chip'], [class*='repo-more']")].map((e) => e.textContent.trim());
            res.push({ text: (r.textContent ?? "").replace(/\s+/g, " ").trim().slice(0, 60), overlap, repoBits });
          }
          return res;
        });
        checks.overlaps = checks.rows.filter((r) => r.overlap).length;
        checks.heavyRow = checks.rows.find((r) => r.text.includes("Harden"));
        delete checks.rows;
      }
      if (p === "WS_DETAIL") {
        checks.goalInHeader = await page.locator(".ws-dhead [data-testid='ws-goal']").count();
        checks.goalInInspector = await page.getByTestId("ws-inspector").getByTestId("ws-goal").count();
        const fold = page.getByTestId("ws-fold-done-toggle");
        checks.foldText = (await fold.textContent().catch(() => null))?.trim() ?? null;
        checks.nodesBefore = await page.locator("[data-testid^='ws-tree-node-']").count();
        if (checks.foldText) {
          await fold.click();
          await page.waitForTimeout(500);
          checks.nodesAfter = await page.locator("[data-testid^='ws-tree-node-']").count();
          checks.foldTextAfter = (await fold.textContent().catch(() => null))?.trim() ?? null;
        }
        await page.screenshot({ path: join(out, `${label}-ws-detail-expanded-${width}-${theme}.png`), fullPage: false });
        // Escape closes only the top layer: open the Director overlay
        // over the detail, then Escape once.
        const drawer = page.getByTestId("director-drawer");
        await page.getByTestId("director-trigger").click().catch(() => {});
        await page.waitForTimeout(500);
        checks.overlayOpen = await drawer.isVisible().catch(() => false);
        await page.keyboard.press("Escape");
        await page.waitForTimeout(500);
        checks.overlayAfterEsc = await drawer.isVisible().catch(() => false);
        checks.foldAfterEsc = (await fold.textContent().catch(() => null))?.trim() ?? null;
        checks.urlAfterEsc = new URL(page.url()).pathname;
        checks.inspectorAfterEsc = await page.getByTestId("ws-inspector").count();
      }
      const name = path.replace(/^\/next\/?/, "").replace(/[^a-z0-9]+/gi, "-") || "root";
      await page.screenshot({ path: join(out, `${label}-${name}-${width}-${theme}.png`), fullPage: false });
      results.push({ label, width, theme, path, checks, problems: problems.slice(before) });
      console.log(JSON.stringify(results.at(-1)));
    }
    await ctx.close();
  }
}
writeFileSync(join(out, `${label}-results.json`), JSON.stringify(results, null, 2));
await browser.close();
