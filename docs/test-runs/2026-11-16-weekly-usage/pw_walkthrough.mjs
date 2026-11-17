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

// Playwright driver for docs/test-plans/2026-11-16.md §5 Item 4:
// - Files tasks from /next/ in America/Los_Angeles and Asia/Kolkata browser contexts
//   and records the X-Grain-Time-Zone request header + stored task.timeZone.
// - Reads /next/telemetry, /next/settings/models, and classic /settings UsageCard
//   at 1440x900 and 390x844 in light and dark themes.
// - Checks DST-boundary formatting in Intl.DateTimeFormat.
// Run from ui-ng/ with PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright.

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(path.join(process.cwd(), "package.json"));
const { chromium } = require("@playwright/test");

const [baseURL, accessCode, outFile, shotsDir = ""] = process.argv.slice(2);
if (!baseURL || !accessCode || !outFile) {
  console.error("usage: node pw_walkthrough.mjs <url> <code> <out.json> [shotsDir]");
  process.exit(2);
}

const ZONES = ["America/Los_Angeles", "Asia/Kolkata"];
const SIZES = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "phone", width: 390, height: 844 },
];
const THEMES = ["light", "dark"];

const results = {
  baseURL,
  filed: [],
  views: [],
  classic: [],
  dstChecks: [],
};

const browser = await chromium.launch();
const capturedShots = [];

