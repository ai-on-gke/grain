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

// Playwright screenshot harness for docs/test-plans/2026-11-04.md §4 walkthrough.
// Captures 1440x900 and 390x844 in both light and dark modes, expands the
// Provenance disclosure on task pages so Filed by / Provenance / Comments are
// all visible, and reports DOM text + horizontal overflow checks.
import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(
  new URL("../../../ui/package.json", import.meta.url),
);
const { chromium } = require("playwright");

const [
  ,
  ,
  baseUrl = "http://127.0.0.1:14300",
  accessCode = "",
  outDir = ".",
  prefix = "before",
  routesCsv = "/tasks/3,/tasks/4,/tasks/6,/settings/agents",
] = process.argv;

fs.mkdirSync(outDir, { recursive: true });
const routes = routesCsv
  .split(",")
  .map((r) => r.trim())
  .filter(Boolean);

const viewports = [
  { name: "1440", width: 1440, height: 960 },
  { name: "390", width: 390, height: 900 },
];
const schemes = ["light", "dark"];

const browser = await chromium.launch({ executablePath: "/usr/bin/chromium" });
const summary = [];

try {
  for (const scheme of schemes) {
    for (const vp of viewports) {
      const ctx = await browser.newContext({
        viewport: { width: vp.width, height: vp.height },
        colorScheme: scheme,
        extraHTTPHeaders: accessCode
          ? { "X-Grain-Access-Code": accessCode }
          : {},
      });
      const page = await ctx.newPage();

      // Also set the access-code cookie via the login endpoint if active.
      if (accessCode) {
        await page.request
          .post(`${baseUrl}/api/auth/access-code`, {
            data: { code: accessCode },
          })
          .catch(() => {});
      }

      for (const route of routes) {
        const slug = route.replace(/^\//, "").replace(/\//g, "-");
        const filename = `${prefix}-${slug}-${vp.name}-${scheme}.png`;
        const fullPath = path.join(outDir, filename);

        await page.goto(`${baseUrl}${route}`, {
          waitUntil: "domcontentloaded",
        });
        await page.waitForTimeout(800);

        // Expand "Provenance" disclosure on task detail pages if collapsed.
        if (route.startsWith("/tasks/")) {
          const provBtn = page.getByRole("button", { name: /^Provenance/i });
          if ((await provBtn.count()) > 0) {
            const expanded = await provBtn
              .first()
              .getAttribute("aria-expanded");
            if (expanded === "false") {
              await provBtn.first().click();
              await page.waitForTimeout(200);
            }
          }
        }

        await page.screenshot({ path: fullPath, fullPage: true });

        const domCheck = await page.evaluate(() => {
          const doc = document.documentElement;
          const horizontalOverflowPx = Math.max(
            0,
            doc.scrollWidth - doc.clientWidth,
          );
          const filedByEl = document.querySelector('[data-testid="filed-by"]');
          const bodyText = document.body ? document.body.innerText : "";
          return {
            horizontalOverflowPx,
            filedByTestIdText: filedByEl ? filedByEl.textContent : null,
            hasAliceOwner: bodyText.includes("alice@example.com"),
            hasBobOwner: bodyText.includes("bob@example.com"),
            hasFiledFrom:
              bodyText.includes("filed from ") ||
              bodyText.includes("from claude-code") ||
              bodyText.includes("from codex-cli"),
          };
        });

        summary.push({
          file: filename,
          route,
          viewport: `${vp.width}x${vp.height}`,
          scheme,
          ...domCheck,
        });
      }

      await ctx.close();
    }
  }
} finally {
  await browser.close();
}

fs.writeFileSync(
  path.join(outDir, `${prefix}-ui-checks.json`),
  JSON.stringify(summary, null, 2) + "\n",
);
console.log(JSON.stringify(summary, null, 2));
