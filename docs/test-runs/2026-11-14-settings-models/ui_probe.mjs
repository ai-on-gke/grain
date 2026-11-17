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

// docs/test-plans/2026-11-14.md §4 item 2 (UI half): which Refresh
// controls each person sees on /next/settings/models and
// /next/settings/connections of the env.sh daemon, signed in through
// iapsim. Usage: node ui_probe.mjs <out-dir> [email...]

import fs from "node:fs";
import { createRequire } from "node:module";

const requireNg = createRequire(new URL("../../../ui-ng/package.json", import.meta.url));
const { chromium } = requireNg("@playwright/test");

const out = process.argv[2] || "/tmp/walk-models-ui";
const emails = process.argv.slice(3);
if (!emails.length) emails.push("alice@example.com", "admin2@example.com", "ops@example.com", "viewer@example.com");
const proxy = "http://127.0.0.1:4296";
fs.mkdirSync(out, { recursive: true });

const browser = await chromium.launch();
const report = {};
for (const email of emails) {
  const context = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  await context.addCookies([{ name: "iapsim_email", value: email, url: proxy }]);
  const page = await context.newPage();
  report[email] = {};
  for (const section of ["models", "connections"]) {
    await page.goto(`${proxy}/next/settings/${section}`, { waitUntil: "domcontentloaded" });
    await page.waitForSelector(".main-stage > *:not(router-outlet)", { timeout: 15000 });
    await page.waitForTimeout(1500);
    const buttons = page.getByRole("button", { name: /refresh/i });
    const seen = [];
    for (let i = 0; i < (await buttons.count()); i++) {
      const b = buttons.nth(i);
      if (!(await b.isVisible())) continue;
      seen.push({
        label: (await b.getAttribute("aria-label")) || (await b.innerText()).trim(),
        enabled: await b.isEnabled(),
      });
    }
    report[email][section] = seen;
    await page.screenshot({ path: `${out}/${email.split("@")[0]}-${section}.png`, fullPage: true });
  }
  await context.close();
}
await browser.close();
console.log(JSON.stringify(report, null, 1));
