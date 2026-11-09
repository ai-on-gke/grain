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

// settings-mode.mjs URL CODE MODE: §2 item 6's save-and-reload check for
// Settings -> General -> Updates. Picks MODE ("off", "release" or
// "daily") in the "Upgrade automatically" picker as a person would,
// waits for the PUT /api/settings it sends, reloads the page and prints
// what the picker and GET /api/settings say now. Run from ui/ with
// PLAYWRIGHT_BROWSERS_PATH=/var/cache/playwright; screenshot.mjs then
// shoots the saved state.
import { createRequire } from "node:module";
import path from "node:path";
import { signIn } from "../../../ui/e2e/accesscode.js";

const { chromium } = createRequire(path.join(process.cwd(), "package.json"))(
  "@playwright/test",
);
const LABELS = {
  off: "Off (manual only)",
  release: "On each release (recommended)",
  daily: "Once a day",
};
const [url, code, mode] = process.argv.slice(2);
const browser = await chromium.launch();
const context = await browser.newContext({
  viewport: { width: 1440, height: 900 },
  storageState: await signIn(url, code),
});
const page = await context.newPage();
const picker = () =>
  page.locator('[data-setting-id="autoUpgradeOn"] [role="combobox"]');
await page.goto(`${url}/settings/general#updates`);
await picker().waitFor();
const before = (await picker().innerText()).trim();
const writes = [];
page.on("request", (r) => {
  if (r.method() !== "GET" && r.url().includes("/api/"))
    writes.push(
      `${r.method()} ${new URL(r.url()).pathname} ${r.postData() || ""}`,
    );
});
if (before !== LABELS[mode]) {
  await picker().click();
  await page.getByRole("option", { name: LABELS[mode], exact: true }).click();
  for (let i = 0; i < 40; i++) {
    const now = await (await page.request.get(`${url}/api/settings`)).json();
    if (now.autoUpgradeOn === mode) break;
    await page.waitForTimeout(250);
  }
}
await page.reload();
await picker().waitFor();
const after = (await picker().innerText()).trim();
const settings = await (await page.request.get(`${url}/api/settings`)).json();
console.log(
  JSON.stringify(
    {
      mode,
      pickerBefore: before,
      writes,
      pickerAfterReload: after,
      autoUpgradeOn: settings.autoUpgradeOn,
      autoUpgradeChannel: settings.autoUpgradeChannel,
      ok: after === LABELS[mode] && settings.autoUpgradeOn === mode,
    },
    null,
    2,
  ),
);
await browser.close();
