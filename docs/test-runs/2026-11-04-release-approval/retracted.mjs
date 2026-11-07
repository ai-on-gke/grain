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

// retracted.mjs URL CODE OUT_DIR: §2 item 5 on the live daemon
// start-daemon.sh started, running a release the registry has since
// marked <running>-retracted. On Settings -> Upgrade it reads the
// RetractedReleaseAlert, presses "Move back to latest (...)" then
// Upgrade -- the POST /api/upgrade that sends is captured and refused
// here, so the deployment is not really downgraded -- and then presses
// "Upgrade to <newer>" and Upgrade for real, waiting for that upgrade to
// start. Run from ui/ with PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright.
import fs from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import { signIn } from "../../../ui/e2e/accesscode.js";

const { chromium } = createRequire(path.join(process.cwd(), "package.json"))(
  "@playwright/test",
);
const [url, code, out] = process.argv.slice(2);
const browser = await chromium.launch();
const context = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  storageState: await signIn(url, code),
});
const page = await context.newPage();
const dialogs = [];
page.on("dialog", (d) => {
  dialogs.push(d.message());
  d.accept();
});
await page.goto(`${url}/settings/upgrade`);
const alert = page.getByRole("alert").filter({ hasText: "retracted" });
await alert.waitFor();
const report = { alert: (await alert.innerText()).replace(/\s+/g, " ").trim() };
await page.screenshot({ path: path.join(out, "upgrade-retracted-alert.png") });
const upgradeButton = page
  .getByTestId("upgrade-section")
  .getByRole("button", { name: "Upgrade", exact: true });

// 1. "Move back to latest": capture what Upgrade would start, start nothing.
let captured = null;
await page.route("**/api/upgrade", async (route) => {
  if (route.request().method() !== "POST") return route.continue();
  captured = route.request().postDataJSON();
  await route.fulfill({
    status: 409,
    contentType: "application/json",
    body: '{"error":"walkthrough: not downgrading"}',
  });
});
await alert.getByRole("button", { name: /^Move back to latest/ }).click();
report.latestButton = await alert
  .getByRole("button", { name: /^Move back to latest/ })
  .innerText();
await upgradeButton.click();
await page.waitForTimeout(1000);
report.latestUpgradeRequest = captured;
await page.unroute("**/api/upgrade");

// 2. "Upgrade to <newer>": for real.
const newer = alert.getByRole("button", { name: /^Upgrade to v/ });
report.newerButton = await newer.innerText();
await newer.click();
const [resp] = await Promise.all([
  page.waitForResponse(
    (r) => r.url().endsWith("/api/upgrade") && r.request().method() === "POST",
  ),
  upgradeButton.click(),
]);
report.newerUpgradeRequest = resp.request().postDataJSON();
report.newerUpgradeResponse = {
  status: resp.status(),
  phase: (await resp.json()).phase,
  branch: (await resp.json()).branch,
};
report.dialogs = dialogs;
await fs.writeFile(
  path.join(out, "retracted.json"),
  JSON.stringify(report, null, 2) + "\n",
);
console.log(JSON.stringify(report, null, 2));
await browser.close();
