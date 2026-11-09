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

// Layout audit for the 2026-11-07 §1 walkthrough. For every URL given, at
// 1440x900 and 390x844 in light and dark, it loads the page in Chromium,
// screenshots it, and reports page errors plus horizontal overflow: the
// document wider than the viewport, or visible elements whose box pokes past
// the right edge without an ancestor that scrolls horizontally.
//
//   cd ui-registry && PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright \
//     node ../docs/test-runs/2026-11-07-grain-registry/layout-audit.mjs \
//     /tmp/grwalk/shots/audit http://127.0.0.1:4256/ http://127.0.0.1:4256/directory ...
//
// Run it from ui-registry/ so @playwright/test resolves.

import { createRequire } from "node:module";
import fs from "node:fs/promises";
import path from "node:path";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");

const [out, ...urls] = process.argv.slice(2);
const VIEWPORTS = { desktop: { width: 1440, height: 900 }, phone: { width: 390, height: 844 } };
await fs.mkdir(out, { recursive: true });
const browser = await chromium.launch();
let problems = 0;
for (const url of urls) {
  for (const [vpName, vp] of Object.entries(VIEWPORTS)) {
    for (const scheme of ["light", "dark"]) {
      const ctx = await browser.newContext({ viewport: vp, colorScheme: scheme });
      await ctx.addInitScript((m) => {
        try {
          localStorage.setItem("grain.themeMode", m);
        } catch {}
      }, scheme);
      const page = await ctx.newPage();
      const errors = [];
      page.on("pageerror", (e) => errors.push(String(e)));
      const resp = await page.goto(url, { waitUntil: process.env.WAIT || "networkidle", timeout: 15000 }).catch((e) => ({ status: () => String(e) }));
      await page.waitForTimeout(Number(process.env.SETTLE_MS || 400));
      const audit = await page.evaluate(() => {
        const vw = window.innerWidth;
        const scrolls = (el) => {
          for (let p = el.parentElement; p; p = p.parentElement) {
            const ox = getComputedStyle(p).overflowX;
            if (ox === "auto" || ox === "scroll" || ox === "hidden" || ox === "clip") return true;
          }
          return false;
        };
        const offenders = [];
        for (const el of document.body.querySelectorAll("*")) {
          const r = el.getBoundingClientRect();
          if (r.width === 0 || r.height === 0) continue;
          if (r.right > vw + 1 && !scrolls(el)) {
            offenders.push(`${el.tagName.toLowerCase()}${el.className && typeof el.className === "string" ? "." + el.className.trim().split(/\s+/).join(".") : ""} right=${Math.round(r.right)}`);
          }
        }
        return { docWidth: document.documentElement.scrollWidth, vw, offenders: offenders.slice(0, 5), title: document.title };
      });
      const slug = url.replace(/^https?:\/\/[^/]+/, "").replace(/[^a-z0-9]+/gi, "_").replace(/^_|_$/g, "") || "root";
      const port = new URL(url).port;
      const file = path.join(out, `${port}-${slug}-${vpName}-${scheme}.png`);
      await page.screenshot({ path: file });
      const overflow = audit.docWidth > audit.vw + 1 || audit.offenders.length > 0;
      if (overflow || errors.length) problems++;
      console.log(
        `${overflow || errors.length ? "FLAG" : "ok  "} ${url} ${vpName} ${scheme} status=${resp?.status?.()} title=${JSON.stringify(audit.title)} doc=${audit.docWidth}/${audit.vw}` +
          (audit.offenders.length ? ` offenders=${audit.offenders.join(", ")}` : "") +
          (errors.length ? ` errors=${errors.join(" | ")}` : ""),
      );
      await ctx.close();
    }
  }
}
await browser.close();
console.log(`${problems} flagged page renders`);
