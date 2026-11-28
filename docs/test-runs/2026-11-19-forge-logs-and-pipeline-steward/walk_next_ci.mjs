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

// Playwright walkthrough script for /next/ci GitHub Actions run-log zip fallback.
// Opens Run 42 (gha:acme:widgets:42) on a live grain daemon, loads and verifies
// all 3 job logs (build, test/unit, lint), checks single-download caching and
// step order, and captures desktop (1440px) + phone (390px) light/dark views.

import fs from "node:fs";
import path from "node:path";
import { createRequire } from "node:module";

const require = createRequire(import.meta.url);
const root = path.resolve(path.dirname(new URL(import.meta.url).pathname), "../../..");
let chromium;
for (const candidate of [
  path.join(root, "ui-ng/node_modules/playwright"),
  path.join(root, "ui-ng/node_modules/@playwright/test"),
  path.join(root, "ui/node_modules/playwright"),
  path.join(root, "ui/node_modules/@playwright/test"),
]) {
  if (fs.existsSync(candidate)) {
    ({ chromium } = require(candidate));
    break;
  }
}
if (!chromium) {
  throw new Error("playwright module not found under ui-ng/ or ui/");
}

const daemonUrl = process.env.DAEMON_URL || "http://127.0.0.1:4280";
const mockUrl = process.env.MOCK_URL || "http://127.0.0.1:4284";
const token = process.env.GRAIN_TOKEN || "";
const outDir = process.env.SHOT_DIR || "/tmp/walk-s8/screenshots";
fs.mkdirSync(outDir, { recursive: true });

async function getCounts() {
  const res = await fetch(`${mockUrl}/_mock/counts`);
  return res.json();
}

async function loginContext(browser, viewport, colorScheme) {
  const ctx = await browser.newContext({
    viewport,
    colorScheme,
    extraHTTPHeaders: token ? { Authorization: `Bearer ${token}` } : {},
  });
  const page = await ctx.newPage();
  const consoleErrors = [];
  const pageErrors = [];
  page.on("console", (msg) => {
    if (msg.type() === "error") consoleErrors.push(msg.text());
  });
  page.on("pageerror", (err) => pageErrors.push(String(err)));

  // Establish session cookie via /api/auth/access-code using browser fetch without Bearer header.
  if (token) {
    const loginRes = await fetch(`${daemonUrl}/api/auth/access-code`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code: token }),
    });
    const setCookie = loginRes.headers.get("set-cookie");
    if (setCookie) {
      const pair = setCookie.split(";")[0];
      const eq = pair.indexOf("=");
      if (eq > 0) {
        await ctx.addCookies([{
          name: pair.slice(0, eq),
          value: pair.slice(eq + 1),
          url: daemonUrl,
        }]);
      }
    }
  }
  return { ctx, page, consoleErrors, pageErrors };
}

