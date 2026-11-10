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

// Playwright task-pane inspector and button driver for §9 walkthrough.
// Run with NODE_PATH=/home/debian/work/ui/node_modules or via ui's node.

import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const uiDir = path.resolve(here, "../../../ui");
const requireFromUI = createRequire(path.join(uiDir, "package.json"));
const { chromium } = requireFromUI("@playwright/test");
const { signIn } = await import(path.join(uiDir, "e2e/accesscode.js"));

const [mode, baseUrl, code, taskId, buttonName] = process.argv.slice(2);
if (!mode || !baseUrl || !code || !taskId) {
  console.error("usage: ui_probe.mjs <inspect|click> <url> <code> <taskId> [buttonName]");
  process.exit(2);
}

const storageState = await signIn(baseUrl, code);
const browser = await chromium.launch();
try {
  const context = await browser.newContext({
    viewport: { width: 1440, height: 900 },
    storageState,
  });
  const page = await context.newPage();
  await page.goto(`${baseUrl}/tasks/${taskId}`, { waitUntil: "domcontentloaded" });
  await page
    .getByRole("button", { name: /^(Run now|Close|Reopen)$/ })
    .first()
    .waitFor({ state: "visible", timeout: 10000 });
  await page.waitForTimeout(250);

  if (mode === "click") {
    const btn = page.getByRole("button", { name: buttonName, exact: true }).first();
    await btn.waitFor({ state: "visible", timeout: 10000 });
    await Promise.all([
      page.waitForResponse((r) => r.url().includes(`/api/tasks/${taskId}/`) && r.request().method() === "POST", {
        timeout: 10000,
      }).catch(() => null),
      btn.click(),
    ]);
    await page.waitForTimeout(400);
  }

  const runNowLoc = page.getByRole("button", { name: "Run now", exact: true });
  const runNowCount = await runNowLoc.count();
  let runNowButton = { present: false, disabled: null };
  if (runNowCount > 0) {
    runNowButton = {
      present: true,
      disabled: await runNowLoc.first().isDisabled(),
    };
  }
  const hints = await page.locator(".detail-actions .hint, .hint.run-now-hint, .hint.framework-paused-hint").allTextContents();
  const runNowHintLoc = page.locator(".run-now-hint");
  const runNowHint = (await runNowHintLoc.count()) > 0 ? (await runNowHintLoc.first().textContent()).trim() : null;
  const fwPausedLoc = page.locator(".framework-paused-hint");
  const frameworkPausedHint = (await fwPausedLoc.count()) > 0 ? (await fwPausedLoc.first().textContent()).trim() : null;
  const allButtons = await page.evaluate(() =>
    Array.from(document.querySelectorAll("button"))
      .map((b) => ({ text: b.textContent.trim(), disabled: b.disabled }))
      .filter((b) => b.text && ["Run now", "Defer", "Undefer", "Pause", "Resume", "Retry", "Close", "Reopen", "Stop"].includes(b.text)),
  );

  console.log(
    JSON.stringify({
      mode,
      taskId,
      clicked: mode === "click" ? buttonName : undefined,
      runNowButton,
      runNowHint,
      frameworkPausedHint,
      hints: hints.map((h) => h.trim()),
      buttons: allButtons,
    }),
  );
} finally {
  await browser.close();
}