try {
  // 1. File a task from /next/ in each timezoneId context (and record request header + stored timeZone).
  for (const tz of ZONES) {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      timezoneId: tz,
      extraHTTPHeaders: { Authorization: `Bearer ${accessCode}` },
    });
    const page = await context.newPage();
    await page.addInitScript((c) => {
      localStorage.setItem("grain.accessCode", c);
      localStorage.removeItem("grain.timeZone");
    }, accessCode);
    let capturedHeader = null;
    page.on("request", (req) => {
      if (req.url().endsWith("/api/tasks") && req.method() === "POST") {
        capturedHeader = req.headers()["x-grain-time-zone"] ?? null;
      }
    });

    await page.goto(new URL("/next/tasks", baseURL).href, { waitUntil: "domcontentloaded" });
    await page.getByTestId("rail-new-task").waitFor({ state: "visible", timeout: 15000 });
    await page.getByTestId("rail-new-task").click();
    await page.getByTestId("new-task-sheet").waitFor({ state: "visible" });
    const title = `Playwright TZ check (${tz})`;
    await page.getByTestId("nt-title").fill(title);
    const respPromise = page.waitForResponse(
      (res) => res.url().endsWith("/api/tasks") && res.request().method() === "POST",
    );
    await page.getByTestId("new-task-submit").click();
    const resp = await respPromise;
    const status = resp.status();
    const body = await resp.json().catch(() => ({}));
    const taskId = body?.id || body?.task?.id || "";

    // Also inspect stored task.timeZone via GET /api/tasks/<id>
    let storedTimeZone = null;
    if (taskId) {
      const detail = await page.evaluate(
        async ({ id, code }) => {
          const r = await fetch(`/api/tasks/${id}`, {
            headers: { Authorization: `Bearer ${code}` },
          });
          return r.json();
        },
        { id: taskId, code: accessCode },
      );
      storedTimeZone = detail?.task?.timeZone ?? detail?.timeZone ?? "";
    }

    const browserTz = await page.evaluate(
      () => Intl.DateTimeFormat().resolvedOptions().timeZone,
    );

    results.filed.push({
      ui: "next",
      timezoneId: tz,
      browserResolvedTz: browserTz,
      requestHeader: capturedHeader,
      status,
      taskId,
      storedTimeZone,
    });
    await context.close();
  }

  // 2. Render /next/telemetry and /next/settings/models at 1440/390 light/dark in each timezoneId.
  for (const tz of ZONES) {
    for (const size of SIZES) {
      for (const theme of THEMES) {
        const context = await browser.newContext({
          viewport: { width: size.width, height: size.height },
          timezoneId: tz,
          extraHTTPHeaders: { Authorization: `Bearer ${accessCode}` },
        });
        const page = await context.newPage();
        await page.addInitScript(
          ({ t, c }) => {
            localStorage.setItem("grain.accessCode", c);
            localStorage.setItem("grain.themeMode", t);
            localStorage.removeItem("grain.timeZone");
          },
          { t: theme, c: accessCode },
        );

        // /next/telemetry
        await page.goto(new URL("/next/telemetry", baseURL).href, { waitUntil: "domcontentloaded" });
        await page.getByTestId("telemetry-usage").waitFor({ state: "visible", timeout: 15000 });
        await page.waitForTimeout(600);

        const telData = await page.evaluate(() => {
          const textOf = (sel) =>
            Array.from(document.querySelectorAll(sel)).map((el) =>
              (el.textContent || "").replace(/\s+/g, " ").trim(),
            );
          const sideways =
            document.documentElement.scrollWidth - document.documentElement.clientWidth;
          const resetEls = Array.from(
            document.querySelectorAll('[data-testid="usage-scope-reset"]'),
          ).map((el) => {
            const pill = el.parentElement?.querySelector(".status-pill");
            const rBox = el.getBoundingClientRect();
            const pBox = pill ? pill.getBoundingClientRect() : null;
            return {
              text: (el.textContent || "").replace(/\s+/g, " ").trim(),
              pillText: pill ? (pill.textContent || "").replace(/\s+/g, " ").trim() : null,
              sameLineAsPill: pBox ? rBox.y < pBox.y + pBox.height && rBox.x >= pBox.x + pBox.width - 1 : null,
            };
          });
          return {
            sideways,
            globalBanner: textOf('[data-testid="global-dispatch-banner"]'),
            weeklyLines: textOf(".tel-weekly-usage"),
            weeklySpent: textOf(".tel-weekly-spent"),
            resetPills: resetEls,
            usageTableText: textOf('[data-testid="usage-scopes-table"]'),
          };
        });

        if (shotsDir) {
          fs.mkdirSync(shotsDir, { recursive: true });
          const tzSlug = tz.toLowerCase().replace(/[^a-z0-9]+/g, "-");
          await page.getByTestId("telemetry-usage").scrollIntoViewIfNeeded();
          const shotPath = path.join(shotsDir, `telemetry-${tzSlug}-${size.name}-${theme}.png`);
          await page.screenshot({ path: shotPath });
          capturedShots.push({
            label: `telemetry · ${tz} · ${size.name} · ${theme}`,
            path: shotPath,
          });
        }

        // /next/settings/models
        await page.goto(new URL("/next/settings/models", baseURL).href, {
          waitUntil: "domcontentloaded",
        });
        await page.getByTestId("card-model-defaults").waitFor({ state: "visible", timeout: 15000 });
        await page.waitForTimeout(600);
        const modelsData = await page.evaluate(() => {
          const textOf = (sel) =>
            Array.from(document.querySelectorAll(sel)).map((el) =>
              (el.textContent || "").replace(/\s+/g, " ").trim(),
            );
          return {
            sideways:
              document.documentElement.scrollWidth - document.documentElement.clientWidth,
            globalBanner: textOf('[data-testid="global-dispatch-banner"]'),
            modelsQuotaBanner: textOf('[data-testid="models-quota-banner"]'),
          };
        });

        if (shotsDir) {
          const tzSlug = tz.toLowerCase().replace(/[^a-z0-9]+/g, "-");
          const shotPath = path.join(
            shotsDir,
            `settings-models-${tzSlug}-${size.name}-${theme}.png`,
          );
          await page.screenshot({ path: shotPath });
          capturedShots.push({
            label: `settings/models · ${tz} · ${size.name} · ${theme}`,
            path: shotPath,
          });
        }

        results.views.push({
          timezoneId: tz,
          size: size.name,
          theme,
          telemetry: telData,
          settingsModels: modelsData,
        });
        await context.close();
      }
    }
  }

  // 3. Spot-check classic UI UsageCard on /settings in America/Los_Angeles and Asia/Kolkata.
  for (const tz of ZONES) {
    const context = await browser.newContext({
      viewport: { width: 1440, height: 900 },
      timezoneId: tz,
      extraHTTPHeaders: { Authorization: `Bearer ${accessCode}` },
    });
    const page = await context.newPage();
    await page.addInitScript((c) => {
      localStorage.setItem("grain.accessCode", c);
      localStorage.removeItem("grain.timeZone");
    }, accessCode);
    await page.goto(new URL("/settings", baseURL).href, { waitUntil: "domcontentloaded" });
    await page.waitForTimeout(1200);
    // Navigate to Models section if tabbed, or find usage-card
    const modelsTab = page.getByRole("button", { name: /models/i }).first();
    if (await modelsTab.isVisible().catch(() => false)) {
      await modelsTab.click().catch(() => {});
      await page.waitForTimeout(800);
    }
    const classicData = await page.evaluate(() => {
      const card = document.querySelector('[data-testid="usage-card"]');
      const rows = Array.from(document.querySelectorAll('[data-testid="usage-row"]')).map((tr) =>
        Array.from(tr.querySelectorAll("td")).map((td) =>
          (td.textContent || "").replace(/\s+/g, " ").trim(),
        ),
      );
      const warnings = Array.from(document.querySelectorAll('[data-testid="usage-warning"]')).map(
        (el) => (el.textContent || "").replace(/\s+/g, " ").trim(),
      );
      const alerts = Array.from(document.querySelectorAll('[role="alert"]')).map((el) =>
        (el.textContent || "").replace(/\s+/g, " ").trim(),
      );
      return {
        hasUsageCard: !!card,
        rows,
        warnings,
        alerts,
      };
    });
    results.classic.push({ timezoneId: tz, ...classicData });
    await context.close();
  }

  // 4. DST-boundary formatting checks in Chromium's Intl engine for America/Los_Angeles & Asia/Kolkata.
  const dstContext = await browser.newContext({ timezoneId: "America/Los_Angeles" });
  const dstPage = await dstContext.newPage();
  results.dstChecks = await dstPage.evaluate(() => {
    const cases = [
      // US Spring forward 2026-03-08 02:00 PST -> 03:00 PDT (10:00 UTC)
      { label: "LA before spring-forward", tz: "America/Los_Angeles", iso: "2026-03-08T09:30:00Z" },
      { label: "LA after spring-forward", tz: "America/Los_Angeles", iso: "2026-03-08T10:30:00Z" },
      // US Fall back 2026-11-01 02:00 PDT -> 01:00 PST (09:00 UTC)
      { label: "LA before fall-back", tz: "America/Los_Angeles", iso: "2026-11-01T08:30:00Z" },
      { label: "LA after fall-back", tz: "America/Los_Angeles", iso: "2026-11-01T09:30:00Z" },
      // Half-hour offset zone (no DST)
      { label: "Kolkata midnight rollover", tz: "Asia/Kolkata", iso: "2026-11-17T19:00:00Z" },
    ];
    return cases.map((c) => {
      const fmt = new Intl.DateTimeFormat("en-US", {
        timeZone: c.tz,
        month: "short",
        day: "numeric",
        hour: "numeric",
        minute: "2-digit",
        timeZoneName: "short",
      });
      return { ...c, formatted: fmt.format(new Date(c.iso)) };
    });
  });
  await dstContext.close();

  if (shotsDir && capturedShots.length > 0) {
    const sheetContext = await browser.newContext({
      viewport: { width: 1600, height: 900 },
    });
    const sheetPage = await sheetContext.newPage();
    const tiles = capturedShots
      .map((s) => {
        const b64 = fs.readFileSync(s.path).toString("base64");
        return `<figure><figcaption>${s.label}</figcaption><img src="data:image/png;base64,${b64}"></figure>`;
      })
      .join("");
    await sheetPage.setContent(`<!doctype html>
<html><head><style>
  body { margin: 0; padding: 16px; background: #14161a; color: #e6e8ec; font: 12px/1.4 ui-monospace, SFMono-Regular, Menlo, monospace; }
  .grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 14px; }
  figure { margin: 0; background: #1e2128; border: 1px solid #303540; border-radius: 6px; padding: 8px; }
  figcaption { margin-bottom: 6px; color: #c7ccd6; }
  img { display: block; width: 100%; height: auto; border-radius: 3px; }
</style></head><body><div class="grid">${tiles}</div></body></html>`);
    await sheetPage.screenshot({
      path: path.join(shotsDir, "contact-sheet.png"),
      fullPage: true,
    });
    await sheetContext.close();
  }
} finally {
  await browser.close();
}

fs.writeFileSync(outFile, JSON.stringify(results, null, 2) + "\n", "utf-8");
console.log(`Wrote ${outFile}`);