async function main() {
  const browser = await chromium.launch({ headless: true });
  const version = browser.version();
  const report = {
    chromiumVersion: version,
    daemonUrl,
    checks: [],
    screenshots: [],
  };

  try {
    // 1. Desktop light: select acme/widgets + GitHub Actions, open Run 42, inspect all 3 jobs.
    const { ctx, page, consoleErrors, pageErrors } = await loginContext(
      browser,
      { width: 1440, height: 900 },
      "light",
    );

    await page.goto(`${daemonUrl}/next/ci`, { waitUntil: "domcontentloaded" });
    await page.waitForSelector('[data-testid="ci-headline"]', { timeout: 15000 });

    // Select repo acme/widgets
    await page.getByTestId("ci-repo-pill").click();
    await page.getByTestId("ci-repo-menu").getByRole("option", { name: "acme/widgets" }).click();

    // Switch provider to GitHub Actions
    await page.getByTestId("ci-provider-github").click();

    // Wait for Run 42 ("CI fallback") to appear in runs list and select it
    const run42Row = page.getByTestId("ci-runs-list").locator("button, li, [role='button']").filter({ hasText: "CI fallback" }).first();
    await run42Row.waitFor({ state: "visible", timeout: 15000 });
    await run42Row.click();

    // Wait for inspector jobs to appear
    await page.getByTestId("ci-job-701").waitFor({ state: "visible", timeout: 15000 });
    await page.getByTestId("ci-job-702").waitFor({ state: "visible", timeout: 15000 });
    await page.getByTestId("ci-job-703").waitFor({ state: "visible", timeout: 15000 });

    // Load test/unit (failed job 702: open by default -> click "Load GitHub job log")
    const job702 = page.getByTestId("ci-job-702");
    const fetchBtn702 = job702.getByTestId("ci-fetch-gha-log");
    await fetchBtn702.waitFor({ state: "visible", timeout: 10000 });
    await fetchBtn702.click();
    // Wait until the fetched GitHub job log renders (button flips to "Refresh GitHub job log"),
    // distinct from any already-visible recorded step-tail log viewer below it.
    await job702.getByRole("button", { name: "Refresh GitHub job log" }).waitFor({ state: "visible", timeout: 15000 });
    const viewer702 = job702.getByTestId("ci-log-viewer").first();
    await viewer702.waitFor({ state: "visible", timeout: 15000 });
    const text702 = await viewer702.innerText();

    // Expand build (job 701: passed -> expanding automatically fetches log)
    const job701 = page.getByTestId("ci-job-701");
    await job701.locator("button.ci-job-head").click();
    const viewer701 = job701.getByTestId("ci-log-viewer").first();
    await viewer701.waitFor({ state: "visible", timeout: 15000 });
    const text701 = await viewer701.innerText();

    // Expand lint (job 703: passed -> expanding automatically fetches log)
    const job703 = page.getByTestId("ci-job-703");
    await job703.locator("button.ci-job-head").click();
    const viewer703 = job703.getByTestId("ci-log-viewer").first();
    await viewer703.waitFor({ state: "visible", timeout: 15000 });
    const text703 = await viewer703.innerText();

    const setupIdx = text702.indexOf("setup env ok");
    const runIdx = text702.indexOf("FAIL TestWidget");
    const countsAfter = await getCounts();

    const overflowDesktop = await page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
    );

    const shot1 = path.join(outDir, "next-ci-run42-1440-light.png");
    await page.screenshot({ path: shot1, fullPage: true });
    report.screenshots.push(shot1);

    report.checks.push({
      name: "desktop-1440-light-run42-all-3-jobs",
      job701ContainsBuild: text701.includes("build step ok") && text701.includes("artifact built"),
      job702ContainsOrderedSteps: setupIdx >= 0 && runIdx > setupIdx,
      job703ContainsLint: text703.includes("lint clean") && text703.includes("0 issues"),
      run42ZipHits: countsAfter["/repos/acme/widgets/actions/runs/42/logs"] || 0,
      blob42ZipHits: countsAfter["/_blob/run/42.zip"] || 0,
      job701Hits: countsAfter["/repos/acme/widgets/actions/jobs/701/logs"] || 0,
      job702Hits: countsAfter["/repos/acme/widgets/actions/jobs/702/logs"] || 0,
      job703Hits: countsAfter["/repos/acme/widgets/actions/jobs/703/logs"] || 0,
      horizontalOverflow: overflowDesktop,
      consoleErrors,
      pageErrors,
      countsAfter,
    });

    // Also test in-UI error banner on Run 45 (dual failure)
    const run45Row = page.getByTestId("ci-runs-list").locator("button, li, [role='button']").filter({ hasText: "CI dual fail" }).first();
    await run45Row.click();
    const job951 = page.getByTestId("ci-job-951");
    await job951.waitFor({ state: "visible", timeout: 10000 });
    await job951.getByTestId("ci-fetch-gha-log").click();
    const actionErr = page.getByTestId("ci-action-error");
    await actionErr.waitFor({ state: "visible", timeout: 10000 });
    const errText45 = await actionErr.innerText();
    report.checks.push({
      name: "desktop-1440-light-run45-dual-failure-banner",
      errorText: errText45,
      mentionsBoth: errText45.includes("no bytes at all") && errText45.includes("run 45"),
      leaksSecret: errText45.includes("SECRET"),
    });

    await ctx.close();

    // 2. Desktop dark (1440px)
    const dark = await loginContext(browser, { width: 1440, height: 900 }, "dark");
    await dark.page.goto(`${daemonUrl}/next/ci`, { waitUntil: "domcontentloaded" });
    await dark.page.waitForSelector('[data-testid="ci-headline"]', { timeout: 15000 });
    await dark.page.getByTestId("ci-repo-pill").click();
    await dark.page.getByTestId("ci-repo-menu").getByRole("option", { name: "acme/widgets" }).click();
    await dark.page.getByTestId("ci-provider-github").click();
    await dark.page.getByTestId("ci-runs-list").locator("button, li, [role='button']").filter({ hasText: "CI fallback" }).first().click();
    await dark.page.getByTestId("ci-job-702").getByTestId("ci-fetch-gha-log").click();
    await dark.page.getByTestId("ci-job-702").getByRole("button", { name: "Refresh GitHub job log" }).waitFor({ state: "visible", timeout: 15000 });
    await dark.page.getByTestId("ci-job-702").getByTestId("ci-log-viewer").first().waitFor({ state: "visible", timeout: 15000 });
    const shot2 = path.join(outDir, "next-ci-run42-1440-dark.png");
    await dark.page.screenshot({ path: shot2, fullPage: true });
    report.screenshots.push(shot2);
    report.checks.push({
      name: "desktop-1440-dark-run42",
      consoleErrors: dark.consoleErrors,
      pageErrors: dark.pageErrors,
    });
    await dark.ctx.close();

    // 3. Phone light (390x844)
    const phone = await loginContext(browser, { width: 390, height: 844 }, "light");
    await phone.page.goto(`${daemonUrl}/next/ci`, { waitUntil: "domcontentloaded" });
    await phone.page.waitForSelector('[data-testid="ci-headline"]', { timeout: 15000 });
    await phone.page.getByTestId("ci-repo-pill").click();
    await phone.page.getByTestId("ci-repo-menu").getByRole("option", { name: "acme/widgets" }).click();
    await phone.page.getByTestId("ci-provider-github").click();
    await phone.page.getByTestId("ci-runs-list").locator("button, li, [role='button']").filter({ hasText: "CI fallback" }).first().click();
    await phone.page.getByTestId("ci-job-702").getByTestId("ci-fetch-gha-log").click();
    await phone.page.getByTestId("ci-job-702").getByRole("button", { name: "Refresh GitHub job log" }).waitFor({ state: "visible", timeout: 15000 });
    await phone.page.getByTestId("ci-job-702").getByTestId("ci-log-viewer").first().waitFor({ state: "visible", timeout: 15000 });
    const overflowPhone = await phone.page.evaluate(
      () => document.documentElement.scrollWidth > document.documentElement.clientWidth,
    );
    const shot3 = path.join(outDir, "next-ci-run42-390-light.png");
    await phone.page.screenshot({ path: shot3, fullPage: true });
    report.screenshots.push(shot3);
    report.checks.push({
      name: "phone-390-light-run42",
      horizontalOverflow: overflowPhone,
      consoleErrors: phone.consoleErrors,
      pageErrors: phone.pageErrors,
    });
    await phone.ctx.close();
  } finally {
    await browser.close();
  }

  console.log(JSON.stringify(report, null, 2));
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
